// 加载消费者的真实事务接缝；Source 始终来自同一 Store 的实际查询。
// Real transaction seams: Source always comes from the same Store's lookup.
package postgresit

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

type luFixture struct {
	db, adm *sql.DB
	schema  string
	ctx     context.Context
	tx      *sql.Tx
	st      *refpg.Store
	source  repository.FileSnapshotSource
	desired repository.FileInfoSnapshot
	catalog repository.FileMimeCatalog
	winner  content.Metadata
}

func luSetup(t *testing.T) luFixture {
	t.Helper()
	db, schema := nodeFixture(t)
	adm, ctx := adminPool(t), nodeTestContext(t)
	cfReservations(t, db)
	svc, objectStore, root := cfBinaryService(t, db)
	const payload = "binflow loaded file consumer real transaction"
	winner, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{})
	if err != nil {
		t.Fatalf("publish binary: %v", err)
	}
	oldMD := ufOldSnapshot()
	id := ufCommitNodeWithSnapshot(t, db, 2801, "loadedR", "old", "a.bin", winner, &oldMD)
	tx, st := nodeTx(t, db)
	// 真实读取与后续更新共享 transactionalStore；不手工拼 Existing。
	// The real lookup and update share one transactional Store; no manual Existing.
	source, err := st.LookupSnapshotSource(ctx, fileRefOf(t, 0, "loadedR", "old", "a.bin", winner).Path)
	if err != nil || source.Reference.ID != id {
		t.Fatalf("lookup source = (%+v, %v)", source, err)
	}
	if !source.SHA1ActualValid || !source.MD5ActualValid || !source.SHA256ActualValid {
		t.Fatal("published Actual columns must all be non-NULL")
	}
	cat, err := repository.NewFileMimeCatalog([]repository.FileMimeDefinition{{Type: "application/octet-stream", Extensions: "bin"}}, repository.FileMimeCaseRootASCII)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	// 独立字面期望来自已提交夹具，不经加载/物化函数生成 Desired。
	// Independent literals describe the committed fixture, not a load/materialize oracle.
	desired, err := repository.NewFileInfoSnapshot(repository.FileInfoSnapshotInput{
		RepoKeyUTF16: mdUnits("loadedR"), RepoPathUTF16: mdUnits("old/a.bin"), NameUTF16: mdUnits("a.bin"),
		CreatedMillis: 1111111111111, LastModifiedMillis: 2222222222222, LastUpdatedMillis: 3333333333333,
		CreatedByUTF16: mdUnits("old-creator"), ModifiedByUTF16: nil, SizeBytes: winner.Size,
		MimeTypeUTF16: mdUnits("application/octet-stream"),
		Checksums: []repository.ChecksumEntryInput{
			{Kind: repository.ChecksumSHA1, Actual: mdUnits(winner.SHA1), Original: mdUnits("old-orig-1")},
			{Kind: repository.ChecksumMD5, Actual: mdUnits(winner.MD5), Original: nil},
			{Kind: repository.ChecksumSHA256, Actual: mdUnits(winner.SHA256), Original: mdUnits("NO_ORIG")},
		},
	})
	if err != nil {
		t.Fatalf("desired: %v", err)
	}
	t.Cleanup(func() {
		ufRequireBinaryFields(t, adm, schema, winner)
		cfRequireObjectBytes(t, objectStore, winner.SHA1, payload)
		if n := cfCountFiles(t, root); n != 1 {
			t.Errorf("object files = %d, want 1", n)
		}
		cfRequireBound(t, adm, schema, 0)
		cfRequireCurrent(t, schema, 0)
	})
	return luFixture{db: db, adm: adm, schema: schema, ctx: ctx, tx: tx, st: st, source: source, desired: desired, catalog: cat, winner: winner}
}

func luUpdate(t *testing.T, f luFixture, desired repository.FileInfoSnapshot, d *ufDelegates, changed bool, count int64) {
	t.Helper()
	got, err := repository.UpdateLoadedFileIfChanged(f.ctx, repository.LoadedFileUpdateInput{
		Source: f.source, Desired: desired, CreatedPolicy: repository.PreserveLoadedFileCreated,
	}, f.catalog, d.updateRef)
	if err != nil || got.Changed != changed || got.Count != count {
		t.Fatalf("loaded update = (%+v, %v), want Changed=%v Count=%d", got, err, changed, count)
	}
	if changed {
		d.wantUpdateOnce(t)
		if d.saved.ID != f.source.Reference.ID {
			t.Fatal("write ID did not come from the real source")
		}
	} else if d.update != 0 {
		t.Fatalf("no-op delegate calls = %d, want 0", d.update)
	}
}

func luOldRow(t *testing.T, f luFixture) {
	t.Helper()
	ufRequireOldNode(t, f.adm, f.schema, f.source.Reference.ID, "loadedR", "old", "a.bin", f.winner)
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("node count = %d, want 1", n)
	}
}

// 零调用在实际委托缝上计数；相同行值本身不能证明跳过。
// Count the real delegate seam: equal row values alone do not prove a skip.
func TestUpdateLoadedFileSameTransactionNoOp(t *testing.T) {
	f := luSetup(t)
	d := &ufDelegates{st: f.st}
	luUpdate(t, f, f.desired, d, false, 0)
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	luOldRow(t, f)
}

// MIME 仅影响原始比较；schema 无 MIME 列，真实等值重写不声称持久化 MIME。
// MIME affects raw comparison only: this schema has no persisted MIME column.
func TestUpdateLoadedFileMimeOnlyExecutesRealUpdate(t *testing.T) {
	f := luSetup(t)
	fields := f.desired.Fields()
	fields.MimeTypeUTF16 = nil
	desired, err := repository.NewFileInfoSnapshot(fields)
	if err != nil {
		t.Fatal(err)
	}
	d := &ufDelegates{st: f.st}
	luUpdate(t, f, desired, d, true, 1)
	equalSnapshot(t, d.saved.Metadata, ufOldSnapshot())
	if d.saved.Content != f.winner {
		t.Fatal("MIME-only write changed content operands")
	}
	luOldRow(t, f)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("caller commit: %v", err)
	}
	luOldRow(t, f)
}

func luChanged(t *testing.T, f luFixture) repository.FileInfoSnapshot {
	t.Helper()
	fields := f.desired.Fields()
	fields.CreatedMillis, fields.LastModifiedMillis, fields.LastUpdatedMillis = 4444444444444, 5555555555555, 6666666666666
	fields.ModifiedByUTF16 = mdUnits("loaded-writer")
	s, err := repository.NewFileInfoSnapshot(fields)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func luProvisional(t *testing.T, f luFixture) {
	t.Helper()
	var created, modified, updated int64
	var actor string
	if err := f.tx.QueryRowContext(f.ctx, fmt.Sprintf("SELECT created, modified, updated, modified_by FROM %s.nodes WHERE node_id=$1", f.schema), f.source.Reference.ID).Scan(&created, &modified, &updated, &actor); err != nil {
		t.Fatalf("same-tx metadata: %v", err)
	}
	if created != 4444444444444 || modified != 5555555555555 || updated != 6666666666666 || actor != "loaded-writer" {
		t.Fatalf("same-tx metadata = %d/%d/%d/%q", created, modified, updated, actor)
	}
}

// 提交前同事务看到新值，独立连接仍看到旧值；提交后独立核对七列。
// Same-tx visibility and independent old/new reads establish caller-owned commit.
func TestUpdateLoadedFileMetadataCommitVisibility(t *testing.T) {
	f := luSetup(t)
	d := &ufDelegates{st: f.st}
	luUpdate(t, f, luChanged(t, f), d, true, 1)
	luProvisional(t, f)
	luOldRow(t, f)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("caller commit: %v", err)
	}
	c, m, u, cb, mb, so, mo := metadataRowOf(t, f.adm, f.schema, f.source.Reference.ID)
	if !c.Valid || c.Int64 != 4444444444444 || !m.Valid || m.Int64 != 5555555555555 || !u.Valid || u.Int64 != 6666666666666 ||
		!cb.Valid || cb.String != "old-creator" || !mb.Valid || mb.String != "loaded-writer" || !so.Valid || so.String != "old-orig-1" || mo.Valid {
		t.Fatalf("independent committed metadata = %v/%v/%v/%v/%v/%v/%v", c, m, u, cb, mb, so, mo)
	}
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("commit created extra nodes: %d", n)
	}
}

// 尾部在真实读取后同事务删除该行，模拟 stale Source；更新 count=0 不转创建。
// Tail deletes a genuinely looked-up row in the same transaction: stale Source
// yields a real zero-row update, never an implicit create; rollback restores it.
func TestUpdateLoadedFileRollbackAndAbsentIDCount(t *testing.T) {
	f := luSetup(t)
	d := &ufDelegates{st: f.st}
	luUpdate(t, f, luChanged(t, f), d, true, 1)
	luProvisional(t, f)
	luOldRow(t, f)
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	luOldRow(t, f)

	t.Run("stale-source-real-zero-count", func(t *testing.T) {
		tx, st := nodeTx(t, f.db)
		source, err := st.LookupSnapshotSource(f.ctx, f.source.Reference.Path)
		if err != nil {
			t.Fatalf("tail real lookup: %v", err)
		}
		res, err := tx.ExecContext(f.ctx, fmt.Sprintf("DELETE FROM %s.nodes WHERE node_id=$1", f.schema), source.Reference.ID)
		if err != nil {
			t.Fatalf("tail delete: %v", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("tail deleted rows = %d, %v", n, err)
		}
		var count int
		if err := tx.QueryRowContext(f.ctx, fmt.Sprintf("SELECT count(*) FROM %s.nodes WHERE node_id=$1", f.schema), source.Reference.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("same-tx absent row = %d, %v", count, err)
		}
		tail := f
		tail.tx, tail.st, tail.source = tx, st, source
		d2 := &ufDelegates{st: st}
		luUpdate(t, tail, luChanged(t, tail), d2, true, 0)
		if err := tx.QueryRowContext(f.ctx, fmt.Sprintf("SELECT count(*) FROM %s.nodes WHERE node_id=$1", f.schema), source.Reference.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("zero update inserted a row: %d, %v", count, err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("tail rollback: %v", err)
		}
		luOldRow(t, f)
	})
}

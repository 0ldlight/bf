// 已发布 binary 赋值的真实消费者接缝；不手工构造 Desired、不模拟 Store。
// Real published-binary consumer seams: no manual Desired or substitute Store.
package postgresit

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
)

type lbFixture struct {
	luFixture
	next content.Metadata
}

func lbSetup(t *testing.T) lbFixture {
	t.Helper()
	db, schema := nodeFixture(t)
	adm, ctx := adminPool(t), nodeTestContext(t)
	cfReservations(t, db)
	svc, objects, root := cfBinaryService(t, db)
	const payloadA = "loaded binary assignment published A"
	const payloadB = "loaded binary assignment published B with different size"
	a, err := svc.Add(ctx, strings.NewReader(payloadA), filestore.Expected{})
	if err != nil {
		t.Fatalf("publish A: %v", err)
	}
	b, err := svc.Add(ctx, strings.NewReader(payloadB), filestore.Expected{})
	if err != nil {
		t.Fatalf("publish B: %v", err)
	}
	if a.SHA1 == b.SHA1 || a.MD5 == b.MD5 || a.SHA256 == b.SHA256 || a.Size == b.Size {
		t.Fatal("fixture needs distinct actuals and size")
	}
	oldMD := ufOldSnapshot()
	id := ufCommitNodeWithSnapshot(t, db, 2901, "loadedR", "old", "a.bin", a, &oldMD)
	tx, st := nodeTx(t, db)
	source, err := st.LookupSnapshotSource(ctx, fileRefOf(t, 0, "loadedR", "old", "a.bin", a).Path)
	if err != nil || source.Reference.ID != id {
		t.Fatalf("same-tx LookupSnapshotSource = (%+v, %v)", source, err)
	}
	cat, err := repository.NewFileMimeCatalog([]repository.FileMimeDefinition{{Type: "application/octet-stream", Extensions: "bin"}}, repository.FileMimeCaseRootASCII)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	// 调用方事务的清理先注册于 nodeTx；此处独立核对已发布资源，绝不补偿删除。
	// Independent published-resource checks: node rollback never compensates them.
	t.Cleanup(func() {
		for _, pair := range []struct {
			meta    content.Metadata
			payload string
		}{{a, payloadA}, {b, payloadB}} {
			ufRequireBinaryFields(t, adm, schema, pair.meta)
			cfRequireObjectBytes(t, objects, pair.meta.SHA1, pair.payload)
		}
		if n := cfCountFiles(t, root); n != 2 {
			t.Errorf("published object count = %d, want 2", n)
		}
		cfRequireBound(t, adm, schema, 0)
		cfRequireCurrent(t, schema, 0)
	})
	return lbFixture{luFixture: luFixture{db: db, adm: adm, schema: schema, ctx: ctx, tx: tx, st: st, source: source, catalog: cat, winner: a}, next: b}
}

func lbUpdate(t *testing.T, f lbFixture, published content.Metadata, changed bool) {
	t.Helper()
	d := &ufDelegates{st: f.st}
	got, err := repository.UpdateLoadedFileBinary(f.ctx, repository.LoadedFileBinaryInput{
		Source: f.source, Published: published, CreatedPolicy: repository.PreserveLoadedFileCreated,
	}, f.catalog, d.updateRef)
	wantCount := int64(0)
	if changed {
		wantCount = 1
	}
	if err != nil || got.Changed != changed || got.Count != wantCount {
		t.Fatalf("binary consumer = (%+v, %v), want Changed=%v Count=%d", got, err, changed, wantCount)
	}
	if !changed {
		if d.update != 0 {
			t.Fatalf("same-binary callback count = %d, want 0", d.update)
		}
		return
	}
	d.wantUpdateOnce(t)
	if d.saved.ID != f.source.Reference.ID || d.saved.Path != f.source.Reference.Path || d.saved.Content != published {
		t.Fatalf("callback identity/content = %+v, want source identity and published winner", d.saved)
	}
	equalSnapshot(t, d.saved.Metadata, ufOldSnapshot())
}

func lbRequireProvisional(t *testing.T, f lbFixture) {
	t.Helper()
	var sha1, sha256, md5, repo, path, name string
	var size int64
	var kind, depth int16
	if err := f.tx.QueryRowContext(f.ctx, fmt.Sprintf("SELECT node_type, repo, node_path, node_name, depth, sha1_actual, sha256, md5_actual, bin_length FROM %s.nodes WHERE node_id=$1", f.schema), f.source.Reference.ID).Scan(&kind, &repo, &path, &name, &depth, &sha1, &sha256, &md5, &size); err != nil {
		t.Fatalf("same-tx row: %v", err)
	}
	if kind != 1 || repo != "loadedR" || path != "old" || name != "a.bin" || depth != 2 || sha1 != f.next.SHA1 || sha256 != f.next.SHA256 || md5 != f.next.MD5 || size != f.next.Size {
		t.Fatalf("same-tx identity/content = %d/%s/%s/%s/%d/%s/%s/%s/%d", kind, repo, path, name, depth, sha1, sha256, md5, size)
	}
	var created, modified, updated int64
	var creator, original string
	// NULL 列用 sql.NullString 在独立旧行 helper 中完整核对；事务内用 IS NULL。
	// Assert all seven metadata columns in-transaction, including SQL nullness.
	var modifierNull, md5Null bool
	if err := f.tx.QueryRowContext(f.ctx, fmt.Sprintf("SELECT created, modified, updated, created_by, modified_by IS NULL, sha1_original, md5_original IS NULL FROM %s.nodes WHERE node_id=$1", f.schema), f.source.Reference.ID).Scan(&created, &modified, &updated, &creator, &modifierNull, &original, &md5Null); err != nil {
		t.Fatalf("same-tx metadata: %v", err)
	}
	if created != 1111111111111 || modified != 2222222222222 || updated != 3333333333333 || creator != "old-creator" || !modifierNull || original != "old-orig-1" || !md5Null {
		t.Fatal("binary assignment changed metadata or raw Original nullness")
	}
}

// 提交后独立连接核对新内容和原 ID/身份/七列；旧对象与新对象都保留。
// Independent post-commit reads verify new content and unchanged identity/metadata.
func TestUpdateLoadedFileBinaryPublishedCommit(t *testing.T) {
	f := lbSetup(t)
	lbUpdate(t, f, f.next, true)
	lbRequireProvisional(t, f)
	luOldRow(t, f.luFixture)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("caller commit: %v", err)
	}
	ufRequireOldNode(t, f.adm, f.schema, f.source.Reference.ID, "loadedR", "old", "a.bin", f.next)
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("node count after commit = %d, want 1", n)
	}
}

// 相同真实 winner 不能为了重写等值行而调用 Store.Update。
// An identical real winner must skip the actual Store.Update callback.
func TestUpdateLoadedFileBinarySameWinnerNoOp(t *testing.T) {
	f := lbSetup(t)
	lbUpdate(t, f, f.winner, false)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("caller commit: %v", err)
	}
	luOldRow(t, f.luFixture)
}

// 先证明调用方事务临时内容已变，外部仍旧；回滚后旧行恢复，binary 不回滚。
// Provisional change is visible only inside caller tx; rollback keeps published bytes.
func TestUpdateLoadedFileBinaryCallerRollback(t *testing.T) {
	f := lbSetup(t)
	lbUpdate(t, f, f.next, true)
	lbRequireProvisional(t, f)
	luOldRow(t, f.luFixture)
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	luOldRow(t, f.luFixture)
}

// 规范形状不是发布/登记证明：实际 FK 拒绝未发布 Calculate 结果，无隐式 Add。
// Canonical shape proves no registration: the real FK rejects unpublished metadata.
func TestUpdateLoadedFileBinaryUnregisteredWinnerRejected(t *testing.T) {
	f := lbSetup(t)
	unpublished, err := content.Calculate(f.ctx, strings.NewReader("canonical but intentionally never published or registered"))
	if err != nil {
		t.Fatalf("calculate only: %v", err)
	}
	var count int
	if err := f.adm.QueryRowContext(f.ctx, fmt.Sprintf("SELECT count(*) FROM %s.binaries WHERE sha1=$1", f.schema), unpublished.SHA1).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unregistered precondition = %d, %v", count, err)
	}
	d := &ufDelegates{st: f.st}
	got, err := repository.UpdateLoadedFileBinary(f.ctx, repository.LoadedFileBinaryInput{
		Source: f.source, Published: unpublished, CreatedPolicy: repository.PreserveLoadedFileCreated,
	}, f.catalog, d.updateRef)
	if err == nil || got != (repository.FileDirtyUpdateResult{}) {
		t.Fatalf("unregistered result = (%+v, %v), want zero/error", got, err)
	}
	d.wantUpdateOnce(t)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23503" || pgerr.ConstraintName != "nodes_binaries_fk" {
		t.Fatalf("want actual nodes FK failure, got %v", err)
	}
	if d.saved.Content != unpublished || d.saved.ID != f.source.Reference.ID {
		t.Fatal("unregistered operands were repaired or rerouted")
	}
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("caller rollback after FK failure: %v", err)
	}
	luOldRow(t, f.luFixture)
	if err := f.adm.QueryRowContext(f.ctx, fmt.Sprintf("SELECT count(*) FROM %s.binaries WHERE sha1=$1", f.schema), unpublished.SHA1).Scan(&count); err != nil || count != 0 {
		t.Fatalf("consumer implicitly registered binary: %d, %v", count, err)
	}
}

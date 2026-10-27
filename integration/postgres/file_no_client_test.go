// 已存在 local/no-client 节点消费者的真库接缝；不构造 Desired 或模拟 Store。
// Real existing-local/no-client seams, without manual Desired or fake Store.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
)

type ncRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func ncInput(f lbFixture, published content.Metadata, policy repository.LocalFileChecksumPolicy, modified, updated int64, user []uint16) repository.LocalFileNoClientInput {
	return repository.LocalFileNoClientInput{
		Source: f.source, Published: published, CreatedPolicy: repository.PreserveLoadedFileCreated,
		ChecksumPolicy: policy, ResourceModifiedMillis: modified, UpdatedMillis: updated, CurrentUserUTF16: user,
	}
}

func ncUpdate(t *testing.T, f lbFixture, in repository.LocalFileNoClientInput) {
	t.Helper()
	d := &ufDelegates{st: f.st}
	got, err := repository.UpdateLocalFileWithoutClientChecksums(f.ctx, in, f.catalog, d.updateRef)
	if err != nil || !got.Changed || got.Count != 1 {
		t.Fatalf("no-client update = (%+v,%v), want Changed=true Count=1", got, err)
	}
	d.wantUpdateOnce(t)
	if d.saved.ID != f.source.Reference.ID || d.saved.Path != f.source.Reference.Path || d.saved.Content != in.Published {
		t.Fatal("write must retain source identity and use published winner")
	}
	if d.saved.Metadata == nil || d.saved.Metadata.SHA1OriginalUTF16() != nil || d.saved.Metadata.MD5OriginalUTF16() != nil {
		t.Fatal("no-client write must clear both persisted Originals to SQL NULL")
	}
}

// 同事务和独立连接共用字面断言，覆盖身份、内容和全部七列及空性。
// Literal assertions work for caller tx and independent pool, including nullness.
func ncRequireRow(t *testing.T, f lbFixture, reader ncRowReader, published content.Metadata, modified, updated int64, user *string) {
	t.Helper()
	var kind, depth int16
	var repo, path, name, sha1, sha256, md5 string
	var size, created, gotModified, gotUpdated int64
	var creator, modifier, sha1Orig, md5Orig sql.NullString
	err := reader.QueryRowContext(f.ctx, fmt.Sprintf(
		"SELECT node_type,repo,node_path,node_name,depth,sha1_actual,sha256,md5_actual,bin_length,"+
			"created,modified,updated,created_by,modified_by,sha1_original,md5_original FROM %s.nodes WHERE node_id=$1", f.schema), f.source.Reference.ID).
		Scan(&kind, &repo, &path, &name, &depth, &sha1, &sha256, &md5, &size, &created, &gotModified, &gotUpdated, &creator, &modifier, &sha1Orig, &md5Orig)
	if err != nil {
		t.Fatalf("read no-client row: %v", err)
	}
	if kind != 1 || repo != "loadedR" || path != "old" || name != "a.bin" || depth != 2 || sha1 != published.SHA1 || sha256 != published.SHA256 || md5 != published.MD5 || size != published.Size {
		t.Fatal("node identity/content differs from independent expected facts")
	}
	if created != 1111111111111 || gotModified != modified || gotUpdated != updated || !creator.Valid || creator.String != "old-creator" || sha1Orig.Valid || md5Orig.Valid {
		t.Fatalf("metadata = %d/%d/%d/%v/%v/%v, want unchanged creator and cleared Originals", created, gotModified, gotUpdated, creator, sha1Orig, md5Orig)
	}
	if user == nil {
		if modifier.Valid {
			t.Fatalf("modifiedBy = %v, want SQL NULL", modifier)
		}
	} else if !modifier.Valid || modifier.String != *user {
		t.Fatalf("modifiedBy = %v, want %q", modifier, *user)
	}
}

// CLIENT 模式：真实 B 与请求 modified=0，回退 created；长 actor 只在既有写映射缩写。
// CLIENT: published B, modified=0 falls back to created; existing mapper bounds actor.
func TestLocalFileNoClientClientCommit(t *testing.T) {
	f := lbSetup(t)
	const updated = int64(7777777777777)
	rawUser := mdUnits(strings.Repeat("u", 70))
	wantUser := strings.Repeat("u", 61) + "..."
	in := ncInput(f, f.next, repository.LocalFileChecksumClient, 0, updated, rawUser)
	ncUpdate(t, f, in)
	ncRequireRow(t, f, f.tx, f.next, 1111111111111, updated, &wantUser)
	luOldRow(t, f.luFixture)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("caller commit: %v", err)
	}
	ncRequireRow(t, f, f.adm, f.next, 1111111111111, updated, &wantUser)
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("committed node count = %d, want 1", n)
	}
}

// SERVER 同 winner 仍写；提交后的新加载再次重建 SHA256 NO_ORIG，不能变成 no-op。
// SERVER same-winner stays dirty; a fresh load reconstructs the SHA256 marker again.
func TestLocalFileNoClientServerSameWinnerFreshReload(t *testing.T) {
	f := lbSetup(t)
	const modified, updated = int64(2222222222222), int64(3333333333333)
	ncUpdate(t, f, ncInput(f, f.winner, repository.LocalFileChecksumServer, modified, updated, nil))
	ncRequireRow(t, f, f.tx, f.winner, modified, updated, nil)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("first caller commit: %v", err)
	}
	ncRequireRow(t, f, f.adm, f.winner, modified, updated, nil)

	// 新事务真实读取，而非重用旧 Source 或手工恢复 Original marker。
	// Fresh transactional lookup, without stale Source reuse or a fabricated marker.
	tx, st := nodeTx(t, f.db)
	source, err := st.LookupSnapshotSource(f.ctx, f.source.Reference.Path)
	if err != nil {
		t.Fatalf("fresh LookupSnapshotSource: %v", err)
	}
	if source.Reference.ID != f.source.Reference.ID || source.Reference.Metadata == nil || source.Reference.Metadata.SHA1OriginalUTF16() != nil || source.Reference.Metadata.MD5OriginalUTF16() != nil {
		t.Fatal("fresh row must have the same ID and persisted NULL Originals")
	}
	second := f
	second.tx, second.st, second.source = tx, st, source
	ncUpdate(t, second, ncInput(second, f.winner, repository.LocalFileChecksumServer, modified, updated, nil))
	ncRequireRow(t, second, tx, f.winner, modified, updated, nil)
	if err := tx.Commit(); err != nil {
		t.Fatalf("second caller commit: %v", err)
	}
	ncRequireRow(t, second, f.adm, f.winner, modified, updated, nil)
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("fresh-load repeat created nodes: %d", n)
	}
}

// 回滚前必须先看到真实 provisional 更新，外部旧行不因 consumer 隐式提交而变化。
// Prove provisional content/metadata before rollback; no implicit consumer commit.
func TestLocalFileNoClientCallerRollback(t *testing.T) {
	f := lbSetup(t)
	const modified, updated = int64(4444444444444), int64(-99)
	user := "rolling-user"
	ncUpdate(t, f, ncInput(f, f.next, repository.LocalFileChecksumClient, modified, updated, mdUnits(user)))
	ncRequireRow(t, f, f.tx, f.next, modified, updated, &user)
	luOldRow(t, f.luFixture)
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	luOldRow(t, f.luFixture)
}

// 仅 Calculate 的新摘要虽规范，真实 FK 仍拒绝；没有隐式 binary 登记或发布。
// A canonical Calculate-only digest fails the actual FK without implicit registration.
func TestLocalFileNoClientUnregisteredBinaryRejected(t *testing.T) {
	f := lbSetup(t)
	unregistered, err := content.Calculate(f.ctx, strings.NewReader("no-client canonical metadata never published"))
	if err != nil {
		t.Fatalf("calculate: %v", err)
	}
	var count int
	query := fmt.Sprintf("SELECT count(*) FROM %s.binaries WHERE sha1=$1", f.schema)
	if err := f.adm.QueryRowContext(f.ctx, query, unregistered.SHA1).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unregistered precondition = %d,%v", count, err)
	}
	d := &ufDelegates{st: f.st}
	in := ncInput(f, unregistered, repository.LocalFileChecksumServer, 0, 999, mdUnits("not-published"))
	got, err := repository.UpdateLocalFileWithoutClientChecksums(f.ctx, in, f.catalog, d.updateRef)
	if err == nil || got != (repository.FileDirtyUpdateResult{}) {
		t.Fatalf("unregistered result = (%+v,%v), want zero/error", got, err)
	}
	d.wantUpdateOnce(t)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23503" || pgerr.ConstraintName != "nodes_binaries_fk" {
		t.Fatalf("want real nodes FK rejection, got %v", err)
	}
	if d.saved.ID != f.source.Reference.ID || d.saved.Content != unregistered {
		t.Fatal("consumer repaired/rerouted unregistered binary facts")
	}
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	luOldRow(t, f.luFixture)
	if err := f.adm.QueryRowContext(f.ctx, query, unregistered.SHA1).Scan(&count); err != nil || count != 0 {
		t.Fatalf("implicit registry row = %d,%v", count, err)
	}
}

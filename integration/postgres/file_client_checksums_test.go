// 原始客户端摘要的真实 local 消费者接缝；Source 和 binary winner 均来自实际组件。
// Real local client-checksum seams with actual Source reads and published winners.
package postgresit

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
)

func ccMatching(p content.Metadata) repository.ClientFileChecksums {
	return repository.ClientFileChecksums{
		SHA1Original: mdUnits(strings.ToUpper(p.SHA1)), SHA256Original: mdUnits(strings.ToUpper(p.SHA256)), MD5Original: mdUnits(strings.ToUpper(p.MD5)),
	}
}

func ccSuccess(t *testing.T, f lbFixture, published content.Metadata, checksums repository.ClientFileChecksums, policy repository.LocalFileChecksumPolicy, modified, updated int64, user []uint16, changed bool) {
	t.Helper()
	d := &ufDelegates{st: f.st}
	in := ncInput(f, published, policy, modified, updated, user)
	got, err := repository.UpdateLocalFileWithClientChecksums(f.ctx, in, checksums, f.catalog, d.updateRef)
	count := int64(0)
	if changed {
		count = 1
	}
	if err != nil || got.Changed != changed || got.Count != count {
		t.Fatalf("client update = (%+v,%v), want Changed=%v Count=%d", got, err, changed, count)
	}
	if !changed {
		if d.update != 0 {
			t.Fatalf("no-op callback count = %d, want 0", d.update)
		}
		return
	}
	d.wantUpdateOnce(t)
	if d.saved.ID != f.source.Reference.ID || d.saved.Path != f.source.Reference.Path || d.saved.Content != published {
		t.Fatal("write must derive identity from source and content from published winner")
	}
}

// 全投影和七列由独立字面预期核对，不借产品 Desired/normalizer 生成 oracle。
// Verify full projection and seven columns against facts, not a product-built Desired.
func ccRow(t *testing.T, f lbFixture, reader ncRowReader, published content.Metadata, modified, updated int64, user, sha1Original, md5Original *string) {
	t.Helper()
	var kind, depth int16
	var repo, path, name, sha1, sha256, md5 string
	var size, created, gotModified, gotUpdated int64
	var creator, modifier, so, mo sql.NullString
	err := reader.QueryRowContext(f.ctx, fmt.Sprintf("SELECT node_type,repo,node_path,node_name,depth,sha1_actual,sha256,md5_actual,bin_length,created,modified,updated,created_by,modified_by,sha1_original,md5_original FROM %s.nodes WHERE node_id=$1", f.schema), f.source.Reference.ID).
		Scan(&kind, &repo, &path, &name, &depth, &sha1, &sha256, &md5, &size, &created, &gotModified, &gotUpdated, &creator, &modifier, &so, &mo)
	if err != nil {
		t.Fatalf("client row read: %v", err)
	}
	if kind != 1 || repo != "loadedR" || path != "old" || name != "a.bin" || depth != 2 || sha1 != published.SHA1 || sha256 != published.SHA256 || md5 != published.MD5 || size != published.Size {
		t.Fatal("client row identity/content differs from expected facts")
	}
	if created != 1111111111111 || gotModified != modified || gotUpdated != updated || !creator.Valid || creator.String != "old-creator" {
		t.Fatal("client row times/creator changed unexpectedly")
	}
	for _, pair := range []struct {
		name string
		got  sql.NullString
		want *string
	}{{"modifiedBy", modifier, user}, {"SHA1Original", so, sha1Original}, {"MD5Original", mo, md5Original}} {
		if pair.want == nil {
			if pair.got.Valid {
				t.Fatalf("%s = %v, want SQL NULL", pair.name, pair.got)
			}
		} else if !pair.got.Valid || pair.got.String != *pair.want {
			t.Fatalf("%s = %v, want %q", pair.name, pair.got, *pair.want)
		}
	}
}

// CLIENT 比较新 B 而不是旧 A；大写 client Original 仅合法 hex 被规范为小写。
// CLIENT matches new B, not old A; valid uppercase originals normalize to lowercase.
func TestLocalClientChecksumsMatchingPublishedCommit(t *testing.T) {
	f := lbSetup(t)
	const modified, updated = int64(4444444444444), int64(5555555555555)
	user := "client-writer"
	ccSuccess(t, f, f.next, ccMatching(f.next), repository.LocalFileChecksumClient, modified, updated, mdUnits(user), true)
	ccRow(t, f, f.tx, f.next, modified, updated, &user, &f.next.SHA1, &f.next.MD5)
	luOldRow(t, f.luFixture)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("caller commit: %v", err)
	}
	ccRow(t, f, f.adm, f.next, modified, updated, &user, &f.next.SHA1, &f.next.MD5)
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("node count = %d, want 1", n)
	}
}

// 只让 SHA256 不匹配，证明未持久化 Original 的第三种摘要也参与 CLIENT 验证。
// Only SHA256 mismatches: verification must include this non-persisted Original kind.
func TestLocalClientChecksumsSHA256MismatchSkipsStore(t *testing.T) {
	f := lbSetup(t)
	checksums := ccMatching(f.next)
	checksums.SHA256Original = mdUnits(strings.Repeat("0", 64))
	d := &ufDelegates{st: f.st}
	in := ncInput(f, f.next, repository.LocalFileChecksumClient, 4444, 5555, mdUnits("must-not-stamp"))
	got, err := repository.UpdateLocalFileWithClientChecksums(f.ctx, in, checksums, f.catalog, d.updateRef)
	if !errors.Is(err, repository.ErrClientChecksumMismatch) || got != (repository.FileDirtyUpdateResult{}) {
		t.Fatalf("SHA256 mismatch = (%+v,%v), want zero/checksum error", got, err)
	}
	if d.update != 0 {
		t.Fatalf("mismatch Store calls = %d, want 0", d.update)
	}
	var modified, updated int64
	var actorNull bool
	if err := f.tx.QueryRowContext(f.ctx, fmt.Sprintf("SELECT modified,updated,modified_by IS NULL FROM %s.nodes WHERE node_id=$1", f.schema), f.source.Reference.ID).Scan(&modified, &updated, &actorNull); err != nil {
		t.Fatal(err)
	}
	if modified != 2222222222222 || updated != 3333333333333 || !actorNull {
		t.Fatal("CLIENT mismatch stamped caller transaction")
	}
	luOldRow(t, f.luFixture)
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	luOldRow(t, f.luFixture)
}

// SERVER 保留 raw mismatch；SHA256 Original 仅参与比较/策略，不虚构 SQL 列。
// SERVER retains mismatching raw Original; SHA256 Original has no SQL column.
func TestLocalClientChecksumsServerMismatchAndUnregistered(t *testing.T) {
	f := lbSetup(t)
	checksums := ccMatching(f.next)
	md5Mismatch := strings.Repeat("0", 32)
	checksums.MD5Original = mdUnits(md5Mismatch)
	checksums.SHA256Original = mdUnits(strings.Repeat("0", 64))
	user := "server-writer"
	const modified, updated = int64(4444), int64(5555)
	ccSuccess(t, f, f.next, checksums, repository.LocalFileChecksumServer, modified, updated, mdUnits(user), true)
	ccRow(t, f, f.tx, f.next, modified, updated, &user, &f.next.SHA1, &md5Mismatch)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("SERVER commit: %v", err)
	}
	ccRow(t, f, f.adm, f.next, modified, updated, &user, &f.next.SHA1, &md5Mismatch)

	// 新事务真实读取后，规范但未 Add 的 C 必须到达真实 FK，而不是隐式发布。
	// Fresh real lookup; canonical Calculate-only C must fail the actual FK.
	t.Run("unregistered-C", func(t *testing.T) {
		tx, st := nodeTx(t, f.db)
		source, err := st.LookupSnapshotSource(f.ctx, f.source.Reference.Path)
		if err != nil {
			t.Fatalf("fresh source: %v", err)
		}
		tail := f
		tail.tx, tail.st, tail.source = tx, st, source
		c, err := content.Calculate(f.ctx, strings.NewReader("client checksum C intentionally never added"))
		if err != nil {
			t.Fatal(err)
		}
		var count int
		query := fmt.Sprintf("SELECT count(*) FROM %s.binaries WHERE sha1=$1", f.schema)
		if err := f.adm.QueryRowContext(f.ctx, query, c.SHA1).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unregistered C precondition = %d,%v", count, err)
		}
		d := &ufDelegates{st: st}
		in := ncInput(tail, c, repository.LocalFileChecksumClient, modified, updated, mdUnits(user))
		got, err := repository.UpdateLocalFileWithClientChecksums(f.ctx, in, repository.ClientFileChecksums{SHA256Original: mdUnits("NO_ORIG")}, f.catalog, d.updateRef)
		if err == nil || got != (repository.FileDirtyUpdateResult{}) {
			t.Fatalf("unregistered C = (%+v,%v), want zero/error", got, err)
		}
		d.wantUpdateOnce(t)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "23503" || pgerr.ConstraintName != "nodes_binaries_fk" {
			t.Fatalf("want real nodes FK failure, got %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("C rollback: %v", err)
		}
		ccRow(t, f, f.adm, f.next, modified, updated, &user, &f.next.SHA1, &md5Mismatch)
		if err := f.adm.QueryRowContext(f.ctx, query, c.SHA1).Scan(&count); err != nil || count != 0 {
			t.Fatalf("implicit C registration = %d,%v", count, err)
		}
	})
}

// 真正 no-op 的 seed 将 SHA1/MD5 Original 置 NULL；不能用 old-orig-1 冒充匹配。
// A genuine no-op seed has NULL SHA1/MD5 Originals, never the mismatching old literal.
func TestLocalClientChecksumsGenuineNoOpThenRollback(t *testing.T) {
	f := lbSetup(t)
	res, err := f.tx.ExecContext(f.ctx, fmt.Sprintf("UPDATE %s.nodes SET sha1_original=NULL,md5_original=NULL WHERE node_id=$1", f.schema), f.source.Reference.ID)
	if err != nil {
		t.Fatalf("seed nullable Originals: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("seed rows = %d,%v", n, err)
	}
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	tx, st := nodeTx(t, f.db)
	source, err := st.LookupSnapshotSource(f.ctx, f.source.Reference.Path)
	if err != nil {
		t.Fatalf("fresh no-op source: %v", err)
	}
	f.tx, f.st, f.source = tx, st, source
	const oldModified, oldUpdated = int64(2222222222222), int64(3333333333333)
	checksums := repository.ClientFileChecksums{SHA256Original: mdUnits("NO_ORIG")}
	ccSuccess(t, f, f.winner, checksums, repository.LocalFileChecksumClient, oldModified, oldUpdated, nil, false)
	ccRow(t, f, f.tx, f.winner, oldModified, oldUpdated, nil, nil, nil)
	ccRow(t, f, f.adm, f.winner, oldModified, oldUpdated, nil, nil, nil)

	user := "rollback-client"
	ccSuccess(t, f, f.next, checksums, repository.LocalFileChecksumClient, 7777, 8888, mdUnits(user), true)
	ccRow(t, f, f.tx, f.next, 7777, 8888, &user, nil, nil)
	ccRow(t, f, f.adm, f.winner, oldModified, oldUpdated, nil, nil, nil)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	ccRow(t, f, f.adm, f.winner, oldModified, oldUpdated, nil, nil, nil)
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("node count = %d, want 1", n)
	}
}

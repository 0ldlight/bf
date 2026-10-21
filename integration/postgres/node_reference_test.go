// 事务内文件引用的真实 PostgreSQL 场景（BIN-132）：调用方事务所有权、
// 临时计数、唯一/外键约束、点哨兵边界、Kind 忽略、回滚保留与行锁取消。
// Real-PostgreSQL scenarios for transaction-scoped file references: caller
// transaction ownership, provisional counts, unique/FK constraints, the dot
// sentinel boundary, kind-ignoring lookup, rollback preservation and
// lock-wait cancellation.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// nodeFixture 复用 newRegistry 的独立 bfreg_ schema，再显式应用 nodes 投影
// 模式（依赖同 schema 的 binaries 外键目标），返回绑定该 schema 的连接池。
// nodeFixture reuses the isolated bfreg_ schema from newRegistry and then
// applies the nodes projection schema explicitly (its FK targets binaries in
// the same schema), returning a pool bound to that schema.
func nodeFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	_, schema := newRegistry(t)
	db := registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, refpg.SchemaSQL); err != nil {
		t.Fatalf("apply nodes schema: %v", err)
	}
	return db, schema
}

func nodeMeta(sha1 string) content.Metadata {
	return content.Metadata{SHA1: sha1, SHA256: strings.Repeat("2", 64), MD5: strings.Repeat("3", 32), Size: 21}
}

func seedNodeBinary(t *testing.T, adm *sql.DB, schema, sha1 string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO %s.binaries (sha1, md5, bin_length, sha256) VALUES ($1, $2, $3, $4)", schema),
		sha1, strings.Repeat("3", 32), int64(21), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("seed binary row: %v", err)
	}
}

func fileRefOf(t *testing.T, id int64, repo, parent, name string, meta content.Metadata) repository.FileReference {
	t.Helper()
	p, err := repository.NewPath(repo, parent, name, true)
	if err != nil {
		t.Fatalf("NewPath(%q,%q,%q): %v", repo, parent, name, err)
	}
	return repository.FileReference{ID: id, Path: p, Content: meta}
}

// nodeTx 开启调用方自有事务并绑定 Store；BeginTx 使用 background 上下文
// （该 ctx 绑定事务整个生命周期，不得提前 cancel），语句各自携带有限 ctx；
// t.Cleanup 兜底回滚，即使 t.Fatal 也不遗留持锁事务。
// nodeTx begins a caller-owned transaction bound to a Store; BeginTx uses a
// background context (that ctx governs the whole tx lifetime and must not
// be cancelled early), statements carry their own bounded ctx, and
// t.Cleanup rolls back as a safety net so even a t.Fatal leaves no
// lock-holding tx.
func nodeTx(t *testing.T, db *sql.DB) (*sql.Tx, *refpg.Store) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	st, err := refpg.New(tx)
	if err != nil {
		t.Fatalf("refpg.New: %v", err)
	}
	return tx, st
}

func countNodes(t *testing.T, adm *sql.DB, schema string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int
	if err := adm.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s.nodes", schema)).Scan(&n); err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	return n
}

func pgCodeOf(t *testing.T, err error) string {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("driver cause must stay reachable via Unwrap, got %v", err)
	}
	return pgErr.Code
}

func TestNodeCreateLookupCanonicalRoundTrip(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	sha1A := strings.Repeat("1", 40)
	seedNodeBinary(t, adm, schema, sha1A)
	metaA := nodeMeta(sha1A)

	tx, st := nodeTx(t, db)
	if n, err := st.Create(ctx, fileRefOf(t, 101, "repoA", "a/b", "f.bin", metaA)); err != nil || n != 1 {
		t.Fatalf("Create = (%d, %v), want (1, nil)", n, err)
	}
	var nodeType int16
	var repo, nodePath, nodeName string
	var depth int16
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	// 独立连接在提交前不可见 → 用事务内读校验规范列。
	// An independent connection cannot see the uncommitted row; verify the
	// canonical columns through the same transaction instead.
	if err := tx.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT node_type, repo, node_path, node_name, depth FROM %s.nodes WHERE node_id = 101", schema)).
		Scan(&nodeType, &repo, &nodePath, &nodeName, &depth); err != nil {
		t.Fatalf("in-tx row read: %v", err)
	}
	if nodeType != 1 || repo != "repoA" || nodePath != "a/b" || nodeName != "f.bin" || depth != 3 {
		t.Fatalf("stored row = (%d %q %q %q %d), want canonical projection", nodeType, repo, nodePath, nodeName, depth)
	}
	// 空父路径按字面空串映射为 SQL '.' 哨兵。
	// An empty parent maps (empty string only) to the SQL '.' sentinel.
	if n, err := st.Create(ctx, fileRefOf(t, 102, "repoA", "", "top.bin", metaA)); err != nil || n != 1 {
		t.Fatalf("Create(empty parent) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT node_path FROM %s.nodes WHERE node_id = 102", schema)).Scan(&nodePath); err != nil {
		t.Fatalf("in-tx sentinel read: %v", err)
	}
	if nodePath != "." {
		t.Fatalf("empty parent stored as %q, want dot sentinel", nodePath)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoA", "a/b", "f.bin", metaA).Path)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.ID != 101 || got.Path.PathName() != "a/b/f.bin" || got.Path.Repo() != "repoA" ||
		!got.Path.IsFile() || got.Content != metaA {
		t.Fatalf("Lookup returned %+v, want id 101 with canonical meta", got)
	}
	// 输入 Kind 被忽略：folder 查询命中同一文件行。
	// The input kind is ignored: a folder-kind query hits the same file row.
	folderQuery, err := repository.NewPath("repoA", "a/b", "f.bin", false)
	if err != nil {
		t.Fatalf("NewPath(folder): %v", err)
	}
	if got, err = lookSt.Lookup(ctx, folderQuery); err != nil || got.ID != 101 {
		t.Fatalf("folder-kind Lookup = (%+v, %v), want id 101", got, err)
	}
	// 空父路径经 '.' 哨兵往返为空串。
	// The empty parent round-trips through the '.' sentinel.
	if got, err = lookSt.Lookup(ctx, fileRefOf(t, 0, "repoA", "", "top.bin", metaA).Path); err != nil ||
		got.ID != 102 || got.Path.Parent() != "" {
		t.Fatalf("empty-parent Lookup = (%+v, %v), want id 102 with empty parent", got, err)
	}
	// 大小写与组件敏感：仓库/父/名大小写不同与组件切分不同都是不同键。
	// Case and component sensitivity: differing case in any component or a
	// different component split are distinct keys.
	if n, err := lookSt.Create(ctx, fileRefOf(t, 103, "REPOA", "a/b", "f.bin", metaA)); err != nil || n != 1 {
		t.Fatalf("Create(upper repo) = (%d, %v), want distinct key", n, err)
	}
	if n, err := lookSt.Create(ctx, fileRefOf(t, 104, "repoA", "a/b", "F.BIN", metaA)); err != nil || n != 1 {
		t.Fatalf("Create(upper name) = (%d, %v), want distinct key", n, err)
	}
	if n, err := lookSt.Create(ctx, fileRefOf(t, 105, "repoA", "", "a/b/f.bin", metaA)); err != nil || n != 1 {
		t.Fatalf("Create(flat name) = (%d, %v), want distinct component split", n, err)
	}
	if got, err = lookSt.Lookup(ctx, fileRefOf(t, 0, "repoA", "", "a/b/f.bin", metaA).Path); err != nil ||
		got.ID != 105 || got.Path.Parent() != "" || got.Path.Name() != "a/b/f.bin" {
		t.Fatalf("flat-name Lookup = (%+v, %v), want id 105 with split components", got, err)
	}
	if err := lookTx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 5 {
		t.Fatalf("node count = %d, want 5", n)
	}
}

func TestNodeUncommittedInvisibleCommitVisibleRollbackAbsent(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	seedNodeBinary(t, adm, schema, strings.Repeat("4", 40))
	meta := nodeMeta(strings.Repeat("4", 40))

	tx, st := nodeTx(t, db)
	if n, err := st.Create(ctx, fileRefOf(t, 201, "repoB", "", "keep.bin", meta)); err != nil || n != 1 {
		t.Fatalf("Create = (%d, %v), want (1, nil)", n, err)
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("uncommitted row visible outside: count = %d, want 0", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("committed row invisible: count = %d, want 1", n)
	}

	tx2, st2 := nodeTx(t, db)
	if n, err := st2.Create(ctx, fileRefOf(t, 202, "repoB", "", "gone.bin", meta)); err != nil || n != 1 {
		t.Fatalf("Create(pre-rollback) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("rolled-back row survived: count = %d, want 1", n)
	}
}

func TestNodeDuplicateTupleAndIDReject23505(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	sha1 := strings.Repeat("5", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	tx, st := nodeTx(t, db)
	if n, err := st.Create(ctx, fileRefOf(t, 301, "repoC", "dir", "a.bin", meta)); err != nil || n != 1 {
		t.Fatalf("seed Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	tx2, st2 := nodeTx(t, db)
	_, err := st2.Create(ctx, fileRefOf(t, 302, "repoC", "dir", "a.bin", meta))
	if err == nil {
		t.Fatal("duplicate tuple Create must fail")
	}
	if code := pgCodeOf(t, err); code != "23505" {
		t.Fatalf("duplicate tuple SQLSTATE = %s, want 23505", code)
	}
	if s := err.Error(); s != "postgres nodes: create file reference" {
		t.Fatalf("Error() = %q, want fixed create stage text", s)
	}
	// 失败不隐藏中止的事务：同事务后续写入得到 25P02。
	// The failure never hides the aborted transaction: a follow-up write
	// in the same tx sees 25P02.
	_, err = st2.Create(ctx, fileRefOf(t, 303, "repoC", "dir", "b.bin", meta))
	if err == nil {
		t.Fatal("write in aborted tx must fail")
	}
	if code := pgCodeOf(t, err); code != "25P02" {
		t.Fatalf("aborted-tx SQLSTATE = %s, want 25P02", code)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	// 调用方回滚保留原行。
	// The caller rollback preserves the original row.
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("count after rollback = %d, want 1 (original preserved)", n)
	}

	tx3, st3 := nodeTx(t, db)
	if _, err := st3.Create(ctx, fileRefOf(t, 301, "repoC", "dir", "other.bin", meta)); err == nil {
		t.Fatal("duplicate id Create must fail")
	} else if code := pgCodeOf(t, err); code != "23505" {
		t.Fatalf("duplicate id SQLSTATE = %s, want 23505", code)
	}
	if err := tx3.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestNodeCreateMissingBinaryFK23503(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	tx, st := nodeTx(t, db)
	// 该 SHA-1 不在 binaries：FK 拒绝，明细错误经 Unwrap 可达。
	// The SHA-1 is absent from binaries: the FK rejects and the driver
	// detail stays reachable via Unwrap.
	_, err := st.Create(context.Background(), fileRefOf(t, 401, "repoD", "", "x.bin",
		nodeMeta(strings.Repeat("6", 40))))
	if err == nil {
		t.Fatal("Create with missing binary must fail")
	}
	if code := pgCodeOf(t, err); code != "23503" {
		t.Fatalf("missing FK SQLSTATE = %s, want 23503", code)
	}
	if s := err.Error(); s != "postgres nodes: create file reference" {
		t.Fatalf("Error() = %q, want fixed create stage text", s)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
}

func TestNodeUpdateSwitchesSHA1AndKeepsBothBinaries(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	sha1A, sha1B := strings.Repeat("7", 40), strings.Repeat("8", 40)
	seedNodeBinary(t, adm, schema, sha1A)
	seedNodeBinary(t, adm, schema, sha1B)

	tx, st := nodeTx(t, db)
	if n, err := st.Create(ctx, fileRefOf(t, 501, "repoE", "old", "a.bin", nodeMeta(sha1A))); err != nil || n != 1 {
		t.Fatalf("seed Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	tx2, st2 := nodeTx(t, db)
	if n, err := st2.Update(ctx, fileRefOf(t, 501, "repoE", "new", "a.bin", nodeMeta(sha1B))); err != nil || n != 1 {
		t.Fatalf("Update = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoE", "new", "a.bin", nodeMeta(sha1B)).Path)
	if err != nil {
		t.Fatalf("Lookup(new path): %v", err)
	}
	if got.ID != 501 || got.Content.SHA1 != sha1B {
		t.Fatalf("updated Lookup = (id %d sha1 %q), want stable id 501 with new sha1", got.ID, got.Content.SHA1)
	}
	if _, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoE", "old", "a.bin", nodeMeta(sha1A)).Path); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Lookup(old path) = %v, want sql.ErrNoRows", err)
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, _, _, _, ok := rowOf(t, adm, schema, sha1A); !ok {
		t.Fatal("both binary rows must be retained (old sha1)")
	}
	if _, _, _, _, ok := rowOf(t, adm, schema, sha1B); !ok {
		t.Fatal("both binary rows must be retained (new sha1)")
	}
	// 缺失 ID：0,nil，绝不转创建。
	// A missing ID yields 0,nil with no create.
	tx3, st3 := nodeTx(t, db)
	if n, err := st3.Update(ctx, fileRefOf(t, 424242, "repoE", "x", "y.bin", nodeMeta(sha1A))); err != nil || n != 0 {
		t.Fatalf("Update(missing id) = (%d, %v), want (0, nil)", n, err)
	}
	if err := tx3.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("count after missing-id update = %d, want 1 (no insert)", n)
	}
}

func TestNodeFailedTxnRollbackPreservesOldReference(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	sha1A := strings.Repeat("9", 40)
	seedNodeBinary(t, adm, schema, sha1A)
	metaA := nodeMeta(sha1A)

	tx, st := nodeTx(t, db)
	if n, err := st.Create(ctx, fileRefOf(t, 601, "repoF", "dir", "a.bin", metaA)); err != nil || n != 1 {
		t.Fatalf("seed Create = (%d, %v), want (1, nil)", n, err)
	}
	if n, err := st.Create(ctx, fileRefOf(t, 602, "repoF", "dir", "b.bin", metaA)); err != nil || n != 1 {
		t.Fatalf("seed Create b = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 同一事务：先前成功的写入 + FK 失败的 Update → 调用方回滚后全部消失，
	// 旧引用保留。
	// One transaction: an earlier successful write plus an FK-failing
	// Update; the caller rollback discards both and keeps the old
	// reference.
	tx2, st2 := nodeTx(t, db)
	if n, err := st2.Create(ctx, fileRefOf(t, 603, "repoF", "dir", "c.bin", metaA)); err != nil || n != 1 {
		t.Fatalf("in-tx Create = (%d, %v), want (1, nil)", n, err)
	}
	if _, err := st2.Update(ctx, fileRefOf(t, 601, "repoF", "dir", "a.bin",
		nodeMeta(strings.Repeat("a", 40)))); err == nil {
		t.Fatal("Update to missing binary must fail")
	} else if code := pgCodeOf(t, err); code != "23503" {
		t.Fatalf("FK-failed Update SQLSTATE = %s, want 23503", code)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 2 {
		t.Fatalf("count after rollback = %d, want 2 (earlier writes rolled back)", n)
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoF", "dir", "a.bin", metaA).Path)
	if err != nil || got.ID != 601 || got.Content != metaA {
		t.Fatalf("old reference not preserved: (%+v, %v)", got, err)
	}
	// 唯一冲突的 Update 同样回滚保留旧引用。
	// A unique-conflict Update also rolls back and keeps the old ref.
	if _, err := lookSt.Update(ctx, fileRefOf(t, 601, "repoF", "dir", "b.bin", metaA)); err == nil {
		t.Fatal("Update onto occupied tuple must fail")
	} else if code := pgCodeOf(t, err); code != "23505" {
		t.Fatalf("unique Update SQLSTATE = %s, want 23505", code)
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got2 := countNodes(t, adm, schema); got2 != 2 {
		t.Fatalf("count = %d, want 2", got2)
	}
}

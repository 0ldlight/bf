// 事务边界场景（BIN-132 续）：延迟约束的临时计数与失败提交、原始 folder 行、
// 存量 NULL 映射、行锁取消与真实 filestore/binary.Service 组合。
// Transaction-boundary scenarios (continued): provisional counts under a
// deferred constraint, raw folder rows, legacy NULL mapping, lock-wait
// cancellation and the real filestore/binary.Service composition.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	nodereg "github.com/0ldlight/bf/internal/registry/postgres"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

func TestNodeDeferredCommitFailureRowAbsent(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	sha1 := strings.Repeat("b", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// 自有的延迟约束触发器：INSERT 成功、COMMIT 失败。
	// An owned deferred constraint trigger: the INSERT succeeds and the
	// COMMIT fails.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"CREATE FUNCTION %s.bfnode_block() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'deferred test constraint'; END $$ LANGUAGE plpgsql", schema)); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"CREATE CONSTRAINT TRIGGER bfnode_block AFTER INSERT ON %s.nodes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s.bfnode_block()", schema, schema)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	tx, st := nodeTx(t, db)
	// 成功计数在调用方 Commit 之前是临时的：这里返回 1,nil 而 Commit 失败。
	// A successful count stays PROVISIONAL until the caller commits: this
	// returns 1,nil while the commit then fails.
	n, err := st.Create(ctx, fileRefOf(t, 701, "repoG", "", "a.bin", meta))
	if err != nil || n != 1 {
		t.Fatalf("Create under deferred trigger = (%d, %v), want provisional (1, nil)", n, err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("Commit must fail under the deferred constraint")
	}
	if got := countNodes(t, adm, schema); got != 0 {
		t.Fatalf("row survived a failed commit: count = %d, want 0", got)
	}
	// 移除自有注入后同一引用在新事务中成功。
	// After removing the owned injection, the same reference succeeds in a
	// fresh transaction.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER bfnode_block ON %s.nodes", schema)); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	tx2, st2 := nodeTx(t, db)
	if n, err := st2.Create(ctx, fileRefOf(t, 701, "repoG", "", "a.bin", meta)); err != nil || n != 1 {
		t.Fatalf("retry Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("retry commit: %v", err)
	}
	if got := countNodes(t, adm, schema); got != 1 {
		t.Fatalf("count after retry = %d, want 1", got)
	}
}

func TestNodeRawFolderRowLookupAndGuards(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	sha1 := strings.Repeat("c", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// 原始 folder 行（node_type=0）：投影允许存储，但不提供 folder API。
	// A raw folder row (node_type=0): the projection stores it but ships
	// no folder API.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth) VALUES (801, 0, 'repoH', 'dir', 'd', 2)", schema)); err != nil {
		t.Fatalf("seed raw folder row: %v", err)
	}
	tx, st := nodeTx(t, db)
	got, err := st.Lookup(ctx, fileRefOf(t, 0, "repoH", "dir", "d", meta).Path)
	if err == nil || got != (repository.FileReference{}) {
		t.Fatalf("folder Lookup = (%+v, %v), want zero ref + error", got, err)
	}
	if !errors.Is(err, refpg.ErrNotFile) {
		t.Fatalf("folder Lookup must expose ErrNotFile, got %v", err)
	}
	if s := err.Error(); s != "postgres nodes: lookup file reference" {
		t.Fatalf("Error() = %q, want fixed lookup stage text", s)
	}
	folderQuery, ferr := repository.NewPath("repoH", "dir", "d", false)
	if ferr != nil {
		t.Fatalf("NewPath(folder): %v", ferr)
	}
	if _, err := st.Lookup(ctx, folderQuery); !errors.Is(err, refpg.ErrNotFile) {
		t.Fatalf("folder-kind query on folder row must still be ErrNotFile, got %v", err)
	}
	// 同键 Create 与原始行冲突（唯一索引不分 Kind）；冲突后事务中止，回滚。
	// A Create on the same key conflicts with the raw row (the unique
	// index ignores kind); the conflict aborts the transaction, so the
	// caller rolls back before the guard check.
	if _, err := st.Create(ctx, fileRefOf(t, 802, "repoH", "dir", "d", meta)); err == nil {
		t.Fatal("Create over raw folder key must fail")
	} else if code := pgCodeOf(t, err); code != "23505" {
		t.Fatalf("folder-key Create SQLSTATE = %s, want 23505", code)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback after conflict: %v", err)
	}
	// Update 的文件 Kind 守卫（新事务）：folder 行 0 行命中且 node_type 不变。
	// The Update file-kind guard in a fresh transaction: the folder row
	// matches zero rows and node_type never changes.
	tx2, st2 := nodeTx(t, db)
	if n, err := st2.Update(ctx, fileRefOf(t, 801, "repoH", "dir", "d2", meta)); err != nil || n != 0 {
		t.Fatalf("Update(folder row) = (%d, %v), want (0, nil)", n, err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit guard update: %v", err)
	}
	var nodeType int16
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	if err := adm.QueryRowContext(rctx, fmt.Sprintf(
		"SELECT node_type FROM %s.nodes WHERE node_id = 801", schema)).Scan(&nodeType); err != nil || nodeType != 0 {
		t.Fatalf("folder row node_type = (%d, %v), want unchanged 0", nodeType, err)
	}
}

func TestNodeLegacyNullChecksumsMapEmptyWithoutMutation(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	sha1 := strings.Repeat("d", 40)
	seedNodeBinary(t, adm, schema, sha1)

	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth, bin_length, sha1_actual, md5_actual, sha256) "+
			"VALUES (901, 1, 'repoI', 'old', 'a.bin', 2, 7, $1, NULL, NULL)", schema), sha1); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	tx, st := nodeTx(t, db)
	got, err := st.Lookup(ctx, fileRefOf(t, 0, "repoI", "old", "a.bin", nodeMeta(sha1)).Path)
	if err != nil {
		t.Fatalf("Lookup(legacy row): %v", err)
	}
	if got.ID != 901 || got.Content.MD5 != "" || got.Content.SHA256 != "" ||
		got.Content.SHA1 != sha1 || got.Content.Size != 7 {
		t.Fatalf("legacy Lookup = %+v, want NULL md5/sha256 mapped empty", got)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	// 无回填/变异：行内 MD5/SHA256 仍为 NULL。
	// No backfill or mutation: the stored md5/sha256 stay NULL.
	var md5ns, sha256ns sql.NullString
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()
	if err := adm.QueryRowContext(rctx, fmt.Sprintf(
		"SELECT md5_actual, sha256 FROM %s.nodes WHERE node_id = 901", schema)).Scan(&md5ns, &sha256ns); err != nil {
		t.Fatalf("re-read legacy row: %v", err)
	}
	if md5ns.Valid || sha256ns.Valid {
		t.Fatalf("legacy row mutated: md5=%v sha256=%v", md5ns, sha256ns)
	}
}

func TestNodeRowLockCancellationAndFreshTx(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	sha1 := strings.Repeat("e", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// 持锁方在独立连接池上同步取表锁；defer Rollback 保证 t.Fatal 也释放。
	// The holder takes the table lock synchronously on an independent
	// pool; the deferred Rollback releases it even on t.Fatal.
	holder := registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema)
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer lockCancel()
	lockTx, err := holder.BeginTx(lockCtx, nil)
	if err != nil {
		t.Fatalf("holder begin: %v", err)
	}
	defer lockTx.Rollback()
	if _, err := lockTx.ExecContext(lockCtx, fmt.Sprintf(
		"LOCK TABLE %s.nodes IN ACCESS EXCLUSIVE MODE", schema)); err != nil {
		t.Fatalf("lock table: %v", err)
	}
	tx, st := nodeTx(t, db)
	// 有限时限的语句取消：原因保留，文案固定。
	// A bounded statement cancellation: the cause is preserved and the
	// text stays fixed.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if n, err := st.Create(ctx, fileRefOf(t, 1001, "repoJ", "", "a.bin", meta)); err == nil || n != 0 {
		t.Fatalf("locked Create = (%d, %v), want (0, error)", n, err)
	} else {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("locked Create must preserve the deadline cause, got %v", err)
		}
		if s := err.Error(); s != "postgres nodes: create file reference" {
			t.Fatalf("Error() = %q, want fixed create stage text", s)
		}
	}
	// 语句被取消后连接由 database/sql 关闭，显式回滚可能报 conn closed：
	// 事务已随连接终结（锁亦释放），释放正确性由下方持锁回滚后的新事务
	// 成功证明；兜底回滚仍由 t.Cleanup 保证。
	// After the statement cancellation database/sql closes the connection,
	// so the explicit rollback may report conn closed: the transaction is
	// already finished (and its locks released); correctness is proven by
	// the fresh transaction succeeding after the holder rolls back, and
	// the t.Cleanup rollback remains the safety net.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		t.Logf("rollback after cancellation: %v (transaction already finalized)", err)
	}
	// 释放锁后，新事务成功。
	// After releasing the lock, a fresh transaction succeeds.
	if err := lockTx.Rollback(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	tx2, st2 := nodeTx(t, db)
	okCtx, okCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer okCancel()
	if n, err := st2.Create(okCtx, fileRefOf(t, 1001, "repoJ", "", "a.bin", meta)); err != nil || n != 1 {
		t.Fatalf("post-lock Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := countNodes(t, adm, schema); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
}

func TestNodeFailureKeepsBinaryAndRetrySucceeds(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := context.Background()
	reg, err := nodereg.New(db)
	if err != nil {
		t.Fatalf("registry New: %v", err)
	}
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	svc, err := binary.New(store, reg)
	if err != nil {
		t.Fatalf("binary.New: %v", err)
	}

	payload := "binflow node reference integration payload"
	staged, err := content.Calculate(ctx, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("content.Calculate: %v", err)
	}
	// binary.Service 先提交内容与登记（与节点事务相互独立）。
	// binary.Service commits content and registration first,
	// independently of the node transaction.
	if _, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{}); err != nil {
		t.Fatalf("svc.Add: %v", err)
	}
	if _, _, _, _, ok := rowOf(t, adm, schema, staged.SHA1); !ok {
		t.Fatal("binary registration row missing after Add")
	}

	// 节点事务失败（未知 SHA-1 触发 FK）并回滚：字节与登记行都保留，无补偿。
	// The node transaction fails (an unknown SHA-1 trips the FK) and the
	// caller rolls back: bytes and the registry row survive, with no
	// compensation.
	tx, st := nodeTx(t, db)
	badRef := fileRefOf(t, 1101, "repoK", "dir", "a.bin", staged)
	badRef.Content = content.Metadata{SHA1: strings.Repeat("f", 40), SHA256: staged.SHA256, MD5: staged.MD5, Size: staged.Size}
	if _, err := st.Create(ctx, badRef); err == nil {
		t.Fatal("node Create must fail against a missing binary")
	} else if code := pgCodeOf(t, err); code != "23503" {
		t.Fatalf("node FK SQLSTATE = %s, want 23503", code)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	obj, err := store.Open(ctx, staged.SHA1)
	if err != nil {
		t.Fatalf("Open after node failure: %v", err)
	}
	gotBytes, readErr := io.ReadAll(obj)
	obj.Close()
	if readErr != nil || string(gotBytes) != payload {
		t.Fatalf("bytes not preserved: readErr=%v len=%d", readErr, len(gotBytes))
	}
	if _, _, _, _, ok := rowOf(t, adm, schema, staged.SHA1); !ok {
		t.Fatal("registry row lost after node rollback")
	}
	if got := countNodes(t, adm, schema); got != 0 {
		t.Fatalf("node count = %d, want 0", got)
	}

	// 换新读取器重试：对象复用（同一 SHA-1 可 Open），新节点事务成功。
	// A fresh-reader retry: the object is reused (the same SHA-1 stays
	// Open-able) and a new node transaction succeeds.
	if _, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{}); err != nil {
		t.Fatalf("svc.Add retry: %v", err)
	}
	tx2, st2 := nodeTx(t, db)
	if n, err := st2.Create(ctx, fileRefOf(t, 1101, "repoK", "dir", "a.bin", staged)); err != nil || n != 1 {
		t.Fatalf("retry node Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("retry node commit: %v", err)
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoK", "dir", "a.bin", staged).Path)
	if err != nil || got.ID != 1101 || got.Content != staged {
		t.Fatalf("post-retry Lookup = (%+v, %v), want id 1101 with staged meta", got, err)
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

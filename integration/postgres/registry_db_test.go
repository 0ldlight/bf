// 真实 PostgreSQL 场景验证：插入/独立查询、零字节、既有记录复用、NULL 与
// Java-blank SHA-256 回填、NBSP 保留、并发一致性、查询失败回滚、COMMIT
// 失败零结果与锁等待取消。
// Real-PostgreSQL scenarios: insert/independent find, zero bytes, stored
// reuse, NULL and Java-blank SHA-256 backfill, NBSP preservation,
// concurrent identity, query-failure rollback, COMMIT-failure zero result
// and lock-wait cancellation.
package postgresit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0ldlight/bf/internal/content"
)

func metaOf(sha1, sha256, md5 string, size int64) content.Metadata {
	return content.Metadata{SHA1: sha1, SHA256: sha256, MD5: md5, Size: size}
}

func TestRegisterInsertsAndIndependentFindVerifiesColumns(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	meta := metaOf(strings.Repeat("1", 40), strings.Repeat("2", 64), strings.Repeat("3", 32), 42)

	got, err := reg.Register(context.Background(), meta)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got != meta {
		t.Fatalf("fresh Register returned %+v, want incoming %+v", got, meta)
	}
	sha1, md5, length, sha256, ok := rowOf(t, adm, schema, meta.SHA1)
	if !ok {
		t.Fatal("independent SQL must find the inserted row")
	}
	if sha1 != meta.SHA1 || md5 != meta.MD5 || length != 42 || !sha256.Valid || sha256.String != meta.SHA256 {
		t.Fatalf("stored row = (%q %q %d %v), want canonical columns", sha1, md5, length, sha256)
	}
	if n := countRows(t, adm, schema); n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}

func TestRegisterZeroByteSize(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	// 空串的规范摘要、Size 0 是合法的最小输入。
	// Canonical digests of the empty string with Size 0 are valid input.
	meta, err := content.Calculate(context.Background(), strings.NewReader(""))
	if err != nil {
		t.Fatalf("content.Calculate(empty): %v", err)
	}
	got, err := reg.Register(context.Background(), meta)
	if err != nil {
		t.Fatalf("Register empty content: %v", err)
	}
	if got != meta || got.Size != 0 {
		t.Fatalf("zero-byte Register returned %+v, want %+v", got, meta)
	}
	_, _, length, _, ok := rowOf(t, adm, schema, meta.SHA1)
	if !ok || length != 0 {
		t.Fatalf("stored bin_length = %d (found=%v), want 0", length, ok)
	}
}

func TestRegisterReturnsStoredMetadataWithoutOverwrite(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	ctx := context.Background()
	first := metaOf(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 32), 7)
	if _, err := reg.Register(ctx, first); err != nil {
		t.Fatalf("seed Register: %v", err)
	}
	// 同 SHA-1 的历史记录：不同 MD5/长度，非空白 SHA-256；Register 不得覆盖。
	// A historical row with the same SHA-1 but different MD5/length and a
	// nonblank SHA-256 must survive Register untouched.
	_, err := adm.ExecContext(ctx, fmt.Sprintf(
		"UPDATE %s.binaries SET md5 = $1, bin_length = $2, sha256 = $3 WHERE sha1 = $4", schema),
		strings.Repeat("d", 32), int64(999), strings.Repeat("e", 64), first.SHA1)
	if err != nil {
		t.Fatalf("seed historical values: %v", err)
	}
	incoming := metaOf(first.SHA1, strings.Repeat("f", 64), strings.Repeat("9", 32), 1)
	got, err := reg.Register(ctx, incoming)
	if err != nil {
		t.Fatalf("Register against stored row: %v", err)
	}
	want := metaOf(first.SHA1, strings.Repeat("e", 64), strings.Repeat("d", 32), 999)
	if got != want {
		t.Fatalf("Register returned %+v, want stored %+v", got, want)
	}
	_, md5, length, sha256, _ := rowOf(t, adm, schema, first.SHA1)
	if md5 != want.MD5 || length != 999 || sha256.String != want.SHA256 {
		t.Fatalf("stored row was overwritten: (%q %d %v)", md5, length, sha256)
	}
}

func TestRegisterBackfillsNullSHA256Only(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	ctx := context.Background()
	// 历史行：SHA-256 为 NULL，MD5/长度与输入不同。
	// Historical row with NULL SHA-256 and differing MD5/length.
	hist := metaOf(strings.Repeat("a", 40), "ignored", strings.Repeat("c", 32), 12)
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO %s.binaries (sha1, md5, bin_length, sha256) VALUES ($1, $2, $3, NULL)", schema),
		hist.SHA1, hist.MD5, hist.Size); err != nil {
		t.Fatalf("seed NULL-sha256 row: %v", err)
	}
	incoming := metaOf(hist.SHA1, strings.Repeat("f", 64), strings.Repeat("9", 32), 1)
	got, err := reg.Register(ctx, incoming)
	if err != nil {
		t.Fatalf("Register backfill: %v", err)
	}
	want := metaOf(hist.SHA1, incoming.SHA256, hist.MD5, hist.Size)
	if got != want {
		t.Fatalf("Register returned %+v, want backfilled %+v", got, want)
	}
	_, md5, length, sha256, _ := rowOf(t, adm, schema, hist.SHA1)
	if md5 != hist.MD5 || length != hist.Size {
		t.Fatalf("backfill touched md5/length: (%q %d)", md5, length)
	}
	if !sha256.Valid || sha256.String != incoming.SHA256 {
		t.Fatalf("sha256 not backfilled: %v", sha256)
	}
}

func TestRegisterBackfillsJavaBlankSHA256Only(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	ctx := context.Background()
	hist := metaOf(strings.Repeat("b", 40), "ignored", strings.Repeat("c", 32), 5)
	// 存量 Java-blank SHA-256（空格+制表+回车混合）按缺失处理。
	// A stored Java-blank SHA-256 (mixed spaces/tabs/returns) counts as
	// missing.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO %s.binaries (sha1, md5, bin_length, sha256) VALUES ($1, $2, $3, $4)", schema),
		hist.SHA1, hist.MD5, hist.Size, " \t\r "); err != nil {
		t.Fatalf("seed blank-sha256 row: %v", err)
	}
	incoming := metaOf(hist.SHA1, strings.Repeat("f", 64), strings.Repeat("9", 32), 1)
	got, err := reg.Register(ctx, incoming)
	if err != nil {
		t.Fatalf("Register blank backfill: %v", err)
	}
	want := metaOf(hist.SHA1, incoming.SHA256, hist.MD5, hist.Size)
	if got != want {
		t.Fatalf("Register returned %+v, want backfilled %+v", got, want)
	}
	_, md5, length, sha256, _ := rowOf(t, adm, schema, hist.SHA1)
	if md5 != hist.MD5 || length != hist.Size {
		t.Fatalf("blank backfill touched md5/length: (%q %d)", md5, length)
	}
	if !sha256.Valid || sha256.String != incoming.SHA256 {
		t.Fatalf("sha256 not backfilled over blank: %v", sha256)
	}
}

func TestRegisterPreservesNBSPSHA256AsNonBlank(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	ctx := context.Background()
	hist := metaOf(strings.Repeat("d", 40), "ignored", strings.Repeat("c", 32), 8)
	// NBSP 对 Java Character.isWhitespace 非空白：既不回填也不改写。
	// NBSP is nonblank for Java Character.isWhitespace: no backfill, no
	// rewrite.
	const nbsp = "\u00a0"
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO %s.binaries (sha1, md5, bin_length, sha256) VALUES ($1, $2, $3, $4)", schema),
		hist.SHA1, hist.MD5, hist.Size, nbsp); err != nil {
		t.Fatalf("seed NBSP-sha256 row: %v", err)
	}
	incoming := metaOf(hist.SHA1, strings.Repeat("f", 64), strings.Repeat("9", 32), 1)
	got, err := reg.Register(ctx, incoming)
	if err != nil {
		t.Fatalf("Register against NBSP row: %v", err)
	}
	want := metaOf(hist.SHA1, nbsp, hist.MD5, hist.Size)
	if got != want {
		t.Fatalf("Register returned %+v, want stored %+v", got, want)
	}
	_, _, _, sha256, _ := rowOf(t, adm, schema, hist.SHA1)
	if !sha256.Valid || sha256.String != nbsp {
		t.Fatalf("NBSP sha256 must be preserved verbatim: %v", sha256)
	}
}

func TestConcurrentIdenticalRegistrationsLeaveOneRow(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	meta := metaOf(strings.Repeat("c", 40), strings.Repeat("d", 64), strings.Repeat("e", 32), 100)
	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	results := make([]content.Metadata, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			results[i], errs[i] = reg.Register(ctx, meta)
		}(i)
	}
	wg.Wait()
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if results[i] != meta {
			t.Fatalf("worker %d returned %+v, want %+v", i, results[i], meta)
		}
	}
	if n := countRows(t, adm, schema); n != 1 {
		t.Fatalf("row count after concurrency = %d, want 1", n)
	}
}

func TestQueryFailureFailsWithFixedStageAndNoRow(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	ctx := context.Background()
	// 移走表让 SELECT 失败：确定性 SQL 错误路径。
	// Removing the table fails the SELECT: a deterministic SQL error path.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"ALTER TABLE %s.binaries RENAME TO binaries_off", schema)); err != nil {
		t.Fatalf("rename table: %v", err)
	}
	t.Cleanup(func() {
		renameCtx, renameCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer renameCancel()
		if _, err := adm.ExecContext(renameCtx, fmt.Sprintf(
			"ALTER TABLE %s.binaries_off RENAME TO binaries", schema)); err != nil {
			t.Errorf("restore table: %v", err)
		}
	})
	got, err := reg.Register(ctx, metaOf(strings.Repeat("a", 40), strings.Repeat("b", 64), strings.Repeat("c", 32), 1))
	if err == nil {
		t.Fatal("Register must fail when the query fails")
	}
	if got != (content.Metadata{}) {
		t.Fatalf("failed Register returned %+v, want zero Metadata", got)
	}
	if s := err.Error(); s != "postgres registry: query existing record" {
		t.Fatalf("Error() = %q, want fixed query stage text", s)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("driver cause must stay reachable via Unwrap, got %v", err)
	}
}

func TestCommitFailureReturnsZeroAndLeavesNoRow(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	ctx := context.Background()
	// 自有的确定性延迟约束触发器：INSERT 成功、COMMIT 失败。
	// An owned deterministic deferred constraint trigger: the INSERT
	// succeeds and the COMMIT fails.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"CREATE FUNCTION %s.bfreg_block() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'deferred test constraint'; END $$ LANGUAGE plpgsql", schema)); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"CREATE CONSTRAINT TRIGGER bfreg_block AFTER INSERT ON %s.binaries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s.bfreg_block()", schema, schema)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	meta := metaOf(strings.Repeat("1", 40), strings.Repeat("2", 64), strings.Repeat("3", 32), 9)
	got, err := reg.Register(ctx, meta)
	if err == nil {
		t.Fatal("Register must fail when COMMIT fails")
	}
	if got != (content.Metadata{}) {
		t.Fatalf("commit-failed Register returned %+v, want zero Metadata", got)
	}
	if s := err.Error(); s != "postgres registry: commit" {
		t.Fatalf("Error() = %q, want fixed commit stage text", s)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("driver cause must stay reachable via Unwrap, got %v", err)
	}
	if _, _, _, _, ok := rowOf(t, adm, schema, meta.SHA1); ok {
		t.Fatal("no row may survive a failed COMMIT")
	}
	// 移除自有注入后重试成功：证明失败来自注入约束而非内容。
	// Removing the owned injection lets the retry succeed: the failure was
	// the injected constraint, not the content.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER bfreg_block ON %s.binaries", schema)); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	got, err = reg.Register(ctx, meta)
	if err != nil {
		t.Fatalf("Register after removing constraint: %v", err)
	}
	if got != meta {
		t.Fatalf("retry returned %+v, want %+v", got, meta)
	}
}

func TestLockWaitCancellationPreservesCause(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	meta := metaOf(strings.Repeat("9", 40), strings.Repeat("8", 64), strings.Repeat("7", 32), 3)
	// 在 admin 池（与 registry 池相互独立）上同步取 ACCESS EXCLUSIVE 锁：
	// 无持锁 goroutine/通道；defer Rollback 保证任何 t.Fatal 都释放锁，
	// Begin/Exec 均带有限时 context，不存在无限等待。
	// Take the ACCESS EXCLUSIVE lock synchronously on the admin pool
	// (independent of the registry pool): no holder goroutine or channels;
	// the deferred Rollback releases the lock even on t.Fatal, and both
	// Begin and Exec carry bounded contexts, so nothing can wait forever.
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer lockCancel()
	lockTx, err := adm.BeginTx(lockCtx, nil)
	if err != nil {
		t.Fatalf("lock tx begin: %v", err)
	}
	defer lockTx.Rollback()
	if _, err := lockTx.ExecContext(lockCtx, fmt.Sprintf("LOCK TABLE %s.binaries IN ACCESS EXCLUSIVE MODE", schema)); err != nil {
		t.Fatalf("lock table: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, err := reg.Register(ctx, meta)
	if err == nil {
		t.Fatal("Register must fail while the table lock is held")
	}
	if got != (content.Metadata{}) {
		t.Fatalf("lock-wait Register returned %+v, want zero Metadata", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error must preserve the deadline cause, got %v", err)
	}
	// 显式回滚释放锁，再验证同一登记成功；同步仅依赖锁的生命周期。
	// Rollback explicitly to release the lock, then verify the same
	// registration succeeds; synchronization rides on the lock lifetime
	// only, never timing.
	if err := lockTx.Rollback(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	okCtx, okCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer okCancel()
	if _, err := reg.Register(okCtx, meta); err != nil {
		t.Fatalf("Register after lock release: %v", err)
	}
}

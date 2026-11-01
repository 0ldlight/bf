// ReadPool 的确定性单元回归（先行 RED）：用 std database/sql 假驱动复现
// BIN-204 CI（ci-postgres-attempt1.json）的 read_pool_deadline 失败——查询
// 以零行完成（Store.Lookup 得到 NoRows）后，驱动事务的 Rollback 在拿到
// opCtx 截止后才返回独立 sentinel 回滚错误；此时 database/sql 事务已被
// 手动 Rollback CAS 置 done，自动回滚只能看到 ErrTxDone，于是旧实现先
// 返回回滚错误、丢掉 opCtx.Err()，errors.Is(context.DeadlineExceeded)
// 被屏蔽。配套守卫：opCtx 活跃时回滚失败不得伪造 deadline、也不得把被
// 丢弃的 NoRows 并入回滚错误（下载层把 sql.ErrNoRows 分类为 404）；正常
// NoRows/成功/查询期截止竞争路径回归。真实 SQL 行为归 integration 门禁。
// Deterministic unit regression for the ReadPool, written first (RED): a std
// database/sql fake driver reproduces the BIN-204 CI read_pool_deadline
// failure (ci-postgres-attempt1.json) — the query completes with zero rows
// (Store.Lookup sees NoRows), then the driver transaction's Rollback returns
// an independent sentinel rollback error only after the opCtx deadline it
// captured at BeginTx fires; by then the database/sql transaction has been
// CAS-marked done by the manual Rollback, so the automatic rollback only ever
// sees ErrTxDone, and the old implementation returns the rollback error first
// and drops opCtx.Err(), masking errors.Is(context.DeadlineExceeded). Guard
// cases: with opCtx alive a rollback failure must not fabricate a deadline and
// must not join the discarded NoRows cause (the download layer classifies
// sql.ErrNoRows as 404); the normal NoRows/success/query-deadline race paths
// regress too. Real SQL behavior belongs to the integration gate.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/repository"
)

// errRPRollbackFailed 是假驱动的独立回滚 sentinel：与 context 错误无关，
// 保证 errors.Is 在旧/新实现下都可判；文本固定，不含任何输入。
// errRPRollbackFailed is the fake driver's independent rollback sentinel:
// unrelated to any context error so errors.Is stays decidable under both the
// old and fixed implementation; the text is fixed and input-free.
var errRPRollbackFailed = errors.New("read pool fake: rollback failed")

// rpKnobs 是假驱动的故障面板：计数器（begin/rollback/commit/只读请求）
// 证明恰一次回滚、零提交；故障开关在 sql.Open 前设置后不再变更。
// rpKnobs is the fake driver's fault panel: the counters (begin/rollback/
// commit/read-only requests) prove exactly one rollback and zero commits;
// every fault switch is set before sql.Open and never mutated afterwards.
type rpKnobs struct {
	mu                sync.Mutex
	begins            int
	rollbacks         int
	commits           int
	readOnlyBegins    int
	beginCtx          context.Context
	rollbackCompleted chan struct{}

	rollbackErr      error            // Rollback 返回的 sentinel（nil=成功）
	rollbackWaits    bool             // Rollback 先等 BeginTx 拿到的 opCtx Done
	queryWaitsForCtx bool             // QueryContext 阻塞到自身 ctx Done 后返回 ctx.Err()
	rows             [][]driver.Value // nil=零行（NoRows）；否则逐行返回
}

func (k *rpKnobs) counts() (begins, rollbacks, commits, readOnly int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.begins, k.rollbacks, k.commits, k.readOnlyBegins
}

// awaitRollback 等待手动或自动驱动回滚完成；定时器仅用于防止测试挂死。
// awaitRollback waits for manual or automatic driver cleanup to finish;
// the timer is only a test watchdog, not synchronization by elapsed time.
func (k *rpKnobs) awaitRollback(t *testing.T) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-k.rollbackCompleted:
	case <-timer.C:
		t.Fatal("driver rollback did not complete")
	}
}

const rpDriverName = "readpool_fault"

var (
	rpSetsMu sync.Mutex
	rpSeq    int
	rpSets   = map[string]*rpKnobs{}
)

func init() { sql.Register(rpDriverName, rpDriver{}) }

// rpOpenDB 注册一次性 knob 集并返回未触库的 *sql.DB；t.Cleanup 关闭并
// 清理注册项（sql.Open 本身不拨号）。
// rpOpenDB registers a one-shot knob set and returns a *sql.DB that has not
// connected (sql.Open itself never dials); t.Cleanup closes and unregisters.
func rpOpenDB(t *testing.T, knobs *rpKnobs) *sql.DB {
	t.Helper()
	knobs.rollbackCompleted = make(chan struct{})
	rpSetsMu.Lock()
	defer rpSetsMu.Unlock()
	rpSeq++
	name := fmt.Sprintf("%s/%d", t.Name(), rpSeq)
	if _, dup := rpSets[name]; dup {
		t.Fatalf("read pool fake knob set %q already registered", name)
	}
	rpSets[name] = knobs
	db, err := sql.Open(rpDriverName, name)
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", rpDriverName, err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		rpSetsMu.Lock()
		delete(rpSets, name)
		rpSetsMu.Unlock()
	})
	return db
}

type rpDriver struct{}

func (rpDriver) Open(name string) (driver.Conn, error) {
	rpSetsMu.Lock()
	knobs := rpSets[name]
	rpSetsMu.Unlock()
	if knobs == nil {
		return nil, fmt.Errorf("read pool fake: unknown knob set %q", name)
	}
	return &rpConn{knobs: knobs}, nil
}

// rpConn 只实现 ConnBeginTx + QueryerContext：Prepare/Begin 一旦被走到即
// 固定报错，证明读取路径不落_prepare_、不走旧 Begin。
// rpConn implements only ConnBeginTx + QueryerContext: reaching Prepare or
// the legacy Begin fails with a fixed error, proving the read path never
// prepares and never uses the old Begin.
type rpConn struct{ knobs *rpKnobs }

func (c *rpConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("read pool fake: Prepare must never run; conn implements QueryContext")
}

func (c *rpConn) Close() error { return nil }

func (c *rpConn) Begin() (driver.Tx, error) {
	return nil, errors.New("read pool fake: Begin must never run; conn implements BeginTx")
}

// BeginTx 记录调用方 ctx（即 ReadPool 的 opCtx：database/sql 在派生取消
// ctx 之前把原始 ctx 传给驱动）与只读请求计数。
// BeginTx records the caller ctx (the ReadPool's opCtx: database/sql passes
// the original ctx to the driver before deriving the cancelable one) plus
// the read-only request count.
func (c *rpConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.knobs.mu.Lock()
	defer c.knobs.mu.Unlock()
	c.knobs.begins++
	c.knobs.beginCtx = ctx
	if opts.ReadOnly {
		c.knobs.readOnlyBegins++
	}
	return &rpTx{knobs: c.knobs}, nil
}

func (c *rpConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.knobs.queryWaitsForCtx {
		// 模拟真实驱动在依赖截止时中止在途查询：等自身 ctx Done 后原样
		// 上抛 ctx.Err()。
		// Model a real driver aborting an in-flight query on the dependency
		// deadline: wait for this ctx Done, then surface ctx.Err() verbatim.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &rpRows{rows: c.knobs.rows}, nil
}

// rpTx 的 Rollback 是本回归的支点：rollbackWaits 时先等 BeginTx 拿到的
// opCtx Done（父 ctx 保持活跃，Done 只能来自截止；手动 Rollback 取消的
// 是 database/sql 派生的子 ctx，不影响它），截止确实发生后才返回独立
// sentinel——此刻 sql.Tx 已被手动 Rollback CAS 置 done，自动回滚只能
// ErrTxDone，稳定暴露旧实现屏蔽 deadline。2 秒保险上限防挂死。
// rpTx.Rollback is the pivot of this regression: with rollbackWaits it first
// waits for the opCtx Done captured at BeginTx (the parent stays alive, so
// Done can only come from the deadline; the manual Rollback cancels
// database/sql's derived child ctx, not this one) and only after the
// deadline really fired returns the independent sentinel — at that moment
// the sql.Tx has been CAS-marked done by the manual Rollback so the
// automatic rollback only sees ErrTxDone, stably exposing the old
// implementation masking the deadline. A 2s guard cap prevents hangs.
type rpTx struct{ knobs *rpKnobs }

func (tx *rpTx) Commit() error {
	tx.knobs.mu.Lock()
	tx.knobs.commits++
	tx.knobs.mu.Unlock()
	return errors.New("read pool fake: Commit must never be called")
}

func (tx *rpTx) Rollback() error {
	defer close(tx.knobs.rollbackCompleted)
	tx.knobs.mu.Lock()
	tx.knobs.rollbacks++
	ctx := tx.knobs.beginCtx
	tx.knobs.mu.Unlock()
	if tx.knobs.rollbackWaits && ctx != nil {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			return errors.New("read pool fake: rollback deadline gate timed out")
		}
	}
	return tx.knobs.rollbackErr
}

// rpRows 按序返回预设行；列清单与 lookupFile 的 SELECT 完全一致，零行
// 即 NoRows。
// rpRows serves the preset rows in order; the column list matches lookupFile's
// SELECT exactly, and zero rows means NoRows.
type rpRows struct {
	rows [][]driver.Value
	i    int
}

func (r *rpRows) Columns() []string {
	return []string{"node_id", "node_type", "repo", "node_path", "node_name",
		"bin_length", "sha1_actual", "md5_actual", "sha256", "created",
		"modified", "updated", "created_by", "modified_by",
		"sha1_original", "md5_original"}
}

func (r *rpRows) Close() error { return nil }

func (r *rpRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// rpPath 返回一个合法读取路径；假驱动不检查查询参数。
// rpPath returns one valid read path; the fake driver never inspects query
// arguments.
func rpPath(t *testing.T) repository.Path {
	t.Helper()
	p, err := repository.NewPath("repo-r", "dir", "file.bin", true)
	if err != nil {
		t.Fatalf("NewPath: %v", err)
	}
	return p
}

// rpAliveParent 返回未被取消的可控父 ctx：依赖子截止不得被误当成父
// HTTP 取消，父ctx 在整个调用期间必须保持活跃。
// rpAliveParent returns a controllable parent ctx that is never cancelled
// during the call: a dependency's sub-deadline must not be mistaken for the
// parent HTTP cancellation, so the parent has to stay alive throughout.
func rpAliveParent() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// TestReadPoolRollbackFailureMustNotMaskDeadline 是 BIN-204 CI 复现的
// 核心 RED：查询零行完成（NoRows 已产生）后回滚在依赖截止后才失败——
// 错误必须同时让独立回滚 sentinel 与 context.DeadlineExceeded 可达，且
// 不得误报取消、零 Source、固定安全文案、恰一次回滚/零提交/只读事务。
// TestReadPoolRollbackFailureMustNotMaskDeadline is the core RED of the
// BIN-204 CI repro: the query completes with zero rows (NoRows already
// produced) and the rollback fails only after the dependency deadline — the
// error must keep both the independent rollback sentinel and
// context.DeadlineExceeded reachable, never report a cancellation, return a
// zero Source with the fixed safe text, exactly one rollback, zero commits
// and a read-only transaction.
func TestReadPoolRollbackFailureMustNotMaskDeadline(t *testing.T) {
	knobs := &rpKnobs{rollbackErr: errRPRollbackFailed, rollbackWaits: true}
	db := rpOpenDB(t, knobs)
	pool, err := NewReadPool(db, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("NewReadPool: %v", err)
	}
	parent, stop := rpAliveParent()
	defer stop()
	src, err := pool.LookupSnapshotSource(parent, rpPath(t))
	if err == nil {
		t.Fatal("LookupSnapshotSource = nil error, want rollback failure")
	}
	if src != (repository.FileSnapshotSource{}) {
		t.Fatalf("LookupSnapshotSource source = %+v, want zero Source", src)
	}
	if got := err.Error(); got != stagePoolRollback {
		t.Fatalf("Error() = %q, want fixed safe text %q", got, stagePoolRollback)
	}
	if !errors.Is(err, errRPRollbackFailed) {
		t.Fatalf("error must keep the rollback sentinel reachable: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dependency deadline masked by rollback failure: %v", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("rollback failure must not become missing content")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("dependency deadline must not masquerade as cancellation: %v", err)
	}
	if perr := parent.Err(); perr != nil {
		t.Fatalf("parent context must stay alive: %v", perr)
	}
	begins, rollbacks, commits, readOnly := knobs.counts()
	if begins != 1 || rollbacks != 1 || commits != 0 || readOnly != 1 {
		t.Fatalf("driver counts = begin %d, rollback %d, commit %d, read-only %d; want 1/1/0/1",
			begins, rollbacks, commits, readOnly)
	}
}

// TestReadPoolRollbackFailureWithoutDeadlineStaysRollbackError 守卫修复
// 不过度：opCtx 仍活跃、仅回滚失败时不得伪造 deadline，也不得把被丢弃
// 的 NoRows 查询原因并入（下载层把 sql.ErrNoRows 分类为 404，回滚失败
// 必须保持存储失败归因）。
// TestReadPoolRollbackFailureWithoutDeadlineStaysRollbackError guards the
// fix against over-correction: with opCtx still alive and only the rollback
// failing, no deadline may be fabricated and the discarded NoRows lookup
// cause must not be joined either (the download layer classifies
// sql.ErrNoRows as 404 while a rollback failure must stay a storage
// failure).
func TestReadPoolRollbackFailureWithoutDeadlineStaysRollbackError(t *testing.T) {
	knobs := &rpKnobs{rollbackErr: errRPRollbackFailed}
	db := rpOpenDB(t, knobs)
	pool, err := NewReadPool(db, poolTimeoutMax)
	if err != nil {
		t.Fatalf("NewReadPool: %v", err)
	}
	parent, stop := rpAliveParent()
	defer stop()
	src, err := pool.LookupSnapshotSource(parent, rpPath(t))
	if err == nil {
		t.Fatal("LookupSnapshotSource = nil error, want rollback failure")
	}
	if src != (repository.FileSnapshotSource{}) {
		t.Fatalf("LookupSnapshotSource source = %+v, want zero Source", src)
	}
	if got := err.Error(); got != stagePoolRollback {
		t.Fatalf("Error() = %q, want fixed safe text %q", got, stagePoolRollback)
	}
	if !errors.Is(err, errRPRollbackFailed) {
		t.Fatalf("error must keep the rollback sentinel reachable: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("no deadline fired, one must not be fabricated: %v", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("discarded NoRows lookup cause must stay masked by the rollback failure: %v", err)
	}
	if perr := parent.Err(); perr != nil {
		t.Fatalf("parent context must stay alive: %v", perr)
	}
	begins, rollbacks, commits, readOnly := knobs.counts()
	if begins != 1 || rollbacks != 1 || commits != 0 || readOnly != 1 {
		t.Fatalf("driver counts = begin %d, rollback %d, commit %d, read-only %d; want 1/1/0/1",
			begins, rollbacks, commits, readOnly)
	}
}

// TestReadPoolNoRowsAfterCleanRollback 回归正常缺失路径：回滚成功时
// NoRows 原因链原样透传（阶段文案不变），零 Source、恰一次回滚。
// TestReadPoolNoRowsAfterCleanRollback regresses the normal missing path:
// with a clean rollback the NoRows cause chain passes through unchanged
// (same stage text), with a zero Source and exactly one rollback.
func TestReadPoolNoRowsAfterCleanRollback(t *testing.T) {
	knobs := &rpKnobs{}
	db := rpOpenDB(t, knobs)
	pool, err := NewReadPool(db, poolTimeoutMax)
	if err != nil {
		t.Fatalf("NewReadPool: %v", err)
	}
	parent, stop := rpAliveParent()
	defer stop()
	src, err := pool.LookupSnapshotSource(parent, rpPath(t))
	if err == nil {
		t.Fatal("LookupSnapshotSource = nil error, want NoRows")
	}
	if src != (repository.FileSnapshotSource{}) {
		t.Fatalf("LookupSnapshotSource source = %+v, want zero Source", src)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error must keep sql.ErrNoRows reachable: %v", err)
	}
	if got := err.Error(); got != stageLookup {
		t.Fatalf("Error() = %q, want unchanged lookup stage text %q", got, stageLookup)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("clean NoRows path must not carry a deadline: %v", err)
	}
	begins, rollbacks, commits, _ := knobs.counts()
	if begins != 1 || rollbacks != 1 || commits != 0 {
		t.Fatalf("driver counts = begin %d, rollback %d, commit %d; want 1/1/0",
			begins, rollbacks, commits)
	}
}

// TestReadPoolSuccessRollsBackOnce 回归成功路径：单行合法投影返回非零
// Source，随后恰一次回滚、零提交。
// TestReadPoolSuccessRollsBackOnce regresses the success path: one valid
// row yields a nonzero Source followed by exactly one rollback and zero
// commits.
func TestReadPoolSuccessRollsBackOnce(t *testing.T) {
	knobs := &rpKnobs{rows: [][]driver.Value{{
		int64(7), int64(1), "repo-r", "dir", "file.bin", int64(11),
		"sha1", "md5", "sha256", int64(1000), int64(2000),
		nil, nil, nil, nil, nil,
	}}}
	db := rpOpenDB(t, knobs)
	pool, err := NewReadPool(db, poolTimeoutMax)
	if err != nil {
		t.Fatalf("NewReadPool: %v", err)
	}
	parent, stop := rpAliveParent()
	defer stop()
	src, err := pool.LookupSnapshotSource(parent, rpPath(t))
	if err != nil {
		t.Fatalf("LookupSnapshotSource: %v", err)
	}
	if src.Reference.ID != 7 || !src.SHA1ActualValid || src.Reference.Content.SHA256 != "sha256" {
		t.Fatalf("LookupSnapshotSource source = %+v, want the scanned row projection", src)
	}
	begins, rollbacks, commits, readOnly := knobs.counts()
	if begins != 1 || rollbacks != 1 || commits != 0 || readOnly != 1 {
		t.Fatalf("driver counts = begin %d, rollback %d, commit %d, read-only %d; want 1/1/0/1",
			begins, rollbacks, commits, readOnly)
	}
}

// TestReadPoolQueryDeadlineSurfacesAsStorageFailure 回归查询期截止的
// 自动回滚竞争：查询阻塞到依赖截止后失败，手动/自动回滚恰有一个走到
// 驱动；截止以普通存储错误上抛（deadline 可达、非取消），父 ctx 活跃。
// TestReadPoolQueryDeadlineSurfacesAsStorageFailure regresses the
// query-time deadline with the automatic-rollback race: the query blocks
// until the dependency deadline and fails, exactly one of the manual or
// automatic rollbacks reaches the driver, and the deadline surfaces as an
// ordinary storage error (deadline reachable, no cancellation) while the
// parent stays alive.
func TestReadPoolQueryDeadlineSurfacesAsStorageFailure(t *testing.T) {
	knobs := &rpKnobs{queryWaitsForCtx: true}
	db := rpOpenDB(t, knobs)
	pool, err := NewReadPool(db, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("NewReadPool: %v", err)
	}
	parent, stop := rpAliveParent()
	defer stop()
	src, err := pool.LookupSnapshotSource(parent, rpPath(t))
	if err == nil {
		t.Fatal("LookupSnapshotSource = nil error, want deadline failure")
	}
	if src != (repository.FileSnapshotSource{}) {
		t.Fatalf("LookupSnapshotSource source = %+v, want zero Source", src)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dependency deadline must stay reachable: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("dependency deadline must not masquerade as cancellation: %v", err)
	}
	if got := err.Error(); got != stagePoolRollback {
		t.Fatalf("Error() = %q, want fixed safe text %q", got, stagePoolRollback)
	}
	if errors.Is(err, errRPRollbackFailed) {
		t.Fatalf("no rollback fault was armed, the fake sentinel must not appear: %v", err)
	}
	if perr := parent.Err(); perr != nil {
		t.Fatalf("parent context must stay alive: %v", perr)
	}
	// 手动/自动回滚恰有一个走到驱动，且可能在函数返回后才落地。
	// Exactly one of the manual or automatic rollbacks reaches the driver,
	// possibly only after the call has returned.
	knobs.awaitRollback(t)
	begins, rollbacks, commits, _ := knobs.counts()
	if begins != 1 || rollbacks != 1 || commits != 0 {
		t.Fatalf("driver counts = begin %d, rollback %d, commit %d; want exactly 1/1/0",
			begins, rollbacks, commits)
	}
}

// TestNewReadPoolRejectsInvalidConfiguration 回归构造前置：nil db 与越界
// 超时固定返回 ErrInvalidReadPool，合法参数不触库即成功。
// TestNewReadPoolRejectsInvalidConfiguration regresses the constructor
// preconditions: a nil db and out-of-range timeouts return the fixed
// ErrInvalidReadPool, while valid arguments succeed without touching the
// database.
func TestNewReadPoolRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewReadPool(nil, time.Second); !errors.Is(err, ErrInvalidReadPool) {
		t.Fatalf("NewReadPool(nil db) = %v, want ErrInvalidReadPool", err)
	}
	knobs := &rpKnobs{}
	db := rpOpenDB(t, knobs)
	for name, timeout := range map[string]time.Duration{
		"zero":     0,
		"negative": -time.Second,
		"too big":  poolTimeoutMax + time.Millisecond,
	} {
		if _, err := NewReadPool(db, timeout); !errors.Is(err, ErrInvalidReadPool) {
			t.Fatalf("%s: NewReadPool(non-nil db, %v) = %v, want ErrInvalidReadPool", name, timeout, err)
		}
	}
	for _, timeout := range []time.Duration{time.Nanosecond, poolTimeoutMax} {
		if _, err := NewReadPool(db, timeout); err != nil {
			t.Fatalf("NewReadPool(valid boundary %v) = %v, want nil", timeout, err)
		}
	}
	if begins, rolls, commits, _ := knobs.counts(); begins != 0 || rolls != 0 || commits != 0 {
		t.Fatal("constructor contacted the database")
	}
}

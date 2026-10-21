// 号段预留原语的单元契约（先行 RED）：固定公共 API 尚未实现时，本文件以
// 编译失败记录初始 RED；实现后覆盖保证不触库的分支（nil 构造、输入前置、
// 预取消、常量错误/Unwrap/不回显）与 stdlib 伪驱动故障注入——Begin/Exec/
// RowsAffected/Commit 失败、异常受影响行数、提交结果不明不返回号段且绝不
// 重试。真实 SQL 行为由 integration/postgres 门禁验证。
// Unit contract for the ID-range reservation primitive, written first (RED):
// while the fixed public API is missing this file records the initial RED as a
// compile failure; afterwards it covers the guaranteed no-DB branches (nil
// constructor, input preconditions, pre-cancellation, constant errors with
// Unwrap and no echo) plus stdlib fake-driver fault injection — Begin/Exec/
// RowsAffected/Commit failures, odd affected counts, and the ambiguous-commit
// path that never returns a range and never retries. Real SQL behavior belongs
// to the integration/postgres gate.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
)

// faultKnobs 是伪驱动的共享故障面板：sentinel 错误保证 errors.Is 可达；
// 计数器证明单次执行、无重试、无连接建立（no-DB 分支）。
// faultKnobs is the fake driver's shared fault panel: sentinel errors keep
// causes reachable through errors.Is, and the counters prove single
// execution, no retry and no connection for no-DB branches.
type faultKnobs struct {
	mu          sync.Mutex
	connects    int
	execCount   int
	commitCount int
	rollbackCnt int
	beginErr    error
	execErr     error
	queryErr    error
	commitErr   error
	rowsCount   int64
	rowsErr     error
}

func (k *faultKnobs) snapshot() (connects, execCount, commitCount, rollbacks int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.connects, k.execCount, k.commitCount, k.rollbackCnt
}

const faultDriverName = "idres_fault"

var (
	faultKnobSetsMu sync.Mutex
	faultKnobSeq    int
	faultKnobSets   = map[string]*faultKnobs{}
)

func init() { sql.Register(faultDriverName, faultDriver{}) }

// faultDB 注册一次性的伪驱动 knob 集并返回不触库的 *sql.DB（sql.Open 本身
// 不连接）；t.Cleanup 关闭并清理注册项。
// faultDB registers a one-shot knob set and returns a *sql.DB that has not
// connected (sql.Open itself never dials); t.Cleanup closes and unregisters.
func faultDB(t *testing.T, knobs *faultKnobs) *sql.DB {
	t.Helper()
	faultKnobSetsMu.Lock()
	defer faultKnobSetsMu.Unlock()
	faultKnobSeq++
	name := fmt.Sprintf("%s/%d", t.Name(), faultKnobSeq)
	if _, dup := faultKnobSets[name]; dup {
		t.Fatalf("fault knob set %q already registered", name)
	}
	faultKnobSets[name] = knobs
	db, err := sql.Open(faultDriverName, name)
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", faultDriverName, err)
	}
	t.Cleanup(func() {
		faultKnobSetsMu.Lock()
		delete(faultKnobSets, name)
		faultKnobSetsMu.Unlock()
		_ = db.Close()
	})
	return db
}

type faultDriver struct{}

func (faultDriver) Open(name string) (driver.Conn, error) {
	faultKnobSetsMu.Lock()
	knobs := faultKnobSets[name]
	faultKnobSetsMu.Unlock()
	if knobs == nil {
		return nil, fmt.Errorf("idres fault: unknown knob set %q", name)
	}
	knobs.mu.Lock()
	knobs.connects++
	knobs.mu.Unlock()
	return &faultConn{knobs: knobs}, nil
}

type faultConn struct{ knobs *faultKnobs }

func (c *faultConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("idres fault: Prepare must never run; conn implements Execer/Queryer")
}

func (c *faultConn) Close() error { return nil }

func (c *faultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.knobs.mu.Lock()
	defer c.knobs.mu.Unlock()
	if c.knobs.beginErr != nil {
		return nil, c.knobs.beginErr
	}
	return &faultTx{conn: c}, nil
}

func (c *faultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.knobs.mu.Lock()
	defer c.knobs.mu.Unlock()
	c.knobs.execCount++
	if c.knobs.execErr != nil {
		return nil, c.knobs.execErr
	}
	return faultResult{knobs: c.knobs}, nil
}

func (c *faultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.knobs.mu.Lock()
	defer c.knobs.mu.Unlock()
	if c.knobs.queryErr != nil {
		return nil, c.knobs.queryErr
	}
	return nil, errors.New("idres fault: queries not configured for this knob set")
}

func (c *faultConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

type faultTx struct{ conn *faultConn }

// Commit 模拟“提交结果不明”：伪驱动不区分已应用与未应用的提交，只要
// commitErr 非空，客户端看到的失败就可能意味着服务端已提交——Reserve 必须
// 不返回号段、不重试、不回读。
// Commit models the ambiguous commit: the fake driver does not distinguish
// applied from unapplied commits, so with a non-nil commitErr the observed
// failure may still mean the server committed — Reserve must return no range,
// never retry and never read back.
func (tx *faultTx) Commit() error {
	tx.conn.knobs.mu.Lock()
	defer tx.conn.knobs.mu.Unlock()
	tx.conn.knobs.commitCount++
	return tx.conn.knobs.commitErr
}

func (tx *faultTx) Rollback() error {
	tx.conn.knobs.mu.Lock()
	defer tx.conn.knobs.mu.Unlock()
	tx.conn.knobs.rollbackCnt++
	return nil
}

type faultResult struct{ knobs *faultKnobs }

func (r faultResult) LastInsertId() (int64, error) { return 0, nil }

func (r faultResult) RowsAffected() (int64, error) {
	r.knobs.mu.Lock()
	defer r.knobs.mu.Unlock()
	if r.knobs.rowsErr != nil {
		return 0, r.knobs.rowsErr
	}
	return r.knobs.rowsCount, nil
}

func TestNewReservationsRejectsNilDB(t *testing.T) {
	got, err := NewReservations(nil)
	if err == nil {
		t.Fatal("NewReservations(nil) must fail")
	}
	if got != nil {
		t.Fatalf("NewReservations(nil) returned %v, want nil", got)
	}
	if err.Error() != "postgres id reservations: nil database" {
		t.Fatalf("NewReservations(nil) error text = %q, want bounded constant", err.Error())
	}
}

func TestReserveRejectsInvalidInputBeforeSQL(t *testing.T) {
	knobs := &faultKnobs{}
	r, err := NewReservations(faultDB(t, knobs))
	if err != nil {
		t.Fatalf("NewReservations: %v", err)
	}
	cases := []struct {
		name             string
		expected, amount int64
	}{
		{"negative expected", -1, 1},
		{"zero amount", 0, 0},
		{"negative amount", 0, -5},
		{"sum overflows at max", math.MaxInt64, 1},
		{"sum overflows interior", math.MaxInt64 - 3, 4},
		{"sum overflows large amount", 5, math.MaxInt64 - 4},
	}
	for _, tc := range cases {
		got, ok, err := r.Reserve(context.Background(), tc.expected, tc.amount)
		if got != (IDRange{}) || ok || err == nil {
			t.Fatalf("%s: Reserve = (%v, %v, %v), want zero range, false, error", tc.name, got, ok, err)
		}
		if !errors.Is(err, ErrInvalidIDRange) {
			t.Fatalf("%s: errors.Is(err, ErrInvalidIDRange) = false, err = %v", tc.name, err)
		}
		if err.Error() != ErrInvalidIDRange.Error() {
			t.Fatalf("%s: error text = %q, want fixed sentinel without echoing input", tc.name, err.Error())
		}
	}
	connects, execCount, _, _ := knobs.snapshot()
	if connects != 0 || execCount != 0 {
		t.Fatalf("input validation touched the pool: connects = %d, execCount = %d, want 0/0", connects, execCount)
	}
}

func TestReservationsPreCancelledContext(t *testing.T) {
	knobs := &faultKnobs{}
	r, err := NewReservations(faultDB(t, knobs))
	if err != nil {
		t.Fatalf("NewReservations: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cur, err := r.Current(ctx)
	if cur != 0 || err == nil {
		t.Fatalf("Current(pre-cancelled) = (%d, %v), want (0, error)", cur, err)
	}
	if err.Error() != idStageCancelled {
		t.Fatalf("Current(pre-cancelled) text = %q, want %q", err.Error(), idStageCancelled)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context.Canceled must stay reachable via Unwrap, got %v", err)
	}
	if strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("error text echoes the cause: %q", err.Error())
	}

	got, ok, err := r.Reserve(ctx, 0, 5)
	if got != (IDRange{}) || ok || err == nil {
		t.Fatalf("Reserve(pre-cancelled) = (%v, %v, %v), want zero range, false, error", got, ok, err)
	}
	if err.Error() != idStageCancelled {
		t.Fatalf("Reserve(pre-cancelled) text = %q, want %q", err.Error(), idStageCancelled)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context.Canceled must stay reachable via Unwrap, got %v", err)
	}
	connects, execCount, _, _ := knobs.snapshot()
	if connects != 0 || execCount != 0 {
		t.Fatalf("pre-cancel touched the pool: connects = %d, execCount = %d, want 0/0", connects, execCount)
	}
}

func TestCurrentFaultMapping(t *testing.T) {
	queryErr := errors.New("idres fault: query failed")
	knobs := &faultKnobs{queryErr: queryErr}
	r, err := NewReservations(faultDB(t, knobs))
	if err != nil {
		t.Fatalf("NewReservations: %v", err)
	}
	cur, err := r.Current(context.Background())
	if cur != 0 || err == nil {
		t.Fatalf("Current = (%d, %v), want (0, error)", cur, err)
	}
	if err.Error() != idStageCurrent {
		t.Fatalf("Current error text = %q, want %q", err.Error(), idStageCurrent)
	}
	if !errors.Is(err, queryErr) {
		t.Fatalf("query cause must stay reachable via Unwrap, got %v", err)
	}
}

func TestReserveFaultMatrix(t *testing.T) {
	beginErr := errors.New("idres fault: begin failed")
	execErr := errors.New("idres fault: exec failed")
	rowsErr := errors.New("idres fault: rows affected failed")
	commitErr := errors.New("idres fault: commit transport lost")
	// 兜底回滚只在 Begin 成功且事务未到 Commit 结局的失败路径触达驱动；
	// Begin 失败无事务可回滚，Commit 成功/失败后 Rollback 是 ErrTxDone
	// 空操作（不触达驱动）。
	// The best-effort rollback reaches the driver only on failure paths after
	// a successful Begin without a Commit outcome; Begin failure has no tx to
	// roll back and Rollback after any Commit outcome is an ErrTxDone no-op
	// that never reaches the driver.
	cases := []struct {
		name      string
		knobs     *faultKnobs
		wantText  string
		wantCause error
		wantComms int
		wantExecs int
		wantRolls int
	}{
		{
			name:      "begin failure",
			knobs:     &faultKnobs{beginErr: beginErr},
			wantText:  idStageBegin,
			wantCause: beginErr,
			wantExecs: 0,
			wantRolls: 0,
		},
		{
			name:      "exec failure",
			knobs:     &faultKnobs{execErr: execErr},
			wantText:  idStageReserve,
			wantCause: execErr,
			wantExecs: 1,
			wantRolls: 1,
		},
		{
			name:      "rows affected failure",
			knobs:     &faultKnobs{rowsErr: rowsErr},
			wantText:  idStageReserve,
			wantCause: rowsErr,
			wantExecs: 1,
			wantRolls: 1,
		},
		{
			// 受影响行数本身是驱动诊断细节，只经 Unwrap 出现。
			// The affected count itself is driver diagnostic detail and
			// appears only under Unwrap.
			name:      "unexpected affected count",
			knobs:     &faultKnobs{rowsCount: 2},
			wantText:  idStageReserve,
			wantCause: nil,
			wantExecs: 1,
			wantRolls: 1,
		},
		{
			// commitAgedAp 语义：伪提交可能已被服务端应用，但客户端只见错误。
			// commitAgedAp semantics: the fake commit may already be applied
			// server-side while the client observes only the error.
			name:      "commit failure after applied update",
			knobs:     &faultKnobs{rowsCount: 1, commitErr: commitErr},
			wantText:  idStageCommit,
			wantCause: commitErr,
			wantComms: 1,
			wantExecs: 1,
			wantRolls: 0,
		},
	}
	for _, tc := range cases {
		knobs := tc.knobs
		r, err := NewReservations(faultDB(t, knobs))
		if err != nil {
			t.Fatalf("%s: NewReservations: %v", tc.name, err)
		}
		got, ok, err := r.Reserve(context.Background(), 2, 3)
		if got != (IDRange{}) || ok || err == nil {
			t.Fatalf("%s: Reserve = (%v, %v, %v), want zero range, false, error", tc.name, got, ok, err)
		}
		if err.Error() != tc.wantText {
			t.Fatalf("%s: error text = %q, want fixed %q", tc.name, err.Error(), tc.wantText)
		}
		if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
			t.Fatalf("%s: cause must stay reachable via Unwrap, got %v", tc.name, err)
		}
		_, execCount, commitCount, rollbacks := knobs.snapshot()
		if execCount != tc.wantExecs {
			t.Fatalf("%s: exec count = %d, want %d (no retry)", tc.name, execCount, tc.wantExecs)
		}
		if commitCount != tc.wantComms {
			t.Fatalf("%s: commit count = %d, want %d", tc.name, commitCount, tc.wantComms)
		}
		if rollbacks != tc.wantRolls {
			t.Fatalf("%s: driver rollback count = %d, want %d", tc.name, rollbacks, tc.wantRolls)
		}
	}
}

func TestReserveSuccessAndZeroRowPaths(t *testing.T) {
	knobs := &faultKnobs{rowsCount: 1}
	r, err := NewReservations(faultDB(t, knobs))
	if err != nil {
		t.Fatalf("NewReservations: %v", err)
	}
	got, ok, err := r.Reserve(context.Background(), 4, 6)
	if err != nil || !ok {
		t.Fatalf("Reserve(4,6) = (%v, %v, %v), want ({5 10}, true, nil)", got, ok, err)
	}
	if got.First != 5 || got.Last != 10 {
		t.Fatalf("Reserve(4,6) range = {%d %d}, want {5 10} (inclusive, expected+1 .. expected+amount)", got.First, got.Last)
	}
	_, execCount, commitCount, _ := knobs.snapshot()
	if execCount != 1 || commitCount != 1 {
		t.Fatalf("success path counts = (%d exec, %d commit), want 1/1", execCount, commitCount)
	}

	zeroKnobs := &faultKnobs{rowsCount: 0}
	r2, err := NewReservations(faultDB(t, zeroKnobs))
	if err != nil {
		t.Fatalf("NewReservations: %v", err)
	}
	got, ok, err = r2.Reserve(context.Background(), 4, 6)
	if got != (IDRange{}) || ok || err != nil {
		t.Fatalf("Reserve zero-row = (%v, %v, %v), want (zero range, false, nil)", got, ok, err)
	}
	_, execCount, commitCount, _ = zeroKnobs.snapshot()
	if execCount != 1 || commitCount != 1 {
		t.Fatalf("zero-row path counts = (%d exec, %d commit), want 1/1 (no-change commit still commits)", execCount, commitCount)
	}
}

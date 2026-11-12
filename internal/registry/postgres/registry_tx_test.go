// Registry 事务级单元契约：可注入 query/exec/事务行为的 fake driver 覆盖
// Register 的真实 SQL 分支（新建、存量复用、回填与各失败阶段）；真实
// PostgreSQL 行为归 integration/postgres。
// Transaction-level unit contract for the Registry: an injectable
// query/exec/transaction fake driver covers Register's real SQL branches
// (fresh insert, stored reuse, backfill and every failure stage); real
// PostgreSQL behavior belongs to integration/postgres.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

// 事务 fake 的固定哨兵；文本安全，不含输入。
// Fixed sentinels of the transaction fake; texts stay input-free.
var (
	errTxFakeQuery    = errors.New("postgres registry test: unexpected query")
	errTxFakeExec     = errors.New("postgres registry test: unexpected exec")
	errTxFakeRollback = errors.New("postgres registry test: rollback fault")
	errTxFakeScan     = errors.New("postgres registry test: scan fault")
)

// txHub 记录事务级计数并按测试注入查询/执行行为；并发安全。
// txHub records transaction-level counters and injects per-test
// query/exec behavior; safe for concurrent use.
type txHub struct {
	mu           sync.Mutex
	begins       int
	commits      int
	rollbacks    int
	commitErr    error
	rollbackErr  error
	query        func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error)
	exec         func(query string, args []driver.NamedValue) (int64, error)
	lastQuery    []driver.NamedValue
	lastQuerySQL string
	lastExec     []driver.NamedValue
	lastExecSQL  string
}

func (h *txHub) snapshot() (begins, commits, rollbacks int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.begins, h.commits, h.rollbacks
}

func (h *txHub) execCalls() (sqlText string, args []driver.NamedValue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastExecSQL, h.lastExec
}

func (h *txHub) queryArgs() (sqlText string, args []driver.NamedValue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastQuerySQL, h.lastQuery
}

type txConnector struct{ h *txHub }

func (c *txConnector) Connect(context.Context) (driver.Conn, error) { return &txConn{h: c.h}, nil }
func (c *txConnector) Driver() driver.Driver                        { return txDriver{h: c.h} }

type txDriver struct{ h *txHub }

func (txDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("postgres registry test: open via OpenDB only")
}

type txConn struct{ h *txHub }

func (c *txConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *txConn) Close() error                        { return nil }

// Begin 仅满足接口形状；database/sql 优先 ConnBeginTx。
// Begin only satisfies the interface shape; database/sql prefers ConnBeginTx.
func (c *txConn) Begin() (driver.Tx, error) {
	return nil, errors.New("postgres registry test: begin via BeginTx only")
}

func (c *txConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.h.mu.Lock()
	c.h.begins++
	c.h.mu.Unlock()
	return &txTx{h: c.h}, nil
}

func (c *txConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.h.mu.Lock()
	hook := c.h.query
	c.h.lastQuerySQL = query
	c.h.lastQuery = append([]driver.NamedValue(nil), args...)
	c.h.mu.Unlock()
	if hook == nil {
		return nil, errTxFakeQuery
	}
	cols, rows, err := hook(query, args)
	if err != nil {
		return nil, err
	}
	return &txRows{cols: cols, rows: rows}, nil
}

func (c *txConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.h.mu.Lock()
	hook := c.h.exec
	c.h.lastExecSQL = query
	c.h.lastExec = append([]driver.NamedValue(nil), args...)
	c.h.mu.Unlock()
	if hook == nil {
		return nil, errTxFakeExec
	}
	n, err := hook(query, args)
	if err != nil {
		return nil, err
	}
	return txResult(n), nil
}

type txTx struct{ h *txHub }

func (t *txTx) Commit() error {
	t.h.mu.Lock()
	t.h.commits++
	err := t.h.commitErr
	t.h.mu.Unlock()
	return err
}

func (t *txTx) Rollback() error {
	t.h.mu.Lock()
	t.h.rollbacks++
	err := t.h.rollbackErr
	t.h.mu.Unlock()
	return err
}

type txResult int64

func (r txResult) LastInsertId() (int64, error) { return 0, nil }
func (r txResult) RowsAffected() (int64, error) { return int64(r), nil }

type txRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *txRows) Columns() []string { return r.cols }
func (r *txRows) Close() error      { return nil }

func (r *txRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

// txBinCols 对应 Register 的 binaries 四列查询形状。
// txBinCols mirrors Register's four-column binaries query shape.
var txBinCols = []string{"sha1", "md5", "bin_length", "sha256"}

// newTxRegistry 返回绑定 txHub 的 Registry；缺省查询/执行都报意外调用。
// newTxRegistry returns a Registry bound to the txHub; unset hooks treat
// any query or exec as an unexpected call.
func newTxRegistry(t *testing.T) (*Registry, *txHub) {
	t.Helper()
	h := &txHub{}
	db := sql.OpenDB(&txConnector{h: h})
	t.Cleanup(func() { _ = db.Close() })
	reg, err := New(db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return reg, h
}

// txStoredRow 构造一条命中行；sha256 传 nil 表示 SQL NULL。
// txStoredRow builds one found row; a nil sha256 means SQL NULL.
func txStoredRow(sha1, md5 string, length int64, sha256 any) []driver.Value {
	return []driver.Value{sha1, md5, length, sha256}
}

func TestRegisterFreshInsertReturnsIncomingVerbatim(t *testing.T) {
	reg, h := newTxRegistry(t)
	in := validMeta()
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		return txBinCols, nil, nil // 缺行：走新建分支。
	}
	affected := int64(1)
	h.exec = func(query string, _ []driver.NamedValue) (int64, error) {
		return affected, nil
	}

	got, err := reg.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register fresh insert: %v", err)
	}
	if got != in {
		t.Fatalf("Register fresh = %+v, want incoming verbatim %+v", got, in)
	}
	if s := h.lastExecSQL; s == "" || !containsAll(s, "INSERT INTO binaries", "ON CONFLICT DO NOTHING") {
		t.Fatalf("fresh exec SQL = %q, want INSERT ... ON CONFLICT DO NOTHING", s)
	}
	if _, args := h.execCalls(); len(args) != 4 ||
		args[0].Value != in.SHA1 || args[1].Value != in.MD5 ||
		args[2].Value != in.Size || args[3].Value != in.SHA256 {
		t.Fatalf("fresh insert args = %+v, want [sha1 md5 size sha256] bound in order", args)
	}
	if _, args := h.queryArgs(); len(args) != 1 || args[0].Value != in.SHA1 {
		t.Fatalf("lookup args = %+v, want the sha1 bound alone", args)
	}
	if begins, commits, rollbacks := h.snapshot(); begins != 1 || commits != 1 || rollbacks != 0 {
		t.Fatalf("tx begins/commits/rollbacks = %d/%d/%d, want 1/1/0", begins, commits, rollbacks)
	}

	// 并发冲突输掉（0 行受影响）仍原样返回输入：目标丢弃 create 布尔值。
	// A lost concurrent conflict (0 affected rows) still returns the
	// incoming metadata verbatim: the target discards the create boolean.
	affected = 0
	got, err = reg.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register lost conflict: %v", err)
	}
	if got != in {
		t.Fatalf("Register lost conflict = %+v, want incoming verbatim %+v", got, in)
	}
	if _, commits, rollbacks := h.snapshot(); commits != 2 || rollbacks != 0 {
		t.Fatalf("after lost conflict commits/rollbacks = %d/%d, want 2/0", commits, rollbacks)
	}
}

func TestRegisterFoundNonblankReturnsStoredWithoutWrites(t *testing.T) {
	reg, h := newTxRegistry(t)
	in := validMeta()
	storedSHA256 := strings.Repeat("d", 64)
	storedMD5 := strings.Repeat("e", 32)
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		return txBinCols, [][]driver.Value{txStoredRow(in.SHA1, storedMD5, int64(42), storedSHA256)}, nil
	}
	h.exec = func(string, []driver.NamedValue) (int64, error) {
		t.Fatal("nonblank stored sha256 must not write")
		return 0, nil
	}

	got, err := reg.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register stored reuse: %v", err)
	}
	want := content.Metadata{SHA1: in.SHA1, SHA256: storedSHA256, MD5: storedMD5, Size: 42}
	if got != want {
		t.Fatalf("Register stored = %+v, want stored metadata %+v (md5/length never overwritten)", got, want)
	}
	if begins, commits, rollbacks := h.snapshot(); begins != 1 || commits != 1 || rollbacks != 0 {
		t.Fatalf("tx begins/commits/rollbacks = %d/%d/%d, want 1/1/0", begins, commits, rollbacks)
	}

	// NBSP 存量 SHA-256 按非空白保留：原样返回，绝不回填。
	// A stored NBSP SHA-256 counts as nonblank and is preserved verbatim;
	// no backfill ever runs.
	nbsp := strings.Repeat("\u00a0", 64)
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		return txBinCols, [][]driver.Value{txStoredRow(in.SHA1, storedMD5, int64(7), nbsp)}, nil
	}
	got, err = reg.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register NBSP reuse: %v", err)
	}
	if got.SHA256 != nbsp || got.MD5 != storedMD5 || got.Size != 7 {
		t.Fatalf("Register NBSP = %+v, want stored values verbatim", got)
	}
}

// containsAll 报告 s 是否包含每个子串；仅测试内使用。
// containsAll reports whether s contains every substring; test-only.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

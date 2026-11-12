package security

// 本文件提供同包测试专用的可脚本化 database/sql 后端：按查询文本子串
// 匹配回放行/执行结果/错误，并记录全部查询与参数供行为断言。它绝不触网；
// 每条连接共享同一份脚本与记录器。
// This file supplies the same-package scriptable database/sql backend for
// tests: rows/exec results/errors are replayed by query-text substring
// matching while every query and argument is recorded for behavioral
// assertions. It never touches the network; every connection shares one
// script and recorder.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ssQuery 记录一次查询的文本与参数（参数转为 driver.Value 基础类型）。
// ssQuery records one query's text and arguments (as base driver values).
type ssQuery struct {
	Query string
	Args  []driver.Value
}

// ssResult 是脚本化应答：row 为 nil 表示零行（QueryRow 折叠为
// sql.ErrNoRows）；err 非空时优先返回；closeErr 让 rows.Close() 失败；
// affected 仅对 Exec 有意义。
// ssResult is the scripted reply: a nil row means zero rows (QueryRow
// folds it into sql.ErrNoRows); a nonnil err wins; closeErr fails
// rows.Close(); affected matters for Exec only.
type ssResult struct {
	columns  []string
	row      []driver.Value
	affected int64
	err      error
	closeErr error
}

// ssRows 回放恰一行（或零行）；Close 可注入错误。
// ssRows replays exactly one row (or zero); Close may inject an error.
type ssRows struct {
	columns  []string
	row      []driver.Value
	closed   bool
	closeErr error
}

func (r *ssRows) Columns() []string { return r.columns }
func (r *ssRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.closeErr
}
func (r *ssRows) Next(dest []driver.Value) error {
	if r.row == nil {
		return io.EOF
	}
	copy(dest, r.row)
	r.row = nil
	return nil
}

// ssTx 是脚本化事务：提交/回滚事件进入记录器。
// ssTx is the scripted transaction: commit/rollback events reach the
// recorder.
type ssTx struct{ script *ssScript }

func (t *ssTx) Commit() error   { t.script.addEvent("tx-commit"); return nil }
func (t *ssTx) Rollback() error { t.script.addEvent("tx-rollback"); return nil }

// ssScript 汇聚全部连接共享的查询记录、事件、应答函数与并发峰值。
// ssScript aggregates the query log, events, responder and concurrency
// peak shared by all connections.
type ssScript struct {
	mu        sync.Mutex
	queries   []ssQuery
	events    []string
	responder func(ctx context.Context, query string, args []driver.Value) ssResult
	inflight  int
	peak      int
}

func (s *ssScript) record(query string, args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, a := range args {
		values[i] = a.Value
	}
	s.mu.Lock()
	s.queries = append(s.queries, ssQuery{Query: query, Args: values})
	s.mu.Unlock()
	return values
}

func (s *ssScript) addEvent(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, name)
}

// snapshotQueries 返回已记录查询的副本。
// snapshotQueries returns a copy of the recorded queries.
func (s *ssScript) snapshotQueries() []ssQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ssQuery(nil), s.queries...)
}

// peakInflight 返回应答函数并发峰值快照。
// peakInflight snapshots the responder concurrency peak.
func (s *ssScript) peakInflight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

func (s *ssScript) enterResponder() {
	s.mu.Lock()
	s.inflight++
	if s.inflight > s.peak {
		s.peak = s.inflight
	}
	s.mu.Unlock()
}

func (s *ssScript) leaveResponder() {
	s.mu.Lock()
	s.inflight--
	s.mu.Unlock()
}

// ssUnexpected 构造"未脚本化查询"错误。
// ssUnexpected builds the "unscripted query" error.
func ssUnexpected(query string) error {
	return errors.New("security-test: unscripted query: " + query)
}

// ssConn 是单条脚本化连接；查询/执行/事务全部经脚本应答。
// ssConn is one scripted connection; queries/execs/transactions are all
// answered through the script.
type ssConn struct{ script *ssScript }

func (c *ssConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("security-test: unexpected prepare")
}
func (c *ssConn) Close() error { return nil }
func (c *ssConn) Begin() (driver.Tx, error) {
	return &ssTx{script: c.script}, nil
}

func (c *ssConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := c.script.record(query, args)
	c.script.enterResponder()
	defer c.script.leaveResponder()
	result := c.script.responder(ctx, query, values)
	if result.err != nil {
		return nil, result.err
	}
	columns := result.columns
	if columns == nil {
		// 未显式给列名时按行值数量推导，保证 Scan 目标数一致。
		// Without explicit column names derive the count from the row so
		// Scan destinations line up.
		columns = make([]string, len(result.row))
	}
	return &ssRows{columns: columns, row: result.row, closeErr: result.closeErr}, nil
}

func (c *ssConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := c.script.record(query, args)
	c.script.enterResponder()
	defer c.script.leaveResponder()
	result := c.script.responder(ctx, query, values)
	if result.err != nil {
		return nil, result.err
	}
	return ssAffected(result.affected), nil
}

// ssAffected 把脚本行数包装为 driver.Result。
// ssAffected wraps the scripted count as a driver.Result.
type ssAffected int64

func (n ssAffected) LastInsertId() (int64, error) {
	return 0, errors.New("security-test: no last-insert id")
}
func (n ssAffected) RowsAffected() (int64, error) { return int64(n), nil }

// ssDriver 仅仅桥接到脚本。
// ssDriver merely bridges to the script.
type ssDriver struct{ script *ssScript }

func (d ssDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("security-test: unexpected open")
}

// ssConnector 每次连接返回共享脚本的新 ssConn。
// ssConnector returns a fresh ssConn sharing the script per connection.
type ssConnector struct{ script *ssScript }

func (c *ssConnector) Connect(context.Context) (driver.Conn, error) {
	return &ssConn{script: c.script}, nil
}
func (c *ssConnector) Driver() driver.Driver { return ssDriver{script: c.script} }

// ssOpenDB 打开一个脚本化 *sql.DB；测试结束关闭。
// ssOpenDB opens one scripted *sql.DB closed by the test.
func ssOpenDB(t *testing.T, responder func(context.Context, string, []driver.Value) ssResult) (*sql.DB, *ssScript) {
	t.Helper()
	if responder == nil {
		responder = func(context.Context, string, []driver.Value) ssResult {
			return ssResult{err: errors.New("security-test: no responder configured")}
		}
	}
	script := &ssScript{responder: responder}
	db := sql.OpenDB(&ssConnector{script: script})
	t.Cleanup(func() { db.Close() })
	return db, script
}

// ssNewStore 在脚本化 db 上构造真实 Store。
// ssNewStore builds the real Store over the scripted db.
func ssNewStore(t *testing.T, responder func(context.Context, string, []driver.Value) ssResult) (*Store, *ssScript) {
	t.Helper()
	db, script := ssOpenDB(t, responder)
	store, err := NewStore(db, 10*time.Second)
	if err != nil || store == nil {
		t.Fatalf("NewStore(scripted db) = (%v, %v), want store", store, err)
	}
	return store, script
}

// ssQueriesMatching 返回文本包含 needle 的已记录查询。
// ssQueriesMatching returns recorded queries whose text contains needle.
func ssQueriesMatching(script *ssScript, needle string) []ssQuery {
	var found []ssQuery
	for _, q := range script.snapshotQueries() {
		if strings.Contains(q.Query, needle) {
			found = append(found, q)
		}
	}
	return found
}

package process

// 本文件提供本包测试专用的可脚本化 database/sql 后端：按查询文本子串匹
// 配回放行/执行结果/错误，并记录全部查询与参数供行为断言。它绝不触网；
// 每条连接共享同一份脚本与记录器。
// This file supplies the package-local scriptable database/sql backend for
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
	"sync"
	"testing"
)

// psQuery 记录一次查询的文本与参数。
// psQuery records one query's text and arguments.
type psQuery struct {
	Query string
	Args  []driver.Value
}

// psResult 是脚本化应答：row 为 nil 表示零行；err 非空时优先返回；
// affected 仅对 Exec 有意义。
// psResult is the scripted reply: a nil row means zero rows; a nonnil err
// wins; affected matters for Exec only.
type psResult struct {
	columns  []string
	row      []driver.Value
	affected int64
	err      error
}

// psRows 回放恰一行（或零行）。
// psRows replays exactly one row (or zero).
type psRows struct {
	columns []string
	row     []driver.Value
	closed  bool
}

func (r *psRows) Columns() []string { return r.columns }
func (r *psRows) Close() error {
	r.closed = true
	return nil
}
func (r *psRows) Next(dest []driver.Value) error {
	if r.row == nil {
		return io.EOF
	}
	copy(dest, r.row)
	r.row = nil
	return nil
}

// psTx 是脚本化事务：提交/回滚事件进入记录器。
// psTx is the scripted transaction: commit/rollback events reach the
// recorder.
type psTx struct{ script *psScript }

func (t *psTx) Commit() error   { t.script.addEvent("tx-commit"); return nil }
func (t *psTx) Rollback() error { t.script.addEvent("tx-rollback"); return nil }

// psScript 汇聚全部连接共享的查询记录、事件与应答函数。
// psScript aggregates the query log, events and responder shared by all
// connections.
type psScript struct {
	mu        sync.Mutex
	queries   []psQuery
	events    []string
	responder func(context.Context, string, []driver.Value) psResult
}

func (s *psScript) record(query string, args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, a := range args {
		values[i] = a.Value
	}
	s.mu.Lock()
	s.queries = append(s.queries, psQuery{Query: query, Args: values})
	s.mu.Unlock()
	return values
}

func (s *psScript) addEvent(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, name)
}

// snapshotQueries 返回已记录查询的副本。
// snapshotQueries returns a copy of the recorded queries.
func (s *psScript) snapshotQueries() []psQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]psQuery(nil), s.queries...)
}

// snapshotEvents 返回已记录事件的副本。
// snapshotEvents returns a copy of the recorded events.
func (s *psScript) snapshotEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// psUnexpected 构造"未脚本化查询"错误。
// psUnexpected builds the "unscripted query" error.
func psUnexpected(query string) error {
	return errors.New("process-test: unscripted query: " + query)
}

// psConn 是单条脚本化连接。
// psConn is one scripted connection.
type psConn struct{ script *psScript }

func (c *psConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("process-test: unexpected prepare")
}
func (c *psConn) Close() error { return nil }
func (c *psConn) Begin() (driver.Tx, error) {
	return &psTx{script: c.script}, nil
}

// BeginTx 接受任意隔离级别（checkUploadWriter 使用 READ COMMITTED）。
// BeginTx accepts any isolation level (checkUploadWriter uses READ
// COMMITTED).
func (c *psConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return &psTx{script: c.script}, nil
}

func (c *psConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	values := c.script.record(query, args)
	result := c.script.responder(ctx, query, values)
	if result.err != nil {
		return nil, result.err
	}
	columns := result.columns
	if columns == nil {
		columns = make([]string, len(result.row))
	}
	return &psRows{columns: columns, row: result.row}, nil
}

func (c *psConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := c.script.record(query, args)
	result := c.script.responder(ctx, query, values)
	if result.err != nil {
		return nil, result.err
	}
	return psAffected(result.affected), nil
}

// psAffected 把脚本行数包装为 driver.Result。
// psAffected wraps the scripted count as a driver.Result.
type psAffected int64

func (n psAffected) LastInsertId() (int64, error) {
	return 0, errors.New("process-test: no last-insert id")
}
func (n psAffected) RowsAffected() (int64, error) { return int64(n), nil }

// psConnector 每次连接返回共享脚本的新 psConn。
// psConnector returns a fresh psConn sharing the script per connection.
type psConnector struct{ script *psScript }

func (c *psConnector) Connect(context.Context) (driver.Conn, error) {
	return &psConn{script: c.script}, nil
}
func (c *psConnector) Driver() driver.Driver { return psDriver{script: c.script} }

// psDriver 仅仅桥接到脚本。
// psDriver merely bridges to the script.
type psDriver struct{ script *psScript }

func (d psDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("process-test: unexpected open")
}

// psOpenDB 打开一个脚本化 *sql.DB；测试结束关闭。
// psOpenDB opens one scripted *sql.DB closed by the test.
func psOpenDB(t *testing.T, responder func(context.Context, string, []driver.Value) psResult) (*sql.DB, *psScript) {
	t.Helper()
	if responder == nil {
		responder = func(context.Context, string, []driver.Value) psResult {
			return psResult{err: errors.New("process-test: no responder configured")}
		}
	}
	script := &psScript{responder: responder}
	db := sql.OpenDB(&psConnector{script: script})
	t.Cleanup(func() { db.Close() })
	return db, script
}

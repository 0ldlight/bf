package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/0ldlight/bf/internal/repository"
)

// read pool 的固定错误文案与操作阶段：Error() 只输出固定操作归因，原文
// 经 Unwrap 保留给可信调用方；不回显任何输入或连接信息。
// Fixed error text and operation stages for the read pool: Error() prints
// only the fixed operation attribution while Unwrap keeps the original cause
// for trusted callers; no input or connection detail is ever echoed.
var (
	// ErrInvalidReadPool 表示构造参数不满足显式本地前置（nil db 或超时
	// 越界）；文本固定且不含输入。
	// ErrInvalidReadPool means constructor arguments fail the explicit local
	// preconditions (a nil db or an out-of-range timeout); the text is fixed
	// and input-free.
	ErrInvalidReadPool = errors.New("postgres read pool: invalid configuration")
)

const (
	stagePoolBegin    = "postgres read pool: begin read-only transaction"
	stagePoolRollback = "postgres read pool: rollback read-only transaction"
)

// poolTimeoutBounds 与 Store 相同的有界超时：>0 且 ≤1 分钟。
// poolTimeoutBounds mirrors the Store's bounded timeout: >0 and ≤1 minute.
const poolTimeoutMax = time.Minute

// ReadPool 是下载读取路径的短事务节点读取池：每次 LookupSnapshotSource
// 在调用方 context 下开启一个有界只读事务，经既有 Store 投影执行恰一次
// 查询，随后立即回滚释放连接。事务从不跨越内容传输，也不新增任何 SQL
// 读取模型；本池不做 Close/Ping/DDL，db 生命周期归调用方。
// ReadPool is the short-transaction node read pool for the download path:
// every LookupSnapshotSource begins one bounded read-only transaction under
// the caller's context, performs exactly one query through the existing
// Store projection and rolls back immediately to release the connection. No
// transaction ever spans content transfer and no SQL read model is added;
// the pool never closes, pings or applies DDL and the db lifetime belongs to
// the caller.
type ReadPool struct {
	db      *sql.DB
	timeout time.Duration
}

// NewReadPool 拒绝 nil db 与越界超时（≤0 或 >1 分钟），返回固定
// ErrInvalidReadPool；成功时构造只读短查询池，不接触数据库。
// NewReadPool rejects a nil db and out-of-range timeouts (≤0 or >1 minute)
// with the fixed ErrInvalidReadPool; success builds the read-only short-query
// pool without contacting the database.
func NewReadPool(db *sql.DB, timeout time.Duration) (*ReadPool, error) {
	if db == nil {
		return nil, ErrInvalidReadPool
	}
	if timeout <= 0 || timeout > poolTimeoutMax {
		return nil, ErrInvalidReadPool
	}
	return &ReadPool{db: db, timeout: timeout}, nil
}

// LookupSnapshotSource 在从调用方 context 派生的有界只读事务里委托既有
// Store.LookupSnapshotSource（含全部既有守卫与 NoRows 原因链），返回前
// 必须回滚；回滚失败则丢弃查询结果并返回固定回滚错误。依赖自身超时（
// 派生 context 截止）以普通错误上抛：父 HTTP 仍活跃时由上层按存储失败
// 映射500，不与请求取消混淆。
// LookupSnapshotSource delegates to the existing Store.LookupSnapshotSource
// (with every existing guard and the NoRows cause chain preserved) inside a
// bounded read-only transaction derived from the caller's context, and rolls
// back before returning; a rollback failure discards the query result and
// returns the fixed rollback error. A dependency's own timeout (the derived
// context deadline) surfaces as an ordinary error: while the parent HTTP
// request stays active the upper layer maps it to a 500 storage failure, not
// to a request cancellation.
func (p *ReadPool) LookupSnapshotSource(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	opCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	tx, err := p.db.BeginTx(opCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return repository.FileSnapshotSource{}, &opError{op: stagePoolBegin, cause: err}
	}
	store, err := New(tx)
	if err != nil {
		_ = tx.Rollback()
		return repository.FileSnapshotSource{}, &opError{op: stagePoolBegin, cause: err}
	}
	source, lookupErr := store.LookupSnapshotSource(opCtx, path)
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		return repository.FileSnapshotSource{}, &opError{op: stagePoolRollback, cause: rollbackErr}
	}
	if err := opCtx.Err(); err != nil {
		return repository.FileSnapshotSource{}, &opError{op: stagePoolRollback, cause: err}
	}
	if lookupErr != nil {
		return repository.FileSnapshotSource{}, lookupErr
	}
	return source, nil
}

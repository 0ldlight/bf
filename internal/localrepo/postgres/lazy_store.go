package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
	nodepg "github.com/0ldlight/bf/internal/repository/postgres"
)

// 节点阶段固定文案；Error() 不含输入与原因文本。
// Fixed node-phase texts; Error() carries neither input nor cause text.
const (
	opUploadGateNotAcquired = "postgres upload: node phase gate not acquired"
	opUploadNodeBegin       = "postgres upload: node transaction begin"
	opUploadNodeStoreInit   = "postgres upload: node store init"
	opUploadNodeFinished    = "postgres upload: node phase already finished"
	opUploadNodeLookup      = "postgres upload: node lookup"
	opUploadNodeCreate      = "postgres upload: node create"
	opUploadNodeUpdate      = "postgres upload: node update"
	opUploadIDCurrent       = "postgres upload: id current bound"
	opUploadIDReserve       = "postgres upload: id range reserve"
	opUploadIDConflict      = "postgres upload: id range reserve conflict"
	opUploadIDOutside       = "postgres upload: id allocation outside node phase"
	opUploadCommitOutside   = "postgres upload: commit outside node phase"
)

// uploadOpError 携带固定阶段文案与原始原因；不修改原因对象本身。
// uploadOpError pairs a fixed stage text with the original cause; the cause
// object itself is never modified.
type uploadOpError struct {
	op    string
	cause error
}

func (e *uploadOpError) Error() string { return e.op }
func (e *uploadOpError) Unwrap() error { return e.cause }

// uploadPersistErr 返回 ErrUploadPersistence 标记的错误（原因链完整保留，
// 供 HTTP 先于任何客户端哨兵判定）。
// uploadPersistErr returns an ErrUploadPersistence-marked error with the
// full cause chain preserved (HTTP must test it before any client sentinel).
func uploadPersistErr(op string, cause error) error {
	if cause == nil {
		return &uploadOpError{op: op, cause: localrepo.ErrUploadPersistence}
	}
	return &uploadOpError{op: op, cause: errors.Join(localrepo.ErrUploadPersistence, cause)}
}

// uploadInputErr 返回 ErrInvalidUploadInput 标记的调用方输入错误。
// uploadInputErr returns an ErrInvalidUploadInput-marked caller-input error.
func uploadInputErr(op string) error {
	return &uploadOpError{op: op, cause: localrepo.ErrInvalidUploadInput}
}

// lazyNodeStore 是每请求的懒节点事务：首次使用时才以父 ctx 派生 NodeTimeout
// 有界节点 ctx、取得 Saver 的 size-1 gate 并 BeginTx（READ COMMITTED，读写）；
// 正文读取期间绝不开启节点事务/连接。节点 ctx 覆盖排队/SQL/分配/提交窗口。
// lazyNodeStore is the per-request lazy node transaction: on first use it
// derives the NodeTimeout-bounded node ctx from the parent ctx, takes the
// Saver's size-1 gate and BeginTx (READ COMMITTED, read-write); no node
// transaction or connection ever exists during the body read. The node ctx
// covers the queue/SQL/allocation/commit window.
type lazyNodeStore struct {
	saver         *UploadSaver
	recheck       func(context.Context, *sql.Tx) error
	nodeCtx       context.Context
	cancel        context.CancelFunc
	tx            *sql.Tx
	store         *nodepg.Store
	gateHeld      bool
	committed     bool
	finished      bool
	rollbackFault error
	reserved      int64
	reservedOK    bool
}

// begin 在首次节点操作时执行有界开启；后续调用为空操作。
// begin performs the bounded start on the first node operation; later calls
// are no-ops.
func (l *lazyNodeStore) begin(ctx context.Context) error {
	if l.tx != nil {
		return nil
	}
	if l.finished {
		return uploadPersistErr(opUploadNodeFinished, nil)
	}
	l.nodeCtx, l.cancel = context.WithTimeout(ctx, l.saver.nodeTimeout)
	select {
	case l.saver.gate <- struct{}{}:
		l.gateHeld = true
	case <-l.nodeCtx.Done():
		l.finish()
		return uploadPersistErr(opUploadGateNotAcquired, l.nodeCtx.Err())
	}
	tx, err := l.saver.db.BeginTx(l.nodeCtx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		l.finish()
		return uploadPersistErr(opUploadNodeBegin, err)
	}
	l.tx = tx
	if l.recheck != nil {
		if err := l.recheck(l.nodeCtx, tx); err != nil {
			l.finish()
			return err
		}
	}
	store, err := nodepg.New(tx)
	if err != nil {
		l.finish()
		return uploadPersistErr(opUploadNodeStoreInit, err)
	}
	l.store = store
	return nil
}

// LookupSnapshotSource 首次调用即开启节点事务；缺失行原样透传 sql.ErrNoRows
// 链（供核心走创建分支），其余查询失败标为持久化故障。
// LookupSnapshotSource starts the node transaction on first use; a missing
// row passes through with the sql.ErrNoRows chain intact (the core then
// takes the create branch) and every other query fault is persistence-marked.
func (l *lazyNodeStore) LookupSnapshotSource(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	if err := l.begin(ctx); err != nil {
		return repository.FileSnapshotSource{}, err
	}
	source, err := l.store.LookupSnapshotSource(l.nodeCtx, path)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repository.FileSnapshotSource{}, err
		}
		return repository.FileSnapshotSource{}, uploadPersistErr(opUploadNodeLookup, err)
	}
	return source, nil
}

// Create/Update 委托同一节点事务并标记失败为持久化故障。
// Create/Update delegate to the same node transaction and mark any fault as
// a persistence error.
func (l *lazyNodeStore) Create(ctx context.Context, ref repository.FileReference) (int64, error) {
	if err := l.begin(ctx); err != nil {
		return 0, err
	}
	count, err := l.store.Create(l.nodeCtx, ref)
	if err != nil {
		return 0, uploadPersistErr(opUploadNodeCreate, err)
	}
	return count, nil
}

func (l *lazyNodeStore) Update(ctx context.Context, ref repository.FileReference) (int64, error) {
	if err := l.begin(ctx); err != nil {
		return 0, err
	}
	count, err := l.store.Update(l.nodeCtx, ref)
	if err != nil {
		return 0, uploadPersistErr(opUploadNodeUpdate, err)
	}
	return count, nil
}

// nextID 真实调用 Reservations.Current 后 Reserve(cur,1) 恰好一次：冲突是
// 固定持久化错误，绝不重试、不自动引导号段。始终使用同一有界节点 ctx。
// nextID really calls Reservations.Current then Reserve(cur,1) exactly once:
// a conflict is a fixed persistence error with no retry and no automatic
// range bootstrap. It always uses the same bounded node ctx.
func (l *lazyNodeStore) nextID(context.Context) (int64, error) {
	if l.tx == nil {
		return 0, uploadPersistErr(opUploadIDOutside, nil)
	}
	current, err := l.saver.reservations.Current(l.nodeCtx)
	if err != nil {
		return 0, uploadPersistErr(opUploadIDCurrent, err)
	}
	claimed, ok, rerr := l.saver.reservations.Reserve(l.nodeCtx, current, 1)
	if rerr != nil {
		return 0, uploadPersistErr(opUploadIDReserve, rerr)
	}
	if !ok {
		return 0, uploadPersistErr(opUploadIDConflict, nil)
	}
	l.reserved, l.reservedOK = claimed.First, true
	return claimed.First, nil
}

// lookupAuthoritative 在同一节点事务内重读权威 Source；任何失败（含缺行）
// 都是持久化故障，绝不回读已提交后的数据。
// lookupAuthoritative re-reads the authoritative Source inside the same node
// transaction; any failure (a missing row included) is a persistence fault
// and nothing is ever read back after commit.
func (l *lazyNodeStore) lookupAuthoritative(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	if err := l.begin(ctx); err != nil {
		return repository.FileSnapshotSource{}, err
	}
	source, err := l.store.LookupSnapshotSource(l.nodeCtx, path)
	if err != nil {
		return repository.FileSnapshotSource{}, uploadPersistErr(opUploadAuthoritativeLookup, err)
	}
	return source, nil
}

// commit 恰好一次提交；仅显式成功才置 committed。
// commit commits exactly once; only an explicit success sets committed.
func (l *lazyNodeStore) commit() error {
	if l.tx == nil {
		return uploadPersistErr(opUploadCommitOutside, nil)
	}
	if err := l.tx.Commit(); err != nil {
		return uploadPersistErr(opUploadCommit, err)
	}
	l.committed = true
	return nil
}

// finish 是所有退出路径的幂等清理：先尽力回滚未确认提交的事务（仅忽略
// sql.ErrTxDone；标准 Rollback 无 ctx 形参，也不保证任意 driver 硬期限），
// 再取消节点 ctx，最后释放 gate。回滚故障被记录交由 Save 的 defer 升级为
// 外部持久化优先错误。
// finish is the idempotent cleanup on every exit path: best-effort rollback
// of any begun-but-unconfirmed transaction first (ignoring only
// sql.ErrTxDone; the standard Rollback takes no ctx and guarantees no hard
// arbitrary-driver deadline), then node-ctx cancellation and finally gate
// release. A rollback fault is recorded for Save's defer to upgrade into the
// outer persistence-first error.
func (l *lazyNodeStore) finish() {
	if l.finished {
		return
	}
	l.finished = true
	if l.tx != nil && !l.committed {
		if err := l.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			l.rollbackFault = err
		}
	}
	if l.cancel != nil {
		l.cancel()
	}
	if l.gateHeld {
		<-l.saver.gate
		l.gateHeld = false
	}
}

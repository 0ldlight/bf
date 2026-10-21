// 本文件实现显式本地 PostgreSQL 已提交号段预留原语：不是内存 ID 生成器、
// 初始化器、保存服务或 HTTP 面，也不宣称原版目标分配器/自动提交/HA 运行
// 时对等。Reservations 只持有调用方提供的 *sql.DB；每次预留开启自有的
// READ COMMITTED 事务并做一次以旧上界为条件的参数化 UPDATE，影响 1 行才
// 在 Commit 成功后返回闭区间号段；影响 0 行提交无变更事务并返回零结果
// （不区分缺行与过期期望，调用方用 Current 处理）。提交结果不明（传输
// 失败可能已提交也可能未提交）时绝不自动重试、回读或返回号段；成功提交
// 只是数据库确认，不是掉电/fsync/副本保证，可信调用方自担连接池与配置。
// 输入在触库前拒绝（负期望、非正步长、上界溢出），缺行原样经 Unwrap 保留
// sql.ErrNoRows，负持久上界以固定错误包裹 ErrInvalidIDRange 拒绝。
// This file implements the explicit local PostgreSQL committed ID-range
// reservation primitive: it is not an in-memory ID generator, initializer,
// save service or HTTP surface, and claims no original target allocator/
// autocommit/HA runtime parity. Reservations holds only a caller-supplied
// *sql.DB; every reservation begins its OWN READ COMMITTED transaction and
// performs one parameterized conditional UPDATE on the old bound. One
// affected row yields the inclusive range, returned only after Commit
// succeeds; zero affected rows commit the no-change transaction and return a
// zero result (missing row and stale expectation are not distinguished — the
// caller resolves them through Current). An ambiguous commit (a transport
// failure may or may not have committed) never triggers an automatic retry,
// readback or range return; a successful Commit is a database acknowledgement,
// not a power-loss/fsync/replica guarantee, and the trusted caller owns the
// pool and configuration. Inputs are rejected before touching the pool
// (negative expected bound, nonpositive amount, upper-bound overflow), a
// missing row preserves sql.ErrNoRows verbatim through Unwrap, and a negative
// persisted bound is rejected with a fixed error wrapping ErrInvalidIDRange.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"math"
)

// ReservationSchemaSQL 是全新自有库的最小号段预留模式（unique_ids 表与
// general 种子行），仅供可信引导/测试显式应用；构造与操作绝不执行 DDL，
// 无 ON CONFLICT/自动建行/并发引导，非负 CHECK 是显式本地政策而非目标
// 复制模式。重复应用的种子 INSERT 不做 upsert、按原样报错。
// ReservationSchemaSQL is the minimal fresh-owned reservation schema (the
// unique_ids table plus the general seed row) for explicit trusted bootstrap
// or tests only; the constructor and operations never execute DDL, there is
// no ON CONFLICT, automatic row creation or concurrent bootstrap, and the
// nonnegative CHECK is explicit local policy rather than a copied target
// schema. Re-applying the seed INSERT does no upsert and fails as-is.
//
//go:embed id_reservations.sql
var ReservationSchemaSQL string

// ErrInvalidIDRange 是固定哨兵：Reserve 在触库前拒绝负期望、非正步长或
// 上界溢出（含 Last 超过 MaxInt64）；也作为负持久上界拒绝错误的 Unwrap
// 原因。文本固定，永不回显输入。
// ErrInvalidIDRange is the fixed sentinel under which Reserve rejects a
// negative expected bound, a nonpositive amount or an upper-bound overflow
// (including a Last beyond MaxInt64) before touching the pool; it also rides
// under Unwrap of the negative persisted-bound rejection. The text is fixed
// and never echoes input.
var ErrInvalidIDRange = errors.New("postgres id reservations: invalid id range")

// 号段阶段文案是独立命名的固定常量（与 store.go 的 nodes 阶段不同名）：
// Error() 只输出操作归因，原始错误对象仅经 Unwrap 供可信调用方。
// The id-stage texts are separately named fixed constants (distinct from the
// nodes stages in store.go): Error() prints only the operation attribution
// and the original error object stays under Unwrap for trusted callers.
const (
	idStageInvalid   = "postgres id reservations: invalid reservation input"
	idStageCancelled = "postgres id reservations: cancelled before statement"
	idStageCurrent   = "postgres id reservations: read current bound"
	idStageBegin     = "postgres id reservations: begin reservation transaction"
	idStageReserve   = "postgres id reservations: reserve id range"
	idStageCommit    = "postgres id reservations: commit reservation"
)

// idNamespaceGeneral 固定为 general（对齐打包全新模式的 unique_ids 引导
// 行）；其他命名空间行不读不改，任意命名空间 API 不在本切片。
// idNamespaceGeneral is fixed to general (aligned with the packaged fresh
// unique_ids bootstrap row); other namespace rows are neither read nor
// written, and an arbitrary-namespace API stays out of this slice.
const idNamespaceGeneral = "general"

// IDRange 是闭区间号段：First=旧上界+1，Last=旧上界+amount，Last 不超过
// MaxInt64。零值表示未取得号段。
// IDRange is the inclusive reserved range: First = old bound + 1 and
// Last = old bound + amount, with Last never beyond MaxInt64. The zero value
// means no range was obtained.
type IDRange struct {
	First int64
	Last  int64
}

// Reservations 绑定调用方拥有的 *sql.DB（可信连接池/配置由调用方提供），
// 与 Store 不同：Store 要求调用方传入 *sql.Tx，而号段预留的每次操作由
// Reservations 自行开启并提交自有事务。无自有连接状态；构造不做 DDL、
// Ping、Close 或连接创建。
// Reservations binds a caller-owned *sql.DB (the trusted pool/configuration
// is the caller's responsibility). Unlike Store, which requires a caller
// *sql.Tx, every reservation operation begins and commits its OWN transaction
// here. It holds no connection state of its own; the constructor performs no
// DDL, Ping, Close or connection creation.
type Reservations struct {
	db *sql.DB
}

// NewReservations 拒绝 nil *sql.DB，返回有界常量错误；不做 DB 访问、Ping、
// Close、DDL 或连接创建。
// NewReservations rejects a nil *sql.DB with a bounded constant error; no DB
// access, Ping, Close, DDL or connection creation happens here.
func NewReservations(db *sql.DB) (*Reservations, error) {
	if db == nil {
		return nil, errors.New("postgres id reservations: nil database")
	}
	return &Reservations{db: db}, nil
}

// Current 直接读取已提交的 general 行持久上界（有限上下文的单条参数化
// 查询，无需自有事务）。缺行返回 0 与固定文案，errors.Is(err,
// sql.ErrNoRows) 成立（原样经 Unwrap 保留，供调用方区分缺行）；负持久
// 上界返回 0 与固定错误（Unwrap 链含 ErrInvalidIDRange）——全新 CHECK 阻止
// 该值，此处仅为存量异常的显式拒绝。不分配、不自动初始化；所有失败的
// Error() 恒为固定 idStageCurrent 文案，原因仅经 Unwrap 供可信诊断。
// Current directly reads the committed persisted bound of the general row
// (one bounded parameterized query, no owned transaction needed). A missing
// row returns 0 with the fixed text and errors.Is(err, sql.ErrNoRows) true
// (preserved verbatim through Unwrap so callers can distinguish the miss); a
// negative persisted bound returns 0 with a fixed error whose Unwrap chain
// carries ErrInvalidIDRange — the fresh CHECK prevents that value, and this
// is only an explicit rejection of legacy anomalies. It never allocates or
// auto-initializes; every failure keeps the fixed idStageCurrent Error()
// text, with the cause reachable only through Unwrap for trusted diagnostics.
func (r *Reservations) Current(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, &opError{op: idStageCancelled, cause: err}
	}
	var current int64
	err := r.db.QueryRowContext(ctx,
		"SELECT current_id FROM unique_ids WHERE index_type = $1", idNamespaceGeneral).
		Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, &opError{op: idStageCurrent, cause: sql.ErrNoRows}
	case err != nil:
		return 0, &opError{op: idStageCurrent, cause: err}
	case current < 0:
		return 0, &opError{op: idStageCurrent,
			cause: fmt.Errorf("%s: %w", idStageInvalid, ErrInvalidIDRange)}
	}
	return current, nil
}

// Reserve 以单次条件 UPDATE 预留闭区间号段：期望上界 expected 与步长
// amount 先在触库前校验（expected>=0、amount>0、amount<=MaxInt64-expected），
// 违反即返回零号段、false 与固定哨兵 ErrInvalidIDRange（不回显输入）；
// 预取消上下文同样在池操作前以固定错误拒绝。随后用调用方 *sql.DB 开启
// 自有 READ COMMITTED 事务，执行唯一一条参数化 UPDATE ... WHERE
// index_type='general' AND current_id=expected 并读取受影响行数——无
// SELECT/MAX/序列、无自动重试/upsert/自动建行。影响 1 行：候选号段
// {expected+1, expected+amount} 仅在 Commit 成功后返回；影响 0 行：提交
// 无变更事务并返回零号段、false、nil（不区分缺行与过期期望，调用方用
// Current 处理）；其他行数或 RowsAffected 错误返回固定 idStageReserve
// 错误并尽力回滚。Begin/SQL/计数/提交/取消任一失败都返回零号段、false、
// 错误——尤其提交结果不明（提交可能已应用）时绝不自动重试、回读或返回
// 号段。兜底回滚经 defer 清理，对已成功/结果不明的提交不保证可撤销；自有
// 事务上的取消遵循 database/sql 当前上下文契约。
// Reserve claims an inclusive range with one conditional UPDATE. The expected
// bound and amount are validated before touching the pool (expected>=0,
// amount>0, amount<=MaxInt64-expected); any violation returns a zero range,
// false and the fixed sentinel ErrInvalidIDRange (input never echoed), and a
// pre-cancelled context is rejected with a fixed error before any pool
// operation. It then begins an OWN READ COMMITTED transaction on the caller
// *sql.DB and executes the single parameterized UPDATE ... WHERE
// index_type='general' AND current_id=expected, reading the affected count —
// no SELECT/MAX/sequence, no automatic retry, upsert or row creation. One
// affected row: the candidate range {expected+1, expected+amount} is returned
// only after Commit succeeds. Zero affected rows: the no-change transaction
// is committed and a zero range with false, nil returns (missing row and
// stale expectation stay indistinguishable; the caller resolves via Current).
// Any other count or RowsAffected error returns the fixed idStageReserve
// error after a best-effort rollback. Any Begin/SQL/count/Commit/
// cancellation failure returns a zero range, false and an error — in
// particular an ambiguous commit (it may already be applied) never triggers
// an automatic retry, readback or range return. The safety-net rollback runs
// via defer and is not guaranteed to undo a committed or ambiguous commit;
// cancellation on the owned transaction follows the current database/sql
// context contract.
func (r *Reservations) Reserve(ctx context.Context, expected, amount int64) (IDRange, bool, error) {
	if expected < 0 || amount <= 0 || amount > math.MaxInt64-expected {
		return IDRange{}, false, ErrInvalidIDRange
	}
	if err := ctx.Err(); err != nil {
		return IDRange{}, false, &opError{op: idStageCancelled, cause: err}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return IDRange{}, false, &opError{op: idStageBegin, cause: err}
	}
	// 兜底回滚清理：Commit 成功后为无害空操作；对已成功/结果不明的提交
	// 不保证可撤销。
	// Safety-net rollback cleanup: a harmless no-op after a successful Commit
	// and not guaranteed to undo a committed or ambiguous one.
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		"UPDATE unique_ids SET current_id = $1 WHERE index_type = $2 AND current_id = $3",
		expected+amount, idNamespaceGeneral, expected)
	if err != nil {
		return IDRange{}, false, &opError{op: idStageReserve, cause: err}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return IDRange{}, false, &opError{op: idStageReserve, cause: err}
	}
	switch n {
	case 1:
		if err := tx.Commit(); err != nil {
			return IDRange{}, false, &opError{op: idStageCommit, cause: err}
		}
		return IDRange{First: expected + 1, Last: expected + amount}, true, nil
	case 0:
		// 无变更事务照常提交：缺行/过期期望不加锁阻塞后续观察者。
		// The no-change transaction still commits so a missing row or stale
		// expectation does not hold locks against later observers.
		if err := tx.Commit(); err != nil {
			return IDRange{}, false, &opError{op: idStageCommit, cause: err}
		}
		return IDRange{}, false, nil
	default:
		return IDRange{}, false, &opError{op: idStageReserve,
			cause: fmt.Errorf("affected %d rows, want 0 or 1", n)}
	}
}

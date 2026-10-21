// 本文件实现显式本地 PostgreSQL 已提交号段预留原语 Reservations：不是
// 内存 ID 生成器、初始化器、保存服务或 HTTP 面。核心不变量：调用方提供
// 可信 *sql.DB 与操作 ctx；号段闭区间仅在 Commit 成功后返回；影响 0 行
// 返回零结果；提交结果不明绝不重试/回读/返回号段。完整契约（引导来源、
// 政策与边界）见 docs/ID-RESERVATIONS.md。
// This file implements Reservations, the explicit local PostgreSQL committed
// ID-range reservation primitive: not an in-memory generator, initializer,
// save service or HTTP surface. Core invariants: the caller supplies the
// trusted *sql.DB and the operation contexts; the inclusive range is
// returned only after Commit succeeds; zero affected rows yield a zero
// result; an ambiguous commit is never retried, read back or returned. See
// docs/ID-RESERVATIONS.md for the full contract (bootstrap provenance,
// policies and limits).
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
// 无 ON CONFLICT/自动建行/并发引导。来源限定：打包全新 postgresql.sql 只
// 建 unique_ids 表、不含 general 种子 INSERT；目标 general 零值初始化来自
// IdGenerator.initializeIdGenerator 的缺行写零——本种子行是显式本地全新
// 引导投影，非复制目标 SQL；非负 CHECK 亦是显式本地政策。DDL 不带 IF NOT
// EXISTS（仅限全新应用），重复应用在 CREATE TABLE/种子 INSERT 处按原样
// 报错，无 upsert。
// ReservationSchemaSQL is the minimal fresh-owned reservation schema (the
// unique_ids table plus the general seed row) for explicit trusted bootstrap
// or tests only; the constructor and operations never execute DDL, with no
// ON CONFLICT, automatic row creation or concurrent bootstrap. Provenance:
// the packaged fresh postgresql.sql only creates the unique_ids table and
// carries no general seed INSERT; the target's general zero initialization
// belongs to IdGenerator.initializeIdGenerator writing zero for an absent
// row — this seed row is an explicit local fresh-bootstrap projection, not
// copied target SQL, and the nonnegative CHECK is likewise explicit local
// policy. The DDL carries no IF NOT EXISTS (fresh-only application);
// re-application fails as-is at the CREATE TABLE or the seed INSERT, with no
// upsert.
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

// idNamespaceGeneral 固定为 general（目标分配器共用的 unique_ids general
// 行）；其他命名空间行不读不改，任意命名空间 API 不在本切片。
// idNamespaceGeneral is fixed to general (the unique_ids general row the
// target allocators share); other namespace rows are neither read nor
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

// Current 直接读取已提交的 general 行持久上界（单条参数化查询，无需自有
// 事务；ctx 由调用方提供并自行设界，本方法不施加内部期限）。缺行返回 0
// 且 errors.Is(err, sql.ErrNoRows) 成立——原始错误对象（database/sql 折叠
// 的裸哨兵或驱动包裹错误）原样经 Unwrap 保留，绝不替换；负持久上界返回 0
// 与固定错误（Unwrap 链含 ErrInvalidIDRange）——全新 CHECK 阻止该值，此处
// 仅为存量异常的显式读取侧拒绝。不分配、不自动初始化。失败 Error() 为
// 固定阶段文案（预取消用取消阶段，其余用读取阶段），原因仅经 Unwrap 供
// 可信诊断。
// Current directly reads the committed persisted bound of the general row
// (one parameterized query, no owned transaction; the caller supplies and
// bounds the ctx — this method adds no internal deadline). A missing row
// returns 0 with errors.Is(err, sql.ErrNoRows) true — the ORIGINAL error
// object (database/sql's folded bare sentinel or a driver-wrapped error)
// stays verbatim under Unwrap and is never replaced; a negative persisted
// bound returns 0 with a fixed error whose Unwrap chain carries
// ErrInvalidIDRange — the fresh CHECK prevents that value, so this is only
// the explicit read-side rejection of legacy anomalies. It never allocates
// or auto-initializes. Failures carry fixed stage texts (the cancelled stage
// for pre-cancellation, the read stage otherwise), with causes reachable
// only through Unwrap for trusted diagnostics.
func (r *Reservations) Current(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, &opError{op: idStageCancelled, cause: err}
	}
	var current int64
	err := r.db.QueryRowContext(ctx,
		"SELECT current_id FROM unique_ids WHERE index_type = $1", idNamespaceGeneral).
		Scan(&current)
	// 缺行路径原样保留原始错误对象：空结果集由 database/sql 折叠为裸
	// sql.ErrNoRows，驱动也可能返回包裹 ErrNoRows 的自有错误——两种情况都
	// 不替换/二次包裹原因（errors.Is(err, sql.ErrNoRows) 均成立）。
	// The miss path preserves the original error object verbatim: an empty
	// result set is folded by database/sql into the bare sql.ErrNoRows while
	// a driver may return its own error wrapping ErrNoRows — neither is
	// replaced or re-wrapped (errors.Is(err, sql.ErrNoRows) holds for both).
	if err != nil {
		return 0, &opError{op: idStageCurrent, cause: err}
	}
	if current < 0 {
		return 0, &opError{op: idStageCurrent,
			cause: fmt.Errorf("%s: %w", idStageInvalid, ErrInvalidIDRange)}
	}
	return current, nil
}

// Reserve 以单次条件 UPDATE 预留闭区间号段；完整政策见
// docs/ID-RESERVATIONS.md。核心语义：expected/amount 在触库前校验
// （expected>=0、amount>0、amount<=MaxInt64-expected），违反即零号段、
// false、固定哨兵 ErrInvalidIDRange（不回显输入）；预取消 ctx 同样在池
// 操作前拒绝（ctx 由调用方提供并自行设界）。随后在自有 READ COMMITTED
// 事务内执行唯一一条参数化 UPDATE ... WHERE index_type='general' AND
// current_id=expected 并读取受影响行数——无 SELECT/MAX/序列/自动建行。
// 影响 1 行：候选号段 {expected+1, expected+amount} 仅在 Commit 成功后
// 返回；影响 0 行：提交无变更事务并返回零号段、false、nil（不区分缺行与
// 过期期望）；其他行数/RowsAffected 错误：固定阶段错误 + 尽力回滚。任何
// Begin/SQL/计数/提交/取消失败都返回零号段、false、错误——提交结果不明
// （可能已应用）绝不自动重试、回读或返回号段。注意边界：Go database/sql
// 可能在连接获取阶段透明恢复 driver.ErrBadConn（BeginTx/查询在语句执行
// 前重取连接），本适配器自身绝不重复预留 UPDATE/Commit，也不自动和解
// 结果不明的提交；兜底回滚经 defer 清理，对已成功/结果不明的提交不保证
// 可撤销；自有事务上的取消遵循当前 database/sql 上下文契约。
// Reserve claims an inclusive range with one conditional UPDATE; the full
// policy lives in docs/ID-RESERVATIONS.md. Core semantics: expected/amount
// are validated before touching the pool (expected>=0, amount>0,
// amount<=MaxInt64-expected); violations return the zero range, false and
// the fixed sentinel ErrInvalidIDRange (input never echoed), and a
// pre-cancelled ctx is likewise rejected before any pool operation (the
// caller supplies and bounds the ctx). It then executes the single
// parameterized UPDATE ... WHERE index_type='general' AND
// current_id=expected inside an OWN READ COMMITTED transaction and reads
// the affected count — no SELECT/MAX/sequence/automatic row creation. One
// affected row: the candidate range {expected+1, expected+amount} returns
// only after Commit succeeds. Zero affected rows: the no-change transaction
// commits and returns the zero range, false, nil (missing row and stale
// expectation stay indistinguishable). Any other count or RowsAffected
// error: a fixed stage error plus a best-effort rollback. Any Begin/SQL/
// count/Commit/cancellation failure returns the zero range, false and an
// error — an ambiguous commit (possibly already applied) is never
// automatically retried, read back or returned as a range. Boundary note:
// Go database/sql may transparently recover driver.ErrBadConn while
// acquiring a connection (BeginTx/query re-acquire before any statement
// runs); this adapter itself never repeats the reservation UPDATE/Commit
// and never auto-reconciles an unknown commit. The safety-net rollback runs
// via defer, is not guaranteed to undo a committed or ambiguous commit, and
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

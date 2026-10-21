# 号段预留原语 / ID-range reservation primitive

状态 / State: BIN-135 已实现的**显式本地 PostgreSQL 已提交号段预留原语**（`internal/repository/postgres` 的 `Reservations`）。它不是内存 ID 生成器、初始化器、保存服务或 HTTP 面，也不宣称原版目标分配器、autocommit/提交确认运行时或 HA 行为对等（调查事实见[节点 ID 契约](NODE-ID-CONTRACT.md)）。
The implemented **explicit local PostgreSQL committed ID-range reservation primitive** (`Reservations` in `internal/repository/postgres`). It is not an in-memory ID generator, initializer, save service or HTTP surface, and claims no parity with the original target allocator, its unobserved autocommit/commit semantics or HA behavior (investigation facts stay in the [node-ID contract](NODE-ID-CONTRACT.md)).

## 公共 API / Public API

```go
type IDRange struct { First, Last int64 }        // 闭区间 / inclusive
type Reservations struct { /* 调用方拥有的 *sql.DB / caller-owned */ }
func NewReservations(db *sql.DB) (*Reservations, error)
func (r *Reservations) Current(ctx context.Context) (int64, error)
func (r *Reservations) Reserve(ctx context.Context, expected, amount int64) (IDRange, bool, error)
var ReservationSchemaSQL string                  // go:embed id_reservations.sql
var ErrInvalidIDRange = errors.New(...)          // 固定哨兵 / fixed sentinel
```

构造拒绝 nil `*sql.DB`（有界常量错误），不做 DB 访问/Ping/Close/DDL/连接创建。API 固定，不设推测性领域接口或通用工具包。
The constructor rejects a nil `*sql.DB` (bounded constant error) and performs no DB access, Ping, Close, DDL or connection creation. The API is fixed; no speculative domain interface or generic utility package exists.

## 事务所有权 / Transaction ownership

同包内两种所有权并存 / Two ownership models coexist in one package:

- `Store`（[文件引用](NODE-REFERENCES.md)）：调用方传入 `*sql.Tx`，Store 从不 Begin/Commit/Rollback。
  `Store` ([file references](NODE-REFERENCES.md)): the caller passes a `*sql.Tx`; Store never begins, commits or rolls back.
- `Reservations`（本原语）：调用方只提供可信 `*sql.DB`（连接池/配置自担），每次 `Reserve` 由本原语开启并提交**自有** READ COMMITTED 事务。
  `Reservations` (this primitive): the caller supplies only a trusted `*sql.DB` (pool/configuration owned by the caller); every `Reserve` begins and commits its **own** READ COMMITTED transaction.

成功提交只是数据库确认，不是掉电/fsync/副本保证；可信调用方提供连接池与配置。`Current`/`Reserve` 接收调用方 ctx 且不施加内部期限——需要时限时由调用方为 ctx 设界。Go `database/sql` 可能在连接获取阶段透明恢复 `driver.ErrBadConn`（`BeginTx`/查询在语句执行前重取连接）；本适配器自身绝不重复预留 UPDATE/Commit，也不自动和解结果不明的提交，无需重新设计安全获取。
A successful Commit is a database acknowledgement only — not a power-loss/fsync/replication guarantee; the trusted caller supplies the pool and configuration. `Current`/`Reserve` receive the caller's ctx and add no internal deadline — the caller bounds the context where a deadline is required. Go's `database/sql` may transparently recover `driver.ErrBadConn` while acquiring a connection (BeginTx/query re-acquire before any statement runs); this adapter itself never repeats the reservation UPDATE/Commit and never auto-reconciles an unknown commit, so no redesign of safe acquisition is needed.

## 全新引导 / Fresh bootstrap

`ReservationSchemaSQL`（仅供可信全新引导/测试显式应用；构造与操作绝不执行 DDL）定义：
`ReservationSchemaSQL` (applied explicitly by trusted fresh bootstrap/tests only; the constructor and operations never execute DDL) defines:

- `unique_ids(index_type VARCHAR(32) NOT NULL PRIMARY KEY, current_id BIGINT NOT NULL CHECK (current_id >= 0))`——非负 CHECK 是**显式本地政策**，不是目标复制模式；
  the nonnegative CHECK is **explicit local policy**, not a copied target schema;
- 种子行 `('general', 0)`：固定 general 命名空间，其他命名空间行不读不改。来源限定：打包全新 `postgresql.sql` 只建 `unique_ids` 表、不含 general 种子 INSERT；目标的 general 零值初始化属于 `IdGenerator.initializeIdGenerator` 的缺行写零——本种子行是**显式本地全新引导投影**，非复制目标 SQL。
  the seed row `('general', 0)`: the general namespace is fixed and other namespace rows are neither read nor written. Provenance: the packaged fresh `postgresql.sql` only creates the `unique_ids` table with no general seed INSERT; the target's general zero initialization belongs to `IdGenerator.initializeIdGenerator` writing zero for an absent row — this seed row is an **explicit local fresh-bootstrap projection**, not copied target SQL.

无 ON CONFLICT/自动建行/upsert、并发引导、完整目标 schema 导入或任意命名空间 API。DDL 不带 IF NOT EXISTS（仅限全新应用）：重复应用在 CREATE TABLE 或种子 INSERT 处按原样报错，无 upsert。全新 CHECK 阻止负持久上界，`Current` 仍显式拒绝（固定错误包裹 `ErrInvalidIDRange`）存量异常——读取侧拒绝由确定性单元夹具与真实库夹具共同验证。
No ON CONFLICT, automatic row creation/upsert, concurrent bootstrap, full target-schema import or arbitrary-namespace API. The DDL carries no IF NOT EXISTS (fresh-only application): re-application fails as-is at the CREATE TABLE or the seed INSERT, with no upsert. The fresh CHECK prevents a negative persisted bound, and `Current` still rejects legacy anomalies explicitly (fixed error wrapping `ErrInvalidIDRange`) — the read-side rejection is verified by a deterministic unit fixture alongside the real-database fixtures.

## 行为契约 / Behavior contract

- `Current`：单条参数化 SELECT，直接读已提交的 general 持久上界；不分配、不自动初始化。缺行返回 0 且 `errors.Is(err, sql.ErrNoRows)` 成立——原始错误对象（database/sql 折叠的裸哨兵或驱动包裹错误）原样经 Unwrap 保留，绝不替换；预取消在池操作前以固定错误拒绝（失败文案为固定阶段常量：预取消用取消阶段，其余用读取阶段）。
  `Current`: one parameterized SELECT directly reading the committed general bound; no allocation, no auto-initialization. A missing row returns 0 with `errors.Is(err, sql.ErrNoRows)` true — the ORIGINAL error object (database/sql's folded bare sentinel or a driver-wrapped error) stays verbatim under Unwrap and is never replaced; a pre-cancelled context is rejected before any pool operation (failure texts are fixed stage constants: the cancelled stage for pre-cancellation, the read stage otherwise).
- `Reserve` 输入前置（触库前，固定哨兵 `ErrInvalidIDRange`，零号段/false，不回显输入）：`expected >= 0`、`amount > 0`、`amount <= MaxInt64-expected`（`Last` 恒不超过 `MaxInt64`）。
  `Reserve` input preconditions (before the pool, fixed sentinel `ErrInvalidIDRange`, zero range/false, no input echo): `expected >= 0`, `amount > 0`, `amount <= MaxInt64-expected` (`Last` never exceeds `MaxInt64`).
- `Reserve` 主体：自有事务内单条参数化条件 `UPDATE ... WHERE index_type='general' AND current_id=expected`；无 SELECT/MAX/序列、自动重试/upsert/自动建行。
  `Reserve` body: one parameterized conditional `UPDATE ... WHERE index_type='general' AND current_id=expected` inside the owned transaction; no SELECT/MAX/sequence, automatic retry, upsert or row creation.
- 影响行数语义 / Affected-count semantics:
  - `1`：候选闭区间 `{expected+1, expected+amount}` **仅在 Commit 成功后**返回；
    `1`: the candidate inclusive range is returned **only after Commit succeeds**;
  - `0`：提交无变更事务，返回零号段/false/nil——不区分缺行与过期期望，调用方用 `Current` 处理；
    `0`: the no-change transaction commits and a zero range/false/nil returns — missing row and stale expectation stay indistinguishable; resolve via `Current`;
  - 其他行数/`RowsAffected` 错误：固定阶段错误 + 尽力回滚。
    any other count or `RowsAffected` error: fixed stage error plus best-effort rollback.
- 提交结果不明 / Ambiguous commit: Begin/SQL/计数/提交/取消任一失败都返回零号段/false/错误；提交传输失败可能已提交也可能未提交，**绝不自动重试、回读或返回号段**。兜底回滚经 defer 清理，不保证撤销已成功/结果不明的提交；自有事务上的取消遵循当前 `database/sql` 上下文契约。
  Any Begin/SQL/count/Commit/cancellation failure returns a zero range/false/error; a commit transport failure may or may not have committed and is **never automatically retried, read back or returned as a range**. The safety-net rollback runs via defer and is not guaranteed to undo a committed or ambiguous commit; cancellation on the owned transaction follows the current `database/sql` context contract.
- 错误契约 / Error contract: `Error()` 恒为固定操作归因文案（阶段常量与 nodes 阶段不同名），原始错误对象仅经 `Unwrap` 供可信调用方诊断（`errors.Is/As` 可达）；永不回显输入或 DSN/凭据，SQL/原始驱动细节仅留在可信测试诊断中。
  `Error()` is always the fixed operation-attribution text (stage constants named distinctly from the nodes stages); the original error object stays under `Unwrap` for trusted diagnostics (`errors.Is/As` reachable); input, DSNs and credentials are never echoed, and SQL/raw-driver details live only in trusted test diagnostics.

## 证据与验证 / Evidence and verification

- 单元（`internal/repository/postgres/id_reservations_test.go`）：nil 构造、触库前输入前置（含 MaxInt64 边界）、预取消、常量错误/Unwrap/不回显、包裹 ErrNoRows 原因的对象身份保留（裸/驱动包裹两路径）、确定性负持久上界读取侧拒绝（零值、固定文案、Unwrap 链含哨兵、不回显存储值）、stdlib 伪驱动故障注入（Begin/Exec/RowsAffected/Commit、异常行数、提交结果不明不返回号段且单次执行无重试）。
  Unit: nil constructor, pre-pool input preconditions (including MaxInt64 boundaries), pre-cancellation, constant errors with Unwrap/no echo, wrapped-ErrNoRows cause identity preserved verbatim (both the bare and driver-wrapped paths), the deterministic negative-persisted-bound read-side rejection (zero value, fixed text, sentinel under Unwrap, no stored-value echo), and stdlib fake-driver fault injection (Begin/Exec/RowsAffected/Commit, odd counts, ambiguous commit returning no range with single execution and no retry).
- 真实 PostgreSQL（`integration/postgres/id_reservations_test.go`、`id_reservations_tx_test.go`，一次性测试库门禁；新测试各拥随机 schema 并行调度）：初值/闭区间/过期期望、全新池重开不复用、固定 general 键与他命名空间行并存、缺行 ErrNoRows 与不自动建行、真实 CHECK/PK 约束、MaxInt64 精确边界与溢出拒绝、并发独立池同期望恰一胜者且每个落败者零号段、有界锁等待取消后新操作成功、延迟约束真实 Commit 失败不返回号段、调用方节点事务先开启并在存活期间预留新号段后回滚——独立已提交预留上界保持推进、节点行未落库、既有 binaries 行保留。
  Real PostgreSQL (disposable-database gate; the new tests each own a random schema and run in parallel): initial bound/inclusive ranges/stale expectation, fresh-pool reopen without reuse, the fixed general key beside another namespace row, missing-row ErrNoRows without auto-creation, real CHECK/PK constraints, exact MaxInt64 boundary and overflow rejection, exactly one winner among concurrent independent pools with every loser returning the zero range, bounded lock-wait cancellation followed by fresh success, deferred-constraint real Commit failure returning no range, and a caller node transaction opened FIRST whose rollback after reserving a new range mid-flight leaves the independently committed bound advanced, the node row absent and the existing binaries rows retained.

## 边界与后续 / Limits and follow-ups

内存 ID 生成、分配器编排/保存服务、重读-有界重试循环、任意命名空间 API、并发引导初始化、完整目标 schema 导入、HTTP 面与原版运行时故障对等验证均不在本切片。
The in-memory ID generation, allocator/save orchestration, reload-with-bounded-retry loop, arbitrary-namespace API, concurrent bootstrap initialization, full target-schema import, any HTTP surface and original-runtime fault-parity validation remain outside this slice.

# PostgreSQL 元数据登记 / PostgreSQL metadata registry
状态 / State: BIN-129 适配器切片，已实现并以真实 PostgreSQL 17.10 验证；产品 HTTP、迁移框架与 HA 未接入。
Adapter slice implemented and verified against a real PostgreSQL 17.10 instance; product HTTP, a migration framework and HA remain out of scope.

## 范围与证据 / Scope and evidence
`internal/registry/postgres` 以标准库 `database/sql` 实现 `binary.Registry`（PostgreSQL 为开发默认；驱动由调用方提供，生产代码不绑定 pgx）。语义对齐目标 Artifactory 7.161.26（revision 86126900）`binary-store-core` 4.372.5 的 `insertRecordInDb`/`createDataRecord`/`BinariesDao.create` 静态字节码证据（私有 javap 记录，见[研究索引](RESEARCH-INDEX.md)）；本页是公开规范来源。
`internal/registry/postgres` implements `binary.Registry` over the standard-library `database/sql` with PostgreSQL as the development default; the caller supplies the driver and production code does not bind to pgx. Semantics follow the static bytecode evidence of the target's `insertRecordInDb`/`createDataRecord`/`BinariesDao.create` (private javap notes; see the [research index](RESEARCH-INDEX.md)) — this page is the public canonical source.

目标静态分支与本地实现的逐一对应：
The target's static branches map one-to-one onto the local implementation:

1. 以 SHA-1 查找记录（READ COMMITTED 单事务）。Find the record by SHA-1 inside one READ COMMITTED transaction.
2. 缺失 → `INSERT ... ON CONFLICT DO NOTHING`；即使并发冲突写入零行（目标丢弃 `create` 的布尔返回值），Commit 后仍原样返回**输入**元数据。没有无条件回读、冲突拒绝或自动重试——一致并发内容元数据才是实测运行范围。
   Absent → `INSERT ... ON CONFLICT DO NOTHING`; even when a lost concurrent conflict writes zero rows (the target discards the `create` boolean), the **incoming** metadata is returned verbatim after Commit. There is no unconditional readback, collision rejection or automatic retry — consistent concurrent content metadata is the tested runtime scope.
3. 命中且存储 SHA-256 非空白 → 返回存储元数据，不覆盖 MD5/长度。
   Found with a nonblank stored SHA-256 → the stored metadata wins; MD5/length are never overwritten.
4. 命中但存储 SHA-256 为 NULL 或 Java-blank → 仅回填 SHA-256、回读、Commit 后返回。
   Found with NULL or Java-blank stored SHA-256 → backfill SHA-256 only, reload, return after Commit.

## 规范内部前置 vs 目标怪癖 / Canonical precondition vs target quirk
目标 `BinaryEntityWithValidation` 构造器对非空白字段校验精确长度且构造器**忽略**格式校验布尔结果，因此目标并不完整拒绝畸形输入。本适配器对 `content.Calculate` 的内部输入采用更严格前置：SHA-1/SHA-256/MD5 必须是精确长度的小写 ASCII 十六进制（40/64/32）且 `Size>=0`，在任何数据库访问之前以 `ErrInvalidMetadata` 拒绝；错误文本永不回显值。这是显式本地前置，不是目标畸形输入/HTTP 行为对齐，也不做 normalize/trim。
The target's `BinaryEntityWithValidation` constructor validates exact lengths for nonblank fields but **ignores** the boolean format-validation result, so the target does not fully reject malformed input. This adapter is stricter for internal `content.Calculate` outputs: SHA-1/SHA-256/MD5 must be exact-length lowercase ASCII hex (40/64/32) with `Size>=0`, rejected with `ErrInvalidMetadata` before any database access; error text never echoes values. This is an explicit local precondition — not target malformed-input/HTTP parity — with no normalization or trimming.

Java-blank 判定复刻 `Character.isWhitespace`：ASCII 9..13、28..31 与 Zs/Zl/Zp，但排除 NBSP(U+00A0)/U+2007/U+202F；NEL(U+0085) 非空白。NULL（`sql.NullString`）与 Java-blank 存量值同样触发回填，NBSP 存量值按非空白保留原样。
The Java-blank check replicates `Character.isWhitespace`: ASCII 9..13, 28..31 and Zs/Zl/Zp minus the nonbreaking U+00A0/U+2007/U+202F; NEL U+0085 is not blank. NULL (`sql.NullString`) and Java-blank stored values both trigger backfill; a stored NBSP value counts as nonblank and is preserved verbatim.

## 模式 / Schema
`schema.sql`（v1，embed 为 `SchemaSQL`）仅供可信引导/测试**显式**应用；`New`/`Register` 绝不自动执行 DDL、Ping 或建连接。表 `binaries`：`sha1 varchar(40)` 主键、`md5 varchar(32)` 非空、`bin_length bigint` 非负约束、`sha256 varchar(64)` 可空（显式表示历史缺失）。无节点表、外键或迁移框架——这是首个全新库模式，不是生产升级/迁移保证。表名不带 schema 限定，路径由调用方可信连接配置决定；所有值均为 SQL 参数。
`schema.sql` (v1, embedded as `SchemaSQL`) is applied only **explicitly** by trusted bootstrap/tests; `New`/`Register` never execute DDL, Ping or create connections. Table `binaries`: `sha1 varchar(40)` primary key, `md5 varchar(32)` not null, `bin_length bigint` with a nonnegative constraint, nullable `sha256 varchar(64)` (explicit historical-missing representation). No node table, foreign keys or migration framework — a first fresh-database schema, not a production upgrade/migration guarantee. The table name stays schema-unqualified; the caller's trusted connection configuration decides the path, and every value is a bound SQL parameter.

## 事务、失败与所有权 / Transaction, failure and ownership
元数据事务与 provider/节点保存相互独立（对齐[写入顺序](BINARY-WRITE.md)）。每次失败返回零 `Metadata` 与固定操作文案；`Unwrap()` 原样保留原因（`errors.Is/As` 可达），永不以 `%w` 把底层文本并入 `Error()`。自有事务失败即回滚，非 `ErrTxDone` 的回滚错误与原始原因并列保留；**Commit 失败后事务结局不确定**（模糊网络结局），不做回读或回滚保证。`New` 只拒绝 nil `*sql.DB`；连接创建、驱动与 `Close` 归调用方，`Registry` 无自有连接状态、支持并发调用。
The metadata transaction is independent of provider/node saving (see the [write ordering](BINARY-WRITE.md)). Every failure returns a zero `Metadata` and a fixed operation text; `Unwrap()` exposes the untouched cause (reachable via `errors.Is/As`) and never merges underlying text into `Error()` through `%w`. A failed own transaction is rolled back, preserving a non-`ErrTxDone` rollback error alongside the original cause; after a **Commit failure the transaction outcome is uncertain** (ambiguous network outcome) with no readback/rollback guarantee. `New` rejects only a nil `*sql.DB`; connection creation, the driver and `Close` belong to the caller, and the stateless `Registry` allows concurrent calls.

## 验证 / Verification
- 单元（`internal/registry/postgres`，`-race`）：nil db、非法输入在任何驱动访问前拒绝（1MiB 非法串不出现在错误文本）、取消、固定文案与 typed-cause 保留、Java-blank 语义表。
  Unit (`-race`): nil db; invalid inputs rejected before any driver access (a 1MiB invalid string never appears in error text); cancellation; fixed texts with typed-cause preservation; the Java-blank semantics table.
- 真实数据库门禁（`integration/postgres` 嵌套模块 + `scripts/check-postgres.sh`，pgx v5.11.0 仅测试引入）：独立 SQL 插入/查询、零字节、存量复用不覆盖 MD5/长度、NULL 与 Java-blank 回填、NBSP 保留、8 并发同元数据一行、查询失败、延迟约束触发器致 COMMIT 失败且零结果无行、锁等待取消保留原因、`binary.Service`+真实 filestore 登记失败字节可 Open、移除自有注入后重试成功。环境门禁（缺失 DSN 或未声明一次性库即失败、不 Skip）与一次性库/独立 schema 清理内建于套件。
  Real-database gate (nested `integration/postgres` module + `scripts/check-postgres.sh`, pgx v5.11.0 as a test-only dependency): insert/independent find, zero bytes, stored reuse without MD5/length overwrite, NULL and Java-blank backfill, NBSP preservation, 8 concurrent identical registrations leaving one row, query failure, a deferred constraint trigger causing COMMIT failure with zero result and no row, lock-wait cancellation preserving the cause, and `binary.Service` with the real filestore keeping bytes Open-able when registration fails plus a successful retry after removing the owned injection. The environment gate (missing DSN or disposable declaration fails the run, no Skips) and disposable-database/per-test-schema cleanup are built into the suite.
- 与目标边界：以上是本仓专属真实 PostgreSQL 17.10 运行证据；目标原库的数据库失败路径在原始环境中未实测，静态证据不等价于目标运行时行为。
  Boundary: the above is BinFlow's own real PostgreSQL 17.10 runtime evidence; the target's original database-failure paths were never exercised at runtime, and static evidence is not target runtime behavior.

## 非目标 / Non-goals
UAT 数据库拓扑、HA/复制、迁移框架、节点表/GC/删除、无条件插入后回读、冲突拒绝/重试、对象存储决策、Fern 使用文档变更。
No UAT database topology, HA/replication, migration framework, node table/GC/deletes, unconditional post-insert readback, collision rejection/retry, object-store decisions, or Fern changes here.

## 关联 / Links
- 写入顺序与登记失败语义 / Ordering and failure semantics: [BINARY-WRITE.md](BINARY-WRITE.md)
- 内容模型 / Content identity: [CONTENT-METADATA.md](CONTENT-METADATA.md)
- 目标存储证据 / Target storage evidence: [FILESTORE-CONTRACT.md](FILESTORE-CONTRACT.md)

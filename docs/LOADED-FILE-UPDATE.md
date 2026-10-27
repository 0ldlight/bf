# 加载文件更新接入 / Loaded-file update integration

状态：已实现，最终提交验证和合并状态以 PR 为准。
State: implemented; final commit validation and merge status follow the PR.

```go
func UpdateLoadedFileIfChanged(ctx context.Context, in LoadedFileUpdateInput,
    catalog FileMimeCatalog,
    update func(context.Context, FileReference) (int64, error),
) (FileDirtyUpdateResult, error)
```

本接缝从一个 [FileSnapshotSource](FILE-SNAPSHOT-SOURCE.md) 派生行 ID 和比较基线，立即调用既有[原始快照更新门](FILE-SNAPSHOT-UPDATE.md)。`Desired` 是调用方的完整原始变更后状态；不再归一化 Original、MIME、时间或名字。调用方保证它属于该行，并负责 Source 新鲜度；本层不重新读取或提供 CAS。
This seam derives the row ID and comparison baseline from one source and immediately consumes the existing raw-snapshot update gate. Desired is the caller's complete raw post-mutation state, with no further Original/MIME/time/name normalization. Callers own row association and source freshness; there is no re-read or CAS.

## 显式支持域 / Explicit profile

`CreatedPolicy` 必须为非零 `PreserveLoadedFileCreated`，表示普通已有文件路线中复制与比较的有效 update-created 配置全程稳定为 false。零值及其他值返回固定 `ErrUnsupportedLoadedFileCreatedPolicy`；不读取全局配置，不把注册默认值当成部署观察。
CreatedPolicy must explicitly select nonzero PreserveLoadedFileCreated: effective update-created=false throughout ordinary existing-file copying and comparison. Zero/other values return the fixed sentinel. The layer reads no global configuration and infers no deployment value from a registered default.

目标普通路线为 L→A、A→B 和 A→C，比较使用 B。选定策略下临时复制时钟不保留，modified 回退幂等，正常加载身份不变，checksum 条目值不再归一化。因此物化后的加载值与三份成功复制的比较字段相等，不需要另建公开基线对象或虚构时钟。true/动态策略可能改变 modified，不能只切换 IgnoreCreated 来支持。
The ordinary target route is L→A, A→B and A→C, comparing against B. In the selected profile temporary copy clocks do not survive, modified fallback is idempotent, loaded identity remains stable and checksum values are not normalized again. Materialized loaded fields therefore equal the successful copies' comparison fields, without public baseline objects or fabricated clocks. True/changing policy can alter modified and cannot be supported merely by toggling IgnoreCreated.

Source 保持[即时加载快照](IMMEDIATE-FILE-SNAPSHOT.md)的 Metadata、Actual 空性、UTF-8 与 Original 支持域；MIME 保持[目录与物化](FILE-MIME-MATERIALIZATION.md)的显式配置事实、ASCII 查询和冲突边界。缺元数据旧行、目录或不支持 Original 不隐式转创建或猜测值。Desired 的来源仍是既有门的调用方契约，不代表上传/请求生产者已实现。
Source retains the immediate-loader Metadata, Actual-nullness, UTF-8 and Original profile; MIME retains the approved configuration-fact, ASCII-query and conflict boundaries. Unsupported/legacy rows never become implicit creates or guessed values. Desired retains the existing gate's caller contract, not an implemented upload/request producer.

## 顺序与结果 / Order and results

依次检查取消、非 nil 委托、Source 正 ID、显式策略，随后只加载一次、只物化加载侧 MIME，再无损转换加载 repoKey/path 并校验 `repoKey + "/" + path`。源路径验证在 MIME 后、任何相同跳过前，不从 Desired 或 NodePath 重新猜测路径。最后把基线、原样 Desired、Source ID 和 IgnoreCreated=false 交给既有门，其再次取消检查及写侧映射保持原合同。ctx 必须非 nil。
Order is cancellation, nonnil callback, positive source ID, explicit policy, one load, loaded-side MIME materialization, then lossless loaded repoKey/path conversion and validation of their slash-joined storage path. Validation follows MIME and precedes equality skip, without reconstructing the source path from Desired or NodePath. The existing gate then receives the baseline, untouched Desired, source ID and IgnoreCreated=false; its cancellation recheck and write mapping remain intact. Context must be nonnil.

错误返回零结果且输入不变；成功沿用 Changed/Count，含 Changed=true、Count=0。事务提交/回滚由调用方负责。MIME-only 差异仍调用节点更新，即使 SQL 投影未变；当前 schema 无 MIME 列，此行为不表示已持久化 MIME。
Errors return zero and preserve inputs; success retains Changed/Count, including Changed=true with Count=0. Callers own commit/rollback. MIME-only differences still invoke node updates even when the SQL projection is unchanged; this schema has no MIME column, so that does not establish MIME persistence.

## 证据与限制 / Evidence and limits

BIN-185 复用目标 7.161.26 rev86126900 的冻结字节码与 REA 包证据，并执行真实 Node/Jaxb 工厂链：16 组中 14 条成功链、42 次复制；12 组通过真实 setter 预置缓存 MIME，两组实际解析 null/no-dot。10 个类绑定 CodeSource/字节摘要；两个 dotted/null-MIME 场景因 Home 未绑定失败，明确不算业务拒绝或兼容成功。dotted 基线依赖 MIME 部件验证与静态复制链组合，不声称目标完整 wrapper 差分。
BIN-185 reuses target 7.161.26 rev86126900 frozen bytecode and REA package evidence. Real Node/Jaxb probes check 14 successful chains and 42 copies from 16 cases: twelve preseed cached MIME via real setters, two resolve null/no-dot. Ten classes bind code sources/byte hashes. Two dotted/null-MIME cases fail on unbound Home, establishing neither business rejection nor parity. Dotted baselines compose MIME component validation with the static copy chain, not a full-wrapper differential.

本地单元验证原始差异、竞争错误顺序与所有权；真实 PostgreSQL 接缝覆盖同事务读取后跳过、MIME-only 实际更新调用、元数据提交可见、调用方回滚及真实零行更新。具体执行结果绑定提交，不用测试代码存在代替通过证明。
Local tests cover raw differences, competing-error order and ownership; real PostgreSQL seams cover same-transaction lookup/skip, actual MIME-only update, committed metadata visibility, caller rollback and real zero-row updates. Execution results are commit-bound; test existence alone is not a passing result.

本层不承诺 Java 异常时源 MIME 缓存副作用，不返回可变/original/virgin 生命周期。锁、属性/关注、resetAfterSave、六参构造、XML 部署配置、完整 Unicode、HTTP/权限和完整保存分派仍独立推进；节点跳过不表示这些工作完成。
This layer promises no Java failure-side source-cache effects or mutable/original/virgin lifecycle. Locks, properties/watches, reset, six-argument construction, XML deployment configuration, full Unicode, HTTP/auth and full save dispatch remain separate; skipping a node update does not complete them.

仅替换已发布 size/三 Actual、内部生产 Desired 的消费者见[已发布 binary 信息接入](LOADED-FILE-BINARY.md)。两者共用私有准备函数，本页 raw Desired 契约不变。
The published-binary consumer builds its own size/Actual-only Desired. Both share private preparation; this raw-Desired contract is unchanged.


输入的 `OriginalCase` 仅选择[加载基线规则](LOADED-ORIGINAL-CASING.md)，零值保持旧行为；显式 I/U+0131 支持、未知值错误顺序与回读闭包见该规范。请求值、raw Desired 与本消费者的事务/生命周期责任不变。
Input OriginalCase selects only the [loaded-baseline rules](LOADED-ORIGINAL-CASING.md); zero retains existing behavior. Request values, raw Desired and transaction/lifecycle responsibilities remain unchanged.

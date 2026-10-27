# 已发布 binary 信息接入 / Published-binary information integration

状态：已实现，精确提交验证与合并状态以 PR 为准。
State: implemented; exact-commit checks and merge status follow the PR.

```go
type LoadedFileBinaryInput struct {
    Source        FileSnapshotSource
    Published     content.Metadata
    CreatedPolicy LoadedFileCreatedPolicy
    OriginalCase  LoadedOriginalCase
}
func UpdateLoadedFileBinary(ctx context.Context, in LoadedFileBinaryInput,
    catalog FileMimeCatalog,
    update func(context.Context, FileReference) (int64, error),
) (FileDirtyUpdateResult, error)
```

本消费者从 Source 内部生产 Desired，只替换 Published 的 size 和三 Actual，不要求调用方拼快照。原始 Original（含 NO_ORIG）、身份/缓存名字、MIME、三个时间及 actors 保持加载基线值，再消费[原始节点更新门](FILE-SNAPSHOT-UPDATE.md)。同 binary 可跳过；旧 Actual 的大写、NULL、空或畸形不被重验，可由新 binary 替换。
This consumer builds Desired internally, replacing only size and three Actuals. Raw Originals (including NO_ORIG), identity/cached name, MIME, times and actors retain their baseline values before the existing raw node-update gate. Identical binaries may skip; old uppercase/NULL/empty/malformed Actuals are not revalidated and can be replaced.

## 顺序与输入 / Order and inputs

与[加载文件更新](LOADED-FILE-UPDATE.md)共用包内基线准备：取消、委托、Source 正 ID、显式稳定 `PreserveLoadedFileCreated` 策略、加载一次、MIME 物化、无损 raw 源路径验证。原消费者的 raw Desired 与错误顺序不变；不公开额外基线 helper，不重复加载。
Both consumers share private preparation: cancellation, callback, positive source ID, explicit stable preserve-created policy, one load, MIME materialization and lossless raw-source-path validation. The original consumer retains raw Desired/error order; there is no public baseline helper or duplicate loading.

准备成功后、相同跳过之前，按 SHA1、SHA256、MD5、size 检查 Published：三个固定长度（40/64/32）小写 ASCII hex，size>=0；否则返回固定 `ErrInvalidPublishedFileMetadata`，不规范化或回显输入。这是本地支持画像，比目标 setter 的完整接受域更窄。ctx 必须非 nil；错误零结果且输入不变，成功保持 Changed/Count（含 0），事务由调用方控制。
After preparation and before equality skip, Published must contain fixed-length lowercase ASCII hex SHA1/256/MD5 (40/64/32) and nonnegative size, checked in that order. A fixed sentinel rejects unsupported values without normalization/echo. This local profile is narrower than the target setter. Context must be nonnil; errors return zero and preserve inputs, while success retains Changed/Count including zero. Callers own transactions.

Published 必须来自真实 binary 服务已发布/登记的 winner。形状校验不认证字节、跨摘要元组或登记；Source 新鲜度/行关联仍由调用方保证，Store 的真实约束仍执行。本函数不发布、登记、补偿或删除 binary；节点失败或回滚不撤回已发布字节。
Published must come from the actual binary-service published/registered winner. Shape checks authenticate neither bytes, tuple provenance nor registration. Callers own source freshness/row association; real Store constraints still apply. This function never publishes/registers/compensates/deletes binaries, and node failure/rollback does not undo published bytes.

## 目标路线与 Original / Target route and Original

对应目标 `setFileBinaryInfo` 在成功通知变更之后的值变换，使用 raw `getOriginalOrNoOrig`，不能把 NO_ORIG 替成 Actual。`OriginalCase=Invariant` 保留旧的 ASCII 无大写 I 加载域；两个显式规则及 U+0131 回读闭包见[加载规则](LOADED-ORIGINAL-CASING.md)。所有成功加载域中，恰长合法 hex 已小写；非 hex（包括显式规则输出的 i/U+0131）在目标验证型构造器中保持原样；其他长度也不再归一化，SHA256 Original 固定 NO_ORIG。因此 binary 阶段可以逐码元保留 Original，不逆造请求原值或替换为 Actual。
This models setFileBinaryInfo after successful notification, using raw getOriginalOrNoOrig rather than substituting Actual. Invariant retains the legacy ASCII-without-capital-I domain; see [loading rules](LOADED-ORIGINAL-CASING.md) for explicit modes and dotless-i closure. Across supported loaded states, valid exact-length hex is already lowercase, while nonhex (including explicit i/U+0131 outputs) and other lengths remain unchanged under the target validating constructor. SHA256 Original stays NO_ORIG, so the binary stage preserves raw loaded units.

普通上传 `saveResource` 是另一条组合路线：先 client setters（缺条目清空 Original，并校验旧 Actual），再填 binary、仓库 checksum policy、时间/用户及管理员覆盖和拦截器。直接赋值不能替代这些阶段；导入/移动 fillInfo 也不是普通上传。本片不盖章元数据，不执行 notifyChanges、锁/CAS/session、属性/stats、reset、HTTP 或权限流程。
Ordinary saveResource separately runs client setters (clearing absent-request Originals and validating old Actuals), binary assignment, checksum policy, metadata/admin overrides and interceptors. Direct assignment does not replace them; import/move fillInfo is another route. This slice performs no stamping, notification, locking/CAS/session, properties/stats, reset, HTTP or authorization.

## 证据与验证 / Evidence and validation

BIN-187/188 绑定 7.161.26 rev86126900 同一 WAR 的五类静态字节码与 REA 包证据。12 输入双分支真实值方法重放中 direct 12 成功，no-client 8 成功、4 在旧 Actual 校验失败，227 次完整快照比较；10 运行类与 26 唯一依赖绑定来源/摘要。11 组预置缓存 MIME、1 组 null/no-dot，不冒充完整 wrapper/saveResource、dotted Home 或部署观察。
Same-WAR static bytecode and REA evidence bind the target. Twelve two-branch real-value replays yield twelve direct successes, eight no-client successes and four old-Actual failures, with 227 full snapshots, ten runtime class identities and 26 pinned dependencies. Eleven cases preseed cached MIME and one resolves null/no-dot; this establishes no full wrapper/upload, dotted Home or deployment result.

独立单元与故障回归保护精确字段替换、Original 保留、旧 Actual 修复、Published/源错误顺序和输入所有权；原加载消费者回归继续执行。真实 PostgreSQL 使用实际 binary 服务、真实 Lookup/Store，覆盖提交、同 binary 跳过、回滚与未登记 SHA1 拒绝。最终执行结果绑定 PR 提交，不以测试文件存在代替通过证明。
Independent unit/mutation checks protect field replacement, Original preservation, old-Actual repair, guard order and ownership, alongside the original consumer regressions. Real PostgreSQL uses the actual binary service, Lookup and Store for commit, skip, rollback and unregistered-SHA1 rejection. Passing results must bind the final PR commit.

无客户端摘要的本地写入组合见[独立消费者](LOCAL-FILE-NO-CLIENT.md)：它先校验旧 Actual 并清空 Original；本页直接赋值的旧 Actual 修复与 Original 保留契约不变。两个消费者仅共享私有 Published 校验和内容字段构造。
The local no-client consumer validates old Actuals and clears Originals. Direct assignment retains its repair/preservation contract; only private Published validation and content-field construction are shared.


输入的 `OriginalCase` 仅选择[加载基线规则](LOADED-ORIGINAL-CASING.md)，零值保持旧行为；显式 I/U+0131 支持、未知值错误顺序与回读闭包见该规范。请求值、raw Desired 与本消费者的事务/生命周期责任不变。
Input OriginalCase selects only the [loaded-baseline rules](LOADED-ORIGINAL-CASING.md); zero retains existing behavior. Request values, raw Desired and transaction/lifecycle responsibilities remain unchanged.

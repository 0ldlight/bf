# 已有本地文件无客户端摘要写入 / Existing local-file writes without client checksums

状态：已实现，最终提交检查与合并状态以 PR 为准。
State: implemented; final checks and merge status follow the PR.

```go
func UpdateLocalFileWithoutClientChecksums(ctx context.Context,
    in LocalFileNoClientInput, catalog FileMimeCatalog,
    update func(context.Context, FileReference) (int64, error),
) (FileDirtyUpdateResult, error)
```

选定路线为已有本地文件、无客户端 checksum、无管理员覆盖或改变状态的拦截器/Jar/属性分支。输入包含真实 Source、已发布 Published、ResourceModifiedMillis、UpdatedMillis、CurrentUserUTF16、显式 CreatedPolicy 与 ChecksumPolicy；内部生成 Desired，不接受独立行 ID 或手拼快照。
The selected route is an existing local file with no client checksums/admin overrides or mutating interceptor/Jar/property branches. Inputs carry the actual Source, Published winner, resource-modified/updated time facts, raw UTF-16 user and explicit created/checksum policies. Desired is produced internally, without a separate ID or caller-built snapshot.

## 顺序与字段 / Order and fields

先沿用[加载文件准备](LOADED-FILE-UPDATE.md)，随后按 SHA1、SHA256、MD5 校验旧 Actual：null 允许，非 null 必须是恰长 ASCII hex，大小写均接受，空串或畸形拒绝 `ErrInvalidLoadedActual`。这与[直接 binary 赋值](LOADED-FILE-BINARY.md)不同，后者不重验旧 Actual，可以修复旧值；不能混用两条路线。
After existing baseline preparation, validate old Actuals in SHA1/256/MD5 order: null is allowed; nonnull must be exact-length ASCII hex of either case. Empty/malformed values return the fixed sentinel. Direct binary assignment does not revalidate old Actuals and can repair them; the routes differ.

再验证 Published 的小写 canonical 三摘要和非负 size；继而要求显式 `LocalFileChecksumClient` 或 `LocalFileChecksumServer`，零/其他策略返回 `ErrUnsupportedLocalFileChecksumPolicy`。这是调用方已配置非 nil 本地策略的事实选择，不读取默认、不支持 remote/custom/missing policy。加载/MIME/raw 源路径错误先于旧 Actual，旧 Actual 先于 Published，Published 先于 checksum 策略错误。
Then validate canonical lowercase Published digests/nonnegative size and require explicit CLIENT or SERVER. Zero/other policy returns the fixed unsupported sentinel. This selects a configured nonnil local policy, not a default or remote/custom/missing-policy implementation. Error priority is preparation, old Actual, Published, checksum policy.

Desired 的三 Original 全为 null，size/三 Actual 来自 Published。created、createdBy、身份/缓存名字和已物化 MIME 保持；lastModified 在请求值>0时取请求，否则取原 created；lastUpdated 取显式事实，modifiedBy 取原始 UTF-16 user，直到既有 NodeMetadata 投影才执行 Actor 缩写。
Desired clears all three Originals and takes size/Actuals from Published. Preserve created/by, identity/name and materialized MIME. Modified uses a positive resource value or falls back to existing created; updated/user use explicit facts. Actor abbreviation remains in the existing write projection.

当前 fresh 加载总会产生 SHA256 Original=NO_ORIG，清为 null 后比较不同。因此即使相同 binary、相同时间和用户仍走 dirty 更新，提交后重新读取再调用也如此；schema 不存 SHA256 Original，不能为了强制 no-op 擅自保留 marker。raw actor 早缩写在此恒 dirty 投影域可能无法行为区分，测试不虚称已捕获该任意变异。
Fresh loading always supplies SHA256 Original=NO_ORIG; clearing it makes the state dirty even for identical bytes/time/user, including after another fresh read. The schema does not persist this Original; retaining the marker merely to force a no-op would be wrong. Early actor abbreviation may be observationally indistinguishable in this always-dirty projection domain; tests do not claim to kill that arbitrary mutation.

最后消费既有原始节点门，使用 Source ID、IgnoreCreated=false，保留取消检查与原始比较。ctx 必须非 nil；错误零结果、输入不变，成功 Changed/Count 含 0 沿用原义；调用方拥有事务、Source 新鲜度/行关联和 binary 来源。发布的字节不因后续节点失败或回滚自动撤回。
Consume the existing raw gate with Source ID and IgnoreCreated=false, retaining cancellation checks/comparison. Context must be nonnil; errors return zero without input mutation, while successful Changed/Count includes zero. Callers own transactions, source freshness/association and binary provenance. Later node failure/rollback does not retract published bytes.

## 策略归约与证据 / Policy reduction and evidence

同一目标 7.161.26 WAR 的真实 LocalRepoChecksumPolicy 对三条 Original=null、Actual 为 canonical 的集合在 CLIENT/SERVER 下均 verify=true。8 个真实策略场景、136 个独立断言，以及 7 运行类/26 唯一依赖来源摘要已核对。该类直接实现 ChecksumPolicy，不继承 ChecksumPolicyBase，不能套用 Base 的空集合结果。
Actual same-WAR LocalRepoChecksumPolicy accepts all-null Originals with canonical Actuals under both CLIENT and SERVER. Eight policy cases, 136 independent assertions and seven runtime-class/26 dependency identities are verified. This class implements ChecksumPolicy directly, so ChecksumPolicyBase's empty-set behavior does not apply.

本函数在这个精确值域中不需要 verifier 回调或永真占位函数，不代表通用策略层已经完成。前轮真实值方法重放还独立核实旧 Actual 验证失败前缀及时间/原始用户赋值；这些是部件证据，不是完整 saveResource、notifyChanges、权限/管理员、拦截器、锁/会话、属性/stats、部署或 HTTP 成功。
The exact domain needs no verifier callback or always-true placeholder, without claiming a complete policy layer. Prior real-value replay separately checks old-Actual failure prefixes and time/raw-user setters. These are component observations, not full upload/notification/auth/admin/interceptor/lock/session/property/stats/deployment/HTTP success.

独立单元/故障回归验证顺序、Original 清空、重复 dirty、字段/时间/用户与输入所有权。真实 PostgreSQL 覆盖 CLIENT 提交、SERVER fresh 相同内容重复写、调用方回滚和未登记 binary 的实际 FK 拒绝，并独立检查两个已发布 binary 的记录/字节保留。原两个消费者回归继续运行，具体通过结果绑定提交。
Independent unit/mutation checks cover ordering, clearing, repeated dirty writes, fields/time/user and ownership. Real PostgreSQL covers CLIENT commit, SERVER fresh same-content repeated writes, caller rollback and actual unregistered-binary FK rejection, preserving both published binaries/bytes. Both existing consumers retain their regressions; passing results are commit-bound.

通用[客户端摘要消费者](LOCAL-FILE-CLIENT-CHECKSUMS.md)现承载本算法；本入口传全nil，旧定义类型与本页始终dirty契约保持。中性 LocalFileWriteInput 只是旧类型别名，不生成另一个DTO版本。
The generic consumer now owns the algorithm; this entry passes nil facts and retains its defined type/always-dirty contract. The neutral input name is only an alias.

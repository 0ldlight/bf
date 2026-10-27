# 已有本地文件客户端摘要写入 / Existing local-file writes with client checksums

状态：已实现，最终提交检查与合并状态以 PR 为准。
State: implemented; final checks and merge status follow the PR.

```go
type LocalFileWriteInput = LocalFileNoClientInput
type ClientFileChecksums struct {
    SHA1Original, SHA256Original, MD5Original []uint16
}
func UpdateLocalFileWithClientChecksums(ctx context.Context,
    in LocalFileWriteInput, checksums ClientFileChecksums,
    catalog FileMimeCatalog,
    update func(context.Context, FileReference) (int64, error),
) (FileDirtyUpdateResult, error)
```

LocalFileWriteInput 是既有事实 DTO 的中性别名，旧定义类型与反射名称不变。[无客户端摘要入口](LOCAL-FILE-NO-CLIENT.md)以全 nil checksums 委托本函数；原有顺序、结果与副作用范围不变。
The neutral input alias preserves the existing defined type/reflection name. The no-client entry delegates with nil checksum facts, retaining its order, results and side-effect scope.

ClientFileChecksums 是资源对象已解码的 raw Original，不是未验证 HTTP header。nil 覆盖缺条目/null Original，非 nil 空串独立保留。内部 NO_ORIG marker 的语义不认证它的来源；本接口不实现 header 解析、认证或谁可提供 marker 的授权。
Checksums are decoded resource raw Originals, not unvalidated HTTP headers. Nil covers absent entries/null Original; nonnil empty remains distinct. Internal NO_ORIG semantics authenticate no provenance; header parsing, authentication and marker authorization are not implemented here.

## 顺序与归一化 / Order and normalization

先沿用既有准备（取消/委托/ID/created policy/加载/MIME/raw 源路径），再校验旧 Actual、Published canonical 和显式本地 CLIENT/SERVER。随后仅对恰长 ASCII hex Original 小写化；其他长度、非 hex、空、null、孤立 UTF-16 均复制原值，不 trim，不套用加载侧“恰长就小写”的不同规则。
Retain preparation, old-Actual validation, canonical Published and explicit local-policy guards. Then lowercase only exact-length ASCII-hex Originals. Copy other lengths/nonhex/empty/null/isolated UTF-16 unchanged, without trimming or the loader's different normalization rule.

对**新的 Actual**校验全部 SHA1/SHA256/MD5。CLIENT 的任一 nonblank Original mismatch 返回固定 `ErrClientChecksumMismatch`，在时间/用户盖章与节点委托之前拒绝；SERVER 接受并保留 mismatch。blank 按 Java char/UTF-16 isWhitespace 判断，EM SPACE 属 blank，而 NBSP、NUL、NEL、figure space、narrow NBSP 与孤立代理不属 blank。
Verify every SHA1/256/MD5 against **new Actuals**. Any nonblank mismatch returns a fixed CLIENT sentinel before stamping/node delegation; SERVER accepts and retains it. Blank follows Java char/UTF-16 whitespace: EM SPACE is blank; NBSP, NUL, NEL, figure space, narrow NBSP and isolated surrogates are not.

NO_ORIG 必须精确匹配大小写、不带额外空白，原样存于 Desired；只有其 policy/getOriginal 视图使用 Actual，因此通过验证。不能把 marker 写成 Actual 或 null，也不能从另一个 policy 基类误抄“忽略 SHA256”的规则。
NO_ORIG is exact and case-sensitive, without extra whitespace, and remains raw in Desired. Only its policy/getOriginal view uses Actual. Never replace the stored marker with Actual/null or import another policy family's SHA256 exclusion.

策略成功后应用既有 size/Actual 和时间/用户事实：created/by、身份及 MIME 保持；modified 非正回退 created；updated 与 raw user 原样，Actor 缩写仍在写投影。最后调用原始节点门。错误零结果、输入不变；ctx 必须非 nil，事务、Source 新鲜度/行关联和 Published 来源由调用方负责。
After policy success retain the existing content/stamping flow: preserve created/by, identity and MIME; nonpositive modified falls back to created, updated/raw user remain facts, and abbreviation stays at write projection. Consume the existing raw gate. Errors return zero and preserve inputs; context must be nonnil. Callers own transactions, freshness/row association and publication provenance.

## 比较与持久化边界 / Comparison and persistence boundaries

客户端 SHA256 Original=NO_ORIG 可以保留加载基线，若其他 raw 字段均相同则真正 no-op。原无客户端路径仍把它清为 null，因此 fresh 相同内容仍 dirty。新域中若仅 raw user 尾部不同，即使 Actor 投影相同，也必须更新；提前缩写会误跳过，该错误现有独立回归。
A client SHA256 marker can preserve the baseline and permit a genuine no-op when all other raw fields match. No-client writes still clear it and remain dirty after fresh load. Raw user-tail differences must update even if the Actor projection is equal; early abbreviation can now cause a tested false-clean skip.

SERVER 接受 invalid/nonhex Original 不等于任意值都能通过 SQL 编码/列约束或后续加载。`OriginalCase=Invariant` 的[加载器](IMMEDIATE-FILE-SNAPSHOT.md)保留不含大写 I 的恰长 ASCII 域；显式规则支持 I 与 U+0131 闭包，其他恰长非 ASCII 仍不支持，详见[规则规范](LOADED-ORIGINAL-CASING.md)。本片允许真实资源写侧的 raw 值而不默默扩大加载域、丢弃或小写化它。SHA256 Original 没有 SQL 列，不宣称持久化该字段。
SERVER policy acceptance guarantees neither SQL encoding/column acceptance nor later loader support. The [invariant loader](IMMEDIATE-FILE-SNAPSHOT.md) retains exact-length ASCII without capital I; explicit modes add I/dotless-i closure, with other exact-length non-ASCII still unsupported under the [selected rules](LOADED-ORIGINAL-CASING.md); this write side does not widen that domain, discard values or lowercase them silently. SHA256 Original has no SQL column.

## 证据与验证 / Evidence and validation

BIN-190 的同 WAR 真实 ChecksumInfo 两阶段构造与 LocalRepoChecksumPolicy 矩阵：16 输入×CLIENT/SERVER，32 结果、515 独立断言；7 运行类/26 依赖来源摘要核对。先以旧 Actual 构造，再以新 Actual 构造后验证，区分“匹配旧但不匹配新”和匹配新值；不把部件执行当完整上传/部署结果。
Same-WAR two-stage constructors and local policies yield 32 results from sixteen inputs/two policies, with 515 independent assertions and seven class/26 dependency identities. Verification follows replacement with new Actuals, distinguishing matches to old versus new content. These are component results, not full upload/deployment execution.

独立单元/故障回归与既有三个消费者一起验证；真实 PostgreSQL 覆盖 CLIENT 匹配新内容提交、SHA256-only mismatch 零写拒绝、SERVER mismatch 保存及实际 FK 拒绝、真正 no-op 和调用方回滚。Published bytes/记录保留单独核对。具体通过结果绑定提交。
Independent unit/mutation checks run with all three prior consumers. Real PostgreSQL covers matching CLIENT commit, SHA256-only rejection without writes, SERVER mismatch persistence/actual FK rejection, genuine no-op and caller rollback, separately preserving binary records/bytes. Results bind the final commit.

本片仍不实现 HTTP/auth、管理员覆盖、Jar/拦截器、属性/stats、notifyChanges、锁/CAS/session/reset 或时间/用户来源生产者；没有新增 schema 或依赖。
HTTP/auth, admin overrides, Jar/interceptors, properties/stats, notification, locking/CAS/session/reset and clock/user producers remain separate. No schema/dependency change is introduced.


输入的 `OriginalCase` 仅选择[加载基线规则](LOADED-ORIGINAL-CASING.md)，零值保持旧行为；显式 I/U+0131 支持、未知值错误顺序与回读闭包见该规范。请求值、raw Desired 与本消费者的事务/生命周期责任不变。
Input OriginalCase selects only the [loaded-baseline rules](LOADED-ORIGINAL-CASING.md); zero retains existing behavior. Request values, raw Desired and transaction/lifecycle responsibilities remain unchanged.

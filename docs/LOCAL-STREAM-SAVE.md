# 普通流式本地保存 / Ordinary local stream save

状态：BIN-196 有限内部入口已实现；本地产品单元/race、真实 PostgreSQL、故障变异与共享检查通过。精确提交 PR/默认分支 CI 由交付记录独立核验，不计完整上传兼容。
State: the bounded internal entry is implemented and local unit/race, real PostgreSQL, mutation and shared gates passed. Delivery independently verifies exact-commit PR/default CI; full upload parity remains open.

## 命令边界 / Command boundary

`internal/localrepo.SaveFile(ctx, reader, FileInput, FileDependencies)` 接收真实 `io.Reader`、原始 repo/path、已解码三 Original、资源时间、认证后 raw UTF-16 用户、显式节点策略、预检模式及可选已解码长度。调用方不传 Published、Source、Metadata 或 ID。
The command accepts an actual reader, raw identity/resource facts, decoded Originals, authenticated user units, explicit node policies/precheck mode and optional decoded length. Publication and loaded/fresh node state are produced internally.

依赖为初始化的 `*binary.Service` 与[已发布本地保存](LOCAL-FILE-SAVE-PUBLISHED.md)依赖；顺序为真实 `Add` 一次，再 `SavePublishedLocalFile` 一次。节点与成功结果的 Published 都使用 registry 返回的 canonical winner，它可以与本次流计算的元组不同。
An initialized binary service publishes/registers once, then the existing node command saves once. Both node input and successful Published output use the registry's canonical winner, which may differ from newly computed stream metadata.

成功返回 `{Published, Save}`；任何错误两部分均归零。固定阶段错误不回显输入或 cause 文本，Unwrap 保留 `errors.Is/As`。ctx 必须非 nil；typed-nil 依赖、零值 binary.Service 和并发修改输入不在调用前提内。
Success returns publication plus the actual node result; errors return both as zero. Fixed stage text preserves the original cause through Unwrap. Callers supply nonnil context/initialized dependencies and do not mutate inputs concurrently.

## 预检与长度 / Precheck and length

`SkipChecksumPrecheck` 与 `VerifyChecksumPrecheck` 必须显式选择，独立于节点 CLIENT/SERVER；不推断全局开关有效值或默认值。目标 Local 策略的 shouldVerifyBadClientChecksum 为 true，包括 SERVER。
The explicit precheck mode is independent of node CLIENT/SERVER policy. No effective global flag/default is inferred; the bound target local policy permits byte verification for SERVER too.

Skip 不发送摘要 Expected，也不解码 Original；raw UTF-16 继续交给节点。Verify 使用 fresh Actual=null 画像：nil、空与精确 `NO_ORIG` 无 Expected；其余有效 UTF-16 无损转换，交既有 filestore 做 Java blank、恰长 ASCII hex 小写化与比较。
Skip omits digest expectations and preserves raw Originals for the node. Verify maps fresh Originals with null Actual: nil/empty/exact NO_ORIG omit expectations; other valid UTF-16 is decoded losslessly for existing filestore blank/hex/comparison rules.

Verify 按 SHA1→SHA256→MD5 顺序在读流前拒绝孤立/倒置代理；合法代理对支持。这是有限本地编码边界，不是目标拒绝观察。`no_orig`、NBSP、NEL 不能误作缺省；节点仍收到原始切片，不接收预检字符串或替换后的 marker。
Verify rejects isolated/reversed surrogates before reading, in SHA1/SHA256/MD5 order; valid pairs are supported. This is a local support limit, not observed target rejection. Lowercase marker/NBSP/NEL remain nonblank, and node Originals stay raw.

`DeclaredSize *int64` 两种模式均复制其值传给 provider；nil 缺省，负值或不匹配在真实读流后、发布前由现有长度校验拒绝，不提前校验。原始 HTTP 长度解析独立。
Both modes copy and pass decoded size to the provider. Negative/mismatching sizes fail after actual reading and before publication through the existing length validator. Raw HTTP parsing is separate.

## 顺序、所有权与失败 / Order, ownership and failure

入口先取消检查，再空依赖、空 reader、显式模式与编码支持检查；这些是明确本地前置约束，不宣称目标异常顺序等价。随后 Add，成功后才进入节点路径、CLIENT/SERVER、加载画像、catalog、查询与采时守卫。
Local cancellation/configuration/reader/mode/encoding guards precede Add. They make no target exception-order claim. Node path/policy/profile/catalog/lookup/clock stages remain after successful publication.

读流、Expected、provider 或 registry 错误阻止节点查询及取 ID；Verify+SERVER 可提前摘要拒绝。Skip+CLIENT 的摘要拒绝发生在已发布/登记之后；节点路径或目录拒绝也不能前移来隐藏这些副作用。
Reader/expectation/provider/registry errors skip node lookup/allocation. Verify may reject SERVER early; Skip+CLIENT can reject after publication/registration. Node path/folder failures remain late.

调用方持有 reader 与真实节点 Tx，入口绝不关闭 reader 或 begin/commit/rollback。registry 与 ID 按既有独立提交契约；成功节点结果在调用方提交前仍临时。失败不重试、upsert、重查或补偿已发布字节、registry 或已提交 ID；回滚允许保留字节和 ID 洞。
Callers own the reader and actual node transaction; the entry closes/manages neither. Registry/ID ownership follows existing independent commits. Node success is provisional until caller commit. Failures/rollback do not retry or compensate durable bytes, registry rows or reserved IDs.

## 证据与未决范围 / Evidence and remaining scope

固定 Artifactory 7.161.26 同 WAR 静态调用链显示先 addBinary、后节点保存；BinaryHeadersUtil 受独立全局摘要开关影响，Content-Length 不受该摘要开关限制。静态方法顺序不能证明部署事务边界。
Bound same-WAR static evidence orders binary addition before node save and separates the digest flag from Content-Length. Static order does not establish deployed transaction boundaries.

真实私有 header helper 78 组合及 PostProcessChecksumBinaryStream 12 组合，共 90 参考、523 独立断言；28 同 WAR JAR、6 运行类来源已核验。受控 JVM26.0.2.1/ROOT 观察证明局部 getter/header 与显式 close 校验；目标 stream 关闭底层一次不等于完整上传所有权结论。
Ninety actual component references (78 header, 12 stream) have 523 independent assertions and checked provenance for 28 JARs/six classes. Controlled JVM26/ROOT observations cover local header/getter and explicit stream-close validation only.

未执行公有全局配置读取、完整目标 saveResource/provider/registry/SQL/HTTP；本轮组件证据不代表包内 JDK25、部署 Locale 或新产品 GREEN。原始资料保持私有，来源摘要见[研究索引](RESEARCH-INDEX.md)。
Public global configuration, complete target save/provider/registry/SQL/HTTP and packaged-JDK/deployment behavior remain unexecuted. Component evidence is not new product GREEN; raw research stays private.

本地产品出口：9 组单元/race、8 项编译行为故障变异、3 项真实 PostgreSQL 顶层（含 6 个预检/策略/长度子场景）及共享检查通过，独立审查无阻断。真实流由入口实际发布，核验 canonical winner、提交/全新读取/no-op/更新回滚、早期拒绝与已发布后拒绝、23505/字节/ID 保留；数据库逐顶层固定 5m/race/count=1 完整执行。临时资源清理结果由私有交付证据记录。
Local product gates passed: nine unit/race groups, eight compiled behavioral mutations, three real-PostgreSQL tops (including six precheck/policy/size subcases), shared checks and independent review. The actual stream entry exercised canonical winners, commit/fresh read/noop/update rollback, early/late failures and SQLSTATE 23505 with retained bytes/IDs. Each database top completed with an unchanged five-minute race/count=1 gate.

完整 HTTP/auth、锁/session/admin、预计算 header、Unicode/Locale、上传及部署兼容仍开放。
Full transport/auth/locking/session/admin/precalculated-header/Unicode/upload/deployment parity remains open.

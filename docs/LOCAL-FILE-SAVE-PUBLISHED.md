# 已发布本地文件保存 / Published local-file save

状态：BIN-195 已实现有限内部命令，本地出口已通过；交付仍按精确提交核验 PR 与默认分支 CI，不计为完整兼容。
State: the bounded internal command is implemented with local gates passing. Delivery separately verifies exact-commit PR and default-branch CI; this is not full compatibility.

## 命令边界 / Command boundary

`SavePublishedLocalFile(ctx, LocalFileSaveInput, LocalFileSaveDependencies)` 是普通单写者内部节点命令：实际查询一次；命中消费[已有客户端摘要更新](LOCAL-FILE-CLIENT-CHECKSUMS.md)；缺失在内部产生完整元数据并调用[普通创建](FILE-CREATE.md)。这样保留真实读取与新建状态来源，调用方无需伪造已加载 Source 或预拼 Metadata。
The ordinary single-writer command performs one actual lookup, delegates hits to the existing client-checksum updater, and internally produces complete fresh metadata before ordinary creation. Callers supply neither a fabricated loaded Source nor preassembled Metadata.

输入保留原始 repo/path、真实已发布 `content.Metadata`、已解码三 Original、请求 created/modified 毫秒、认证后 raw UTF-16 用户及显式策略。没有 caller ID、Source、Metadata 或 updated 时间。发布内容和用户/marker 来源由上游负责；本命令不证明任意 Published 元组对应真实字节。
Inputs carry raw repo/path, actually published content, decoded Originals, request-created/modified milliseconds, authenticated raw user units and explicit policies. There is no caller ID/Source/Metadata/updated timestamp. Upstream owns publication, principal and marker provenance; an arbitrary Published tuple is not proof of stored bytes.

`LocalFileSaveStore` 直接使用 `LookupSnapshotSource/Create/Update`；现有事务绑定 Store 满足它。缺失协议为 `errors.Is(err, sql.ErrNoRows)`，其他错误（包括目录拒绝）不能转创建。命中行的 ID/身份权威；legacy nil Metadata、坏 ID、加载错误和更新 0 行均不能回退创建。
The narrow store port uses existing methods directly. Only the preserved standard missing-row sentinel dispatches to creation; folders and other errors terminate. Stored hit identity/ID are authoritative; legacy metadata, invalid IDs, loader errors and zero-row updates never become creates.

结果 `{ID, Created, Changed, Count}`：创建 `(id,true,true,1)`；跳过 `(id,false,false,0)`；执行更新 `(id,false,true,count)`，count 可为 0；错误返回全零。ctx 必须非 nil；Store/NextID/Clock 必须真实初始化，typed-nil 实现不在调用前提内，命中也要求这些依赖非 nil。
Results distinguish creation, skipped updates and executed zero-row updates. Errors return zero. Context must be nonnil; initialized Store/NextID/Clock are required even on hits, and typed-nil implementations violate the caller precondition.

## 路径支持域 / Path support profile

查询复用 raw 双参构造、force-file、一次 metadata 名字剥离及既有 NewPath。创建继续接收最初 raw repo/path，不能把查询映射后的名字再次送入创建而剥第二次 suffix。普通 `a/`、`a//` 与复合 metadata 名字仍以单次映射身份处理，不引入 `path.Clean` 或斜杠折叠。
Queries reuse existing raw construction, force-file, one metadata-name strip and NewPath. Missing creation retains the original request, avoiding a second suffix transformation. Ordinary trailing slashes and compound metadata names retain the established mapping; no path cleaning or slash collapsing is added.

本地画像要求 raw/forced path 相等，且既有 parent 在 Java trim/边界 slash 规范化后不变；`/ a/`、`/a /`、`a/ /`、`a/ / /b` 等暴露边界空白的路径被显式排除。root/blank、映射 root、nested 空名字、literal-dot、非法 UTF-8/NUL 与既有存储校验分别拒绝。新命令在查询/取 ID 前守卫，不改变旧 CreateFile 的取 ID 后映射顺序。
The finite profile requires raw/forced path equality and a parent fixed point under existing normalization. Exposed boundary-whitespace cases are excluded. Root/blank, mapped roots, nested blank names, literal-dot and encoding/storage errors reject before lookup/allocation. This does not move the standalone creator's existing post-allocation mapping.

## 时钟与新建元数据 / Clocks and fresh metadata

`FileSaveClock.NowMillis()` 是阶段性 primitive 时间源；`SystemFileSaveClock` 实际调用 `time.Now().UnixMilli()`。值可相等、非正或倒退，不夹紧为单调时间。旧公开更新入口仍使用调用方显式 `UpdatedMillis`，其语义不变。
The concrete system adapter captures wall milliseconds at each requested stage. Values need not be positive, distinct or monotonic. The older public update API retains its explicit captured UpdatedMillis semantics.

命中保留既有准备/旧 Actual/Published/策略/Original 校验；校验成功且选择 modified 后才采 updated，没有 constructor 采样。created/by、存储身份和 MIME 沿用现有消费规则。CreatedPolicy、OriginalCase 与 MIME catalog 仅在命中消费，不能阻挡缺失分支。
Hits retain existing validation order and sample updated only after checksum verification and modified selection, with no constructor capture. Existing created/by, identity and MIME rules remain. Loaded-only policies/casing/catalog do not block a missing node.

缺失先复查取消，再采 constructor-created；校验 Published 与显式 CLIENT/SERVER，规范化三 Original、应用新 Actual 后验证策略。CLIENT mismatch 在 updated/ID/插入之前拒绝；SERVER 保留 mismatch；NO_ORIG 只改变策略视图，不认证来源。
Missing nodes recheck cancellation, capture constructor-created, then validate publication/policy, normalize Originals, apply new Actuals and verify. CLIENT rejection precedes updated/allocation/insertion; SERVER retains mismatches. Marker semantics authorize no provenance.

modified >0 原样，否则回退 constructor-created；随后采 updated、设置 raw modified-by，再以请求 created 原样覆盖 created（包括 0/负值）及 created-by，不能回头改 modified。Actor 在节点投影才按既有 64 码元规则缩写。完整非 nil FileMetadata 保留三时间及 SHA1/MD5 Original；SHA256 Original 没有 SQL 列。
Positive modified is retained; otherwise it falls back to constructor-created. Capture updated and raw modifier, then overwrite created verbatim from the request, including zero/negative, and set creator without recomputing modified. Actor abbreviation occurs at node projection. Complete metadata preserves three times and SHA1/MD5 Originals; SHA256 Original has no SQL column.

## 事务与失败 / Transactions and failures

调用方持有真实节点事务，本命令不 begin/commit/rollback/upsert/重查/重试。NextID 使用现有创建端口；真实 Reservations 只有 Current/Reserve，可经一次 Current→Reserve(expected,1) 的具体分配器取得独立已提交 ID，冲突失败不自动重试，不宣称完整 HA 分配器。
Callers own the actual node transaction. The command performs no transaction management, upsert, relookup or retry. The existing allocation port may use one concrete committed Current→Reserve(expected,1) operation; conflict failure is not retried, and this is no full HA allocator claim.

节点失败或调用方回滚不撤回已发布字节或归还已提交 ID；唯一键失败保持失败，更新 0 行不转插入。SERVER 接受 raw Original 不保证 SQL 编码/列约束或未来有限加载画像能接受它。
Node failure/rollback neither withdraws published bytes nor returns committed IDs. Unique-key failures stay failures, and zero-row updates do not insert. SERVER acceptance guarantees neither SQL acceptance nor compatibility with a later bounded loader.

## 证据与出口 / Evidence and gates

固定 Artifactory 7.161.26 同 WAR 真实类组合参考：60 输入、51 Node 投影、9 CLIENT false，独立核验 2,201 断言。真实构造器时钟未替换；setter 和 Node 投影验证顺序/值，未实际执行完整 saveResource。另 36 路径输入中有限身份守卫准入 10 个；其中 8 个通过既有 Go 创建映射对照，2 个由既有存储校验拒绝，不计为保存命令产品验收。
Same-WAR actual-class composition yields 60 inputs, 51 Node projections, nine CLIENT false results and 2,201 independent assertions with an untouched real constructor clock. This is not a complete saveResource execution. A separate 36-input path reference admits ten identities; eight match existing Go creation and two meet existing storage rejection, not product-command acceptance.

类/JAR 来源与父 WAR 条目摘要核对，原始输出保持私有。本机受控 JVM 26.0.2.1 不等于包内 JDK25、部署 Locale 或在线服务。合成 Published tuple 是参考输入，不是实际 binary 发布结果。
Class/JAR provenance is checked against parent WAR entries; raw evidence stays private. Controlled local JVM 26 execution establishes neither bundled-JDK25 nor deployed behavior. Synthetic published metadata is a reference input, not observed publication.

本地出口：11 组新单元/race、6 项真实编译行为故障变异、3 项真实 PostgreSQL 顶层场景（含 4 项拒绝子场景）、旧公开更新回归及共享检查通过。数据库逐顶层执行 race/count=1/固定 5 分钟，覆盖真实发布、独立预留、提交、无变更、回滚和 23505；最终源码冻结后重新核验，未把源码变化触发的无效运行计为通过。精确提交全量 CI 以最终 PR 与合并交付记录为准。
Local gates passed: 11 new unit/race groups, six compiled behavioral mutations, three real-PostgreSQL top-level scenarios (including four rejection subcases), old public-update regression and shared checks. Each database scenario ran race/count=1 with an unchanged five-minute timeout, covering actual publication, independent reservations, commit/noop/rollback and SQLSTATE 23505. Frozen-source runs supersede invalidated attempts. Exact-commit full CI is bound separately by the final PR and merge delivery record.

仍不含 HTTP/auth/admin override、祖先创建、锁/CAS、session/cache/通知、Jar/拦截器、属性/stats、完整 Unicode/Locale、上传/部署或发布。此文档属于仓库工程资料，Fern 仅维护使用文档。
HTTP/auth/admin, ancestors, locking/CAS, sessions/caches/notifications, Jar/interceptors, properties/stats, full Unicode/Locale, complete upload/deployment and release remain separate. This is repository engineering documentation; Fern remains usage-only.

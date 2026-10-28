# 普通本地文件读取 / Ordinary local-file read

BIN-197 是 REP-001/STO-001 的有限内部 eager 读取契约：已保存节点身份 → 一次节点查询 → 节点 Actual SHA1 → 调用方持有的真实完整字节流。它不提供 HTTP、鉴权、HEAD、条件或范围请求，也不证明完整 Generic 下载、session/cache、GC 或 provider 链兼容。
BIN-197 defines a bounded internal eager read: stored node identity → one node lookup → node Actual SHA1 → a caller-owned full byte stream. HTTP/auth/HEAD/conditional/range behavior and complete Generic/session/cache/GC/provider compatibility remain outside scope.

## 接口与身份 / Interface and identity

`localrepo.ReadFile(ctx, path, FileReadDependencies{Store, Content})` 返回 `FileReadResult{Found, Source, Body}`。窄依赖直接复用 PostgreSQL `LookupSnapshotSource` 与 filestore `Open`；不新增 registry 查询、完整快照或 MIME/Original 物化链。
The narrow ports reuse PostgreSQL lookup and filestore open directly; no registry lookup or snapshot/MIME/Original materialization is required.

`path` 必须是已成功构造的存储 `repository.Path`；不接收 raw HTTP/create 路径，不再次剥 suffix、不 clean/decode/rebuild。ctx 非 nil，依赖必须初始化；typed-nil 实现不受支持。Content 即使走空流分支也须初始化。
The input is an already-constructed stored Path, passed unchanged. Context must be nonnil and dependencies initialized; typed-nil implementations are unsupported, including on the empty branch.

命中返回同一次查询的权威 Source，保留其 ID/Path/Content、三个 Actual SQL NULL 位及 nullable Metadata，不能用请求身份覆写。`Metadata=nil` 的遗留行可读；MD5/SHA256 值及 Valid 位原样保留，不由空串或形状重新推导。
The same-query Source is authoritative, including stored identity, content, all three SQL nullability flags and nullable metadata. Legacy nil metadata is allowed; other checksum values/flags are copied unchanged.

## 结果与错误顺序 / Results and error order

顺序为 pre-cancel → nil 依赖 → 输入路径守卫 → 一次查询。输入须 repo 非空、file 且非 root；其他存储身份守卫仍由 adapter 负责。所有错误返回全零结果，固定阶段错误保留 Unwrap 原因且不回显输入。
Order is pre-cancellation, dependencies, finite path guard, then lookup. All errors return a zero result; fixed stage messages preserve causes without echoing inputs. Adapter identity checks remain adapter-owned.

`sql.ErrNoRows` 返回全零结果及 nil：Found=false、Source 零、Body=nil；其他查询、folder、schema 错误保持 lookup 阶段原因，丢弃 partial Source。NoRows 之后不再检查取消。
NoRows is successful absence with a zero result; other lookup/folder/schema failures retain their cause. Partial results are discarded, and absence has no later cancellation recheck.

命中后先检查取消，再要求 Source ID>0、有效非 root file Path、SHA1ActualValid=true；不满足则 ErrInvalidFileReadSource。NULL SHA1 是明确本地支持边界，不回退 Original/SHA256/registry，不声称目标精确异常顺序等价。
A hit rechecks cancellation, then guards source identity and non-NULL SHA1. NULL SHA1 is an explicit local exclusion with no alternate-checksum or registry fallback and no target exception-order claim.

只有精确小写 SHA1(empty) `da39a3ee5e6b4b0d3255bfef95601890afd80709` 直接返回独立真实空流，不调用 Open、不要求物理对象，与节点 Size 无关。其他 key 原样仅 Open 一次；present-empty、畸形或大小写 key 由既有 filestore 拒绝并保留 ErrInvalidKey。非空对象缺失保留 os.ErrNotExist，是 open 错误，不能伪装节点缺失或空成功。
Only exact lowercase SHA1(empty) synthesizes an independent empty stream without a physical object. Every other key reaches Open unchanged once. Invalid keys and missing nonempty objects remain distinct errors, separate from absent nodes and NULL SHA1.

节点 Size/digests 是元数据提示，并非本次实测；原样保留，即使与真实字节长度不符。读取不预读、fstat 覆写、重算摘要、截断或补齐；空摘要的非零 Size 也不改成0。
Node size/digests remain metadata hints, never measured-byte claims. No pre-read, metadata replacement, rehash, truncation or padding occurs.

## 流生命周期 / Stream lifetime

Open 为最后可失败阶段，之后直接返回 Found=true、原 Source 与 Body；不追加 ctx 检查或关闭。调用方负责 Read/Close，后续 I/O 错误在返回后发生，不是 ReadFile 已经完成的成功下载。取消不会自动关闭已返回流或使其具备 context-aware 读取。
Open is the last fallible stage. The caller owns subsequent reads/closes and their errors; successful return does not establish complete consumption. Later cancellation does not automatically close or interrupt the stream.

本地 empty Body 用原子关闭位：零长度 Read 在关闭前后均 (0,nil)，打开时非空 Read 为 (0,io.EOF)，关闭后非空 Read 为 (0,os.ErrClosed)，重复 Close 均 nil；不同 Body 相互独立。此规则不泛化为所有 filestore 流的 Close 幂等，也不声称 Java 异常类型/文本或完整 closeQuietly 等价。
The local empty body checks zero-length reads before its atomic closed bit; it returns EOF while open and os.ErrClosed after close for nonempty reads. Close is idempotent and bodies independent. This does not generalize to other bodies or establish Java exception/full-handle close parity.

调用方持有 SQL Tx 与 store 生命周期；读取不提交、回滚、补偿或修复节点。已打开本地文件描述符可跨 Tx 结束继续读；不扩大为 store.Close、并发删除、GC 或未来 provider 的通用保证。
The caller owns transaction/store lifetime. No transaction control, compensation or repair occurs. An opened local descriptor survives transaction end; broader store/GC/provider lifetime guarantees remain unproven.

## 证据与验证状态 / Evidence and validation

固定目标 Artifactory 7.161.26 rev86126900：复用同 WAR 静态调用链及 REA archive 清单；有限实际 ZeroBytesBinaryElement、NullResourceStreamHandle、SimpleResourceStreamHandle 参考含81项独立断言，5个同 WAR JAR 与8个运行类身份核验。仅精确小写空摘要命中；实际 handle 的 size 与输入流字节数独立，关闭归属未提前转移。
Pinned-target static evidence and bounded actual-component observations support the contract: 81 independent assertions, five same-WAR JARs and eight loaded-class identities. Exact-empty matching and supplied-size behavior are observed component facts.

v2 增补关闭前后零长度读，纠正 verifier 最初“关闭后零长度读应失败”的错误假设；实际目标始终返回0。v1 原始证据保留。运行环境为本机 OpenJDK26.0.2.1，不证明包内 JDK25 或部署等价；没有执行完整 BinaryService/provider、服务器、HTTP、权限或目标 SQL。
The v2 probe corrects the verifier's initial closed-zero-length assumption using actual results; v1 evidence is retained. Local JVM26 observations establish neither bundled-JDK25/deployment parity nor full service/provider/server/HTTP/auth/SQL behavior. Raw research remains private.

产品 RED 已在可编译基线捕获空对象缺失；最终9组单元/race、10项可编译故障变异、3组真实 PostgreSQL/文件系统测试及共享检查通过，独立源码审查无未关闭必改项。精确提交与默认分支 CI 另以 PR 检查结果为准；本切片不代表完整下载兼容。
The compiled RED baseline detects the missing-empty-object defect. Nine unit/race groups, ten compiled mutation checks, three real PostgreSQL/filesystem groups and shared checks pass; independent source review has no unresolved required findings. Exact-commit/default-branch CI is tracked in the PR and does not establish full download compatibility.

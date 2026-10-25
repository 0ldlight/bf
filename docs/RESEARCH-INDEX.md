# 研究索引 / Research provenance

此公开索引只保存可公开的身份、摘要、结论和限制。原版安装包、反编译、原始 API 采样与环境记录保存在私有资料库，不从站点提供下载。
This public index contains releasable identity, digests, findings and limitations. Original artifacts, decompilation and raw captures remain private.

## 目标基线 / Target baseline

2026-10-19 原版运行 API 返回 Artifactory **7.161.26**、revision **86126900**；授权返回 Enterprise Plus Trial。该观察仅证明样本身份与当时授权，不能证明所有功能已测试。
The observed runtime reports the target version/revision and trial entitlement. Identity is not feature-validation evidence.

| 字段 / Field | 值 / Value |
|---|---|
| Portable artifact ID | `baseline/artifactory-pro/7.161.26/linux-amd64/package` |
| Source | [JFrog official distribution](https://releases.jfrog.io/artifactory/artifactory-pro/org/artifactory/pro/jfrog-artifactory-pro/7.161.26/jfrog-artifactory-pro-7.161.26-linux.tar.gz) |
| Bytes | `2559888096` |
| SHA-256 | `b967a1fc263ec8cf0314194fc24daf99e10a130ca8e19b076c8a18faf02bc4d9` |
| Inventory | `57506` TAR members; logical member bytes `4143856880` |
| Product properties | version `7.161.26`; revision `86126900`; build `7.161.26-20260907150505-1` |

摘要由完整文件重新计算，TAR 目录逐成员扫描；没有执行包内程序。来源为官方地址，但仅本机摘要不构成独立签名验证。
The digest was computed over the full file and the TAR scanned member by member without executing its programs. A locally computed digest alone is not independent signature verification.

## 组件证据 / Component evidence

以下组件 ID 继承目标包 ID，私有 manifest 保存包内路径、大小、摘要和工具输出；跨主机通过 ID/摘要定位，不能仅复制绝对路径。
Component IDs inherit the package identity. Private manifests retain member paths, sizes, digests and tool output for portable resolution.

| Component ID suffix | 观察 / Observation | 限制 / Limit |
|---|---|---|
| `artifactory-war` | SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37`; 824 ZIP members | 清单/构建元数据，非执行行为 / inventory only |
| `access-war` | SHA-256 `dc871abf684789cf54977ed88ded2524493390c5b35ffa05b9893c391d692ca5`; 498 ZIP members | 清单/构建元数据，非执行行为 / inventory only |
| `router-go` | SHA-256 `0808170da829e6c4bc658cb5a6e0a9daf792ddaa3c630b9baf7b2336bf1f1b90`; ELF amd64, embedded `go1.26.7` | 嵌入字符串不证明工具链或依赖真实性 / embedded metadata only |
| `metadata-go` | ELF amd64, embedded `go1.26.6`, module version `(devel)` | 未恢复源码或函数行为 / no recovered source/behavior |

REA Router 证据 ID：`ev_8d5f8a6a49ade8c62588c355c2f0057a4b5942e347c0b0cab4bc9f75494ac836`。
REA Metadata 证据 ID：`ev_998440c643db3eb012cf6df0effd5eb5b28e8855ef23de2ec59cc5369297ee06`。

WAR manifest 的 `Build-Jdk-Spec` 均为 `25`。抽样 Artifactory 第一方 JAR 标记为 `7.161.15`，Access JAR 标记为 `7.191.16`。这些是组件版本，不能直接当作产品版本或据此断言包损坏。
WAR manifests report JDK 25. Sampled Artifactory and Access JARs use their own versions; component/product version layers must be kept distinct.

包内还存在 frontend、event、topology、jfconfig、jfconnect、evidence 等目录。存在某组件不等于 BinFlow 必须逐服务重建；按职责、调用与目标授权调查必需能力，映射到[能力矩阵 PLAT-001](CAPABILITY-MATRIX.md)。
Bundled presence does not establish implementation topology. Establish required behavior and dependencies before choosing service boundaries.

## 历史恢复资料 / Historical recovery

历史目录的根 README 声明 `7.161.24`，恢复 POM 为 `7.161.24-recovered`，另有内部标签 `7.161.14`。仅对根元数据文件计算了摘要，未宣称已校验整个历史树。
Historical root metadata reports these labels. Only selected root metadata files were hashed, not the full recovered tree.

这些资料是线索：缺少 `sourcesContent` 的前端 sourcemap 近似重建不等于可构建 TypeScript；Ghidra C 伪代码不等于恢复 Go 源码。多层版本标记需要归属核实，不能替代目标包和运行证据。
Approximate sourcemap reconstruction is not buildable TypeScript, and Ghidra pseudocode is not recovered Go source. Establish version-layer provenance and validate against the target.

## 本地内容存储 / Local filestore

2026-10-20 按所有者要求核对目标包字节码及专用实例，确认本地 provider 的 SHA-1 物理寻址与多摘要模型；见[存储对齐记录](FILESTORE-CONTRACT.md)。记录列出暂存/移动/数据库边界及证据限制，未宣布内容存储实现、并发写、GC 或 HA 兼容完成。
The source-grounded filestore note distinguishes static findings, bounded runtime evidence and open verification.

## PostgreSQL 元数据登记 / PostgreSQL metadata registry

2026-10-21 按目标 `binary-store-core` 4.372.5 字节码静态证据实现元数据登记分支（查找/插入冲突返回输入/SHA-256 回填），并以真实 PostgreSQL 17.10 运行验证；规范来源与证据边界见[PostgreSQL 登记记录](POSTGRES-REGISTRY.md)。`binary-store-api` 4.372.5（SHA-256 `3606d807…`）的 `javap -v` 进一步显示 `TransactionHandler.invokeInNewTransaction` 的 `@Transactional(REQUIRES_NEW)` 声明在接口上且未指定隔离级别：它只佐证独立新事务，目标实际运行时隔离级别未经验证，本地 READ COMMITTED 是显式适配器策略。目标原库的数据库事务/失败路径未实测，静态证据不等价于目标运行时行为。
The metadata-registration branches follow static bytecode evidence of the target's binary-store-core 4.372.5 and were verified against a real PostgreSQL 17.10 instance; the [PostgreSQL registry note](POSTGRES-REGISTRY.md) is the canonical source. `javap -v` of `binary-store-api` 4.372.5 (SHA-256 `3606d807…`) additionally shows `TransactionHandler.invokeInNewTransaction` declaring `@Transactional(REQUIRES_NEW)` on the interface without pinning an isolation level: it corroborates only an independent new transaction, the target's effective runtime isolation is unverified, and local READ COMMITTED is explicit adapter policy. The target's own database-transaction/failure paths remain untested at runtime; static evidence is not target runtime behavior.

## 仓库节点路径 / Repository node path

2026-10-21 按目标 `NodePath`/`NodeUtils` 静态字节码与 23 例 JVM 回放（私有证据）实现内部路径身份值模型；23 例回放仅在本地 OpenJDK 26.0.2.1 上运行目标 WAR 内 storage-db 组件的精确路径类对象，非运行中 Artifactory HTTP/数据库采样。规范来源与证据边界见[节点路径记录](REPOSITORY-NODE-PATH.md)。仅路径身份，不宣称仓库保存、HTTP、SQL 或目标畸形输入运行时对等。
The internal path-identity value model follows static bytecode of the target's `NodePath`/`NodeUtils` plus a 23-case private JVM replay that ran only exact target path class objects from the storage-db component inside the target WAR under a local OpenJDK 26.0.2.1 — not live Artifactory HTTP/DB sampling. The [node-path note](REPOSITORY-NODE-PATH.md) is the canonical source. Path identity only: no repository-save, HTTP, SQL or target malformed-input runtime parity is claimed.

## 事务内文件引用 / Transaction-scoped file references

2026-10-21 按目标 `NodesDao`/`BaseDao` 静态字节码与打包全新 `postgresql.sql`（SHA-256 `667a150c…dbf133cf` 行 23..53，仅全新模式，安装库未查询）实现文件引用的最小 nodes 投影：调用方拥有事务与节点 ID，成功计数在 Commit 前是临时的，字面空↔点 SQL 边界与 `sha1_actual → binaries` 外键。规范来源、本地策略与边界见[文件引用记录](NODE-REFERENCES.md)。不宣称完整目标 Node/HTTP/schema 导入、PathValidator/认证替代或目标数据库故障运行时对等。
The minimal file-reference nodes projection follows static bytecode of the target's `NodesDao`/`BaseDao` plus the packaged fresh `postgresql.sql` (SHA-256 `667a150c…dbf133cf`, lines 23..53 — fresh schema only, the installed DB was never queried): caller-owned transactions and node IDs, provisional successful counts until commit, the literal empty↔dot SQL boundary and the `sha1_actual → binaries` FK. The [node-references note](NODE-REFERENCES.md) is the canonical source for local policies and boundaries. No full target Node/HTTP/schema import, PathValidator/auth replacement, or target DB-fault runtime parity is claimed.

## 节点 ID 分配 / Node ID allocation

2026-10-22 校验同一目标 WAR 的 storage-db、addon-ha、storage-common 和 config 组件，核实 general 号段、Simple/HA 分支及事件序列区别；13 项受控 JVM 回放直接执行目标分配器 class，周边配置/异常辅助类型/JDBC 使用显式替身，含 8 实例分配 40 个唯一 ID。规范来源与限制见[节点 ID 契约](NODE-ID-CONTRACT.md)。未实现分配器，未执行原版真实数据库/Spring/HA 故障验证。
The same target WAR supplies the general-range, Simple/HA and separate event-sequence evidence. Thirteen controlled JVM observations execute exact allocator class bytes with explicit surrounding doubles, including forty unique IDs from eight instances. The [node-ID contract](NODE-ID-CONTRACT.md) owns the scope and limits. No allocator implementation or live target DB/Spring/HA failure validation is claimed.

2026-10-22 上述调查的第一实现切片落地：显式本地 PostgreSQL 已提交号段预留原语 `Reservations`（调用方 `*sql.DB`、自有 READ COMMITTED 事务的条件更新、仅 Commit 成功返回闭区间号段）。契约与验证边界见[号段预留记录](ID-RESERVATIONS.md)；这是原创本地政策，不宣称目标分配器/autocommit/HA 运行时对等。
The first implementation slice of that investigation landed: the explicit local PostgreSQL committed ID-range reservation primitive `Reservations` (caller-owned `*sql.DB`, a conditional update inside an owned READ COMMITTED transaction, the inclusive range returned only after a successful Commit). The [reservation note](ID-RESERVATIONS.md) owns the contract and verification limits; this is original local policy with no target allocator/autocommit/HA runtime parity claimed.

## 存储路径校验 / Storage path validation

2026-10-22 按目标 `artifactory-common` 7.161.15 内精确 `PathValidator` 类（含其真实打包 `commons-lang3` 3.20.0）的私有受控纯方法回放（270524 个唯一有效 UTF-8 输入，本地 OpenJDK 26.0.2.1，隔离 URLClassLoader，无依赖替身）实现序列化存储路径检查器 `ValidateStoragePath`：未来保存消费者在 ID 消费/编排之前调用；`NewPath` 身份构造器不变；无效 UTF-8 为本地拒绝策略。规范来源、静态保存顺序证据与限制见[存储路径校验记录](STORAGE-PATH-VALIDATION.md)。不宣称完整 FileService/HTTP/数据库回放、认证或文件系统安全。
Following a private controlled pure-method replay of the exact `PathValidator` class in the target's `artifactory-common` 7.161.15 (with its real packaged `commons-lang3` 3.20.0 — 270524 unique valid-UTF-8 inputs, local OpenJDK 26.0.2.1, isolated URLClassLoader, no dependency doubles), the serialized storage-path checker `ValidateStoragePath` landed: the future save consumer calls it before ID consumption/orchestration; the `NewPath` identity constructor is unchanged; invalid UTF-8 is a local rejection policy. The [storage-path validation note](STORAGE-PATH-VALIDATION.md) is the canonical source for the contract, the static save-order evidence and the limits. No full FileService/HTTP/DB replay, auth, or filesystem-safety claim is made.

## 文件节点保存链 / File-node save chain

2026-10-22 校验同一目标 WAR 的 storage-db/storage-common/papi 及真实打包依赖，静态梳理 `FileServiceImpl.createFile/updateFile` 顺序（校验先于 ID、映射失败在 ID 之后、异常类型分支）、私有 `fileInfoToNode` 元数据映射与 RepoPath 入口变体；受控回放 60 断言/36 观察，直接运行精确目标类与私有映射方法（13 个真实 JAR、无替身，OpenJDK 26.0.2.1/Locale ROOT）。规范来源、合成示例与限制见[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)。未执行 createFile/DAO/Spring/真实 DB/HTTP；未配对 UTF-16 actor 的边界已在[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)中部分消解（仅所选参数 profile——真实 JDBC/JSON/数据库接受仍未知）。
The same target WAR's storage-db/storage-common/papi components and real packaged dependencies were verified statically for the `FileServiceImpl.createFile/updateFile` ordering (validation before the ID, mapping failures after it, exception-type branches), the private `fileInfoToNode` metadata mapping and the RepoPath entry variants; a controlled replay of 60 assertions/36 observations ran the exact target classes and the private mapper directly (13 real JARs, no doubles, OpenJDK 26.0.2.1/Locale ROOT). The [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) owns the canonical facts, synthetic examples and limits. Neither createFile/DAO/Spring nor real DB/HTTP ran; the unpaired-UTF-16 actor boundary is now partially resolved in [save caller and parameter encoding](SAVE-CALLER-ENCODING.md) (selected parameter profile only — real JDBC/JSON/DB acceptance still unknown).

## 保存调用方与参数编码 / Save caller and parameter encoding

2026-10-22 BIN-138 研究切片：静态梳理上传调用方到所选 PostgreSQL 参数编码边界（`NodesDao` actor getter → `JdbcHelper` 参数化 `executeUpdate` → `setObject`/`setString`/`setNull` → `SimpleParameterList` 字面量与惰性 UTF-8 缓存）、HTTP 请求路径构造与 actor/管理员头门控；受控回放 192 断言/18 案例，直接运行目标 Node 与实际驱动参数表（14 个真实 JAR = BIN-137 的 13 个同 WAR 依赖 + 分发包 `app/artifactory/tomcat/lib` 驱动，OpenJDK 26.0.2.1/Locale ROOT，无替身）。分发包随附驱动只证明 SHIPPED，不证明有效配置。规范来源与边界见[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)。未执行真实 JDBC 写入/servlet/Spring/鉴权/数据库；JSON/数据库接受/其他协议调用方仍未知。
A BIN-138 research slice (2026-10-22): static mapping from the upload caller to the selected PostgreSQL parameter-encoding boundary (`NodesDao` actor getters → `JdbcHelper` parameterized `executeUpdate` → `setObject`/`setString`/`setNull` → `SimpleParameterList` literal storage with a lazy UTF-8 cache), the HTTP request-path construction and the actor/admin-header guards; a controlled replay of 192 assertions/18 cases ran the target Node and the actual driver parameter list directly (14 real JARs = BIN-137's 13 same-WAR dependencies plus the distribution `app/artifactory/tomcat/lib` driver, OpenJDK 26.0.2.1/Locale ROOT, no doubles). The bundled driver proves SHIPPED availability only, not effective configuration. The [save-caller-encoding note](SAVE-CALLER-ENCODING.md) owns the canonical facts and limits. No real JDBC write/servlet/Spring/auth/database execution; JSON/database acceptance/other-protocol callers remain unknown.

## Actor 值 / Actor value

2026-10-22 BIN-139 实现切片：按所有者批准，把[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)与[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)已绑定的 actor 表示事实实现为 `internal/repository` 的可空原始 UTF-16 纯值 `Actor`（nil=null、非 nil 空=空串、<=64 码元原样、>64 首码元 61 加三个 `0x002e`、未配对代理原样保留、构造即复制且保留有界、无任何 String/UTF-8/JSON/SQL 转换）。契约与边界见[Actor 值记录](ACTOR-VALUE.md)。仅纯值：既有 FileReference/SQL/保存编排/HTTP 不变，元数据模型/SQL/传输仍开放。根独立对照已完成：135966 个原始 UTF-16 输入与两个真实目标 Node actor getter 逐码元一致（271932 次 getter 对照），race 探针另核实所有权与并发读者；详见[Actor 值记录](ACTOR-VALUE.md)。
A BIN-139 implementation slice (2026-10-22): with owner approval, the already-bound actor representation facts from the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) and [save caller and parameter encoding](SAVE-CALLER-ENCODING.md) landed as the nullable raw-UTF-16 pure value `Actor` in `internal/repository` (nil=null, nonnil empty=empty string, <=64 units verbatim, above that the first 61 units plus three `0x002e`, unpaired surrogates kept verbatim, copy-on-construction with bounded retention, no String/UTF-8/JSON/SQL conversion). The [Actor value note](ACTOR-VALUE.md) owns the contract and boundaries. Pure value only: the existing FileReference/SQL/save orchestration/HTTP stay unchanged while the metadata model/SQL/transport remain open. The root's independent comparison completed: 135966 raw UTF-16 inputs matched both actual target Node actor getters unit by unit (271932 getter comparisons), with a race probe also verifying ownership and concurrent readers; see the canonical [Actor value note](ACTOR-VALUE.md).

## 文件元数据值 / File metadata value

2026-10-22 BIN-140 实现切片：按所有者批准，把[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)的逐节点元数据表示实现为 `internal/repository` 的显式快照纯值 `FileMetadata`（三个独立 signed int64 毫秒逐字复制、复用既有 `Actor`、两个可空原始 UTF-16 original 构造与读取各复制、任意长度不缩写不归一化）。契约与边界见[文件元数据记录](FILE-METADATA.md)。仅纯值：消费者/SQL/迁移/传输/保存编排/HTTP 仍开放；根独立验证 68721 组冻结七字段参考、481047 次字段比较通过，所有权/竞态和两项故障回归通过；详见规范记录。
A BIN-140 implementation slice (2026-10-22): with owner approval, the per-node metadata representation from the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) landed as the explicit-snapshot pure value `FileMetadata` in `internal/repository` (three independent signed int64 milliseconds copied verbatim, reuse of the existing `Actor`, two nullable raw-UTF-16 originals defensively copied on construction and on every read, no abbreviation or normalization at any length). The [file-metadata note](FILE-METADATA.md) owns the contract and boundaries. Pure value only: consumers/SQL/migration/transport/save orchestration/HTTP remain open; independent verification passed for 68721 frozen seven-field cases and 481047 field comparisons, plus ownership/race checks and two fault regressions; see the canonical note.

## 文件元数据持久化 / File metadata persistence

2026-10-23 BIN-142：已按所有者批准实现文件元数据七列投影、PostgreSQL 私有编码与显式升级/受保护回退。唯一规范、参考来源、独立复验及门禁边界见[文件元数据持久化记录](FILE-METADATA-PERSISTENCE.md)；主分支合并与发布由所有者另审。
BIN-142 (2026-10-23) implements the owner-approved seven-column file-metadata projection, private PostgreSQL codec and explicit upgrade/guarded downgrade. The [file-metadata persistence note](FILE-METADATA-PERSISTENCE.md) owns the contract, reference provenance, independent replay and gate boundaries; main-branch merging and release require separate owner approval.

## 普通更新消费者 / Ordinary-update consumer

2026-10-23 BIN-153–159：复用同一目标 WAR 的保存链字节码，并定向补查上游调用方：服务方法使用调用方既有 ID、更新计数原样返回；上层构造时校验快照路径，实际 UPDATE 整体写回元数据。按所有者批准实现的薄消费者及本地策略见[普通更新记录](FILE-UPDATE.md)。本地真实 PostgreSQL 验证覆盖调用方回滚、延迟约束提交失败及非规范登记胜者拒绝；这些证据不证明目标有效 Spring 装配、数据库运行行为或完整上传/移动兼容。原始材料和逐项来源绑定保留在私有研究记录。
BIN-153–159 (2026-10-23) reuse the same target WAR's save-chain bytecode and add focused caller inspection: the service uses a caller-supplied existing ID and returns the update count verbatim; upper construction validates the snapshot path and an actual UPDATE rewrites the metadata. The owner-approved thin consumer and local policies are described in the [ordinary-update note](FILE-UPDATE.md). Local real-PostgreSQL evidence covers caller rollback, deferred-constraint commit failure and rejection of a noncanonical registration winner. This does not establish effective target Spring wiring, target database execution or complete upload/move parity. Raw materials and individual provenance bindings remain private.

## 证据规则 / Evidence rules

1. 私有 manifest 记录来源、获取时间、父制品、工具/版本、摘要、证据 ID 和限制；原始输出保持私有。
   Preserve provenance/tool identity/digests and limitations privately.
2. 分开标记静态事实、实际运行观察与推论；任何兼容结论都关联具体样本和测试。
   Separate static facts, runtime observations and inference.
3. 调查输出形成原创行为契约与边界测试；公开仓库不复制受控实现或原始采样。
   Produce original behavioral contracts and tests without publishing controlled implementations/captures.

[P0 计划](P0-PLAN.md)规定研究出口；后续完整依赖清单、前端/其他平台组件及行为采样仍需追加证据，当前索引不宣称逆向或兼容完成。
Further dependency/frontend/platform analysis and behavioral sampling remain open; this inventory is not completed reverse engineering or parity.

- [本地 provider / Local provider](LOCAL-FILESTORE.md)：内部 Put/Open 及故障/并发/路径验证；HTTP、数据库引用、GC 与 HA 仍待接入。

## 文件保存分派与变化判断 / File-save dispatch and dirty checking

2026-10-24 BIN-160/161/162 复用同一目标冻结字节码，定向补查两个比较链直接依赖，区分加载行与请求身份、完整原始快照比较、条件性路径再映射及本地遗留元数据策略。来源、结论与未观察项以[文件保存分派契约](FILE-SAVE-DISPATCH-CONTRACT.md)为准。新增导出已按父制品条目和类 SHA-256 独立复核；本轮无新目标运行或数据库探针，不预设新的 API 或分派实现。
BIN-160/161/162 reuse frozen bytecode from the same target and inspect two direct comparison dependencies, separating loaded/request identity, raw-snapshot comparison, conditional path remapping and local legacy-metadata policy. The [file-save dispatch contract](FILE-SAVE-DISPATCH-CONTRACT.md) owns the findings and unknowns. New exports were independently checked against parent artifact entries and class SHA-256 digests. No new target runtime or database probe was performed, and no new API or dispatcher implementation is implied.

## 文件信息原始快照 / File-info raw snapshot

2026-10-24 BIN-163–167：按所有者批准的原始 `FileInfoSnapshot` 纯值与受限 `IsIdentical` 比较器，冻结 API、字段/画像/零值/错误与验证状态以[文件信息原始快照](FILE-INFO-SNAPSHOT.md)为唯一规范。参考侧证据经根批准：19 个真实 JAR 与钉定 WAR 条目逐字节核对、20 个类摘要核对、144 行真实谓词观察（created 两种画像各 72 行，参考 JSON SHA-256 `d9adb8e5…`）；单独运行主 ConstantValues 读取抛 home-not-bound，真实 ItemInfoUtils 捕获 Exception 回退系统属性——部署生效配置未观察。Go 纯值实现、race、144 行独立差分与共享检查通过；PR CI 仍为合并门禁，完整分派器及接线尚未实现。
BIN-163–167 (2026-10-24): the owner-approved raw `FileInfoSnapshot` pure value and restricted `IsIdentical` comparator; the [file-info snapshot note](FILE-INFO-SNAPSHOT.md) owns the frozen API, fields/profile/zero/error policy and verification state. Root-approved reference evidence: 19 real JARs byte-matched to pinned-WAR entries, 20 class digest checks, and 144 real predicate rows (72 per created profile; reference JSON SHA-256 `d9adb8e5…`); the standalone primary ConstantValues lookup throws home-not-bound while the real ItemInfoUtils catches Exception and falls back to a system property — effective deployment configuration unobserved. The Go pure value, race tests, 144-row independent differential and shared checks pass; PR CI remains a merge gate, and full dispatch and wiring remain unimplemented.

## 七字段写侧投影 / Seven-field write projection

2026-10-25 BIN-168–171：已批准从原始快照到既有 FileMetadata 的纯投影，规则与验证限制见[映射记录](FILE-SNAPSHOT-MAPPING.md)。受控真实 mapper 对 34 组输入返回 28 个 Node、拒绝 6 组缺失/空白 SHA1；仅成功行作七字段参考。13 个 JAR 对齐固定 WAR、14 类摘要核对；独立 Go 对照 28 组真实成功结果及 34 组本地投影策略。缺 SHA1→nil 是明确的本地投影扩展，不是原版保存成功行为。原始语料保持私有；完整分派/旧行重建/HTTP 仍开放。
BIN-168–171 (2026-10-25): the approved pure projection bridges raw snapshots and FileMetadata; the [mapping note](FILE-SNAPSHOT-MAPPING.md) owns rules and limits. Controlled real mapping returned 28 Nodes and rejected six absent/blank-SHA1 inputs. Only successful rows supply seven-field references. Thirteen JARs match the pinned WAR, 14 class digests were checked, and independent Go comparison covers both successful target rows and 34 local-policy fixtures. Absent-SHA1-to-nil is a local projection extension, never a target-save success claim. Raw data stays private; full dispatch, legacy reconstruction and HTTP remain open.

## 2026-10-25 — 显式快照更新 / Explicit snapshot update

BIN-172–175 将原始比较与七字段投影接入已有文件更新；写操作数仅由同一个 Desired 产生。固定包内身份与既有静态锚点已复核，本轮无新增目标执行；20 项独立公共 API 场景、三种可编译故障及三项真实 PostgreSQL 接缝已验证。原始历史逆推不等于加载域构造；后者及完整保存生命周期仍开放。规范见[快照更新](FILE-SNAPSHOT-UPDATE.md)，最终单元/CI 状态见 PR。
The explicit seam connects raw comparison and metadata projection to the existing updater using one Desired state. Package identity and prior static anchors were rechecked without new target execution. Independent API, fault and real-database seams were validated; loaded-state construction and full save lifecycle remain open. The canonical note and final PR checks own the implementation contract and validation state.

## 2026-10-26 — 加载域与空性读取 / Loaded domain and nullable reads

BIN-176/177 核实固定目标的 Node 读取与复制链，根有限参考十次调用（九成功、一目录拒绝），十四实际 JAR/十一类来源经核对；另有十个冻结类摘要复核。观察到三个条目保留 null/empty、lastModified 非正回退 created、Original 的长度与受控 Locale 规则；MIME getter/copy 的源写回仅作静态结论。未执行 JDBC 或完整保存。根以现有更新门复现 NULL→空串导致 false-clean，先保留 SQL 空性再设计加载组装。新读接口实现/验证状态见[规范记录](FILE-SNAPSHOT-SOURCE.md)及 PR，原始资料私有。
Bounded real-class observations and prior static copy evidence identify the NULL-preservation prerequisite. Controlled Locale and immediate MIME observations do not establish deployment values or full save parity; the canonical source note and PR own implementation status.

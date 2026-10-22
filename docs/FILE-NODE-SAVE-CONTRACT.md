# 文件节点保存契约 / File-node save contract

状态 / State: BIN-137 调查切片；**本切片不新增任何保存服务、SQL 或运行时编排实现**（项目既有的最小 FileReference/Store 投影保持不变，见下文）。本页记录目标 `FileServiceImpl.createFile/updateFile`、私有实例方法 `fileInfoToNode` 元数据映射与 RepoPath 入口变体的静态事实、受控回放观察与实现缺口；不提出新模型、接口、ADR 或架构决定。
Research slice only; **this slice ADDS no save-service, SQL or runtime-orchestration implementation** (the project's existing minimal FileReference/Store projection is unchanged — see below). This page records static facts, controlled-replay observations and implementation gaps for the target's `FileServiceImpl.createFile/updateFile`, the private-instance-method `fileInfoToNode` metadata mapping and the RepoPath entry variants. It proposes no new model, interface, ADR or architecture decision.

## 目标与证据身份 / Target and evidence identities

目标为 Artifactory 7.161.26 revision 86126900，与[节点 ID 契约](NODE-ID-CONTRACT.md)、[存储路径校验](STORAGE-PATH-VALIDATION.md)同一 WAR；父级/来源上下文见[研究索引](RESEARCH-INDEX.md)。以下摘要为机器生成的私有研究证据值，逐字复制：
Target: Artifactory 7.161.26 revision 86126900, the same WAR as the [node-ID contract](NODE-ID-CONTRACT.md) and [storage-path validation](STORAGE-PATH-VALIDATION.md); parent/source context is in the [research index](RESEARCH-INDEX.md). The digests below are machine-generated private research evidence values, copied verbatim:

| 组件 / Component | SHA-256 |
|---|---|
| 目标 WAR / Target WAR | `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37` |
| artifactory-storage-db-7.161.15.jar | `7687e761d65c1c48819c9dc51ca9478bc31965402c528aae914878052c8164e9` |
| artifactory-storage-common-7.161.15.jar | `7c157b1fb2be2b6f0b7f99eb8edd34ff0ac46bff2be02f6da1e7cddd8b50dc1e` |
| artifactory-papi-7.161.15.jar | `494c1eccb978c3105385ad59c958a0b3272dc5fdfd1d177681fa9d859aa236e8` |
| jfrog-http-client-13.21.0.jar | `434cd89d56efc5b6e78c6ba7b5d45aa612db550f4b1266902908fb4c10b30511` |
| commons-lang3-3.20.0.jar | `69e5c9fa35da7a51a5fd2099dfe56a2d8d32cf233e2f6d770e796146440263f4` |

## RepoPath 入口变体 / RepoPath entry variants

以下全部是**辅助类型观察**：已覆盖的静态调用方（见[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)）选用的重载已静态梳理，各协议处理器在真实运行时的选择仍未观察，本页不作一般 HTTP 保证。本页不改动也不重复[路径身份值模型](REPOSITORY-NODE-PATH.md)与[原始校验器](STORAGE-PATH-VALIDATION.md)契约。
All entries below are **helper observations**: the overloads used by the static callers covered so far (see [save caller and parameter encoding](SAVE-CALLER-ENCODING.md)) are mapped statically, while the per-protocol handlers' real runtime selections remain unobserved — no general HTTP guarantee is made here. This page neither changes nor duplicates the [path identity model](REPOSITORY-NODE-PATH.md) or the [raw validator](STORAGE-PATH-VALIDATION.md) contract.

| 入口 / Entry | 语义 / Semantics |
|---|---|
| 工厂单串 create / single-string factory | 先 `PathUtils.formatPath`（反斜杠→'/'、折叠重复斜杠）再去首部斜杠、按第一个 '/' 拆 repo/path；不 URL 解码、不折叠 '..'。回放：`repo/a\b`→path `a/b`；`//repo///a//b/`→`repo/a/b/`（folder）；`repo/../x`、`repo/a%2fb` 原样保留；`repo/a/ `（尾空格）→file 路径 `repo/a`。 Applies formatPath (backslash→'/', collapsing repeats), strips leading slashes, splits the first '/' into repo/path; no URL decoding or '..' collapsing. Replay: `repo/a\b`→path `a/b`; `//repo///a//b/`→`repo/a/b/` (folder); `repo/../x` and `repo/a%2fb` survive verbatim; `repo/a/ ` (trailing space)→the file path `repo/a`. |
| 构造器 2 参 `RepoPathImpl(String,String)` / 2-arg constructor | folder 标志取**修剪前**原始 path 是否以 '/' 结尾；repo 键用 `StringUtils.trimToNull`（Java `String.trim` 语义：移除边界所有 ≤U+0020 的码元，含 ASCII 控制字符，非仅空白；结果 null 被拒绝），path 用 trimToEmpty 加边界 trimSlashes；内部重复斜杠、点段、反斜杠与大小写/百分号不归一。回放：`('repo',' /a//b/ ')`→path `a//b` 且 file=true（原始串不以 '/' 结尾）；`'a/'`→folder 且串行化 `repo/a/`。 The folder flag comes from the RAW pre-trim trailing '/'; the repo key uses `StringUtils.trimToNull` (Java `String.trim` semantics: boundary CODE UNITS ≤U+0020, ASCII controls included — not merely whitespace — with the resulting null rejected), the path trimToEmpty plus boundary trimSlashes; inner repeats, dot segments, backslashes, case and percent escapes stay. Replay: `('repo',' /a//b/ ')`→path `a//b`, file=true (the raw input did not end '/'); `'a/'`→folder serializing `repo/a/`. |
| 构造器 3 参 `RepoPathImpl(String,String,boolean)` / 3-arg constructor | 显式 boolean folder 标志**直接取代**原始尾斜杠判断（回放：显式 false 时边界斜杠被去除仍是 file；显式标志与根谓词的相互作用见下方"根与谓词重叠"行）。 The explicit boolean folder flag REPLACES the raw-trailing-slash test (replay: explicit false strips the boundary slash yet stays file; its interplay with the root predicates is qualified in the root-predicate-overlap row below). |
| 工厂 3 参 `create(String,String,String)` / 3-string factory | **无 boolean 标志**：修剪 parent 边界斜杠；空或 '.' 的 parent 退为仅 name，否则 parent+'/'+name 再走 2 参构造器；null name 配非空 parent 经 Java 串接得字面 `a/null`。 NO boolean flag: the parent's boundary slashes are trimmed; a blank or '.' parent selects the name alone, otherwise parent+'/'+name re-enters the 2-arg constructor; a null name with a non-blank parent yields the literal `a/null` via Java concatenation. |
| 根与谓词重叠 / root predicate overlap | `isRoot` 用 Java blank；isFolder=原始 folder 标志**或** root，isFile=非原始 folder 标志——由 null/空 path 构造的 root 可 file/folder 同真（回放证实）。`toPath`=repoKey+'/'+path，非根 folder 末尾补 '/'。不得在既有身份契约之外强迫谓词互补。 `isRoot` uses Java blank; isFolder is the raw flag OR root while isFile is NOT the raw flag — a root built from a null/empty path can be BOTH file and folder (replay-confirmed). `toPath` is repoKey+'/'+path plus a final '/' for a non-root folder. Predicates must not be forced to complement outside the existing identity contract. |
| FileInfo 强制文件 / FileInfo file coercion | `FileInfoImpl(path)` 经 `RepoPathUtils.toFileRepoPath` 强制为文件并保留原 `repoPath.getName()`（回放：folder `RepoPath('f/')` 得文件路径 `repo/f`）；size 0、id -1；created/lastModified 取同一 currentTimeSupplier 样本，lastUpdated 在 additionalInfo 构造器内另行采样——不得推三时相等。`setCreated` 原样复制；`setLastModified(x)` 用 x>0?x:当前 created；getter 均来自该状态。 `FileInfoImpl(path)` coerces the RepoPath to a file via `RepoPathUtils.toFileRepoPath`, retaining the original `getName()` (replay: folder `RepoPath('f/')` yields file path `repo/f`); size 0, id -1; created/lastModified take one currentTimeSupplier sample while lastUpdated samples separately in the additionalInfo constructor — no universal three-time equality. `setCreated` copies exactly; `setLastModified(x)` uses x>0?x:the current created; getters read that state. |

## 静态创建/更新序列 / Static create/update sequence

- `createFile(FileInfo)`（字节码顺序）：非空 file 检查 → `PathValidator.validate(file.getRepoPath().toPath())` → `dbService.nextId` → 条件插入模式检查。普通分支 `createFileNode` → `fileInfoToNode` → `NodesDao.create`。**首个 ID 在校验和映射/NodeBuilder.build 之前已被消费**，缺 SHA1 等映射失败发生在取得 ID 之后；无空洞编号不是契约。条件插入是另一分支，其辅助方法再次申请 ID——分配器事实归[节点 ID 契约](NODE-ID-CONTRACT.md)，本页不重复。实际模式/有效装配未知。
  `createFile(FileInfo)` (bytecode order): a non-null file check → `PathValidator.validate(...toPath())` → `dbService.nextId` → the conditional-insert mode check. The ordinary branch runs `createFileNode` → `fileInfoToNode` → `NodesDao.create`. **The initial ID is consumed before checksum mapping/NodeBuilder.build**, so mapping failures such as a missing SHA1 happen after the ID is taken; gap-free numbering is not a contract. The conditional insert is a separate branch whose helper requests another ID — allocator facts belong to the [node-ID contract](NODE-ID-CONTRACT.md). The actual mode/effective wiring is unknown.
- `createFileNode` 静态异常分支：`SQLIntegrityConstraintViolationException`→`SQLIntegrityException`；其他 `SQLException`→`VfsException`；受影响行数必须等于 1 否则 `IllegalStateException`；等于 1 时返回传入的 nodeId。这些是静态异常类型分支，**不是**观察到的驱动映射、HTTP 状态或事务/提交语义。
  Static branches: `SQLIntegrityConstraintViolationException`→`SQLIntegrityException`; other `SQLException`→`VfsException`; the affected count must equal 1 or `IllegalStateException`; on 1 the supplied nodeId returns. These are static exception-type branches — **not** observed driver mappings, HTTP statuses or transaction/commit semantics.
- `updateFile(long, FileInfo)`：`fileInfoToNode`(既有 ID)→`NodesDao.update`→返回计数；`SQLException` 包为 `VfsException`。**本方法**无 PathValidator、无 nextId、无 count==1 检查；不得据此断言上层调用方跳过校验或公开更新接受非法输入。
  `updateFile(long, FileInfo)`: `fileInfoToNode` (existing ID) → `NodesDao.update` → returns the count; `SQLException` wraps in `VfsException`. **This method** has no PathValidator, no nextId and no count==1 check; that must not be read as higher-level callers skipping validation or a public update accepting invalid input.
- 本页不含任何运行时 HTTP/数据库保证；真实事务、提交与错误→HTTP 行为未观察。
  No runtime HTTP/database guarantee is made here; real transactions, commits and error→HTTP behavior were never observed.

## 元数据映射 / Metadata mapping

`fileInfoToNode`（`FileService` 上的**私有实例方法**，回放以默认未注入实例直接调用）字段来源 / field sources of `fileInfoToNode`, the **private instance method** invoked on a default un-injected instance during replay:

| Node 字段 / Node field | 来源 / Source |
|---|---|
| node_id | 调用方/服务提供的 ID（update 用既有 ID）。 The caller/service-supplied ID (existing ID on update). |
| nodePath | `file.getRepoPath()`；NodeBuilder 若未提供则从 repo/path/name/file 重建。 NodeBuilder rebuilds from repo/path/name/file if absent. |
| file / bin_length | file=true；length=`getSize()` 原样复制。 file=true; length copies `getSize()` verbatim. |
| created/modified/updated | `getCreated()/getLastModified()/getLastUpdated()`；映射**不**替换当前时间。 the getters; mapping never substitutes a current time. |
| createdBy/modifiedBy | `getCreatedBy()/getModifiedBy()`，见下方缩写。 the getters, see abbreviation below. |
| SHA1 | **必须**存在 ChecksumInfo 且 `getActual()` 非 Java 空白，否则 `VfsException`；actual=`getActual()`（大小写原样）；original=`getOriginalOrNoOrig()`（NO_ORIG 标记原样保留）。 **Required**: a ChecksumInfo with non-Java-blank `getActual()`, else `VfsException`; actual keeps its case; original is `getOriginalOrNoOrig()` with the NO_ORIG marker preserved verbatim. |
| MD5 | 可选：对象存在则 actual 与 originalOrNoOrig 都映射（各自可为 null），缺席则两列为 null。 Optional: when present both actual and originalOrNoOrig map (either may be null); absent leaves both null. |
| SHA256 | 可选：`getActual()`→Node.sha2→SQL `sha256`；无 original SHA256 字段（回放：D*64 映射，其 original 不映射）。 Optional: `getActual()`→Node.sha2→SQL `sha256`; no original-SHA256 field (replay: D*64 mapped, its original not). |
| MIME | **此处不读取**。 Not read here. |

- 映射不调用 `checksumsMatch`，不做校验和语法/大小/时序校验。`ChecksumInfo` 真校验构造器拒绝非 null 的畸形 actual（ChecksumType 语法）、保留 actual 大小写、把合法 original 十六进制小写化并保留畸形/空/null original；`getOriginal` 仅对 NO_ORIG 标记返回 actual；false-validation 构造器放行畸形 actual，且把 **UTF-16 长度恰等于类型长度**的 original（即使畸形）小写化——与真校验构造器"仅小写化合法十六进制"不同（默认 locale，回放强制 ROOT）。**不要**把映射等同于完整上传接受：回放中 false-validation 放行的非空白 `'bad'` actual 原样通过映射，这不建立上传接受。
  The mapping calls neither `checksumsMatch` nor checksum grammar/size/chronology validation. The true-validation `ChecksumInfo` constructor rejects a nonnull malformed actual, preserves actual case, lowercases valid original hex and keeps malformed/empty/null originals; `getOriginal` returns actual only for the NO_ORIG marker; the false-validation constructor admits malformed actuals AND lowercases any original whose UTF-16 length equals the type length (even a malformed one) — unlike the true-validation constructor, which lowercases valid hex only (default locale; replay forced ROOT). Do **not** equate the mapper with full upload acceptance: a non-blank `'bad'` actual admitted via false-validation passes the mapper unchanged, which establishes no upload acceptance.
- Node 构造器分别对 createdBy/modifiedBy 做 `StringUtils.abbreviate(value,64)`；模型边界上 null 与空串不同；超过 64 个 UTF-16 code unit 时截为首 61 个单元加 `'...'`。回放：60 个 ASCII 字符 + 表情符号（U+1F600，即 `U+D83D U+DE00` 两个单元）+ 尾巴在 `'...'` 前留下**未配对高位代理** `U+D83D`（以转义 UTF-16 单元记录，非无效 UTF-8 输出）。该字符串在所选 PostgreSQL 参数编码边界上的表现已由[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)部分消解（未配对单元编码为字节 `3f`，Node 在编码**前**保留该单元）；真实 JDBC 写入/数据库接受/读回、JSON 序列化与 Go 的字节/rune 计数差异**仍未知**。
  The Node constructor abbreviates createdBy/modifiedBy separately via `StringUtils.abbreviate(value,64)`; null and empty differ at the model boundary; beyond 64 UTF-16 code units it keeps the first 61 plus `'...'`. Replay: 60 ASCII chars + an emoji (U+1F600, i.e. the two units `U+D83D U+DE00`) + a tail leaves an **unpaired high surrogate** `U+D83D` before the dots (recorded as escaped UTF-16 units, not invalid UTF-8). Its appearance at the selected PostgreSQL parameter-encoding boundary (byte `3f`, with the Node keeping the unit BEFORE encoding) is now partially resolved in [save caller and parameter encoding](SAVE-CALLER-ENCODING.md); real JDBC writes/database acceptance/readback, JSON serialization, and the Go byte/rune counting difference **remain unknown**.
- 三层边界须分开：Node/NodeBuilder 模型层接受显式负时间与负长度（回放：created=-7、modified=0、updated=3、length=-11 原样保留；新 builder 默认三时取同一 currentTimeMillis 样本、length 0、file false、actors/checksums null、要求 id>0）；FileInfo setter 回退（`setLastModified(0)` 已在映射前变为 created，回放得 created100/modified100/updated2/length-11）；完整上传接受完全未观察。
  Keep three layers apart: the Node/NodeBuilder model accepts explicit negative times and lengths (replay: created=-7, modified=0, updated=3, length=-11 preserved; a fresh builder defaults all three times to one currentTimeMillis sample, length 0, file false, actors/checksums null, requiring id>0); the FileInfo setter fallback (`setLastModified(0)` already becomes the created before mapping — replay produced created100/modified100/updated2/length-11); and full upload acceptance is entirely unobserved.

## SQL 目标 / SQL target

`NodesDao` 静态 INSERT 映射 **18 列**：node_id,node_type,repo,node_path,node_name,depth,created,created_by,modified,modified_by,updated,bin_length,sha1_actual,sha1_original,md5_actual,md5_original,sha256,repo_path_checksum（末列为**派生** `calcUniqueKey`：对点哨兵替换**之前**的 repo+'/'+path+'/'+name UTF-8 字节做 SHA1）。node_path/node_name 的 null/空串经既有 helper 变 '.'；UPDATE 按 ID 映射身份/元数据/校验和/键。
The static INSERT maps **18 columns**: node_id,node_type,repo,node_path,node_name,depth,created,created_by,modified,modified_by,updated,bin_length,sha1_actual,sha1_original,md5_actual,md5_original,sha256,repo_path_checksum (the last derived by `calcUniqueKey`: SHA1 over the UTF-8 bytes of repo+'/'+path+'/'+name **before** the SQL dot sentinel). A null/empty node_path/node_name becomes '.' via the existing helper; UPDATE maps identity/metadata/checksums/key by ID.

以下归属指引是**由字段归属得出的实现推论**，不是新批准的 Go 模型/schema 决定：规范内容元数据（共享 binaries 登记）与逐节点 origins/actors 应当分离——客户端 original 校验和、时间戳与操作者属于每个路径的节点记录，不能附到共享二进制内容登记上。当前[文件引用适配器](NODE-REFERENCES.md)有意保持最小投影（无时间/操作者/originals/键列）；本页**不默许任何 schema 迁移**，真实行/数据库事务行为未执行。
The ownership guidance below is an **implementation inference from field ownership**, not a newly approved Go model/schema decision: canonical content metadata (the shared binaries registry) and per-node origins/actors should stay separate — client original checksums, timestamps and actors belong to the per-path node record, never attached to the shared binary-content registry. The current [reference adapter](NODE-REFERENCES.md) intentionally remains a minimal projection (no time/actor/original/key columns); this page **implies no schema migration**, and real rows/database transactions were never exercised.

## 证据边界 / Evidence boundary

- REA 在 3 个归档清单内核对了 **16 个选定 class** 的 ZIP entry 身份；提取清单共 **28 个选定 class 绑定**（本任务未选定任何 Spring 装配资源，也不宣称 REA 核对全部 28 个），`javap -c -p -v` 静态检查对应方法字节码；反编译仅为线索。清单身份不证明 JVM 语义或装配接线。
  REA verified the ZIP-entry identity of **16 selected classes** across 3 archive inventories; the extraction manifest lists **28 selected class bindings** in total (this task selected NO Spring assembly resources, and REA verifying all 28 is not claimed); javap statically examines the method bytecode; decompilation is a lead only. Inventory identity proves no JVM semantics or assembly wiring.
- 受控回放：**60 断言 / 36 观察记录**，直接运行精确目标类（RepoPathImpl、RepoPathFactory、FileInfoImpl/ItemInfoImpl、NodeBuilder/Node、ChecksumInfo 及默认未注入 FileService 实例上的**私有实例方法** `fileInfoToNode`）；13 个真实打包 JAR 类路径，**无任何 class/JDBC/DAO 替身**；OpenJDK 26.0.2.1、Locale ROOT、隔离 URLClassLoader 的父加载器为 JDK 平台加载器（java.sql 异常类型可见）。未运行 DAO、nextId、Spring 启动、真实 DB/HTTP/认证，未联系原版服务；SLF4J 走其真实 NOP，因为**回放作用域类路径内不含** provider（不对原 WAR 是否打包 provider 作任何断言），不宣称日志对等。
  Controlled replay: **60 assertions / 36 observation records** running the exact target classes (RepoPathImpl, RepoPathFactory, FileInfoImpl/ItemInfoImpl, NodeBuilder/Node, ChecksumInfo and the private instance method `fileInfoToNode` on a default un-injected FileService); a 13-JAR real packaged classpath with **no class/JDBC/DAO doubles**; OpenJDK 26.0.2.1, Locale ROOT, an isolated URLClassLoader whose parent is the JDK platform loader (java.sql exception types visible). No DAO, nextId, Spring startup, real DB/HTTP/auth and no original service were run; SLF4J took its real NOP because the **scoped replay classpath includes no provider** (no claim about whether the original WAR packages one) — no logging parity is claimed.
- 回放映射**仅在其投影上**强于静态证据：`createFile`、DAO 调用与真实异常分支未被执行；上文的异常类型分支仍是静态事实。
  The replayed mapper is stronger than static evidence **only for its projection**: `createFile`, DAO calls and the actual exception branches were never executed; the exception-type branches above remain static facts.
- 所有示例为合成/自有观察，非外部 HTTP/API 响应断言；原始字节码、回放源码与证据留在私有资料库。
  All examples are synthetic/owned observations, not external HTTP/API response claims; raw bytecode, replay sources and evidence stay private.

## 未决问题与实现边界 / Followup unknowns and implementation boundary

- 存储路径校验必须发生在真实调用方选定的 RepoPath 构造**之后**、ID 申请**之前**；单串工厂的归一化不得给现有纯检查器[Storage-path validation](STORAGE-PATH-VALIDATION.md)注入新的隐式归一化规则，检查器保持不变。
  Storage-path validation must run after the real caller's chosen RepoPath construction and before the ID request; the single-string factory's normalization must not leak new implicit rules into the unchanged pure checker.
- 未观察项：各协议处理器实际选用的工厂重载、上游上传校验和/长度校验、条件插入的实际模式与有效装配、真实事务/提交/错误→HTTP/权限行为、null 与空 actor 的下游区分；未配对 UTF-16 actor 的边界已**部分**消解——所选 PostgreSQL 参数编码给出字节 `3f`（见[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)），但真实 JDBC 写入/JSON/数据库接受/其他协议调用方/其他驱动仍未知，仍需直接 writer/transport 证据后才能选 Go 字符串策略。
  Unobserved: the factory overload each protocol handler actually selects, upstream upload checksum/length validation, the conditional mode/effective wiring, real transaction/commit/error→HTTP/permission behavior, and downstream null-vs-empty actor distinction. The unpaired-UTF-16 actor boundary is now **partially** resolved — the selected PostgreSQL parameter encoding yields byte `3f` (see [save caller and parameter encoding](SAVE-CALLER-ENCODING.md)) — yet real JDBC writes/JSON/database acceptance/other-protocol callers/other drivers remain unknown, and direct writer/transport evidence is still required before choosing a Go string policy.
- 现有 FileReference/Store 保持最小；未来变更需要测试、新增列的版本/迁移政策、保存消费者接口所有权，以及[节点 ID 契约](NODE-ID-CONTRACT.md)与[号段预留记录](ID-RESERVATIONS.md)已有的分配器/取消/不确定提交规则。本页不预建跨领域框架。
  FileReference/Store stays minimal; future changes need tests, a version/migration policy for added columns, save-consumer interface ownership, and the allocator/cancellation/uncertain-commit rules already recorded in the [node-ID contract](NODE-ID-CONTRACT.md) and the [reservation note](ID-RESERVATIONS.md). This page builds no speculative framework.

## 关联 / Links

- 保存链相邻切片 / Adjacent save-chain slices: [NODE-ID-CONTRACT.md](NODE-ID-CONTRACT.md)、[STORAGE-PATH-VALIDATION.md](STORAGE-PATH-VALIDATION.md)、[SAVE-CALLER-ENCODING.md](SAVE-CALLER-ENCODING.md)、[NODE-REFERENCES.md](NODE-REFERENCES.md)、[ID-RESERVATIONS.md](ID-RESERVATIONS.md)
- 证据索引 / Evidence index: [RESEARCH-INDEX.md](RESEARCH-INDEX.md)

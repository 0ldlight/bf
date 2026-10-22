# 保存调用方与参数编码 / Save caller and parameter encoding

状态 / State: BIN-138 研究切片，**本切片不新增任何运行时实现**（项目既有的 Go 元数据/模型/SQL 保持不变，见下文）；不新增模型、SQL、接口、ADR 或架构决定。本页记录目标上传调用方到 PostgreSQL 参数编码边界的静态事实与受控回放观察，作为[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)中"未配对 UTF-16 actor 序列化"UNKNOWN 的**部分**消解。
Research slice only — **this slice ADDS no runtime implementation** (the project's existing Go metadata/model/SQL stay unchanged — see below); no new model, SQL, interface, ADR or architecture decision. This page records static facts and controlled-replay observations from the target's upload caller down to the selected PostgreSQL parameter-encoding boundary, as a **partial** resolution of the unpaired-UTF-16-actor UNKNOWN in the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md).

## 目标与证据身份 / Target and evidence identities

目标为 Artifactory 7.161.26 revision 86126900，与[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)同一 WAR 与同一分发包。以下摘要为机器生成的私有研究证据值，逐字复制：
Target: Artifactory 7.161.26 revision 86126900, the same WAR and distribution as the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md). The digests below are machine-generated private research evidence values, copied verbatim:

| 组件 / Component | SHA-256 |
|---|---|
| 分发包 / Distribution tar.gz | `b967a1fc263ec8cf0314194fc24daf99e10a130ca8e19b076c8a18faf02bc4d9` |
| Artifactory WAR | `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37` |
| artifactory-core-7.161.15.jar | `1abd1ce70341b8125a12b0ef9c3b7d95074e71c90f734c776fdf973297fac831` |
| artifactory-api-7.161.15.jar | `e025580e61df908be80ea08eda49d67bf6cf7439802c679310bcc09ef0f76593` |
| jfrog-db-infra-13.21.0.jar | `f6348abb110238949d58376b08970f40fbd83c1ba74bd7e22c31940b2701ce20` |
| postgresql-42.7.13.jar | `6e0e4cc2d8cae902084f8a2b18728b073a6fd9d1f87c9d8bff8f298c18185b93` |

## 证据层 / Evidence tiers

- 静态引导 / Static lead：反编译仅为线索，`javap` 核对方法字节码。静态抽取覆盖 **8 个 JAR 组件、23 个选定 class 绑定**；REA 在 **3 个新归档清单**（db-infra/core/driver）中按 ZIP entry 身份核对其中 **10 个选定 class**——清单身份不证明 JVM 语义、装配接线，也不是说回放运行了 23 个 class。
  Decompilation is a lead only; javap verifies the method bytecode. Static extraction covers **8 JAR components with 23 selected class bindings**; REA verified the ZIP-entry identity of **10 selected classes** across **3 new archive inventories** (db-infra/core/driver) — inventory identity proves no JVM semantics or wiring, and REA did not run 23 classes.
- 驱动来源 / Driver provenance：PostgreSQL 42.7.13 **不在**选中 WAR 的 WEB-INF/lib 清单内；它取自分发包成员 `app/artifactory/tomcat/lib/postgresql-42.7.13.jar`（完整父包哈希加 entry/JAR/class 摘要绑定，非其他组件副本、非另行下载）。这只证明**随包提供**（SHIPPED），不证明实际部署选用的驱动或其有效配置。
  PostgreSQL 42.7.13 is NOT in the selected WAR's WEB-INF/lib inventory; it comes from the distribution member `app/artifactory/tomcat/lib/postgresql-42.7.13.jar` (bound by the full parent hash plus entry/JAR/class digests — not another component's copy, not a download). This proves SHIPPED availability only, NOT the actually deployed driver's selection or effective configuration.
- 受控回放 / Controlled replay：**192 断言 / 18 案例**；**14 个真实 JAR** 类路径 = BIN-137 的 **13 个同 WAR 依赖**加分发包驱动，隔离 URLClassLoader（父加载器为 JDK 平台加载器），OpenJDK 26.0.2.1，显式 `Locale.setDefault(Locale.ROOT)`（语言标签 `und`），**无任何 class/JDBC/DAO/依赖替身**。回放直接构造目标 `SimpleParameterList`：其构造器本就支持 nullable `TypeTransferModeRegistry`，传 null 是**真实的受支持 profile**，非替代实现。
  **192 assertions / 18 cases**; a **14-real-JAR** classpath = the **13 same-WAR dependencies** inherited from BIN-137 plus the distribution driver, an isolated URLClassLoader (parent = JDK platform loader), OpenJDK 26.0.2.1, explicit `Locale.setDefault(Locale.ROOT)` (tag `und`), **no class/JDBC/DAO/dependency doubles**. Replay built the target `SimpleParameterList` directly: its own constructor supports a nullable `TypeTransferModeRegistry`, so passing null exercises a real supported profile, not a substitute.

## 静态 JDBC 写入链 / Static JDBC writer chain

以下为**静态**事实（无真实调用/事务/异常分支观察）：`NodesDao` 的 actor getter（created_by/modified_by 槽位）经 `JdbcHelper` 参数化 `executeUpdate` → `DbStatementUtils.setParamsToStmt` → `PreparedStatement.setObject`（String 或 null 原样，无 trim/归一化/缩写/null↔空转换）。打包 PostgreSQL 分支上 `PgPreparedStatement.setObject`：null→`setNull(Types.OTHER)`，String→`setString`；`setString` 依配置选 varchar OID 1043 或未指定 OID 0（该配置事实独立于本链，不在此断言实际生效值）。
STATIC facts (no real calls/transactions/exception branches observed): the `NodesDao` actor getters (created_by/modified_by slots) flow through `JdbcHelper`'s parameterized `executeUpdate` → `DbStatementUtils.setParamsToStmt` → `PreparedStatement.setObject` (String or null verbatim; no trim, normalization, abbreviation or null↔empty conversion). On the packaged PostgreSQL branch `PgPreparedStatement.setObject`: null→`setNull(Types.OTHER)`, String→`setString`; `setString` picks varchar OID 1043 or unspecified OID 0 by configuration (a separate fact — the effective value is not asserted here).

`SimpleParameterList.setStringParameter` 先存**字面量**并清空编码缓存（不立即编码）；`getV3Length` 在缓存未命中时执行 `value.toString().getBytes(StandardCharsets.UTF_8)`（固定 UTF-8）并缓存字节。`writeV3Value`/`QueryExecutorImpl.sendBind`（null 写 -1，不进 payload 分支）只是**静态**连接关系，**从未执行**；`Encoding(UTF8).encode` 仅为对照控制，不是断言 Bind 字节的理由。不得推广到简单查询模式、其他数据库或实际生效配置。
`SimpleParameterList.setStringParameter` stores the **literal** and clears the encoded cache (no immediate encoding); on a cache miss `getV3Length` runs `value.toString().getBytes(StandardCharsets.UTF_8)` (fixed UTF-8) and caches the bytes. `writeV3Value`/`QueryExecutorImpl.sendBind` (null writes -1 without the payload branch) are **static** connections, **NEVER executed**; `Encoding(UTF8).encode` was an extra control only, not the basis for any Bind-bytes claim. Nothing here extends to simple-query mode, other databases or the chosen live configuration.

## 回放观察（合成，无数据库接受断言）/ Replay observations (synthetic, no DB acceptance claim)

回放把 BIN-137 的 Node actor 缩写结果**独立传入**实际驱动参数表并读取其惰性编码缓存（绑定/重绑与缓存读取，纯参数表层面；OID 1043/0 均在纯参数表上演练，重绑清缓存并重置字节）：
The replay passed BIN-137's Node actor abbreviation **independently** into the actual driver's parameter list and read its lazy encoding cache (binding/rebinding and cache reads on the pure parameter list; both OID 1043/0 exercised there, rebinding clearing the cache and resetting the bytes):

| 案例 / Case | Node 值 / Node value | 参数字节 / Parameter bytes |
|---|---|---|
| null | null（保持 null） / stays null | NULL 标记，无 payload / NULL marker, no payload |
| 空串 / empty | 空、非 null / empty, non-null | 非 null 标记，零长度 payload / non-null marker, zero-length payload |
| 65 ASCII / 65 ASCII | 61×`A`+`...`（缩写，64 UTF-16 单元）/ abbreviated, 64 UTF-16 units | 61×`41`+`2e2e2e`（64 字节 / 64 bytes） |
| 60 ASCII+emoji+尾 / 60 ASCII+emoji+tail（U+1F600 跨界拆分 / split at the boundary） | 60×`A`+**未配对高位 D83D**+`...`（64 单元，编码**前**保留）/ 60×`A`+**unpaired high D83D**+`...` (64 units, kept BEFORE encoding) | 60×`41`+`3f`+`2e2e2e`（64 字节 / 64 bytes） |
| 59 ASCII+emoji+tailtail（完整对在 `...` 前 / full pair before the dots） | 59×`A`+完整对+`...`（64 单元，对存活）/ 59×`A`+full pair+`...` (64 units, pair survives) | 59×`41`+`f09f9880`+`2e2e2e`（66 字节 / 66 bytes） |
| 字面 `'?'` / literal `'?'` | 保留 / kept | `3f`（与未配对代理同字节；**不是** U+FFFD `efbfbd`）/ same byte as an unpaired surrogate; **NOT** U+FFFD `efbfbd` |
| NUL `a\u0000b` | 保留 / kept | `610062`（只证明编码，**不**证明 DB 接受）/ proves encoding only, **not** DB acceptance |

- 模型与写入边界必须分开：**Node 在编码之前就保留未配对 UTF-16 单元**；`'?'`（0x3f）是所选 PostgreSQL 参数编码边界上的表现，不是模型值。ASCII 65/BMP 65 均触发缩写；组合序列、空白、引号、反斜杠原样保留（该层无归一化）。
  Keep the model and writer boundaries apart: the **Node retains the unpaired UTF-16 unit BEFORE encoding**; `'?'` (0x3f) is its appearance at the selected PostgreSQL parameter-encoding boundary, not the model value. ASCII-65/BMP-65 both abbreviate; combining sequences, whitespace, quotes and backslash pass verbatim (no normalization at that layer).
- 无任何 JSON 序列化、行读回、SQL 约束/长度/事务观察，无公开 HTTP 断言；这些是参数表示，非真实 SQL 往返。
  No JSON serialization, row readback, SQL constraint/length/transaction observations and no public-HTTP claims; these are parameter representations, not a real SQL roundtrip.

## 静态调用方路径链（限于已检方法）/ Static caller path chain (bounded to examined methods)

- `HttpArtifactoryRequest` 构造器调用 `RequestUtils`（取 `getRequestURI`、按长度剥除非空 contextPath 前缀，默认 `includeQuery=false`）→ `calculateRepoPath`。实际 servlet 容器/过滤器路由与被接受的 URI **未知**。
  The `HttpArtifactoryRequest` constructor calls `RequestUtils` (getRequestURI, strips a nonblank contextPath prefix by its length, default `includeQuery=false`) → `calculateRepoPath`. Actual servlet-container/filter routing and accepted URIs are **unknown**.
- `calculateRepoPath` 在布尔参数 `InfoFactory.createRepoPath(String,String,boolean)` **之前**处理 repo 矩阵参数、UTF-8 URL 解码、字面加号保护及 repo 键/点段/归档/zip 辅助（有效设置/MIME 映射未知）。`normalizePathPart` 只处理 `/./`、结尾 `/.` 与精确 `./`，**不是**通用 RFC `..` 解析器。调用方归一化独立于工厂单串 `create`，也独立于既有纯检查器 `ValidateStoragePath`——请求归一化**不得**复制进纯检查器。
  `calculateRepoPath` processes repo matrix params, UTF-8 URL decoding, literal-plus protection and the repo-key/dot-segment/archive/zip helpers (effective settings/MIME mapping unknown) BEFORE the boolean `InfoFactory.createRepoPath(String,String,boolean)`. `normalizePathPart` handles only `/./`, a trailing `/.`, and exact `./` — it is **NOT** a general RFC `..` resolver. Caller normalization is separate from the single-string factory `create` and from the existing pure `ValidateStoragePath` checker — request normalization must **not** be copied into the pure checker.
- 工厂无统一策略：`InfoFactoryHolder` 静态默认经 `BasicFactory` 到 `JaxbInfoFactory`（对应 `RepoPathImpl` 2 参/3 参布尔构造器）；`UploadServiceImpl.validateRequestAndUpload` 用 `InternalRepoPathFactory` 3 参布尔；`DbStoringRepoMixin.saveResource` 以 2 参重建。BIN-137 的边界观察仅在匹配边界适用，不得假设所有路由共用一个工厂重载。
  No single factory policy: the `InfoFactoryHolder` static default goes through `BasicFactory` to `JaxbInfoFactory` (the `RepoPathImpl` 2-arg/3-arg-boolean constructors); `UploadServiceImpl.validateRequestAndUpload` uses the `InternalRepoPathFactory` 3-arg boolean form; `DbStoringRepoMixin.saveResource` reconstructs with 2 args. BIN-137's boundary observations apply only at matching boundaries — no one factory overload for all routes.
- 实际 servlet/过滤器/安全服务/Spring/协议路由**从未执行**；本页不含 HTTP 示例接受断言。
  Actual servlet/filter/security/Spring/protocol routing was **never executed**; this page makes no HTTP example-acceptance claims.

## 静态 actor 来源与管理员头 / Static actor origin and admin headers

`DbStoringRepoMixin.saveResource` 静态上：`securityService.currentUsername`→`setModifiedBy`，新文件另设 `setCreatedBy`（`ItemAdditionalInfo` 直接 setter 不截断；Node 映射/构造器缩写见[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)，服务本身**未执行**）。头 `X-Artifactory-Created-By`/`X-Artifactory-Modified-By` 的填充由 `populateItemInfoFromHeaders` 以 **isAdmin** 门控且仅复制非空白值（`StringUtils.isNotBlank`）；`overrideFileDetailsFromRequest` **再次**检查 isAdmin，仅非空白 actor 覆盖、created 时间仅 >0 生效。这两个 isAdmin 守卫同属本链；角色评估/鉴权解析从未执行，本页**不**据此宣称普通用户可冒充他人。
Statically, `DbStoringRepoMixin.saveResource`: `securityService.currentUsername`→`setModifiedBy`, a new file also gets `setCreatedBy` (the `ItemAdditionalInfo` raw setters do not truncate; the Node mapping/constructor abbreviation is in the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) — the service itself **never ran**). Population of the `X-Artifactory-Created-By`/`X-Artifactory-Modified-By` headers is gated by **isAdmin** in `populateItemInfoFromHeaders` and copies nonblank values only (`StringUtils.isNotBlank`); `overrideFileDetailsFromRequest` checks isAdmin **AGAIN**, overriding only nonblank actors and only a created time >0. Both isAdmin guards belong to this chain; role evaluation/auth resolution was never executed, and this page does **not** claim ordinary users can impersonate others.

## 对既有 UNKNOWN 的部分消解与实现边界 / Partial resolution and implementation boundary

- 本页**仅**消解所选参数 profile 上的编码边界 UNKNOWN：未配对单元在所选 PostgreSQL 参数编码处变为字节 `3f`。真实 JDBC 写入、JSON 序列化、数据库接受/读回、其他驱动、简单查询模式、其他工厂/class、实际装配与配置**仍未知**。
  This resolves the encoding-boundary UNKNOWN **only** for the selected parameter profile: the unpaired unit becomes byte `3f` at the selected PostgreSQL parameter encoding. Real JDBC writes, JSON serialization, database acceptance/readback, other drivers, simple-query mode, alternate factories/classes and effective wiring/configuration **remain unknown**.
- 既有 Go 元数据/模型/SQL **不变**。未来 Go 模型必须**显式决定** UTF-16 缩写、actor 可空性与编码策略（普通 Go UTF-8/rune 截断是另一种策略）；本页不做任何静默拒绝、替换、模型/schema/迁移选择。
  Existing Go metadata/model/SQL are **unchanged**. A future Go model must **explicitly decide** UTF-16 abbreviation, actor nullability and the encoding policy (ordinary Go UTF-8/rune truncation would be a different policy); this page makes no silent rejection, replacement, model/schema or migration choice.

## 关联 / Links

- 相邻切片 / Adjacent slices: [FILE-NODE-SAVE-CONTRACT.md](FILE-NODE-SAVE-CONTRACT.md)、[STORAGE-PATH-VALIDATION.md](STORAGE-PATH-VALIDATION.md)、[REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md)
- 证据索引 / Evidence index: [RESEARCH-INDEX.md](RESEARCH-INDEX.md)

# 普通下载可信接线契约 / Trusted wiring for ordinary downloads

BIN-200 承接[独立 GET/HEAD 适配器](GENERIC-DOWNLOAD-INTEGRATION.md)，定义接入服务前必须补齐的真实身份、仓库事实、授权和生命周期。本次只交付证据与可实施边界，不新增认证实现或生产路由，也不把测试身份注入当作产品认证。
BIN-200 defines the identity, repository, authorization and lifetime requirements for wiring the standalone adapter into the service. This delivery is evidence and a concrete boundary contract, not implemented authentication or production routing.

## 新观察与证据等级 / New observations and evidence levels

2026-10-29 对版本接口返回 7.161.26/rev86126900 的专用实例，使用独立 local/generic 仓库、自主33字节制品、非管理员用户与只涉及该用户/仓库的 read permission target。两组成功矩阵共46次请求，546项独立离线断言通过；原始HAR/响应/控制记录保持私有。所有测试资源经归属核实后删除，另做9项只读命名资源缺席复核。
Two controlled matrices used isolated repositories, synthetic bytes, non-admin users and owned read grants. Forty-six requests pass 546 independent offline checks. Owned resources were deleted and nine name-scoped absence checks were repeated read-only. Raw evidence remains private.

| 受控场景 / Case | 观察 / Observed result |
|---|---|
| 管理员普通 GET/HEAD / Admin | 200；GET完整33字节，HEAD无正文 / Complete GET bytes, bodyless HEAD |
| 匿名、错误密码、未知用户名、无效Bearer / Missing or invalid credentials | 本样本401，带 Basic challenge / 401 with a Basic challenge |
| 有效用户、无读授权；或 read 仅覆盖 `allowed/**` 而访问 `dir/...` | 独立无坏密码干扰实验为403、无challenge；已存在和缺失路径均拒绝 / Clean experiment denies existing and missing paths with 403 and no challenge |
| 给实际用户授予该仓库 `**` read / Explicit read grant | GET/HEAD 200；缺资源404，未知仓库404 / Success; missing resources and unknown repositories are 404 |
| 匿名访问缺资源/未知仓库 / Anonymous missing/unknown | 当前配置均401；未暴露仓库/节点存在性 / 401 under current configuration |
| 连续失败后的临时阻断 / Temporary recurrent-failure block | 可为403且带Basic challenge；与已认证路径拒绝不同 / May be 403 with a Basic challenge, distinct from path denial |

challenge 在这些入口样本中为 `Basic realm="Artifactory Realm"`。HEAD 对应状态与表示头存在，正文为零；这不是由零正文推导服务器没有认证或查询。无效Bearer样本不证明有效JWT的格式、签名验证或token生命周期。
The observed entry challenge uses the stated realm. Bodyless HEAD does not prove absence of authentication/lookup work. An invalid Bearer sample establishes no valid-token verification or lifecycle contract.

第一组采样先发了错误密码，随后同用户名的有效凭据请求遭临时阻断；其中403→200不能用于推断权限缓存传播时间。另一组使用新身份且没有先发坏密码，独立观察路径权限。producer标签表示探测意图，实际原因由响应记录与控制顺序共同核实，不能只按状态或字节数归类。
Wrong-password probes contaminated subsequent same-user requests through temporary blocking. Their 403-to-200 transition is not a permission-cache timing measurement. A fresh-user experiment isolates path authorization; producer labels alone do not establish failure causes.

用户创建时传 `groups=[]` 仍被目标加入默认 `readers`；仅对本次新用户执行组列表更新，复读响应不再列出默认组后才采样，有效read权限另用矩阵核实。初次假设失败及修正记录保留，不计作成功矩阵。不得把“请求传空组”直接解释成无有效权限，组成员关联的变更只涉及本次新用户，没有改既有组的属性/授权、其他用户或既有permission target。
An empty creation-time groups list still resulted in default readers membership. Only that controlled user was updated; its reread no longer listed the default group, and effective read access was checked separately. The initial failed harness assumption is retained; membership changes involved only the new test user, with no edits to existing group attributes/grants, other users or existing permission targets.

静态证据另外绑定固定WAR中28项class/JAR/javap记录；实际 web.xml 映射确认 AccessFilter 在 RepoFilter 之前。身份报告原有八份签名输出不含方法体，独立补齐实际 `-c` 输出后才采用相应委托/匿名分支事实。版本接口不证明部署WAR字节；静态方法体与HAR解码也不证明所有运行分支。
Static evidence separately binds 28 class/JAR/javap records and the actual WAR web.xml filter order. Eight identity dumps originally lacked method bodies; independent disassembly supplies the required bodies. Version labels, static analysis and HAR decoding do not attest deployed bytes or every executed branch.

## 已核实的责任边界 / Verified responsibility boundaries

- 普通请求的认证入口先于仓库分发；Basic过滤器与认证manager分层。所查用户口令provider调用Access客户端认证，所查token provider调用Access gRPC认证。这是具体调用事实，不是“整个产品从不本地验证密码/JWT”的全局结论。
  Authentication precedes repository dispatch. The inspected password/token providers delegate to Access clients; this does not prove the whole product lacks other local verification.
- 普通generic仓库的路径校验/读权限在节点查询之前；仓库存在性来自持久化配置派生的服务/缓存，不能由nodes的repo键推断。local/generic类型是独立仓库事实。
  Ordinary generic path/read checks precede node lookup. Repository existence and local/generic type derive from configuration, not node rows.
- 权限target、用户/组与仓库级include/exclude是不同输入。所查主授权路径委托Access缓存；本体legacy/影子匹配器不能冒充已验证的Access端完整匹配算法。全局/仓库隐藏策略还可把内部401/403映射为404，当前实验未切换这些设置。
  Permission targets, principals/groups and repository include/exclude are distinct inputs. Legacy matching code is not proof of complete Access runtime semantics; resource-hiding settings remain configuration-dependent.
- 当前Go产品只有Actor审计纯值与DownloadAuth端口，没有真实凭据验证、principal、仓库目录或权限存储。主模块还没有PostgreSQL驱动；短读事务接线仅在集成测试中存在。
  The product has an audit Actor and an auth port, but no credential verifier, principal/repository/grant store, production database driver or short-read wiring.

## 接线方案与先后次序 / Wiring design and order

首个完整接入里程碑仍限显式配置的 ordinary local/generic、既有ASCII `.bin`/`.txt` GET/HEAD。先补真实身份与仓库/授权基础，再装配服务；不把原版作为BinFlow运行上游，不用默认匿名、硬编码Allow、Actor或请求头用户名充当已验证principal。
Keep the existing finite download profile. Implement real identity/repository/access foundations before mounting; never proxy the reference implementation or turn request claims/test doubles into authoritative principals.

| 边界 / Boundary | 必需输入与输出 / Contract |
|---|---|
| CredentialVerifier | 不透明凭据→经真实校验的主体ID、有效组/管理员事实，或明确拒绝/临时阻断/服务失败；未知拒绝，不从Basic解码或JWT parse直接造身份 / Actual verification produces authority; decoding alone does not |
| RepositoryCatalog | 精确repo键→存在性、key、local/generic类型与配置revision；未知返回独立缺席，存储错误独立失败；不用节点命中替代 / Independent repository facts |
| ReadPolicy | 权威principal×仓库事实×规范存储Path→read允许/拒绝/error；由真实持久授权数据决定，不用“有任意角色即允许” / Persisted authorization, not role-name shortcuts |
| RequestReadStore | 调用方context下的有界短读事务→既有LookupSnapshotSource→回滚释放；事务不跨内容传输，不新增SQL读取模型 / Short caller-owned transactions |
| Composition root | 显式配置、数据库池、filestore、真实认证/授权、仓库目录→下载路由；失败关闭并正确释放已创建资源 / Verified dependencies before routing |

入口次序为：识别下载入口→真实认证/临时阻断门→仓库目录解析→受支持画像与read策略→一次节点查询/投影→GET Open与传输，HEAD零Open。路径不得在原始画像守卫前被路由器clean/decode；健康入口不被下载认证替换。内部query、Range/条件头等既有排除保持为明确本地策略，不能外推为目标完整路由规则。
The entry authenticator precedes catalog/path access. Preserve raw-URI validation before normalization, single node access and body ownership; health remains separate. Existing profile exclusions remain explicit local policies.

认证失败渲染与路径授权渲染分开。BIN-199的Denied固定403且不接受challenge，不能直接表达“认证入口临时阻断403带challenge”；由外层认证入口负责该响应，通过后才交给适配器。适配器Auth实现消费由可信入口放入私有上下文的主体并执行ReadPolicy，不能重复把未经验证的头解析成主体。未知仓库的404由目录分发处理，现有适配器域外400不自动升级成目标完整规则。
Render entry failures separately from path denials. The existing Denied result cannot express a challenged authentication-block 403; an outer trusted entry handles it before the adapter. The adapter consumes verified private context and applies read policy. Catalog absence is a distinct dispatch outcome.

初期不引入正向凭据/权限缓存，避免在尚未验证失效机制时缓存旧授权；这是明确本地实现选择，不声称时序等价。失败尝试门必须有界并能区分临时阻断与路径拒绝，持续时间不能由样本中的剩余秒数倒推为固定配置。Artifactory缓存键的最终SHA1/原始串表示尚未绑定，不纳入已冻结协议。
Initially omit positive credential/grant caching rather than invent invalidation semantics; this is a local choice. Bound failure tracking and keep temporary blocking distinct. Neither remaining delay samples nor unverified cache-key representations define the target protocol.

## 配置、启动与生命周期 / Configuration and lifetime

采用显式health-only与download-enabled两种模式。既有默认健康模式可保持；启用下载则必须提供受支持仓库、真实安全后端、数据库和filestore配置，缺项/半配置/不支持的类型拒绝启动，不能静默退回健康模式造成错误就绪。敏感值只来自主机本地受控配置，错误/日志不输出DSN、凭据或授权头。
Use explicit health-only and download-enabled modes. Enabling downloads requires all real dependencies and supported configuration; partial setup fails startup rather than silently reporting healthy readiness. Secrets and connection values never enter logs.

启动顺序：配置校验→有界DB连接/Ping与schema版本检查→仓库与安全事实校验→filestore打开→构造认证/授权/适配器→挂接并监听。pgx可复用集成模块已有精确版本，但进入产品主模块前记录版本、校验与许可；schema由显式可信引导管理，启动不自动DDL，也不默认创建管理员或read授权。
Validate configuration, database/schema and catalog/security facts before opening storage and mounting. Reuse of the existing test driver needs an explicit product dependency record. Schema/bootstrap operations stay explicit; startup creates no implicit admin/grant.

关停先停止接收并排空HTTP请求，再关闭filestore与数据库。若超时不能假称排空成功或在活跃读流上先关共享依赖；明确记录强制退出策略。当前provider正在阻塞的Read无法被适配器阶段检查强制中断，这一限制保留。
Drain requests before closing shared storage/database dependencies. A timeout is not successful draining; define forced shutdown explicitly and retain the blocked-provider-read limitation.

Basic凭据的生产传输沿用[已批准TLS入口](UAT-PROPOSAL.md)，Go HTTP优先绑定loopback并置于受控TLS终结层后；不把任意转发头或外部用户头作为已验证身份。测试用loopback HTTP不构成公开明文凭据入口许可。
Use the approved TLS boundary for production credentials, preferably with loopback Go HTTP behind it. Forwarded/user headers alone establish no identity; loopback test HTTP is not approval for public plaintext credentials.

## 实施出口与剩余缺口 / Execution exits and remaining gaps

下一个实施范围应是“真实本地身份/授权基础＋受控仓库事实＋下载组合根”一条完整业务线，禁止继续仅把接口占位接到路由。具体密码存储/校验算法、Access服务端验证及完整permission匹配仍需对对应平台制品补证；它们当前没有Go实现，不可把本契约登记为可部署服务。
The next implementation milestone joins actual local security/catalog foundations and the composition root. Password verification/storage, Access-side verification and complete permission matching still need their corresponding platform evidence and implementation; this contract is not a deployable service.

必需验收：真实凭据与真实仓库/grant记录下保存→新GET完整字节→HEAD同表示零Open；缺/错凭据、临时阻断、无权、未知仓库、缺节点及对象失败分别断言状态/挑战头/零越权节点访问；修改/撤销凭据和授权后旧决定不可继续使用；配置失败、schema缺失、启动中途失败和关停在途流都有真实进程/数据库/文件系统测试。沿用RED/GREEN、race、故障回归、共享检查、独立审查与精确PR/默认分支CI。
Require genuine credential/catalog/grant records, end-to-end bytes/HEAD, distinct entry/path failures, revocation, configuration/schema/startup failures and active-stream shutdown tests, with the existing delivery gates.

本次46个参考请求及546项核验不替代上述产品验收；未覆盖有效Bearer、外部目录/SSO、匿名开启、切换隐藏策略、完整模式匹配、锁号持久化或集群缓存一致性。PKG-007/API-001/REP-001及平台身份能力继续开放。
Reference checks do not replace product tests. Valid-token/external identity/anonymous-enabled/hiding/pattern/lockout/cache behavior and parent capabilities remain open.

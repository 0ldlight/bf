# 下载身份与授权基础 / Download security foundations

本片为独立 GET/HEAD 读取器提供真实数据库身份、仓库目录与读授权。`internal/security` 组合这些依赖；BIN-201 交付时命令仅提供健康入口；后续显式模式、依赖与资源生命周期接入见[下载进程装配](DOWNLOAD-PROCESS.md)，外部 TLS 与部署仍需独立验证。本片不提供账户管理 API、完整上传、Token 登录或部署。
This slice composes persisted identity, catalog and read authorization with the standalone GET/HEAD reader. Subsequent command configuration and lifetime wiring are documented in [process composition](DOWNLOAD-PROCESS.md); external TLS and deployment remain independently verified boundaries.

## 组件与所有权 / Components and ownership

- `GenerateCredential`、`CheckCredential`：有限 Argon 存储画像；不记录密码或摘要。
  A bounded credential codec with no credential logging.
- `Store`：调用方拥有 `*sql.DB`，显式操作超时；请求方法只读，不建表、升级摘要、创建管理员或关闭池。
  The caller owns the SQL pool; request methods never bootstrap or mutate security state.
- `Principal`：私有 ID 与规范用户名，仅实际验证成功后产生；零值无效。Actor、Basic 解码结果和代理头不是身份。
  An immutable verified database identity, never an audit Actor or decoded request claim.
- `postgres.NewReadPool`：每次节点查询建立有界只读事务，返回前回滚释放；事务不跨内容流传输。
  Each node lookup uses a bounded read-only transaction released before streaming.
- `NewAuthenticatedDownload`：先校验显式配置和安全 schema，再核实配置仓库；成功后返回完整独立处理器，不负责池/文件系统生命周期。
  Construction validates dependencies and catalog facts before returning a composed standalone handler.

安全 schema 通过 `security.SchemaSQL` 由可信引导流程显式执行；这是 BinFlow 原创 v1 表模型，并非 Access 数据库副本。仅插入版本标记，不预置用户、组、仓库或授权。`Validate` 核对版本及必需列的被动读取，不声称可检测任意 schema 漂移。表结构和账户管理入口不能暴露给普通下载请求。
Apply the original BinFlow schema explicitly through a trusted bootstrap. Validation checks version and required columns, not arbitrary schema drift. There are no default accounts or grants.

## 密码支持域与依据 / Credential profile and evidence

实际本地发布制品 `access.war` 的 SHA-256 为 `dc871abf684789cf54977ed88ded2524493390c5b35ffa05b9893c391d692ca5`，其中 Access 组件为 7.191.16；这不是在线 Artifactory 产品版本或部署字节证明。包内实际 ArgonEncoder 的受控运行产生 9 份冻结向量，42 项观察及 9 项新进程重放断言通过。独立类型路由字节码确定外层类型前缀；并未启动完整 Access 登录服务。
The pinned local artifact contains Access component 7.191.16. Nine frozen vectors and 51 component assertions establish the bounded encoder profile, not deployed product bytes or full login behavior.

支持存储格式严格为 `argon$$argon2id$v=19$m=512,t=16,p=1$<salt>$<key>`，salt 16 字节、结果 32 字节、规范无填充标准 Base64。使用固定参数 Argon2id 和常数时间摘要比较；参数、版本、长度或编码不符在 KDF 前拒绝，不接受输入驱动的高成本参数。此成本是兼容事实，不是密码强度建议；不支持 FIPS、PBKDF2、bcrypt、旧格式、自动升级或 Spring 宽松解析的全部输入。
Only the canonical fixed Argon2id profile is accepted. Unsupported encodings fail before expensive computation. The observed cost is a compatibility fact, not a security recommendation.

原始密码必须为非空有效 UTF-8、最多 1024 字节；保留空格、NUL 和 Unicode 原字节，不做 trim 或规范化。生成函数始终把输入作为原始密码重新加盐散列，不把看似编码后的字符串直接存储。用户规范化仅支持有限 ASCII：两端移除不大于空格的 ASCII 字节并转小写，再核实 64 字节标识范围；不宣称完整 Java Locale/Unicode 等价。
Raw passwords are byte-preserved within the explicit UTF-8 bound. Username normalization is a finite ASCII subset, not full Locale parity.

密码实现依赖固定版本 `golang.org/x/crypto v0.57.0` 与间接 `golang.org/x/sys v0.48.0`（BSD-3-Clause）；模块摘要由 Go 校验。集成模块按 MVS 同步更新间接 `x/text v0.42.0` 与 `x/sync v0.23.0`。本地官方模块源码、来源元数据与许可证参与依赖审查。每个 Store 最多同时执行 4 次验证 KDF；未知或禁用账户的有效原始输入也执行一次私有 dummy 验证，始终拒绝且不产生身份。不声称网络响应时间完全一致。
Pinned official Go modules implement the KDF. Each Store bounds concurrent verification and performs dummy work for unknown or disabled accounts; this is not a network timing-equality guarantee.

## 请求顺序与拒绝策略 / Request order and rejection

1. 下载入口核实单个有界 Basic 认证头，不使用 Cookie、Bearer 或用户名代理头回退。
   Validate the sole bounded Basic header without fallback identity sources.
2. 进程内失败门检查后，读取当前账户并实际验证密码；成功后才写入私有请求上下文。
   Apply the failure gate and verify current persisted credentials before creating request identity.
3. 从原始 URI 提取仓库键，独立读取目录；不从节点行推断仓库存在性。
   Resolve authoritative catalog facts independently of node data.
4. 既有适配器校验有限路径/方法画像，再读取当前账户、管理员、直接或组 read 授权。
   Apply the existing finite request profile and current persisted read policy.
5. 授权后只查一次节点；GET 才打开内容，HEAD 零 Open，沿用既有流式错误及关闭契约。
   Look up the node once after authorization; GET opens content, HEAD does not.

目录仅接受独立配置的 local/generic 仓库，正 revision；首片处理器只服务一个显式仓库。read grant 仅支持明确的仓库全域 `**`，直接用户或组二选一；其他 glob、排除或路径范围不能被放宽成全域许可。当前有效管理员事实来自数据库，不能从用户名或请求角色名推断。不提供正向权限缓存，每次重新读取；已获授权的在途传输可能完成，后续请求必须看见已提交撤权。
The initial catalog supports local/generic repositories and repository-wide `**` grants only. Unsupported patterns are never widened into permission. Every authorization reads current facts; an already authorized transfer may complete.

缺失、未知或错误凭据为 401 带 Basic challenge；禁用账户采用相同本地隐私策略，并非目标全部状态映射结论。显式阈值次数的失败各返回 401，后续临时阻断为 403 带 challenge；路径权限拒绝为 403 无 challenge。缺席仓库独立返回 404；依赖或存储编码错误为固定 500，无私有详情。失败计数仅记录错误凭据，容量受限；不能通过淘汰仍在阻断的账户绕过封禁。
Credential rejection, temporary authentication blocking and read denial have distinct challenge behavior. Operational failures return fixed private-data-free errors. The bounded failure gate never evicts an active block to allow a bypass.

失败门是进程内策略，显式配置窗口、阈值、阻断时长与容量，不等同于持久或分布式 Access 锁定。成功验证清除该用户名的失败记录，但不缓存密码、组或许可。实际请求取消中止传输；依赖自行超时且请求仍有效时为 500。HEAD 错误无正文并带正确长度；认证前置错误没有制品响应头。
The process-local gate is not distributed lockout parity. Real request cancellation aborts; an independently timed-out dependency remains an operational error.

## 后续边界 / Remaining boundaries

进程模式、受控配置、PostgreSQL 驱动与启动/排空/关闭接入由[下载进程装配](DOWNLOAD-PROCESS.md)负责；外部 HTTPS/代理来源与部署仍须独立验证。当前不开放账户管理，不自动迁移/创建凭据；部署仍需独立验证。完整授权匹配、Token/FIPS、持久锁定、Unicode/Locale 和其他下载协议能力继续留在能力矩阵与 Linear 中。
Process configuration and lifetime wiring are covered by the linked process contract. Broader identity, policy and protocol compatibility remain open.

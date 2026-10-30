# 下载进程装配 / Download process composition

BIN-202 在[下载安全基础](DOWNLOAD-SECURITY.md)之上增加进程配置与生命周期；保留默认健康模式，仅显式下载模式装配真实 PostgreSQL、filestore 与认证处理器。此实现是 BinFlow 进程策略，不新增原版协议、完整身份兼容或部署结论。操作使用说明维护在独立 Fern 文档仓库。
BIN-202 adds original process configuration and lifetime policy on top of the security foundations. It does not establish complete protocol compatibility or deployment readiness; operator guidance belongs in the separate Fern repository.

## 配置边界 / Configuration boundary

`internal/process` 拥有配置装载与真实依赖装配；`cmd/binflow` 只负责信号、固定类别日志和返回状态。未知模式和半配置拒绝启动；不通过默认健康响应伪装启用失败。下载配置从主机本地受控 JSON 文件读取，环境只提供非敏感选择器。文件必须为绝对路径、服务身份所有、普通文件、0400 或 0600，最多 16 KiB；Unix 打开时拒绝最终符号链接并避免 FIFO 阻塞。此策略不声称父目录命名空间不可被所有者更改。
Configuration is explicit and fail-closed. A bounded owner-only regular JSON file carries private settings; selectors are non-secret. Final-component symlinks and non-regular files are refused without claiming immutable parent namespaces.

JSON 九个必需字段为 `listen_addr`、`postgres_dsn`、`filestore_root`、`repo_key`、`challenge`、`failure_threshold`、`failure_window_seconds`、`block_seconds`、`max_tracked_users`。未知或重复字段、第二个 JSON 值、错误类型和超界值拒绝。Config 的字符串与 Go 格式表示固定隐藏内容；错误也不携带原始配置、OS 路径或驱动异常，包括原始错误解包链。
The nine required fields form a closed schema. Duplicate/unknown fields, trailing documents and invalid values fail. Config formatting and returned errors never reveal private values or wrap raw driver parse errors.

下载监听仅限字面量回环 IPv4/IPv6，允许端口 0 作受控临时监听；不能通过 DNS、Host 或 Forwarded 头证明传输可信。外部访问仍须由另行验证的 HTTPS 终止器承接，本次未变更部署。健康模式继续使用既有监听环境变量与默认回环地址，允许原有测试镜像的健康监听；下载模式拒绝竞争的监听环境设置。
Downloads use a literal loopback origin; a separately verified HTTPS terminator remains responsible for external access. Header claims do not establish transport trust. Health mode retains its existing listener behavior.

PostgreSQL 使用现有精确版本 pgx v5.11.0（MIT），进入产品主模块前记录依赖和模块校验。连接 URL 必须显式提供单主机、端口、用户、非空密码、数据库和 sslmode；只允许可选 search_path 与显式的 default_query_exec_mode=exec。`disable` 只接受字面量回环数据库，远端必须使用系统 CA 的 `verify-full`；无服务文件、密码文件、客户端证书或静默 TLS 回退支持。下载模式发现 PG 前缀环境项即拒绝，不修改进程全局环境；显式空文件参数阻止默认客户端证书与密码文件来源。其他数据库、定制 CA、mTLS 和多主机另留后续画像。
The pinned driver accepts an explicit bounded URL profile. Loopback-only plaintext and remote hostname-verified TLS are separate explicit cases. Ambient PG settings are rejected rather than mutated; unsupported credential/transport sources never silently fill missing configuration.

## 资源所有权与原始路由 / Ownership and raw routing

启动顺序为配置和取消检查、连接池与有界 Ping、真实安全与节点读取器、filestore、被动 schema/仓库核实、监听绑定，最后宣布就绪。固定操作和启动预算为 10 秒；池最多 8 个连接。失败只逆序关闭已经取得的资源，不自动 DDL、升级、建用户或赋权。filestore 的既有目录与 staging 创建规则保留，失败回收不删除既有数据路径。
Startup validates before readiness, with bounded database operations and explicit reverse-order cleanup. No DDL, users or grants are created. Existing filestore initialization semantics remain intact, without deleting data paths during rollback.

下载仅在原始 RequestURI 的 `/artifactory/` 前缀上分发给真实认证处理器；不通过路由器 clean、unescape 或仅根据解析后的 URL.Path 选择制品。其他请求沿用既有健康处理器，保持健康 GET/HEAD/405/404 行为。
Raw-prefix dispatch preserves the existing download guard. Health behavior remains separate and unchanged.

## 排空与超时 / Drain and timeout

运行取消先关闭准入，再执行最多 10 秒的 graceful Shutdown；它不会立即取消已准入请求的独立 BaseContext。准入计数与封门共用互斥锁，defer 在正常返回和 panic 时都释放计数，拒绝封门后的请求且不访问共享依赖。
Cancellation seals admission before graceful shutdown while admitted requests retain an independent context. One synchronized counter proves actual handler completion, including panic paths.

`Server.Close` 只关闭连接，不等待处理器结束。排空超时后会关闭连接、取消请求并返回固定非零失败；只有计数归零后才关闭 filestore，再关闭数据库池。如果处理器仍被阻塞，一个明确的延后回收协程保留资源直到它实际退出，不能把强制路径登记为成功排空。命令在失败返回后退出，由 OS 最终回收进程资源；嵌入调用方则可能观察到延后回收。10 秒是排空政策预算，不是任意 OS Close、调度或可信 ready 回调的硬实时保证。
Connection closure is not a handler join. Timed-out work retains shared resources until actual completion through a single deferred cleanup path. Forced process exit is failure, not a successful drain; the budget is not a hard real-time guarantee for arbitrary OS operations or callbacks.

本片继续限定既有 Basic、ASCII 下载路径、local/generic 目录和全仓 read 支持域；不增加完整 Access、Token/FIPS、复杂模式或 Unicode 兼容承诺。工程证据和失败修正保持私有，能力状态以矩阵及 Linear 为准。
The existing finite download and security support domains remain unchanged. Engineering evidence stays private and parent capability status remains open.

## 无缓存执行画像 / Uncached execution profile

DSN 可显式增加 `default_query_exec_mode=exec`；仅此值可接受，未知值、空值或重复参数拒绝。显式画像使用 pgx 的单轮扩展协议执行，关闭语句与描述缓存；不使用 SQL 字符串插值，也不依赖命名预处理语句跨事务保留。未提供该参数时保留原驱动默认画像。JSON 九字段、TLS 校验、有限 search_path 与原有错误隐藏不变。
The optional DSN key accepts only `exec`, selecting single-round-trip extended execution with both caches disabled. Missing, empty and duplicate are distinct: omission preserves the existing driver default, while empty/duplicate values fail. No SQL interpolation or named-statement reuse is introduced.

该画像只解决驱动执行选择，不证明任意 transaction pooler 已兼容。真实部署仍须核实目标对启动 search_path 的处理、多连接/多事务的 schema 隔离、权限、取消与回收；一次成功连接或健康响应不足以证明这些行为。
The execution choice does not attest a transaction pooler. Deployment must verify startup parameters, schema isolation across connections/transactions, privileges and cancellation against the actual endpoint.

UAT 可由服务专属运行环境提供受控 CA 集合，保持系统信任池读取与 verify-full 的证书链和主机名校验。信任文件须来自经认证的运维输入；不能从未验证握手采信证书。DSN 不接受 sslrootcert 文件参数，本片也不增加 JSON CA 字段；专属信任配置及精确制品的正反验证属于部署出口。
A service-scoped CA bundle is an explicit deployment input, not a new DSN or JSON field. Its provenance and exact-artifact positive/negative checks remain deployment gates; chain and hostname verification stay enabled.

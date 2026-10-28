# 能力矩阵 v1 / Capability matrix

本矩阵冻结调查范围和稳定标识，不代表已完成兼容。每项拆分为具体行为、客户端/平台版本和测试证据后才能验收；新增子项不得悄然缩小父项范围。
This first matrix freezes investigation scope and stable IDs, not parity claims. Acceptance requires behavioral cases, version combinations and evidence; splitting an item must preserve its full scope.

## 状态与证据 / States and evidence

- 待调查 / Investigate：目标版本实际条件尚未冻结。
- 待实现 / Implement：需求已确认，BinFlow 实现与对照验证尚未完成。
- 已观察 / Observed：仅对列出的原版样本成立，不代表 BinFlow 通过。
- 已验证 / Verified：同一契约在指定原版及 BinFlow 提交通过，附运行证据。

当前下表均为“待调查/待实现”，没有产品兼容项已验证。内置 `/healthz` 只验证开发进程，不计入原版 REST 兼容。
All entries below remain under investigation/implementation. The internal health endpoint is engineering evidence only.

## 功能调查范围 / Functional coverage

| ID | 能力 / Capability | 冻结与验收内容 / Required contract | 状态 / State |
|---|---|---|---|
| REP-001 | 仓库 / Repositories | local/remote/virtual、布局、配置、优先级、代理与缓存 / types, layout, configuration, resolution, proxy/cache | 待调查/实现 |
| PKG-001 | Maven | 发布/解析、元数据、快照、校验与客户端版本 / publish, resolve, snapshots, metadata, checksums | 待调查/实现 |
| PKG-002 | npm | scoped 包、元数据、标签、认证与客户端 / scoped packages, metadata, tags, authentication | 待调查/实现 |
| PKG-003 | OCI / Docker | push/pull、manifest、tag、blob、认证 / manifests, tags, blobs, authentication | 待调查/实现 |
| PKG-004 | PyPI | simple/index、上传、下载、客户端 / indexes, upload/download, clients | 待调查/实现 |
| PKG-005 | Go modules | module proxy、版本、校验、客户端 / proxy, versions, checksums, clients | 待调查/实现 |
| PKG-006 | Helm | chart/index 与目标版本 OCI 行为 / charts, indexes, applicable OCI behavior | 待调查/实现 |
| PKG-007 | Generic | 路径、属性、上传/下载、范围与条件请求 / paths, properties, transfers, range/conditional requests | 待调查/实现 |
| PKG-999 | 其他包类型 / Remaining package types | 从目标版本逐项枚举，分配稳定子 ID，不以首批类型替代全范围 / enumerate every target type and assign stable IDs | 待调查/实现 |
| API-001 | REST | 路径/方法、请求响应、错误、分页、状态码与版本边界 / methods, representations, errors, pagination | 待调查/实现 |
| API-002 | AQL | 语法、字段、关系、过滤/排序/分页、权限与错误 / syntax, fields, relations, queries and errors | 待调查/实现 |
| SEC-001 | 身份 / Identity | 用户、组、Token、认证集成与生命周期 / users, groups, tokens, integrations, lifecycle | 待调查/实现 |
| SEC-002 | 权限 / Authorization | permission target、路径/仓库/操作授权、拒绝与信息泄露边界 / targets, paths, actions, denial behavior | 待调查/实现 |
| STO-001 | 制品数据 / Artifact storage | checksum、去重、对象/文件存储、一致性、GC 与恢复 / integrity, deduplication, backends, GC, recovery | 待调查/实现 |
| META-001 | 元数据与搜索 / Metadata/search | 属性、索引、查询与重建 / properties, indexes, queries, rebuild | 待调查/实现 |
| BUILD-001 | 构建信息 / Build info | 发布、查询、依赖关联、推广与保留策略 / publication, dependencies, promotion, retention | 待调查/实现 |
| SYNC-001 | 复制与联合 / Replication/federation | 拓扑、冲突、断网重试、一致性与授权条件 / topology, conflicts, recovery and entitlement | 待调查/实现 |
| AUD-001 | 审计与运维 / Audit/operations | 日志、事件、配置、监控与管理边界 / logs, events, configuration, administration | 待调查/实现 |
| LIC-001 | BinFlow 授权 / Licensing | 试用、签发、到期、功能与离线授权；商业规则待批准 / trial, issuance, expiry, entitlements, offline; business rules pending | 待调查/实现 |
| INT-001 | CLI / CI | JFrog CLI 与 CI 集成，锁定版本和实际调用 / pinned clients and exercised workflows | 待调查/实现 |
| INT-002 | 外部产品接入 / External products | Artifactory 的集成面；独立产品自身按需求排除 / integration surfaces within the agreed product boundary | 待调查/实现 |
| PLAT-001 | 必需平台能力 / Required platform | 核实 Router/Access/Metadata 等职责与依赖，随包存在不等于必须独立部署 / verify roles/dependencies, without inferring topology from bundle presence | 待调查/实现 |
| UI-001 | 产品界面 / Product UI | 完整主流程、中文/英文、深浅主题、键盘/移动端 / workflows, languages, themes, keyboard/mobile | 待调查/实现 |

## 平台组合 / Platform combinations

| ID | 维度 / Dimension | 必须冻结的条件 / Conditions to establish |
|---|---|---|
| DB-001 | PostgreSQL | 目标支持版本、驱动、迁移与同一存储契约；开发默认不等于全覆盖 / versions, migrations and shared contracts |
| DB-002 | MySQL | 版本、方言、事务与并发 / versions, dialect, transactions, concurrency |
| DB-003 | MariaDB | 与 MySQL 分开验证 / verify independently |
| DB-004 | SQL Server | 版本、字符/排序、事务与迁移 / versions, collation, transactions, migrations |
| DB-005 | Oracle | 版本、类型、事务与迁移 / versions, types, transactions, migrations |
| DB-006 | Derby | 先核实目标版本是否适用及限制，不提前排除 / determine target applicability and limits |
| DEP-001 | Kubernetes / Helm | 单节点/HA、资源、持久卷、升级与恢复 / topology, resources, volumes, upgrade/recovery |
| DEP-002 | Docker / Compose | 支持拓扑、卷、网络、升级与恢复 / topology, volumes, networking, recovery |
| DEP-003 | Linux archive | 安装、启动、服务、升级/卸载 / installation, service, upgrade/removal |
| DEP-004 | RPM / DEB | 两类包分别验证版本、脚本与升级路径 / validate each package family |
| ENV-001 | Linux / CPU | 目标发行版/版本/架构逐项核实；Windows 排除 / enumerate target combinations; Windows excluded |
| ENV-002 | 离线/代理/内网 / Offline/proxy/private network | 安装依赖、解析、认证与故障 / dependencies, resolution, auth, failures |
| OPS-001 | HA / resilience | 并发、节点/存储/网络故障、恢复与一致性 / failure injection and consistency |
| OPS-002 | 生命周期 / Lifecycle | BinFlow 自身升级、备份恢复、结构迁移；无旧业务数据迁移 / product upgrades/recovery, without legacy business-data migration |
| PERF-001 | 性能 / Performance | 受控原版基准、负载/资源/数据集、延迟/吞吐与退化 / controlled workloads, latency, throughput and degradation |

这些组合的具体版本仍待调查，不能根据当前机器或单个安装包推断完整支持清单。
Exact supported versions remain unknown; the current host and one package cannot establish the full support matrix.

## 验收记录 / Acceptance records

每条行为至少包含：能力 ID、原版版本/授权、平台与客户端、输入、预期输出/副作用、BinFlow 提交、执行命令、结果及私有证据 ID。正常、错误、权限和边界分别记录；不适用须说明依据。
Each case records identity, environment, input, outputs/effects, commit, command, result and evidence ID across success/error/authorization/boundary behavior. Explain any non-applicability.

基线来源由[研究索引](RESEARCH-INDEX.md)管理，实施依赖由[P0 计划](P0-PLAN.md)及 Linear 管理。
The research index owns provenance; the P0 plan and Linear own implementation dependencies.

BIN-195 的[已发布本地文件保存](LOCAL-FILE-SAVE-PUBLISHED.md)是 REP-001/STO-001 内部普通单写者节点切片：真实查询分派、内部新建元数据和既有命中更新；有限路径与参考证据已记录，产品单元/真实 PostgreSQL/故障与共享检查已通过，交付按精确提交核验 CI。它不缩小父项范围，也不使 Generic/REST/权限/完整上传变为已验证。
BIN-195 is a bounded internal node-save slice under REP-001/STO-001. Its contract records actual lookup dispatch, fresh metadata production, existing updates and finite evidence; local unit/database/mutation/shared gates passed, with CI verified against exact delivery commits. Parent scope and unverified Generic/REST/auth/full-upload status remain unchanged.

BIN-196 的[普通流式本地保存](LOCAL-STREAM-SAVE.md)冻结真实 reader→binary 发布/登记→节点保存的有限契约；90 项真实组件参考/523 断言仅支持局部 header/stream 语义，本地产品单元/race、真实 PostgreSQL、故障与共享检查通过，交付独立核验精确 CI，不提升父项兼容状态。
BIN-196 freezes the bounded real-stream publication-to-node contract. Ninety component references/523 assertions establish local header/stream semantics only; local unit/race/database/fault/shared gates passed, with exact-commit CI verified separately at delivery and parent compatibility stays unverified.

BIN-197 的[普通本地文件读取](LOCAL-FILE-READ.md)冻结 REP-001/STO-001 内部已保存 Path→同次节点 Source→Actual SHA1→真实完整流的 eager 契约，含精确空摘要无物理对象分支。81项实际组件参考断言只支持有限空流/handle 规则；产品 RED/GREEN、9组单元/race、3组真实 PostgreSQL/文件系统、10项故障变异、共享检查与独立实现审查通过；精确提交 CI 另行核验，不提升 Generic/REST/权限或父项兼容状态。
BIN-197 freezes a bounded internal eager stored-path-to-bytes contract, including exact-empty synthesis. Product unit/race, real database/filesystem, mutation/shared and independent review gates pass; exact-commit CI is verified separately; broader compatibility remains unverified.

BIN-198 的[下载接入契约](GENERIC-DOWNLOAD-INTEGRATION.md)补充25组实际 GET/HEAD/路径/缺失/匿名样本及独立453断言核验，核对有限 local HEAD 无内容打开的静态分支，冻结下一普通下载适配器的支持域与验收。仅契约，尚无产品 HTTP 下载路由；PKG-007/API-001 等父项保持未验证。
BIN-198 adds bounded download observations and acceptance for the next ordinary adapter. This is a contract, not an implemented HTTP download capability; parent parity remains unverified.

# BinFlow — 已确认需求 / Confirmed Requirements

更新 / Updated: 2026-10-16 (Asia/Shanghai)
状态 / Status: 需求已收敛，启动架构待用户审阅；尚未进入实施。 Requirements consolidated; startup architecture awaits owner review; implementation has not started.

## 产品范围 / Product scope

- 正式名称 BinFlow，商业交付，公开 GitHub 仓库：https://github.com/0ldlight/bf 。
  Product: BinFlow; commercial delivery; public GitHub repository.
- 使用 Go 与 TypeScript；目标基线为 Artifactory 7.161.26 Enterprise+ 及其必需的平台服务。
  Implement in Go and TypeScript; baseline: Artifactory 7.161.26 Enterprise+ and required platform services.
- 外部产品集成能力纳入 Artifactory 功能范围；Xray、Distribution、Curation 等独立产品自身不在实现范围。
  Include Artifactory integration surfaces; separate products themselves are outside scope.
- 功能、包客户端、REST API、AQL、权限、错误行为、JFrog CLI 和 CI 集成全部对齐。
  Align features, package clients, REST API, AQL, authorization, errors, JFrog CLI and CI integrations.
- 不迁移现有业务数据；BinFlow 自身版本升级、备份恢复与数据结构升级仍需实现。
  No legacy business-data migration; BinFlow upgrades, backup/restore and schema evolution remain required.
- 阶段交付不缩减最终全功能范围；未完成的兼容项必须显式追踪。
  Incremental delivery does not reduce final scope; incomplete compatibility must remain visible.

## 部署与兼容 / Deployment and compatibility

- 支持 Kubernetes/Helm、Docker/Compose、Linux 压缩包、RPM/DEB；不支持 Windows。
  Support Kubernetes/Helm, Docker/Compose, Linux archives, RPM/DEB; Windows excluded.
- Linux 发行版、版本、CPU 架构、离线安装、代理与内网环境，按目标版本实际能力建立矩阵。
  Freeze a target-version matrix for Linux distributions, versions, CPU architectures, offline and proxy/private-network operation.
- 各部署方式的单节点、HA、升级、回滚、备份恢复，按原版实际支持条件对齐。
  Match supported topology, HA, upgrade, rollback and recovery per deployment method.
- 多数据库类型属于最终验收范围，不以开发环境 PostgreSQL 代替其他数据库的验证。
  Multiple database engines are required; PostgreSQL development does not replace other-engine validation.
- 公网 UAT：uat.binflow.org，公开体验；用户允许 CI 自动部署。
  Public UAT: uat.binflow.org; automated CI deployment authorized.
- 本地 K8s 用于开发、集成、兼容、性能与 HA 测试，可创建项目 namespace；基础服务由项目搭建。
  Local Kubernetes hosts development and validation; project namespaces and supporting services may be provisioned.
- UAT 适合作为轻量演示环境，其资源不作为企业容量与 HA 验收依据。
  UAT is a lightweight demo; enterprise capacity and HA acceptance use separate resources.

## 前端与设计 / Frontend and design

- 采用 shadcn/ui；功能完整、主要操作流程一致，重新设计视觉与交互。
  Use shadcn/ui; preserve capabilities and primary workflows with a redesigned interface.
- 产品支持简体中文和英文、深浅主题、移动端适配、键盘操作。
  Simplified Chinese/English, light/dark themes, mobile layouts and keyboard workflows.
- 设计参考：https://minimals.cc/dashboard 、https://shadcn-admin.netlify.app/ 及目标 Artifactory 实例。
  References: Minimals, Shadcn Admin and the baseline instance.
- 研发文档、注释、提交说明和 PR 描述按双语要求制定规范；代码标识符使用英文。
  Establish bilingual conventions for engineering artifacts; code identifiers use English.
- 已允许安装必要 CLI、依赖、skills 和插件；具体采用项仍需控制范围、来源和版本。
  Necessary development tools and integrations may be installed; selection and versions remain controlled.

## 商业授权与研究资料 / Licensing and research materials

- BinFlow 需自主实现试用、许可证签发、到期管理、功能授权与离线授权能力，参考 Artifactory 的产品设计。
  Implement BinFlow trial, issuance, expiration, entitlements and offline licensing.
- 代码许可证交由 Codex 提出方案；拟采用 AGPL-3.0-only 与商业双许可，详见启动提案，尚未发布许可证。
  Proposed source model: AGPL-3.0-only plus commercial licensing; no license has been published.
- 原版安装包、反编译结果、截图和接口采样放入私有资料库；公开仓库保存原创实现与可公开资料。
  Keep original packages, decompilation, captures and samples in a private research store; publish original implementation and releasable materials.
- 私有资料库需支持跨 Mac 使用，定位以制品 ID 与摘要为准，不依赖本地绝对路径。
  Private research artifacts must be portable through IDs/checksums rather than machine-specific paths.
- 用户密码、API Key、SSH 凭据、签发私钥与 kubeconfig 不进入代码库、公开任务或日志。
  Credentials and signing private keys stay outside source control, public tasks and logs.

## 团队、审批与执行 / Team, approvals and execution

- 真人团队仅项目所有者；架构选型、main 分支 PR 合并、版本发布由所有者决定。
  Sole human: project owner; owner approves architecture, main merges and releases.
- Codex 为主 agent，负责规划、派单、审查、验证与集成；Claude Code 执行子任务，模型 GLM-5.3。
  Codex orchestrates and reviews; Claude Code runs implementation tasks with GLM-5.3.
- 智谱 Coding Plan Pro；所有执行主机合计最多 3 个 Claude Worker。
  Zhipu Coding Plan Pro; maximum three concurrent Claude Workers across execution hosts.
- 当前 Mac 用于初期；后续全部工作迁移到另一台 Intel/AMD64 Mac，该机器已配置 Claude Code 与 K8s 访问。
  Start on the current Mac, then move all work to the prepared Intel/AMD64 Mac.
- 最终需后台调度、失败重试、阻塞等待人工处理；恢复依据为持久任务、代码提交和交接记录。
  Require persistent scheduling, retries and human-blocked states; resume through durable tasks and commits.
- Agent 创建分支、提交、推送和 PR；所有者审核合并。
  Agents prepare branches/commits/pushes/PRs; owner merges.
- 全部任务、里程碑、决策待办与阻塞在现有 Linear binflow 项目处理。
  Use the existing Linear binflow project for planning and approvals.
- BOOST.md 已由用户明确撤销适用；不再构成前置条件。
  Owner explicitly retired BOOST.md; it is no longer a prerequisite.

## 环境核实与剩余条件 / Verification and remaining conditions

- 2026-10-16 已重新核实原版 API：7.161.26，revision 86126900；许可证类型 Enterprise Plus Trial。
  Reverified baseline API; installed version matches target, license reports Enterprise Plus Trial.
- UAT SSH 只读检查成功，DNS 指向用户提供的主机；详见本机环境记录。
  UAT read-only SSH inspection and DNS verification succeeded; details remain in local environment notes.
- 2026-10-09 已验证 Claude Code 到 GLM-5.3 的一轮无工具调用；代码任务和并发尚未验证。
  A one-turn no-tool GLM-5.3 probe passed earlier; coding workflows and concurrency remain unverified.
- Linear 于 2026-10-16 返回 USER_NOT_LOGGED_IN；需重新连接后定位现有项目。
  Linear currently requires reconnection before locating the existing project.
- 启动实施前需要所有者审阅 STARTUP-PROPOSAL.md；具体性能指标以受控对照试验后确定。
  Owner reviews STARTUP-PROPOSAL.md before implementation; quantitative targets follow controlled baseline measurements.

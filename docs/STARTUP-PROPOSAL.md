# BinFlow — 启动提案 / Startup Proposal

日期 / Date: 2026-10-16
状态 / Status: 待架构审批；本文不是已实现能力说明。 Pending architecture approval; this is a proposal, not implemented behavior.

## 1. 研发目标与完成定义 / Objective and definition of done

目标为 Artifactory 7.161.26 Enterprise+ 全功能及必需平台服务的原创 Go/TypeScript 实现。范围以版本冻结的功能矩阵为准。
Build an original Go/TypeScript implementation of the defined baseline. A version-frozen feature matrix governs completion.

每项兼容能力必须具备：
Each compatibility item must have:
1. 功能 ID、版本、入口、前置条件与依赖 / feature ID, version, entry points, preconditions and dependencies.
2. 原版可复现证据及私有资料引用 / reproducible baseline evidence and private artifact references.
3. 正常、异常、权限和边界案例 / success, error, authorization and boundary cases.
4. BinFlow 实现位置、测试结果与构建摘要 / implementation, test evidence and build identity.
5. 差异结论与审阅结果 / disposition of differences and review.
6. 对应数据库、包客户端、部署与架构组合 / applicable database, client, deployment and CPU combinations.

完成状态区分：待调查、已确认、开发中、验证中、通过、有差异、外部依赖阻塞；只有证据通过才声明兼容。
Track discovery, implementation and verified parity separately. Working demos alone do not establish parity.

## 2. 架构决定建议 / Proposed architectural decisions

### ADR-001 Go HTTP 与服务结构 / Go HTTP and service structure

选择 Go 标准库 net/http + chi，按领域组织模块，按负载与信任边界拆分运行单元。
Use net/http + chi; organize by domain and separate runtime units where load or trust boundaries justify it.

理由：采用标准 Handler 接口，便于控制上传下载、代理、Range、取消与包协议路由；兼容性由测试证明，不由框架保证。
Rationale: standard handlers support explicit transfer/proxy behavior. Compatibility requires tests, not router selection.

初期运行单元：
Initial runtime units:
- 核心服务：仓库配置、元数据、权限应用、包协议适配、REST/AQL、审计。
  Core: repositories, metadata, authorization application, protocol adapters, REST/AQL and audit.
- 后台任务进程：索引、清理、复制、联邦、定时任务等，按阶段增加。
  Workers: indexing, cleanup, replication, federation and scheduled operations as delivered.
- 管理前端：静态资产独立打包，由 HTTP 入口交付。
  Admin frontend: separately built static assets served through the HTTP entry point.
- 授权签发控制面与公开体验实例独立，签发私钥不进入体验环境。
  License issuer is isolated from the public demo; the demo receives no issuance private key.

对必需平台服务逐个调查接口、信任关系与运行约束，再决定独立进程还是模块化实现。
Investigate required platform interfaces and trust boundaries before final process decomposition.

### ADR-002 存储与多数据库 / Storage and multiple databases

数据库访问使用 database/sql、明确的领域存储接口、数据库方言与迁移适配；驱动逐个验证。
Use database/sql with explicit repository interfaces, dialects and migrations; validate drivers individually.

首批调查 PostgreSQL、MySQL、MariaDB、SQL Server、Oracle；Derby 的目标版本适用范围单独调查，不能默默删除。
Investigate five primary engines and separately establish any applicable Derby requirement.

PostgreSQL 为开发默认。建立相同功能契约在各数据库上的测试；避免把 PostgreSQL 特有 SQL 暴露到领域逻辑。
Use PostgreSQL for development; test common contracts per engine and isolate dialect-specific queries.

迁移必须记录版本、校验、锁与失败恢复；对不安全的降级采用备份恢复流程，明确条件。
Version and verify migrations; define safe rollback or restore conditions explicitly.

二进制存储与元数据解耦，提供本地文件系统和对象存储适配；按目标版本矩阵扩展存储提供商。
Separate blob storage from metadata; start with filesystem and object storage adapters and expand per baseline.

需验证：校验和部署、去重、并发写入、断点/分段、代理缓存、元数据与二进制一致性、GC 与恢复。
Validate checksums, deduplication, transfers, caching, consistency, GC and recovery.

### ADR-003 前端 / Frontend

React + TypeScript + Vite + shadcn/ui；路由、服务端状态和复杂列表分别评估 TanStack Router、Query、Table/Virtual。
Use React/TypeScript/Vite/shadcn; evaluate focused TanStack libraries for routing, server state and large lists.

统一设计 tokens、布局、表单、仓库树、表格、权限反馈、错误与空态；支持双语、主题、键盘与移动端。
Define consistent tokens and application patterns across languages, themes and input modes.

首批设计验收页面：登录、仓库列表、制品浏览与详情、仓库配置、权限配置。
First design review: sign-in, repositories, artifact browser/detail, repository configuration and permissions.

Storybook 展示关键状态；Playwright 验证浏览器流程和 BinFlow 视觉回归；axe-core 辅助无障碍检查。
Use Storybook for state coverage, Playwright for flows/regressions and axe-core for accessibility checks.

视觉基线为用户批准的 BinFlow 设计，不以原版像素相同作为验收条件。
Approved BinFlow designs define visual baselines; baseline Artifactory defines functionality and primary workflow evidence.

### ADR-004 商业与代码许可 / Commercial and source licensing

建议代码采用 AGPL-3.0-only，并由有权授权的权利人提供另一套商业许可。正式 LICENSE/商业条款在批准后整理。
Propose AGPL-3.0-only plus a separate commercial license offered by the appropriate rights holder.

AGPL 允许商业使用和修改，并包含针对网络交互修改版本的源码提供要求；它不能作为禁止商用条款。
AGPL permits commercial use/modification and includes source-availability obligations for modified network-facing versions; it is not a noncommercial restriction.

商业交付围绕商业许可选项、官方签名发行版、支持与交付服务定义，不承诺源码公开后无法修改授权检查。
Define commercial offerings around licensing options, official distributions and services; public source remains modifiable.

外部贡献在合并前明确可用于所需许可模式的授权；第三方依赖保留各自许可和声明。
Establish contribution rights before accepting changes; retain dependency licenses and notices.

### ADR-005 BinFlow 运行许可证 / Runtime licensing

原版产品授权作为行为研究输入；BinFlow 使用自己的格式、标识、密钥与签发流程。
Study product behavior while using BinFlow-specific identifiers, formats and keys.

拟定能力：试用申请/签发、客户与实例/集群绑定、功能集合、容量/节点额度、有效期、续期、离线导入导出、密钥轮换与审计。
Proposed capabilities: trial issuance, customer/cluster binding, entitlements, quotas, expiry/renewal, offline exchange, rotation and audit.

使用非对称签名；运行实例只持有验证公钥。到期、宽限期、撤销、时钟回退和离线条件必须在商业规则中分别定义。
Use asymmetric signatures with verification-only runtime keys. Explicitly define expiry, grace, revocation, clock rollback and offline behavior.

拟定默认：到期不删除数据；保留管理、导出、读取恢复通道，付费写操作按批准规则限制。该策略需在授权专项 ADR 中审阅。
Proposed default: preserve data and recovery access on expiry; paid operations follow reviewed rules.

试用时长、价格、额度、宽限期作为可配置商业参数，在正式对外发布前由所有者确定。
Pricing and entitlement values are configurable business parameters owned by the project owner before release.

## 3. 环境与交付 / Environments and delivery

- 本机：编辑、Agent 编排、CLI 与快速测试 / Mac: editing, agents, CLI and fast tests.
- 本地 K8s：dev、test、兼容、性能与 HA / local Kubernetes: development, validation, performance and HA.
- UAT：单机轻量演示；建议 Compose + PostgreSQL + 本地二进制卷 + HTTP/TLS 入口，设置资源与演示数据配额。
  UAT: lightweight single-host demo; propose Compose/PostgreSQL/local blob volumes/TLS entry, with explicit resource and data limits.
- UAT 不承载完整监控栈、原版实例、生产签发私钥或大规模压力测试。
  Keep heavyweight validation and issuance control out of the small UAT environment.
- dev/test 对象存储产品在启动阶段比较维护、资源、S3 兼容和许可后决定，未锁定具体厂商。
  Select development object storage after a focused compatibility/resource/license evaluation.
- TLS 拟通过 ACME 自动申请和续期，先检查 80/443 可达性、现有站点与域名控制。
  Propose automated ACME TLS after verifying ingress and domain prerequisites.
- UAT 公开体验：管理员私有、演示账号权限受限、隔离演示仓库、上传配额和定期重置。
  Public demo: private administration, limited demo roles, isolated repositories, quotas and resets.
- 公开安装包与镜像拟使用 GitHub Releases/GHCR；私有研究制品使用单独受控资料库。
  Propose Releases/GHCR for distributable artifacts and a separate private research store.

流水线 / Pipelines:
1. PR：同一组仓库脚本执行格式、静态分析、单元、关键契约与前端检查。
   PRs use shared repository scripts for consistent quality gates.
2. Jenkins：访问本地 K8s，承担集成、原版差异、多数据库与 HA/性能测试。
   Jenkins runs private-environment integration and compatibility validation.
3. 所有者合并 main 后，CircleCI 构建对应提交的镜像、摘要与部署清单，自动部署 UAT 并执行 smoke。
   After owner merge, CircleCI builds identifiable artifacts, deploys UAT and runs smoke tests.
4. Git tag、对外 release、签名发行包及正式版本发布需要所有者批准；UAT 自动部署不自动创建 release。
   Public releases require owner approval; UAT deployment alone does not publish a release.
5. 部署失败保留证据；恢复上一镜像前先判断数据库版本兼容性，禁止盲目自动降级 schema。
   Preserve failure evidence and check schema compatibility before application rollback.
6. 文档统一使用已确认的 Fern，复用现有组织或站点，发布域名为 docs.binflow.org；文档变更需要预览、链接/配置检查和可追踪的发布记录，具体发布接入在 P0 核实。
   Use the confirmed Fern platform and existing organization/site at docs.binflow.org; validate previews, links/configuration and traceable publication during P0.

文档范围覆盖产品、API、运维和研发规划/规范/ADR，延续中英双语。公开内容与私有环境/研究资料须分别审阅访问范围；私有内容不能因迁移文档而进入公开站点。版本控制的 Fern 文档源是迁移后的维护入口，仓库 README 和必要的 Agent 接续入口保留链接，避免双份正文。
Documentation includes product, API, operations, engineering plans/conventions/ADRs and both languages. Review public/private access separately. Maintain versioned Fern sources after migration and retain repository/agent entry-point links without duplicate content.

具体 CI Secret 和测试服务接入在启动预检中核实；不自动覆盖现有全局配置。
Verify actual CI connections and secrets during bootstrap without overwriting shared global settings.

## 4. Agent 团队与调度 / Agent team and scheduler

这是拟建角色体系，不表示已有团队进程或后台服务运行。
These are proposed roles; no persistent agent team or daemon has been started.

| 角色 / Role | 责任与产出 / Responsibility and output |
|---|---|
| Codex 主 agent / Lead | 功能与架构方案、依赖拆分、任务验收、审查和 PR 集成 / planning, review, acceptance and integration |
| 行为研究 / Behavior research | API、配置、协议、原版案例与证据索引 / baseline evidence and contracts |
| Go 核心与存储 / Core/storage | 领域逻辑、blob、数据库和迁移 / domains, blobs, engines and migrations |
| 包协议 / Package protocols | 各生态、缓存、虚拟仓库与客户端契约 / ecosystems and client compatibility |
| 身份权限与授权 / Identity/licensing | 认证授权、集群信任、许可证和审计 / identity, trust, licensing and audit |
| 前端设计工程 / Frontend | tokens、组件、页面、交互和无障碍 / UI system and workflows |
| 测试与兼容 / Compatibility QA | 原版对照、数据库、部署、异常和故障案例 / differential and failure validation |
| 平台与可靠性 / Platform | Helm/Compose/包安装、流水线、恢复和调度 / packaging, pipelines, recovery and scheduling |
| 独立审查 / Independent review | 正确性、数据边界、安全与依赖审查 / correctness and security review |

所有 Claude 角色轮流占用最多 3 个 GLM-5.3 执行槽；实现者不能作为同一变更唯一审查者。
All Claude roles share three slots. The implementer is not the sole reviewer.

每任务独立 worktree/分支，声明文件责任范围、输入证据、验收条件、上下文预算和输出报告；禁止隐式再派生 Claude 进程绕过并发。
Each task has an isolated checkout, ownership and acceptance contract; nested processes must not bypass concurrency control.

调度器建议使用 Go，持久化执行状态；Linear 管理业务任务与审批，调度数据库管理认领、租约、重试和进程记录。
Propose a Go scheduler; Linear owns planning/approvals, while the execution database owns leases and process state.

状态 / States:
Ready → Claimed → Running → Verifying → Review → AwaitingOwnerMerge → Done
失败分为 Retryable、Blocked、Cancelled；配额/认证/架构问题不无限重试。
Failures have explicit retryable or blocked states; quota/auth/architecture issues do not loop.

拟定重试：瞬时网络错误指数退避最多 2 次；重复实现失败转诊断/阻塞。限制任务预算与排队深度。
Propose bounded backoff for transient failures; repeated implementation failures require diagnosis.

当前主机退出调度、确认 Worker 停止、提交/交接或保存私有快照后，再由另一台 Mac 接管。
Handover stops workers and persists work before the next Mac takes ownership.

失联主机中的运行请求不能仅因租约超时就认定已释放；重派前核对实际进程，避免超过套餐并发。
Do not equate a lease timeout with a terminated provider request; verify before reassignment.

Codex 后台接入通过 CLI/SDK 适配器验证认证和恢复；额度不可用时持久等待，不自动改用其他付费模型。
Verify the Codex adapter and pause durably on unavailable capacity; do not silently change paid providers.

## 5. 反编译研究与 token 管理 / Research and token efficiency

- 下载目标版本并计算摘要，建立安装包、内含服务、配置、数据库资源、前端包与版本清单。
  Verify packages and inventory all included components.
- Java 按模块/符号/调用关系索引；Go 伪码按业务符号与函数边界切分，标记 runtime/第三方内容。
  Index Java structure and split Go pseudocode by symbol/function with provenance.
- 前端调查微前端、静态包与公开文档，补齐真实行为证据；不把旧版残缺还原视作全量源码。
  Establish frontend evidence without treating incomplete old extraction as complete source.
- 默认用符号检索、全文索引和小片段读取；有明确收益后才增加向量索引。
  Start with symbols and full-text retrieval; add embeddings only after measured benefit.
- 研究输出为行为规范、边界案例和测试契约；实现任务仅加载相关上下文。
  Produce contracts and cases; load task-specific context.
- 每次记录 token、失败类型、重读与返工；节省目标不能替代验证质量。
  Measure tokens and rework without weakening validation.

## 6. 阶段与验收 / Phases and acceptance

| 阶段 / Phase | 交付与出口 / Deliverable and exit |
|---|---|
| P0 基线与研发体系 / Foundation | 环境预检、双语规范、功能矩阵 v1、研究索引、CI smoke、Linear 流程、Fern 文档迁移与 docs.binflow.org 接入、可跨 Mac 接续的一条 Agent 任务 / reproducible foundation, Fern documentation/domain integration and one complete agent workflow |
| P1 最小贯通 / Vertical slice | 登录、权限、Generic local 仓库、上传下载、校验和、元数据、双语主题、K8s与UAT；用户审阅 / end-to-end verified slice |
| P2 通用仓库能力 / Repository foundation | local/remote/virtual、缓存、属性、搜索/AQL、构建信息、清理、API 契约 / shared repository and API capabilities |
| P3 包生态 / Ecosystems | 按依赖推进 Maven、npm、Docker/OCI、PyPI、Go、Helm 及目标版本剩余类型；逐个客户端验收 / all scoped ecosystem adapters and client tests |
| P4 企业能力 / Enterprise | 身份集成、精细权限、HA、复制/联邦、审计、许可证与平台必需服务 / enterprise feature contracts |
| P5 部署与兼容完成 / Deployment coverage | 数据库、Linux/CPU、所有安装方式、离线、代理、升级恢复、性能和故障矩阵 / deployment and resilience acceptance |
| P6 发布候选 / Release candidate | 完整追踪矩阵、已知差异清单、文档/运维手册、签名制品；所有者发布决定 / traceable evidence and owner release decision |

平台与数据库契约在 P0/P1 建立，跨阶段持续测试；P5 是覆盖出口，不是首次验证。
Establish portability contracts early; later phases complete coverage rather than introduce it for the first time.

性能不套用无环境条件的“原版指标”；按相同资源、客户端、数据与缓存条件测量目标版本，再提出 p95/p99、吞吐、内存和恢复目标。
Benchmark controlled comparable environments before approving quantitative targets.

先用 P0/P1 实测任务吞吐和返工率，再给出后续工期区间；不以并发数推算固定完成日期。
Estimate later phases from measured throughput and rework rather than theoretical concurrency.

## 7. 实施前审阅 / Pre-implementation review

需要所有者决定：
Owner decisions requested:
- ADR-001/002/003 的核心、数据库和前端架构方向 / architecture direction.
- AGPL 与商业双许可方向 / licensing direction.
- 分阶段顺序、流水线职责、调度与迁移机制 / delivery and execution plan.

接入事项：
Connection actions:
- Linear 当前返回 USER_NOT_LOGGED_IN；恢复连接后读取现有项目，不另建同名项目。
  Restore the existing Linear connection before synchronization.
- CI/GHCR 权限、UAT 80/443、试用剩余时间/功能可用性及目标平台矩阵在 P0 核实。
  Verify CI credentials, ingress, baseline entitlement and version matrices during P0.
- 现有 Fern 文档源为 [0ldlight/binflow-docs](https://github.com/0ldlight/binflow-docs)，当前组织 `binflow`、默认站点 `binflow.docs.buildwithfern.com`，配置已包含 `custom-domain: docs.binflow.org`。2026-10-18 经认证 API 返回默认站点 LIVE；自定义域名与内容迁移仍需验收。
  Reuse the verified current organization/default site and existing custom-domain configuration. Authenticated API reports the default site LIVE; custom-domain and content-migration acceptance remain open.
- 文档仓库已有 GitHub Actions 检查、PR 预览和 main 发布工作流；2026-10-18 已配置 FERN_TOKEN Secret，现有 main 提交 `c69ee35` 的[发布重跑成功](https://github.com/0ldlight/binflow-docs/actions/runs/39141090564/attempts/2)。文档内容迁移与自定义域名验收仍未完成；复用现有文档流水线，不重复创建 CircleCI 文档发布流程。
  Existing Actions workflows cover checks, PR previews and main publication. CI authentication and the linked publication rerun passed on 2026-10-18. Content migration/custom-domain acceptance remain open; reuse the existing workflow.
- 任一外部服务到期或不可用，明确阻塞具体功能，不能把缺失证据登记为通过。
  Record evidence gaps as scoped blockers, not passed parity.

## 8. 参考 / References

- chi: https://pkg.go.dev/github.com/go-chi/chi/v5
- Database requirements: https://docs.jfrog.com/installation/docs/artifactory-database-requirements
- Database matrix: https://docs.jfrog.com/installation/docs/database-support-matrix
- AGPL text: https://opensource.org/license/agpl-3-0
- Storybook: https://storybook.js.org/docs
- Playwright visual testing: https://playwright.dev/docs/test-snapshots
- Accessibility: https://playwright.dev/docs/accessibility-testing
- Codex SDK: https://learn.chatgpt.com/docs/codex-sdk
- Claude noninteractive execution: https://code.claude.com/docs/en/headless
- Fern custom domains: https://buildwithfern.com/learn/dashboard/configuration/custom-domains
- Fern project structure: https://buildwithfern.com/learn/docs/getting-started/project-structure

在线文档可能覆盖比目标更新的版本；目标支持结论必须回到 7.161.26 证据。
Live documentation may describe newer versions; freeze baseline conclusions against 7.161.26 evidence.

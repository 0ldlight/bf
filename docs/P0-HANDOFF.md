# 接续研发 / Continue the project

## 接续入口 / Start with the contract

依次读取[已确认需求](PROJECT-BRIEF.md)、[架构决定](ARCHITECTURE.md)、[P0 计划](P0-PLAN.md)、[工程地图](ENGINEERING-MAP.md)、[能力矩阵](CAPABILITY-MATRIX.md)与[研究索引](RESEARCH-INDEX.md)。已确认需求无需重新访谈。
Read the requirements, decisions, P0 plan, engineering map, matrix and research index without restarting discovery.

源码仓库是 [0ldlight/bf](https://github.com/0ldlight/bf)，使用文档维护在 [0ldlight/binflow-docs](https://github.com/0ldlight/binflow-docs)，工程文档维护在本仓库 `docs/`。任务、依赖、审批与最新状态使用[现有 Linear 项目](https://linear.app/binfloow/project/binflow-2ad2bc04f63e)。
Keep usage docs in the separate Fern repository and engineering docs in source `docs/`; use the existing Linear project as the task authority.

全部产品代码都在 `bf` 主检出：每任务一条分支，仅在确有必要时在 `bf` 内创建隔离 worktree（如 `.binflow/worktrees/`），不建立兄弟代码目录。历史遗留的本地 `.binflow` 归档与证据是被忽略的私有材料，不是活跃代码源。代码结构导航见[工程地图](ENGINEERING-MAP.md)。
All product code stays in the `bf` primary checkout: one branch per task, an isolated worktree inside `bf` (for example `.binflow/worktrees/`) only when truly needed, and no sibling code directories. The historical local `.binflow` archive/evidence is ignored private material, not an active source. See the [engineering map](ENGINEERING-MAP.md) for code navigation.

## 本机配置 / Host-local access

各主机独立配置 GitHub、K8s、Claude/GLM-5.3、原版、Jenkins、CircleCI 与 UAT 访问。仅在被忽略的本机环境记录中保存地址与凭据文件引用，凭据本身不写入文档或公开任务。
Provision access per host. Store private endpoints and credential-file references only in ignored environment notes; never put secrets in documentation/tasks.

2026-10-30 所有者确认：后续 BinFlow UAT 部署可使用已连接 Supabase 插件提供的数据库。插件已可见名为 `binflow` 的健康项目；实际部署时再核实目标项目、连接方式与迁移状态，并通过私有环境配置注入数据库凭据。此约定记录可用资源，不代表已完成数据库接入或 UAT 部署；项目标识、数据库地址及凭据继续仅保存在本机私有环境记录中。
On 2026-10-30, the owner authorized the database available through the connected Supabase plugin for future BinFlow UAT deployments. A healthy project named `binflow` is visible through the plugin. Verify the target project, connection method and migration state at deployment time, and inject database credentials through private environment configuration. This records an available resource, not completed database integration or UAT deployment; keep project identifiers, database endpoints and credentials in private host-local notes only.

私有资料通过制品 ID 与摘要定位，不假定另一台 Mac 使用相同绝对路径。
Locate research by artifact ID/digest rather than assuming identical host paths.

`.binflow` 是单机私有状态（任务记录、槽位、控制脚本、研究资料），Git 不覆盖；每台执行主机应定期执行 `scripts/backup-local-state.sh <私有目标目录>` 生成带校验的归档，迁移或检修前必须先备份。
`.binflow` is single-host private state (task records, slots, control scripts, research material) that Git never covers; every execution host should run `scripts/backup-local-state.sh <private destination>` regularly and always before migration or maintenance.

## Worker 生命周期 / Worker lifecycle

每条任务声明独立分支、工作目录、文件责任、输入证据、验收、上下文范围和报告位置；工作目录遵循上方主检出约定。所有 Claude 角色及重试共用最多 5 个 GLM-5.3 执行槽，禁止隐式派生 Worker。
Each task declares its branch, working directory, ownership, evidence, acceptance and report contract; its working directory follows the primary-checkout policy above. All roles/retries share five slots; no hidden workers.

2026-10-19 所有者确认其他主机均已停止，当前 Mac 接管。之后每次迁移都应重新核对真实进程、任务状态和未完成请求；租约超时不代表进程或服务端请求已终止。
The owner confirmed handover on 2026-10-19. Future migrations must verify actual processes and outstanding requests; lease expiry does not establish termination.

任务恢复依据包括私有任务/槽位记录、Claude 会话标识、Git 提交与 PR；Worker 退出后仍需独立审查与验证，不能直接登记 Done。
Resume through durable task/slot records, session IDs, commits and PRs. Process exit still requires independent review.

## 迁移主机前 / Before changing hosts

1. 停止认领新任务，核实在途请求与真实进程，保存工作区/提交与私有报告。
   Stop claims, verify in-flight work and persist code/private reports.
2. 在 Linear 更新准确状态与阻塞；未验证的能力不得登记为通过。
   Record accurate state without promoting unverified work.
3. 在新主机重新核实认证、路由与执行槽，再按合同恢复；保留旧会话作为可审计证据。
   Recheck host access/slots before resuming and retain audit evidence.

本页是工程接续正文；旧交接快照保存在 Git 历史中。实际出口仍以[P0 计划](P0-PLAN.md)和 Linear 为准。
This is the continuation contract; old handover snapshots remain in Git history and Linear retains current exit state.

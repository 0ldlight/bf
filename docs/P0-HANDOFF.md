# 接续研发 / Continue the project

## 接续入口 / Start with the contract

依次读取[已确认需求](PROJECT-BRIEF.md)、[架构决定](ARCHITECTURE.md)、[P0 计划](P0-PLAN.md)、[能力矩阵](CAPABILITY-MATRIX.md)与[研究索引](RESEARCH-INDEX.md)。已确认需求无需重新访谈。
Read the requirements, decisions, P0 plan, matrix and research index without restarting discovery.

源码仓库是 [0ldlight/bf](https://github.com/0ldlight/bf)，使用文档维护在 [0ldlight/binflow-docs](https://github.com/0ldlight/binflow-docs)，工程文档维护在本仓库 `docs/`。任务、依赖、审批与最新状态使用[现有 Linear 项目](https://linear.app/binfloow/project/binflow-2ad2bc04f63e)。
Keep usage docs in the separate Fern repository and engineering docs in source `docs/`; use the existing Linear project as the task authority.

## 本机配置 / Host-local access

各主机独立配置 GitHub、K8s、Claude/GLM-5.3、原版、Jenkins、CircleCI 与 UAT 访问。仅在被忽略的本机环境记录中保存地址与凭据文件引用，凭据本身不写入文档或公开任务。
Provision access per host. Store private endpoints and credential-file references only in ignored environment notes; never put secrets in documentation/tasks.

私有资料通过制品 ID 与摘要定位，不假定另一台 Mac 使用相同绝对路径。
Locate research by artifact ID/digest rather than assuming identical host paths.

## Worker 生命周期 / Worker lifecycle

每条任务声明独立 worktree/分支、文件责任、输入证据、验收、上下文范围和报告位置。所有 Claude 角色及重试共用最多 3 个 GLM-5.3 执行槽，禁止隐式派生 Worker。
Each task has an isolated worktree, ownership, evidence, acceptance and report contract. All roles/retries share three slots; no hidden workers.

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

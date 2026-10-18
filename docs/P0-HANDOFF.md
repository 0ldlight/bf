# P0 跨 Mac 交接 / Cross-Mac P0 Handover

日期 / Date: 2026-10-16 (Asia/Shanghai)
分支 / Branch: codex/planning-handoff
远端 / Remote: https://github.com/0ldlight/bf.git

## 当前指令与状态 / Current instruction and status

用户要求提交现有规划内容，准备在另一台 Intel/AMD64 Mac 开始 P0。本机仅提交与交接，不执行 P0。
The owner requested this planning snapshot for starting P0 on another Intel/AMD64 Mac. This turn performs handover only.

已确认需求以 PROJECT-BRIEF.md 为准；架构和代码许可建议以 STARTUP-PROPOSAL.md 为准。
PROJECT-BRIEF.md holds confirmed requirements; STARTUP-PROPOSAL.md holds proposed architecture and licensing.

架构、main PR 合并和版本发布仍由用户决定；不要把“提交规划”登记为所有 ADR 已获批准。
Architecture, main merges and releases remain owner decisions; committing planning documents does not approve every ADR.

后续新增的已确认要求：全部 BinFlow 项目文档使用 Fern，域名 docs.binflow.org；复用 [现有文档源仓库](https://github.com/0ldlight/binflow-docs) 与 [Fern Dashboard](https://dashboard.buildwithfern.com)。2026-10-18 核实组织为 `binflow`、默认站点为 `binflow.docs.buildwithfern.com`，取代先前带数字后缀的标识。P0 包含文档迁移、双语、访问范围、预览检查及域名/发布接入；当前未完成 BinFlow 内容迁移。私有环境、凭据与研究资料不得直接发布到公开文档站。
Subsequent confirmed requirement: use Fern for all project documentation at docs.binflow.org and reuse the existing repository/site linked above. P0 includes migration, both languages, access review, previews/checks and domain/publishing integration. BinFlow content migration remains incomplete; preserve private-material boundaries.

文档站配置已包含目标域名；仓库仍是入门示例，已有检查/预览/发布工作流。最新发布因缺少 FERN_TOKEN 失败，域名和 HTTPS 还需在成功发布后核实。不要把配置存在或 fern-check 成功当成已上线。
The site already configures the domain but contains starter examples. Existing publishing failed for missing FERN_TOKEN; validate domain/HTTPS after successful publication. Configuration and a passing check do not establish a live site.

2026-10-18：通过 fern-api CLI 登录并验证当前 `binflow` 组织的 Fern MCP 连接；API 返回默认站点 LIVE，关联上述文档仓库。Codex MCP 已按当前组织配置，凭据仅在本机配置中；这不替代文档仓库 GitHub Actions 的 FERN_TOKEN，自定义域名仍待验收。
2026-10-18: authenticated CLI/MCP access verified for the current organization and default LIVE site linked to the docs repository. Codex MCP credentials remain host-local; CI authentication and custom-domain acceptance are separate checks.

继续时不重启需求访谈；仅针对具体未决事项或新发现提出问题。可先推进 P0 的只读预检、基线调查和任务细化。
Do not restart requirements discovery. Ask only about concrete unresolved decisions or new findings; begin with P0 preflight and baseline investigation.

## 在另一台 Mac 获取 / Obtain on the next Mac

全新目录 / Fresh checkout:

```sh
git clone --branch codex/planning-handoff https://github.com/0ldlight/bf.git
cd bf
git status --short --branch
```

已有 checkout / Existing checkout:

```sh
git fetch origin
git switch --track origin/codex/planning-handoff
```

若本地已有同名分支，切换到该分支并在检查工作区后执行 git pull --ff-only；保留未提交工作，禁止强制重置。
If the local branch already exists, switch to it and pull with --ff-only after checking the working tree. Preserve local work.

## 需要本机独立配置的内容 / Host-local prerequisites

- gh 登录与仓库写权限 / GitHub CLI authentication and repository access.
- Codex 登录、Claude Code 及智谱 GLM-5.3 凭据 / Codex and Claude Code authentication; GLM-5.3 credentials.
- kubeconfig 及 context；用户确认新 Mac 已配好访问，仍需验证 / Kubernetes access, reported configured but requiring verification.
- Artifactory、Jenkins、UAT 的访问方式 / Baseline, CI and UAT access.
- 恢复 Linear 连接；读取现有 binflow 项目，禁止重复创建 / Reconnect Linear and locate the existing project.
- 项目所需 skills/plugins 在新主机重新检查；不能假设与当前主机一致 / Recheck skills and plugins on the new host.

当前电脑 docs/ENVIRONMENT.local.md 包含不带密码的环境地址与历史检查记录，可通过私有渠道复制到新 checkout；该文件被忽略，不随 Git 迁移。
The current Mac has ignored docs/ENVIRONMENT.local.md with noncredential environment notes. Transfer it privately if needed; Git does not carry it.

密码、API Key、签发私钥和完整 kubeconfig 不在交接提交中，需通过现有凭据配置或私有渠道单独提供。
Passwords, API keys, signing private keys and complete kubeconfig are excluded and require separate local provisioning.

旧反编译资料仍仅存在原主机，且对应旧版本；7.161.26 安装包尚未下载与反编译。不要把旧资料当成目标基线。
Old decompilation remains on the original host and is not the target version. The target package has not been downloaded or decompiled.

## 已完成验证 / Completed checks

- 2026-10-16：Artifactory API 7.161.26，revision 86126900，Enterprise Plus Trial。
  Baseline version matches; recheck expiry and required feature availability during P0.
- UAT SSH 与 DNS 只读核实成功；Ubuntu 24.04、x86_64、4 CPUs、约 4 GiB RAM、40 GiB 根盘。
  UAT read-only inspection succeeded; use it as a lightweight public demo.
- 2026-10-09：K8s 4 个 amd64 节点，默认 Longhorn；这是历史快照，需刷新。
  Historical Kubernetes inventory must be refreshed.
- Claude Code 到 GLM-5.3 的单轮无工具调用成功；读写代码、测试和三并发未验证。
  A minimal model probe passed; full worker execution and concurrency are unverified.
- 2026-10-16：Linear 返回 USER_NOT_LOGGED_IN；没有同步任何任务或创建项目。
  Linear was disconnected; no project/tasks were created.

## 尚未执行 / Not yet performed

没有产品代码、Go/前端工程、namespace、数据库、对象存储、CI 配置、TLS、P0 Worker 或后台调度器。
No application scaffold, infrastructure provisioning, CI configuration, TLS, P0 worker or daemon has been implemented.

没有执行数据库、客户端、部署或性能兼容测试；文档中的角色与能力均为计划。
No compatibility or performance suites have run; planned roles and capabilities are not active services.

## P0 建议执行顺序 / Suggested P0 sequence

1. 读取需求与提案，记录具体架构决定；未批准的决定交给所有者审阅，已确认要求不重复询问。
   Review requirements/proposals and record owner decisions without repeating confirmed questions.
2. 检查新主机工具、架构、认证、内网路由、K8s 资源、原版试用状态与 CI 连接。
   Preflight the new host, baseline entitlement and environment connectivity.
3. 恢复 Linear，定位现有项目，建立 P0 子任务、依赖与验收条件。
   Restore Linear and create scoped P0 tasks in the existing project.
4. 下载并校验 7.161.26 包；私有存放，建立组件清单、版本冻结与研究索引。
   Verify the target package and build a private versioned research inventory.
5. 建立研发规范、领域接口、可重复检查入口与必要环境配置；依架构批准情况逐项实施。
   Establish conventions, contracts and reproducible checks according to approved decisions.
   文档使用 Fern 统一维护，迁移可公开的现有项目文档并接入 docs.binflow.org；核实私有文档访问控制、预览、链接/配置检查及发布证据。
   Maintain documentation in Fern, migrate releasable project documents and connect docs.binflow.org; verify private-document access, previews/checks and publication evidence.
6. 跑通一条 Claude 任务：独立 worktree、GLM-5.3、明确输入、验证、报告、提交与 PR。
   Execute one end-to-end worker task with reproducible evidence and owner-reviewed PR.
7. 验证当前主机到新主机的任务恢复记录和执行槽控制，再扩展并发与后台调度。
   Validate durable handover and slot accounting before scaling execution.

P0 出口按启动提案：基线、矩阵 v1、研究索引、CI smoke、Linear 流程、Fern 文档迁移与域名接入和可接续的一条 Agent 任务；P0 完成不等于产品已可用。
P0 exits with reproducible foundations including Fern documentation/domain integration, not a finished product.

所有 Claude 角色及重试共用最多 3 个执行槽。当前主机本次没有启动项目 Worker，不要在两台主机重复派发同一任务。
All Claude roles/retries share three execution slots. This handover started no project workers; avoid duplicate task dispatch.

## 给新会话的接续提示 / Prompt for the next session

> 请读取 README.md、docs/PROJECT-BRIEF.md、docs/STARTUP-PROPOSAL.md、docs/P0-HANDOFF.md，接续 BinFlow 的 P0。已确认需求无需重新访谈。Codex 主导，Claude Code 使用 GLM-5.3 执行任务，最多 3 并发；任务和审批使用现有 Linear binflow 项目。先检查本机凭据与环境、恢复 Linear，再处理具体待决策 ADR。架构、main 合并和版本发布由我决定。私有资料与凭据不进入公开仓库。
>
> Read the four planning documents and continue P0 without restarting requirements discovery. Codex leads; Claude Code uses GLM-5.3 with three shared slots. Use the existing Linear project, verify host-local access, and obtain owner decisions for unresolved ADRs. Keep research material and credentials private.
>
> 全部项目文档使用 Fern，域名 docs.binflow.org；复用所有者现有组织或站点，文档迁移与域名接入属于 P0。先定位现有站点与源仓库，并核实公开/私有访问范围。
>
> Maintain all project documentation with Fern at docs.binflow.org using the owner's existing organization/site. Include migration and domain integration in P0, first locating the existing site/source repository and verifying public/private access.

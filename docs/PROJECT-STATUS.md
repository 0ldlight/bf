# 项目状态 / Project status

2026-10-19 所有者授权启动 P0 并批准 Go/存储/前端三项方向；**P0 仍在进行，没有任何产品兼容项完成，也没有产品版本发布**。本页 2026-11-13 以默认分支合并记录（PR #67–#99）与仓库实测为证据刷新。
P0 was authorized on 2026-10-19 with three approved directions and **remains in progress: no product-parity capability is complete and no release has been published**. This page was refreshed on 2026-11-13 from merged default-branch evidence (PRs #67–#99) and repository measurements.

原版完整安装包摘要与组件调查位于[研究索引](RESEARCH-INDEX.md)，所有兼容项按[能力矩阵](CAPABILITY-MATRIX.md)逐项取证；[Linear](https://linear.app/binfloow/project/binflow-2ad2bc04f63e)是任务和审批的实时来源。
The research index and matrix describe evidence boundaries; Linear is the live task/approval authority.

## 近期合并（公开证据）/ Recently merged (public evidence)

- **普通上传链（BIN-209–BIN-220，PR #67–#86）**：从上传输入画像、覆盖写判定、权限预检、写授权持久化、提交锁、只读重检到进程路由与运行时 RLS 初始化，已在默认分支合并；受控 UAT 对普通 PUT 的验收已记录（PR #83/#86）。
  **Ordinary upload chain (BIN-209–BIN-220, PRs #67–#86)**: the upload input profile, overwrite decision, permission preflight, persisted write grants, commit lock, transactional recheck, process routing and runtime RLS initialization are merged; bounded UAT acceptance for ordinary PUT is documented (PRs #83/#86).
- **JSON 制品与条件 GET（PR #88–#90）**：普通传输支持不透明 JSON 制品并有受控 UAT 验收记录（#88/#89）；有界条件 GET 不打开命中的内容即可回答（#90）。
  **JSON artifacts and conditional GET (PRs #88–#90)**: ordinary transfers support opaque JSON artifacts with recorded bounded UAT acceptance (#88/#89); the bounded conditional GET answers without opening matched content (#90).
- **研发流程与工具链加固（PR #87、#97）**：staticcheck 门禁与覆盖率报告进入共享检查脚本，govulncheck 以固定版本进入模块工具依赖，清理脚本增加竞态护栏（#97）。
  **Process/tooling hardening (PRs #87, #97)**: the staticcheck gate and coverage reporting joined the shared check script, govulncheck is pinned as a module tool dependency, and the prune script gained race guards (#97).
- **依赖更新（PR #93/#94/#99）**：actions/setup-go 7.0.0、pgx v5 5.12.0（integration 模块）、golang.org/x/crypto 0.58.0（双模块同步）。
  **Dependency updates (PRs #93/#94/#99)**: actions/setup-go 7.0.0, pgx v5 5.12.0 (integration module) and golang.org/x/crypto 0.58.0 (both modules in sync).
- **测试覆盖提升（PR #98）**：PostgreSQL 适配器单元覆盖提升（id 预留、快照查找、读池等）。
  **Test coverage raises (PR #98)**: PostgreSQL adapter unit coverage raised (id reservations, snapshot lookup, read pool and peers).

另有进行中的交付：security/process 单元覆盖提升在 PR #100 审查中；govulncheck 专用 CI 作业等待 workflow 凭据启用。
In flight: the security/process unit-coverage raise is under review in PR #100; the dedicated govulncheck CI job awaits workflow credentials.

## 单元测试覆盖率 / Unit-test coverage

默认分支（660edb8）`go test -cover` 实测（2026-11-13）：
Measured with `go test -cover` on the default branch (660edb8) on 2026-11-13:

| 包 / Package | 覆盖率 / Coverage |
|---|---|
| internal/binary、internal/content | 100% |
| internal/repository、internal/localrepo | 97.7% |
| internal/registry/postgres | 97.0% |
| internal/httpserver | 96.4% |
| cmd/binflow-contract | 93.2% |
| internal/compatprobe | 94.7% |
| internal/localrepo/postgres | 85.0% |
| internal/repository/postgres | 84.7% |
| internal/filestore | 84.0% |
| internal/security | 69.6%（PR #100 审查中为 98.8%）/ 69.6% (98.8% in PR #100 under review) |
| internal/process | 68.8%（PR #100 审查中为 84.1%）/ 68.8% (84.1% in PR #100 under review) |
| cmd/binflow | 0%（组合根，仅冒烟）/ 0% (composition root, smoke-covered only) |

覆盖率只统计单元测试；真实 PostgreSQL 契约由 CI 的实库作业与 UAT 验收另行取证。
Coverage counts unit tests only; real-PostgreSQL contracts are evidenced separately by the CI database job and UAT acceptance.

## CI 分工 / CI division of labor

- **GitHub Actions（验证）**：`Check` 运行共享 `./scripts/check.sh`（gofmt、模块校验、vet、staticcheck、race 测试、临时构建、真实进程 smoke）；`PostgreSQL registry (real DB)` 对固定 PostgreSQL 17.10 镜像运行 `./scripts/check-postgres.sh`。
  **GitHub Actions (validation)**: `Check` runs the shared `./scripts/check.sh` (gofmt, module verify, vet, staticcheck, race tests, temporary build, real-process smoke); `PostgreSQL registry (real DB)` runs `./scripts/check-postgres.sh` against a pinned PostgreSQL 17.10 image.
- **Jenkins（构建）**：制品构建出口（Jenkinsfile），本仓库不做部署。
  **Jenkins (builds)**: the artifact build exit (Jenkinsfile); no deployment from this repository.
- **CircleCI（UAT 部署）**：仅负责 UAT 部署出口。
  **CircleCI (UAT deploys)**: owns the UAT deployment exit only.
- **未启用**：govulncheck 专用 CI 作业（工具已在 go.mod 固定，作业等待 workflow 凭据）。
  **Not yet enabled**: the dedicated govulncheck CI job (the tool is pinned in go.mod; the job awaits workflow credentials).

## 未完成项 / Open items

- **UAT 运行时验证整体未完成**：目前仅普通 PUT（#86）与 JSON 制品（#89）两条流程有受控验收记录。
  **UAT runtime verification is incomplete overall**: only ordinary PUT (#86) and JSON artifacts (#89) carry recorded bounded acceptance.
- **能力矩阵全部条目仍为待调查/待实现**：没有任何产品兼容项达到"已验证"状态（见[能力矩阵](CAPABILITY-MATRIX.md)）。
  **Every capability-matrix entry remains investigate/implement**: no product-parity item has reached the verified state (see the [capability matrix](CAPABILITY-MATRIX.md)).
- **govulncheck CI 作业**待 workflow 凭据后启用。
  The **govulncheck CI job** awaits workflow credentials.
- 安全/流程包的覆盖提升（PR #100）尚未合并。
  The security/process coverage raise (PR #100) is not yet merged.

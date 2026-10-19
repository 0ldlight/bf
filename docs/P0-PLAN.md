# P0 基线与研发体系 / Foundation

P0 于 2026-10-19 获所有者授权启动。目标是可复验的研发基础，不是可用产品。
The owner authorized P0 on 2026-10-19. Its outcome is a reproducible foundation, not a finished product.

## 已批准方向 / Approved directions

- Go `net/http` + chi，按领域组织模块。 Go HTTP/chi with domain boundaries.
- `database/sql`、领域存储接口，PostgreSQL 为开发默认；其他目标数据库继续调查与验证。 Explicit storage contracts with PostgreSQL for development and all target engines retained in scope.
- React + TypeScript + Vite + shadcn/ui；具体扩展库仍按任务评估。 Approved frontend direction; additional libraries remain scoped decisions.
- Fern 只存放 BinFlow 使用文档，域名 `docs.binflow.org`；工程文档维护在本源码仓库 `docs/`（2026-10-20 范围修订）。 Fern holds usage docs only; engineering docs stay in source `docs/`.

商业双许可、运行许可证商业规则、对象存储产品及完整后台调度细则仍分别审阅。P0 启动不等于这些决定已获批准。
Commercial/source licensing, runtime entitlement rules, object-storage selection and full scheduler details remain separate decisions.

## 任务与依赖 / Tasks and dependencies

任务和审批只在 [Linear binflow 项目](https://linear.app/binfloow/project/binflow-2ad2bc04f63e) 跟踪。本页记录实施顺序和风险，不维护第二份状态清单。
Linear is the task/approval authority; this page records sequence and risks rather than a duplicate task tracker.

| 任务 / Task | 依赖 / Dependencies | 验证出口 / Evidence |
|---|---|---|
| [BIN-118 基线冻结 / Baseline](https://linear.app/binfloow/issue/BIN-118) | 无 / None | 原版版本/授权、完整包摘要、组件清单 / Runtime identity, package digest, components |
| [BIN-119 矩阵 v1 / Matrix](https://linear.app/binfloow/issue/BIN-119) | BIN-118 | 稳定能力 ID、证据和未决状态 / IDs, evidence, explicit unknowns |
| [BIN-120 私有索引 / Research](https://linear.app/binfloow/issue/BIN-120) | BIN-118 | 制品 ID、摘要、版本边界 / Portable provenance |
| [BIN-121 使用文档 / Usage docs](https://linear.app/binfloow/issue/BIN-121) | 无 / None | 导航、双语、链接和预览 / Navigation, languages, links, preview |
| [BIN-122 域名验收 / Domain](https://linear.app/binfloow/issue/BIN-122) | BIN-121 | DNS/TLS/页面与部署来源 / Live domain evidence |
| [BIN-123 Go 健康切片 / Health](https://linear.app/binfloow/issue/BIN-123) | 无 / None | 单元/竞态、构建、进程 smoke / Tests, build, runtime |
| [BIN-124 共享检查与 CI / Checks](https://linear.app/binfloow/issue/BIN-124) | BIN-123 | 本机/PR 同一检查入口、Jenkins 任务 / Shared checks and CI execution |
| [BIN-125 CircleCI/UAT](https://linear.app/binfloow/issue/BIN-125) | BIN-124 | Jenkins 制品输入、部署、失败与恢复证据 / Artifact consumption and recovery |
| [BIN-126 Worker 接续 / Handover](https://linear.app/binfloow/issue/BIN-126) | BIN-123 | 合同、槽位、验证、提交、PR与恢复 / Durable reviewed task |
| [BIN-127 出口审阅 / Exit review](https://linear.app/binfloow/issue/BIN-127) | 上述出口 / Above exits | 所有者确认与显式未完成项 / Owner review and visible gaps |

## 首轮实施与检查点 / First increments

1. 冻结版本与研究来源；Fern 使用文档整理和独立 Go 健康切片可同时推进。
   Freeze baseline/provenance while preparing usage documentation and implementing an independent health slice.
2. 复查摘要、文档检查与 Worker 测试，再接入共享 CI、Jenkins 和 CircleCI/UAT。
   Review package checksums, documentation checks and worker tests before delivery integrations.
3. 所有 PR 由所有者合并。每项验收有证据后才更新 Linear；P0 未通过项不得隐藏或标记完成。
   The owner merges PRs. Update completion only after evidence; retain all incomplete exits.

## 风险与控制 / Risks and controls

- 试用授权于 2026-11-03 到期；优先采集目标版本证据。 Baseline trial expires on 2026-11-03; prioritize reproducible evidence.
- 旧反编译资料声明为 7.161.24，多层版本标记待核实归属；只作线索，不能代替 7.161.26。 Older recovered material is a lead, not target evidence.
- 其他主机已由所有者确认停止；当前 Mac 接管，所有 GLM-5.3 角色/重试最多共用三个槽位。 Owner confirmed host handover; all roles/retries share three slots.
- 失联或租约超时不等于进程结束；恢复前核对实际进程。 A timeout does not establish process termination.
- 原版资料、环境配置与凭据保持私有；公开文档只引用制品标识与摘要。 Preserve private research/credentials and publish only portable references.
- Jenkins 构建制品、CircleCI 仅部署 UAT（所有者 2026-10-19 确认）；UAT runtime、Jenkins Agent 和自定义域名仍需实际验证。 Jenkins builds; CircleCI only deploys UAT. Runtime/agent/domain acceptance requires observed evidence.

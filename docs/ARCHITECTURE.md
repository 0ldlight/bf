# 架构决定 / Architecture decisions

## 审批记录 / Approval record

所有者于 2026-10-19 批准以下三个方向。完整理由和细节保存在[启动提案](STARTUP-PROPOSAL.md)，本页只记录决定状态。
The owner approved three directions; the proposal holds their rationale/details and this page records status.

| 决定 / Decision | 状态 / State | 批准范围 / Scope |
|---|---|---|
| ADR-001 | 已批准方向 / Direction approved | Go net/http + chi，按领域组织 / Domain-oriented Go HTTP |
| ADR-002 | 已批准方向 / Direction approved | database/sql、领域存储接口、PostgreSQL 开发默认；保留其他数据库验证 / Storage contracts and multi-engine scope |
| ADR-003 | 已批准方向 / Direction approved | React + TypeScript + Vite + shadcn/ui |
| ADR-004 | 提案 / Proposed | AGPL 与商业双许可；未发布 LICENSE / Source/commercial licensing |
| ADR-005 | 提案 / Proposed | 自主运行许可证、签发与到期策略 / Runtime entitlements/issuance/expiry |
| 文档平台 / Documentation | 已确认 / Confirmed | 2026-10-20 修订：Fern 仅使用文档；工程资料在源码 `docs/` / Usage docs in Fern; engineering in source |
| P0 UAT | 已批准 / Approved 2026-10-19 | [运行与制品交付方案](UAT-PROPOSAL.md)：独立 CI 身份、非 root systemd、Caddy HTTPS；Jenkins 构建、CircleCI 仅部署 / Scoped identities, runtime and transport |
| 对象存储 / Object storage | 待比较 / Investigation | 维护、资源、S3 兼容与许可 / Maintenance, resources, compatibility, licensing |
| 后台调度 / Scheduler | 方向提案 / Proposed details | 持久认领、租约、失败、恢复；未实现 daemon / Durable execution design |

选择框架不构成兼容性证据；“已批准方向”也不等于该领域已完成。
Framework selection is not compatibility evidence, and approved direction is not implementation completion.

main 合并与正式版本发布继续由所有者决定。所有具体待办、阻塞和审阅在 [Linear](P0-PLAN.md) 中追踪。
The owner retains merge/release decisions; task/review state remains in the linked plan's Linear index.

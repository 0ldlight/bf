# 研发规范 / Engineering conventions

## 文档与语言 / Documentation and language

BinFlow 使用文档维护在 Fern；需求、架构、研发、研究与交接文档维护在本仓库 `docs/`。文档、注释、提交说明和 PR 描述提供中文与英文，代码标识符使用英文。完整解释只维护一处，其他页面链接到其规范来源。
Maintain usage docs in Fern and engineering docs in source `docs/`, with Chinese/English engineering artifacts and English code identifiers. Link to one canonical explanation rather than duplicating it.

[文档贡献流程](DOCUMENTATION.md)规定预览和发布；[架构决定](ARCHITECTURE.md)明确已批准与待审阅范围。
The linked workflows define publication and decision boundaries.

## 代码与验证 / Code and verification

先写可失败的行为契约，再实现最小切片并保存真实 RED/GREEN 证据。测试覆盖成功、错误、权限和边界；不新增跳过、不删断言、不降低阈值以通过检查。
Write a failing behavioral contract before the minimal implementation. Preserve real red/green evidence and never weaken checks to turn them green.

Go 变更通过 `./scripts/check.sh`（或 `make check`）执行格式、模块校验、vet、竞态测试、临时构建和真实进程 smoke。入口在[源码 PR #1](https://github.com/0ldlight/bf/pull/1)审阅，本机和 PR 使用同一脚本；Jenkins 与 UAT 的实际出口继续由 [BIN-124](https://linear.app/binfloow/issue/BIN-124) / [BIN-125](https://linear.app/binfloow/issue/BIN-125) 取证。
Run the shared script for formatting, module verification, vet, race tests, builds and process smoke. Local/PR checks share the same entry; Jenkins/UAT evidence remains separately tracked.

```sh
./scripts/check.sh
# or / 或
make check
```

依赖从官方来源选择，记录精确版本、校验与许可；避免不必要的库、占位领域实现和未经批准的全局配置修改。
Use official dependencies with pinned versions/checksums/licenses; avoid unnecessary abstractions and placeholder domains.

## 审查与交付 / Review and delivery

每任务独立分支/worktree；实现者不是唯一审查者。Codex 审查正确性、边界、安全、可读性与验证证据，所有者合并和发布。
Use isolated branches/worktrees, independent review and owner-controlled merges/releases.

PR 描述说明具体行为、验证与限制，关联 Linear 和能力 ID。制品、构建、测试与部署结果按提交摘要追踪。
PRs link behavior, evidence, limitations, Linear and capability IDs; trace artifacts/tests/deployments to commits.

## 研究与隐私 / Research and privacy

研究输出是行为规范、边界案例和可原创实现的测试契约。安装包、反编译源码、接口原始采样、环境地址与凭据保存在私有资料库。
Research yields contracts and cases. Keep original packages/decompilation/raw captures/private environments and credentials in the private store.

旧资料中的版本字符串、静态代码、运行观察和推论分别标记，不能把命中文本当成运行证据；完整边界在[研究索引](RESEARCH-INDEX.md)。
Distinguish version labels, static evidence, runtime observations and inference; a text match is not observed behavior.

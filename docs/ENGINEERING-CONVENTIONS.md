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

主检出（以及每个任务 worktree）是单写者资源：任务默认在自己的分支与隔离 worktree 中进行；确需直接操作某个检出时，先以 `scripts/checkout-lock.sh with <label> -- <command…>` 取得互斥锁，避免与其他会话竞态写同一工作区。锁按检出生效、随进程退出回收，`status`/`release` 用于检查与显式清理。
Each checkout (the primary checkout and every task worktree) is a single-writer resource: tasks stay on their own branch and isolated worktree by default, and any session that must operate directly inside a checkout first takes the mutual-exclusion lock via `scripts/checkout-lock.sh with <label> -- <command...>`. Locks are per checkout, reclaimed when the holding process exits; `status` and `release` inspect and clear them.

PR 描述说明具体行为、验证与限制，关联 Linear 和能力 ID。制品、构建、测试与部署结果按提交摘要追踪。
PRs link behavior, evidence, limitations, Linear and capability IDs; trace artifacts/tests/deployments to commits.

## 研究与隐私 / Research and privacy

研究输出是行为规范、边界案例和可原创实现的测试契约。安装包、反编译源码、接口原始采样、环境地址与凭据保存在私有资料库。
Research yields contracts and cases. Keep original packages/decompilation/raw captures/private environments and credentials in the private store.

旧资料中的版本字符串、静态代码、运行观察和推论分别标记，不能把命中文本当成运行证据；完整边界在[研究索引](RESEARCH-INDEX.md)。
Distinguish version labels, static evidence, runtime observations and inference; a text match is not observed behavior.

## REA 参与研发 / REA in development

REA 用于核实 Artifactory 兼容研发中依赖发布制品、反编译或原版运行观察的问题。先读取现有源码、行为契约与研究证据；当 API 语义、错误响应、权限边界、元数据、前端交互或平台组件依赖仍有疑点时，主动使用 REA。BinFlow 自有完整源码的一般实现、重构与调试使用常规源码工具。
Use REA for compatibility questions that depend on shipped artifacts, decompilation or reference runtime observations. Read existing source, contracts and evidence first; use REA for unresolved API semantics, errors, permission boundaries, metadata, frontend behavior or platform dependencies. Use ordinary source tools for routine work on BinFlow's complete source.

1. **先确定问题与目标。** 将待核实问题关联能力 ID 和现有 Linear 任务。按[研究索引](RESEARCH-INDEX.md)的制品 ID、版本与摘要定位目标；历史恢复资料只提供线索。目标不明确时先澄清，不能用附近样本代替。
   **Bind the question and target.** Link the question to capability IDs and the existing Linear task. Resolve the artifact by ID, version and digest; historical recovery is a lead. Clarify ambiguous targets rather than choosing a nearby sample.
2. **按证据类型选工具。** 加载 `reverse-engineer-anything` 技能，依据当前服务实际公布的工具与参数选择发布包、JavaScript、原生组件或运行观察入口。Java WAR/JAR 与恢复代码先用适合其格式的现有工具；REA 未支持的能力明确记录限制。
   **Choose tools by evidence type.** Load the REA skill and use the live server's advertised tools and schemas for packages, JavaScript, native components or runtime observations. Inspect Java WAR/JAR and recovered code with suitable existing tools; record unsupported REA capabilities explicitly.
3. **先复用，再定向补证。** 优先读取已有证据摘要与相关模块，围绕具体问题补充调用。记录目标及父制品摘要、工具/版本、证据 ID、函数/文件/请求定位、静态事实、运行观察、推论、未知项与覆盖限制；可复用原始证据导出到私有资料库，不能只保留会话内引用。
   **Reuse and focus evidence.** Start with existing summaries and relevant modules. Record artifact and parent digests, tool/version, evidence IDs, function/file/request locations, static facts, runtime observations, inferences, unknowns and coverage. Export reusable evidence privately instead of relying only on session references.
4. **用契约驱动实现。** 将结论整理为原创行为规范和可失败的测试，覆盖成功、错误、权限与边界；实现最小切片后保存 RED/GREEN 证据。兼容性结论绑定具体版本、样本和验证结果，静态分析本身不能证明运行兼容。
   **Implement through contracts.** Turn findings into original behavior specifications and failing tests for success, errors, permissions and boundaries. Preserve red/green evidence for the minimal implementation. Tie compatibility claims to exact versions, samples and validation; static analysis alone does not prove runtime parity.
5. **保存研究与交接。** 在研究索引追加可公开摘要，在任务交接中记录证据位置、结论、未决问题和下一步；遵循现有私有资料与原创实现边界。运行观察只在任务授权的目标、环境与操作范围内进行。
   **Persist research and handoff.** Add releasable summaries to the research index and retain evidence locations, findings, open questions and next steps in the handoff. Keep the existing private-data and original-implementation boundaries. Scope runtime observations to the targets, environments and actions authorized for the task.
6. **区分连接与能力故障。** 工具可用时直接使用，不在每个任务前重复安装或诊断。工具缺失、调用被阻止或所需引擎不可用时，按技能的连接恢复指南定位原因；独立启动验证不等于当前助手已连接。保留可继续的研究工作，按具体缺口恢复，不自动修改全局配置或安装其他引擎。
   **Separate connection and capability failures.** Use available tools directly without repeated setup or diagnostics. Follow the skill's recovery guide for absent tools, blocked calls or unavailable engines; an independent startup check does not prove the active assistant is connected. Continue supported research and repair the specific gap without automatically changing global configuration or installing other engines.

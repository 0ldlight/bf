# 维护文档 / Maintain the docs

## 维护入口 / Source of truth

Fern 只存放 BinFlow 使用文档：产品使用、经过验证的安装与管理员操作、已支持 API、真实版本发布说明。源码在 [0ldlight/binflow-docs](https://github.com/0ldlight/binflow-docs)，站点为 [docs.binflow.org](https://docs.binflow.org)。
Fern contains only BinFlow usage documentation for verified behavior and released versions.

需求、架构、研发规范、P0 计划、研究索引、CI 实施与交接资料维护在本源码仓库 `docs/`，不在 Fern 中隐藏保存或发布。任务与审批维护在 Linear；私有环境和研究原始资料继续保持私有。
Engineering documents live in source `docs/`, outside Fern. Linear owns tasks/approvals; private evidence remains private.

此范围于 2026-10-20 由所有者明确，替代此前“所有项目文档进入 Fern”的安排。每份正文只维护一处，README 提供链接。
The owner's scope revision supersedes the earlier all-docs-in-Fern arrangement. Maintain one canonical copy per document.

## 变更流程 / Change workflow

1. 为一个可审阅的文档增量创建分支；更新原有规范页面，只有新主题才新增页面。
   Create a focused branch and edit existing canonical content before adding pages.
2. 保留中英双语、稳定 slug；新页面必须加入 docs.yml 导航。
   Preserve both languages and stable slugs; register new pages in navigation.
3. 使用仓库固定的 Fern CLI 验证，修复新增错误与失效链接。原有告警与本次引入的问题分别记录。
   Validate with the pinned CLI and distinguish existing warnings from new defects.
4. 提交 PR，通过配置检查与 Fern 预览，再由所有者审阅合并。
   Use checked/previewed PRs and owner review/merge.

```sh
fern check
fern generate --docs --preview
```

main 更新后，现有 GitHub Actions 发布工作流使用受控 FERN_TOKEN 发布到配置的实例。预览成功、发布成功和域名浏览器验收分别留证。
The existing main workflow publishes through a controlled Secret. Preview, publication and live-domain acceptance require separate evidence.

## 链接与迁移 / Links and migration

内部链接使用由导航及 frontmatter 得到的 URL 路径，不能用本机文件路径。移动或删除已发布页面时，为实际旧 URL 添加有意义的重定向。
Use actual route paths, not filesystem paths; redirect moved/deleted published URLs to meaningful replacements.

私有环境与研究记录不导入公开站点。修改全局主题、认证或发布机制时先声明影响范围与验证方式。
Exclude private records from public publication and state the impact/evidence for global site changes.

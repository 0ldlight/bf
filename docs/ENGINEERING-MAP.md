# 工程地图 / Engineering map

状态 / State: BIN-133 既有代码的导航地图；本页只描述**当前存在**的结构与经 import 证实的依赖方向，不是架构重设计。行为契约一律以各规范文档为准，本页不重复。
A navigation map of the code that exists today. It records the current structure and the dependency direction confirmed by actual imports — it is not an architecture redesign. Behavioral contracts stay in their canonical documents, which this page links instead of duplicating.

## 模块布局 / Module layout

| 模块 / Module | 路径 / Path | 说明 / Role |
|---|---|---|
| `github.com/0ldlight/bf` | 仓库根 / repository root | 全部产品 Go 代码与单元测试 / All product Go code and unit tests |
| `github.com/0ldlight/bf/integration/postgres` | `integration/postgres` | 独立 Go 模块（`replace` 指向 `../..`），对真实 PostgreSQL 运行集成测试；DSN 由环境变量提供，不写入文档 / Separate Go module (`replace ../..`) running integration tests against a real PostgreSQL; DSN comes from environment variables, never from docs |

## 包与入口 / Packages and entry points

| 包 / Package | 角色 / Role | 规范文档 / Canonical doc |
|---|---|---|
| `cmd/binflow` | HTTP 服务入口，组装运行时（当前仅内部 `/healthz`，见 `internal/httpserver`）；运行时接线属于 cmd / HTTP service entry point; runtime wiring belongs to cmd | [PROJECT-BRIEF.md](PROJECT-BRIEF.md) |
| `cmd/binflow-contract` | 契约 CLI：只读引用探针，调用 `internal/compatprobe` / Contract CLI running the read-only reference probe | [GENERIC-DOWNLOAD-CONTRACT.md](GENERIC-DOWNLOAD-CONTRACT.md) |
| `internal/content` | 制品字节流的内容元数据（MD5/SHA-1/SHA-256），无内部依赖 / Content metadata for artifact byte streams; no internal imports | [CONTENT-METADATA.md](CONTENT-METADATA.md) |
| `internal/filestore` | 文件系统适配器：本地文件内容存储的布局与校验原语、临时写入后原子发布（不是纯领域模型）/ Filesystem adapter: layout and validation primitives for the local file content store, staging-then-atomic publish (not a pure domain model) | [FILESTORE-CONTRACT.md](FILESTORE-CONTRACT.md)、[LOCAL-FILESTORE.md](LOCAL-FILESTORE.md) |
| `internal/binary` | 编排缝：先写内容 provider、后登记元数据的二进制写入顺序 / Orchestration seam: provider-before-registration write order | [BINARY-WRITE.md](BINARY-WRITE.md) |
| `internal/registry/postgres` | 适配器：`database/sql` 实现内容元数据登记（驱动由调用方注入），SQL 属于本包 / Adapter: content-metadata registration over `database/sql`, caller-supplied driver; owns its SQL | [POSTGRES-REGISTRY.md](POSTGRES-REGISTRY.md) |
| `internal/repository` | 领域：`Path`/`Key` 路径身份与 `FileReference` 投影；只依赖 `internal/content` / Domain: `Path`/`Key` node path identity and the `FileReference` projection; imports only `internal/content` | [REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md)、[NODE-REFERENCES.md](NODE-REFERENCES.md) |
| `internal/repository/postgres` | 适配器：调用方事务（`*sql.Tx`）上的最小 nodes SQL 投影；不开启/提交事务、不建 DDL / Adapter: minimal transaction-scoped nodes SQL projection on a caller-owned `*sql.Tx` | [NODE-REFERENCES.md](NODE-REFERENCES.md) |
| `internal/httpserver` | 仅内部健康端点 `/healthz`（chi）；不导入任何领域包，不是 Artifactory 兼容面 / Health-only internal endpoint; imports no domain package, not a compatibility surface | [ARCHITECTURE.md](ARCHITECTURE.md) |
| `internal/compatprobe` | 冻结契约的只读黑盒探针，供 `binflow-contract` 使用 / Read-only blackbox probe for the frozen contract used by `binflow-contract` | [GENERIC-DOWNLOAD-CONTRACT.md](GENERIC-DOWNLOAD-CONTRACT.md) |

## 依赖方向（经 import 证实）/ Dependency direction (confirmed by imports)

```
cmd/binflow ──────────► internal/httpserver
cmd/binflow-contract ─► internal/compatprobe

internal/content        （无内部依赖 / no internal imports）
internal/filestore    ─► internal/content
internal/binary       ─► internal/content, internal/filestore
internal/repository   ─► internal/content
internal/registry/postgres     ─► internal/content          （适配器持有 SQL / adapter owns SQL）
internal/repository/postgres   ─► internal/content,
                                   internal/repository       （适配器持有 SQL / adapter owns SQL）
integration/postgres  ─► 上述各包（仅测试，独立模块 / tests only, separate module）
```

规则（本页记录的是当前经 import 证实的状态）/ Rules (current state confirmed by imports at the time of this map):
- 领域包（`content`、`binary`、`repository`）不导入任何 SQL 适配器 / Domain packages (`content`, `binary`, `repository`) import no SQL adapters.
- 适配器包拥有自己的 SQL 并依赖领域：`registry/postgres`、`repository/postgres`；`filestore` 是文件系统适配器，只依赖 `content` / Adapter packages own their SQL and depend on the domain: `registry/postgres`, `repository/postgres`; `filestore` is the filesystem adapter, importing only `content`.
- 运行时接线只出现在 `cmd` / Runtime wiring lives only in `cmd`.
- 当前代码中没有通用 utils 包或占位接口脚手架（据本页撰写时的 import 扫描）；后续设计仍以[架构决定](ARCHITECTURE.md)为准 / Today's import scan shows no generic utils package or placeholder interface scaffolds; future designs still follow the recorded architecture decisions.

## 检查命令 / Check commands

```sh
./scripts/check.sh        # 或 make check：格式、模块校验、vet、race 测试、构建、smoke、UAT
                          # or make check: format, mod verify, vet, race tests, build, smoke, UAT
```

格式门禁通过 `scripts/check-format.py` 用 Git 发现源码（已跟踪 + 未跟踪未忽略），被忽略的本地归档/研究工作树（如 `.binflow/worktrees`）不参与扫描；门禁自身的测试由共享脚本一并运行。
The format gate uses `scripts/check-format.py` to discover sources through Git (tracked plus untracked non-ignored); ignored local archive/research worktrees (for example `.binflow/worktrees`) are never scanned; the gate's own tests run from the shared script.

真实 PostgreSQL 集成门禁（需要一次性测试库，DSN 只经环境变量提供，不记录值）：
The real-PostgreSQL integration gate (disposable test database required; DSN via environment only, never logged):

```sh
BINFLOW_TEST_POSTGRES_DSN='<一次性测试库 DSN>' \
BINFLOW_TEST_POSTGRES_DISPOSABLE=1 ./scripts/check-postgres.sh
```

## 后续工作落点 / Where future work is discussed

- **节点 ID 分配与保存编排 / Node ID allocation and save orchestration**: 未来节点 ID 分配器先依据[节点 ID 调查契约](NODE-ID-CONTRACT.md)；保存事务边界与对应 HTTP 面在 [NODE-REFERENCES.md](NODE-REFERENCES.md)、[REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md) 与 Linear 中讨论定案后再实现；本页与当前代码不预设分配器或 HTTP 设计。
  Future allocator, save-transaction boundary and any HTTP surface are designed in the linked canonical docs and Linear before implementation; nothing here presupposes an allocator or HTTP design.
- **前端 / Frontend**: 按 [ARCHITECTURE.md](ARCHITECTURE.md) ADR-003 已批准方向（React + TypeScript + Vite + shadcn/ui），仅在真实 UI 切片启动时开始；当前无任何脚手架。
  Per approved ADR-003 direction; begins only when an actual UI slice starts. No scaffold exists.
- **文档边界 / Documentation boundary**: 本仓库 `docs/` 只放工程文档；使用文档在 Fern（见 [DOCUMENTATION.md](DOCUMENTATION.md)）。
  Engineering documents only in this repository's `docs/`; usage documentation lives in Fern.

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
| `internal/repository` | 领域：`Path`/`Key` 路径身份、`FileReference` 投影、序列化存储路径检查器 `ValidateStoragePath`、可空原始 UTF-16 操作者纯值 `Actor` 与逐节点文件元数据显式快照 `FileMetadata`；只依赖 `internal/content` / Domain: `Path`/`Key` node path identity, the `FileReference` projection, the `ValidateStoragePath` serialized-storage-path checker, the nullable raw-UTF-16 actor pure value `Actor` and the explicit per-node file-metadata snapshot `FileMetadata`; imports only `internal/content` | [REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md)、[STORAGE-PATH-VALIDATION.md](STORAGE-PATH-VALIDATION.md)、[NODE-REFERENCES.md](NODE-REFERENCES.md)、[ACTOR-VALUE.md](ACTOR-VALUE.md)、[FILE-METADATA.md](FILE-METADATA.md) |
| `internal/repository`（续 / cont'd） | 普通创建核心 `CreateFile`（固定顺序编排，事务归调用方）与私有路径 helper / the ordinary create core `CreateFile` (fixed-order orchestration, caller-owned transaction) with private path helpers | [FILE-CREATE.md](FILE-CREATE.md) |
| `internal/repository`（续 / cont'd） | 普通更新核心 `UpdateFile`（固定顺序编排、非 nil 显式快照、计数原样直通，事务归调用方）/ the ordinary update core `UpdateFile` (fixed-order orchestration, nonnil explicit snapshot, verbatim count passthrough, caller-owned transaction) | [FILE-UPDATE.md](FILE-UPDATE.md) |
| `internal/repository/postgres` | 适配器：`Store` 在调用方事务（`*sql.Tx`）上做最小 nodes SQL 投影（不开启/提交事务、不建 DDL），含七列文件元数据投影与嵌入的受信任 v1→v2 升级/回退 SQL（仅供可信运维/测试显式执行）；同包 `Reservations` 提供显式本地号段预留原语，每次预留开启自有 READ COMMITTED 事务 / Adapter: `Store` is the minimal transaction-scoped nodes SQL projection on a caller-owned `*sql.Tx` (never begins/commits, no DDL), including the seven-column file-metadata projection and the embedded trusted v1→v2 upgrade/downgrade SQL (explicit trusted operator/test execution only); the same package's `Reservations` is the explicit local ID-range reservation primitive, which begins its own READ COMMITTED transaction per reservation | [NODE-REFERENCES.md](NODE-REFERENCES.md)、[FILE-METADATA-PERSISTENCE.md](FILE-METADATA-PERSISTENCE.md)、[ID-RESERVATIONS.md](ID-RESERVATIONS.md) |
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

- **节点 ID 分配与保存编排 / Node ID allocation and save orchestration**: 未来节点 ID 分配器先依据[节点 ID 调查契约](NODE-ID-CONTRACT.md)；持久化号段预留的第一片已作为显式本地原语 `Reservations` 落地（调用方 `*sql.DB` + 自有 READ COMMITTED 事务，见[号段预留记录](ID-RESERVATIONS.md)）；目标 create/update 保存链、元数据映射与 RepoPath 入口变体的调查事实见[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)，其上游调用方与所选 PostgreSQL 参数编码边界的调查事实见[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)（保存链仍仅研究、未实现；其中 actor 表示策略已落地为纯值 `Actor`，见[Actor 值记录](ACTOR-VALUE.md)）。分配器编排、保存事务边界与对应 HTTP 面仍在 [NODE-REFERENCES.md](NODE-REFERENCES.md)、[REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md) 与 Linear 中讨论定案后再实现；本页与当前代码不预设分配器或 HTTP 设计。
  Future allocator, save-transaction boundary and any HTTP surface are designed in the linked canonical docs and Linear before implementation; the first durable-reservation slice already exists as the explicit local primitive `Reservations` (caller `*sql.DB` plus an owned READ COMMITTED transaction, see the [reservation note](ID-RESERVATIONS.md)), and the research-only facts for the target's create/update save chain, metadata mapping and RepoPath entry variants live in the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md), with its upstream caller and selected-PostgreSQL parameter-encoding facts in [save caller and parameter encoding](SAVE-CALLER-ENCODING.md) (the save chain and its adapter remain research only, unimplemented; the actor representation policy from those facts already exists as the pure value `Actor`, see the [Actor value note](ACTOR-VALUE.md)). Nothing here presupposes an allocator or HTTP design.
- **保存分派边界 / Save-dispatch boundaries**: 已冻结的分派、完整快照变化判断与条件性路径重映射见[文件保存分派契约](FILE-SAVE-DISPATCH-CONTRACT.md)；其首个已批准实现切片——原始 `FileInfoSnapshot` 纯值与 `IsIdentical`——**已实现**，契约与验证状态见[文件信息原始快照](FILE-INFO-SNAPSHOT.md)；写侧七字段转换见[快照映射](FILE-SNAPSHOT-MAPPING.md)。完整分派器仍未实现；首个消费者见[快照更新](FILE-SNAPSHOT-UPDATE.md)。
  The [file-save dispatch contract](FILE-SAVE-DISPATCH-CONTRACT.md) stays canonical for dispatch, complete-snapshot dirty checking and conditional path remapping; its first approved implementation slice — the raw `FileInfoSnapshot` pure value with `IsIdentical` — is **implemented** with contract and verification state in the [file-info snapshot note](FILE-INFO-SNAPSHOT.md). See the [snapshot mapping](FILE-SNAPSHOT-MAPPING.md) for the seven-field write projection. The complete dispatcher remains unimplemented; see the first consumer in [snapshot update](FILE-SNAPSHOT-UPDATE.md).
- **前端 / Frontend**: 按 [ARCHITECTURE.md](ARCHITECTURE.md) ADR-003 已批准方向（React + TypeScript + Vite + shadcn/ui），仅在真实 UI 切片启动时开始；当前无任何脚手架。
  Per approved ADR-003 direction; begins only when an actual UI slice starts. No scaffold exists.
- **文档边界 / Documentation boundary**: 本仓库 `docs/` 只放工程文档；使用文档在 Fern（见 [DOCUMENTATION.md](DOCUMENTATION.md)）。
  Engineering documents only in this repository's `docs/`; usage documentation lives in Fern.

- **快照读取来源 / Snapshot read source**：[FileSnapshotSource](FILE-SNAPSHOT-SOURCE.md) 与适配器 LookupSnapshotSource 从同次扫描保留三个 Actual 的空性；旧 Lookup 行为保持不变，加载组装仍另行设计。
  The nullable source retains three Actual validity bits from one scan; loaded-state assembly remains separate.
- [加载态文件身份 / Loaded file identity](LOADED-FILE-IDENTITY.md)：显式 file 路径与独立缓存名字转换；完整快照组装仍另行定案。
  Explicit-file path and independent cached-name conversion; complete snapshot assembly remains separate.

- [即时加载快照 / Immediate loaded snapshot](IMMEDIATE-FILE-SNAPSHOT.md)：从 Source 装齐即时字段，明确 Original 的受限支持域；复制基线和保存接线仍另行定案。
  Source-to-snapshot assembly with an explicit Original profile; copy-baseline and save wiring remain separate.

- [文件 MIME 目录与物化 / File MIME catalog and materialization](FILE-MIME-MATERIALIZATION.md)：真实配置目录与显式 ASCII 查询画像，只转换 MIME 状态，不建立复制基线。
  Concrete configuration catalog and bounded MIME state transition; copied baselines remain separate.

加载状态更新接入 / Loaded-state update consumer: [契约与支持域 / Contract and profile](LOADED-FILE-UPDATE.md)，`internal/repository/file_loaded_update.go`；独立单元与真实数据库接缝分别在 repository 与 integration/postgres 的同名测试。

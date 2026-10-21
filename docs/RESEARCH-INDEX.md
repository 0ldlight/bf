# 研究索引 / Research provenance

此公开索引只保存可公开的身份、摘要、结论和限制。原版安装包、反编译、原始 API 采样与环境记录保存在私有资料库，不从站点提供下载。
This public index contains releasable identity, digests, findings and limitations. Original artifacts, decompilation and raw captures remain private.

## 目标基线 / Target baseline

2026-10-19 原版运行 API 返回 Artifactory **7.161.26**、revision **86126900**；授权返回 Enterprise Plus Trial。该观察仅证明样本身份与当时授权，不能证明所有功能已测试。
The observed runtime reports the target version/revision and trial entitlement. Identity is not feature-validation evidence.

| 字段 / Field | 值 / Value |
|---|---|
| Portable artifact ID | `baseline/artifactory-pro/7.161.26/linux-amd64/package` |
| Source | [JFrog official distribution](https://releases.jfrog.io/artifactory/artifactory-pro/org/artifactory/pro/jfrog-artifactory-pro/7.161.26/jfrog-artifactory-pro-7.161.26-linux.tar.gz) |
| Bytes | `2559888096` |
| SHA-256 | `b967a1fc263ec8cf0314194fc24daf99e10a130ca8e19b076c8a18faf02bc4d9` |
| Inventory | `57506` TAR members; logical member bytes `4143856880` |
| Product properties | version `7.161.26`; revision `86126900`; build `7.161.26-20260907150505-1` |

摘要由完整文件重新计算，TAR 目录逐成员扫描；没有执行包内程序。来源为官方地址，但仅本机摘要不构成独立签名验证。
The digest was computed over the full file and the TAR scanned member by member without executing its programs. A locally computed digest alone is not independent signature verification.

## 组件证据 / Component evidence

以下组件 ID 继承目标包 ID，私有 manifest 保存包内路径、大小、摘要和工具输出；跨主机通过 ID/摘要定位，不能仅复制绝对路径。
Component IDs inherit the package identity. Private manifests retain member paths, sizes, digests and tool output for portable resolution.

| Component ID suffix | 观察 / Observation | 限制 / Limit |
|---|---|---|
| `artifactory-war` | SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37`; 824 ZIP members | 清单/构建元数据，非执行行为 / inventory only |
| `access-war` | SHA-256 `dc871abf684789cf54977ed88ded2524493390c5b35ffa05b9893c391d692ca5`; 498 ZIP members | 清单/构建元数据，非执行行为 / inventory only |
| `router-go` | SHA-256 `0808170da829e6c4bc658cb5a6e0a9daf792ddaa3c630b9baf7b2336bf1f1b90`; ELF amd64, embedded `go1.26.7` | 嵌入字符串不证明工具链或依赖真实性 / embedded metadata only |
| `metadata-go` | ELF amd64, embedded `go1.26.6`, module version `(devel)` | 未恢复源码或函数行为 / no recovered source/behavior |

REA Router 证据 ID：`ev_8d5f8a6a49ade8c62588c355c2f0057a4b5942e347c0b0cab4bc9f75494ac836`。
REA Metadata 证据 ID：`ev_998440c643db3eb012cf6df0effd5eb5b28e8855ef23de2ec59cc5369297ee06`。

WAR manifest 的 `Build-Jdk-Spec` 均为 `25`。抽样 Artifactory 第一方 JAR 标记为 `7.161.15`，Access JAR 标记为 `7.191.16`。这些是组件版本，不能直接当作产品版本或据此断言包损坏。
WAR manifests report JDK 25. Sampled Artifactory and Access JARs use their own versions; component/product version layers must be kept distinct.

包内还存在 frontend、event、topology、jfconfig、jfconnect、evidence 等目录。存在某组件不等于 BinFlow 必须逐服务重建；按职责、调用与目标授权调查必需能力，映射到[能力矩阵 PLAT-001](CAPABILITY-MATRIX.md)。
Bundled presence does not establish implementation topology. Establish required behavior and dependencies before choosing service boundaries.

## 历史恢复资料 / Historical recovery

历史目录的根 README 声明 `7.161.24`，恢复 POM 为 `7.161.24-recovered`，另有内部标签 `7.161.14`。仅对根元数据文件计算了摘要，未宣称已校验整个历史树。
Historical root metadata reports these labels. Only selected root metadata files were hashed, not the full recovered tree.

这些资料是线索：缺少 `sourcesContent` 的前端 sourcemap 近似重建不等于可构建 TypeScript；Ghidra C 伪代码不等于恢复 Go 源码。多层版本标记需要归属核实，不能替代目标包和运行证据。
Approximate sourcemap reconstruction is not buildable TypeScript, and Ghidra pseudocode is not recovered Go source. Establish version-layer provenance and validate against the target.

## 本地内容存储 / Local filestore

2026-10-20 按所有者要求核对目标包字节码及专用实例，确认本地 provider 的 SHA-1 物理寻址与多摘要模型；见[存储对齐记录](FILESTORE-CONTRACT.md)。记录列出暂存/移动/数据库边界及证据限制，未宣布内容存储实现、并发写、GC 或 HA 兼容完成。
The source-grounded filestore note distinguishes static findings, bounded runtime evidence and open verification.

## PostgreSQL 元数据登记 / PostgreSQL metadata registry

2026-10-21 按目标 `binary-store-core` 4.372.5 字节码静态证据实现元数据登记分支（查找/插入冲突返回输入/SHA-256 回填），并以真实 PostgreSQL 17.10 运行验证；规范来源与证据边界见[PostgreSQL 登记记录](POSTGRES-REGISTRY.md)。`binary-store-api` 4.372.5（SHA-256 `3606d807…`）的 `javap -v` 进一步显示 `TransactionHandler.invokeInNewTransaction` 的 `@Transactional(REQUIRES_NEW)` 声明在接口上且未指定隔离级别：它只佐证独立新事务，目标实际运行时隔离级别未经验证，本地 READ COMMITTED 是显式适配器策略。目标原库的数据库事务/失败路径未实测，静态证据不等价于目标运行时行为。
The metadata-registration branches follow static bytecode evidence of the target's binary-store-core 4.372.5 and were verified against a real PostgreSQL 17.10 instance; the [PostgreSQL registry note](POSTGRES-REGISTRY.md) is the canonical source. `javap -v` of `binary-store-api` 4.372.5 (SHA-256 `3606d807…`) additionally shows `TransactionHandler.invokeInNewTransaction` declaring `@Transactional(REQUIRES_NEW)` on the interface without pinning an isolation level: it corroborates only an independent new transaction, the target's effective runtime isolation is unverified, and local READ COMMITTED is explicit adapter policy. The target's own database-transaction/failure paths remain untested at runtime; static evidence is not target runtime behavior.

## 仓库节点路径 / Repository node path

2026-10-21 按目标 `NodePath`/`NodeUtils` 静态字节码与 23 例 JVM 回放（私有证据）实现内部路径身份值模型；23 例回放仅在本地 OpenJDK 26.0.2.1 上运行目标 WAR 内 storage-db 组件的精确路径类对象，非运行中 Artifactory HTTP/数据库采样。规范来源与证据边界见[节点路径记录](REPOSITORY-NODE-PATH.md)。仅路径身份，不宣称仓库保存、HTTP、SQL 或目标畸形输入运行时对等。
The internal path-identity value model follows static bytecode of the target's `NodePath`/`NodeUtils` plus a 23-case private JVM replay that ran only exact target path class objects from the storage-db component inside the target WAR under a local OpenJDK 26.0.2.1 — not live Artifactory HTTP/DB sampling. The [node-path note](REPOSITORY-NODE-PATH.md) is the canonical source. Path identity only: no repository-save, HTTP, SQL or target malformed-input runtime parity is claimed.

## 事务内文件引用 / Transaction-scoped file references

2026-10-21 按目标 `NodesDao`/`BaseDao` 静态字节码与打包全新 `postgresql.sql`（SHA-256 `667a150c…dbf133cf` 行 23..53，仅全新模式，安装库未查询）实现文件引用的最小 nodes 投影：调用方拥有事务与节点 ID，成功计数在 Commit 前是临时的，字面空↔点 SQL 边界与 `sha1_actual → binaries` 外键。规范来源、本地策略与边界见[文件引用记录](NODE-REFERENCES.md)。不宣称完整目标 Node/HTTP/schema 导入、PathValidator/认证替代或目标数据库故障运行时对等。
The minimal file-reference nodes projection follows static bytecode of the target's `NodesDao`/`BaseDao` plus the packaged fresh `postgresql.sql` (SHA-256 `667a150c…dbf133cf`, lines 23..53 — fresh schema only, the installed DB was never queried): caller-owned transactions and node IDs, provisional successful counts until commit, the literal empty↔dot SQL boundary and the `sha1_actual → binaries` FK. The [node-references note](NODE-REFERENCES.md) is the canonical source for local policies and boundaries. No full target Node/HTTP/schema import, PathValidator/auth replacement, or target DB-fault runtime parity is claimed.

## 节点 ID 分配 / Node ID allocation

2026-10-22 校验同一目标 WAR 的 storage-db、addon-ha、storage-common 和 config 组件，核实 general 号段、Simple/HA 分支及事件序列区别；13 项受控 JVM 回放直接执行目标分配器 class，周边配置/异常辅助类型/JDBC 使用显式替身，含 8 实例分配 40 个唯一 ID。规范来源与限制见[节点 ID 契约](NODE-ID-CONTRACT.md)。未实现分配器，未执行原版真实数据库/Spring/HA 故障验证。
The same target WAR supplies the general-range, Simple/HA and separate event-sequence evidence. Thirteen controlled JVM observations execute exact allocator class bytes with explicit surrounding doubles, including forty unique IDs from eight instances. The [node-ID contract](NODE-ID-CONTRACT.md) owns the scope and limits. No allocator implementation or live target DB/Spring/HA failure validation is claimed.

2026-10-22 上述调查的第一实现切片落地：显式本地 PostgreSQL 已提交号段预留原语 `Reservations`（调用方 `*sql.DB`、自有 READ COMMITTED 事务的条件更新、仅 Commit 成功返回闭区间号段）。契约与验证边界见[号段预留记录](ID-RESERVATIONS.md)；这是原创本地政策，不宣称目标分配器/autocommit/HA 运行时对等。
The first implementation slice of that investigation landed: the explicit local PostgreSQL committed ID-range reservation primitive `Reservations` (caller-owned `*sql.DB`, a conditional update inside an owned READ COMMITTED transaction, the inclusive range returned only after a successful Commit). The [reservation note](ID-RESERVATIONS.md) owns the contract and verification limits; this is original local policy with no target allocator/autocommit/HA runtime parity claimed.

## 证据规则 / Evidence rules

1. 私有 manifest 记录来源、获取时间、父制品、工具/版本、摘要、证据 ID 和限制；原始输出保持私有。
   Preserve provenance/tool identity/digests and limitations privately.
2. 分开标记静态事实、实际运行观察与推论；任何兼容结论都关联具体样本和测试。
   Separate static facts, runtime observations and inference.
3. 调查输出形成原创行为契约与边界测试；公开仓库不复制受控实现或原始采样。
   Produce original behavioral contracts and tests without publishing controlled implementations/captures.

[P0 计划](P0-PLAN.md)规定研究出口；后续完整依赖清单、前端/其他平台组件及行为采样仍需追加证据，当前索引不宣称逆向或兼容完成。
Further dependency/frontend/platform analysis and behavioral sampling remain open; this inventory is not completed reverse engineering or parity.

- [本地 provider / Local provider](LOCAL-FILESTORE.md)：内部 Put/Open 及故障/并发/路径验证；HTTP、数据库引用、GC 与 HA 仍待接入。

# 文件保存分派契约 / File-save dispatch contract

状态：目标静态调查与本地接缝盘点；首个已批准实现切片——原始 `FileInfoSnapshot` 纯值与 `IsIdentical`（见[文件信息原始快照](FILE-INFO-SNAPSHOT.md)）——已实现；首个[快照更新门](FILE-SNAPSHOT-UPDATE.md)已有显式调用方接缝，完整保存分派器仍未实现。本页不批准新的锁或会话设计。
State: target static research and local seam inventory. The first approved implementation slice — the raw `FileInfoSnapshot` pure value with `IsIdentical` (see the [file-info snapshot note](FILE-INFO-SNAPSHOT.md)) — is implemented; the first [snapshot update gate](FILE-SNAPSHOT-UPDATE.md) supplies an explicit caller seam, while the complete dispatcher remains unimplemented. This page approves no new lock or session design.

## 来源 / Provenance

目标为 Artifactory 7.161.26 rev 86126900，同一 WAR SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37`。BIN-160/161/162 复用冻结目标类，并定向检查比较链的两个直接依赖。原始类、反编译与逐指令锚点保留在私有资料，公开来源见[研究索引](RESEARCH-INDEX.md)。本轮没有运行目标保存会话、Spring、DAO、数据库或 HTTP；静态调用顺序不证明生效事务配置。
The target is Artifactory 7.161.26 rev 86126900 from the same pinned WAR. BIN-160/161/162 reuse frozen classes and inspect two direct comparison dependencies. Raw classes, disassembly and instruction anchors remain private. No target save session, Spring, DAO, database or HTTP was executed in this round; static call order does not establish effective transaction configuration.

## 分派与生命周期 / Dispatch and lifecycle

目标 provider 可以直接返回已有写锁条目；其他路线包含祖先处理、写锁和可变条目查找。普通文件路线区分缺失与命中，目录命中不能当成缺失再创建。已有文件的包装器 ID 和快照来自加载行，新文件使用请求路径。条件创建另有分支，不能合并为普通创建或补出自动 upsert。
The provider can return an existing write-locked entry; other routes include ancestor handling, locking and mutable-item lookup. The ordinary file route distinguishes missing items from hits; a folder hit must not become a missing-file create. Existing files use a loaded row ID and snapshot; new files use the request path. Conditional creation is separate, not an implicit upsert.

可变条目保存包含变化判断、节点写入和独立的属性/关注行为。会话在提交前触发保存，新文件链也存在提前 flush，不能概括为“所有 SQL 仅在 beforeCommit 执行”。
Mutable-item saving includes dirty checks, node writes and separate property/watch behavior. Sessions save before commit, while the new-file route can flush earlier; SQL does not universally wait for beforeCommit.

本地 [CreateFile](FILE-CREATE.md)、[UpdateFile](FILE-UPDATE.md) 与 [Store](NODE-REFERENCES.md) 立即调用 SQL 委托，提交/回滚由调用方负责。调用方拥有事务结局，不等于实现了目标延迟执行及其错误发生时点。锁、缓存与拦截器顺序尚未表示；唯一索引冲突也不能代替锁或保证并发更新顺序。
Local CreateFile, UpdateFile and Store invoke SQL delegates immediately while callers own commit/rollback. Ownership of the transaction outcome does not implement target deferred execution or error timing. Locks, caches and interceptor order remain unrepresented; uniqueness conflicts do not replace locks or establish concurrent-update order.

## 变化判断 / Dirty checking

文件链调用 FileInfoImpl.isIdentical，与普通 equals 不同：比较 lastModified、条件参与的 created、RepoPath、名字、lastUpdated、原始 createdBy/modifiedBy、校验和条目以及 size/mimeType。校验和按类型匹配并比较 actual 与原始 original；null、空串和 NO_ORIG 字面值不能任意合并。ID 不参与。
The file chain calls FileInfoImpl.isIdentical, distinct from ordinary equals. It compares lastModified, conditionally created, RepoPath, name, lastUpdated, raw createdBy/modifiedBy, checksum entries, size and mimeType. Checksums match by type and compare actual and raw original values. Null, empty strings and the literal NO_ORIG are distinct. ID is excluded.

created 的配置开关注册默认 false，即默认参与比较，部署生效值未观察。文件构造器的 long 参数是节点 ID；created 与 lastModified 共用一个时钟样本，lastUpdated 独立读取。三个字段并非三次独立取样，新样本也不保证毫秒改变。复制快照可能解析并缓存 MIME，其生效配置与完整运行结果未验证。
The created flag is registered as false, including created in comparison by default; its effective deployment value is unobserved. The constructor's long argument is a node ID. Created and lastModified share one clock sample; lastUpdated reads independently. Three fields do not mean three independent samples, and a fresh sample need not change the millisecond. Snapshot copying can resolve and cache MIME; effective configuration and full runtime results remain unverified.

[FileMetadata](FILE-METADATA.md) 表示映射后的节点值，Actor 已经过持久化边界的缩写。它缺少完整原始 FileInfo、校验和图空性和 MIME，不能直接承担原版完整变化判断；纯值获批也不等于批准删除上层判断。节点未更新时，目标属性/关注操作仍可能执行。
FileMetadata represents mapped node values whose actors are already abbreviated at the persistence boundary. It lacks the complete raw FileInfo, checksum-map presence and MIME, so it cannot directly implement the complete target dirty check. Pure-value approval does not authorize deleting that gate. Property/watch work may still execute when the node update is skipped.

已批准的原始快照表示与比较器契约（含显式 `ignoreCreated` 布尔）见[文件信息原始快照](FILE-INFO-SNAPSHOT.md)；该切片已实现，仅纯值，不接 SQL、属性/关注或分派。
The approved raw-snapshot representation and comparator contract (with the explicit `ignoreCreated` boolean) live in the [file-info snapshot note](FILE-INFO-SNAPSHOT.md); that slice is implemented, pure value only, wired to no SQL, properties/watches or dispatch.

## 路径重新映射 / Path remapping

目标加载节点路径时重建 RepoPath，写入映射再经 NodePath.fromRepoPath 读取 RepoPath.getName；后者会剥除一个受守卫控制的元数据后缀。因此，若加载行名为 `f:properties` 且确实进入该写入映射，重建出的节点名可变为 `f`。这是静态组合结论，不能推成每次保存必执行 SQL 或必改变名字：上游变化判断可能跳过节点更新。
Target loading rebuilds a RepoPath, and persistence mapping reads RepoPath.getName through NodePath.fromRepoPath. That getter strips one guarded metadata suffix. Thus, when a loaded row named `f:properties` actually reaches this write mapping, its rebuilt node name can become `f`. This is a static composition result, not proof that every save executes SQL or changes the name: the upstream dirty check can skip the node update.

本地 Lookup 返回已构造 Path；PathName 拼回字符串后交给 UpdateFile，也会再次经过同类剥除。这个例子不能靠“已映射路径应永久原样保留”的假设修正，也不能据此宣称两端完整往返等价；须区分加载、变化判断与持久化映射，分别验证。额外规范化、存储路径画像和调用路线仍影响结果。
Local Lookup returns a constructed Path. Passing its PathName string to UpdateFile also applies this stripping again. An assumption that a mapped path must remain unchanged would not resolve this case. Nor does it establish complete round-trip parity: loading, dirty checking and persistence mapping need separate verification, with constructor normalization, local path policy and caller routing still affecting outcomes.

## 缺失元数据与后续出口 / Missing metadata and next gates

本地 Lookup 将七列全 NULL 的旧记录返回为 Metadata=nil；UpdateFile 要求显式非 nil 快照；Store.Update 的 nil 则表示保留原七列。这些接口用途不同，分派器不能默默填零、造时间或绕过更新契约。目标创建把三个 primitive 时间映射为明确参数，因此本地七列全 NULL 创建只能称为本地历史兼容策略。
Local Lookup returns Metadata=nil for legacy rows with all seven columns NULL. UpdateFile requires an explicit nonnil snapshot; Store.Update with nil preserves the old columns. These interfaces serve different purposes: a dispatcher must not silently fill zeros, invent timestamps or bypass the update contract. Target creation maps three primitive timestamps to explicit parameters, so the local all-NULL create shape is a local legacy policy.

下一实现提案须说明比较状态的完整表示、加载/请求路径映射、旧记录策略及尚未表示的锁/会话范围。消费者回归只新增接缝场景，复用已有适配器、编码、号段和真实 PostgreSQL 回归；大型重复语料不能替代边界设计。权限、HTTP 与正式发布仍按独立范围推进。
The next implementation proposal must define complete comparison state, loaded/request path mapping, legacy-row policy and unrepresented lock/session scope. Consumer regressions should add new seam scenarios and reuse existing adapter, codec, reservation and real-PostgreSQL tests. Repeated large corpora cannot replace boundary design. Authorization, HTTP and release remain separate scopes.

七字段写侧转换现由[NodeMetadata 纯投影](FILE-SNAPSHOT-MAPPING.md)提供。投影成功不验证完整保存接受条件，不重建旧行，也不提供节点身份或内容 actual 参数；分派器仍须明确这些边界。
The [NodeMetadata pure projection](FILE-SNAPSHOT-MAPPING.md) supplies seven write-side fields. Success validates no full-save acceptance, reconstructs no legacy row and supplies neither identity nor content actuals. Dispatch still needs explicit contracts for those boundaries.

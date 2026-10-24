# 文件信息原始快照 / File-info raw snapshot

状态 / State: 已实现所有者批准的原始快照纯值与受限比较器，等待 PR 审阅及合并。完整保存分派器与变化门接线仍未实现。
State: the approved raw-snapshot pure value and restricted comparator are implemented, pending PR review and merge. Full save dispatch and dirty-gate wiring remain unimplemented.

## 范围与来源 / Scope and provenance

- 冻结审批接口来自 BIN-163 owner-proposal（SHA-256 `91e06b06…7c53`）；比较器语义转引自[保存分派契约](FILE-SAVE-DISPATCH-CONTRACT.md)，表示事实见[文件元数据值](FILE-METADATA.md)、[Actor 值](ACTOR-VALUE.md)与[仓库节点路径身份](REPOSITORY-NODE-PATH.md)。本页是公开双语摘要，不复制私有原始证据。
  The frozen API comes from the BIN-163 owner proposal (SHA-256 `91e06b06…7c53`); comparator semantics are transcribed from the dispatch contract with representation facts in the linked notes. This page is a releasable bilingual summary and copies no private raw evidence.
- 参考侧证据（BIN-165，根已批准）：19 个真实 JAR 与钉定 WAR（SHA-256 `9c2c1883…`）条目逐字节核对、20 个类摘要核对、144 行真实谓词观察（created 两种画像各 72 行）、参考 JSON SHA-256 `d9adb8e5…`。单独运行主 ConstantValues 读取抛 home-not-bound；真实 ItemInfoUtils 捕获 Exception 并回退系统属性。**部署生效配置未观察**，不据此断言部署环境行为。
  Reference side (BIN-165, root-approved): 19 real JARs byte-matched against pinned-WAR entries, 20 class digest checks, 144 real predicate rows (72 per created profile) and reference JSON SHA-256 `d9adb8e5…`. The standalone primary ConstantValues lookup throws home-not-bound; the real ItemInfoUtils catches Exception and falls back to a system property. **Effective deployment configuration is unobserved** and no deployed behavior is inferred.
- 本切片只交付 `internal/repository` 内的不可变纯值与其比较：无 SQL/schema、持久化转换、HTTP/权限、时钟/MIME/配置提供者、保存分派或变化门接线，也不把既有 Lookup/CreateFile/UpdateFile 串成保存流程。
  This slice delivers only an immutable pure value and its comparison in `internal/repository`: no SQL/schema, persistence conversion, HTTP/auth, clock/MIME/config providers, save dispatch or dirty gate, and no assembly of existing seams into a save lifecycle.

## 已批准 API / Approved API

```go
type ChecksumKind uint8
const (
    ChecksumSHA1 ChecksumKind = iota + 1
    ChecksumMD5
    ChecksumSHA256
)

type ChecksumEntryInput struct {
    Kind ChecksumKind
    Actual, Original []uint16
}
type FileInfoSnapshotInput struct {
    RepoKeyUTF16, RepoPathUTF16, NameUTF16 []uint16
    CreatedMillis, LastModifiedMillis, LastUpdatedMillis int64
    CreatedByUTF16, ModifiedByUTF16 []uint16
    SizeBytes int64
    MimeTypeUTF16 []uint16
    Checksums []ChecksumEntryInput
}
type FileInfoSnapshot struct { /* private immutable state */ }

func NewFileInfoSnapshot(FileInfoSnapshotInput) (FileInfoSnapshot, error)
func (s FileInfoSnapshot) Fields() FileInfoSnapshotInput
func (s FileInfoSnapshot) IsIdentical(
    other FileInfoSnapshot, ignoreCreated bool,
) (bool, error)
```

- 不新增额外 API：无 `HasChecksums`、策略枚举、setter、null 容器标志、NPE 模拟或运行时类标签；与既有纯值一致，不提供自定义 `String`/JSON/SQL 转换。
  No extra API: no HasChecksums, policy enum, setters, null-container flag, NPE simulation or runtime-class tag; consistent with the existing pure values, no custom String/JSON/SQL conversion.

## 字段语义 / Field semantics

- repoKey/path 保存已捕获 `RepoPathImpl` 的字段值；缓存 name 独立保存，永不从路径重算。捕获不 trim、不 clean、不剥元数据后缀、不强制 file、不转成既有 `Path`。
  RepoKey/path hold captured RepoPathImpl field values; the cached name is stored independently, never recomputed from the path. Capture performs no trimming, cleaning, suffix stripping, file coercion or conversion to the existing Path.
- 三个时间与 size 逐字保留完整 signed int64，无时钟读取、排序或回退。actor/MIME/actual/original 为原始 UTF-16：nil=Java null，非 nil 空=空串，NO_ORIG 原样，SHA-256 original 保留；不缩写、不小写化、不替换标记、不重编码。
  The three times and size retain full signed int64 verbatim with no clock read, ordering or fallback. Actors, MIME, actual and original are raw UTF-16: nil is Java null, nonnil empty is the empty string, NO_ORIG stays verbatim and the SHA-256 original is kept; no abbreviation, lowercasing, marker substitution or re-encoding.
- `Checksums` 的 nil 与空切片都表示**正常非 null 容器中的空图**，不是 Java null 容器；条目缺失与存在但 actual=null 不同；按类型键匹配、输入次序无关、每已知类型至多一条；枚举全集冻结为 sha1/md5/sha256。
  Nil and empty input slices both denote an empty map inside a valid nonnull container, never a Java null container. A missing entry differs from a present null actual; matching is keyed by kind, order is irrelevant and each known kind appears at most once; the frozen universe is sha1/md5/sha256.
- `ignoreCreated` 必须显式传入：true 跳过 created，false 比较 created；不读配置、环境或时钟，注册默认值不视为部署生效值。ID 与 folder 标志不参与比较；比较结果本身不直接控制 SQL、属性/关注、锁或会话行为。
  `ignoreCreated` is supplied explicitly — true skips created, false compares it — with no configuration, environment or clock reads, and the registered default is not a deployed value. ID and folder flags are excluded; the result does not itself control SQL, properties/watches, locks or sessions.

## 原始捕获 vs 既有映射值 / Raw capture vs existing mapped values

- 不复用 [Actor 值](ACTOR-VALUE.md)：它在超过 64 码元时缩写为首 61 码元加三点，会丢弃比较器实际读取的完整原始 actor 操作数。
  Actor is not reused: it abbreviates beyond 64 units to 61 units plus dots, losing the full raw actor operands the comparator reads.
- [FileMetadata](FILE-METADATA.md) 表示 `fileInfoToNode` 映射**之后**的节点值，缺少按类型的条目存在性及可空 actual/original、MIME、独立 name/repoKey/path 与 SHA-256 original，不能承担完整变化判断。
  FileMetadata represents post-`fileInfoToNode`-mapping node values. It lacks keyed entry presence and nullable actual/original values, MIME, independent name/repoKey/path and the SHA-256 original needed for the complete dirty check.
- 本地 `NewPath` 保持 repo 原样；parent 先做 Java trim 再去除边界 `/`，name 仅做 Java trim。`NewPath` 本身不剥元数据后缀；`NodePath.fromRepoPath` 等读取 `RepoPath.getName` 的边界会取得该 getter 已按守卫规则处理的名字。快照保存已捕获的 RepoPathImpl 字段与独立缓存 name，不重新执行这些映射。
  Local `NewPath` preserves repo verbatim; parent receives Java trim followed by boundary-`/` removal, while name receives Java trim only. `NewPath` itself strips no metadata suffix. Boundaries such as `NodePath.fromRepoPath` that read `RepoPath.getName` receive the name after that getter's guarded suffix handling. The snapshot stores captured RepoPathImpl fields and the independent cached name without rerunning those mappings.
- 类型不蕴含规范状态：`FileReference.Content` 的 `content.Metadata` 是普通结构体（三个十六进制串与 size），**不是经校验的规范状态**，其类型不强制小写十六进制或合法值；Actor/FileMetadata/`Path` 的构造归一化只发生在各自边界。快照因此显式捕获原始状态，不依赖"映射类型必为规范化且非空"的假设，捕获期零归一化。
  Types imply no canonical state: `content.Metadata` in `FileReference.Content` is a plain struct (three hex strings and size), **not a validated canonical state**, and its type enforces no lowercase-hex/canonical validity; Actor/FileMetadata/`Path` normalization happens only at their own boundaries. The snapshot captures raw state explicitly, assumes no canonical/nonnull mapped types and applies no normalization at capture.

## 受限画像、零值与错误 / Profile, zero value and errors

- 快照是**原始捕获、结构完整的具体文件状态**：画像仅覆盖 FileInfoImpl、RepoPathImpl、FileAdditionalInfo 的结构完整状态——非 null repoKey/path/name、非 null 校验和容器与内部图、非 null 条目且键等于条目类型。捕获不承诺任何构造器可达性，也不模拟目标构造器归一化或复制生命周期；null 容器或未知类必须在进入画像前拒绝，不得先转成空图。
  The snapshot is a raw captured, structurally complete concrete file state: the profile covers structurally complete states of the named classes — nonnull repoKey/path/name, nonnull checksum container and inner map, nonnull entries keyed by their own kind. Capture promises no constructor reachability and emulates no target constructor normalization or copy lifecycle; null containers or unknown classes are rejected before capture rather than normalized to empty.
- 构造拒绝 nil repoKey/path/name 与未知或重复 Kind，返回固定文案的 `ErrInvalidFileInfoSnapshot`，错误不回显原始内容。
  Construction rejects nil repoKey/path/name and unknown or duplicate kinds, returning `ErrInvalidFileInfoSnapshot` with fixed messages that never echo raw content.
- 零值快照无效：`Fields` 对有效快照返回完整防御性副本，对零值返回零输入且不宣称有效；`IsIdentical` 任一侧为无效零值即返回该错误。这是显式本地 Go API 策略，不声称复刻 Java NPE；Java null/NPE 图不在本布尔画像内。
  The zero snapshot is invalid: `Fields` returns a full defensive copy for valid snapshots and a zero input for a zero snapshot without claiming validity; `IsIdentical` returns that error when either side is an invalid zero. This is explicit local Go policy, not Java NPE parity; Java null/NPE graphs are outside this boolean profile.

## 所有权与复杂度 / Ownership and complexity

- 构造与 `Fields` 均深复制嵌套切片：复制开销为所捕获码元数的线性时间（O(n)）；值副本可共享私有只读存储，无 setter；Checksums 返回次序不作约定。修改构造输入、输入结构体或任何返回切片都不能改变既有快照；并发读者可各自修改自己的副本。
  Construction and `Fields` deep-copy nested slices with linear cost in captured units (O(n)); value copies may share private read-only storage and there are no setters; Checksums iteration order is unspecified. Mutating the construction input, the input struct or any returned slice never changes an existing snapshot; concurrent readers may mutate their own copies.
- 全部操作纯内存：无 IO、无 DB、无网络、无时钟/配置/环境读取；比较只读已捕获操作数，开销以操作数大小为界。
  All operations are pure in memory: no IO, database, network, clock, configuration or environment reads; comparison only reads captured operands, bounded by their size.

## 边界 / Boundaries

- 不跨边界扩展 FileMetadata；不新增 SQL/schema、分派接线、HTTP/权限、时钟/MIME/配置提供者或发布；合并与后续接线另经所有者审批。
  FileMetadata is not extended across boundaries; no SQL/schema, dispatch wiring, HTTP/auth, clock/MIME/config providers or releases are added. Merge and later wiring require owner approval.

## 验证 / Verification

- 固定目标的两个 created 配置画像各 72 行真实比较参考；独立 Go 对照共 144 行、864 项断言，检查构造、字段精确读回及双向比较，无失配。原始目标字节与语料保持私有。
  Two observed created profiles supply 72 real reference rows each. Independent Go comparison covers 144 rows and 864 assertions across construction, exact field readback and symmetric comparison, with no mismatches. Target bytes and raw fixtures remain private.
- 本地回归覆盖原始 UTF-16、nullable、三个校验和类型、独立名字、完整 int64、有效空状态与无效零值；输入/输出的嵌套及重叠切片复制和并发读取经过 race 检查。
  Local regressions cover raw UTF-16, nullable values, all checksum kinds, independent names, full int64, valid empty state and invalid zero values; race checks cover nested/overlapping copy ownership and concurrent reads.
- 可编译故障验证覆盖 actor 缩写、遗漏 SHA-256/MD5 original、误用 created 开关、误拒绝有效空状态、夹断 size 与重复输出条目；回归以断言失败捕获这些故障。
  Compiling fault checks cover actor abbreviation, omitted SHA-256/MD5 originals, misused created policy, rejected valid empty state, clamped size and duplicated output entries; regressions catch them through assertion failures.
- `./scripts/check.sh` 通过格式、模块校验、vet、全仓 race、构建与真实进程 smoke。每个 PR 仍须通过 Check 与真实 PostgreSQL CI；本片没有新增 SQL/schema 或本地数据库探针。
  The shared script passes formatting, module verification, vet, repository-wide race tests, builds and real-process smoke. Each PR must also pass Check and real-PostgreSQL CI. This slice adds no SQL/schema or local database probe.

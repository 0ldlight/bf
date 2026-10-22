# 文件元数据值 / File metadata value

状态 / State: BIN-140 实现切片（2026-10-22 所有者批准"批准方案并实施 FileMetadata 纯值"）。本页是 `internal/repository` 中逐节点文件元数据显式快照 `FileMetadata` 的唯一规范说明；表示事实源自[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)与[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)（actor 表示复用[Actor 值](ACTOR-VALUE.md)），此处不重复证据。
State: BIN-140 implementation slice (owner approval on 2026-10-22). This page is the single canonical note for the explicit per-node file-metadata snapshot `FileMetadata` in `internal/repository`; the representation facts come from the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) and [save caller and parameter encoding](SAVE-CALLER-ENCODING.md), with the actor representation reusing the [Actor value](ACTOR-VALUE.md) — evidence is not repeated here.

## API

- `FileMetadataInput{CreatedMillis, ModifiedMillis, UpdatedMillis int64; CreatedBy, ModifiedBy Actor; SHA1Original, MD5Original []uint16}`：显式输入。两个 Actor 值已经 `NewActor` 规范化；两个 original 表示 `fileInfoToNode` 映射**之后**的值（NO_ORIG 标记原样），`ChecksumInfo`/`FileInfo` 构造器与 setter 的归一化、时间回退与小写化属于其他边界，不在本输入内重复。没有 original SHA256 字段。
  The explicit input. Both Actor values are already normalized by `NewActor`; both originals represent post-`fileInfoToNode`-mapping values (the NO_ORIG marker verbatim), while `ChecksumInfo`/`FileInfo` constructor/setter normalization, time fallback and lowercasing stay at their own boundaries. There is no original-SHA256 field.
- `NewFileMetadata(in FileMetadataInput) FileMetadata`：构造显式快照。三个时间逐字复制、相互独立，完整保留 signed int64 范围（含负数与 0）；不取当前时间、不排序、不强迫相等、不转换 `time.Time`。零值快照时间为 0、两 Actor 为 null、两 original 为 nil——这是显式快照零值，不是目标新建 Java builder 的当前时间默认值；未来创建流程自行提供已选值。
  Constructs the explicit snapshot. The three times copy verbatim and independently across the full signed int64 range (negatives and zero included) with no clock, ordering, forced equality or `time.Time` conversion. The zero snapshot has zero times, null actors and nil originals — an explicit-snapshot zero, not the target fresh-builder clock defaults; future creation code supplies its own chosen values.
- `(m FileMetadata) CreatedMillis()/ModifiedMillis()/UpdatedMillis() int64`、`CreatedBy()/ModifiedBy() Actor`：Actor 按值保存与返回，复用其封装只读接口，无额外截断或转换。
  Actors are stored and returned by value, reusing their encapsulated read-only API with no extra truncation or conversion.
- `(m FileMetadata) SHA1OriginalUTF16()/MD5OriginalUTF16() []uint16`：每次返回防御性副本。nil 表示 null，非 nil 空（包括大容量空子切片输入）表示空串；其余逐码元保留任意长度——大小写、NO_ORIG、空白/组合码元、NUL、孤立/反序/有效代理与畸形值一律不做 Actor 式 64 码元缩写、不做十六进制/长度/上传合法性验证、不小写化、不替换标记、不按 UTF-8/rune 重编码。original 不是 Actor 类型。
  Each returns a defensive copy. nil means null and nonnil empty (including large-capacity empty subslice inputs) means the empty string; everything else keeps every unit verbatim at any length — case, NO_ORIG, whitespace/combining units, NUL, lone/reversed/valid surrogates and malformed values get no Actor-style 64-unit abbreviation, no hex/length/upload validation, no lowercasing, no marker substitution and no UTF-8/rune re-encoding. Originals are not the Actor type.
- 所有权：构造对两个 original 各复制一次（恰好输入长度，O(n)，不保留调用方后备数组与多余容量，重叠子切片互不共享后备存储）；getter 每次各复制一次。修改构造输入、输入结构体、返回切片或值副本都不能改变既有快照；并发读者可各自修改自己的副本；构造与调用方写入并发不在承诺内。
  Ownership: construction copies each original once (exactly the input length, O(n), retaining no caller backing array or excess capacity, with overlapping subslices never sharing a backing store) and each getter copies on every read. Mutating the construction input, the input struct, a returned slice or a value copy can never change an existing snapshot; concurrent readers may mutate their own copies; construction racing caller writes is outside the contract.

## 边界 / Boundaries

- 有意不提供自定义 `String`/JSON/`driver.Valuer`/`sql.Scanner`/传输转换；默认 fmt/反射行为不是脱敏或传输保证。未来传输/SQL 层需独立决定长度、编码与拒绝行为。
  No custom `String`/JSON/`driver.Valuer`/`sql.Scanner`/transport conversion is provided on purpose; default fmt/reflection behavior is not a redaction or transport guarantee. Future transport/SQL boundaries own length, encoding and rejection policy.
- 本切片只交付纯值：既有 `FileReference`、`Path`、`Actor`、`ValidateStoragePath`、Store、schema、保存编排、HTTP 与运行接线不变；消费者、SQL/迁移、传输与保存编排仍开放（见[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)的剩余 UNKNOWN）。
  This slice delivers only the pure value: the existing `FileReference`, `Path`, `Actor`, `ValidateStoragePath`, Store, schema, save orchestration, HTTP and runtime wiring are unchanged; consumers, SQL/migration, transport and save orchestration remain open in the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) UNKNOWNs.
- 后续切片已落地 SQL 消费者：postgres Store 的七列元数据投影、私有编解码与受信任 v1→v2 升级/回退 SQL 见[文件元数据持久化](FILE-METADATA-PERSISTENCE.md)；本页仍只约束纯值本身。
  A later slice landed the SQL consumer: the postgres Store's seven-column projection, private codec and trusted v1→v2 upgrade/downgrade SQL live in the [file-metadata persistence note](FILE-METADATA-PERSISTENCE.md); this page still governs the pure value only.
- 原版 Java 模型/驱动源事实仍限定在上述研究页；本页不复制原始回放输出。
  The original Java model/driver source facts stay scoped to the linked research pages; no raw replay output is copied here.

## 验证 / Verification

`go test -race ./internal/repository -run FileMetadata -count=1` 覆盖零值快照、全 signed int64 时间边界与独立性（±2^53 邻域、long 极值）、Actor 组合（null/空/原样/缩写）、original null/非 nil 空区分（含大容量空子切片）、全部 65536 单码元与超长值逐码元保留、不做缩写/小写化/标记替换、输入/返回切片/值副本所有权、重叠子切片隔离与 8 个并发读者（各自修改副本）。真实行为 RED/GREEN 证据与 SHA-256 见任务目录；根独立验证结果见下文。
`go test -race ./internal/repository -run FileMetadata -count=1` covers the zero snapshot, full-signed-int64 time boundaries and independence (the ±2^53 neighborhood, long extremes), actor composition (null/empty/verbatim/abbreviated), original null-versus-nonnil-empty fidelity (including large-capacity empty subslices), verbatim retention of all 65536 single units and long values, no abbreviation/lowercasing/marker substitution, input/returned-slice/value-copy ownership, overlapping-subslice isolation and 8 concurrent readers (each mutating its own copies). Real behavioral RED/GREEN evidence and SHA-256 digests stay archived in the task directory; the independent verification results follow below.


## 独立验证 / Independent verification

Go race 探针对照实现前冻结的真实目标 NodeBuilder/Node 参考：68721 组显式七字段输入全部一致，共 481047 次字段比较；另验证零值、原始输入未改、输入结构体重赋、输入/getter/值副本所有权、重叠 original 及大容量空切片的 null/empty 区分，8 个并发读者各 256 次，共 2048 次迭代。不保留输入后备数组的结论来自构造器源码的按长度分配与复制；未用返回切片容量或 GC 指标替代这一证明。
A race-instrumented Go probe matched all 68721 explicit seven-field inputs against the actual target NodeBuilder/Node reference frozen before implementation, totaling 481047 field comparisons. It also checked the zero value, untouched input, input-struct reassignment, input/getter/value-copy ownership, overlapping originals and null/empty fidelity for large-capacity empty slices; 8 concurrent readers executed 256 iterations each (2048 total). Constructor source review of allocation by length and copying establishes that caller backing arrays are not retained; returned-slice capacity or GC counters are not used as substitutes for that reasoning.

两项隔离故障验证确认永久回归能捕获 modified actor 错接成 created actor、MD5 original 空串丢为 null；两种故障均编译成功但测试行为失败。该证据限定显式纯值快照，不代表 FileInfo 默认值、ChecksumInfo 归一化、SQL/JSON、保存链或 HTTP 已完成；原始证据保持私有。
Two isolated fault probes confirm that the permanent regressions catch routing the modified actor to the created actor and collapsing an empty MD5 original to null: both faulty variants compile and fail behavioral assertions. This evidence covers the explicit pure-value snapshot, not FileInfo defaults, ChecksumInfo normalization, SQL/JSON, save orchestration or HTTP; raw evidence remains private.

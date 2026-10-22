# Actor 值 / Actor value

状态 / State: BIN-139 实现切片（2026-10-22 所有者批准"批准方案并实施 Actor 纯值"）。本页是 `internal/repository` 中可空原始 UTF-16 `Actor` 纯值的唯一规范说明；表示事实源自[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)与[保存调用方与参数编码](SAVE-CALLER-ENCODING.md)，此处不重复证据。
State: BIN-139 implementation slice (owner approval on 2026-10-22). This page is the single canonical note for the nullable raw-UTF-16 `Actor` pure value in `internal/repository`; the representation facts come from the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) and [save caller and parameter encoding](SAVE-CALLER-ENCODING.md) and are not repeated as evidence here.

## API

- `NewActor(raw []uint16) Actor`：nil 输入表示 Java null；非 nil 空切片表示 Java 空串。长度 <=64 个 UTF-16 码元原样保留；>64 只保留首 61 个码元再加三个 `0x002e`（ASCII 句点）。接受每个 `uint16`——NUL、未配对高低代理、有效代理对——不做 trim、归一化、UTF-8 解码、权限判断或拒绝；无错误返回。构造即复制：输入后备存储从不保留，无论输入多大保留数据不超过 64 个码元。
  nil input maps to the Java null; a nonnil empty slice maps to the Java empty string. Lengths <=64 UTF-16 units are kept verbatim; above 64 only the first 61 units plus three `0x002e` (ASCII dots) survive. Every `uint16` is accepted — NUL, unpaired high/low surrogates, valid pairs — with no trim, normalization, UTF-8 decoding, authorization or rejection; there is no error result. Construction copies: the input backing store is never retained and the retained data never exceeds 64 units regardless of input size.
- `(a Actor) IsNull() bool`：零 `Actor` 与 `NewActor(nil)` 都是 null。
  The zero `Actor` and `NewActor(nil)` are both null.
- `(a Actor) UTF16() []uint16`：防御性副本；null 返回 nil，present-empty 返回非 nil 零长度。修改构造输入或任何返回切片都不能改变既有 `Actor` 或其值副本。无 setter。
  A defensive copy: nil for null, a nonnil zero length for present-empty. Mutating the construction input or any returned slice can never change an existing `Actor` or its value copies. There are no setters.

## 边界 / Boundaries

- 有意不提供自定义 `String`/UTF-8/JSON/`driver.Valuer`/`sql.Scanner` 转换：`'?'`（字节 `3f`）只是[所选 PostgreSQL 参数编码边界](SAVE-CALLER-ENCODING.md)上的表现，本领域值在编码之前保留原始未配对码元；默认 fmt/反射行为不是脱敏日志容器或兼容传输格式的保证。
  No custom `String`/UTF-8/JSON/`driver.Valuer`/`sql.Scanner` conversion is provided on purpose: `'?'` (byte `3f`) is only the appearance at the selected PostgreSQL parameter-encoding boundary while this domain value keeps the raw unpaired unit before encoding; default fmt/reflection behavior is not a redaction-log container or a compatible transport format.
- 本切片只交付纯值：既有 `FileReference`、SQL/schema、保存编排、HTTP 与运行接线不变；元数据模型、SQL 迁移与传输策略仍开放，见[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)的剩余 UNKNOWN。
  This slice delivers only the pure value: the existing `FileReference`, SQL/schema, save orchestration, HTTP and runtime wiring are unchanged; the metadata model, SQL migration and transport policies remain open in the [file-node save contract](FILE-NODE-SAVE-CONTRACT.md) UNKNOWNs.
- 原版 Java 模型/驱动源事实仍分别限定在上述两份研究页；根的独立验证详见下节，本页不复制原始回放输出。
  The original Java model/driver source facts stay scoped to the two research pages above; the root's independent verification is summarized below and no raw replay output is copied here.

## 验证 / Verification

`go test -race ./internal/repository -run Actor -count=1` 覆盖 null/empty/零值、63/64/65 边界、BMP/NUL/空白/组合码元、跨界拆分与保留区内的代理对、孤立/反序/重复代理、输入与返回切片别名、值副本稳定性、并发读者与有界输出。
`go test -race ./internal/repository -run Actor -count=1` covers null/empty/zero, the 63/64/65 boundary, BMP/NUL/whitespace/combining units, split and fully-retained surrogate pairs, lone/reversed/repeated surrogates, input and returned-slice aliasing, value-copy stability, concurrent readers and bounded output.

## 根独立对照 / Root independent comparison

实现提交后，根在冻结的目标 JVM 参考之上完成独立对照（该参考在实现**之前**准备；本次运行将当前 Go 实现与该参考比对，不重新执行目标存续运行时）：135966 个原始 UTF-16 输入与两个真实目标 Node actor getter 逐码元一致（271932 次 getter 对照）；race 检测下的 Go 探针另核实 null/空、零值、构造输入未被改动、输入/返回切片/值副本所有权，并发读者 8 个 × 2048 次迭代。私有清单与摘要在任务目录留档；范围仅纯 `Actor` 值——不声称 SQL/JSON/HTTP/保存链或完整目标兼容。
After the implementation commit, the root completed an independent comparison against the frozen target JVM reference (prepared BEFORE implementation; this run compared the current Go implementation with that reference rather than re-executing the target live runtime): 135966 raw UTF-16 inputs matched both actual target Node actor getters unit by unit (271932 target getter comparisons); a race-instrumented Go probe additionally verified null/empty, the zero value, untouched constructor input, input/getter/value-copy ownership, with 8 concurrent readers over 2048 iterations. The private manifest and digests stay archived in the task directory; the scope is the pure `Actor` value only — no SQL/JSON/HTTP/save-chain or full target compatibility is claimed.

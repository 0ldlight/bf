# 存储路径校验 / Storage path validation

状态 / State: BIN-136 内部纯函数切片，已实现并经 `-race` 单元与共享检查门禁验证；本页是公开规范来源。
An internal pure-function slice implemented and verified by `-race` unit tests and the shared check gate; this page is the public canonical source.

## 范围 / Scope

`internal/repository` 的 `ValidateStoragePath(raw string) error` 是显式存储名检查器，供未来保存消费者在其选定的**序列化存储路径**（目标 `RepoPathImpl.toPath` 静态串行化为 `repoKey+'/'+path`，非根文件夹末尾再带 '/'）上、在后续 ID 消费/保存编排**之前**调用。本切片不接线任何生产消费者。它只判定给定字符串：既不构造也不归一化 RepoPath；既有 `NewPath`/`Key` 身份归一化构造器保持不变（两者互不替代）。目标 `FileServiceImpl.createFile` 静态上先调用 `PathValidator.validate(file.getRepoPath().toPath())` 再取 `nextId`，这是字节码事实，不是应用回放。
`ValidateStoragePath(raw string) error` in `internal/repository` is the explicit storage-name checker for the future save consumer to invoke on its chosen **serialized storage path** (the target `RepoPathImpl.toPath` statically serializes `repoKey+'/'+path`, plus a final '/' for non-root folders) BEFORE later ID consumption/save orchestration. No production consumer is wired in this slice. It judges a given string only: it neither builds nor normalizes a RepoPath, and the existing `NewPath`/`Key` identity-normalization constructor is unchanged (neither substitutes for the other). That the target `FileServiceImpl.createFile` statically calls `PathValidator.validate(file.getRepoPath().toPath())` before `nextId` is a bytecode fact, not an app replay.

## 契约 / Contract

- 固定 API：`ErrInvalidStoragePath`（固定文案 `repository: invalid storage path`，`errors.Is` 可用）；接受返回 nil，拒绝返回该哨兵。错误不回显输入、被拒字符、目标异常文案或凭据。
  Fixed API: `ErrInvalidStoragePath` (fixed text, `errors.Is` works); nil on accept, that sentinel on rejection. Errors never echo the input, rejected character, target exception text or credentials.
- 函数确定性、无状态、O(n) 时间、O(1) 辅助存储、无 I/O；不改动、清洗、归一化、URL 解码或折叠输入。
  The function is deterministic, stateless, O(n) time, O(1) auxiliary storage, no I/O; it never mutates, cleans, normalizes, URL-decodes or collapses the input.
- 空串或全为 Java `Character.isWhitespace` 字符（本 JVM 为 0009..000d、001c..0020、1680、2000..2006、2008..200a、2028、2029、205f、3000，排除 NBSP/U+2007/U+202F/NEL/U+180E）拒绝。
  Empty or all-Java-whitespace strings (0009..000d, 001c..0020, 1680, 2000..2006, 2008..200a, 2028, 2029, 205f, 3000 on this JVM; NBSP/U+2007/U+202F/NEL/U+180E excluded) reject.
- `'/'` 与**字面 U+FFFF** 是等价的分隔符转移；重复分隔符接受；EOF 执行同一终结检查；不折叠斜杠。增补平面字符（U+1FFFF、U+10FFFF 等）是普通字符，绝不截断为 16 位。
  `'/'` and the **literal U+FFFF** are equivalent delimiter transitions; repeated delimiters are accepted; EOF performs the same final check; no slash collapsing. Supplementary-plane characters (U+1FFFF, U+10FFFF, …) are ordinary and never truncated to 16 bits.
- 分隔符/结尾在当前待定状态为空白、点或与号时拒绝，否则进入分隔符后状态。分隔符后紧跟 Java 空白（含空格）拒绝。段内空白仅置空白待定，并非一律禁止；起始独立空白后接普通文本可通过；完整 repo/path 输入改变斜杠上下文，不可预剪裁。
  A delimiter/end rejects when the pending status is space, dot or ampersand, otherwise it enters the after-delimiter status. A Java whitespace char (space included) immediately after a delimiter rejects. Whitespace inside a segment only makes space pending — it is not generally forbidden; standalone leading whitespace followed by ordinary text can pass; a full repo/path input changes the slash context, so no pre-trimming.
- `'.'`/`'&'` 在起点或分隔符后建立点/与号待定；其余位置**保持原状态不变**（粘滞，含空白/点/与号）。`'.'`、`'..'`、`'&'`、`'&&'`、`'.&'`、`'&.'` 段在结尾/分隔符处拒绝；`'.a'`、`'&a'`、`'a.'`、`'a&'` 通过；`'a .'`、`'a &'` 拒绝；`'a .b'`、`'a &b'` 通过。普通字符清除待定状态。
  `'.'`/`'&'` establish dot/ampersand pending at input start or right after a delimiter; elsewhere they **leave the previous status unchanged** (sticky, including space/dot/ampersand). `.`, `..`, `&`, `&&`, `.&`, `&.` segments reject at end/delimiter; `.a`, `&a`, `a.`, `a&` pass; `a .`, `a &` reject; `a .b`, `a &b` pass. Ordinary characters clear the pending status.
- `'"'`、`'*'`、`'?'`、`'\\'`、`'|'` 无条件拒绝。`':'`、`'<'`、`'>'`、`'#'`、`';'`、`'%'` 及其余普通字符（含 NUL、多数控制字符、百分号串、未归一化组合）在上下文条件内接受；不引入额外平台文件名策略、Unicode 归一化或 NUL 过滤。
  `'"'`, `'*'`, `'?'`, `'\\'`, `'|'` reject anywhere. `':'`, `'<'`, `'>'`, `'#'`, `';'`, `'%'` and all other ordinary characters (NUL, many controls, percent runs, unnormalized combining forms) are accepted subject to the contextual conditions; no extra platform filename policy, Unicode normalization or NUL filtering is introduced.
- 有效 UTF-8 的接受/拒绝对齐目标类。**无效 UTF-8 是本地 Go 边界策略显式拒绝**（原创测试断言），非目标 Java 对等：Java null 无法用此 string API 表示（目标对 null 抛 NPE，空串拒绝），未配对 UTF-16 不在有效 UTF-8 等价范围内。
  Valid-UTF-8 acceptance/rejection matches the target class. **Invalid UTF-8 is explicitly rejected as a LOCAL Go boundary policy** (asserted by original tests), not target-Java parity: Java null is unrepresentable in this string API (the target throws NPE on null, rejects empty), and unpaired UTF-16 is outside valid-UTF-8 equivalence.
- 接受不代表文件系统安全、认证或路径穿越防护；目标异常消息/理由区分保持私有研究数据，固定内部错误是本地诊断政策的刻意选择；不做任何 HTTP 状态/文案声明。
  Acceptance is not filesystem-safety, auth or traversal protection; the target exception messages/reason distinctions stay private research data — the fixed internal error is a deliberate local diagnostic policy — and no HTTP status/message claims are made.

## 证据与验证 / Evidence and verification

证据身份（完整 SHA-256，无缩写；父级/来源上下文见[研究索引](RESEARCH-INDEX.md)）/ Evidence identities (FULL SHA-256, no abbreviations; see the [research index](RESEARCH-INDEX.md) for parent/source context):

| 对象 / Object | 版本 / Version | SHA-256 |
|---|---|---|
| 目标 WAR / target WAR | Artifactory 7.161.26 revision 86126900 | `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37` |
| `artifactory-common` JAR | 7.161.15 | `32bd6c2a23c2b109b3c1c8ea4947c0a093e5fc1436aea04a8eeb29c0afc77a05` |
| `org.artifactory.util.PathValidator` class | 7.161.15 内精确类 / exact class within 7.161.15 | `23e330a568d6ef565ca4520cad6da8d62d358104f9d73c31699a4f47be9aad3e` |
| `commons-lang3` JAR（真实打包依赖 / real packaged dependency） | 3.20.0 | `69e5c9fa35da7a51a5fd2099dfe56a2d8d32cf233e2f6d770e796146440263f4` |

上述精确 `PathValidator` 类连同其真实打包 `commons-lang3` 3.20.0 经隔离 `URLClassLoader` 在本地 OpenJDK 26.0.2.1 直接运行，无依赖替身。私有受控纯方法回放覆盖 270524 个唯一有效 UTF-8 输入（256014 接受/14510 拒绝）：全非代理 BMP 四上下文、长度 0..4 穷举七字符序列、种子 15000 长路径与命名 Unicode 用例。这是受控纯方法回放，不是完整应用/FileService 执行、HTTP、数据库、认证或目标运行时接线。
The exact `PathValidator` class above, together with its real packaged `commons-lang3` 3.20.0, ran directly under an isolated `URLClassLoader` on a local OpenJDK 26.0.2.1 with no dependency doubles. A private controlled pure-method replay covered 270524 unique valid-UTF-8 inputs (256014 accept/14510 reject): full non-surrogate BMP in four contexts, exhaustive seven-token sequences of length 0..4, 15000 seeded longer paths and named Unicode cases. This is a controlled pure-method replay, not full-application/FileService execution, HTTP, DB, auth or target runtime wiring.

公开测试是原创 Go 表达（表驱动、25 个 Java 空白字符三向拒绝/段内接受、非断行排除、粘滞点/与号/空白迁移、重复分隔符与字面 U+FFFF、增补平面、控制/NUL、不解码不归一化、无效 UTF-8 本地拒绝、固定哨兵不泄露输入、`NewPath` 行为不变边界），经 `go test -race ./internal/repository` 与 `./scripts/check.sh`；全部通过。私有回放语料不进入跟踪文件。
Public tests are original Go expressions (table-driven; all 25 Java whitespace chars rejected in three positions and accepted inside a segment; nonbreaking exclusions; sticky dot/ampersand/space transitions; repeated delimiters and the literal U+FFFF; supplementary plane; controls/NUL; no decoding or normalization; local invalid-UTF-8 rejection; the fixed sentinel never leaking input; and an explicit `NewPath`-unchanged boundary), run through `go test -race ./internal/repository` and `./scripts/check.sh`; all green. The private replay corpus never enters tracked files.

## 关联 / Links

- 路径身份值模型 / Path identity value model: [REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md)
- 证据索引 / Evidence index: [RESEARCH-INDEX.md](RESEARCH-INDEX.md)

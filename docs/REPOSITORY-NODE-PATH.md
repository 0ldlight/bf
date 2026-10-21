# 仓库节点路径身份 / Repository node path identity
状态 / State: BIN-131 内部值模型切片，已实现并经 `-race` 单元与共享检查门禁验证；本页是公开规范来源。
Internal value-model slice implemented and verified by `-race` unit tests and the shared check gate; this page is the public canonical source.

## 范围 / Scope
`internal/repository` 的 `Path`/`Key` 是仓库节点的路径身份值模型，对齐目标 Artifactory 7.161.26（revision 86126900）`NodePath`/`NodeUtils` 的静态字节码与 23 例 JVM 回放（私有证据，见[研究索引](RESEARCH-INDEX.md)）。回放仅在本地 OpenJDK 26.0.2.1 上运行从目标 WAR（SHA-256 `9c2c1883…8c9f37`）内 storage-db 组件（JAR SHA-256 `7687e761…6c8164e9`）加载的精确目标路径类对象，不是对运行中 Artifactory HTTP/数据库的采样。本切片只做路径身份：不含仓库保存功能、HTTP、SQL 模式、GC、认证、HA，也不含 `toRepoPath`/`fromRepoPath`。
`Path`/`Key` in `internal/repository` model repository node path identity, following static bytecode of the target's `NodePath`/`NodeUtils` plus a 23-case JVM replay (private evidence; see the [research index](RESEARCH-INDEX.md)). The replay ran only exact target path class objects loaded from the storage-db component (JAR SHA-256 `7687e761…6c8164e9`) inside the target WAR (SHA-256 `9c2c1883…8c9f37`) under a local OpenJDK 26.0.2.1 — not live Artifactory HTTP/DB sampling. This slice is path identity only: no repository save feature, HTTP, SQL schema, GC, auth, HA, or `toRepoPath`/`fromRepoPath`.

## 契约 / Contract
- `NewPath(repo,parent,name,file)` 在归一化**之前**拒绝 Java-blank 的 repo，或 Java-blank 的 name 且 parent 非 Java-blank；返回零 `Path` 与固定文案哨兵 `ErrInvalidPath`（不含输入字节）。除此之外构造器不做校验。
  `NewPath` rejects, **before** normalization, a Java-blank repo, or a Java-blank name with a non-Java-blank parent, returning a zero `Path` and the fixed-text sentinel `ErrInvalidPath` (no input bytes echoed). The constructor performs no other validation.
- 归一化仅两步：parent 先 Java `String.trim`（仅两端码元 <= U+0020），再仅移除边界 '/'（对齐目标 `PathUtils.trimSlashes`）；name 只做同样的 Java trim。保留内部双斜杠、点段、反斜杠、百分号与名字中的斜杠；无 `path.Clean`、无 Go `TrimSpace`、无 URL 解码、无折叠、无小写化。repo 字节原样保留。
  Normalization is exactly two steps: the parent gets Java `String.trim` then boundary-`'/'` removal only (target `PathUtils.trimSlashes`); the name gets Java trim only. Internal double slashes, dot segments, backslashes, percent runs and name slashes survive; no `path.Clean`, Go `TrimSpace`, URL decoding, collapsing or lowercasing. Repo bytes are retained verbatim.
- `Key` 仅含三个未导出字符串（repo/parent/name），可比较、区分大小写、排除 file/folder；同三个归一化字符串的两种 Kind 共享同一 `Key`。不做拼接串身份，冒号/斜杠分隔符不可能碰撞。
  `Key` holds the three unexported strings only; it is comparable, case sensitive and excludes file/folder, so both kinds share one `Key` for identical normalized components. There is no joined-string identity, so colon/slash delimiters cannot collide.
- `IsRoot`：归一化父与名都 Java-blank（不只是空串）；Kind 不影响根。`PathName`：父 Java-blank 返回 name，否则 `parent+'/'+name`。不提供自定义 `String()`（Go 的 fmt 仍会默认打印结构体字段）：`Path`/`Key` 不是脱敏日志容器，诊断输出必须按信任边界过滤路径。
  `IsRoot`: normalized parent and name both Java-blank (not merely empty); kind never affects rootness. `PathName`: name alone for a blank parent, else `parent+'/'+name`. No custom `String()` is provided (Go's fmt still prints struct fields by default): `Path`/`Key` are not redacted logging containers, so diagnostics must filter paths according to the trust boundary.
- `Depth()`：根 0；Java-blank 父 1；否则归一化父内 '/' 计数 +2。只有 `Depth` 检查 32767 上界（构造器接受过深的值），溢出返回 0 与 `ErrDepthExceeded`。
  `Depth()`: 0 for the root; 1 for a Java-blank parent; otherwise slash count of the normalized parent plus 2. Only `Depth` enforces the 32767 bound (the constructor accepts deeper values); overflow returns 0 with `ErrDepthExceeded`.
- Java-blank 复刻 `Character.isWhitespace`：ASCII 9..13、28..31 与 Zs/Zl/Zp，排除 NBSP(U+00A0)/U+2007/U+202F；NEL(U+0085) 非空白；不使用 `unicode.IsSpace`。Go 空串表示 Java null/空串边界，不用指针。
  Java-blank replicates `Character.isWhitespace`: ASCII 9..13, 28..31 and Zs/Zl/Zp minus U+00A0/U+2007/U+202F; NEL (U+0085) is not blank; `unicode.IsSpace` is not used. A Go empty string represents the Java null/empty boundary without pointers.
- 零值 `Path`/`Key` 是非法构造值：构造器出错返回零值，但 Go var/字面量同样能写出零值；消费者必须使用成功构造的值，本切片无运行期持久化消费者。构造器不提供认证、安全或路径穿越保证。
  Zero `Path`/`Key` values are invalid constructed values: constructors return zero on error, yet Go var/literals also produce them; consumers must use successfully constructed values, and this slice has no runtime persistence consumer. The constructor offers no auth, security or path-traversal guarantee.

## 边界 / Boundaries
畸形 UTF-8 不在目标 Java String 等价范围内，本层不额外拒绝也不宣称完整畸形输入对等；JVM 回放归一化输出仅作私有对照，公开测试为原创 Go 表达。
Malformed UTF-8 is outside the target Java String equivalence; this layer neither rejects it additionally nor claims full malformed-input parity. The JVM replay outputs stay a private reference; the public tests are original Go expressions.

## 验证 / Verification
`internal/repository` 表驱动与专项用例（`-race`）：拒绝先于归一化（'/' 父 + 空名）、NBSP/NEL 非空白、控制字符 NUL/US 的 Java trim、repo 保留空格、内部双斜杠、点段/反斜杠/名字斜杠不清洗、Unicode 空白根保留、Kind 排除与大小写/组件分隔身份、深度 32767/32768、零输出与固定错误文本；全部经 `./scripts/check.sh`。
Table-driven and focused cases (`-race`): reject-before-normalize ('/' parent + empty name), NBSP/NEL nonblank, Java trim of NUL/US controls, retained repo spaces, internal double slashes, no dot/backslash/name-slash cleaning, Unicode-blank root retention, kind-excluded and case/component-separated identity, depth 32767/32768, zero outputs and fixed error texts; all through `./scripts/check.sh`.

## 关联 / Links
- 证据索引 / Evidence index: [RESEARCH-INDEX.md](RESEARCH-INDEX.md)
- 内容身份 / Content identity: [CONTENT-METADATA.md](CONTENT-METADATA.md)
- 写入顺序 / Write ordering: [BINARY-WRITE.md](BINARY-WRITE.md)

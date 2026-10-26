# 文件 MIME 目录与物化 / File MIME catalog and materialization

状态：已实现，等待 PR 审阅及合并；最终提交验证以 PR 为准。
Status: implemented, pending PR review and merge; final commit checks follow the PR.

```go
func NewFileMimeCatalog(defs []FileMimeDefinition, profile FileMimeCaseProfile) (FileMimeCatalog, error)
func MaterializeFileMime(snapshot FileInfoSnapshot, catalog FileMimeCatalog) (FileInfoSnapshot, error)
```

目录从 `FileMimeDefinition{Type, Extensions string}` 的已解码属性事实实际构造，再为多份快照查找；不接受每文件预计算的 MIME 或回调。XML transport/旧版本转换、Home 与部署配置接线不在本片，目录不会自动加载系统或操作系统 MIME 表。
Definitions supply configuration facts; the constructor and materializer implement tokenization, caching and lookup. They do not implement an XML transport or deployment loader.

## 目录支持域 / Catalog profile

- 显式选择 `FileMimeCaseRootASCII` 或 `FileMimeCaseTurkicASCII`，零/未知值拒绝。Type/Extensions 必须为有效 UTF-8；Type 是显式非 null 字符串，空串合法，大小写和尾空白原样保留。
- Extensions 按逗号分割，逐 token 用 Java trim/blank 去空并去重；保持配置键大小写，不用 Go 通用 TrimSpace。NBSP/NEL/U+2007/U+202F 与 Java blank 的差异不能忽略。
- 重复 Type 拒绝构造。不同 Type 的同一扩展名永久标 ambiguous，该键查找报错，其他键继续可用；后续条目不能清除冲突。默认配置的 `xsl` 属此类。
- nil/空定义在有效 profile 下得到有效空目录；构造后目录私有不可变，可复制值并并发只读，输入修改不影响它。

Duplicate-type rejection and per-key ambiguity are local support limits, not target validation. The target's observed conflict winner is not treated as a portable XML ordering rule.

## 物化规则和错误顺序 / Materialization and error order

1. 零快照返回零结果与既有 ErrInvalidFileInfoSnapshot。
2. raw MIME 非 nil（含空串、空白或任意既有 UTF-16）原样复用快照，不访问目录。raw MIME=nil 且整条 RepoPathUTF16 无 U+002E 时，无需目录即得到 `application/octet-stream`。
3. 有点时，扩展名是**整条路径最后一个点之后**的全部码元，可空；父目录的点也参与。先要求有效目录，再要求原扩展名全 ASCII，否则分别返回 ErrInvalidFileMimeCatalog 或 ErrUnsupportedFileMimeExtension。
4. ROOT ASCII 将 A–Z 小写；Turkic ASCII 另将 I 变 U+0131。这个派生 lookup key 允许非 ASCII；原非 ASCII 扩展名仍明确不支持。不读取进程 Locale。
5. 精确匹配原配置键。ambiguous 返回 ErrAmbiguousFileMimeExtension；正常命中保留 Type 的 UTF-16（空 Type→非 nil 空）；未命中用默认类型。不会 trim 查询、改用 basename、剥 metadata、匹配复合扩展名或嗅探内容。

三个新错误的固定文案分别为 `repository: invalid file mime catalog`、`repository: unsupported file mime extension`、`repository: ambiguous file mime extension`；构造错误返回零目录，物化错误返回零快照，均不回显输入。
All errors are fixed and input-free. Cached/no-dot branches bypass even a zero catalog; dotted paths check catalog validity before their ASCII-query profile.

物化只替换 MIME，经既有快照工厂防御复制；所有其他字段逐字保持，不重跑身份、时间回退或 Original 归一化。原快照不变，返回值表达目标 getter 写回缓存后的状态：**并非宣称原版 getter 无副作用**。Fields 副本的修改不影响目录或其他结果。
The immutable result represents the target's source-cache transition while preserving every other field. It neither mutates the input nor claims that the original getter was pure.

## 范围与验证 / Scope and verification

[即时快照](IMMEDIATE-FILE-SNAPSHOT.md)仍返回 raw MIME=nil，本接口需要显式调用；此结果不是 original/virgin 复制基线，不接时钟、Jaxb copy、SQL 或[保存门](FILE-SNAPSHOT-UPDATE.md)。完整 Unicode/Locale 和 XML/部署接入另审。

本地 Go 对照核实73个无冲突默认缓存值及 xsl 冲突、15个实际查找匹配/3个本地排除、5个实际 getter 缓存或无点样本；4个真实字符串 adapter fixture 的15个 token 也逐项一致。独立 public-API 测试覆盖数据保持、错误顺序、所有权与 race。两个依赖 Home 的探针未完成，不计目标拒绝；配置组件组合不冒充绑定 Home 的完整路由。
Private structural cache observations are bound to reviewed code, while public-API regression tests exercise observable behavior. Research models, local exclusions and runtime dependency gaps are accounted separately. Fault/shared/complete CI evidence follows the final PR revision.

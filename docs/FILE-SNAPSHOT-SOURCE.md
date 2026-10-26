# 文件快照读取来源 / File-snapshot read source

状态：已实现，等待 PR 审阅及合并；最终提交验证以 PR 为准。本文只定义保留 Actual 空性的读取来源，不构造 loaded FileInfoSnapshot。
State: implemented, pending PR review and merge; final commit validation follows the PR. This read source preserves Actual nullness without constructing a loaded FileInfoSnapshot.

## 接口 / Interface

```go
// internal/repository
type FileSnapshotSource struct {
    Reference FileReference
    SHA1ActualValid bool
    MD5ActualValid bool
    SHA256ActualValid bool
}

// internal/repository/postgres
func (s *Store) LookupSnapshotSource(ctx context.Context, path repository.Path) (
    repository.FileSnapshotSource, error,
)
```

这是调用方拥有的 DTO；Reference 沿用[节点引用](NODE-REFERENCES.md)语义，其 Metadata 保持既有不可变值契约。不新增构造器、getter、错误或通用 provider。
This caller-owned DTO retains the existing reference and immutable metadata semantics. It adds no constructor, accessor, error or provider abstraction.

## 空性与单次读取 / Nullness and one read

| Valid | Reference.Content 的对应 Actual / Corresponding Actual | 含义 / Meaning |
|---|---|---|
| false | 空串 / empty string | SQL NULL；保留旧字符串投影 / NULL with the legacy string projection |
| true | 包括空串的原值 / verbatim, including empty | SQL 非 NULL / SQL non-NULL |

三个标志取自同一次 Scan 的 sql.NullString.Valid，与 Reference 绑定。不得由字符串是否非空推断，也不另查一次拼接标志。它们不是校验和条目存在性、摘要规范或内容物理完整证明。
The three bits come from the same scan as Reference, never from string non-emptiness or a second query. They establish neither checksum-entry presence, canonical digests nor physical integrity.

旧 Lookup 继续返回 Reference；两方法共用领域专用私有读取逻辑和既有 SELECT、16 列扫描及查询参数。非 nil context 是调用方前提。顺序保留 identity 校验、预取消检查、查询/扫描、文件类型守卫、存储路径重建和元数据形状解码。错误返回各自的零结果，沿用已有固定阶段文案和 errors.Is/As 原因链；不新增扫描后取消检查。
Legacy Lookup returns only Reference. Both methods share the existing query, scan and guard order. Errors return zero values with unchanged stage text and causes; no post-scan cancellation check is added.

## 保留的读取画像 / Retained read profile

存量非规范 Actual 原样读取；写侧 canonical 规则不前移。七列全 NULL 仍为 Metadata=nil，显式零快照仍非 nil，旧接口拒绝的形状和存储路径仍拒绝。NULL size/updated 的零投影、actor 读侧缩写及 repo 字面 `.` 的旧拒绝不变。
Legacy noncanonical Actuals remain readable. Metadata absence versus explicit zero, shape/path rejection, nullable primitive projection and actor abbreviation retain their prior policies.

## 为什么需要单独保留 / Why preserve these bits

原版加载器即使 Actual 为 null 也构造对应校验和条目；raw 比较区分 null 与空串。若把 SQL NULL 的 Existing 组装为空 Actual，可能与 Desired 的空 Actual 误判相同，跳过[快照更新门](FILE-SNAPSHOT-UPDATE.md)应执行的更新。
The target creates checksum entries even for null Actuals. Collapsing null into empty while constructing Existing can cause a false-clean decision in the update gate.

固定目标的十次受控 Node→FileInfo 回放得到九次成功与一次目录拒绝，确认即时加载域的三条目、null/empty、时间回退和受 Locale 影响的 Original 规则；未执行 JDBC、MIME getter、复制或保存。来源与限制见[研究索引](RESEARCH-INDEX.md)。这些证据不代表新读取接口的 Go/数据库验收已完成。
Ten controlled real-class calls establish bounded loading observations, not JDBC, lazy MIME, copying or full save behavior. They do not substitute for the new read API's Go/database checks.

## 后续边界 / Remaining boundaries

本结果不是 FileInfoSnapshot，也不逆造上传原始历史。加载组装仍须明确 lastModified 回退、类型长度 Original 归一化与 Locale、显式 file RepoPath/cached name 和 MIME 生命周期。保留空性是前置条件，不代表完整保存分派、锁/会话或并发安全已实现。
This source is not a FileInfoSnapshot or reconstructed upload history. Loaded-state normalization, Locale, path/name derivation and MIME lifecycle remain separate decisions. Null preservation alone implements no full dispatcher, session or concurrency control.

## 本地验证 / Local verification

私有错误覆盖层在真实 PostgreSQL 上因空串的 Valid 推断失败，随后正确实现的三个主场景通过（六组 Actual 行、元数据形状及错误画像）；两个入口守卫单元、race 与共享检查通过。共享查询的 AST 对照确认旧 SQL/参数、16 列扫描及两个包装器同指一个查询 helper。定向故障与完整真实 PostgreSQL CI 的最终提交记录以 PR 为准。
A real-database behavioral RED precedes the three passing GREEN scenarios. Entry guards, race and shared checks pass; an AST audit checks the original SQL/parameters and one shared 16-column scan. Fault and complete CI evidence are bound to final PR revisions.

加载身份字段的独立转换见[加载态文件身份](LOADED-FILE-IDENTITY.md)；该接口不消费 Source 或隐式构造完整快照。
See [loaded file identity](LOADED-FILE-IDENTITY.md) for the separate identity conversion, which neither consumes Source nor silently assembles a snapshot.

Source 的即时字段组装见[即时加载快照](IMMEDIATE-FILE-SNAPSHOT.md)，其 Original 支持域与 MIME 生命周期限制独立于 reader。
See [immediate loaded snapshot](IMMEDIATE-FILE-SNAPSHOT.md) for assembly; its Original profile and MIME phase do not change the reader's domain.

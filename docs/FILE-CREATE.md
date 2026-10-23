# FILE-CREATE 普通文件创建核心 / Ordinary File-Create Core

- 状态 / Status: 规范说明（已实现切片）/ Canonical note (implemented slice)
- 代码 / Code: `internal/repository/file_create.go`、`internal/repository/file_create_path.go`
- 测试 / Tests: `internal/repository/file_create_test.go`、`internal/repository/file_create_path_test.go`
- 数据库回归 / DB regression: BIN-151（`integration/postgres/file_create_test.go`，本切片不触碰）

## API

```go
type FileCreateInput struct {
    Repo     string
    Path     string
    Content  content.Metadata
    Metadata *FileMetadata
}

func CreateFile(ctx context.Context, in FileCreateInput,
    nextID func(context.Context) (int64, error),
    insert func(context.Context, FileReference) (int64, error)) (int64, error)
```

## 固定顺序 / Fixed order

1. `ctx` 非空由调用方保证；预取消 → `(0, 错误)`，零依赖调用 / a nonnil
   `ctx` is the caller's prerequisite; pre-cancellation yields
   `(0, error)` with zero dependency calls.
2. `nil nextID` 或 `nil insert` → 固定配置错误，不调用任何依赖 / nil
   callbacks are a fixed configuration error calling nothing.
3. 取 ID 前的本地画像拒绝：非法 UTF-8、构造后空 repo、原始
   `RepoPath.getName` 的 NUL 拒绝（构造 trim 先于 NUL 判定；原始路径与
   force-file 后路径可以不同）/ local-profile rejections before ID
   allocation: invalid UTF-8, a repo blank after the construction trim,
   and the original `getName` NUL rejection (the construction trim
   precedes the NUL verdict; the original and forced paths may differ).
4. force-file 后的 file `RepoPath.toPath` 进 `ValidateStoragePath` / the
   forced file `toPath` goes to `ValidateStoragePath`.
5. `nextID` 恰好一次；错误优先于返回的部分 ID；非正 ID 以
   `ErrInvalidAllocatedID` 拒绝且不重取 / `nextID` exactly once; an
   error beats a partially returned ID; a nonpositive ID rejects with
   `ErrInvalidAllocatedID` without re-fetching.
6. 取消检查点丢 ID 不返还 / cancellation checkpoints drop the ID.
7. 映射经真实 helper 语义与既有 `NewPath`；剥离后空名字在取 ID 之后才
   拒绝（ID 空洞）/ mapping uses the real helper semantics and the
   existing `NewPath`; an empty post-strip name rejects only after the
   ID was allocated (an ID hole).
8. `insert` 恰好一次；错误优先于计数；计数严格为 1，否则
   `ErrUnexpectedCreateCount`；成功后无追加取消检查 / `insert` exactly
   once; an error beats the count; the count must be strictly one;
   no cancellation check after success.

## 边界 / Boundaries

- 事务归调用方：不开事务、不提交、不回滚、不重试、无 upsert/回读/补偿；
  成功仅在调用方事务内临时成立 / the transaction is caller-owned: no
  begin/commit/rollback, no retry, no upsert/readback/compensation;
  success is provisional.
- canonical 三摘要、根路径、Depth 与 SQL 错误留在旧 `Store`（`insert`
  内），位于取 ID 之后 / canonical digests, root, Depth and SQL errors
  stay with the legacy `Store` (inside `insert`), after ID allocation.
- `Content` 按值透传；`Metadata` 指针原样透传（nil 即七列 NULL），时间/
  actor/original 不重新归一化 / `Content` passes by value; the
  `Metadata` pointer passes verbatim (nil stays the seven NULL columns)
  with no re-normalization of times/actors/originals.
- 错误：`Error()` 只显示固定阶段文案（不回显输入），`Unwrap` 保留原因供
  `errors.Is/As` / errors show only fixed stage texts (never echoing
  input) and keep causes through `Unwrap`.

## 路径语义锚点 / Path semantics anchors

两参构造器（repo Java trim 非空、path Java trim + 剥边界斜杠、folder
标志取原始尾斜杠）、force-file 重建（第二次 trim 吸收暴露的边界空白）、
`getName`（'/' 与 '\\' 双分隔符、整串 NUL 拒绝、最后冒号受
properties/statistics/maven-metadata 守卫的单次剥离）、`getParent`
（java.io.File Unix 观测：'//' 折叠、尾斜杠剥离、点段不解析；formatPath
随后把 '\\' 转正斜杠）。语义依据为冻结的 javap 反汇编与 135 行 JVM 语料
差分（证据私有保存，不出现在公开文件）/ the two-argument constructor
(repo nonempty after the Java trim, path Java-trimmed plus boundary-slash
strip, folder flag from the raw trailing slash), the force-file rebuild
(the second trim absorbs newly exposed boundary whitespace), `getName`
(both separators, whole-string NUL rejection, a single last-colon strip
guarded by properties/statistics/maven-metadata), and `getParent`
(observed java.io.File Unix behavior: '//' collapses, trailing slash
drops, dot segments unresolved; formatPath then turns '\\' into '/').
The authority is the frozen javap disassembly plus a 135-row JVM corpus
differential (evidence kept privately, absent from public files).

## 相关 / Related

[REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md)（NewPath 身份）、
[STORAGE-PATH-VALIDATION.md](STORAGE-PATH-VALIDATION.md)（校验器）、
[FILE-METADATA.md](FILE-METADATA.md)（元数据透传）、
[NODE-ID-CONTRACT.md](NODE-ID-CONTRACT.md)（ID 契约）、
[NODE-REFERENCES.md](NODE-REFERENCES.md)（FileReference）。

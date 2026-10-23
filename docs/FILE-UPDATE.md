# FILE-UPDATE 普通文件更新核心 / Ordinary File-Update Core

- 状态 / Status: 规范说明（已实现切片）/ Canonical note (implemented slice)
- 代码 / Code: `internal/repository/file_update.go`
- 测试 / Tests: `internal/repository/file_update_test.go`
- 数据库回归 / DB regression: BIN-158（`integration/postgres/file_update_test.go`，独立切片）

## API

```go
type FileUpdateInput struct {
    ID       int64
    Repo     string
    Path     string
    Content  content.Metadata
    Metadata *FileMetadata
}

func UpdateFile(ctx context.Context, in FileUpdateInput,
    update func(context.Context, FileReference) (int64, error)) (int64, error)
```

`update` 委托签名与 `postgres.Store.Update` 一致，可现成注入；领域不依赖
PostgreSQL 适配器 / the delegate signature matches `postgres.Store.Update` for
direct injection; the domain depends on no PostgreSQL adapter.

## 固定顺序 / Fixed order

1. `ctx` 非空由调用方保证；预取消 → `(0, 错误)`，零依赖调用 / a nonnil
   `ctx` is the caller's prerequisite; pre-cancellation yields `(0, error)`
   with zero dependency calls.
2. `nil update` 委托 → 固定配置错误（先于快照检查）；`nil Metadata` →
   固定契约错误 / a nil delegate callback is a fixed configuration error
   (checked before the snapshot); nil `Metadata` is a fixed contract error.
3. 本地画像拒绝（委托前）：非法 UTF-8、两参构造器拒绝、构造后原始
   `RepoPath.getName` NUL 预检；force-file 先于该预检，构造 trim 先于 NUL
   判定（语义同 [FILE-CREATE.md](FILE-CREATE.md)）/ local-profile
   rejections before the delegate: invalid UTF-8, two-argument constructor
   rejection and the original `getName` NUL precheck; force-file precedes
   the precheck and the construction trim precedes the NUL verdict (same
   semantics as [FILE-CREATE.md](FILE-CREATE.md)).
4. force-file 后的 file `toPath` 进 `ValidateStoragePath` / the forced
   file `toPath` goes to
   [ValidateStoragePath](STORAGE-PATH-VALIDATION.md).
5. 取消检查点 / a cancellation checkpoint.
6. `ID <= 0` → 新导出 `ErrInvalidUpdateID`（显式本地守卫，非目标行为；
   旧 Store 的 SQL 边界守卫保留为第二道）/ `ID <= 0` rejects with the
   exported `ErrInvalidUpdateID` (an explicit local guard, not target
   behavior; the legacy Store's SQL-boundary guard remains a second line).
7. 映射经共享 helper 语义与既有 `NewPath`（同 [FILE-CREATE.md](FILE-CREATE.md)
   锚点）/ mapping goes through the shared helper semantics and the
   existing `NewPath`.
8. 委托前取消检查点 / a pre-delegate cancellation checkpoint.
9. `update` 恰好一次：错误优先 → `(0, 包裹错误)`；否则计数**原样直通**
   （含 0；非 Store 委托的 >1 也直通，真实 Store 因主键结构性不可达 >1；
   不强制为 1）；成功后无追加取消检查 / the delegate runs exactly once:
   an error yields `(0, wrapped error)` first; otherwise the count passes
   through **verbatim** (0 included; >1 from a non-Store delegate passes
   too — structurally unreachable in the real Store because of the primary
   key — and is never forced to one); no cancellation check after success.

## 边界 / Boundaries

- 事务归调用方：不开事务、不提交、不回滚、不重试；成功计数仅在调用方
  事务内临时成立 / the transaction is caller-owned; a successful count is
  provisional.
- `Metadata` 必须非 nil：显式零快照（`&FileMetadata{}`）合法，指针与
  `Content` 原样透传，不补时钟/actor/original；nil=保留七列是旧 Store 缝
  的本地遗留策略，不进入本消费者契约（见
  [FILE-METADATA-PERSISTENCE.md](FILE-METADATA-PERSISTENCE.md)）/ a
  nonnil explicit `Metadata` snapshot is required (an explicit zero
  snapshot is legal); the pointer and `Content` pass verbatim with no
  generated clocks/actors/originals; nil=leave-seven-columns is a legacy
  Store-seam local policy outside this consumer contract.
- `Path` 是调用方携带的快照路径，**不是请求的目的地**：本地薄切片无查找
  步骤，携带路径直写身份列，来源与目标"存储行重建快照"的校验来源显式不
  同；身份列可写不构成上传 move/rename 端到端兼容承诺 / `Path` carries a
  caller snapshot, **not a requested destination**: this thin slice has no
  lookup step, the carried path writes the identity columns directly, and
  its provenance explicitly differs from the target's storage-row-rebuilt
  snapshot; identity SET capability establishes no upload move/rename
  end-to-end parity.
- 不拥有：查找、isIdentical 差分门、时钟、ID 分配、upsert/回读/补偿 /
  not owned: lookup, the isIdentical diff gate, clocks, ID allocation,
  upsert/readback/compensation.
- 内容 canonical 三摘要、size、根路径、Depth 校验留在旧 Store（委托内，
  [FILE-METADATA-PERSISTENCE.md](FILE-METADATA-PERSISTENCE.md) 的 SQL 边
  界）/ canonical digests, size, root-path and Depth validations stay with
  the legacy Store (inside the delegate).
- 错误：`Error()` 只显固定阶段文案（不回显输入），`Unwrap` 保留原因供
  `errors.Is/As` / errors show only fixed stage texts (never echoing
  input) and keep causes through `Unwrap`.

## 相关 / Related

[FILE-CREATE.md](FILE-CREATE.md)（同 helper 语义与顺序模式）、
[REPOSITORY-NODE-PATH.md](REPOSITORY-NODE-PATH.md)（NewPath 身份）、
[STORAGE-PATH-VALIDATION.md](STORAGE-PATH-VALIDATION.md)（校验器）、
[FILE-METADATA.md](FILE-METADATA.md)（快照透传）、
[NODE-REFERENCES.md](NODE-REFERENCES.md)（FileReference）、
[FILE-NODE-SAVE-CONTRACT.md](FILE-NODE-SAVE-CONTRACT.md)（上层保存契约）。

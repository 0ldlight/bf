// Package repository 普通文件创建核心：按批准顺序编排 取消检查 → 依赖
// 检查 → 本地画像拒绝 → force-file → 存储路径校验 → 取 ID → 映射 →
// 插入。事务归调用方：本层不开事务、不提交、不回滚、不重试、不做
// upsert/回读/补偿；成功仅是调用方事务内的临时成功。
// Package repository — the ordinary file-create core: orchestrates the
// approved order cancellation-check → dependency-check → local-profile
// rejections → force-file → storage-path validation → id allocation →
// mapping → insert. Transactions stay with the caller: this layer never
// begins/commits/rolls back, never retries, and does no upsert/readback/
// compensation; success is provisional inside the caller transaction.
package repository

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/0ldlight/bf/internal/content"
)

// 阶段文案固定且不含输入字节；cause 经 Unwrap 保留 errors.Is/As 链。
// Stage texts are fixed and input-free; causes survive through Unwrap for
// errors.Is/As chains.
const (
	stageCreateCancelled      = "repository: create file cancelled"
	stageCreateInvalidInput   = "repository: create file invalid input path"
	stageCreateInvalidStorage = "repository: create file invalid storage path"
	stageCreateAllocate       = "repository: create file allocate node id"
	stageCreateBadAllocatedID = "repository: create file invalid allocated id"
	stageCreateMap            = "repository: create file map node path"
	stageCreateInsert         = "repository: create file insert"
	stageCreateBadCount       = "repository: create file unexpected insert count"
)

// ErrInvalidAllocatedID 是 nextID 返回非正数时的固定哨兵错误。
// ErrInvalidAllocatedID is the fixed sentinel for a nonpositive id from
// nextID.
var ErrInvalidAllocatedID = errors.New("repository: invalid allocated node id")

// ErrUnexpectedCreateCount 是 insert 计数不等于 1 时的固定哨兵错误。
// ErrUnexpectedCreateCount is the fixed sentinel for an insert count other
// than one.
var ErrUnexpectedCreateCount = errors.New("repository: unexpected create insert count")

// errNilCreateDependency 是 nil 依赖回调的固定配置错误。
// errNilCreateDependency is the fixed configuration error for nil
// dependency callbacks.
var errNilCreateDependency = errors.New("repository: create file requires nonnil id allocation and insert callbacks")

// createOpError 沿用 Store 的 op/cause 模式：Error() 只显示固定阶段文案，
// Unwrap 保留底层原因。
// createOpError follows the Store op/cause pattern: Error() shows only the
// fixed stage text and Unwrap keeps the underlying cause.
type createOpError struct {
	op    string
	cause error
}

func (e *createOpError) Error() string { return e.op }

func (e *createOpError) Unwrap() error { return e.cause }

// FileCreateInput 是普通文件创建的调用方输入：repo/path 原始字符串、
// 内容元数据按值传递、文件 metadata 指针原样透传（nil 即七列 NULL，不
// 做零值替换，也不重新归一化时间/actor/original）。
// FileCreateInput is the caller input for ordinary file creation: raw
// repo/path strings, content metadata by value, and the file metadata
// pointer passed through verbatim (nil stays the seven NULL columns, no
// zero-value substitution, no re-normalization of times/actors/originals).
type FileCreateInput struct {
	Repo     string
	Path     string
	Content  content.Metadata
	Metadata *FileMetadata
}

// CreateFile 按批准顺序执行普通创建：
//   - ctx 非空由调用方保证；预取消 → (0, 错误) 且零依赖调用；
//   - nil nextID/insert 是固定配置错误，不调用任何依赖；
//   - 本地画像在取 ID 之前拒绝：非法 UTF-8、构造后空 repo、原始
//     RepoPath.getName 的 NUL 拒绝（构造 trim 先于 NUL 判定，边界 NUL
//     被吸收；原始 path 与 force-file 后的 path 可以不同）；
//   - 校验器看到的是 force-file 后的 file RepoPath.toPath；
//   - nextID 恰好一次：错误优先于返回的部分 ID；非正 ID 以
//     ErrInvalidAllocatedID 拒绝且不重取；
//   - 取消检查点丢 ID 不返还；映射经真实 helper 语义与既有 NewPath，
//     空名字（metadata 剥离后）在取 ID 之后才拒绝，留下 ID 空洞；
//   - insert 恰好一次：错误优先于计数，计数必须严格为 1；成功后无追
//     加取消检查，返回的 ID 仅在调用方事务内临时成立。
//
// canonical 三摘要、根路径、Depth 与 SQL 边界留在旧 Store（insert 内），
// 位于取 ID 之后。CreateFile 不拥有事务。
//
// CreateFile performs ordinary creation in the approved order:
//   - a nonnil ctx is the caller's prerequisite; pre-cancellation yields
//     (0, error) with zero dependency calls;
//   - nil nextID/insert is a fixed configuration error calling nothing;
//   - the local profile rejects before id allocation: invalid UTF-8, a
//     repo blank after construction trim, and the original
//     RepoPath.getName NUL rejection (construction trim precedes the NUL
//     verdict so boundary NULs are absorbed; the original path and the
//     forced path may differ);
//   - the validator sees the forced file RepoPath.toPath;
//   - nextID exactly once: an error beats any partially returned id; a
//     nonpositive id rejects with ErrInvalidAllocatedID without re-fetch;
//   - cancellation checkpoints drop the id without returning it; mapping
//     goes through the real helper semantics and the existing NewPath,
//     where an empty (post-strip) name rejects only after the id was
//     allocated, leaving an id hole;
//   - insert exactly once: an error beats the count and the count must be
//     strictly one; no cancellation check follows success, so the returned
//     id stays provisional inside the caller transaction.
//
// The canonical three-digest, root-path, Depth and SQL boundaries stay
// with the legacy Store (inside insert), after id allocation. CreateFile
// owns no transaction.
func CreateFile(ctx context.Context, in FileCreateInput, nextID func(context.Context) (int64, error), insert func(context.Context, FileReference) (int64, error)) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, &createOpError{op: stageCreateCancelled, cause: err}
	}
	if nextID == nil || insert == nil {
		return 0, errNilCreateDependency
	}
	if !utf8.ValidString(in.Repo) || !utf8.ValidString(in.Path) {
		return 0, &createOpError{op: stageCreateInvalidInput, cause: ErrInvalidPath}
	}
	raw, ok := newRawRepoPath(in.Repo, in.Path)
	if !ok {
		return 0, &createOpError{op: stageCreateInvalidInput, cause: ErrInvalidPath}
	}
	// 源码顺序：先 force-file，再对构造后的原始 path 做 getName 预检。
	// Source order: force-file first, then the getName precheck over the
	// constructed (original) path.
	forced := raw.forceFile()
	if _, ok := repoPathGetName(raw.path); !ok {
		return 0, &createOpError{op: stageCreateInvalidInput, cause: ErrInvalidPath}
	}
	// forced 恒为 file：toPath = repoKey+"/"+path（根 file 为 "repo/"）。
	// forced is always a file: toPath = repoKey+"/"+path (root file gives
	// "repo/").
	if err := ValidateStoragePath(forced.repoKey + "/" + forced.path); err != nil {
		return 0, &createOpError{op: stageCreateInvalidStorage, cause: err}
	}
	if err := ctx.Err(); err != nil {
		return 0, &createOpError{op: stageCreateCancelled, cause: err}
	}
	id, err := nextID(ctx)
	if err != nil {
		return 0, &createOpError{op: stageCreateAllocate, cause: err}
	}
	if id <= 0 {
		return 0, &createOpError{op: stageCreateBadAllocatedID, cause: ErrInvalidAllocatedID}
	}
	if err := ctx.Err(); err != nil {
		return 0, &createOpError{op: stageCreateCancelled, cause: err}
	}
	// 取 ID 后的映射：forced.path 已通过预检，此处的 NUL 分支不可达，
	// 仅作防御保底。
	// Post-allocation mapping: forced.path already passed the precheck, so
	// the NUL branch here is unreachable and kept only as a defensive
	// fallback.
	name, ok := repoPathGetName(forced.path)
	if !ok {
		return 0, &createOpError{op: stageCreateMap, cause: ErrInvalidPath}
	}
	nodePath, err := NewPath(forced.repoKey, pathUtilsGetParent(forced.path), name, true)
	if err != nil {
		return 0, &createOpError{op: stageCreateMap, cause: err}
	}
	if err := ctx.Err(); err != nil {
		return 0, &createOpError{op: stageCreateCancelled, cause: err}
	}
	ref := FileReference{ID: id, Path: nodePath, Content: in.Content, Metadata: in.Metadata}
	count, err := insert(ctx, ref)
	if err != nil {
		return 0, &createOpError{op: stageCreateInsert, cause: err}
	}
	if count != 1 {
		return 0, &createOpError{op: stageCreateBadCount, cause: ErrUnexpectedCreateCount}
	}
	return id, nil
}

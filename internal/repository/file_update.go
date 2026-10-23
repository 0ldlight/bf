// Package repository 普通文件更新核心：按批准顺序编排 取消检查 → nil
// 委托 → nil 快照 → 本地画像拒绝 → force-file → 存储路径校验 → 取消
// 检查 → 正 ID 守卫 → 映射 → 委托前取消检查 → 委托一次。事务归调用方：
// 本层不开事务、不提交、不回滚、不重试、不做查找/分配/差分；成功计数
// 仅是调用方事务内的临时结果。契约详见 docs/FILE-UPDATE.md。
// Package repository — the ordinary file-update core: orchestrates the
// approved order cancellation-check → nil callback → nil snapshot →
// local-profile rejections → force-file → storage-path validation →
// cancellation-check → positive-ID guard → mapping → pre-delegate
// cancellation-check → one delegate call. Transactions stay with the
// caller: this layer never begins/commits/rolls back, never retries and
// does no lookup/allocation/diff; a successful count is provisional inside
// the caller transaction. See docs/FILE-UPDATE.md for the contract.
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
	stageUpdateCancelled      = "repository: update file cancelled"
	stageUpdateInvalidInput   = "repository: update file invalid input path"
	stageUpdateInvalidStorage = "repository: update file invalid storage path"
	stageUpdateInvalidID      = "repository: update file invalid id"
	stageUpdateMap            = "repository: update file map node path"
	stageUpdateDelegate       = "repository: update file delegate"
)

// ErrInvalidUpdateID 是输入 ID 非正数时的固定哨兵错误（显式本地守卫，
// 非目标行为；目标包装层 id==-1 是"新建"语义，服务缝无此检查）。
// ErrInvalidUpdateID is the fixed sentinel for a nonpositive input ID —
// an explicit local guard, not target behavior (the target wrapper layer
// treats id==-1 as create-new and the service seam has no such check).
var ErrInvalidUpdateID = errors.New("repository: invalid update id")

// errNilUpdateDependency 是 nil 委托回调的固定配置错误。
// errNilUpdateDependency is the fixed configuration error for a nil
// update callback.
var errNilUpdateDependency = errors.New("repository: update file requires nonnil update callback")

// errMissingUpdateSnapshot 是 nil Metadata 的固定契约错误：本 API 只收
// 显式快照（零快照合法），nil=保留七列的旧 Store 本地策略不进入本消费者。
// errMissingUpdateSnapshot is the fixed contract error for nil Metadata:
// this API accepts only an explicit snapshot (a zero snapshot is valid);
// the legacy Store's nil=leave-seven-columns-untouched local policy stays
// out of this consumer.
var errMissingUpdateSnapshot = errors.New("repository: update file requires nonnil metadata snapshot")

// updateOpError 沿用 Store/create 的 op/cause 模式：Error() 只显固定阶段
// 文案，Unwrap 保留底层原因；不做通用抽象重构，不动 createOpError。
// updateOpError follows the Store/create op/cause pattern: Error() shows
// only the fixed stage text and Unwrap keeps the underlying cause; no
// general error refactor, createOpError stays untouched.
type updateOpError struct {
	op    string
	cause error
}

func (e *updateOpError) Error() string { return e.op }

func (e *updateOpError) Unwrap() error { return e.cause }

// FileUpdateInput 是普通文件更新的调用方输入：既有正 ID 原样传递（无分
// 配、无查找、无 -1→新建回退）；Repo/Path 是调用方携带的快照路径（身份
// SET 能力，非 move/rename 端到端承诺）；Content 按值、Metadata 指针原样
// 透传，不做时钟/actor/original 重归一化。
// FileUpdateInput is the caller input for ordinary file updates: an
// existing positive ID passed verbatim (no allocation, no lookup, no
// -1→create fallback); Repo/Path carry the caller's snapshot path (an
// identity SET capability, not an end-to-end move/rename promise); Content
// passes by value and Metadata by pointer verbatim with no
// clock/actor/original re-normalization.
type FileUpdateInput struct {
	ID       int64
	Repo     string
	Path     string
	Content  content.Metadata
	Metadata *FileMetadata
}

// UpdateFile 按批准顺序执行普通更新：
//   - ctx 非空由调用方保证；预取消 → (0, 错误) 且零依赖调用；nil 委托先
//     于 nil Metadata 检查；
//   - 本地画像在存储路径校验之前拒绝：非法 UTF-8、两参构造器拒绝、构造
//     后原始 path 的 getName NUL 预检（force-file 先于该预检；构造 trim
//     先于 NUL 判定）；
//   - 校验器看到 force-file 后的 file toPath；校验先于正 ID 守卫；
//   - 正 ID 守卫（ErrInvalidUpdateID）先于共享 helper + NewPath 映射；
//   - 委托恰好一次：错误优先返回 (0, 包裹错误)，否则计数原样返回（含 0
//     与非 Store 委托的 >1，不强制为 1）；成功后无追加取消检查。
//
// 内容 canonical 三摘要、size、根路径与 Depth 校验留在旧 Store（委托
// 内）。UpdateFile 不拥有事务。
//
// UpdateFile performs the ordinary update in the approved order:
//   - a nonnil ctx is the caller's prerequisite; pre-cancellation yields
//     (0, error) with zero dependency calls; the nil-callback check
//     precedes the nil-snapshot check;
//   - the local profile rejects before storage-path validation: invalid
//     UTF-8, a two-argument constructor rejection and the getName NUL
//     precheck over the constructed original path (force-file precedes
//     that precheck; the construction trim precedes the NUL verdict);
//   - the validator sees the forced file toPath; validation precedes the
//     positive-ID guard;
//   - the positive-ID guard (ErrInvalidUpdateID) precedes mapping through
//     the shared helpers plus NewPath;
//   - the delegate runs exactly once: an error yields (0, wrapped error)
//     first, otherwise the count returns verbatim (0 included, and >1
//     from a non-Store delegate, never forced to one); no cancellation
//     check is appended after success.
//
// The canonical three-digest, size, root-path and Depth validations stay
// with the legacy Store (inside the delegate). UpdateFile owns no
// transaction.
func UpdateFile(ctx context.Context, in FileUpdateInput, update func(context.Context, FileReference) (int64, error)) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, &updateOpError{op: stageUpdateCancelled, cause: err}
	}
	if update == nil {
		return 0, errNilUpdateDependency
	}
	if in.Metadata == nil {
		return 0, errMissingUpdateSnapshot
	}
	if !utf8.ValidString(in.Repo) || !utf8.ValidString(in.Path) {
		return 0, &updateOpError{op: stageUpdateInvalidInput, cause: ErrInvalidPath}
	}
	raw, ok := newRawRepoPath(in.Repo, in.Path)
	if !ok {
		return 0, &updateOpError{op: stageUpdateInvalidInput, cause: ErrInvalidPath}
	}
	// 源码顺序：先 force-file，再对构造后的原始 path 做 getName 预检。
	// Source order: force-file first, then the getName precheck over the
	// constructed (original) path.
	forced := raw.forceFile()
	if _, ok := repoPathGetName(raw.path); !ok {
		return 0, &updateOpError{op: stageUpdateInvalidInput, cause: ErrInvalidPath}
	}
	// forced 恒为 file：toPath = repoKey+"/"+path（根 file 为 "repo/"）。
	// forced is always a file: toPath = repoKey+"/"+path (a root file gives
	// "repo/").
	if err := ValidateStoragePath(forced.repoKey + "/" + forced.path); err != nil {
		return 0, &updateOpError{op: stageUpdateInvalidStorage, cause: err}
	}
	if err := ctx.Err(); err != nil {
		return 0, &updateOpError{op: stageUpdateCancelled, cause: err}
	}
	if in.ID <= 0 {
		return 0, &updateOpError{op: stageUpdateInvalidID, cause: ErrInvalidUpdateID}
	}
	// 校验后的映射：forced.path 已通过预检，此处的 NUL 分支不可达，仅作
	// 防御保底。
	// Post-validation mapping: forced.path already passed the precheck, so
	// the NUL branch here is unreachable and kept only as a defensive
	// fallback.
	name, ok := repoPathGetName(forced.path)
	if !ok {
		return 0, &updateOpError{op: stageUpdateMap, cause: ErrInvalidPath}
	}
	nodePath, err := NewPath(forced.repoKey, pathUtilsGetParent(forced.path), name, true)
	if err != nil {
		return 0, &updateOpError{op: stageUpdateMap, cause: err}
	}
	if err := ctx.Err(); err != nil {
		return 0, &updateOpError{op: stageUpdateCancelled, cause: err}
	}
	ref := FileReference{ID: in.ID, Path: nodePath, Content: in.Content, Metadata: in.Metadata}
	count, err := update(ctx, ref)
	if err != nil {
		return 0, &updateOpError{op: stageUpdateDelegate, cause: err}
	}
	return count, nil
}

package repository

import (
	"context"
	"errors"
)

// LoadedFileCreatedPolicy 显式选择复制与比较的有效 created 策略。
// LoadedFileCreatedPolicy explicitly selects the effective copy/comparison policy.
type LoadedFileCreatedPolicy uint8

// PreserveLoadedFileCreated 仅支持普通已加载文件且全程 update-created=false。
// 零值无效；这不是部署配置默认值的推断。
// PreserveLoadedFileCreated selects ordinary loaded files with stable
// update-created=false throughout the operation. Zero is invalid; this does
// not infer a deployed configuration from its registered default.
const PreserveLoadedFileCreated LoadedFileCreatedPolicy = 1

// ErrUnsupportedLoadedFileCreatedPolicy 拒绝零值和未支持的策略，不含输入。
// ErrUnsupportedLoadedFileCreatedPolicy rejects zero/unsupported policies
// with a fixed message containing no input.
var ErrUnsupportedLoadedFileCreatedPolicy = errors.New("repository: unsupported loaded file created policy")

// LoadedFileUpdateInput 由 Source 提供 ID/基线，Desired 保持原始变更后状态。
// 调用方保证 Source 新鲜度及 Desired 与该行的关联；本层不重新读取或 CAS。
// LoadedFileUpdateInput derives ID/baseline from Source and preserves raw
// post-mutation Desired. Callers own Source freshness and its association
// with Desired; this layer supplies neither re-reading nor CAS.
type LoadedFileUpdateInput struct {
	Source        FileSnapshotSource
	Desired       FileInfoSnapshot
	CreatedPolicy LoadedFileCreatedPolicy
}

// UpdateLoadedFileIfChanged 在稳定保留 created 画像中装配加载基线并立即
// 消费现有更新门。加载侧只归一化一次 Original；只物化加载侧 MIME，随后
// 校验其存储路径，才允许比较跳过。该画像中三次成功复制的比较字段等价，
// 不需要时钟输入。Desired 不进行物化或修复，写参数仍只从 Desired 派生。
// 顺序为取消、委托、正 ID、策略、加载、MIME、源路径、既有门（含再次取消）。
// ctx 必须非 nil；错误零结果。无事务/锁/属性/关注/复制重置生命周期，亦不
// 承诺 Java 失败路径的源缓存副作用。详见 docs/LOADED-FILE-UPDATE.md。
// UpdateLoadedFileIfChanged assembles a loaded baseline in the stable
// preserve-created profile and immediately consumes the existing gate.
// Original is normalized only at load; only loaded MIME is materialized,
// then its storage path is validated before any equality skip. Successful
// copies have equivalent comparison fields in this profile; no clock input
// is needed. Desired is never materialized/repaired and alone supplies writes.
// Order: cancellation, callback, positive ID, policy, load, MIME, source path,
// existing gate (including its cancellation recheck). Context must be nonnil;
// errors return zero. Transactions, locks, properties/watches, copy resets and
// Java failure-side source-cache effects are outside this one-shot seam.
func UpdateLoadedFileIfChanged(ctx context.Context, in LoadedFileUpdateInput, catalog FileMimeCatalog, update func(context.Context, FileReference) (int64, error)) (FileDirtyUpdateResult, error) {
	baseline, err := prepareLoadedFileUpdate(ctx, in.Source, in.CreatedPolicy, catalog, update)
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	return UpdateFileIfChanged(ctx, FileDirtyUpdateInput{
		Existing: baseline, Desired: in.Desired, IgnoreCreated: false, ID: in.Source.Reference.ID,
	}, update)
}

// 两个实际消费者共用同一准备顺序；不公开独立基线出口。
// Both consumers share the same ordered preparation, with no public baseline API.
func prepareLoadedFileUpdate(ctx context.Context, source FileSnapshotSource, policy LoadedFileCreatedPolicy, catalog FileMimeCatalog, update func(context.Context, FileReference) (int64, error)) (FileInfoSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return FileInfoSnapshot{}, &updateOpError{op: "repository: loaded file update cancelled", cause: err}
	}
	if update == nil {
		return FileInfoSnapshot{}, errNilDirtyUpdateDependency
	}
	if source.Reference.ID <= 0 {
		return FileInfoSnapshot{}, &updateOpError{op: "repository: loaded file update invalid id", cause: ErrInvalidUpdateID}
	}
	if policy != PreserveLoadedFileCreated {
		return FileInfoSnapshot{}, ErrUnsupportedLoadedFileCreatedPolicy
	}
	loaded, err := LoadedFileSnapshot(source)
	if err != nil {
		return FileInfoSnapshot{}, err
	}
	baseline, err := MaterializeFileMime(loaded, catalog)
	if err != nil {
		return FileInfoSnapshot{}, err
	}
	repo, err := fileDirtyUpdateString(baseline.repoKey)
	if err != nil {
		return FileInfoSnapshot{}, err
	}
	path, err := fileDirtyUpdateString(baseline.repoPath)
	if err != nil {
		return FileInfoSnapshot{}, err
	}
	if err := ValidateStoragePath(repo + "/" + path); err != nil {
		return FileInfoSnapshot{}, err
	}
	return baseline, nil
}

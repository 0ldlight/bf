package repository

import (
	"context"
	"errors"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
)

// ErrInvalidPublishedFileMetadata 拒绝本地 canonical 三摘要/非负 size 画像外
// 的 Published；固定文本不包含输入，也不认证 binary 发布或元组来源。
// ErrInvalidPublishedFileMetadata rejects Published outside the local
// canonical-digest/nonnegative-size profile. It neither echoes input nor
// authenticates binary publication or tuple provenance.
var ErrInvalidPublishedFileMetadata = errors.New("repository: invalid published file metadata")

// LoadedFileBinaryInput 要求 Published 来自真实 binary 服务的已发布 winner。
// 调用方仍负责其来源、Source 新鲜度/行关联及事务。
// LoadedFileBinaryInput requires an actually published binary-service winner.
// Callers retain provenance, Source freshness/row association and transactions.
type LoadedFileBinaryInput struct {
	Source        FileSnapshotSource
	Published     content.Metadata
	CreatedPolicy LoadedFileCreatedPolicy
	OriginalCase  LoadedOriginalCase
}

// UpdateLoadedFileBinary 内部生成 Desired，只替换 size/三 Actual，再消费
// 既有节点更新门。先按 PR49 顺序准备基线，再验证 Published，任何相同跳过
// 之前均完成检查。当前成功 loaded 域的 raw Original 已使目标验证型构造器
// 规范化成为恒等变换，故保留它（含 NO_ORIG），不替为 Actual 或 null。
// 直接 binary 赋值不重验旧 Actual，不修改身份、MIME、时间或 actors；不能
// 代替普通上传的 client setters、policy/auth/盖章/通知或生命周期。
// ctx 必须非 nil；错误零结果。详见 docs/LOADED-FILE-BINARY.md。
// UpdateLoadedFileBinary builds Desired internally, replacing only size and
// three Actuals before consuming the existing node-update gate. Prepare the
// baseline in PR49 order, then validate Published before any equality skip.
// In the successful loaded domain, the target validating constructor leaves
// raw Originals unchanged, including NO_ORIG; never substitute Actual/null.
// Direct assignment neither revalidates old Actuals nor stamps identity,
// MIME, times or actors. It does not replace upload client setters, policy,
// auth, stamping, notification or lifecycle. Context must be nonnil; errors
// return zero.
func UpdateLoadedFileBinary(ctx context.Context, in LoadedFileBinaryInput, catalog FileMimeCatalog, update func(context.Context, FileReference) (int64, error)) (FileDirtyUpdateResult, error) {
	baseline, err := prepareLoadedFileUpdate(ctx, in.Source, in.CreatedPolicy, in.OriginalCase, catalog, update)
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	if !validPublishedFileMetadata(in.Published) {
		return FileDirtyUpdateResult{}, ErrInvalidPublishedFileMetadata
	}
	fields := publishedBinaryFields(baseline, in.Published)
	desired, err := NewFileInfoSnapshot(fields)
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	return UpdateFileIfChanged(ctx, FileDirtyUpdateInput{
		Existing: baseline, Desired: desired, IgnoreCreated: false, ID: in.Source.Reference.ID,
	}, update)
}

func validPublishedFileMetadata(meta content.Metadata) bool {
	return publishedFileHex(meta.SHA1, 40) && publishedFileHex(meta.SHA256, 64) &&
		publishedFileHex(meta.MD5, 32) && meta.Size >= 0
}

// 两个实际消费者共享内容字段赋值，不修改 Original 或其他原始字段。
// Both consumers share content assignment, preserving Originals and other fields.
func publishedBinaryFields(baseline FileInfoSnapshot, published content.Metadata) FileInfoSnapshotInput {
	fields := baseline.Fields()
	fields.SizeBytes = published.Size
	for i := range fields.Checksums {
		var actual string
		switch fields.Checksums[i].Kind {
		case ChecksumSHA1:
			actual = published.SHA1
		case ChecksumSHA256:
			actual = published.SHA256
		case ChecksumMD5:
			actual = published.MD5
		}
		fields.Checksums[i].Actual = utf16.Encode([]rune(actual))
	}
	return fields
}

func publishedFileHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

package repository

import (
	"context"
	"errors"
)

// ClientFileChecksums 是资源对象已解码的三种原始 Original（nullable UTF-16）。
// nil 表示缺条目或 null Original；空串独立保留。不是原始 HTTP header，
// 不认证 NO_ORIG marker 的来源或授权。
// ClientFileChecksums carries decoded resource Originals as nullable UTF-16.
// Nil covers absent entries/null Original, preserving empty separately.
// These are not raw HTTP headers or authentication of a trusted marker.
type ClientFileChecksums struct {
	SHA1Original   []uint16
	SHA256Original []uint16
	MD5Original    []uint16
}

// ErrClientChecksumMismatch 是 CLIENT 策略拒绝的固定错误，不回显摘要。
// ErrClientChecksumMismatch is the fixed CLIENT-policy rejection, without digests.
var ErrClientChecksumMismatch = errors.New("repository: client checksum mismatch")

// UpdateLocalFileWithClientChecksums 沿用既有准备/旧 Actual/Published/policy
// 顺序，内部规范化资源 Original 并对新实际摘要执行三种本地策略比较。
// CLIENT mismatch 在盖章和节点写之前拒绝；SERVER 保留 mismatch 值。
// NO_ORIG 原样保存，仅其策略视图使用 Actual；不是 HTTP 信任边界。
// 用户保持 raw UTF-16，直到写投影才缩写。支持域和生命周期限制见
// docs/LOCAL-FILE-CLIENT-CHECKSUMS.md。ctx 必须非 nil；错误零结果。
// UpdateLocalFileWithClientChecksums retains preparation/old-Actual/Published/
// policy order, normalizes resource Originals and verifies all three against
// new Actuals. CLIENT mismatch fails before stamping/node writes; SERVER
// preserves mismatches. Store NO_ORIG raw, using Actual only in its policy
// view; this is no HTTP trust boundary. User stays raw until write projection.
// Context must be nonnil; errors return zero. See the contract for scope.
func UpdateLocalFileWithClientChecksums(ctx context.Context, in LocalFileWriteInput, checksums ClientFileChecksums, catalog FileMimeCatalog, update func(context.Context, FileReference) (int64, error)) (FileDirtyUpdateResult, error) {
	return updateLocalFileWithClientChecksums(ctx, in, checksums, catalog, update, func() int64 { return in.UpdatedMillis })
}

// updatedMillis 只在摘要验证和 modified 回退后采样；公开入口仍使用显式已捕获时间。
// updatedMillis is sampled only after checksum verification and modified fallback.
// The public entry point retains its previously captured timestamp semantics.
func updateLocalFileWithClientChecksums(ctx context.Context, in LocalFileWriteInput, checksums ClientFileChecksums, catalog FileMimeCatalog, update func(context.Context, FileReference) (int64, error), updatedMillis func() int64) (FileDirtyUpdateResult, error) {
	baseline, err := prepareLoadedFileUpdate(ctx, in.Source, in.CreatedPolicy, in.OriginalCase, catalog, update)
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	for _, slot := range [...]struct {
		kind   ChecksumKind
		length int
	}{{ChecksumSHA1, 40}, {ChecksumSHA256, 64}, {ChecksumMD5, 32}} {
		if actual := baseline.checksums[slot.kind].actual; actual != nil && !loadedActualHex(actual, slot.length) {
			return FileDirtyUpdateResult{}, ErrInvalidLoadedActual
		}
	}
	if !validPublishedFileMetadata(in.Published) {
		return FileDirtyUpdateResult{}, ErrInvalidPublishedFileMetadata
	}
	if in.ChecksumPolicy != LocalFileChecksumClient && in.ChecksumPolicy != LocalFileChecksumServer {
		return FileDirtyUpdateResult{}, ErrUnsupportedLocalFileChecksumPolicy
	}
	fields := publishedBinaryFields(baseline, in.Published)
	for i := range fields.Checksums {
		switch fields.Checksums[i].Kind {
		case ChecksumSHA1:
			fields.Checksums[i].Original = normalizeClientOriginal(checksums.SHA1Original, 40)
		case ChecksumSHA256:
			fields.Checksums[i].Original = normalizeClientOriginal(checksums.SHA256Original, 64)
		case ChecksumMD5:
			fields.Checksums[i].Original = normalizeClientOriginal(checksums.MD5Original, 32)
		}
	}
	if !localClientChecksumsMatch(fields.Checksums) && in.ChecksumPolicy == LocalFileChecksumClient {
		return FileDirtyUpdateResult{}, ErrClientChecksumMismatch
	}
	fields.LastModifiedMillis = in.ResourceModifiedMillis
	if fields.LastModifiedMillis <= 0 {
		fields.LastModifiedMillis = fields.CreatedMillis
	}
	fields.LastUpdatedMillis = updatedMillis()
	fields.ModifiedByUTF16 = in.CurrentUserUTF16
	desired, err := NewFileInfoSnapshot(fields)
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	return UpdateFileIfChanged(ctx, FileDirtyUpdateInput{
		Existing: baseline, Desired: desired, IgnoreCreated: false, ID: in.Source.Reference.ID,
	}, update)
}

func normalizeClientOriginal(original []uint16, length int) []uint16 {
	units := fileSnapshotCopyUnits(original)
	if !loadedActualHex(units, length) {
		return units
	}
	for i, unit := range units {
		if unit >= 'A' && unit <= 'F' {
			units[i] = unit + ('a' - 'A')
		}
	}
	return units
}

func localClientChecksumsMatch(checksums []ChecksumEntryInput) bool {
	for _, checksum := range checksums {
		if fileSnapshotEqualUnits(checksum.Original, []uint16{'N', 'O', '_', 'O', 'R', 'I', 'G'}) {
			continue
		}
		for _, unit := range checksum.Original {
			// StringUtils 的 char 扫描，不合并 UTF-16 代理，也不做 trim。
			// Match the char-based scan: no surrogate joining or trimming.
			if !javaBlankRune(rune(unit)) {
				if !fileSnapshotEqualUnits(checksum.Original, checksum.Actual) {
					return false
				}
				break
			}
		}
	}
	return true
}

package repository

import (
	"context"
	"errors"

	"github.com/0ldlight/bf/internal/content"
)

// LocalFileChecksumPolicy 显式选择已配置的非 nil 本地策略，不读取部署默认。
// LocalFileChecksumPolicy selects a configured nonnil local policy explicitly,
// without reading a deployment default.
type LocalFileChecksumPolicy uint8

const (
	LocalFileChecksumClient LocalFileChecksumPolicy = iota + 1
	LocalFileChecksumServer
)

// ErrUnsupportedLocalFileChecksumPolicy 拒绝零值、remote/custom 等未支持策略。
// ErrUnsupportedLocalFileChecksumPolicy rejects zero and unsupported policies.
var ErrUnsupportedLocalFileChecksumPolicy = errors.New("repository: unsupported local file checksum policy")

// ErrInvalidLoadedActual 表示 client setter 前置校验拒绝旧的非 null Actual。
// ErrInvalidLoadedActual marks old nonnull Actual rejected by client-setter validation.
var ErrInvalidLoadedActual = errors.New("repository: invalid loaded actual")

// LocalFileNoClientInput 表示已有本地文件、无客户端摘要、无管理员覆盖的
// 选定路线。时间与原始 UTF-16 用户为调用方已捕获事实，不产生 HTTP/权限
// 或当前用户默认值。Source、Published 与事务责任沿用既有消费者。
// LocalFileNoClientInput selects an existing local file with no client
// checksums/admin overrides. Times and raw UTF-16 user are captured caller
// facts, not HTTP/auth/current-user defaults. Existing provenance and
// transaction responsibilities remain with the caller.
type LocalFileNoClientInput struct {
	Source                 FileSnapshotSource
	Published              content.Metadata
	ResourceModifiedMillis int64
	UpdatedMillis          int64
	CurrentUserUTF16       []uint16
	CreatedPolicy          LoadedFileCreatedPolicy
	ChecksumPolicy         LocalFileChecksumPolicy
}

// UpdateLocalFileWithoutClientChecksums 按既有准备、旧 Actual、Published、
// 显式 local policy 的顺序校验，再清空三 Original 并设置内容/元数据事实。
// 全 null Original 在真实 CLIENT/SERVER verify 中恒通过，因此无需不透明
// verifier 或占位成功函数。fresh 加载的 SHA256 NO_ORIG 也被清空；相同字节
// 仍走 dirty 更新。保留 created/by、身份与 MIME，原始用户只在既有写投影
// 时缩写。非 nil ctx 为前提；错误零结果，不模拟 Java 部分变更副作用。
// 不拥有拦截器、Jar、权限、属性/stats、通知/锁/会话或完整上传生命周期。
// UpdateLocalFileWithoutClientChecksums validates preparation, old Actuals,
// Published, then the explicit local policy. It clears three Originals and
// applies content/metadata facts. Real CLIENT/SERVER verification always
// accepts all-null Originals in this profile, requiring no opaque verifier
// or placeholder success function. Clearing loaded SHA256 NO_ORIG makes even
// identical bytes dirty. Created/by, identity and MIME remain intact; raw
// user abbreviation stays in the existing write projection. Context must be
// nonnil; errors return zero without Java partial-mutation effects. Full
// upload/interceptor/Jar/auth/properties/stats/notification/lock/session scope
// is excluded. See docs/LOCAL-FILE-NO-CLIENT.md.
func UpdateLocalFileWithoutClientChecksums(ctx context.Context, in LocalFileNoClientInput, catalog FileMimeCatalog, update func(context.Context, FileReference) (int64, error)) (FileDirtyUpdateResult, error) {
	baseline, err := prepareLoadedFileUpdate(ctx, in.Source, in.CreatedPolicy, catalog, update)
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
		fields.Checksums[i].Original = nil
	}
	fields.LastModifiedMillis = in.ResourceModifiedMillis
	if fields.LastModifiedMillis <= 0 {
		fields.LastModifiedMillis = fields.CreatedMillis
	}
	fields.LastUpdatedMillis = in.UpdatedMillis
	fields.ModifiedByUTF16 = in.CurrentUserUTF16
	desired, err := NewFileInfoSnapshot(fields)
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	return UpdateFileIfChanged(ctx, FileDirtyUpdateInput{
		Existing: baseline, Desired: desired, IgnoreCreated: false, ID: in.Source.Reference.ID,
	}, update)
}

func loadedActualHex(actual []uint16, length int) bool {
	if len(actual) != length {
		return false
	}
	for _, unit := range actual {
		if !(unit >= '0' && unit <= '9' || unit >= 'a' && unit <= 'f' || unit >= 'A' && unit <= 'F') {
			return false
		}
	}
	return true
}

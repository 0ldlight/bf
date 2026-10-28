package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/0ldlight/bf/internal/content"
)

// LocalFileSaveStore 将查询与写入绑定到调用方持有的真实事务，缺失保留 sql.ErrNoRows。
// LocalFileSaveStore binds the command to an initialized, caller-owned transaction.
// LookupSnapshotSource preserves errors.Is(sql.ErrNoRows) for missing nodes.
type LocalFileSaveStore interface {
	LookupSnapshotSource(context.Context, Path) (FileSnapshotSource, error)
	Create(context.Context, FileReference) (int64, error)
	Update(context.Context, FileReference) (int64, error)
}

// FileSaveClock 按阶段采集毫秒，允许非正、相等或倒退。
// FileSaveClock supplies actual millisecond captures at the consumer's stages.
// Values need not be positive, distinct, or monotonic.
type FileSaveClock interface {
	NowMillis() int64
}

// SystemFileSaveClock 每次读取实际系统墙钟。
// SystemFileSaveClock captures the host wall clock when each stage requests it.
type SystemFileSaveClock struct{}

func (SystemFileSaveClock) NowMillis() int64 { return time.Now().UnixMilli() }

// LocalFileSaveDependencies 在命中时也要求真实初始化的 Store、NextID、Clock；
// Catalog 仅用于加载态。独立提交的 ID 不在本命令内重试或管理事务。
// LocalFileSaveDependencies requires initialized Store, NextID, and Clock even
// for an existing node. Typed nil interface implementations are caller-invalid.
// Catalog is consumed only for loaded nodes. NextID returns one committed ID;
// this command does not own its transaction or retry allocation conflicts.
type LocalFileSaveDependencies struct {
	Store   LocalFileSaveStore
	NextID  func(context.Context) (int64, error)
	Clock   FileSaveClock
	Catalog FileMimeCatalog
}

// LocalFileSaveInput 保留资源原始事实与已发布内容，用户及发布来源由上游保证；
// CreatedPolicy/OriginalCase 只约束已加载节点。
// LocalFileSaveInput carries raw resource facts and already published content.
// The caller authenticates CurrentUserUTF16 and owns published-byte provenance.
// CreatedPolicy and OriginalCase select only the loaded-node support profile.
type LocalFileSaveInput struct {
	Repo                   string
	Path                   string
	Published              content.Metadata
	Checksums              ClientFileChecksums
	ResourceCreatedMillis  int64
	ResourceModifiedMillis int64
	CurrentUserUTF16       []uint16
	CreatedPolicy          LoadedFileCreatedPolicy
	OriginalCase           LoadedOriginalCase
	ChecksumPolicy         LocalFileChecksumPolicy
}

// LocalFileSaveResult 区分新建、跳过和更新零行；调用方提交之前结果尚未持久化。
// LocalFileSaveResult distinguishes creation, skipped updates, and updates of
// zero rows. Success remains provisional until the caller commits its node tx.
type LocalFileSaveResult struct {
	ID      int64
	Created bool
	Changed bool
	Count   int64
}

var (
	ErrNilLocalFileSaveDependency   = errors.New("repository: local file save requires nonnil store, allocator and clock")
	ErrUnsupportedLocalFileSavePath = errors.New("repository: unsupported local file save path")
)

type localFileSaveOpError struct {
	op    string
	cause error
}

func (e *localFileSaveOpError) Error() string { return e.op }
func (e *localFileSaveOpError) Unwrap() error { return e.cause }

// SavePublishedLocalFile 实际查询一次，命中沿用加载更新，缺失内部生成完整元数据；
// 保留存储身份，摘要校验后采 updated。ctx 必须非 nil；不管理事务、重试或补偿，错误零结果。
// SavePublishedLocalFile reads one actual node and consumes the existing local
// update flow on hits or creates complete fresh metadata on missing rows.
// Stored identity is authoritative; updated is sampled after checksum verification.
// ctx must be nonnil. The command owns no transaction, retries, or compensation
// for already published bytes or reserved IDs. Errors return the zero result.
func SavePublishedLocalFile(ctx context.Context, in LocalFileSaveInput, deps LocalFileSaveDependencies) (LocalFileSaveResult, error) {
	if err := ctx.Err(); err != nil {
		return LocalFileSaveResult{}, &localFileSaveOpError{op: "repository: local file save cancelled", cause: err}
	}
	if deps.Store == nil || deps.NextID == nil || deps.Clock == nil {
		return LocalFileSaveResult{}, ErrNilLocalFileSaveDependency
	}
	query, err := localFileSaveLookupPath(in.Repo, in.Path)
	if err != nil {
		return LocalFileSaveResult{}, err
	}
	source, err := deps.Store.LookupSnapshotSource(ctx, query)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return LocalFileSaveResult{}, &localFileSaveOpError{op: "repository: local file save lookup", cause: err}
		}
		if err := ctx.Err(); err != nil {
			return LocalFileSaveResult{}, &localFileSaveOpError{op: "repository: local file save cancelled", cause: err}
		}
		constructorCreated := deps.Clock.NowMillis()
		if !validPublishedFileMetadata(in.Published) {
			return LocalFileSaveResult{}, ErrInvalidPublishedFileMetadata
		}
		if in.ChecksumPolicy != LocalFileChecksumClient && in.ChecksumPolicy != LocalFileChecksumServer {
			return LocalFileSaveResult{}, ErrUnsupportedLocalFileChecksumPolicy
		}
		entries := []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: utf16.Encode([]rune(in.Published.SHA1)), Original: normalizeClientOriginal(in.Checksums.SHA1Original, 40)},
			{Kind: ChecksumSHA256, Actual: utf16.Encode([]rune(in.Published.SHA256)), Original: normalizeClientOriginal(in.Checksums.SHA256Original, 64)},
			{Kind: ChecksumMD5, Actual: utf16.Encode([]rune(in.Published.MD5)), Original: normalizeClientOriginal(in.Checksums.MD5Original, 32)},
		}
		if !localClientChecksumsMatch(entries) && in.ChecksumPolicy == LocalFileChecksumClient {
			return LocalFileSaveResult{}, ErrClientChecksumMismatch
		}
		modified := in.ResourceModifiedMillis
		if modified <= 0 {
			modified = constructorCreated
		}
		updated := deps.Clock.NowMillis()
		metadata := NewFileMetadata(FileMetadataInput{
			CreatedMillis: in.ResourceCreatedMillis, ModifiedMillis: modified, UpdatedMillis: updated,
			CreatedBy: NewActor(in.CurrentUserUTF16), ModifiedBy: NewActor(in.CurrentUserUTF16),
			SHA1Original: entries[0].Original, MD5Original: entries[2].Original,
		})
		id, err := CreateFile(ctx, FileCreateInput{Repo: in.Repo, Path: in.Path, Content: in.Published, Metadata: &metadata}, deps.NextID, deps.Store.Create)
		if err != nil {
			return LocalFileSaveResult{}, err
		}
		return LocalFileSaveResult{ID: id, Created: true, Changed: true, Count: 1}, nil
	}
	result, err := updateLocalFileWithClientChecksums(ctx, LocalFileWriteInput{
		Source: source, Published: in.Published,
		ResourceModifiedMillis: in.ResourceModifiedMillis,
		CurrentUserUTF16:       in.CurrentUserUTF16,
		CreatedPolicy:          in.CreatedPolicy, OriginalCase: in.OriginalCase,
		ChecksumPolicy: in.ChecksumPolicy,
	}, in.Checksums, deps.Catalog, deps.Store.Update, deps.Clock.NowMillis)
	if err != nil {
		return LocalFileSaveResult{}, err
	}
	return LocalFileSaveResult{ID: source.Reference.ID, Changed: result.Changed, Count: result.Count}, nil
}

// 只准入已冻结的普通路径身份支持域，创建仍接收最初原始请求。
// Only the frozen ordinary-path identity profile is admitted. The original
// request strings remain untouched for a later raw creation consumer.
func localFileSaveLookupPath(repo, path string) (Path, error) {
	invalid := func(cause error) (Path, error) {
		return Path{}, &localFileSaveOpError{op: "repository: local file save invalid input path", cause: cause}
	}
	if !utf8.ValidString(repo) || !utf8.ValidString(path) {
		return invalid(ErrInvalidPath)
	}
	raw, ok := newRawRepoPath(repo, path)
	if !ok {
		return invalid(ErrInvalidPath)
	}
	forced := raw.forceFile()
	if javaBlank(raw.path) || javaBlank(forced.path) {
		return invalid(ErrInvalidPath)
	}
	if _, ok := repoPathGetName(raw.path); !ok {
		return invalid(ErrInvalidPath)
	}
	parent := pathUtilsGetParent(forced.path)
	if raw.path != forced.path || parent != trimSlashes(javaTrim(parent)) {
		return invalid(ErrUnsupportedLocalFileSavePath)
	}
	if err := ValidateStoragePath(forced.repoKey + "/" + forced.path); err != nil {
		return Path{}, &localFileSaveOpError{op: "repository: local file save invalid storage path", cause: err}
	}
	name, ok := repoPathGetName(forced.path)
	if !ok {
		return invalid(ErrInvalidPath)
	}
	query, err := NewPath(forced.repoKey, parent, name, true)
	if err != nil {
		return invalid(err)
	}
	if query.IsRoot() {
		return invalid(ErrInvalidPath)
	}
	for _, component := range strings.Split(query.PathName(), "/") {
		if component == "." || component == ".." {
			return invalid(ErrUnsupportedLocalFileSavePath)
		}
	}
	return query, nil
}

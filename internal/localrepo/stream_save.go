// Package localrepo 编排普通本地仓库的真实内容发布与节点保存。
// Package localrepo coordinates actual local content publication and node saving.
package localrepo

import (
	"context"
	"errors"
	"io"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
)

// ChecksumPrecheck 显式选择字节层摘要预检，与节点 CLIENT/SERVER 策略独立。
// ChecksumPrecheck explicitly selects digest prechecks independently of node policy.
type ChecksumPrecheck uint8

const (
	SkipChecksumPrecheck ChecksumPrecheck = iota + 1
	VerifyChecksumPrecheck
)

// FileInput 保留已解码的资源事实；不接受调用方提供的发布结果或节点快照。
// 认证用户、模式配置和 marker 来源由上游负责，原始 HTTP 解析不在本层。
// FileInput contains decoded resource facts, never caller-produced publication
// results or node snapshots. Upstream owns principal/configuration/marker
// provenance and HTTP parsing. Concurrent caller mutation is unsupported.
type FileInput struct {
	Repo, Path                                    string
	Checksums                                     repository.ClientFileChecksums
	ResourceCreatedMillis, ResourceModifiedMillis int64
	CurrentUserUTF16                              []uint16
	CreatedPolicy                                 repository.LoadedFileCreatedPolicy
	OriginalCase                                  repository.LoadedOriginalCase
	ChecksumPolicy                                repository.LocalFileChecksumPolicy
	Precheck                                      ChecksumPrecheck
	DeclaredSize                                  *int64
}

// FileDependencies 要求 binary.New 产生的有效 Publisher 与真实节点依赖。
// 调用方拥有其生命周期和节点事务；typed nil / 零值 Publisher 不受支持。
// FileDependencies requires an initialized Publisher from binary.New and real
// node dependencies. Callers own their lifecycle and node transaction; typed
// nil implementations and a zero-value Publisher violate that precondition.
type FileDependencies struct {
	Publisher *binary.Service
	Node      repository.LocalFileSaveDependencies
}

// FileResult 返回登记层规范结果与节点结果，提交节点事务前尚非持久成功。
// 错误始终全零；这不表示已发布内容、登记或独立 ID 被撤销。
// FileResult carries the canonical registration and provisional node result.
// Errors always return zero, without compensating publication/registration/IDs.
type FileResult struct {
	Published content.Metadata
	Save      repository.LocalFileSaveResult
}

var (
	ErrNilFileDependency           = errors.New("localrepo: file save requires initialized publisher and node dependencies")
	ErrNilFileReader               = errors.New("localrepo: nil file reader")
	ErrUnsupportedChecksumPrecheck = errors.New("localrepo: unsupported checksum precheck mode")
	ErrUnsupportedOriginalEncoding = errors.New("localrepo: unsupported precheck original encoding")
)

type fileOpError struct {
	op    string
	cause error
}

func (e *fileOpError) Error() string { return e.op }
func (e *fileOpError) Unwrap() error { return e.cause }

// SaveFile 先实际发布/登记，再消费规范结果保存节点；路径和节点策略留在后段。
// ctx 必须非 nil，reader 由调用方关闭。本入口不管理事务、重试或补偿。
// SaveFile publishes/registers first, then saves a node from the canonical
// result. Node path/policy guards remain late. Context must be nonnil and the
// caller closes the reader. No transaction management, retries or compensation.
func SaveFile(ctx context.Context, reader io.Reader, in FileInput, deps FileDependencies) (FileResult, error) {
	if err := ctx.Err(); err != nil {
		return FileResult{}, &fileOpError{op: "localrepo: file save cancelled", cause: err}
	}
	if deps.Publisher == nil || deps.Node.Store == nil || deps.Node.NextID == nil || deps.Node.Clock == nil {
		return FileResult{}, ErrNilFileDependency
	}
	if reader == nil {
		return FileResult{}, ErrNilFileReader
	}
	if in.Precheck != SkipChecksumPrecheck && in.Precheck != VerifyChecksumPrecheck {
		return FileResult{}, ErrUnsupportedChecksumPrecheck
	}
	expected := filestore.Expected{}
	if in.DeclaredSize != nil {
		size := *in.DeclaredSize
		expected.Size = &size
	}
	if in.Precheck == VerifyChecksumPrecheck {
		for _, slot := range []struct {
			original []uint16
			expected *string
		}{
			{in.Checksums.SHA1Original, &expected.SHA1},
			{in.Checksums.SHA256Original, &expected.SHA256},
			{in.Checksums.MD5Original, &expected.MD5},
		} {
			value, err := precheckOriginal(slot.original)
			if err != nil {
				return FileResult{}, err
			}
			*slot.expected = value
		}
	}
	published, err := deps.Publisher.Add(ctx, reader, expected)
	if err != nil {
		return FileResult{}, &fileOpError{op: "localrepo: file publication failed", cause: err}
	}
	saved, err := repository.SavePublishedLocalFile(ctx, repository.LocalFileSaveInput{
		Repo: in.Repo, Path: in.Path, Published: published, Checksums: in.Checksums,
		ResourceCreatedMillis: in.ResourceCreatedMillis, ResourceModifiedMillis: in.ResourceModifiedMillis,
		CurrentUserUTF16: in.CurrentUserUTF16, CreatedPolicy: in.CreatedPolicy, OriginalCase: in.OriginalCase, ChecksumPolicy: in.ChecksumPolicy,
	}, deps.Node)
	if err != nil {
		return FileResult{}, &fileOpError{op: "localrepo: file node save failed", cause: err}
	}
	return FileResult{Published: published, Save: saved}, nil
}

// precheckOriginal 只接受可无损解码的 fresh Original；精确 marker 表示无期望。
// 空白和合法 hex 的判断复用 provider，不重复实现 Java 空白或摘要框架。
// precheckOriginal admits losslessly decodable fresh Originals and omits the
// exact marker. The provider owns whitespace and valid-hex normalization.
func precheckOriginal(original []uint16) (string, error) {
	for i := 0; i < len(original); i++ {
		unit := original[i]
		if unit >= 0xd800 && unit <= 0xdbff {
			if i+1 == len(original) || original[i+1] < 0xdc00 || original[i+1] > 0xdfff {
				return "", ErrUnsupportedOriginalEncoding
			}
			i++
		} else if unit >= 0xdc00 && unit <= 0xdfff {
			return "", ErrUnsupportedOriginalEncoding
		}
	}
	value := string(utf16.Decode(original))
	if value == "NO_ORIG" {
		return "", nil
	}
	return value, nil
}

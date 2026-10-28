package localrepo

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"sync/atomic"

	"github.com/0ldlight/bf/internal/repository"
)

// FileReadStore 读取同一次查询的节点事实，不重建 FileInfo 或重选登记结果。
// FileReadStore returns same-query node facts without FileInfo reconstruction
// or a second registry lookup. postgres.Store directly implements it.
type FileReadStore interface {
	LookupSnapshotSource(context.Context, repository.Path) (repository.FileSnapshotSource, error)
}

// FileOpener 通过节点实际 SHA1 打开内容。成功必须返回有效 reader，失败返回 nil。
// FileOpener opens the node's actual SHA1. Success requires a valid reader and
// failure a nil reader; filestore.Store directly implements this contract.
type FileOpener interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

// FileReadDependencies 要求真实初始化的依赖，typed nil 不在调用前提内。
// 调用方拥有依赖和节点事务生命周期，本入口不管理事务。
// FileReadDependencies requires initialized dependencies, excluding typed nils.
// The caller owns dependency and node-transaction lifetimes.
type FileReadDependencies struct {
	Store   FileReadStore
	Content FileOpener
}

// FileReadResult 缺失为零值；成功保留同次查询的 Source 和调用方负责关闭的流。
// Source 的 size/checksum 是节点事实，不表示本次实测；成功打开不代表读取完成。
// FileReadResult is zero for absence. Found results retain same-query Source
// and a caller-owned stream. Size/checksums are node facts, not measurements;
// successful opening does not establish successful completion of future reads.
type FileReadResult struct {
	Found  bool
	Source repository.FileSnapshotSource
	Body   io.ReadCloser
}

var (
	ErrNilFileReadDependency = errors.New("localrepo: file read requires initialized store and content opener")
	ErrInvalidFileReadSource = errors.New("localrepo: unsupported file read source")
)

// ReadFile 接收已构造的存储身份，不再次解析原始请求或剥离 metadata 后缀。
// 一次查询后立即打开实际内容；错误零结果。ctx 必须非 nil，调用方关闭返回流。
// 无鉴权/HTTP、重查、修复、ID 分配或事务控制。后续 Read/Close 错误归 reader。
// ReadFile accepts a constructed storage identity without request parsing or
// repeated suffix mapping. One lookup precedes eager opening; errors return zero.
// Context must be nonnil and the caller closes the returned body. No auth/HTTP,
// relookup, repair, ID allocation or transaction control. Future I/O errors
// belong to the reader; cancellation after return does not close it automatically.
func ReadFile(ctx context.Context, path repository.Path, deps FileReadDependencies) (FileReadResult, error) {
	if err := ctx.Err(); err != nil {
		return FileReadResult{}, &fileOpError{op: "localrepo: file read cancelled", cause: err}
	}
	if deps.Store == nil || deps.Content == nil {
		return FileReadResult{}, ErrNilFileReadDependency
	}
	if path.Repo() == "" || !path.IsFile() || path.IsRoot() {
		return FileReadResult{}, &fileOpError{op: "localrepo: file read invalid path", cause: repository.ErrInvalidPath}
	}
	source, err := deps.Store.LookupSnapshotSource(ctx, path)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FileReadResult{}, nil
		}
		return FileReadResult{}, &fileOpError{op: "localrepo: file lookup failed", cause: err}
	}
	if err := ctx.Err(); err != nil {
		return FileReadResult{}, &fileOpError{op: "localrepo: file read cancelled", cause: err}
	}
	ref := source.Reference
	if ref.ID <= 0 || ref.Path.Repo() == "" || !ref.Path.IsFile() || ref.Path.IsRoot() || !source.SHA1ActualValid {
		return FileReadResult{}, ErrInvalidFileReadSource
	}
	// 精确空 SHA1 走实际目标的空流分支，节点 size 仍原样保留。
	// The exact empty SHA1 selects a synthetic stream, retaining node size.
	if ref.Content.SHA1 == emptyFileSHA1 {
		return FileReadResult{Found: true, Source: source, Body: &emptyFileBody{}}, nil
	}
	body, err := deps.Content.Open(ctx, ref.Content.SHA1)
	if err != nil {
		return FileReadResult{}, &fileOpError{op: "localrepo: file content open failed", cause: err}
	}
	return FileReadResult{Found: true, Source: source, Body: body}, nil
}

const emptyFileSHA1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"

// emptyFileBody 的零长度读取即使关闭后也成功；非空读取按状态返回 EOF/关闭。
// 原子标志仅保证本地 Read/Close 无竞态，不宣称完整 Java 并发或异常等价。
// emptyFileBody permits zero-length reads even after close. Other reads return
// EOF or the Go closed sentinel. Atomic state provides local race safety, not
// complete Java concurrency or exception parity.
type emptyFileBody struct{ closed atomic.Bool }

func (b *emptyFileBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.closed.Load() {
		return 0, os.ErrClosed
	}
	return 0, io.EOF
}

func (b *emptyFileBody) Close() error {
	b.closed.Store(true)
	return nil
}

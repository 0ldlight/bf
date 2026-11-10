package localrepo

import (
	"context"
	"database/sql"
	"errors"

	"github.com/0ldlight/bf/internal/repository"
)

// ErrNilFileOverwriteInput 是前置守卫的固定哨兵错误：nil ctx 或 nil store。
// ErrNilFileOverwriteInput is the fixed sentinel for the entry guards: a nil
// context or a nil store.
var ErrNilFileOverwriteInput = errors.New("localrepo: file overwrite preflight requires nonnil context and store")

// LookupFileOverwrite 读取一次节点事实并计算覆盖条件。调用方提供已初始化
// 的 Store，处理错误后才能使用布尔值，并负责写入时的授权与一致性。
// LookupFileOverwrite computes the overwrite predicate from one node lookup.
// The store must be initialized and reserve sql.ErrNoRows for confirmed absence.
// Cancellation takes precedence and preserves any simultaneous lookup failure
// other than absence. Callers must check the error before using the boolean
// and remain responsible for authorization and consistency at write time.
func LookupFileOverwrite(ctx context.Context, path repository.Path, store FileReadStore, enforceOnIdenticalChecksums bool, requestSHA1, requestSHA256 []uint16) (bool, error) {
	if ctx == nil {
		return false, ErrNilFileOverwriteInput
	}
	if err := ctx.Err(); err != nil {
		return false, &fileOpError{op: "localrepo: file overwrite cancelled", cause: err}
	}
	if store == nil {
		return false, ErrNilFileOverwriteInput
	}
	if path.Repo() == "" || !path.IsFile() || path.IsRoot() {
		return false, &fileOpError{op: "localrepo: file overwrite invalid path", cause: repository.ErrInvalidPath}
	}
	source, err := store.LookupSnapshotSource(ctx, path)
	cancelErr := ctx.Err()
	if cancelErr != nil {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			// 取消与查找错误同时发生：两者原因都保留，取消优先。
			// Cancellation and a lookup error coincide: both causes are
			// preserved while cancellation wins.
			return false, &fileOpError{op: "localrepo: file overwrite cancelled", cause: errors.Join(cancelErr, err)}
		}
		return false, &fileOpError{op: "localrepo: file overwrite cancelled", cause: cancelErr}
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 仅 ErrNoRows 建模缺失：false 且无错误。
			// Only ErrNoRows models absence: false with no error.
			return false, nil
		}
		return false, &fileOpError{op: "localrepo: file overwrite lookup failed", cause: err}
	}
	if source.Reference.Path != path {
		return false, &fileOpError{op: "localrepo: file overwrite path mismatch", cause: repository.ErrInvalidFileSnapshotSource}
	}
	overwrite, err := repository.DecideFileOverwriteFromSource(&source, enforceOnIdenticalChecksums, requestSHA1, requestSHA256)
	if err != nil {
		return false, &fileOpError{op: "localrepo: file overwrite decision failed", cause: err}
	}
	return overwrite, nil
}

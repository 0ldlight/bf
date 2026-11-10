package security

import (
	"context"
	"errors"

	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// UploadPermissionReader 读取当前持久化的上传部署/删除授权事实，不拥有
// 写入、事务或 HTTP 策略。
// UploadPermissionReader reads current persisted upload deploy/delete
// authorization facts; it owns no write, transaction, or HTTP policy.
type UploadPermissionReader interface {
	CanDeploy(context.Context, Principal, RepositoryInfo, repository.Path) (bool, error)
	CanDelete(context.Context, Principal, RepositoryInfo, repository.Path) (bool, error)
}

// 预检阶段的固定文案（不含任何输入字节），用于 storeError 的 op。
// Fixed upload-preflight stage texts (never echoing input bytes) used as
// storeError ops.
// errInvalidUploadInput 是前置守卫的固定诊断哨兵（文案不含输入字节）。
// errInvalidUploadInput is the fixed guard sentinel (text never echoes input).
var errInvalidUploadInput = errors.New("security: upload preflight requires nonnil context and dependencies")

const (
	stageUploadInput  = "security: upload preflight input"
	stageUploadCancel = "security: upload preflight cancelled"
	stageCanDeployRow = "security: can-deploy lookup"
	stageUploadNode   = "security: upload overwrite lookup"
	stageCanDeleteRow = "security: can-delete lookup"
)

// CheckUploadPermissions 根据 deploy、目标节点与所需的 delete 许可执行预检。
// CheckUploadPermissions checks deploy first and checks delete only when the
// overwrite predicate is true. The caller supplies a verified principal,
// current catalog facts, raw digests and initialized readers. It handles the
// error before using the result and owns consistency through commit.
func CheckUploadPermissions(ctx context.Context, principal Principal, repo RepositoryInfo, path repository.Path, grants UploadPermissionReader, nodes localrepo.FileReadStore, enforceOnIdenticalChecksums bool, requestSHA1, requestSHA256 []uint16) (bool, error) {
	if ctx == nil || grants == nil || nodes == nil {
		return false, &storeError{sentinel: ErrConfiguration, op: stageUploadInput, cause: errInvalidUploadInput}
	}
	if principal.ID() <= 0 || principal.Username() == "" {
		return false, &storeError{sentinel: ErrConfiguration, op: stageUploadInput, cause: errInvalidPrincipalArg}
	}
	if repo.Key == "" || repo.Kind != "local" || repo.PackageType != "generic" || repo.Revision <= 0 {
		return false, &storeError{sentinel: ErrConfiguration, op: stageUploadInput, cause: errUnsupportedRepoArg}
	}
	if path.Repo() != repo.Key || !path.IsFile() || path.IsRoot() {
		return false, &storeError{sentinel: ErrConfiguration, op: stageUploadInput, cause: errRepoPathMismatchArg}
	}
	if err := ctx.Err(); err != nil {
		return false, newStoreError(stageUploadCancel, err)
	}
	deploy, err := grants.CanDeploy(ctx, principal, repo, path)
	if err := uploadPreflightError(ctx, stageCanDeployRow, err); err != nil {
		return false, err
	}
	if !deploy {
		return false, nil
	}
	overwrite, err := localrepo.LookupFileOverwrite(ctx, path, nodes, enforceOnIdenticalChecksums, requestSHA1, requestSHA256)
	if err := uploadPreflightError(ctx, stageUploadNode, err); err != nil {
		return false, err
	}
	if !overwrite {
		return true, nil
	}
	remove, err := grants.CanDelete(ctx, principal, repo, path)
	if err := uploadPreflightError(ctx, stageCanDeleteRow, err); err != nil {
		return false, err
	}
	return remove, nil
}

// uploadPreflightError preserves both cancellation and a simultaneous read failure.
func uploadPreflightError(ctx context.Context, stage string, err error) error {
	if cancelErr := ctx.Err(); cancelErr != nil {
		return newStoreError(stageUploadCancel, errors.Join(cancelErr, err))
	}
	if err != nil {
		return newStoreError(stage, err)
	}
	return nil
}

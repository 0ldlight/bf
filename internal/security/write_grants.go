package security

import (
	"context"

	"github.com/0ldlight/bf/internal/repository"
)

var _ UploadPermissionReader = (*Store)(nil)

const canWriteQuery = `
SELECT EXISTS (
    SELECT 1
    FROM security_repositories r
    JOIN security_write_grants g ON g.repo_key = r.repo_key
    JOIN security_users u ON u.user_id = $1 AND u.username = $2 AND u.enabled
    WHERE r.repo_key = $3 AND r.revision = $4
      AND r.kind = 'local' AND r.package_type = 'generic'
      AND g.action = $5 AND g.scope = '**'
      AND (g.user_id = u.user_id OR EXISTS (
          SELECT 1 FROM security_group_members m
          WHERE m.user_id = u.user_id AND m.group_id = g.group_id
      ))
)`

// CanDeploy checks current explicit deploy grants for this file's repository.
func (s *Store) CanDeploy(ctx context.Context, principal Principal, repo RepositoryInfo, path repository.Path) (bool, error) {
	return s.canWrite(ctx, principal, repo, path, "deploy")
}

// CanDelete checks current explicit delete grants for this file's repository.
func (s *Store) CanDelete(ctx context.Context, principal Principal, repo RepositoryInfo, path repository.Path) (bool, error) {
	return s.canWrite(ctx, principal, repo, path, "delete")
}

func (s *Store) canWrite(ctx context.Context, principal Principal, repo RepositoryInfo, path repository.Path, action string) (bool, error) {
	const stage = "security: write grant lookup"
	switch {
	case ctx == nil:
		return false, &storeError{sentinel: ErrConfiguration, op: stage, cause: errInvalidUploadInput}
	case principal.ID() <= 0 || principal.Username() == "":
		return false, &storeError{sentinel: ErrConfiguration, op: stage, cause: errInvalidPrincipalArg}
	case repo.Key == "" || repo.Kind != "local" || repo.PackageType != "generic" || repo.Revision <= 0:
		return false, &storeError{sentinel: ErrConfiguration, op: stage, cause: errUnsupportedRepoArg}
	case path.Repo() != repo.Key || !path.IsFile() || path.IsRoot():
		return false, &storeError{sentinel: ErrConfiguration, op: stage, cause: errRepoPathMismatchArg}
	}
	qctx, cancel := s.opContext(ctx)
	defer cancel()
	var allowed bool
	err := s.db.QueryRowContext(qctx, canWriteQuery, principal.ID(), principal.Username(), repo.Key, repo.Revision, action).Scan(&allowed)
	if err != nil {
		return false, newStoreError(stage, err)
	}
	if err := qctx.Err(); err != nil {
		return false, newStoreError(stage, err)
	}
	return allowed, nil
}

package security

import (
	"context"
	"database/sql"
	"errors"

	"github.com/0ldlight/bf/internal/repository"
	nodepg "github.com/0ldlight/bf/internal/repository/postgres"
)

type txUploadGrants struct{ tx *sql.Tx }

var errUploadCommitSession = errors.New("upload commit requires the configured schema and read committed isolation")

func (g txUploadGrants) CanDeploy(ctx context.Context, p Principal, r RepositoryInfo, path repository.Path) (bool, error) {
	return queryWriteGrant(ctx, g.tx, p, r, path, "deploy")
}

func (g txUploadGrants) CanDelete(ctx context.Context, p Principal, r RepositoryInfo, path repository.Path) (bool, error) {
	return queryWriteGrant(ctx, g.tx, p, r, path, "delete")
}

// RecheckUploadCommit takes the upload locks and checks fresh permission and
// node facts on the caller's READ COMMITTED transaction. The caller keeps
// that transaction through the write and rolls it back on denial or error.
// The trusted schema must be the same one used by the transaction's queries.
func RecheckUploadCommit(ctx context.Context, tx *sql.Tx, schema string, principal Principal, repo RepositoryInfo, path repository.Path, enforce bool, sha1, sha256 []uint16) (bool, error) {
	const stage = "security: upload commit lock"
	if ctx == nil || tx == nil || !uploadLockIdentifier.MatchString(schema) {
		return false, &storeError{sentinel: ErrConfiguration, op: stage, cause: errInvalidUploadInput}
	}
	var compatible bool
	err := tx.QueryRowContext(ctx, `SELECT pg_catalog.current_schema() = $1::text
        AND pg_catalog.current_setting('transaction_isolation') = 'read committed'`, schema).Scan(&compatible)
	if err != nil {
		return false, newStoreError(stage, err)
	}
	if !compatible {
		return false, &storeError{sentinel: ErrConfiguration, op: stage, cause: errUploadCommitSession}
	}
	// Resolve the lock and subsequent unqualified reads/writes in the same
	// trusted schema. pg_temp last prevents implicit temporary-table lookup.
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path = "`+schema+`", pg_temp`); err != nil {
		return false, newStoreError(stage, err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT "`+schema+`".lock_upload_commit()`); err != nil {
		return false, newStoreError(stage, err)
	}
	nodes, err := nodepg.New(tx)
	if err != nil {
		return false, newStoreError(stage, err)
	}
	return CheckUploadPermissions(ctx, principal, repo, path, txUploadGrants{tx}, nodes, enforce, sha1, sha256)
}

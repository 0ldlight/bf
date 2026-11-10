package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var uploadLockIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
var errUploadLockACL = errors.New("upload lock requires a distinct owner and no other direct execute grantees")

// InstallUploadCommitLock installs the fixed lock function and its ACL in an
// operator-owned transaction. Apply after creating the security and node
// tables. The caller owns rollback on error and must use a trusted schema
// and a dedicated function owner with the required table lock privileges.
func InstallUploadCommitLock(ctx context.Context, tx *sql.Tx, schema, uploadRole string) error {
	const stage = "security: install upload commit lock"
	if ctx == nil || tx == nil || !uploadLockIdentifier.MatchString(schema) ||
		!uploadLockIdentifier.MatchString(uploadRole) || strings.HasPrefix(schema, "pg_") ||
		strings.HasPrefix(uploadRole, "pg_") || uploadRole == "public" {
		return &storeError{sentinel: ErrConfiguration, op: stage, cause: errInvalidUploadInput}
	}
	// Both interpolated identifiers are limited to lowercase ASCII and 63 bytes.
	query := fmt.Sprintf(`
CREATE FUNCTION "%[1]s".lock_upload_commit() RETURNS pg_catalog.void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $lock$
BEGIN
    LOCK TABLE "%[1]s".security_users, "%[1]s".security_repositories,
        "%[1]s".security_group_members, "%[1]s".security_write_grants IN SHARE MODE;
    LOCK TABLE "%[1]s".nodes IN SHARE ROW EXCLUSIVE MODE;
END;
$lock$;
REVOKE ALL ON FUNCTION "%[1]s".lock_upload_commit() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION "%[1]s".lock_upload_commit() TO "%[2]s";`, schema, uploadRole)
	if _, err := tx.ExecContext(ctx, query); err != nil {
		return newStoreError(stage, err)
	}
	var restricted bool
	err := tx.QueryRowContext(ctx, `
SELECT p.proowner <> r.oid AND NOT EXISTS (
    SELECT 1 FROM pg_catalog.aclexplode(p.proacl) a
    WHERE a.privilege_type = 'EXECUTE' AND a.grantee NOT IN (p.proowner, r.oid)
)
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_roles r ON r.rolname = $2
WHERE p.oid = pg_catalog.to_regprocedure($1)`, schema+".lock_upload_commit()", uploadRole).Scan(&restricted)
	if err != nil {
		return newStoreError(stage, err)
	}
	if !restricted {
		return &storeError{sentinel: ErrConfiguration, op: stage, cause: errUploadLockACL}
	}
	return nil
}

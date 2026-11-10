package postgresit

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/0ldlight/bf/internal/security"
	"github.com/jackc/pgx/v5/pgconn"
)

// This test exercises the proposed database boundary independently of the saver.
func TestUploadCommitLockBoundary(t *testing.T) {
	e, _ := dseSetup(t)
	ctx := e.f.ctx
	if err := e.f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dghExec(t, e.f, security.WriteGrantsSQL)
	upload, err := e.f.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Rollback()
	schema := e.f.schema
	owner, writer, outsider := schema+"_owner", schema+"_writer", schema+"_outsider"
	exec := func(query string) {
		t.Helper()
		if _, err := upload.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{owner, writer, outsider} {
		exec("CREATE ROLE " + role + " NOLOGIN")
	}
	exec(fmt.Sprintf("GRANT USAGE, CREATE ON SCHEMA %s TO %s", schema, owner))
	exec(fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s, %s", schema, writer, outsider))
	exec("GRANT MAINTAIN ON security_users, security_repositories, security_group_members, security_write_grants, nodes TO " + owner)
	exec("GRANT SELECT ON security_users, security_repositories, security_group_members, security_write_grants, nodes TO " + writer)
	exec("SET LOCAL ROLE " + owner)
	exec("SAVEPOINT extra_default_execute")
	exec("ALTER DEFAULT PRIVILEGES GRANT EXECUTE ON FUNCTIONS TO " + outsider)
	if err := security.InstallUploadCommitLock(ctx, upload, schema, writer); !errors.Is(err, security.ErrConfiguration) {
		t.Fatalf("unexpected default execute grant was accepted: %v", err)
	}
	exec("ROLLBACK TO SAVEPOINT extra_default_execute")
	exec("RELEASE SAVEPOINT extra_default_execute")
	if err := security.InstallUploadCommitLock(ctx, upload, schema, writer); err != nil {
		t.Fatal(err)
	}
	exec("RESET ROLE")
	fn := schema + ".lock_upload_commit()"
	assertDenied := func(query string) {
		t.Helper()
		exec("SAVEPOINT permission_check")
		_, err := upload.ExecContext(ctx, query)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("expected insufficient privilege, got %v", err)
		}
		exec("ROLLBACK TO SAVEPOINT permission_check")
		exec("RELEASE SAVEPOINT permission_check")
	}
	exec("SET LOCAL ROLE " + outsider)
	assertDenied("SELECT " + fn)
	exec("RESET ROLE")
	exec("SET LOCAL ROLE " + writer)
	assertDenied("LOCK TABLE " + schema + ".security_write_grants IN SHARE MODE")
	assertDenied("DELETE FROM " + schema + ".security_write_grants")
	exec("SELECT " + fn)

	tryLock := func(query string) error {
		t.Helper()
		tx, err := e.f.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, query)
		return err
	}
	// NOWAIT uses the database's conflict decision instead of a timing window.
	for _, table := range []string{"security_users", "security_repositories", "security_group_members", "security_write_grants", "nodes"} {
		var pgErr *pgconn.PgError
		if err := tryLock("LOCK TABLE " + table + " IN ROW EXCLUSIVE MODE NOWAIT"); !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("concurrent write lock on %s: %v", table, err)
		}
		if err := tryLock("LOCK TABLE " + table + " IN ACCESS SHARE MODE NOWAIT"); err != nil {
			t.Fatalf("concurrent read lock on %s: %v", table, err)
		}
	}
	var conflict *pgconn.PgError
	if err := tryLock("LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE NOWAIT"); !errors.As(err, &conflict) || conflict.Code != "55P03" {
		t.Fatalf("second upload node lock: %v", err)
	}
	if err := upload.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tryLock("LOCK TABLE security_write_grants, nodes IN ROW EXCLUSIVE MODE NOWAIT"); err != nil {
		t.Fatalf("writer after upload rollback: %v", err)
	}
}

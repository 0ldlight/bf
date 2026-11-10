package postgresit

import (
	"database/sql"
	"errors"
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
	if _, err := upload.ExecContext(ctx, "LOCK TABLE security_users, security_repositories, security_group_members, security_write_grants IN SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	if _, err := upload.ExecContext(ctx, "LOCK TABLE nodes IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}

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

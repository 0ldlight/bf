package postgresit

import (
	"context"
	"errors"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
	"github.com/0ldlight/bf/internal/security"
	"testing"
	"time"
)

// 原始用户 ID 不能授权同名重建账户；版本行漂移与读池超时必须失败关闭。
// Old identity IDs cannot authorize replacement users; version drift and pool deadlines fail closed.
func dseRootBoundaries(t *testing.T) {
	t.Run("schema exact version", func(t *testing.T) {
		e, _ := dseSetup(t)
		if err := e.backend.Validate(e.f.ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := e.f.db.ExecContext(e.f.ctx, "ALTER TABLE security_schema_version DROP CONSTRAINT security_schema_version_version_check, DROP CONSTRAINT security_schema_version_pkey; INSERT INTO security_schema_version(version) VALUES (2)"); err != nil {
			t.Fatal(err)
		}
		if err := e.backend.Validate(e.f.ctx); !errors.Is(err, security.ErrSecurityStore) {
			t.Fatalf("multiple version rows accepted: %v", err)
		}
	})
	t.Run("deleted identity replacement", func(t *testing.T) {
		e, _ := dseSetup(t)
		dseRepo(t, e, "saveR")
		oldID := dseUser(t, e, "reader", "Controlled-reader-passphrase!", true, false)
		dseUserGrant(t, e, "saveR", oldID)
		old, err := e.backend.Authenticate(e.f.ctx, "reader", "Controlled-reader-passphrase!")
		if err != nil {
			t.Fatal(err)
		}
		repo, err := e.backend.LookupRepository(e.f.ctx, "saveR")
		if err != nil {
			t.Fatal(err)
		}
		path, err := repository.NewPath("saveR", "dir", "a.bin", true)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := e.backend.CanRead(e.f.ctx, old, repo, path); err != nil || !ok {
			t.Fatalf("old initial read=%v %v", ok, err)
		}
		if _, err := e.f.db.ExecContext(e.f.ctx, "DELETE FROM security_users WHERE user_id=$1", oldID); err != nil {
			t.Fatal(err)
		}
		newID := dseUser(t, e, "reader", "Controlled-reader-passphrase!", true, false)
		if newID == oldID {
			t.Fatal("ID reused")
		}
		dseUserGrant(t, e, "saveR", newID)
		if ok, err := e.backend.CanRead(e.f.ctx, old, repo, path); err != nil || ok {
			t.Fatalf("old identity authorized replacement: %v %v", ok, err)
		}
		current, err := e.backend.Authenticate(e.f.ctx, "reader", "Controlled-reader-passphrase!")
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := e.backend.CanRead(e.f.ctx, current, repo, path); err != nil || !ok {
			t.Fatalf("replacement read=%v %v", ok, err)
		}
	})
	t.Run("read pool deadline", func(t *testing.T) {
		e, _ := dseSetup(t)
		if err := e.f.tx.Commit(); err != nil {
			t.Fatal(err)
		}
		lock, err := e.f.db.BeginTx(e.f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		if _, err := lock.ExecContext(e.f.ctx, "LOCK TABLE nodes IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		pool, err := refpg.NewReadPool(e.f.db, 20*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		path, err := repository.NewPath("saveR", "dir", "a.bin", true)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.LookupSnapshotSource(e.f.ctx, path)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked pool lookup error=%v", err)
		}
		if e.f.ctx.Err() != nil {
			t.Fatal("dependency timeout canceled parent")
		}
	})
}

package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	uploadpg "github.com/0ldlight/bf/internal/localrepo/postgres"
	"github.com/0ldlight/bf/internal/repository"
	"github.com/0ldlight/bf/internal/security"
	"github.com/jackc/pgx/v5/stdlib"
)

type beforeUploadRead struct {
	io.Reader
	before func()
}

func (r *beforeUploadRead) Read(p []byte) (int, error) {
	if r.before != nil {
		r.before()
		r.before = nil
	}
	return r.Reader.Read(p)
}

func TestAuthorizedSaveRechecksAtCommit(t *testing.T) {
	e, publisher := dseSetup(t)
	ctx := e.f.ctx
	if err := e.f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dghExec(t, e.f, security.WriteGrantsSQL)
	role := e.f.schema + "_commit_writer"
	tx, err := e.f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE ROLE "+role+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []string{
		"GRANT USAGE ON SCHEMA " + e.f.schema + " TO " + role,
		"GRANT SELECT ON security_users, security_repositories, security_group_members, security_write_grants TO " + role,
		"GRANT SELECT, INSERT, UPDATE ON nodes TO " + role,
	} {
		if _, err := tx.ExecContext(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	if err := security.InstallUploadCommitLock(ctx, tx, e.f.schema, role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), dseTimeout)
		defer cancel()
		if _, err := e.f.adm.ExecContext(cleanup, "DROP FUNCTION "+e.f.schema+".lock_upload_commit(); DROP OWNED BY "+role+"; DROP ROLE "+role); err != nil {
			t.Errorf("clean upload lock fixture: %v", err)
		}
	})
	dseRepo(t, e, "saveR")
	const username = "writer"
	const secret = "Synthetic-Writer-Passphrase!"
	id := dseUser(t, e, username, secret, true, false)
	p, err := e.backend.Authenticate(ctx, username, secret)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := e.backend.LookupRepository(ctx, "saveR")
	if err != nil {
		t.Fatal(err)
	}
	allocator := registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), e.f.schema)
	allocator.SetMaxOpenConns(1)
	cfg, err := parseTestConfig(os.Getenv("BINFLOW_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = e.f.schema
	cfg.RuntimeParams["role"] = role
	writerDB := stdlib.OpenDB(*cfg)
	writerDB.SetMaxOpenConns(2)
	t.Cleanup(func() { writerDB.Close() })
	var activeRole string
	if err := writerDB.QueryRowContext(ctx, "SELECT current_user").Scan(&activeRole); err != nil || activeRole != role {
		t.Fatalf("restricted writer role = (%q, %v)", activeRole, err)
	}
	saver, err := uploadpg.NewUploadSaver(uploadpg.UploadDeps{
		DB: writerDB, Publisher: publisher, Clock: &usClock{millis: usMillisOne},
		NodeTimeout: dseTimeout, CommitSchema: e.f.schema, ReservationDB: allocator,
	})
	if err != nil {
		t.Fatal(err)
	}
	grant := func(action string) {
		dghExec(t, e.f, "INSERT INTO security_write_grants(repo_key,user_id,action,scope) VALUES($1,$2,$3,'**')", repo.Key, id, action)
	}
	const payload = "commit recheck payload"
	in := usInput(t, repo.Key, "dir/new.bin", "untrusted-actor", payload)
	if _, err := saver.Save(ctx, strings.NewReader(payload), in); err == nil {
		t.Fatal("authorized saver accepted the unchecked entry point")
	}
	grant("deploy")
	preflight := func() {
		t.Helper()
		got, err := security.CheckUploadPermissions(ctx, p, repo, in.Path, e.backend, e.nodes, false, in.Checksums.SHA1Original, in.Checksums.SHA256Original)
		if err != nil || !got {
			t.Fatalf("preflight = (%t, %v)", got, err)
		}
	}
	denied := func(before func()) {
		t.Helper()
		body := &beforeUploadRead{Reader: strings.NewReader(payload), before: before}
		got, err := saver.SaveAuthorized(ctx, body, in, p, repo, false)
		if got != (repository.FileSnapshotSource{}) || !errors.Is(err, uploadpg.ErrUploadCommitDenied) {
			t.Fatalf("expected commit denial, got source=%+v error=%v", got, err)
		}
	}

	preflight()
	denied(func() {
		dghExec(t, e.f, "DELETE FROM security_write_grants WHERE repo_key=$1 AND user_id=$2", repo.Key, id)
	})
	var count int
	if err := e.f.db.QueryRowContext(ctx, "SELECT count(*) FROM nodes").Scan(&count); err != nil || count != 0 {
		t.Fatalf("nodes after revoked upload = (%d, %v)", count, err)
	}

	grant("deploy")
	preflight()
	denied(func() {
		dghExec(t, e.f, "INSERT INTO nodes(node_id,node_type,repo,node_path,node_name,depth) VALUES(721,1,$1,'dir','new.bin',2)", repo.Key)
	})
	var actual *string
	if err := e.f.db.QueryRowContext(ctx, "SELECT sha1_actual FROM nodes WHERE node_id=721").Scan(&actual); err != nil || actual != nil {
		t.Fatalf("competing node changed: sha1=%v error=%v", actual, err)
	}
	dghExec(t, e.f, "DELETE FROM nodes WHERE node_id=721")
	preflight()
	blocker, err := e.f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, "LOCK TABLE nodes IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := saver.SaveAuthorized(waitCtx, strings.NewReader(payload), in, p, repo, false)
		done <- err
	}()
	waitForUploadNodeLock(t, ctx, e.f.db, e.f.schema)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lock wait: %v", err)
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	preflight()
	got, err := saver.SaveAuthorized(ctx, strings.NewReader(payload), in, p, repo, false)
	if err != nil || got.Reference.ID <= 0 || got.Reference.Metadata == nil ||
		!slices.Equal(got.Reference.Metadata.CreatedBy().UTF16(), mdUnits(username)) {
		t.Fatalf("authorized creation failed or did not bind actor: %v", err)
	}
	handler, err := security.NewAuthenticatedUpload(ctx, security.UploadHTTPConfig{
		HTTPConfig: security.HTTPConfig{
			RepoKey: repo.Key, Challenge: dseChallenge,
			FailureThreshold: 3, FailureWindow: time.Minute,
			BlockDuration: time.Minute, MaxTrackedUsers: 8,
		},
		MaxBodyBytes: 1024, PublicOrigin: "https://example.test",
	}, e.backend, e.nodes, saver)
	if err != nil {
		t.Fatal(err)
	}
	put := func(name, payload string, authenticate bool, before func()) *httptest.ResponseRecorder {
		t.Helper()
		body := &beforeUploadRead{Reader: strings.NewReader(payload), before: before}
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/artifactory/"+repo.Key+"/dir/"+name, body)
		req.ContentLength = int64(len(payload))
		req.Header.Set("Content-Length", strconv.Itoa(len(payload)))
		if authenticate {
			req.SetBasicAuth(username, secret)
		}
		sha1, sha256, _ := usDigests(payload)
		req.Header.Set("X-Checksum-Sha1", sha1)
		req.Header.Set("X-Checksum-Sha256", sha256)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	noRead := func() { t.Fatal("denied HTTP request read the body") }
	if out := put("http.bin", payload, false, noRead); out.Code != 401 || out.Header().Get("WWW-Authenticate") != dseChallenge {
		t.Fatalf("unauthenticated PUT = %d", out.Code)
	}
	if out := put("new.bin", "different payload", true, noRead); out.Code != 403 {
		t.Fatalf("overwrite without delete = %d", out.Code)
	}
	if out := put("new.bin", payload, true, nil); out.Code != 201 {
		t.Fatalf("identical checksum replay with deploy alone = %d", out.Code)
	}
	if out := put("http.bin", payload, true, func() {
		dghExec(t, e.f, "DELETE FROM security_write_grants WHERE repo_key=$1 AND user_id=$2", repo.Key, id)
	}); out.Code != 403 {
		t.Fatalf("commit-time revocation HTTP status = %d", out.Code)
	}
	if err := e.f.db.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE node_name='http.bin'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("HTTP commit denial modified nodes: (%d, %v)", count, err)
	}
	grant("deploy")
	if out := put("http.bin", payload, true, nil); out.Code != 201 {
		t.Fatalf("authenticated create = %d: %s", out.Code, out.Body.String())
	}
}

func waitForUploadNodeLock(t *testing.T, ctx context.Context, db *sql.DB, schema string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM pg_catalog.pg_locks
    WHERE relation = pg_catalog.to_regclass($1)
      AND mode = 'ShareRowExclusiveLock' AND NOT granted
)`, schema+".nodes").Scan(&waiting)
		if err != nil {
			t.Fatalf("observe upload lock wait: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("upload did not wait for the node lock")
		case <-ticker.C:
		}
	}
}

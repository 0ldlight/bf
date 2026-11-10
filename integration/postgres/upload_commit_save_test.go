package postgresit

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	uploadpg "github.com/0ldlight/bf/internal/localrepo/postgres"
	"github.com/0ldlight/bf/internal/repository"
	"github.com/0ldlight/bf/internal/security"
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
	if err := security.InstallUploadCommitLock(ctx, tx, e.f.schema, role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), dseTimeout)
		defer cancel()
		if _, err := e.f.adm.ExecContext(cleanup, "DROP FUNCTION "+e.f.schema+".lock_upload_commit(); DROP ROLE "+role); err != nil {
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
	saver, err := uploadpg.NewUploadSaver(uploadpg.UploadDeps{
		DB: e.f.db, Publisher: publisher, Clock: &usClock{millis: usMillisOne},
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
	got, err := saver.SaveAuthorized(ctx, strings.NewReader(payload), in, p, repo, false)
	if err != nil || got.Reference.ID <= 0 || got.Reference.Metadata == nil ||
		!slices.Equal(got.Reference.Metadata.CreatedBy().UTF16(), mdUnits(username)) {
		t.Fatalf("authorized creation failed or did not bind actor: %v", err)
	}
}

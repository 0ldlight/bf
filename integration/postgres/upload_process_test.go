package postgresit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/process"
	"github.com/0ldlight/bf/internal/security"
)

func TestProcessArtifactsMode(t *testing.T) {
	e, _ := dseSetup(t)
	ctx := e.f.ctx
	if err := e.f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	schema := e.f.schema
	writer, allocator := schema+"_writer", schema+"_allocator"
	const password = "Disposable-Binflow-Database-Role!"
	tx, err := e.f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		security.WriteGrantsSQL,
		"CREATE ROLE " + writer + " LOGIN PASSWORD '" + password + "'",
		"CREATE ROLE " + allocator + " LOGIN PASSWORD '" + password + "'",
		fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s, %s", schema, writer, allocator),
		"GRANT SELECT ON security_schema_version, security_users, security_groups, security_repositories, security_group_members, security_read_grants, security_write_grants TO " + writer,
		"GRANT SELECT, INSERT, UPDATE ON nodes TO " + writer,
		"GRANT SELECT, INSERT, UPDATE (sha256) ON binaries TO " + writer,
		"GRANT SELECT, UPDATE ON unique_ids TO " + allocator,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare process fixture: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := e.f.adm.ExecContext(ctx, "DROP FUNCTION IF EXISTS "+schema+".lock_upload_commit(); DROP OWNED BY "+writer+", "+allocator+"; DROP ROLE "+writer+", "+allocator); err != nil {
			t.Errorf("clean process roles: %v", err)
		}
	})
	const repo = "saveR"
	const user = "writer"
	const secret = "Disposable-Binflow-Artifact-Writer!"
	dseRepo(t, e, repo)
	id := dseUser(t, e, user, secret, true, false)
	dseUserGrant(t, e, repo, id)
	dghExec(t, e.f, "INSERT INTO security_write_grants(repo_key,user_id,action,scope) VALUES($1,$2,'deploy','**')", repo, id)
	readerDSN, _ := dpCommandDSN(t, schema)
	roleDSN := func(role string) string {
		u, err := url.Parse(readerDSN)
		if err != nil {
			t.Fatal("invalid fixture URL")
		}
		u.User = url.UserPassword(role, password)
		return u.String()
	}
	cfg := process.Config{
		Mode: process.ModeArtifacts, ListenAddr: "127.0.0.1:0",
		PostgresDSN: readerDSN, FilestoreRoot: e.f.root,
		HTTP: security.HTTPConfig{
			RepoKey: repo, Challenge: dseChallenge, FailureThreshold: 3,
			FailureWindow: time.Minute, BlockDuration: time.Minute, MaxTrackedUsers: 16,
		},
		Upload: process.UploadConfig{
			WriterDSN: roleDSN(writer), ReservationDSN: roleDSN(allocator),
			PublicOrigin: "https://example.test", MaxBodyBytes: 1024,
		},
	}
	ready := false
	if err := process.Run(ctx, cfg, func(string) { ready = true }); !errors.Is(err, process.ErrStartup) || ready {
		t.Fatalf("missing lock entry: ready=%t err=%v", ready, err)
	}
	install, err := e.f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer install.Rollback()
	if err := security.InstallUploadCommitLock(ctx, install, schema, writer); err != nil {
		t.Fatal(err)
	}
	if err := install.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.f.adm.ExecContext(ctx, "ALTER ROLE "+allocator+" SET default_transaction_read_only = on"); err != nil {
		t.Fatal(err)
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, 15*time.Second)
	defer cancelStartup()
	ready = false
	err = process.Run(startupCtx, cfg, func(string) {
		ready = true
		cancelStartup()
	})
	cancelStartup()
	if !errors.Is(err, process.ErrStartup) || ready {
		t.Fatalf("read-only allocator: ready=%t err=%v", ready, err)
	}
	if _, err := e.f.adm.ExecContext(ctx, "ALTER ROLE "+allocator+" RESET default_transaction_read_only"); err != nil {
		t.Fatal(err)
	}

	runCtx, stop := context.WithCancel(ctx)
	address := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- process.Run(runCtx, cfg, func(addr string) { address <- addr }) }()
	defer func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("artifacts process shutdown: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("artifacts process did not stop")
		}
	}()
	var base string
	select {
	case addr := <-address:
		base = "http://" + addr
	case err := <-done:
		done <- err
		t.Fatalf("artifacts process startup: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	client := dpClient(t)
	dpAssertHealth(t, ctx, client, base)
	const payload = "artifact process payload"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/artifactory/"+repo+"/dir/process.bin", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, secret)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d", res.StatusCode)
	}
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get.SetBasicAuth(user, secret)
	res, err = client.Do(get)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK || string(body) != payload {
		t.Fatalf("GET after PUT: status=%d err=%v", res.StatusCode, err)
	}
}

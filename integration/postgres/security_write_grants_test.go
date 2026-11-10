package postgresit

import (
	"errors"
	"testing"

	"github.com/0ldlight/bf/internal/repository"
	"github.com/0ldlight/bf/internal/security"
)

func TestPersistedUploadGrants(t *testing.T) {
	e, _ := dseSetup(t)
	ctx := e.f.ctx
	const repoKey = "saveR"
	const username = "writer"
	const secret = "Synthetic-Writer-Passphrase!"
	dseRepo(t, e, repoKey)
	id := dseUser(t, e, username, secret, true, true)
	dseUserGrant(t, e, repoKey, id)
	p, err := e.backend.Authenticate(ctx, username, secret)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := e.backend.LookupRepository(ctx, repoKey)
	if err != nil {
		t.Fatal(err)
	}
	path, err := repository.NewPath(repoKey, "dir", "new.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := e.backend.CanDeploy(ctx, p, repo, path); got || !errors.Is(err, security.ErrSecurityStore) {
		t.Fatalf("pre-migration deploy = (%t, %v)", got, err)
	}
	tx, err := e.f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, security.WriteGrantsSQL); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := e.backend.Validate(ctx); err != nil {
		t.Fatalf("read schema validation after migration: %v", err)
	}
	check := func(label string, catalog security.RepositoryInfo, wantDeploy, wantDelete bool) {
		t.Helper()
		gotDeploy, deployErr := e.backend.CanDeploy(ctx, p, catalog, path)
		gotDelete, deleteErr := e.backend.CanDelete(ctx, p, catalog, path)
		if deployErr != nil || deleteErr != nil || gotDeploy != wantDeploy || gotDelete != wantDelete {
			t.Fatalf("%s: deploy=(%t, %v) delete=(%t, %v), want (%t, %t)", label, gotDeploy, deployErr, gotDelete, deleteErr, wantDeploy, wantDelete)
		}
	}
	check("read and effective admin alone", repo, false, false)
	dghExec(t, e.f, "INSERT INTO security_write_grants(repo_key,user_id,action,scope) VALUES($1,$2,'deploy','**')", repoKey, id)
	check("direct deploy", repo, true, false)
	if got, err := security.CheckUploadPermissions(ctx, p, repo, path, e.backend, e.nodes, false, nil, nil); !got || err != nil {
		t.Fatalf("preflight for missing file with deploy = (%t, %v)", got, err)
	}
	dghExec(t, e.f, "INSERT INTO nodes(node_id,node_type,repo,node_path,node_name,depth) VALUES(721,1,$1,'dir','new.bin',2)", repoKey)
	if got, err := security.CheckUploadPermissions(ctx, p, repo, path, e.backend, e.nodes, false, nil, nil); got || err != nil {
		t.Fatalf("preflight for overwrite with deploy alone = (%t, %v)", got, err)
	}

	group := dseGroup(t, e, "writers")
	dseMember(t, e, id, group)
	dghExec(t, e.f, "INSERT INTO security_write_grants(repo_key,group_id,action,scope) VALUES($1,$2,'delete','**')", repoKey, group)
	check("direct and group grant", repo, true, true)
	if got, err := security.CheckUploadPermissions(ctx, p, repo, path, e.backend, e.nodes, false, nil, nil); !got || err != nil {
		t.Fatalf("preflight for overwrite with both grants = (%t, %v)", got, err)
	}
	dghExec(t, e.f, "DELETE FROM security_group_members WHERE user_id=$1 AND group_id=$2", id, group)
	check("membership revoked", repo, true, false)
	dghExec(t, e.f, "UPDATE security_users SET enabled=false WHERE user_id=$1", id)
	check("disabled user", repo, false, false)
	dghExec(t, e.f, "UPDATE security_users SET enabled=true WHERE user_id=$1", id)
	dghExec(t, e.f, "UPDATE security_repositories SET revision=revision+1 WHERE repo_key=$1", repoKey)
	check("stale repository revision", repo, false, false)
	current, err := e.backend.LookupRepository(ctx, repoKey)
	if err != nil {
		t.Fatal(err)
	}
	check("refreshed repository revision", current, true, false)
	dghExec(t, e.f, "DELETE FROM security_write_grants WHERE repo_key=$1 AND user_id=$2", repoKey, id)
	check("deploy revoked", current, false, false)
	if got, err := e.backend.CanRead(ctx, p, current, path); !got || err != nil {
		t.Fatalf("read after write revocation = (%t, %v)", got, err)
	}
}

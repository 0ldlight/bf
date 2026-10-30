package postgresit

import (
	"net/http"
	"net/url"
	"syscall"
	"testing"
)

// TestProcessDownloadCommandExecProfile exercises the explicit non-caching
// query profile through the actual race-built command, PostgreSQL, filestore,
// credentials and TCP handler. Repeated requests cross independent SQL read
// transactions in the fixture's schema. A direct PostgreSQL fixture does not
// prove Supavisor startup-parameter handling or isolation across reassigned
// backend sessions; those require separate deployment acceptance.
// 真实命令/数据库回归覆盖显式 exec 画像；不能代替 Supavisor 跨后端隔离验收。
func TestProcessDownloadCommandExecProfile(t *testing.T) {
	fx := dpSetup(t)
	ctx := fx.f.ctx
	u, err := url.Parse(fx.dsn)
	if err != nil {
		t.Fatal("exec profile: fixture DSN parse failed (value not logged)")
	}
	query := u.Query()
	query.Set("default_query_exec_mode", "exec")
	u.RawQuery = query.Encode()
	execDSN := u.String()
	cfg := dpBaseConfig(execDSN, fx.f.root)
	path := dpWriteFixtureConfig(t, fx, cfg)
	c := dpSpawn(t, ctx, dpDownloadEnv(path))
	dpGuard(c, execDSN, fx.dsn, fx.dsnSecret, fx.f.root, fx.aliceRaw, fx.carolRaw, "Controlled-exec-wrong-1", path)
	addr := dpWaitReady(t, c)
	base := "http://" + addr
	client := dpClient(t)
	artifactURL := base + "/artifactory/" + dpRepoKey + "/dir/a.bin"
	dpAssertHealth(t, ctx, client, base)

	status, header, body, length := dpRequest(t, ctx, client, http.MethodGet, artifactURL, fx.aliceUser, "Controlled-exec-wrong-1")
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
	dseChallengeOf(t, header)
	status, header, body, length = dpRequest(t, ctx, client, http.MethodGet, artifactURL, fx.carolUser, fx.carolRaw)
	dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", false)
	dghNoChallenge(t, header)

	sha1Hex, md5Hex, sha256Hex := dghDigest(dpPayload)
	assertRepresentation := func(method string) {
		t.Helper()
		status, header, body, length := dpRequest(t, ctx, client, method, artifactURL, fx.aliceUser, fx.aliceRaw)
		wantBody := dpPayload
		if method == http.MethodHead {
			wantBody = ""
		}
		if status != http.StatusOK || body != wantBody || length != int64(len(dpPayload)) {
			t.Fatalf("exec profile: %s representation mismatch: status=%d length=%d body-bytes=%d", method, status, length, len(body))
		}
		dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", dpResourceModified41)
		dghNoChallenge(t, header)
	}

	for cycle := 0; cycle < 4; cycle++ {
		// Every lookup opens and releases its own read transaction. Changing
		// the grant between requests also proves there is no stale positive
		// authorization result in the exec-configured command.
		// 每次查询独立读事务；撤权后的下一次请求必须重新查询授权。
		assertRepresentation(http.MethodGet)
		assertRepresentation(http.MethodHead)
		dpExec(t, fx.f, "DELETE FROM security_read_grants WHERE repo_key=$1 AND user_id=$2", dpRepoKey, fx.aliceID)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			status, header, body, length := dpRequest(t, ctx, client, method, artifactURL, fx.aliceUser, fx.aliceRaw)
			dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", method == http.MethodHead)
			dghNoChallenge(t, header)
		}
		dpUserGrant(t, fx.f, dpRepoKey, fx.aliceID)
		assertRepresentation(http.MethodGet)
	}

	dpSignal(t, c, syscall.SIGTERM)
	if code := dpWaitExit(t, c, dpSignalExitTimeout); code != 0 {
		t.Fatalf("exec profile: SIGTERM exit code = %d, want 0 (output not logged)", code)
	}
	if got := c.stdout.text(); got != "" {
		t.Fatalf("exec profile: command wrote %d bytes to stdout (content not logged)", len(got))
	}
	dpAssertPrivate(t, c)
	dpPortReusable(t, addr)
}

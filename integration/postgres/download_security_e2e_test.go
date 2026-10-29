// 真实 PostgreSQL 上的可信下载安全链 e2e：显式应用 security SchemaSQL、经产品
// GenerateCredential 种真实用户/组/仓库/grant 行，用真实 security.Store 认证、
// repository/postgres.NewReadPool 短事务查节点、filestore 真字节与真实 net/http
// 传输验证身份→目录→授权→下载全链。不伪造凭据校验器或读策略，不模拟 SQL。
// Real-PostgreSQL e2e for the trusted download security chain: the security
// SchemaSQL is applied explicitly, real user/group/repository/grant rows are
// seeded through the product GenerateCredential, and identity→catalog→grant→
// download runs over the real security.Store, the short-transaction
// repository/postgres.NewReadPool, real filestore bytes and a real net/http
// transport. No fake credential verifier or read policy, no SQL simulation.
package postgresit

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
	"github.com/0ldlight/bf/internal/security"
)

// dseTimeout 是 NewStore/NewReadPool 的有限读超时：远小于产品上限1分钟。
// dseTimeout is the bounded read timeout for NewStore/NewReadPool, well
// under the product's one-minute cap.
const dseTimeout = 10 * time.Second

// dseChallenge 与 BIN-199 同款固定 Basic challenge；只作可信入口字面量。
// dseChallenge reuses the BIN-199 fixed Basic challenge as the trusted-entry
// literal.
const dseChallenge = `Basic realm="binflow"`

// dseReadStore 只对真实 NewReadPool 的节点查询做透明计数；SQL 与事务语义
// 全部委托产品实现，不为测试另写查询分支。
// dseReadStore only counts lookups on the real NewReadPool delegation; the
// SQL and transaction semantics stay entirely in the product code, with no
// test-side query branch.
type dseReadStore struct {
	inner   localrepo.FileReadStore
	lookups atomic.Int64
}

// lookupCount 是跨 HTTP goroutine 安全的查询计数快照。
// lookupCount is a cross-HTTP-goroutine-safe lookup-count snapshot.
func (s *dseReadStore) lookupCount() int64 { return s.lookups.Load() }

func (s *dseReadStore) LookupSnapshotSource(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	s.lookups.Add(1)
	return s.inner.LookupSnapshotSource(ctx, path)
}

// dseEnv 汇总一个已应用 security schema 的隔离环境：真实 Store 后端、计数
// ReadPool、计数打开器与真实传输服务。所有依赖生命周期归测试清理。
// dseEnv bundles one isolated environment with the security schema applied:
// the real Store backend, the counting ReadPool, the counting opener and the
// real transport server. Test cleanup owns every dependency lifetime.
type dseEnv struct {
	f       splFixture
	backend *security.Store
	nodes   *dseReadStore
	opener  *dghOpener
	reports *dghReports
}

// dseSetup 建立节点/登记夹具并显式应用 security SchemaSQL；不自动 DDL 之外
// 的任何种子行（bootstrap 本身只写版本1，零默认身份/授权由调用方断言）。
// dseSetup builds the node/registry fixture and applies the security
// SchemaSQL explicitly; no seed rows beyond the bootstrap's own version row
// (zero default identities/grants is asserted by callers).
// dseScenarioContext 限制完整多阶段 TCP/SQL 场景的生命周期，而非单次
// 操作预算。既有节点夹具的30秒上下文不适合连续撤权/恢复矩阵；每个产品
// Store/ReadPool 操作仍为10秒，整包5分钟上限保持不变。
// dseScenarioContext bounds the entire multi-stage TCP/SQL scenario rather
// than one operation. Product operations remain 10s and the package keeps
// its 5m timeout; the generic 30s node-fixture lifetime is unchanged.
func dseScenarioContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func dseSetup(t *testing.T) (dseEnv, *binary.Service) {
	t.Helper()
	f, svc := sslSetup(t)
	f.ctx = dseScenarioContext(t)
	if _, err := f.db.ExecContext(f.ctx, security.SchemaSQL); err != nil {
		t.Fatalf("apply security schema: %v", err)
	}
	backend, err := security.NewStore(f.db, dseTimeout)
	if err != nil {
		t.Fatalf("security.NewStore: %v", err)
	}
	pool, err := refpg.NewReadPool(f.db, dseTimeout)
	if err != nil {
		t.Fatalf("refpg.NewReadPool: %v", err)
	}
	return dseEnv{
		f:       f,
		backend: backend,
		nodes:   &dseReadStore{inner: pool},
		opener:  &dghOpener{inner: f.objects},
		reports: &dghReports{},
	}, svc
}

// dseServe 构造真实组合根处理器并挂到真实 net/http 服务；调用前必须已经
// 插入受支持仓库行（构造器会校验 schema 与目录事实）。
// dseServe builds the real composition-root handler and serves it over real
// net/http; a supported repository row must already exist because the
// constructor validates schema and catalog facts.
func dseServe(t *testing.T, e dseEnv, threshold int) *httptest.Server {
	t.Helper()
	handler, err := security.NewAuthenticatedDownload(e.f.ctx, security.HTTPConfig{
		RepoKey:          "saveR",
		Challenge:        dseChallenge,
		FailureThreshold: threshold,
		FailureWindow:    time.Minute,
		BlockDuration:    time.Minute,
		MaxTrackedUsers:  64,
		Report:           e.reports.add,
	}, e.backend, e.nodes, e.opener)
	if err != nil {
		t.Fatalf("security.NewAuthenticatedDownload: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// dseDo 执行一次带 Basic 凭据的真实请求并读尽正文，返回错误而非直接失败：
// 并发 goroutine 无法安全持有 *testing.T，结果统一交回主 goroutine 断言。
// 空用户名表示不带 Authorization 头。
// dseDo performs one real-client request with Basic credentials, drains the
// body and returns errors instead of failing directly: concurrent goroutines
// cannot safely hold a *testing.T, so results are asserted back on the main
// goroutine. An empty username sends no Authorization header at all.
func dseDo(ts *httptest.Server, method, path, username, password string) (int, http.Header, string, int64, error) {
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		return 0, nil, "", 0, err
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		return 0, nil, "", 0, err
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return 0, nil, "", 0, readErr
	}
	return resp.StatusCode, resp.Header, string(body), resp.ContentLength, nil
}

// dseRequest 是 dseDo 的主 goroutine 包装：任何传输/读取失败直接致命。
// dseRequest is the main-goroutine wrapper over dseDo: any transport/read
// failure is fatal.
func dseRequest(t *testing.T, ts *httptest.Server, method, path, username, password string) (int, http.Header, string, int64) {
	t.Helper()
	status, header, body, length, err := dseDo(ts, method, path, username, password)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return status, header, body, length
}

// dseChallengeOf 断言本次真实响应携带逐字 challenge。
// dseChallengeOf asserts the actual response carries the challenge verbatim.
func dseChallengeOf(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("WWW-Authenticate"); got != dseChallenge {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, dseChallenge)
	}
}

// dseCount 用管理池独立统计 security 表行数。
// dseCount counts security table rows through the independent admin pool.
func dseCount(t *testing.T, e dseEnv, table string) int {
	t.Helper()
	var n int
	if err := e.f.adm.QueryRowContext(e.f.ctx,
		fmt.Sprintf("SELECT count(*) FROM %s.%s", e.f.schema, table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// dseUser 经产品 GenerateCredential 用受控合成口令插入真实用户行并返回
// 数据库身份；口令长度满足受控原始值≥14字节的种子约束。
// dseUser inserts a real user row through the product GenerateCredential with
// a controlled synthetic raw secret (>=14 bytes) and returns the database
// identity.
func dseUser(t *testing.T, e dseEnv, username, raw string, enabled, admin bool) int64 {
	t.Helper()
	encoded, err := security.GenerateCredential(raw)
	if err != nil {
		t.Fatalf("GenerateCredential(%q): %v", username, err)
	}
	var id int64
	if err := e.f.db.QueryRowContext(e.f.ctx,
		"INSERT INTO security_users(username, encoded_secret, enabled, effective_admin) VALUES($1,$2,$3,$4) RETURNING user_id",
		username, encoded, enabled, admin).Scan(&id); err != nil {
		t.Fatalf("insert user %q: %v", username, err)
	}
	return id
}

// dseRepo 插入唯一受支持的 local/generic 仓库事实行。
// dseRepo inserts the single supported local/generic repository fact row.
func dseRepo(t *testing.T, e dseEnv, key string) {
	t.Helper()
	dghExec(t, e.f, "INSERT INTO security_repositories(repo_key,kind,package_type,revision) VALUES($1,'local','generic',1)", key)
}

// dseUserGrant / dseGroupGrant 只插入仓库级 '**' 读授权：本切片明确拒绝
// 更窄/通配/排除输入，不把任何输入放宽成允许。
// dseUserGrant / dseGroupGrant insert repository-wide '**' read grants only:
// this slice explicitly rejects narrower/glob/exclude inputs instead of
// widening anything into an allow.
func dseUserGrant(t *testing.T, e dseEnv, key string, userID int64) {
	t.Helper()
	dghExec(t, e.f, "INSERT INTO security_read_grants(repo_key,user_id,scope) VALUES($1,$2,'**')", key, userID)
}

func dseGroup(t *testing.T, e dseEnv, name string) int64 {
	t.Helper()
	var id int64
	if err := e.f.db.QueryRowContext(e.f.ctx,
		"INSERT INTO security_groups(group_name) VALUES($1) RETURNING group_id", name).Scan(&id); err != nil {
		t.Fatalf("insert group %q: %v", name, err)
	}
	return id
}

func dseMember(t *testing.T, e dseEnv, userID, groupID int64) {
	t.Helper()
	dghExec(t, e.f, "INSERT INTO security_group_members(user_id,group_id) VALUES($1,$2)", userID, groupID)
}

func dseGroupGrant(t *testing.T, e dseEnv, key string, groupID int64) {
	t.Helper()
	dghExec(t, e.f, "INSERT INTO security_read_grants(repo_key,group_id,scope) VALUES($1,$2,'**')", key, groupID)
}

// dseNewSecret 经产品编码器生成新口令并写回用户行，返回受控新原始值。
// dseNewSecret generates a new secret through the product encoder, writes it
// back to the user row and returns the controlled new raw value.
func dseNewSecret(t *testing.T, e dseEnv, username string) string {
	t.Helper()
	raw := "Controlled-rotate-" + username + "-15!"
	encoded, err := security.GenerateCredential(raw)
	if err != nil {
		t.Fatalf("GenerateCredential(rotate %q): %v", username, err)
	}
	dghExec(t, e.f, "UPDATE security_users SET encoded_secret=$2 WHERE username=$1", username, encoded)
	return raw
}

// TestSecurityDownloadIdentityAndCatalog 覆盖身份→目录主线与边界：缺
// security schema 时组合根构造拒绝；引导后零默认身份/授权；真实凭据下
// GET 完整字节、HEAD 同表示零 Open；错口令/未知用户/禁用用户/缺失或非
// Basic 凭据为不可区分 401+challenge；无授权 403 无 challenge 且零节点
// 查询；目录缺席 404（即使节点行存在）；不支持存储编码 500 且不计入失败
// 门；全程节点/登记/对象计数不变。
// TestSecurityDownloadIdentityAndCatalog covers the identity→catalog main
// line and boundaries: the composition root rejects construction while the
// security schema is missing; bootstrap leaves zero default identities or
// grants; real credentials yield full-byte GET and a same-representation
// HEAD with zero opens; wrong-password/unknown-user/disabled-user and
// missing or non-Basic credentials give indistinguishable 401+challenge;
// no-grant is 403 without challenge and zero node lookups; catalog absence
// is 404 even with a node row present; an unsupported stored encoding is 500
// without counting toward the failure gate; node/registry/object counters
// never change.
func TestSecurityDownloadIdentityAndCatalog(t *testing.T) {
	t.Run("reviewed database boundaries", dseRootBoundaries)
	f, svc := sslSetup(t)
	f.ctx = dseScenarioContext(t)

	// 缺 security schema：组合根必须拒绝构造（固定错误，绝不静默降级）。
	// Missing security schema: the composition root must refuse construction
	// with the fixed error, never silently degrading.
	missingStore, err := security.NewStore(f.db, dseTimeout)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	missingPool, err := refpg.NewReadPool(f.db, dseTimeout)
	if err != nil {
		t.Fatalf("NewReadPool: %v", err)
	}
	if _, err := security.NewAuthenticatedDownload(f.ctx, security.HTTPConfig{
		RepoKey: "saveR", Challenge: dseChallenge, FailureThreshold: 3,
		FailureWindow: time.Minute, BlockDuration: time.Minute, MaxTrackedUsers: 64,
	}, missingStore, missingPool, f.objects); !errors.Is(err, security.ErrConfiguration) {
		t.Fatalf("missing-schema construction error = %v, want ErrConfiguration", err)
	}

	e := dseEnv{f: f, backend: missingStore, nodes: &dseReadStore{inner: missingPool}, opener: &dghOpener{inner: f.objects}, reports: &dghReports{}}
	if _, err := f.db.ExecContext(f.ctx, security.SchemaSQL); err != nil {
		t.Fatalf("apply security schema: %v", err)
	}
	// 引导后零默认行：无默认管理员、无默认授权（显式 bootstrap 只写版本1）。
	// After bootstrap there are zero default rows: no default admin and no
	// default grant (the explicit bootstrap writes only version 1).
	for _, table := range []string{"security_users", "security_groups", "security_repositories", "security_read_grants"} {
		if got := dseCount(t, e, table); got != 0 {
			t.Fatalf("%s seeded %d default rows, want 0", table, got)
		}
	}

	// 受支持仓库事实先行：构造器会校验目录；此刻仍无任何用户。
	// The supported repository fact comes first because the constructor
	// validates the catalog; no user exists yet at this point.
	dseRepo(t, e, "saveR")
	backend, err := security.NewStore(f.db, dseTimeout)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	e.backend = backend
	ts := dseServe(t, e, 3)
	dghSave(t, f, svc, &splClock{value: 41}, "dir/a.bin", "abc")
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	nodes, rows, files := dghCounts(t, f)
	sha1Hex, md5Hex, sha256Hex := dghDigest("abc")
	url := "/artifactory/saveR/dir/a.bin"

	// 零用户期：伪造凭据不得获得任何默认身份；401 带 challenge、零查询。
	// With zero users, fabricated credentials gain no default identity: 401
	// with challenge and zero lookups.
	status, header, body, length := dseRequest(t, ts, http.MethodGet, url, "admin", "admin-password-14")
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
	dseChallengeOf(t, header)
	if e.nodes.lookupCount() != 0 {
		t.Fatalf("fabricated identity reached node lookup: %d", e.nodes.lookupCount())
	}

	// 真实用户+直接授权：完整主线。
	// Real user with a direct grant: the full main line.
	dseUserGrant(t, e, "saveR", dseUser(t, e, "alice", "Controlled-alice-raw-16!", true, false))
	status, header, body, length = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-alice-raw-16!")
	if status != http.StatusOK || body != "abc" || length != 3 {
		t.Fatalf("alice GET = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", 41)

	// HEAD 同表示、零正文且零内容 Open（表示来自节点事实而非打开）。
	// HEAD keeps the representation, stays bodyless and adds zero content
	// opens (the representation comes from node facts, never an open).
	opensBeforeHead := e.opener.openCount()
	status, header, body, length = dseRequest(t, ts, http.MethodHead, url, "alice", "Controlled-alice-raw-16!")
	if status != http.StatusOK || body != "" || length != 3 {
		t.Fatalf("alice HEAD = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", 41)
	if got := e.opener.openCount(); got != opensBeforeHead {
		t.Fatalf("HEAD opened content: opens=%d, want %d", got, opensBeforeHead)
	}

	// 401 家族逐字节一致且不泄露用户名差异：错口令、未知用户、禁用用户、
	// 缺 Authorization、Bearer 方案。
	// The 401 family is byte-identical and leaks no username distinction:
	// wrong password, unknown user, disabled user, missing Authorization and
	// the Bearer scheme.
	dseUser(t, e, "bob", "Controlled-bob-raw-16!!", false, false)
	var bodies []string
	for _, tc := range []struct{ name, user, pass string }{
		{"wrong-password", "alice", "Controlled-wrong-raw1"},
		{"unknown-user", "ghost", "Controlled-ghost-raw"},
		{"disabled-user", "bob", "Controlled-bob-raw-16!!"},
	} {
		status, header, body, length = dseRequest(t, ts, http.MethodGet, url, tc.user, tc.pass)
		dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
		dseChallengeOf(t, header)
		bodies = append(bodies, body)
	}
	status, header, body, length = dseRequest(t, ts, http.MethodGet, url, "", "")
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
	dseChallengeOf(t, header)
	bodies = append(bodies, body)
	// HEAD 入口失败同样零正文、显式 Content-Length 且带 challenge。
	// A HEAD entry failure is bodyless with an explicit Content-Length and
	// the same challenge.
	status, header, body, length = dseRequest(t, ts, http.MethodHead, url, "ghost", "Controlled-ghost-raw")
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", true)
	dseChallengeOf(t, header)
	req, err := http.NewRequest(http.MethodGet, ts.URL+url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer controlled-token-value")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bearerBody, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusUnauthorized || string(bearerBody) != "unauthorized\n" {
		t.Fatalf("bearer = %d %q", resp.StatusCode, bearerBody)
	}
	if got := resp.Header.Get("WWW-Authenticate"); got != dseChallenge {
		t.Fatalf("bearer WWW-Authenticate = %q", got)
	}
	bodies = append(bodies, string(bearerBody))
	for _, got := range bodies {
		if got != bodies[0] {
			t.Fatalf("401 bodies differ: %q vs %q", got, bodies[0])
		}
		if strings.Contains(got, "alice") || strings.Contains(got, "ghost") || strings.Contains(got, "bob") {
			t.Fatalf("401 body leaks a username: %q", got)
		}
	}
	// 401 家族全部在节点查询之前结束：此前只有允许的 GET 与 HEAD 各查过一次。
	// The whole 401 family ends before any node lookup: only the earlier
	// allowed GET and HEAD have looked up once each.
	if got := e.nodes.lookupCount(); got != 2 {
		t.Fatalf("401 family changed lookups: %d, want 2 (the earlier GET+HEAD allows)", got)
	}

	// 有效身份、无授权：403 无 challenge、零查询（授权先于节点查询）。
	// Valid identity without a grant: 403 with no challenge and zero lookups
	// (authorization precedes node lookup).
	lookupsBefore := e.nodes.lookupCount()
	dseUser(t, e, "carol", "Controlled-carol-raw-3", true, false)
	status, header, body, length = dseRequest(t, ts, http.MethodGet, url, "carol", "Controlled-carol-raw-3")
	dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", false)
	dghNoChallenge(t, header)
	if e.nodes.lookupCount() != lookupsBefore {
		t.Fatalf("policy denial reached node lookup: %d", e.nodes.lookupCount())
	}

	// 目录缺席：URL 指向未知仓库键与删除仓库行都 404，即使节点行仍存在；
	// 目录事实独立于节点（countNodes 仍为1）。
	// Catalog absence: both an unknown URL repo key and a deleted repository
	// row give 404 even while the node row remains; catalog facts stay
	// independent of nodes (countNodes stays 1).
	status, header, body, length = dseRequest(t, ts, http.MethodGet, "/artifactory/otherR/dir/a.bin", "alice", "Controlled-alice-raw-16!")
	dghError(t, status, header, body, length, http.StatusNotFound, "not found\n", false)
	dghNoChallenge(t, header)
	dghExec(t, e.f, "DELETE FROM security_repositories WHERE repo_key='saveR'")
	status, header, body, length = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-alice-raw-16!")
	dghError(t, status, header, body, length, http.StatusNotFound, "not found\n", false)
	dghNoChallenge(t, header)
	if e.nodes.lookupCount() != lookupsBefore {
		t.Fatalf("catalog absence reached node lookup: %d", e.nodes.lookupCount())
	}
	if got := countNodes(t, f.adm, f.schema); got != 1 {
		t.Fatalf("node rows = %d, want 1 (catalog absence must not touch nodes)", got)
	}
	dseRepo(t, e, "saveR")
	dghExec(t, e.f, "INSERT INTO security_read_grants(repo_key,user_id,scope) SELECT 'saveR',user_id,'**' FROM security_users WHERE username='alice'")
	status, _, body, _ = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-alice-raw-16!")
	if status != http.StatusOK || body != "abc" {
		t.Fatalf("restored catalog GET = %d %q", status, body)
	}

	// 不支持存储编码：500 固定体、无 challenge、不计入失败门（恢复后可
	// 立即成功，不被误阻断）。
	// Unsupported stored encoding: fixed 500 body, no challenge, and no
	// failure-gate increment (after restoring, the next request succeeds
	// rather than being wrongly blocked).
	dghExec(t, e.f, "UPDATE security_users SET encoded_secret='bcrypt$$not-the-supported-profile' WHERE username='alice'")
	for i := 0; i < 3; i++ {
		status, header, body, length = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-alice-raw-16!")
		dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
		dghNoChallenge(t, header)
	}
	dseNewSecret(t, e, "alice") // 修好编码并轮换口令，同时验证新口令可用 / repair the encoding via rotation, also proving the new secret works
	status, _, body, _ = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-rotate-alice-15!")
	if status != http.StatusOK || body != "abc" {
		t.Fatalf("post-encoding-repair GET = %d %q", status, body)
	}

	// 真实 SQL 依赖故障也不积累凭据失败；修复表名后同一服务立即恢复。
	// Real SQL dependency failures do not accumulate credential failures.
	dghExec(t, e.f, "ALTER TABLE security_users RENAME TO security_users_offline")
	for i := 0; i < 3; i++ {
		status, header, body, length = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-rotate-alice-15!")
		dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
		dghNoChallenge(t, header)
	}
	dghExec(t, e.f, "ALTER TABLE security_users_offline RENAME TO security_users")
	status, _, body, _ = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-rotate-alice-15!")
	if status != http.StatusOK || body != "abc" {
		t.Fatalf("restored SQL dependency response=%d body=%q", status, body)
	}
	gotNodes, gotRows, gotFiles := dghCounts(t, f)
	if gotNodes != nodes || gotRows != rows || gotFiles != files {
		t.Fatalf("requests changed counters: nodes=%d/%d rows=%d/%d files=%d/%d",
			gotNodes, nodes, gotRows, rows, gotFiles, files)
	}
}

// TestSecurityDownloadRevocationAndGroups 覆盖同一服务存续期内的失效语义：
// 改密后旧凭据立即 401、新凭据 200；禁用、删除用户立即拒绝；组成员与组
// 授权变更下一次请求即生效；管理员标记撤销后不再免授权；删除仓库行转
// 目录 404。无正向缓存：每个决定都重读当前持久状态。
// TestSecurityDownloadRevocationAndGroups covers revocation semantics within
// one live server: after a password change the old secret is 401 and the new
// one 200; disabling or deleting a user rejects immediately; group membership
// and group-grant changes take effect on the next request; removing the
// effective_admin flag ends grant-free admin access; deleting the repository
// row turns into catalog 404. No positive cache exists: every decision rereads
// the current persisted state.
func TestSecurityDownloadRevocationAndGroups(t *testing.T) {
	e, svc := dseSetup(t)
	dseRepo(t, e, "saveR")
	aliceID := dseUser(t, e, "alice", "Controlled-alice-raw-16!", true, false)
	dseUserGrant(t, e, "saveR", aliceID)
	daveID := dseUser(t, e, "dave", "Controlled-dave-raw-15", true, false)
	groupID := dseGroup(t, e, "g1")
	dseMember(t, e, daveID, groupID)
	dseGroupGrant(t, e, "saveR", groupID)
	dseUser(t, e, "root", "Controlled-root-raw-14!", true, true) // effective_admin，无 grant 行 / effective_admin with no grant row
	ts := dseServe(t, e, 64)
	dghSave(t, e.f, svc, &splClock{value: 41}, "dir/a.bin", "abc")
	if err := e.f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	url := "/artifactory/saveR/dir/a.bin"
	requireGet := func(stage, user, pass string, wantStatus int, wantBody string) {
		t.Helper()
		lookupsBefore := e.nodes.lookupCount()
		status, header, body, length := dseRequest(t, ts, http.MethodGet, url, user, pass)
		if wantStatus == http.StatusOK {
			if status != wantStatus || body != wantBody || length != int64(len(wantBody)) {
				t.Fatalf("%s response=%d body=%q length=%d", stage, status, body, length)
			}
			sha1Hex, md5Hex, sha256Hex := dghDigest(wantBody)
			dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", 41)
			dghNoChallenge(t, header)
		} else {
			dghError(t, status, header, body, length, wantStatus, wantBody, false)
			if wantStatus == http.StatusUnauthorized {
				dseChallengeOf(t, header)
			} else {
				dghNoChallenge(t, header)
			}
			if e.nodes.lookupCount() != lookupsBefore {
				t.Fatalf("%s reached node lookup: %d", stage, e.nodes.lookupCount())
			}
		}
	}

	requireGet("baseline direct grant", "alice", "Controlled-alice-raw-16!", http.StatusOK, "abc")
	requireGet("baseline group grant", "dave", "Controlled-dave-raw-15", http.StatusOK, "abc")
	requireGet("baseline admin without grant", "root", "Controlled-root-raw-14!", http.StatusOK, "abc")
	dghExec(t, e.f, "DELETE FROM security_read_grants WHERE repo_key='saveR' AND user_id=$1", aliceID)
	requireGet("direct grant revoked", "alice", "Controlled-alice-raw-16!", http.StatusForbidden, "forbidden\n")
	dseUserGrant(t, e, "saveR", aliceID)
	requireGet("direct grant restored", "alice", "Controlled-alice-raw-16!", http.StatusOK, "abc")

	// 改密：旧凭据 401，新凭据 200，无需重启。
	// Password change: the old secret turns 401 and the new one 200 with no
	// restart.
	newAlice := dseNewSecret(t, e, "alice")
	requireGet("old secret after rotation", "alice", "Controlled-alice-raw-16!", http.StatusUnauthorized, "unauthorized\n")
	requireGet("new secret after rotation", "alice", newAlice, http.StatusOK, "abc")

	// 撤销管理员标记：下一次请求即 403，无 challenge。
	// Revoking the admin flag: the next request is 403 with no challenge.
	dghExec(t, e.f, "UPDATE security_users SET effective_admin=false WHERE username='root'")
	requireGet("admin flag revoked", "root", "Controlled-root-raw-14!", http.StatusForbidden, "forbidden\n")
	dghExec(t, e.f, "UPDATE security_users SET effective_admin=true WHERE username='root'")
	requireGet("admin flag restored", "root", "Controlled-root-raw-14!", http.StatusOK, "abc")

	// 组成员撤销：组授权仍在，但成员关系没了 → 403；恢复成员 → 200。
	// Membership revocation: the group grant stands but the membership is
	// gone → 403; restoring membership → 200.
	dghExec(t, e.f, "DELETE FROM security_group_members WHERE user_id=$1 AND group_id=$2", daveID, groupID)
	requireGet("membership removed", "dave", "Controlled-dave-raw-15", http.StatusForbidden, "forbidden\n")
	dseMember(t, e, daveID, groupID)
	requireGet("membership restored", "dave", "Controlled-dave-raw-15", http.StatusOK, "abc")

	// 组授权撤销：成员仍在 → 403；恢复授权 → 200。
	// Group-grant revocation: the membership stands → 403; restoring the
	// grant → 200.
	dghExec(t, e.f, "DELETE FROM security_read_grants WHERE group_id=$1", groupID)
	requireGet("group grant removed", "dave", "Controlled-dave-raw-15", http.StatusForbidden, "forbidden\n")
	dseGroupGrant(t, e, "saveR", groupID)
	requireGet("group grant restored", "dave", "Controlled-dave-raw-15", http.StatusOK, "abc")

	// 禁用：同体 401（本地隐私政策不区分禁用与坏口令）；恢复 → 200。
	// Disable: the same 401 body (local privacy policy keeps disabled and
	// wrong-password indistinguishable); re-enable → 200.
	dghExec(t, e.f, "UPDATE security_users SET enabled=false WHERE username='alice'")
	requireGet("disabled", "alice", newAlice, http.StatusUnauthorized, "unauthorized\n")
	dghExec(t, e.f, "UPDATE security_users SET enabled=true WHERE username='alice'")
	requireGet("re-enabled", "alice", newAlice, http.StatusOK, "abc")

	// 删除用户：级联清直接授权，身份立即消失 → 401。
	// User deletion: the cascade clears the direct grant and the identity is
	// gone immediately → 401.
	dghExec(t, e.f, "DELETE FROM security_users WHERE username='alice'")
	requireGet("user deleted", "alice", newAlice, http.StatusUnauthorized, "unauthorized\n")
	if got := dseCount(t, e, "security_read_grants"); got != 1 {
		t.Fatalf("direct grant survived user deletion: %d rows, want 1 (the group grant)", got)
	}

	// 删除仓库：目录 404 而节点行仍在；恢复 → 200。
	// Repository deletion: catalog 404 while the node row remains; restoring
	// → 200.
	dghExec(t, e.f, "DELETE FROM security_repositories WHERE repo_key='saveR'")
	requireGet("repository deleted", "dave", "Controlled-dave-raw-15", http.StatusNotFound, "not found\n")
	if got := countNodes(t, e.f.adm, e.f.schema); got != 1 {
		t.Fatalf("node rows = %d, want 1", got)
	}
	dseRepo(t, e, "saveR")
	dseGroupGrant(t, e, "saveR", groupID)
	requireGet("repository restored", "dave", "Controlled-dave-raw-15", http.StatusOK, "abc")
}

// TestSecurityDownloadFailureAndIsolation 覆盖有界失败门与并发隔离：阈值内
// 错口令逐次 401，达到阈值后同身份即使凭据正确也 403+challenge 且零节点
// 工作，独立身份不受牵连；编码/后端失败不计入门；成功认证清除计数；并发
// 混合请求无身份串扰（各自 200/403，查询/打开计数精确）。
// TestSecurityDownloadFailureAndIsolation covers the bounded failure gate and
// concurrent isolation: below-threshold wrong passwords are 401 each; at the
// threshold the same identity is blocked 403+challenge with zero node work
// even with correct credentials while independent identities stay unaffected;
// encoding/backend failures never count; a successful authentication clears
// the counter; concurrent mixed requests show no identity mixing (each side
// keeps its 200/403 and the lookup/open counts stay exact).
func TestSecurityDownloadFailureAndIsolation(t *testing.T) {
	e, svc := dseSetup(t)
	dseRepo(t, e, "saveR")
	dseUserGrant(t, e, "saveR", dseUser(t, e, "alice", "Controlled-alice-raw-16!", true, false))
	dseUserGrant(t, e, "saveR", dseUser(t, e, "eve", "Controlled-eve-raw-15!", true, false))
	dseUser(t, e, "carol", "Controlled-carol-raw-3", true, false) // 有效身份、无授权 / valid identity, no grant
	ts := dseServe(t, e, 3)
	dghSave(t, e.f, svc, &splClock{value: 41}, "dir/a.bin", "abc")
	if err := e.f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	url := "/artifactory/saveR/dir/a.bin"

	// 阈值3：三次错口令各 401；第四次凭据正确仍被入口 403+challenge 阻断，
	// 且不做任何节点工作（查询零增量）。
	// Threshold 3: three wrong passwords give 401 each; the fourth request is
	// blocked 403+challenge at the entry even with correct credentials and
	// performs zero node work (zero lookup delta).
	for i := 0; i < 3; i++ {
		status, header, body, length := dseRequest(t, ts, http.MethodGet, url, "eve", "Controlled-wrong-raw1")
		dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
		dseChallengeOf(t, header)
	}
	lookupsBefore := e.nodes.lookupCount()
	opensBefore := e.opener.openCount()
	status, header, body, length := dseRequest(t, ts, http.MethodGet, url, "eve", "Controlled-eve-raw-15!")
	dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", false)
	dseChallengeOf(t, header)
	if e.nodes.lookupCount() != lookupsBefore || e.opener.openCount() != opensBefore {
		t.Fatalf("blocked entry did node work: lookups=%d/%d opens=%d/%d",
			e.nodes.lookupCount(), lookupsBefore, e.opener.openCount(), opensBefore)
	}

	// 独立身份不受牵连：alice 立即 200。
	// Independent identities stay unaffected: alice succeeds immediately.
	status, _, body, _ = dseRequest(t, ts, http.MethodGet, url, "alice", "Controlled-alice-raw-16!")
	if status != http.StatusOK || body != "abc" {
		t.Fatalf("independent alice after eve block = %d %q", status, body)
	}

	// 成功认证清除失败计数：门按身份键计数，用 frank 验证「两次失败→成功→
	// 再两次失败→成功」始终不被误阻断。
	// A successful authentication clears the failure counter: the gate keys
	// per identity, and frank proves "two failures → success → two more
	// failures → success" is never wrongly blocked.
	dseUserGrant(t, e, "saveR", dseUser(t, e, "frank", "Controlled-frank-raw", true, false))
	for i := 0; i < 2; i++ {
		dseRequest(t, ts, http.MethodGet, url, "frank", "Controlled-wrong-raw1")
	}
	status, _, _, _ = dseRequest(t, ts, http.MethodGet, url, "frank", "Controlled-frank-raw")
	if status != http.StatusOK {
		t.Fatalf("frank success after two failures = %d, want 200", status)
	}
	for i := 0; i < 2; i++ {
		dseRequest(t, ts, http.MethodGet, url, "frank", "Controlled-wrong-raw1")
	}
	status, _, _, _ = dseRequest(t, ts, http.MethodGet, url, "frank", "Controlled-frank-raw")
	if status != http.StatusOK {
		t.Fatalf("frank success after reset = %d, want 200 (success cleared the counter)", status)
	}

	// 非法 Authorization 结构：重复值与 Bearer 均 401+challenge，不触后端。
	// Malformed Authorization structure: duplicate values and Bearer are
	// 401+challenge without touching the backend.
	req, err := http.NewRequest(http.MethodGet, ts.URL+url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header["Authorization"] = []string{"Basic " + basicOf("alice", "Controlled-alice-raw-16!"), "Basic " + basicOf("alice", "Controlled-wrong-raw1")}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	dupBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || string(dupBody) != "unauthorized\n" || resp.Header.Get("WWW-Authenticate") != dseChallenge {
		t.Fatalf("duplicate Authorization = %d %q challenge=%q", resp.StatusCode, dupBody, resp.Header.Get("WWW-Authenticate"))
	}

	// 并发混合请求：4×alice(200) 与 4×carol(403) 并发打同一真实服务；各自
	// 状态正确、无身份串扰，查询/打开计数精确等于允许数。
	// Concurrent mixed requests: 4×alice (200) and 4×carol (403) hit the same
	// real server concurrently; each goroutine sees its own correct status
	// with no identity mixing and the lookup/open counts stay exact.
	lookupsBefore = e.nodes.lookupCount()
	opensBefore = e.opener.openCount()
	type result struct {
		user   string
		status int
		body   string
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			status, _, body, _, err := dseDo(ts, http.MethodGet, url, "alice", "Controlled-alice-raw-16!")
			results <- result{"alice", status, fmt.Sprintf("%s|%v", body, err)}
		}()
		go func() {
			defer wg.Done()
			status, _, body, _, err := dseDo(ts, http.MethodGet, url, "carol", "Controlled-carol-raw-3")
			results <- result{"carol", status, fmt.Sprintf("%s|%v", body, err)}
		}()
	}
	wg.Wait()
	close(results)
	allowed, denied := 0, 0
	for got := range results {
		// 传输错误以 "|<err>" 形式并入正文断言：成功请求必须是 "字节|<nil>"。
		// Transport errors fold into the body assertion as "|<err>": a
		// successful request must read exactly "bytes|<nil>".
		switch got.user {
		case "alice":
			if got.status != http.StatusOK || got.body != "abc|<nil>" {
				t.Errorf("concurrent alice = %d %q", got.status, got.body)
			}
			allowed++
		case "carol":
			if got.status != http.StatusForbidden || got.body != "forbidden\n|<nil>" {
				t.Errorf("concurrent carol = %d %q", got.status, got.body)
			}
			denied++
		}
	}
	if allowed != 4 || denied != 4 {
		t.Fatalf("concurrent mix counts: allowed=%d denied=%d, want 4/4", allowed, denied)
	}
	if got := e.nodes.lookupCount() - lookupsBefore; got != 4 {
		t.Fatalf("concurrent lookups = %d, want 4 (denials never look up)", got)
	}
	if got := e.opener.openCount() - opensBefore; got != 4 {
		t.Fatalf("concurrent opens = %d, want 4", got)
	}
}

// basicOf 生成测试侧 Basic 头值；只用于构造畸形/对照请求，不参与产品身份。
// basicOf builds a Basic header value on the test side; it only shapes
// malformed/contrast requests and never participates in product identity.
func basicOf(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

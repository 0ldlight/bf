// 真实 PostgreSQL 上的普通 PUT 端到端回归：真实 net/http 环回 → 真实
// NewUploadSaver（发布/懒节点事务/真实 Current+Reserve/一次 Commit）→ 201
// 投影 → 现有 NewDownloadHandler+ReadPool+真实 filestore.Open 回读。授权为
// 显式测试写/读端口（声明而非性能证明）；SQL 与本地摘要为独立 oracle。
// Real-PostgreSQL end-to-end regression for the ordinary PUT: a real
// net/http loopback drives the real NewUploadSaver (publication, lazy node
// transaction, real Current+Reserve, exactly one Commit), asserts the 201
// projection and reads back through the existing NewDownloadHandler plus
// ReadPool and the real filestore.Open. Authorization uses explicit TEST
// write/read ports (a declaration, never a proof); SQL and local digests are
// the independent oracle.
package postgresit

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	uploadpg "github.com/0ldlight/bf/internal/localrepo/postgres"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// 固定测试事实：受信起点、固定毫秒钟与冻结的 201 表示常量。
// Fixed test facts: the trusted origin, fixed millis clock values and the
// frozen 201 representation constants.
const (
	uhpOrigin        = "http://files.example.com"
	uhpMillisOne     = int64(1_700_000_000_123)
	uhpMillisTwo     = int64(1_700_000_000_456)
	uhpItemCreated   = "application/vnd.org.jfrog.artifactory.storage.ItemCreated+json;charset=UTF-8"
	uhpCreatedLayout = "2006-01-02T15:04:05.000Z07:00"
)

// uhpClock 是固定毫秒钟，测试在每次 PUT 前串行设置。
// uhpClock is the fixed-millis clock; the test sets it serially per PUT.
type uhpClock struct{ millis int64 }

func (c *uhpClock) NowMillis() int64 { return c.millis }
func (c *uhpClock) set(v int64)      { c.millis = v }

// uhpCountingSaver 是保存端口 tracking wrapper：只计数然后调真实 Saver。
// uhpCountingSaver is the save-port tracking wrapper: it only counts, then
// calls the real Saver.
type uhpCountingSaver struct {
	inner *uploadpg.UploadSaver
	calls atomic.Int64
}

func (s *uhpCountingSaver) Save(ctx context.Context, body io.Reader, in localrepo.UploadInput) (repository.FileSnapshotSource, error) {
	s.calls.Add(1)
	return s.inner.Save(ctx, body, in)
}

// uhpAuth 是显式测试写授权端口（声明而靈权证明），返回真实复制的 ASCII
// 操作者；产品无任何默认放行参与本测试。
// uhpAuth is the explicit TEST write-authorization port (a declaration, not
// a proof) returning a really-copied ASCII operator; no product
// default-allow takes part here.
type uhpAuth struct {
	mu        sync.Mutex
	decision  httpserver.UploadAuthDecision
	challenge string
	operator  []uint16
	calls     int64
}

func (a *uhpAuth) Authorize(_ context.Context, _ *http.Request, _ repository.Path) (httpserver.UploadAuthResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	return httpserver.UploadAuthResult{Decision: a.decision, Challenge: a.challenge, OperatorUTF16: a.operator}, nil
}

func (a *uhpAuth) configure(d httpserver.UploadAuthDecision, challenge string, operator []uint16) {
	a.mu.Lock()
	a.decision, a.challenge, a.operator = d, challenge, operator
	a.mu.Unlock()
}

func (a *uhpAuth) callCount() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// uhpReadAuth 是显式测试读授权端口：恒 Allow 只计数，不与写端口混用。
// uhpReadAuth is the explicit TEST read-authorization port: always Allow,
// counting only, never doubling as the write port.
type uhpReadAuth struct{ calls atomic.Int64 }

func (a *uhpReadAuth) Authorize(_ context.Context, _ *http.Request, _ repository.Path) (httpserver.DownloadAuthResult, error) {
	a.calls.Add(1)
	return httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}, nil
}

// uhpReports 只收集固定失败类别，断言只看类别。
// uhpReports collects only the fixed failure categories.
type uhpReports struct {
	mu   sync.Mutex
	seen []httpserver.UploadFailure
}

func (r *uhpReports) add(f httpserver.UploadFailure) {
	r.mu.Lock()
	r.seen = append(r.seen, f)
	r.mu.Unlock()
}

func (r *uhpReports) count(f httpserver.UploadFailure) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, got := range r.seen {
		if got == f {
			n++
		}
	}
	return n
}

// uhpFixture 组合端到端真实依赖：隔离 schema、两连接池、真实 Publisher/
// filestore/Registry、真实 NewUploadSaver 与两个真实 handler。
// uhpFixture composes the real dependencies: the isolated schema, the
// two-connection pool, the real Publisher/filestore/Registry, the real
// NewUploadSaver and both real handlers.
type uhpFixture struct {
	db, adm *sql.DB
	schema  string
	store   *filestore.Store
	root    string
	clock   *uhpClock
	saver   *uhpCountingSaver
	auth    *uhpAuth
	reports *uhpReports
	ts      *httptest.Server
}

// uhpOracle 投影成兄弟 SaveWorkUnit oracle 所需的值形状。
// uhpOracle projects into the value shape the sibling oracles need.
func (f uhpFixture) uhpOracle() usFixture {
	return usFixture{db: f.db, adm: f.adm, schema: f.schema, store: f.store, root: f.root}
}

// uhpSetup 建立全部真实依赖并启动带 ReadHeaderTimeout/ReadTimeout/
// WriteTimeout 的真实环回服务；PUT 与 GET/HEAD 由测试自有 mux 按方法手工
// 分派（不改生产路由）；nodeFixture 不持有事务，不钉连接。
// uhpSetup builds the real dependencies and starts the real loopback server
// with ReadHeaderTimeout/ReadTimeout/WriteTimeout; PUT and GET/HEAD are
// dispatched manually by method through a test-owned mux (production routes
// untouched); nodeFixture holds no open transaction and pins no connection.
func uhpSetup(t *testing.T, maxBody int64) uhpFixture {
	t.Helper()
	db, schema := nodeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, refpg.ReservationSchemaSQL); err != nil {
		t.Fatalf("apply reservation schema: %v", err)
	}
	db.SetMaxOpenConns(2)
	svc, store, root := cfBinaryService(t, db)
	clock := &uhpClock{millis: uhpMillisOne}
	real, err := uploadpg.NewUploadSaver(uploadpg.UploadDeps{
		DB: db, Publisher: svc, Clock: clock, NodeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewUploadSaver: %v", err)
	}
	saver := &uhpCountingSaver{inner: real}
	auth := &uhpAuth{decision: httpserver.UploadAllow, operator: mdUnits("writer")}
	reports := &uhpReports{}
	upload, err := httpserver.NewUploadHandler(httpserver.UploadDeps{
		RepoKey: "uhpRepo", MaxBodyBytes: maxBody, PublicOrigin: uhpOrigin,
		Auth: auth, Saver: saver, Report: reports.add})
	if err != nil {
		t.Fatalf("NewUploadHandler: %v", err)
	}
	pool, err := refpg.NewReadPool(db, 10*time.Second)
	if err != nil {
		t.Fatalf("refpg.NewReadPool: %v", err)
	}
	download, err := httpserver.NewDownloadHandler(httpserver.DownloadDeps{
		RepoKey: "uhpRepo", Auth: &uhpReadAuth{}, Store: pool, Content: store})
	if err != nil {
		t.Fatalf("NewDownloadHandler: %v", err)
	}
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			download.ServeHTTP(w, r)
			return
		}
		upload.ServeHTTP(w, r)
	})
	ts := httptest.NewUnstartedServer(mux)
	ts.Config.ReadHeaderTimeout = 5 * time.Second
	ts.Config.ReadTimeout = 30 * time.Second
	ts.Config.WriteTimeout = 30 * time.Second
	ts.Start()
	t.Cleanup(ts.Close)
	return uhpFixture{db: db, adm: adminPool(t), schema: schema, store: store, root: root,
		clock: clock, saver: saver, auth: auth, reports: reports, ts: ts}
}

// uhpPut/uhpGet 执行真实请求并读尽正文；host 非空时覆盖 Host（反射测试）。
// uhpPut/uhpGet perform real requests and drain the body; a nonempty host
// overrides the Host header (the reflection test).
func uhpPut(t *testing.T, ts *httptest.Server, rawPath string, headers map[string]string, payload, host string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+rawPath, strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if host != "" {
		req.Host = host
	}
	return uhpDo(t, ts.Client(), req)
}

func uhpGet(t *testing.T, ts *httptest.Server, rawPath, host string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+rawPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	return uhpDo(t, ts.Client(), req)
}

func uhpDo(t *testing.T, client *http.Client, req *http.Request) (int, http.Header, string) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.RequestURI(), err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("%s %s: read body: %v", req.Method, req.URL.RequestURI(), readErr)
	}
	return resp.StatusCode, resp.Header, string(body)
}

// uhpDigest 用测试本地标准库独立计算三种摘要。
// uhpDigest computes the three digests independently with the local
// standard library.
func uhpDigest(payload string) (sha1Hex, sha256Hex, md5Hex string) {
	s1, s2, m5 := sha1.Sum([]byte(payload)), sha256.Sum256([]byte(payload)), md5.Sum([]byte(payload))
	return hex.EncodeToString(s1[:]), hex.EncodeToString(s2[:]), hex.EncodeToString(m5[:])
}

// uhpNodeCreator 从实际 DB 直读创建者（响应 createdBy 的独立 oracle）。
// uhpNodeCreator reads the creator straight from the real DB (the
// independent oracle for the response createdBy).
func uhpNodeCreator(t *testing.T, f uhpFixture, id int64) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var creator sql.NullString
	if err := f.adm.QueryRowContext(ctx,
		fmt.Sprintf("SELECT created_by FROM %s.nodes WHERE node_id = $1", f.schema), id).
		Scan(&creator); err != nil || !creator.Valid {
		t.Fatalf("independent creator read = (%v, %v), want a nonnull row", creator, err)
	}
	return creator.String
}

// uhpCreatedView 是 201 JSON 的解码视图；originalChecksums 用 map 保留键集。
// uhpCreatedView is the decoded 201 JSON view; originalChecksums stays a map
// so the key set stays observable.
type uhpCreatedView struct {
	Repo        string `json:"repo"`
	Path        string `json:"path"`
	Created     string `json:"created"`
	CreatedBy   string `json:"createdBy"`
	DownloadURI string `json:"downloadUri"`
	MimeType    string `json:"mimeType"`
	Size        string `json:"size"`
	Checksums   struct {
		SHA1   string `json:"sha1"`
		MD5    string `json:"md5"`
		SHA256 string `json:"sha256"`
	} `json:"checksums"`
	Original map[string]json.RawMessage `json:"originalChecksums"`
	URI      string                     `json:"uri"`
}

// uhpRequireCreated 逐项核对 201：状态、ItemCreated 媒体类型、精确
// Content-Length、X-Checksum-Sha256、Location==uri==downloadUri==受信起点
// （永不反射 Host）、repo/path/created 毫秒 UTC/createdBy(DB 权威)/mime/
// size 字符串、三 Actual、只相应 Original 键、无 modified/updated 键。
// uhpRequireCreated checks the 201 item by item: status, the ItemCreated
// media type, the exact Content-Length, X-Checksum-Sha256,
// Location==uri==downloadUri==trusted origin (never a Host reflection),
// repo/path/created UTC millis/createdBy (DB authority)/mime/size as a
// string, the three Actuals, only the corresponding Original keys and no
// modified/updated keys.
func uhpRequireCreated(t *testing.T, status int, h http.Header, body, repo, rawPath, payload string,
	wantCreatedMillis int64, wantCreator string, wantOriginalKeys map[string]bool) {
	t.Helper()
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %q)", status, body)
	}
	if got := h.Get("Content-Type"); got != uhpItemCreated {
		t.Fatalf("Content-Type = %q, want the ItemCreated media type", got)
	}
	if got := h.Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q, want the exact body length %d", got, len(body))
	}
	sha1Hex, sha256Hex, md5Hex := uhpDigest(payload)
	if got := h.Get("X-Checksum-Sha256"); got != sha256Hex {
		t.Fatalf("X-Checksum-Sha256 = %q, want %q", got, sha256Hex)
	}
	wantURI := uhpOrigin + "/artifactory/" + repo + "/" + rawPath
	if got := h.Get("Location"); got != wantURI {
		t.Fatalf("Location = %q, want the trusted-origin URI %q", got, wantURI)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		t.Fatalf("decode 201 JSON: %v", err)
	}
	for _, forbidden := range []string{"modified", "updated"} {
		if _, present := top[forbidden]; present {
			t.Fatalf("201 JSON leaks a %q key", forbidden)
		}
	}
	var view uhpCreatedView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("decode 201 JSON fields: %v", err)
	}
	if view.URI != wantURI || view.DownloadURI != wantURI {
		t.Fatalf("uri/downloadUri = %q/%q, want the Location %q", view.URI, view.DownloadURI, wantURI)
	}
	wantMime := "application/octet-stream"
	if strings.HasSuffix(rawPath, ".txt") {
		wantMime = "text/plain"
	}
	if view.Repo != repo || view.Path != "/"+rawPath || view.MimeType != wantMime ||
		view.CreatedBy != wantCreator || view.Size != strconv.Itoa(len(payload)) {
		t.Fatalf("projection identity mismatch: %+v", view)
	}
	if want := time.UnixMilli(wantCreatedMillis).UTC().Format(uhpCreatedLayout); view.Created != want {
		t.Fatalf("created = %q, want %q (exact UTC millis)", view.Created, want)
	}
	if view.Checksums.SHA1 != sha1Hex || view.Checksums.MD5 != md5Hex || view.Checksums.SHA256 != sha256Hex {
		t.Fatalf("checksums = %+v, want the local digest triple", view.Checksums)
	}
	gotKeys := make(map[string]bool, len(view.Original))
	for key := range view.Original {
		gotKeys[key] = true
	}
	if len(gotKeys) != len(wantOriginalKeys) {
		t.Fatalf("originalChecksums keys = %v, want exactly %v", gotKeys, wantOriginalKeys)
	}
	for key := range wantOriginalKeys {
		if !gotKeys[key] {
			t.Fatalf("originalChecksums misses %q: got %v", key, gotKeys)
		}
	}
	if got := string(view.Original["sha256"]); got != `"`+sha256Hex+`"` {
		t.Fatalf("originalChecksums.sha256 = %s, want the canonical Actual", got)
	}
}

// uhpRequireGET 用真实 GET 回读并独立对账：200、实字节、Content-Length/
// MIME/ETag、三 checksum 头，且对收到的字节重算三摘要。
// uhpRequireGET reads back through a real GET and reconciles independently:
// 200, the exact bytes, Content-Length/MIME/ETag, the three checksum headers
// and the three digests recomputed from the received bytes.
func uhpRequireGET(t *testing.T, f uhpFixture, rawPath, payload, wantMime string) {
	t.Helper()
	status, h, body := uhpGet(t, f.ts, rawPath, "attacker.example")
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 (body %q)", status, body)
	}
	if body != payload {
		t.Fatalf("GET bytes changed: got %d bytes, want the %d-byte payload", len(body), len(payload))
	}
	if got := h.Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("GET Content-Length = %q, want %d", got, len(payload))
	}
	sha1Hex, sha256Hex, md5Hex := uhpDigest(body)
	if got := h.Get("Content-Type"); got != wantMime {
		t.Fatalf("GET Content-Type = %q, want %q", got, wantMime)
	}
	if got := h.Get("ETag"); got != sha1Hex {
		t.Fatalf("GET ETag = %q, want %q", got, sha1Hex)
	}
	for key, want := range map[string]string{
		"X-Checksum-Sha1": sha1Hex, "X-Checksum-Md5": md5Hex, "X-Checksum-Sha256": sha256Hex,
	} {
		if got := h.Get(key); got != want {
			t.Fatalf("GET %s = %q, want %q", key, got, want)
		}
	}
}

// uhpRequirePristine 断言零副作用：真实 Saver 零调用、无节点/登记/对象，
// 号段上界仍 0 且行存在。
// uhpRequirePristine asserts zero side effects: zero real Saver calls, no
// node/registration/object, and the ID bound still 0 with its row present.
func uhpRequirePristine(t *testing.T, f uhpFixture) {
	t.Helper()
	if calls := f.saver.calls.Load(); calls != 0 {
		t.Fatalf("real Saver calls = %d, want 0", calls)
	}
	if n := countNodes(t, f.adm, f.schema); n != 0 {
		t.Fatalf("nodes = %d, want 0", n)
	}
	if n := countRows(t, f.adm, f.schema); n != 0 {
		t.Fatalf("binaries rows = %d, want 0", n)
	}
	if n := cfCountFiles(t, f.root); n != 0 {
		t.Fatalf("store files = %d, want 0", n)
	}
	cfRequireBound(t, f.adm, f.schema, 0)
}

// 场景一：全新 PUT（无 checksum headers，Original 仅 sha256）→ 同内容再
// PUT（created/creator 保持，不假定 skip）→ 不同内容覆盖（created/creator
// 保持、modified 前进、新字节回读）→ 空 .txt（size 0、三空摘要、文本
// MIME）；Host 反射攻击不得影响受信起点。
// Scenario one: a fresh PUT (no checksum headers, Original only sha256) ->
// an identical-byte re-PUT (created/creator retained, no skip assumption) ->
// a different-content overwrite (created/creator retained, modified moved,
// new bytes read back) -> an empty .txt (size 0, three empty digests, the
// text MIME); a Host-reflection attack must not affect the trusted origin.
func TestUploadHTTPFreshReplayOverwriteEmptyReadback(t *testing.T) {
	f := uhpSetup(t, 1<<20)
	payloadA := "ordinary http upload alpha bytes\n"
	// fresh 与 replay：同字节/无 headers/同钟值；仍 201，created/creator
	// 保持（不假定 skip），首次另带 Host 反射攻击。
	// Fresh and replay: same bytes, no headers and the same clock value; both
	// 201 with created/creator retained (no skip assumption); the first one
	// also carries the Host-reflection attack.
	for i, host := range []string{"attacker.example", ""} {
		status, h, body := uhpPut(t, f.ts, "/artifactory/uhpRepo/dir/a.bin", nil, payloadA, host)
		uhpRequireCreated(t, status, h, body, "uhpRepo", "dir/a.bin", payloadA,
			uhpMillisOne, uhpNodeCreator(t, f, 1), map[string]bool{"sha256": true})
		if n := countNodes(t, f.adm, f.schema); n != 1 {
			t.Fatalf("nodes after put #%d = %d, want 1", i+1, n)
		}
		usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "a.bin", payloadA,
			uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer", sql.NullString{}, sql.NullString{})
		usRequirePublished(t, f.uhpOracle(), payloadA, 1)
		cfRequireBound(t, f.adm, f.schema, 1)
	}
	uhpRequireGET(t, f, "/artifactory/uhpRepo/dir/a.bin", payloadA, "application/octet-stream")

	// overwrite：新字节、新操作者、新钟值；created/createdBy 保持。
	// Overwrite: new bytes, a new operator and clock value; created/createdBy
	// stay retained.
	payloadB := "ordinary http upload beta bytes with a longer tail\n"
	f.clock.set(uhpMillisTwo)
	f.auth.configure(httpserver.UploadAllow, "", mdUnits("second"))
	status, h, body := uhpPut(t, f.ts, "/artifactory/uhpRepo/dir/a.bin", nil, payloadB, "")
	uhpRequireCreated(t, status, h, body, "uhpRepo", "dir/a.bin", payloadB,
		uhpMillisOne, uhpNodeCreator(t, f, 1), map[string]bool{"sha256": true})
	// 覆盖未带 checksum headers：Original 仍为客户端值驱动的 NULL，而非
	// 新 Actual 的自动填充（CLIENT 画像的原样记录）。
	// The overwrite sends no checksum headers: the Originals stay the
	// client-driven NULLs, never an auto-fill of the new Actuals (verbatim
	// recording under the CLIENT profile).
	usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "a.bin", payloadB,
		uhpMillisOne, uhpMillisTwo, uhpMillisTwo, "writer", "second",
		sql.NullString{}, sql.NullString{})
	if n := countRows(t, f.adm, f.schema); n != 2 {
		t.Fatalf("binaries rows after overwrite = %d, want 2", n)
	}
	if n := cfCountFiles(t, f.root); n != 2 {
		t.Fatalf("store files after overwrite = %d, want 2", n)
	}
	cfRequireBound(t, f.adm, f.schema, 1)
	uhpRequireGET(t, f, "/artifactory/uhpRepo/dir/a.bin", payloadB, "application/octet-stream")

	// empty .txt：零字节正文与三个空内容真实摘要。
	// Empty .txt: a zero-byte body with the three real empty digests.
	t.Run("empty-txt", func(t *testing.T) {
		fe := uhpSetup(t, 1<<20)
		sha1E, sha256E, md5E := uhpDigest("")
		headers := map[string]string{
			"X-Checksum-Sha1": sha1E, "X-Checksum-Sha256": sha256E, "X-Checksum-Md5": md5E}
		status, h, body := uhpPut(t, fe.ts, "/artifactory/uhpRepo/dir/empty.txt", headers, "", "")
		uhpRequireCreated(t, status, h, body, "uhpRepo", "dir/empty.txt", "",
			uhpMillisOne, uhpNodeCreator(t, fe, 1), map[string]bool{"sha1": true, "md5": true, "sha256": true})
		usRequireNode(t, fe.uhpOracle(), 1, "uhpRepo", "dir", "empty.txt", "",
			uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer",
			sql.NullString{String: sha1E, Valid: true}, sql.NullString{String: md5E, Valid: true})
		usRequirePublished(t, fe.uhpOracle(), "", 1)
		cfRequireBound(t, fe.adm, fe.schema, 1)
		uhpRequireGET(t, fe, "/artifactory/uhpRepo/dir/empty.txt", "", "text/plain")
	})
}

// 场景二：正确 checksum 矩阵（各单/三全，Original 只含相应键且 SQL 一致）
// 与错误 checksum 矩阵（各 409，原内容经真实 GET/SQL 保持，无节点/号段
// 变化）；409 均出自真实 Saver 的发布前预检，非伪造错误。
// Scenario two: the right-checksum matrix (each single and all-three, with
// only the corresponding Original keys matching SQL) and the wrong-checksum
// matrix (each 409, the original content kept through real GET/SQL, no
// node/ID-range movement); every 409 comes from the real Saver's
// pre-publication precheck, never a fabricated error.
func TestUploadHTTPChecksumMatrixWrongKeepsOriginal(t *testing.T) {
	payload := "http checksum matrix payload\n"
	sha1Hex, sha256Hex, md5Hex := uhpDigest(payload)
	right := []struct {
		name    string
		headers map[string]string
		keys    map[string]bool
	}{
		{"sha1-only", map[string]string{"X-Checksum-Sha1": sha1Hex}, map[string]bool{"sha1": true, "sha256": true}},
		{"sha256-only", map[string]string{"X-Checksum-Sha256": sha256Hex}, map[string]bool{"sha256": true}},
		{"md5-only", map[string]string{"X-Checksum-Md5": md5Hex}, map[string]bool{"md5": true, "sha256": true}},
		{"all-three", map[string]string{"X-Checksum-Sha1": sha1Hex, "X-Checksum-Sha256": sha256Hex, "X-Checksum-Md5": md5Hex},
			map[string]bool{"sha1": true, "md5": true, "sha256": true}},
	}
	for _, c := range right {
		t.Run("right-"+c.name, func(t *testing.T) {
			// 每用例独立夹具：Original 键集互不残留。
			// A fresh fixture per case: the Original key sets never leak.
			f := uhpSetup(t, 1<<20)
			status, h, body := uhpPut(t, f.ts, "/artifactory/uhpRepo/dir/m.bin", c.headers, payload, "")
			uhpRequireCreated(t, status, h, body, "uhpRepo", "dir/m.bin", payload,
				uhpMillisOne, uhpNodeCreator(t, f, 1), c.keys)
			sha1Orig, md5Orig := sql.NullString{}, sql.NullString{}
			if c.keys["sha1"] {
				sha1Orig = sql.NullString{String: sha1Hex, Valid: true}
			}
			if c.keys["md5"] {
				md5Orig = sql.NullString{String: md5Hex, Valid: true}
			}
			usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "m.bin", payload,
				uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer", sha1Orig, md5Orig)
			usRequirePublished(t, f.uhpOracle(), payload, 1)
			uhpRequireGET(t, f, "/artifactory/uhpRepo/dir/m.bin", payload, "application/octet-stream")
		})
	}

	// 基线成功上传后逐项打错误 header：409、无发布副作用、原内容保留。
	// After one baseline success each wrong header answers 409 with no
	// publication side effect and the original content retained.
	f := uhpSetup(t, 1<<20)
	status, h, body := uhpPut(t, f.ts, "/artifactory/uhpRepo/dir/m.bin", nil, payload, "")
	uhpRequireCreated(t, status, h, body, "uhpRepo", "dir/m.bin", payload,
		uhpMillisOne, uhpNodeCreator(t, f, 1), map[string]bool{"sha256": true})
	for _, kind := range []struct {
		name   string
		header string
		value  string
	}{
		{"sha1", "X-Checksum-Sha1", strings.Repeat("7", 40)},
		{"sha256", "X-Checksum-Sha256", strings.Repeat("7", 64)},
		{"md5", "X-Checksum-Md5", strings.Repeat("7", 32)},
	} {
		t.Run("wrong-"+kind.name, func(t *testing.T) {
			before := f.saver.calls.Load()
			status, h, body := uhpPut(t, f.ts, "/artifactory/uhpRepo/dir/m.bin",
				map[string]string{kind.header: kind.value}, payload, "")
			if status != http.StatusConflict {
				t.Fatalf("wrong %s status = %d, want 409 (body %q)", kind.name, status, body)
			}
			if body != "conflict\n" {
				t.Fatalf("wrong %s body = %q, want the fixed conflict body", kind.name, body)
			}
			if got := h.Get("Location"); got != "" {
				t.Fatalf("409 leaks Location %q", got)
			}
			if calls := f.saver.calls.Load(); calls != before+1 {
				t.Fatalf("wrong %s real Saver calls = %d, want %d (one real attempt)", kind.name, calls, before+1)
			}
			// 无节点变化、无号段推进；原节点行逐字段保持。
			// No node change and no ID progress; the original node row keeps
			// every field.
			if n := countNodes(t, f.adm, f.schema); n != 1 {
				t.Fatalf("nodes after wrong %s = %d, want 1", kind.name, n)
			}
			cfRequireBound(t, f.adm, f.schema, 1)
			usRequireNode(t, f.uhpOracle(), 1, "uhpRepo", "dir", "m.bin", payload,
				uhpMillisOne, uhpMillisOne, uhpMillisOne, "writer", "writer", sql.NullString{}, sql.NullString{})
			usRequirePublished(t, f.uhpOracle(), payload, 1)
			uhpRequireGET(t, f, "/artifactory/uhpRepo/dir/m.bin", payload, "application/octet-stream")
		})
	}
}

// 场景三：拒绝矩阵——401（带 challenge）/403（无 challenge）/非法 rawURI/
// 非法 flags/超过 MaxBody：真实 Saver 零调用，节点/登记/对象/号段全部无
// 变化；只有到达授权阶段的请求才调用授权端口。
// Scenario three: the rejection matrix — 401 (with challenge)/403 (no
// challenge)/illegal rawURI/illegal flags/over MaxBody: zero real Saver
// calls, no node/registration/object/ID-range change; only the requests
// reaching the authorization stage call the auth port.
func TestUploadHTTPRejectionZeroSideEffects(t *testing.T) {
	f := uhpSetup(t, 16)
	const validPath = "/artifactory/uhpRepo/dir/a.bin"
	challenge := `Basic realm="binflow"`
	cases := []struct {
		name       string
		path       string
		headers    map[string]string
		payload    string
		wantStatus int
		wantBody   string
		challenge  string
		tweak      func()
	}{
		{"unauthenticated", validPath, nil, "x", http.StatusUnauthorized, "unauthorized\n", challenge,
			func() { f.auth.configure(httpserver.UploadUnauthenticated, challenge, nil) }},
		{"denied", validPath, nil, "x", http.StatusForbidden, "forbidden\n", "",
			func() { f.auth.configure(httpserver.UploadDenied, "", nil) }},
		{"query", validPath + "?x=1", nil, "x", http.StatusBadRequest, "bad request\n", "", nil},
		{"dot-segment", "/artifactory/uhpRepo/dir/../a.bin", nil, "x", http.StatusBadRequest, "bad request\n", "", nil},
		{"wrong-repo-prefix", "/artifactory/other/dir/a.bin", nil, "x", http.StatusBadRequest, "bad request\n", "", nil},
		{"percent-segment", "/artifactory/uhpRepo/dir/%61.bin", nil, "x", http.StatusBadRequest, "bad request\n", "", nil},
		{"bad-suffix", "/artifactory/uhpRepo/dir/a.exe", nil, "x", http.StatusBadRequest, "bad request\n", "", nil},
		{"checksum-deploy-flag", validPath, map[string]string{"X-Checksum-Deploy": "true"}, "x",
			http.StatusNotImplemented, "not implemented\n", "", nil},
		{"explode-archive-flag", validPath, map[string]string{"X-Explode-Archive": "true"}, "x",
			http.StatusNotImplemented, "not implemented\n", "", nil},
		{"legacy-client-ua", validPath, map[string]string{"User-Agent": "ArtifactoryBuildClient/2.16.0"}, "x",
			http.StatusNotImplemented, "not implemented\n", "", nil},
		{"over-budget", validPath, nil, strings.Repeat("b", 40),
			http.StatusRequestEntityTooLarge, "payload too large\n", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.auth.configure(httpserver.UploadAllow, "", mdUnits("writer"))
			if c.tweak != nil {
				c.tweak()
			}
			status, h, body := uhpPut(t, f.ts, c.path, c.headers, c.payload, "")
			if status != c.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", status, c.wantStatus, body)
			}
			if body != c.wantBody {
				t.Fatalf("body = %q, want %q", body, c.wantBody)
			}
			if got := h.Get("WWW-Authenticate"); got != c.challenge {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, c.challenge)
			}
			if got := h.Get("Location"); got != "" {
				t.Fatalf("%d leaks Location %q", status, got)
			}
			uhpRequirePristine(t, f)
		})
	}
	if calls := f.auth.callCount(); calls != 2 {
		t.Fatalf("write-auth calls = %d, want 2 (only the two auth-stage cases)", calls)
	}
	if calls := f.saver.calls.Load(); calls != 0 {
		t.Fatalf("real Saver calls = %d, want 0", calls)
	}
}

// 场景四：真实延迟约束触发器使 Commit 失败——HTTP 500、无 201 表示、无
// Location/checksum 头、真实 Saver 恰一次（未自动重试）、节点缺席而已提交
// 号段/登记行/对象字节保留；随后真实 GET 因节点缺席回答 404。
// Scenario four: a real deferred constraint trigger fails the Commit — HTTP
// 500, no 201 representation, no Location/checksum headers, exactly one real
// Saver call (no automatic retry), the node absent while the committed ID
// range/registration row/object bytes survive; the following real GET
// answers 404 because the node is absent.
func TestUploadHTTPDeferredCommitFailureServes500KeepsState(t *testing.T) {
	f := uhpSetup(t, 1<<20)
	usDDL(t, f.adm, fmt.Sprintf(
		"CREATE FUNCTION %s.uhp_deferred_reject() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'http e2e deferred rejection'; END $$ LANGUAGE plpgsql", f.schema))
	usDDL(t, f.adm, fmt.Sprintf(
		"CREATE CONSTRAINT TRIGGER uhp_deferred_reject AFTER INSERT ON %s.nodes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s.uhp_deferred_reject()", f.schema, f.schema))
	payload := "published before the deferred http commit fault\n"
	status, h, body := uhpPut(t, f.ts, "/artifactory/uhpRepo/dir/g.bin", nil, payload, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %q)", status, body)
	}
	if body != "internal error\n" {
		t.Fatalf("body = %q, want the fixed internal-error body", body)
	}
	if got := h.Get("Location"); got != "" {
		t.Fatalf("500 leaks Location %q", got)
	}
	if got := h.Get("X-Checksum-Sha256"); got != "" {
		t.Fatalf("500 leaks X-Checksum-Sha256 %q", got)
	}
	if n := f.reports.count(httpserver.UploadFailureSave); n != 1 {
		t.Fatalf("save failure reports = %d, want 1", n)
	}
	if calls := f.saver.calls.Load(); calls != 1 {
		t.Fatalf("real Saver calls = %d, want 1 (no automatic retry)", calls)
	}
	if n := countNodes(t, f.adm, f.schema); n != 0 {
		t.Fatalf("nodes = %d, want 0 (the failed commit rolled back)", n)
	}
	cfRequireBound(t, f.adm, f.schema, 1)
	usRequirePublished(t, f.uhpOracle(), payload, 1)
	gstatus, _, gbody := uhpGet(t, f.ts, "/artifactory/uhpRepo/dir/g.bin", "")
	if gstatus != http.StatusNotFound {
		t.Fatalf("GET after failed commit = %d, want 404 (body %q)", gstatus, gbody)
	}
	if gbody != "not found\n" {
		t.Fatalf("GET body = %q, want the fixed not-found body", gbody)
	}
}

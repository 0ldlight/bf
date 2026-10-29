// 真实 PostgreSQL 上的下载 GET/HEAD 适配器集成测试：真实流 SaveFile 提交、
// 每次查询独立短事务的读取 Store 与真实 net/http 传输；不模拟 SQL 或对象字节。
// Real-PostgreSQL integration tests for the download GET/HEAD adapter: real
// stream SaveFile commits, a per-lookup short-transaction read store and a
// real net/http transport; no SQL or object bytes are faked.
package postgresit

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// dghDigest 由真实字节独立计算三个规范小写十六进制摘要，作为 SQL/HTTP 之外
// 的期望来源。/ dghDigest computes the three canonical lowercase hex digests
// from the real payload as an oracle independent of SQL and HTTP.
func dghDigest(payload string) (sha1Hex, md5Hex, sha256Hex string) {
	s1, m5, s256 := sha1.Sum([]byte(payload)), md5.Sum([]byte(payload)), sha256.Sum256([]byte(payload))
	return hex.EncodeToString(s1[:]), hex.EncodeToString(m5[:]), hex.EncodeToString(s256[:])
}

// dghStore 每次查询开启一个独立短事务：真实 SQL、立即回滚、Source 按值复制
// 返回；任何事务都不跨越并发的 HTTP 流式响应（BIN-199 计划第 8 条）。
// dghStore begins a fresh short transaction per lookup: real SQL, immediate
// rollback and a by-value Source copy; no transaction ever spans concurrent
// HTTP streaming (plan ruling 8).
type dghStore struct {
	db      *sql.DB
	lookups atomic.Int64
}

// lookupCount 是跨 HTTP goroutine 安全的查询计数快照；真实 SQL 委托不变。
// lookupCount is a cross-HTTP-goroutine-safe lookup-count snapshot; the real
// SQL delegation is unchanged.
func (s *dghStore) lookupCount() int64 { return s.lookups.Load() }

func (s *dghStore) LookupSnapshotSource(_ context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	s.lookups.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return repository.FileSnapshotSource{}, err
	}
	defer func() { _ = tx.Rollback() }()
	st, err := refpg.New(tx)
	if err != nil {
		return repository.FileSnapshotSource{}, err
	}
	return st.LookupSnapshotSource(ctx, path)
}

// dghAuth 是可配置的显式授权决定端口：计数调用并允许测试在授权时刻采样
// 查询数，以证明授权先于任何节点查询。/ dghAuth is the configurable
// explicit-authorization port: it counts calls and lets tests sample the
// lookup count at authorization time to prove auth precedes any lookup.
type dghAuth struct {
	mu     sync.Mutex
	result httpserver.DownloadAuthResult
	err    error
	calls  int
	onCall func()
}

func (a *dghAuth) Authorize(_ context.Context, _ *http.Request, _ repository.Path) (httpserver.DownloadAuthResult, error) {
	a.mu.Lock()
	a.calls++
	cb := a.onCall
	res, err := a.result, a.err
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
	return res, err
}

func (a *dghAuth) set(result httpserver.DownloadAuthResult, err error) {
	a.mu.Lock()
	a.result, a.err = result, err
	a.mu.Unlock()
}

// callCount 是锁保护的授权调用计数快照；直接读字段不是同步读取。
// callCount is a lock-backed authorization-call snapshot; reading the field
// directly is not a synchronized read.
func (a *dghAuth) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// dghOpener 透明计数真实 filestore 打开次数；不替换任何打开行为。
// dghOpener transparently counts real filestore opens, replacing nothing.
type dghOpener struct {
	inner localrepo.FileOpener
	opens atomic.Int64
}

// openCount 是跨 HTTP goroutine 安全的打开计数快照；真实打开委托不变。
// openCount is a cross-HTTP-goroutine-safe open-count snapshot; the real
// open delegation is unchanged.
func (o *dghOpener) openCount() int64 { return o.opens.Load() }

func (o *dghOpener) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o.opens.Add(1)
	return o.inner.Open(ctx, key)
}

// dghReports 收集固定诊断类别；断言只看类别，不看原始错误或路径。
// dghReports collects fixed diagnostic categories; assertions check
// categories only, never raw errors or paths.
type dghReports struct {
	mu   sync.Mutex
	seen []httpserver.DownloadFailure
}

func (r *dghReports) add(f httpserver.DownloadFailure) {
	r.mu.Lock()
	r.seen = append(r.seen, f)
	r.mu.Unlock()
}

func (r *dghReports) count(f httpserver.DownloadFailure) int {
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

func (r *dghReports) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// dghServer 用冻结导出 API 构造独立处理器并挂到真实 net/http 服务。
// dghServer builds the unmounted handler through the frozen exported API and
// serves it over a real net/http server.
func dghServer(t *testing.T, f splFixture, auth *dghAuth, store *dghStore, opener *dghOpener, reports *dghReports) *httptest.Server {
	t.Helper()
	handler, err := httpserver.NewDownloadHandler(httpserver.DownloadDeps{
		RepoKey: "saveR",
		Auth:    auth,
		Store:   store,
		Content: opener,
		Report:  reports.add,
	})
	if err != nil {
		t.Fatalf("NewDownloadHandler: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// dghRequest 通过真实客户端执行请求并读尽正文，返回状态、头、正文与
// 声明的 Content-Length。/ dghRequest performs a request over the real
// client, drains the body and returns status, headers, body and the declared
// Content-Length.
func dghRequest(t *testing.T, ts *httptest.Server, method, path string) (int, http.Header, string, int64) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("%s %s: read body: %v", method, path, readErr)
	}
	return resp.StatusCode, resp.Header, string(body), resp.ContentLength
}

// dghRepresentation 核对成功表示头：固定后缀 MIME、未加引号 ETag、三个
// checksum 头、两种 filename 形式、毫秒取整秒的 UTC Last-Modified，且不广告
// Accept-Ranges。Content-Length 由调用方按节点 size 单独断言。
// dghRepresentation checks the success representation headers: the fixed
// suffix MIME, the unquoted ETag, the three checksum headers, both filename
// forms, the seconds-floored UTC Last-Modified and no advertised
// Accept-Ranges. Content-Length is asserted separately from the node size.
func dghRepresentation(t *testing.T, h http.Header, mime, sha1Hex, md5Hex, sha256Hex, name string, modifiedMillis int64) {
	t.Helper()
	if got := h.Get("Content-Type"); got != mime {
		t.Fatalf("Content-Type = %q, want %q", got, mime)
	}
	if got := h.Get("ETag"); got != sha1Hex {
		t.Fatalf("ETag = %q, want unquoted %q", got, sha1Hex)
	}
	for key, want := range map[string]string{
		"X-Checksum-Sha1":   sha1Hex,
		"X-Checksum-Md5":    md5Hex,
		"X-Checksum-Sha256": sha256Hex,
	} {
		if got := h.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	wantDisposition := fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", name, name)
	if got := h.Get("Content-Disposition"); got != wantDisposition {
		t.Fatalf("Content-Disposition = %q, want %q", got, wantDisposition)
	}
	wantModified := time.Unix(modifiedMillis/1000, 0).UTC().Format(http.TimeFormat)
	if got := h.Get("Last-Modified"); got != wantModified {
		t.Fatalf("Last-Modified = %q, want %q", got, wantModified)
	}
	if got := h.Get("Accept-Ranges"); got != "" {
		t.Fatalf("Accept-Ranges advertised: %q", got)
	}
}

// dghError 核对固定错误状态与正文：GET 带固定正文，HEAD 零正文但两者都带
// 显式 Content-Length 与固定错误 Content-Type。
// dghError checks the fixed error status and body: GET carries the fixed
// body, HEAD stays bodyless, and both carry the explicit Content-Length plus
// the fixed error Content-Type.
func dghError(t *testing.T, status int, h http.Header, body string, contentLength int64, wantStatus int, wantBody string, head bool) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d", status, wantStatus)
	}
	if head {
		if body != "" {
			t.Fatalf("HEAD body = %q, want empty", body)
		}
	} else if body != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
	if contentLength != int64(len(wantBody)) {
		t.Fatalf("Content-Length = %d, want %d", contentLength, len(wantBody))
	}
	if got := h.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("error Content-Type = %q", got)
	}
}

// dghNoChallenge 断言本次真实响应不携带 WWW-Authenticate：只有已接纳的
// 401 能带 challenge，其余状态泄漏 challenge 即失败。
// dghNoChallenge asserts the actual response carries no WWW-Authenticate:
// only an admitted 401 may carry a challenge, so leaking one on any other
// status fails.
func dghNoChallenge(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("WWW-Authenticate"); got != "" {
		t.Fatalf("WWW-Authenticate leaked on a non-401 response: %q", got)
	}
}

// dghExec 在隔离 schema 上执行独立 SQL 变更。/ dghExec applies an
// independent SQL mutation on the isolated schema.
func dghExec(t *testing.T, f splFixture, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

// dghObjectPath 返回 filestore 扇出布局下的物理对象路径。
// dghObjectPath returns the physical object path in the filestore fanout
// layout.
func dghObjectPath(root, sha1Hex string) string {
	return filepath.Join(root, sha1Hex[:2], sha1Hex)
}

// dghSave 用真实流命令保存一个文件并断言真实创建结果。
// dghSave saves one file through the real stream command and asserts the
// real creation result.
func dghSave(t *testing.T, f splFixture, svc *binary.Service, clock *splClock, path, payload string) {
	t.Helper()
	in := sslInput()
	in.Path = path
	got, err := localrepo.SaveFile(f.ctx, sslSource(payload), in, sslDeps(f, svc, clock))
	if err != nil || !got.Save.Created || got.Save.ID == 0 {
		t.Fatalf("save %q = %+v %v", path, got, err)
	}
}

// dghCounts 用独立 SQL/文件系统统计节点行、登记行与常规物理对象数。
// dghCounts counts node rows, registry rows and regular physical objects
// through independent SQL/filesystem walks.
func dghCounts(t *testing.T, f splFixture) (nodes, rows, files int) {
	t.Helper()
	return countNodes(t, f.adm, f.schema), countRows(t, f.adm, f.schema), cfCountFiles(t, f.root)
}

// TestDownloadCommittedFilesAndHead 覆盖真实 SaveFile 提交后的完整 GET/HEAD：
// 二进制/文本/空文件的完整字节与表示头、HEAD 零正文且零内容 Open、每请求
// 恰一次授权与查询、空对象移除后由精确空 SHA1 合成，以及节点/登记/ID/对象
// 计数器不变。/ TestDownloadCommittedFilesAndHead covers full GET/HEAD after
// real SaveFile commits: exact bytes and representation headers for
// binary/text/empty files, bodyless HEAD with zero content opens, exactly one
// authorization and lookup per request, empty synthesis after removing the
// empty object, and unchanged node/registry/ID/object counters.
func TestDownloadCommittedFilesAndHead(t *testing.T) {
	f, svc := sslSetup(t)
	clock := &splClock{value: 41}
	const binPayload = "\x00abc\xff"
	const txtPayload = "binflow text payload\n"
	dghSave(t, f, svc, clock, "dir/a.bin", binPayload)
	dghSave(t, f, svc, clock, "dir/b.txt", txtPayload)
	dghSave(t, f, svc, clock, "dir/e.txt", "")
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 空对象即使曾被物理发布也先移除：精确空 SHA1 必须走合成流而不是 Open。
	// Remove the empty object even if physically published: the exact empty
	// SHA1 must take the synthetic stream instead of Open.
	emptySHA1, emptyMD5, emptySHA256 := dghDigest("")
	if err := os.Remove(dghObjectPath(f.root, emptySHA1)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove empty object: %v", err)
	}
	nodes, rows, files := dghCounts(t, f)
	auth := &dghAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
	store, opener, reports := &dghStore{db: f.db}, &dghOpener{inner: f.objects}, &dghReports{}
	ts := dghServer(t, f, auth, store, opener, reports)
	binSHA1, binMD5, binSHA256 := dghDigest(binPayload)
	txtSHA1, txtMD5, txtSHA256 := dghDigest(txtPayload)

	status, header, body, length := dghRequest(t, ts, http.MethodGet, "/artifactory/saveR/dir/a.bin")
	if status != http.StatusOK || body != binPayload || length != int64(len(binPayload)) {
		t.Fatalf("binary GET = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", binSHA1, binMD5, binSHA256, "a.bin", 41)
	if auth.callCount() != 1 || store.lookupCount() != 1 || opener.openCount() != 1 {
		t.Fatalf("first GET counters: auth=%d lookup=%d open=%d", auth.callCount(), store.lookupCount(), opener.openCount())
	}

	status, header, body, length = dghRequest(t, ts, http.MethodHead, "/artifactory/saveR/dir/a.bin")
	if status != http.StatusOK || body != "" || length != int64(len(binPayload)) {
		t.Fatalf("binary HEAD = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", binSHA1, binMD5, binSHA256, "a.bin", 41)
	if store.lookupCount() != 2 || opener.openCount() != 1 {
		t.Fatalf("HEAD must not open content: lookup=%d open=%d", store.lookupCount(), opener.openCount())
	}

	opensBeforeText := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodGet, "/artifactory/saveR/dir/b.txt")
	if status != http.StatusOK || body != txtPayload || length != int64(len(txtPayload)) {
		t.Fatalf("text GET = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "text/plain", txtSHA1, txtMD5, txtSHA256, "b.txt", 41)
	if got := opener.openCount(); got != opensBeforeText+1 {
		t.Fatalf("text GET open delta = %d, want +1", got-opensBeforeText)
	}

	opensBeforeTextHead := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodHead, "/artifactory/saveR/dir/b.txt")
	if status != http.StatusOK || body != "" || length != int64(len(txtPayload)) {
		t.Fatalf("text HEAD = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "text/plain", txtSHA1, txtMD5, txtSHA256, "b.txt", 41)
	if got := opener.openCount(); got != opensBeforeTextHead {
		t.Fatalf("text HEAD open delta = %d, want 0", got-opensBeforeTextHead)
	}

	opensBeforeEmpty := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodGet, "/artifactory/saveR/dir/e.txt")
	if status != http.StatusOK || body != "" || length != 0 {
		t.Fatalf("empty GET = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "text/plain", emptySHA1, emptyMD5, emptySHA256, "e.txt", 41)
	if got := opener.openCount(); got != opensBeforeEmpty {
		t.Fatalf("empty GET opened content: opens=%d, want %d", got, opensBeforeEmpty)
	}

	// 空 HEAD 也是真实请求：同表示头、零正文、零 Open 增量。
	// The empty HEAD is a real request too: same representation headers, a
	// zero-length body and a zero Open delta.
	opensBeforeEmptyHead := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodHead, "/artifactory/saveR/dir/e.txt")
	if status != http.StatusOK || body != "" || length != 0 {
		t.Fatalf("empty HEAD = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "text/plain", emptySHA1, emptyMD5, emptySHA256, "e.txt", 41)
	if got := opener.openCount(); got != opensBeforeEmptyHead {
		t.Fatalf("empty HEAD open delta = %d, want 0", got-opensBeforeEmptyHead)
	}

	// 每个请求恰好一次授权与一次节点查询；两次非空 GET 恰好两次 Open；成功
	// 路径不报告任何失败类别。/ Exactly one authorization and one node lookup
	// per request; exactly two opens for the two nonempty GETs; the success
	// path reports no failure category at all.
	if auth.callCount() != 6 || store.lookupCount() != 6 || opener.openCount() != 2 {
		t.Fatalf("final counters: auth=%d lookup=%d open=%d", auth.callCount(), store.lookupCount(), opener.openCount())
	}
	if reports.total() != 0 {
		t.Fatalf("success path reported failures: %d", reports.total())
	}
	gotNodes, gotRows, gotFiles := dghCounts(t, f)
	if gotNodes != nodes || gotRows != rows || gotFiles != files {
		t.Fatalf("requests changed counters: nodes=%d/%d rows=%d/%d files=%d/%d",
			gotNodes, nodes, gotRows, rows, gotFiles, files)
	}
	if f.alloc.next != 3 || f.alloc.reserve != 3 {
		t.Fatalf("ID reservations changed: next=%d reserve=%d", f.alloc.next, f.alloc.reserve)
	}
}

// TestDownloadMetadataAndObjectFailures 覆盖提交前的行内事实损坏与对象层失败：
// 缺节点 404、目录行 500、nil 元数据/三个 Actual NULL/畸形十六进制/负 size/
// 空 SHA1 正 size/修改时间域外均在提交响应前 500 且零 Open；缺对象与符号链接
// GET 500 而 HEAD 仍 200；节点/登记/ID 计数器不变。
// TestDownloadMetadataAndObjectFailures covers in-row fact corruption before
// commitment and object-layer failures: missing node 404, folder row 500, and
// nil metadata / three NULL actuals / malformed hex / negative size / empty
// SHA1 with positive size / out-of-range mtime all returning 500 with zero
// opens before the response commits; missing object and symlink give GET 500
// while HEAD stays 200; node/registry/ID counters never change.
func TestDownloadMetadataAndObjectFailures(t *testing.T) {
	f, svc := sslSetup(t)
	dghSave(t, f, svc, &splClock{value: 41}, "dir/a.bin", "abc")
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sha1Hex, md5Hex, sha256Hex := dghDigest("abc")
	emptySHA1, emptyMD5, emptySHA256 := dghDigest("")
	// FK 目标行必须先真实登记：畸形 SHA1 与空摘要集合才能被节点引用。
	// FK target rows are registered for real first so the node may reference
	// the malformed SHA1 and the empty digest set.
	dghExec(t, f, "INSERT INTO binaries(sha1,md5,bin_length,sha256) VALUES($1,$2,$3,$4)",
		strings.Repeat("z", 40), strings.Repeat("d", 32), 3, strings.Repeat("2", 64))
	dghExec(t, f, "INSERT INTO binaries(sha1,md5,bin_length,sha256) VALUES($1,$2,$3,$4)",
		emptySHA1, emptyMD5, 0, emptySHA256)
	nodes, rows, files := dghCounts(t, f)
	auth := &dghAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
	store, opener, reports := &dghStore{db: f.db}, &dghOpener{inner: f.objects}, &dghReports{}
	ts := dghServer(t, f, auth, store, opener, reports)
	url := "/artifactory/saveR/dir/a.bin"
	restore := fmt.Sprintf("sha1_actual='%s', md5_actual='%s', sha256='%s', bin_length=3, node_type=1, "+
		"created=-7, modified=41, updated=41, created_by='writer', modified_by='writer', "+
		"sha1_original=NULL, md5_original=NULL", sha1Hex, md5Hex, sha256Hex)

	// 缺节点是唯一 404：固定正文，GET/HEAD 都不打开内容。
	// The missing node is the only 404: fixed body and no content open for
	// either method.
	status, header, body, length := dghRequest(t, ts, http.MethodGet, "/artifactory/saveR/dir/miss.bin")
	dghError(t, status, header, body, length, http.StatusNotFound, "not found\n", false)
	status, header, body, length = dghRequest(t, ts, http.MethodHead, "/artifactory/saveR/dir/miss.bin")
	dghError(t, status, header, body, length, http.StatusNotFound, "not found\n", true)
	if opener.openCount() != 0 {
		t.Fatalf("missing node opened content: opens=%d", opener.openCount())
	}

	faults := []struct {
		name string
		set  string
		// reportProjection 标记除目录行外的全部行内损坏都按 projection 分类。
		// reportProjection marks every in-row corruption except the folder
		// row as classified projection.
		reportProjection bool
	}{
		{"folder-row", "node_type=0", false},
		{"nil-metadata", "created=NULL, modified=NULL, updated=NULL, created_by=NULL, modified_by=NULL, sha1_original=NULL, md5_original=NULL", true},
		{"null-sha1-actual", "sha1_actual=NULL", true},
		{"null-md5-actual", "md5_actual=NULL", true},
		{"null-sha256-actual", "sha256=NULL", true},
		{"three-null-actuals", "sha1_actual=NULL, md5_actual=NULL, sha256=NULL", true},
		{"nonhex-sha1", fmt.Sprintf("sha1_actual='%s'", strings.Repeat("z", 40)), true},
		{"uppercase-md5", fmt.Sprintf("md5_actual='%s'", strings.ToUpper(md5Hex)), true},
		{"negative-size", "bin_length=-5", true},
		{"empty-hash-positive-size", fmt.Sprintf("sha1_actual='%s', md5_actual='%s', sha256='%s'", emptySHA1, emptyMD5, emptySHA256), true},
		{"mtime-negative", "modified=-1", true},
		{"mtime-overflow", "modified=253402300800000", true},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			dghExec(t, f, "UPDATE nodes SET "+fault.set)
			projectionBefore := reports.count(httpserver.DownloadFailureProjection)
			opens := opener.openCount()
			status, header, body, length := dghRequest(t, ts, http.MethodGet, url)
			dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
			status, header, body, length = dghRequest(t, ts, http.MethodHead, url)
			dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", true)
			if opener.openCount() != opens {
				t.Fatalf("corrupt row opened content: opens=%d, want %d", opener.openCount(), opens)
			}
			if fault.reportProjection && reports.count(httpserver.DownloadFailureProjection) == projectionBefore {
				t.Fatal("projection failure was not reported")
			}
			dghExec(t, f, "UPDATE nodes SET "+restore)
		})
	}

	// 修改时间上边界（9999 年最后毫秒）仍是合法表示；秒取整 UTC 输出。
	// The modified-time upper bound (the last millisecond of year 9999)
	// stays a valid representation with the seconds-floored UTC rendering.
	dghExec(t, f, "UPDATE nodes SET modified=253402300799999")
	status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
	if status != http.StatusOK || body != "abc" || length != 3 {
		t.Fatalf("boundary GET = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", 253402300799999)
	dghExec(t, f, "UPDATE nodes SET modified=41")

	// 缺对象：GET 打开失败 500（恰一次 Open）；HEAD 只用有效元数据，仍 200
	// 完整表示且零 Open 增量。/ Missing object: GET fails its single open
	// with 500; HEAD uses only the valid metadata, keeps the full 200
	// representation and adds a zero Open delta.
	if err := os.Remove(dghObjectPath(f.root, sha1Hex)); err != nil {
		t.Fatal(err)
	}
	openBefore := reports.count(httpserver.DownloadFailureOpen)
	opensBeforeMissingGet := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
	dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
	if got := opener.openCount(); got != opensBeforeMissingGet+1 {
		t.Fatalf("missing-object GET open delta = %d, want +1", got-opensBeforeMissingGet)
	}
	if reports.count(httpserver.DownloadFailureOpen) == openBefore {
		t.Fatal("open failure was not reported")
	}
	opensBeforeMissingHead := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodHead, url)
	if status != http.StatusOK || body != "" || length != 3 {
		t.Fatalf("missing-object HEAD = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", 41)
	if got := opener.openCount(); got != opensBeforeMissingHead {
		t.Fatalf("missing-object HEAD open delta = %d, want 0", got-opensBeforeMissingHead)
	}

	// 符号链接对象：打开层拒绝穿越面，GET 500（恰一次 Open）；HEAD 仍 200
	// 且零 Open 增量。/ Symlinked object: the open layer rejects the
	// traversal surface with GET 500 through exactly one open; HEAD still
	// returns 200 with a zero Open delta.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dghObjectPath(f.root, sha1Hex)); err != nil {
		t.Fatal(err)
	}
	opensBeforeSymlinkGet := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
	dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
	if got := opener.openCount(); got != opensBeforeSymlinkGet+1 {
		t.Fatalf("symlink GET open delta = %d, want +1", got-opensBeforeSymlinkGet)
	}
	opensBeforeSymlinkHead := opener.openCount()
	status, header, body, length = dghRequest(t, ts, http.MethodHead, url)
	if status != http.StatusOK || body != "" || length != 3 {
		t.Fatalf("symlink HEAD = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", 41)
	if got := opener.openCount(); got != opensBeforeSymlinkHead {
		t.Fatalf("symlink HEAD open delta = %d, want 0", got-opensBeforeSymlinkHead)
	}

	// 全部请求之后：节点与登记行不变，ID 预留不变；物理常规对象比初始少一
	//（abc 被删除；符号链接非常规文件不计入）。
	// After every request: node and registry rows are unchanged, ID
	// reservations are unchanged, and one fewer regular object remains (abc
	// was deleted; the symlink is not a regular file).
	gotNodes, gotRows, gotFiles := dghCounts(t, f)
	if gotNodes != nodes || gotRows != rows || gotFiles != files-1 {
		t.Fatalf("counters changed: nodes=%d/%d rows=%d/%d files=%d/%d(want %d)",
			gotNodes, nodes, gotRows, rows, gotFiles, files, files-1)
	}
	if f.alloc.next != 1 || f.alloc.reserve != 1 {
		t.Fatalf("ID reservations changed: next=%d reserve=%d", f.alloc.next, f.alloc.reserve)
	}
}

// TestDownloadAuthorizationNoDatabaseAccess 覆盖授权边界：未认证 401 逐字透传
// 可信 challenge、已认证拒绝 403、授权层错误/零决定/非法 challenge 字段 500，
// 全部在查询前结束（零查询、零打开、计数器不变）；随后允许决定恢复 200 并
// 证明授权先于查询。/ TestDownloadAuthorizationNoDatabaseAccess covers the
// authorization boundary: unauthenticated 401 with the trusted challenge passed
// through verbatim, authenticated denial 403, and auth-layer error / zero
// decision / illegal challenge fields as 500 — all ending before any lookup
// (zero lookups, zero opens, unchanged counters); an allow decision then
// restores 200 and proves authorization precedes the lookup.
func TestDownloadAuthorizationNoDatabaseAccess(t *testing.T) {
	f, svc := sslSetup(t)
	dghSave(t, f, svc, &splClock{value: 41}, "dir/a.bin", "abc")
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	nodes, rows, files := dghCounts(t, f)
	auth := &dghAuth{}
	store, opener, reports := &dghStore{db: f.db}, &dghOpener{inner: f.objects}, &dghReports{}
	ts := dghServer(t, f, auth, store, opener, reports)
	url := "/artifactory/saveR/dir/a.bin"
	requireNoAccess := func(stage string) {
		t.Helper()
		if store.lookupCount() != 0 || opener.openCount() != 0 {
			t.Fatalf("%s touched storage: lookup=%d open=%d", stage, store.lookupCount(), opener.openCount())
		}
	}

	auth.set(httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated, Challenge: `Basic realm="binflow"`}, nil)
	status, header, body, length := dghRequest(t, ts, http.MethodGet, url)
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
	if got := header.Get("WWW-Authenticate"); got != `Basic realm="binflow"` {
		t.Fatalf("WWW-Authenticate = %q, want the trusted challenge verbatim", got)
	}
	status, header, body, length = dghRequest(t, ts, http.MethodHead, url)
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", true)
	if got := header.Get("WWW-Authenticate"); got != `Basic realm="binflow"` {
		t.Fatalf("HEAD WWW-Authenticate = %q", got)
	}
	requireNoAccess("unauthenticated 401")

	// 空 challenge、不可打印 challenge 与拒绝时多余 challenge 都是无效授权
	// 结果：500，同样不查询。/ An empty challenge, a nonprintable challenge
	// and a denial with a surplus challenge are all invalid authorization
	// results: 500 with no lookup either.
	for _, invalid := range []httpserver.DownloadAuthResult{
		{Decision: httpserver.DownloadUnauthenticated, Challenge: ""},
		{Decision: httpserver.DownloadUnauthenticated, Challenge: "bad\x01"},
		{Decision: httpserver.DownloadDenied, Challenge: "extra"},
	} {
		auth.set(invalid, nil)
		status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
		dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
		// 本次真实 500 响应不得携带 challenge：不复用任何早先响应的头。
		// The actual 500 response here must carry no challenge: no earlier
		// response's headers are reused.
		dghNoChallenge(t, header)
	}
	requireNoAccess("invalid auth results")

	auth.set(httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied}, nil)
	status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
	dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", false)
	dghNoChallenge(t, header)
	status, header, body, length = dghRequest(t, ts, http.MethodHead, url)
	dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", true)
	dghNoChallenge(t, header)
	requireNoAccess("denied 403")

	auth.set(httpserver.DownloadAuthResult{}, errors.New("trusted auth unavailable"))
	status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
	dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
	dghNoChallenge(t, header)
	auth.set(httpserver.DownloadAuthResult{}, nil)
	status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
	dghError(t, status, header, body, length, http.StatusInternalServerError, "internal error\n", false)
	dghNoChallenge(t, header)
	requireNoAccess("auth error and zero decision")

	gotNodes, gotRows, gotFiles := dghCounts(t, f)
	if gotNodes != nodes || gotRows != rows || gotFiles != files {
		t.Fatalf("denials changed counters: nodes=%d/%d rows=%d/%d files=%d/%d",
			gotNodes, nodes, gotRows, rows, gotFiles, files)
	}
	if f.alloc.next != 1 || f.alloc.reserve != 1 {
		t.Fatalf("ID reservations changed: next=%d reserve=%d", f.alloc.next, f.alloc.reserve)
	}

	// 同一处理器在允许决定后恢复 200，且授权时刻查询数仍为零；采样经原子
	// 写/读跨 goroutine 同步。/ The same handler recovers to 200 under an
	// allow decision, with the lookup count at authorization time still zero;
	// the sample crosses goroutines through an atomic write/read pair.
	var lookupsAtAuth atomic.Int64
	lookupsAtAuth.Store(-1)
	auth.mu.Lock()
	auth.onCall = func() { lookupsAtAuth.Store(store.lookupCount()) }
	auth.mu.Unlock()
	auth.set(httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}, nil)
	sha1Hex, md5Hex, sha256Hex := dghDigest("abc")
	status, header, body, length = dghRequest(t, ts, http.MethodGet, url)
	if status != http.StatusOK || body != "abc" || length != 3 {
		t.Fatalf("allowed GET = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", 41)
	if got := lookupsAtAuth.Load(); got != 0 {
		t.Fatalf("lookup before authorization: %d", got)
	}
	// 授权累计按真实请求数：401 GET+HEAD(2) + 无效结果(3) + 拒绝 GET+HEAD(2)
	// + 授权错误(1) + 零决定(1) + 最终允许(1) = 10。
	// The authorization total follows the real request count: 401 GET+HEAD
	// (2) + invalid results (3) + denial GET+HEAD (2) + auth error (1) +
	// zero decision (1) + final allow (1) = 10.
	if store.lookupCount() != 1 || opener.openCount() != 1 || auth.callCount() != 10 {
		t.Fatalf("allowed counters: auth=%d lookup=%d open=%d", auth.callCount(), store.lookupCount(), opener.openCount())
	}
}

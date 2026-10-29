package httpserver_test

// TRANSPORT 契约（BIN-199）：用真实 httptest.NewServer 与 http.Client 验证
// 下载适配器的线上行为；dt* 助手独立于 UNIT 的 du* 助手。
// TRANSPORT contract (BIN-199): real httptest.NewServer and http.Client verify
// the download adapter on the wire; dt* helpers stay independent of UNIT's du*.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/repository"
)

// dtFixed* 是固定的本地错误正文；不含任何请求内容。
// dtFixed* are the fixed local error bodies; none carries request data.
const (
	dtFixedBadRequest       = "bad request\n"
	dtFixedUnauthorized     = "unauthorized\n"
	dtFixedForbidden        = "forbidden\n"
	dtFixedNotFound         = "not found\n"
	dtFixedMethodNotAllowed = "method not allowed\n"
	dtFixedInternal         = "internal error\n"
	dtFixedNotImplemented   = "not implemented\n"
)

// dtEmptySHA1 是精确空文件的规范 SHA1；触发合成空流，不打开对象。
// dtEmptySHA1 is the canonical SHA1 of the exact empty file; it selects the
// synthetic empty stream without opening an object.
const dtEmptySHA1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"

// dtRepoKey 是注入的仓库键；处理器根路径为 /artifactory/{RepoKey}/...。
// dtRepoKey is the injected repository key; the handler root path is
// /artifactory/{RepoKey}/....
const dtRepoKey = "rk"

// dtAuthAllow 等便捷决定值用于表格初始化。
// dtAuthAllow and friends are convenience decisions for tables.
var (
	dtAuthAllow           = httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}
	dtAuthUnauthenticated = httpserver.DownloadAuthResult{
		Decision:  httpserver.DownloadUnauthenticated,
		Challenge: `Basic realm="dt"`,
	}
	dtAuthDenied = httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied}
)

// dtStep 是合成流的一次读取结果：data 或 err 二选一。
// dtStep is one scripted stream read: either data or err.
type dtStep struct {
	data []byte
	err  error
}

func dtData(data []byte) dtStep { return dtStep{data: data} }
func dtFail(err error) dtStep   { return dtStep{err: err} }
func dtEOF() dtStep             { return dtStep{err: io.EOF} }

// dtBody 是可观测的内容流：脚本步骤 + 关闭计数 + 可注入关闭错误。
// 单步数据可超过调用方缓冲区，剩余字节跨 Read 保留。
// dtBody is an observable content stream: scripted steps plus close counting
// and an injectable close error. A step larger than the caller buffer keeps
// its remainder across Read calls.
type dtBody struct {
	steps    chan dtStep
	pending  []byte
	closeErr error

	mu     sync.Mutex
	closes int
}

func dtScriptBody(closeErr error, steps ...dtStep) *dtBody {
	ch := make(chan dtStep, len(steps))
	for _, s := range steps {
		ch <- s
	}
	close(ch)
	return &dtBody{steps: ch, closeErr: closeErr}
}

func (b *dtBody) Read(p []byte) (int, error) {
	if len(b.pending) == 0 {
		s, ok := <-b.steps
		if !ok {
			return 0, io.EOF
		}
		if s.err != nil {
			return 0, s.err
		}
		b.pending = s.data
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *dtBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	return b.closeErr
}

func (b *dtBody) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

// dtAuth 记录调用并返回配置的决定；hook 优先用于时序控制。
// dtAuth records calls and returns the configured decision; hook wins and is
// reserved for timing control.
type dtAuth struct {
	mu     sync.Mutex
	result httpserver.DownloadAuthResult
	err    error
	hook   func(context.Context, *http.Request, repository.Path) (httpserver.DownloadAuthResult, error)
	calls  int
	paths  []repository.Path
}

func (a *dtAuth) Authorize(ctx context.Context, r *http.Request, p repository.Path) (httpserver.DownloadAuthResult, error) {
	a.mu.Lock()
	a.calls++
	a.paths = append(a.paths, p)
	hook := a.hook
	a.mu.Unlock()
	if hook != nil {
		return hook(ctx, r, p)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.result, a.err
}

func (a *dtAuth) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func (a *dtAuth) lastPath() repository.Path {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.paths) == 0 {
		return repository.Path{}
	}
	return a.paths[len(a.paths)-1]
}

// dtStoreLike 按优先级返回：注入错误、found 固定 Source、路由函数，
// 否则 sql.ErrNoRows；始终记录查询次数与路径。
// dtStoreLike returns by precedence: injected error, the fixed source when
// found, the routing function, otherwise sql.ErrNoRows; lookups and paths are
// always recorded.
type dtStoreLike struct {
	mu      sync.Mutex
	lookup  func(repository.Path) (repository.FileSnapshotSource, bool)
	src     repository.FileSnapshotSource
	found   bool
	err     error
	lookups int
	paths   []repository.Path
}

func (s *dtStoreLike) LookupSnapshotSource(_ context.Context, p repository.Path) (repository.FileSnapshotSource, error) {
	s.mu.Lock()
	s.lookups++
	s.paths = append(s.paths, p)
	fn, src, found, err := s.lookup, s.src, s.found, s.err
	s.mu.Unlock()
	if err != nil {
		return repository.FileSnapshotSource{}, err
	}
	if found {
		return src, nil
	}
	if fn != nil {
		if src, ok := fn(p); ok {
			return src, nil
		}
	}
	return repository.FileSnapshotSource{}, sql.ErrNoRows
}

// dtContent 记录打开调用；open 为空时返回固定错误。
// dtContent records open calls; an absent open returns a fixed error.
type dtContent struct {
	mu    sync.Mutex
	open  func(ctx context.Context, sha1 string) (io.ReadCloser, error)
	opens int
	keys  []string
}

func (c *dtContent) Open(ctx context.Context, sha1 string) (io.ReadCloser, error) {
	c.mu.Lock()
	c.opens++
	c.keys = append(c.keys, sha1)
	open := c.open
	c.mu.Unlock()
	if open == nil {
		return nil, errors.New("dt: content open not configured")
	}
	return open(ctx, sha1)
}

func (c *dtContent) openCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

func (c *dtContent) lastKey() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) == 0 {
		return ""
	}
	return c.keys[len(c.keys)-1]
}

// dtReporter 线程安全地记录失败类别。
// dtReporter records failure categories safely across goroutines.
type dtReporter struct {
	mu         sync.Mutex
	categories []httpserver.DownloadFailure
}

func (r *dtReporter) report(f httpserver.DownloadFailure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.categories = append(r.categories, f)
}

func (r *dtReporter) snapshot() []httpserver.DownloadFailure {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]httpserver.DownloadFailure, len(r.categories))
	copy(out, r.categories)
	return out
}

func (r *dtReporter) has(f httpserver.DownloadFailure) bool {
	for _, got := range r.snapshot() {
		if got == f {
			return true
		}
	}
	return false
}

func (r *dtReporter) only(f httpserver.DownloadFailure) bool {
	got := r.snapshot()
	return len(got) == 1 && got[0] == f
}

// dtMetadata 构造非 nil 的节点元数据快照，仅 ModifiedMillis 参与表示。
// dtMetadata builds a nonnil per-node metadata snapshot; only ModifiedMillis
// feeds the representation.
func dtMetadata(modifiedMillis int64) *repository.FileMetadata {
	m := repository.NewFileMetadata(repository.FileMetadataInput{ModifiedMillis: modifiedMillis})
	return &m
}

// dtSource 构造完全有效的 Source：身份与 (repo,parent,name) 一致、三个
// Actual 非空、元数据非 nil。checksums 由调用方给定。
// dtSource builds a fully valid source: identity matches (repo,parent,name),
// all three Actual bits are set and metadata is nonnil. Callers supply
// checksums.
func dtSource(repo, parent, name, sha1, md5, sha256 string, size int64, modifiedMillis int64) repository.FileSnapshotSource {
	p, err := repository.NewPath(repo, parent, name, true)
	if err != nil {
		panic("dt: invalid source path: " + err.Error())
	}
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID:       7,
			Path:     p,
			Content:  content.Metadata{SHA1: sha1, MD5: md5, SHA256: sha256, Size: size},
			Metadata: dtMetadata(modifiedMillis),
		},
		SHA1ActualValid:   true,
		MD5ActualValid:    true,
		SHA256ActualValid: true,
	}
}

// dtValidSource 用固定小写十六进制摘要构造有效 Source。
// dtValidSource builds a valid source with fixed lowercase hex digests.
func dtValidSource(parent, name string, size int64, modifiedMillis int64) repository.FileSnapshotSource {
	return dtSource(dtRepoKey, parent, name,
		strings.Repeat("a", 40), strings.Repeat("c", 32), strings.Repeat("d", 64),
		size, modifiedMillis)
}

// dtRequireNoError 统一致命断言。
// dtRequireNoError is the single fatal error assertion.
func dtRequireNoError(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", what, err)
	}
}

// dtNewHandler 组装被测处理器并绑定 dt 依赖。
// dtNewHandler assembles the handler under test with dt dependencies.
func dtNewHandler(t *testing.T, auth *dtAuth, store *dtStoreLike, contentOpener *dtContent, rep *dtReporter) http.Handler {
	t.Helper()
	h, err := httpserver.NewDownloadHandler(httpserver.DownloadDeps{
		RepoKey: dtRepoKey,
		Auth:    auth,
		Store:   store,
		Content: contentOpener,
		Report:  rep.report,
	})
	dtRequireNoError(t, "NewDownloadHandler", err)
	return h
}

// dtNewServer 为被测处理器启动真实 HTTP 服务。
// dtNewServer starts a real HTTP server for the handler under test.
func dtNewServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// dtHTTPClient 统一携带超时，任何路径都不会无限等待。
// dtHTTPClient carries a timeout everywhere so no path waits forever.
var dtHTTPClient = &http.Client{Timeout: 10 * time.Second}

// dtWatch 包装被测处理器，记录两类服务端事件：请求上下文取消（真实
// r.Context().Done，取代睡眠推断）与处理器退出（defer 在底层 ServeHTTP
// 展开之后运行，晚于产品内的延迟 Close）。/ dtWatch wraps the handler
// under test and records two server-side events: request-context cancellation
// (a real r.Context().Done edge instead of sleeping) and handler completion
// (its defer runs after the underlying ServeHTTP unwinds, hence after the
// product's deferred Close).
type dtWatch struct {
	handlerOnce  sync.Once
	cancelOnce   sync.Once
	handlerDone  chan struct{}
	ctxCancelled chan struct{}
}

func dtNewWatch() *dtWatch {
	return &dtWatch{
		handlerDone:  make(chan struct{}),
		ctxCancelled: make(chan struct{}),
	}
}

func (w *dtWatch) wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		go func() {
			<-r.Context().Done()
			w.cancelOnce.Do(func() { close(w.ctxCancelled) })
		}()
		defer w.handlerOnce.Do(func() { close(w.handlerDone) })
		h.ServeHTTP(rw, r)
	})
}

// dtWaitChan 有界等待事件通道；超时按失败处理而不是挂起。
// dtWaitChan waits boundedly for an event channel; timeouts fail instead of
// hanging.
func dtWaitChan(t *testing.T, name string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// dtRecvErr 有界接收异步结果。
// dtRecvErr receives an async result boundedly.
func dtRecvErr(t *testing.T, name string, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out receiving %s", name)
		return nil
	}
}

// dtReleaseSteps 幂等关闭 steps 通道，使阻塞的合成 Read 以 EOF 释放，
// 让 t.Cleanup 里的 server.Close 不会永久等待提供者。
// dtReleaseSteps closes the steps channel idempotently so a blocked
// synthetic Read unblocks with EOF and the registered server.Close never
// waits forever on a provider.
func dtReleaseSteps(steps chan dtStep) func() {
	var once sync.Once
	return func() {
		once.Do(func() { close(steps) })
	}
}

// dtRawRequest 用原始 TCP 发送未经客户端归一化的请求行，保留 raw target。
// dtRawRequest sends the request line over raw TCP so the raw target survives
// client-side normalization.
func dtRawRequest(t *testing.T, ts *httptest.Server, method, rawTarget string) (*http.Response, []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", ts.Listener.Addr().String(), 5*time.Second)
	dtRequireNoError(t, "dial test server", err)
	defer conn.Close()
	// 原始 TCP 同样必须有界：拨号与读写都设置期限。
	// Raw TCP stays bounded too: dial and reads carry deadlines.
	dtRequireNoError(t, "set raw socket deadline", conn.SetDeadline(time.Now().Add(10*time.Second)))
	req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: dt\r\nConnection: close\r\n\r\n", method, rawTarget)
	_, err = conn.Write([]byte(req))
	dtRequireNoError(t, "write raw request", err)
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	dtRequireNoError(t, "read raw response", err)
	body, err := io.ReadAll(resp.Body)
	dtRequireNoError(t, "read raw response body", err)
	return resp, body
}

// dtRequireFixedError 断言固定错误表示：状态、正文、类型与长度。
// dtRequireFixedError asserts the fixed error representation: status, body,
// type and length.
func dtRequireFixedError(t *testing.T, resp *http.Response, body []byte, code int, wantBody string) {
	t.Helper()
	if resp.StatusCode != code {
		t.Fatalf("status = %d, want %d", resp.StatusCode, code)
	}
	if string(body) != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", ct, "text/plain; charset=utf-8")
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(wantBody)) {
		t.Fatalf("Content-Length = %q, want %q", cl, strconv.Itoa(len(wantBody)))
	}
}

// dtLastModified 断言 Last-Modified 为毫秒向下取整的 UTC HTTP 日期。
// dtLastModified asserts Last-Modified is the millisecond-floored UTC date.
func dtLastModified(t *testing.T, resp *http.Response, modifiedMillis int64) {
	t.Helper()
	want := time.UnixMilli(modifiedMillis).UTC().Format(http.TimeFormat)
	if got := resp.Header.Get("Last-Modified"); got != want {
		t.Fatalf("Last-Modified = %q, want %q", got, want)
	}
}

// dtRequireRepresentation 断言共享表示头：长度、MIME、未加引号 ETag、
// 三个 X-Checksum、Content-Disposition 双 filename 形式、无 Accept-Ranges。
// dtRequireRepresentation asserts the shared representation headers: length,
// MIME, unquoted ETag, three X-Checksum headers, Content-Disposition with
// both filename forms and no Accept-Ranges.
func dtRequireRepresentation(t *testing.T, resp *http.Response, src repository.FileSnapshotSource) {
	t.Helper()
	ref := src.Reference
	name := ref.Path.Name()
	wantLen := strconv.FormatInt(ref.Content.Size, 10)
	if got := resp.Header.Get("Content-Length"); got != wantLen {
		t.Fatalf("Content-Length = %q, want %q", got, wantLen)
	}
	wantType := "text/plain"
	if strings.HasSuffix(name, ".bin") {
		wantType = "application/octet-stream"
	}
	if got := resp.Header.Get("Content-Type"); got != wantType {
		t.Fatalf("Content-Type = %q, want %q", got, wantType)
	}
	if got := resp.Header.Get("ETag"); got != ref.Content.SHA1 {
		t.Fatalf("ETag = %q, want unquoted %q", got, ref.Content.SHA1)
	}
	if got := resp.Header.Get("X-Checksum-Sha1"); got != ref.Content.SHA1 {
		t.Fatalf("X-Checksum-Sha1 = %q, want %q", got, ref.Content.SHA1)
	}
	if got := resp.Header.Get("X-Checksum-Md5"); got != ref.Content.MD5 {
		t.Fatalf("X-Checksum-Md5 = %q, want %q", got, ref.Content.MD5)
	}
	if got := resp.Header.Get("X-Checksum-Sha256"); got != ref.Content.SHA256 {
		t.Fatalf("X-Checksum-Sha256 = %q, want %q", got, ref.Content.SHA256)
	}
	wantDisp := fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", name, name)
	if got := resp.Header.Get("Content-Disposition"); got != wantDisp {
		t.Fatalf("Content-Disposition = %q, want %q", got, wantDisp)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "" {
		t.Fatalf("Accept-Ranges = %q, want absent", got)
	}
	dtLastModified(t, resp, ref.Metadata.ModifiedMillis())
}

// dtObservedWriter 包装 ResponseRecorder，计数 WriteHeader/Write 并可注入
// 写错误；用于直连（不经网络）证明单次 WriteHeader、零 HEAD 写与关闭一次。
// dtObservedWriter wraps a recorder, counting WriteHeader/Write calls and
// optionally injecting write errors; direct (no network) runs prove a single
// WriteHeader, zero HEAD writes and exactly-one close.
type dtObservedWriter struct {
	*httptest.ResponseRecorder
	mu              sync.Mutex
	headerCalls     int
	statuses        []int
	writes          int
	bytesWritten    int
	failWritesAfter int // -1 表示从不失败。/ -1 means never fail.
	writeErr        error
}

func dtObserve(rec *httptest.ResponseRecorder) *dtObservedWriter {
	return &dtObservedWriter{ResponseRecorder: rec, failWritesAfter: -1}
}

func (w *dtObservedWriter) WriteHeader(code int) {
	w.mu.Lock()
	w.headerCalls++
	w.statuses = append(w.statuses, code)
	w.mu.Unlock()
	w.ResponseRecorder.WriteHeader(code)
}

func (w *dtObservedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	over := w.failWritesAfter >= 0 && w.bytesWritten+len(p) > w.failWritesAfter
	if !over {
		w.bytesWritten += len(p)
	}
	err := w.writeErr
	w.mu.Unlock()
	if over {
		return 0, err
	}
	return w.ResponseRecorder.Write(p)
}

func (w *dtObservedWriter) stats() (headerCalls, writes, bytes int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.headerCalls, w.writes, w.bytesWritten
}

// dtGETSuccessRepresentations 在真实服务上验证 GET 成功表示：二进制、
// 文本与精确空文件；单次查询、按实际 SHA1 打开、空文件零打开。
// dtGETSuccessRepresentations verifies successful GET representations on a
// real server: binary, text and the exact empty file; one lookup, opens by
// actual SHA1 and zero opens for the empty file.
func dtGETSuccessRepresentations(t *testing.T) {
	t.Helper()
	binaryPayload := bytes.Repeat([]byte{0xB7}, 6*1024)
	textPayload := []byte("hello\ndownload\n")
	binSrc := dtValidSource("", "payload.bin", int64(len(binaryPayload)), 1717171717123)
	// 文本必须拥有与二进制不同的内容身份（唯一小写十六进制 SHA1 及配套
	// 摘要），否则按实际 SHA1 打开会命中二进制字节。
	// Text must carry a distinct content identity from binary (its own
	// lowercase-hex SHA1 and matching digests); otherwise opening by the
	// actual SHA1 would serve the binary bytes.
	txtSrc := dtSource(dtRepoKey, "dir", "notes.txt",
		strings.Repeat("b", 40), strings.Repeat("e", 32), strings.Repeat("f", 64),
		int64(len(textPayload)), 0)
	emptySrc := dtSource(dtRepoKey, "", "empty.bin", dtEmptySHA1, strings.Repeat("c", 32), strings.Repeat("d", 64), 0, 253402300799999)

	sources := map[string]repository.FileSnapshotSource{
		"/artifactory/rk/payload.bin":   binSrc,
		"/artifactory/rk/dir/notes.txt": txtSrc,
		"/artifactory/rk/empty.bin":     emptySrc,
	}
	auth := &dtAuth{result: dtAuthAllow}
	storeHook := func(p repository.Path) (repository.FileSnapshotSource, bool) {
		key := "/artifactory/" + p.Repo()
		if parent := p.Parent(); parent != "" {
			key += "/" + parent
		}
		key += "/" + p.Name()
		src, ok := sources[key]
		return src, ok
	}
	store2 := &dtStoreLike{lookup: storeHook}
	opener := &dtContent{}
	opener.open = func(_ context.Context, sha1 string) (io.ReadCloser, error) {
		// 显式匹配两个已知键并拒绝未知键，杜绝默认分支错配。
		// Match the two known keys explicitly and reject anything else so
		// no default branch can silently mismatch.
		switch sha1 {
		case binSrc.Reference.Content.SHA1:
			return dtScriptBody(nil, dtData(binaryPayload), dtEOF()), nil
		case txtSrc.Reference.Content.SHA1:
			return dtScriptBody(nil, dtData(textPayload), dtEOF()), nil
		default:
			return nil, errors.New("dt: unexpected open key")
		}
	}
	rep := &dtReporter{}
	ts := dtNewServer(t, dtNewHandler(t, auth, store2, opener, rep))

	client := dtHTTPClient
	for path, src := range sources {
		t.Run(path, func(t *testing.T) {
			var wantPayload []byte
			if path == "/artifactory/rk/payload.bin" {
				wantPayload = binaryPayload
			} else if path == "/artifactory/rk/dir/notes.txt" {
				wantPayload = textPayload
			}
			resp, err := client.Get(ts.URL + path)
			dtRequireNoError(t, "GET", err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			dtRequireNoError(t, "read GET body", err)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if !bytes.Equal(body, wantPayload) {
				t.Fatalf("body length = %d, want %d", len(body), len(wantPayload))
			}
			dtRequireRepresentation(t, resp, src)
			if wantPayload != nil {
				// 打开键必须是节点实际 SHA1；精确空文件走合成流，无打开。
				// The open key must be the node's actual SHA1; the exact
				// empty file uses the synthetic stream and never opens.
				if opener.lastKey() != src.Reference.Content.SHA1 {
					t.Fatalf("open key = %q, want actual SHA1 %q", opener.lastKey(), src.Reference.Content.SHA1)
				}
			}
		})
	}
	if got := store2.lookupCount(); got != len(sources) {
		t.Fatalf("lookups = %d, want %d (single lookup per request)", got, len(sources))
	}
	if got := auth.callCount(); got != len(sources) {
		t.Fatalf("auth calls = %d, want %d", got, len(sources))
	}
	for _, p := range store2.recordedPaths() {
		if p.Repo() != dtRepoKey {
			t.Fatalf("lookup repo = %q, want %q", p.Repo(), dtRepoKey)
		}
	}
	// 精确空文件不打开对象：三次请求只有两次 open。
	// The exact empty file never opens an object: three requests, two opens.
	if got := opener.openCount(); got != 2 {
		t.Fatalf("opens = %d, want 2 (no open for the empty file)", got)
	}
	if got := len(rep.snapshot()); got != 0 {
		t.Fatalf("reported failures = %v, want none", got)
	}
}

func (s *dtStoreLike) lookupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
}

func (s *dtStoreLike) recordedPaths() []repository.Path {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]repository.Path, len(s.paths))
	copy(out, s.paths)
	return out
}

func TestDownloadTransportGETSuccessRepresentations(t *testing.T) {
	dtGETSuccessRepresentations(t)
}

// dtHEADParity 验证 HEAD 与 GET 表示一致、零正文、零内容打开；对象缺失或
// 不安全（open 错误）时 HEAD 仍 200。
// dtHEADParity verifies HEAD matches the GET representation with a zero body
// and zero content opens; HEAD stays 200 even when opening would fail (missing
// or unsafe object).
func TestDownloadTransportHEADParityAndZeroOpen(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5A}, 4096)
	src := dtValidSource("dir", "payload.bin", int64(len(payload)), 1717171717999)

	getHeaders := func(t *testing.T) http.Header {
		t.Helper()
		body := dtScriptBody(nil, dtData(payload), dtEOF())
		store := &dtStoreLike{lookup: func(repository.Path) (repository.FileSnapshotSource, bool) { return src, true }}
		opener := &dtContent{open: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
		ts := dtNewServer(t, dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, &dtReporter{}))
		resp, err := dtHTTPClient.Get(ts.URL + "/artifactory/rk/dir/payload.bin")
		dtRequireNoError(t, "GET", err)
		defer resp.Body.Close()
		got, err := io.ReadAll(resp.Body)
		dtRequireNoError(t, "read GET body", err)
		if !bytes.Equal(got, payload) {
			t.Fatalf("GET body length = %d, want %d", len(got), len(payload))
		}
		return resp.Header
	}
	headRun := func(t *testing.T) (*http.Response, []byte, *dtContent) {
		t.Helper()
		// open 配置为固定失败：有效元数据的 HEAD 不得打开内容。
		// Open is configured to always fail: HEAD with valid metadata must
		// not open content.
		opener := &dtContent{}
		store := &dtStoreLike{lookup: func(repository.Path) (repository.FileSnapshotSource, bool) { return src, true }}
		ts := dtNewServer(t, dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, &dtReporter{}))
		resp, err := dtHTTPClient.Head(ts.URL + "/artifactory/rk/dir/payload.bin")
		dtRequireNoError(t, "HEAD", err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		dtRequireNoError(t, "read HEAD body", err)
		return resp, body, opener
	}

	t.Run("headersMatchGET", func(t *testing.T) {
		get := getHeaders(t)
		resp, body, opener := headRun(t)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD status = %d, want 200", resp.StatusCode)
		}
		if len(body) != 0 {
			t.Fatalf("HEAD body = %q, want empty", body)
		}
		for _, name := range []string{
			"Content-Length", "Content-Type", "ETag", "X-Checksum-Sha1",
			"X-Checksum-Md5", "X-Checksum-Sha256", "Content-Disposition",
			"Last-Modified",
		} {
			if get.Get(name) != resp.Header.Get(name) {
				t.Fatalf("HEAD %s = %q, want GET value %q", name, resp.Header.Get(name), get.Get(name))
			}
		}
		if got := opener.openCount(); got != 0 {
			t.Fatalf("opens = %d, want 0 (HEAD must not open content)", got)
		}
	})
	t.Run("emptyFileZeroLength", func(t *testing.T) {
		emptySrc := dtSource(dtRepoKey, "e", "empty.bin", dtEmptySHA1, strings.Repeat("c", 32), strings.Repeat("d", 64), 0, 0)
		opener := &dtContent{}
		store := &dtStoreLike{lookup: func(repository.Path) (repository.FileSnapshotSource, bool) { return emptySrc, true }}
		ts := dtNewServer(t, dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, &dtReporter{}))
		resp, err := dtHTTPClient.Head(ts.URL + "/artifactory/rk/e/empty.bin")
		dtRequireNoError(t, "HEAD", err)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Length"); got != "0" {
			t.Fatalf("Content-Length = %q, want 0", got)
		}
		if got := opener.openCount(); got != 0 {
			t.Fatalf("opens = %d, want 0", got)
		}
	})
}

// dtErrorCase 是固定错误表示表格的一条用例。
// dtErrorCase is one row of the fixed-error-representation table.
type dtErrorCase struct {
	name        string
	method      string
	target      string // 原样 target；raw 为真时不经客户端归一化。/ verbatim target; raw bypasses client normalization.
	raw         bool
	header      http.Header
	authResult  *httpserver.DownloadAuthResult
	authErr     error
	storeSrc    *repository.FileSnapshotSource
	storeErr    error
	openBody    io.ReadCloser
	wantCode    int
	wantBody    string
	wantAllow   string
	wantAuth    string // 期望的 WWW-Authenticate。/ expected WWW-Authenticate.
	wantReport  httpserver.DownloadFailure
	wantLookups int
	wantOpens   int
}

// dtErrorEnv 聚合一次用例的可观测依赖。
// dtErrorEnv aggregates the observable dependencies of one case run.
type dtErrorEnv struct {
	auth   *dtAuth
	store  *dtStoreLike
	opener *dtContent
	rep    *dtReporter
}

func dtRunErrorCase(t *testing.T, tc dtErrorCase) {
	t.Helper()
	// 方法始终取自表格本身（默认 GET），绝不再强制改写。
	// The method always comes from the row itself (GET default) and is never
	// overridden by the runner.
	method := tc.method
	if method == "" {
		method = http.MethodGet
	}
	headBodyless := method == http.MethodHead
	env := &dtErrorEnv{
		auth:   &dtAuth{result: dtAuthAllow, err: tc.authErr},
		store:  &dtStoreLike{err: tc.storeErr},
		opener: &dtContent{},
		rep:    &dtReporter{},
	}
	if tc.authResult != nil {
		env.auth.result = *tc.authResult
	}
	if tc.storeSrc != nil {
		env.store.src, env.store.found = *tc.storeSrc, true
	}
	if tc.openBody != nil {
		body := tc.openBody
		env.opener.open = func(context.Context, string) (io.ReadCloser, error) { return body, nil }
	}
	ts := dtNewServer(t, dtNewHandler(t, env.auth, env.store, env.opener, env.rep))

	var resp *http.Response
	var body []byte
	if tc.raw {
		resp, body = dtRawRequest(t, ts, method, tc.target)
	} else {
		req, err := http.NewRequest(method, ts.URL+tc.target, nil)
		dtRequireNoError(t, "build request", err)
		for name, values := range tc.header {
			if len(values) == 0 {
				// 空值头也要真实发送（nil 值同样触发 501）。
				// Even a nil/empty value must reach the wire (still 501).
				req.Header.Set(name, "")
				continue
			}
			for _, v := range values {
				req.Header.Add(name, v)
			}
		}
		resp, err = dtHTTPClient.Do(req)
		dtRequireNoError(t, method+" "+tc.target, err)
		defer resp.Body.Close()
		body, err = io.ReadAll(resp.Body)
		dtRequireNoError(t, "read body", err)
	}
	if headBodyless {
		// HEAD 错误零正文：正文等价断言不适用，长度与类型仍按固定值检查。
		// HEAD errors are bodyless: body equality does not apply; length and
		// type still follow the fixed values.
		if resp.StatusCode != tc.wantCode {
			t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantCode)
		}
		if len(body) != 0 {
			t.Fatalf("HEAD body = %q, want bodyless", body)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("Content-Type = %q, want %q", ct, "text/plain; charset=utf-8")
		}
		if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(tc.wantBody)) {
			t.Fatalf("Content-Length = %q, want %q", cl, strconv.Itoa(len(tc.wantBody)))
		}
	} else {
		dtRequireFixedError(t, resp, body, tc.wantCode, tc.wantBody)
	}
	if tc.wantAllow != "" {
		if got := resp.Header.Get("Allow"); got != tc.wantAllow {
			t.Fatalf("Allow = %q, want %q", got, tc.wantAllow)
		}
	}
	// 预提交错误绝不携带任何表示头（ruling 5：不外泄部分表示）。
	// Precommit errors never carry representation headers (ruling 5: no
	// partial representation leaks).
	for _, h := range []string{"ETag", "X-Checksum-Sha1", "X-Checksum-Md5", "X-Checksum-Sha256", "Content-Disposition", "Last-Modified"} {
		if got := resp.Header.Get(h); got != "" {
			t.Fatalf("%s = %q on fixed error, want absent", h, got)
		}
	}
	if tc.wantAuth != "" {
		if got := resp.Header.Get("WWW-Authenticate"); got != tc.wantAuth {
			t.Fatalf("WWW-Authenticate = %q, want %q", got, tc.wantAuth)
		}
	} else if got := resp.Header.Get("WWW-Authenticate"); got != "" && tc.wantCode != http.StatusUnauthorized {
		t.Fatalf("WWW-Authenticate = %q, want absent outside 401", got)
	}
	if got := env.store.lookupCount(); got != tc.wantLookups {
		t.Fatalf("lookups = %d, want %d", got, tc.wantLookups)
	}
	if got := env.opener.openCount(); got != tc.wantOpens {
		t.Fatalf("opens = %d, want %d", got, tc.wantOpens)
	}
	if tc.wantReport != "" && !env.rep.has(tc.wantReport) {
		t.Fatalf("reported categories = %v, want %q", env.rep.snapshot(), tc.wantReport)
	}
}

// dtValidTargetSrc 提供表格用有效 Source 与对应 target。
// dtValidTargetSrc provides a valid source plus its target for the table.
func dtValidTargetSrc() (string, repository.FileSnapshotSource) {
	return "/artifactory/rk/dir/payload.bin", dtValidSource("dir", "payload.bin", 16, 1717171717000)
}

// dtErrorTable 覆盖方法、原始 URI 守卫、query、条件头、授权三分支、
// 查询/投影/打开失败；每种均断言固定正文与长度，且域外拒绝零查询。
// dtErrorTable covers method, raw-URI gates, query, conditional headers, the
// three auth outcomes and lookup/projection/open failures; every row asserts
// the fixed body/length, and out-of-domain denials perform zero lookups.
func dtErrorTable() []dtErrorCase {
	validTarget, validSrc := dtValidTargetSrc()
	unauth := dtAuthUnauthenticated
	denied := dtAuthDenied
	zeroDecision := httpserver.DownloadAuthResult{}
	unknownDecision := httpserver.DownloadAuthResult{Decision: httpserver.DownloadAuthDecision(9)}
	unauthNoChallenge := httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated}
	unauthBadChallenge := httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated, Challenge: "bad\x80challenge"}
	deniedWithChallenge := httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied, Challenge: `Basic realm="x"`}
	src := validSrc
	mismatch := dtValidSource("dir", "other.bin", 16, 1717171717000)
	return []dtErrorCase{
		{name: "methodPOST", method: http.MethodPost, target: validTarget, wantCode: http.StatusMethodNotAllowed, wantBody: dtFixedMethodNotAllowed, wantAllow: "GET, HEAD"},
		{name: "methodBeatsURIProfile", method: http.MethodPost, target: "/artifactory/rk/dir/p%61yload.bin", raw: true, wantCode: http.StatusMethodNotAllowed, wantBody: dtFixedMethodNotAllowed, wantAllow: "GET, HEAD"},
		{name: "methodPUT", method: http.MethodPut, target: validTarget, wantCode: http.StatusMethodNotAllowed, wantBody: dtFixedMethodNotAllowed, wantAllow: "GET, HEAD"},
		{name: "methodDELETE", method: http.MethodDelete, target: validTarget, wantCode: http.StatusMethodNotAllowed, wantBody: dtFixedMethodNotAllowed, wantAllow: "GET, HEAD"},
		{name: "encodedPercent", method: http.MethodGet, target: "/artifactory/rk/dir/p%61yload.bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "doubleLeadingSlash", method: http.MethodGet, target: "//artifactory/rk/dir/payload.bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "doubleInnerSlash", method: http.MethodGet, target: "/artifactory/rk//payload.bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "trailingSlash", method: http.MethodGet, target: "/artifactory/rk/dir/payload.bin/", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "dotSegment", method: http.MethodGet, target: "/artifactory/rk/./payload.bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "dotDotSegment", method: http.MethodGet, target: "/artifactory/rk/../rk/dir/payload.bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "uppercaseExtension", method: http.MethodGet, target: "/artifactory/rk/dir/payload.BIN", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "noExtension", method: http.MethodGet, target: "/artifactory/rk/dir/payload", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "emptyName", method: http.MethodGet, target: "/artifactory/rk/dir/.bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "archiveSuffix", method: http.MethodGet, target: "/artifactory/rk/dir/payload.bin.zip", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "colonMatrix", method: http.MethodGet, target: "/artifactory/rk/dir/payload.bin;x=1", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "query", method: http.MethodGet, target: "/artifactory/rk/dir/payload.bin?x=1", wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "bareQuery", method: http.MethodGet, target: "/artifactory/rk/dir/payload.bin?", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "uriTooLong", method: http.MethodGet, target: "/artifactory/rk/" + strings.Repeat("a", 9000) + ".bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "rangeHeader", method: http.MethodGet, target: validTarget, header: http.Header{"Range": []string{"bytes=0-1"}}, wantCode: http.StatusNotImplemented, wantBody: dtFixedNotImplemented},
		{name: "rangeBeatsAuth", method: http.MethodGet, target: validTarget, header: http.Header{"Range": []string{"bytes=0-1"}}, authResult: &denied, wantCode: http.StatusNotImplemented, wantBody: dtFixedNotImplemented},
		{name: "rangeEqualFoldCase", method: http.MethodGet, target: validTarget, header: http.Header{"rAnGe": []string{"bytes=0-1"}}, wantCode: http.StatusNotImplemented, wantBody: dtFixedNotImplemented},
		{name: "ifMatchHeader", method: http.MethodGet, target: validTarget, header: http.Header{"If-Match": []string{"\"x\""}}, wantCode: http.StatusNotImplemented, wantBody: dtFixedNotImplemented},
		{name: "ifNoneMatchEmptyValue", method: http.MethodGet, target: validTarget, header: http.Header{"if-none-match": []string{""}}, wantCode: http.StatusNotImplemented, wantBody: dtFixedNotImplemented},
		{name: "ifModifiedSinceNilValue", method: http.MethodGet, target: validTarget, header: http.Header{"If-Modified-Since": {}}, wantCode: http.StatusNotImplemented, wantBody: dtFixedNotImplemented},
		{name: "unauthenticated401", method: http.MethodGet, target: validTarget, authResult: &unauth, wantCode: http.StatusUnauthorized, wantBody: dtFixedUnauthorized, wantAuth: unauth.Challenge},
		{name: "denied403", method: http.MethodGet, target: validTarget, authResult: &denied, wantCode: http.StatusForbidden, wantBody: dtFixedForbidden},
		{name: "authError500", method: http.MethodGet, target: validTarget, authErr: errors.New("dt: auth backend down"), wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantLookups: 0, wantReport: httpserver.DownloadFailureAuth},
		{name: "authZeroDecision500", method: http.MethodGet, target: validTarget, authResult: &zeroDecision, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantReport: httpserver.DownloadFailureAuth},
		{name: "authUnknownDecision500", method: http.MethodGet, target: validTarget, authResult: &unknownDecision, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantReport: httpserver.DownloadFailureAuth},
		{name: "authUnauthNoChallenge500", method: http.MethodGet, target: validTarget, authResult: &unauthNoChallenge, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantReport: httpserver.DownloadFailureAuth},
		{name: "authUnauthBadChallenge500", method: http.MethodGet, target: validTarget, authResult: &unauthBadChallenge, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantReport: httpserver.DownloadFailureAuth},
		{name: "authDeniedWithChallenge500", method: http.MethodGet, target: validTarget, authResult: &deniedWithChallenge, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantReport: httpserver.DownloadFailureAuth},
		{name: "missingNode404", method: http.MethodGet, target: validTarget, wantCode: http.StatusNotFound, wantBody: dtFixedNotFound, wantLookups: 1},
		{name: "lookupError500", method: http.MethodGet, target: validTarget, storeErr: errors.New("dt: lookup blew up"), wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantLookups: 1, wantReport: httpserver.DownloadFailureLookup},
		{name: "projectionIdentityMismatch500", method: http.MethodGet, target: validTarget, storeSrc: &mismatch, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantLookups: 1, wantReport: httpserver.DownloadFailureProjection},
		{name: "projectionUppercaseHex500", method: http.MethodGet, target: validTarget, storeSrc: &src, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantLookups: 1, wantReport: httpserver.DownloadFailureProjection},
		{name: "openFailure500", method: http.MethodGet, target: validTarget, storeSrc: &src, wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantLookups: 1, wantOpens: 1, wantReport: httpserver.DownloadFailureOpen},
	}
}

func TestDownloadTransportFixedErrorResponses(t *testing.T) {
	for _, tc := range dtErrorTable() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// projectionUppercaseHex500 单独构造大写摘要；其余沿用表格。
			// projectionUppercaseHex500 builds its uppercase digest separately.
			if tc.name == "projectionUppercaseHex500" {
				upper := dtSource(dtRepoKey, "dir", "payload.bin",
					strings.ToUpper(strings.Repeat("a", 40)), strings.Repeat("c", 32), strings.Repeat("d", 64),
					16, 1717171717000)
				tc.storeSrc = &upper
			}
			dtRunErrorCase(t, tc)
		})
	}
}

// TestDownloadTransportHeadErrorsBodyless 用同一表格抽样证明 HEAD 错误
// 零正文且状态/长度一致；真实传输本会剥离正文，断言仍检查线上的
// Content-Length 与 Content-Type。
// TestDownloadTransportHeadErrorsBodyless samples the same table to prove HEAD
// errors stay bodyless with matching status/length; the real transport strips
// bodies anyway, so the wire Content-Length and Content-Type carry the check.
func TestDownloadTransportHeadErrorsBodyless(t *testing.T) {
	validTarget, _ := dtValidTargetSrc()
	unauth := dtAuthUnauthenticated
	samples := []dtErrorCase{
		// 合法 HEAD 不存在 405 分支；用 HEAD 的 403/500 补拒绝与内部失败
		// 画像，POST/PUT/DELETE 的 405 已由主表格覆盖。
		// A valid HEAD has no 405 branch; HEAD 403/500 cover the denial and
		// internal-failure profiles instead, while POST/PUT/DELETE 405 rows
		// stay in the main table.
		{name: "head403", method: http.MethodHead, target: validTarget, authResult: &dtAuthDenied, wantCode: http.StatusForbidden, wantBody: dtFixedForbidden},
		{name: "head500AuthError", method: http.MethodHead, target: validTarget, authErr: errors.New("dt: auth backend down"), wantCode: http.StatusInternalServerError, wantBody: dtFixedInternal, wantReport: httpserver.DownloadFailureAuth},
		{name: "head400", method: http.MethodHead, target: "/artifactory/rk/dir/p%61yload.bin", raw: true, wantCode: http.StatusBadRequest, wantBody: dtFixedBadRequest},
		{name: "head501", method: http.MethodHead, target: validTarget, header: http.Header{"Range": []string{"bytes=0-1"}}, wantCode: http.StatusNotImplemented, wantBody: dtFixedNotImplemented},
		{name: "head401", method: http.MethodHead, target: validTarget, authResult: &unauth, wantCode: http.StatusUnauthorized, wantBody: dtFixedUnauthorized, wantAuth: unauth.Challenge},
		{name: "head404", method: http.MethodHead, target: validTarget, wantCode: http.StatusNotFound, wantBody: dtFixedNotFound, wantLookups: 1},
	}
	for _, tc := range samples {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dtRunErrorCase(t, tc)
		})
	}
}

// dtStreamEnv 构造一个成功打开、按脚本读取的流环境；watch 提供处理器
// 退出事件，使 close 计数与报告断言有真实 happens-before 边。
// dtStreamEnv builds an environment that opens successfully and reads the
// scripted stream; watch exposes the handler-completion event so close-count
// and report assertions get a real happens-before edge.
func dtStreamEnv(t *testing.T, body io.ReadCloser, size int64) (*httptest.Server, *dtReporter, *dtWatch) {
	t.Helper()
	src := dtValidSource("dir", "payload.bin", size, 1717171717000)
	opener := &dtContent{open: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
	rep := &dtReporter{}
	store := &dtStoreLike{src: src, found: true}
	watch := dtNewWatch()
	ts := dtNewServer(t, watch.wrap(dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, rep)))
	return ts, rep, watch
}

// dtReadBody 读取正文并容忍提交后中断造成的 EOF 错误。
// dtReadBody reads the body and tolerates EOF errors from committed aborts.
func dtReadBody(t *testing.T, resp *http.Response) ([]byte, error) {
	t.Helper()
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// TestDownloadTransportPostCommitStreamFaults 在真实传输上注入提交后的
// 读取故障：短读、长读与字节后错误。响应必须已提交（客户端可见 200 与
// 部分字节），随后连接中断、只报告 stream、流恰好关闭一次，绝无第二份
// 错误正文。
// TestDownloadTransportPostCommitStreamFaults injects post-commit read faults
// over the real transport: short, long and error-after-bytes. The response
// must already be committed (client sees 200 plus partial bytes), then the
// connection breaks, exactly one stream report is filed, the body closes
// exactly once and no second error body ever appears.
func TestDownloadTransportPostCommitStreamFaults(t *testing.T) {
	t.Run("shortRead", func(t *testing.T) {
		body := dtScriptBody(nil, dtData(bytes.Repeat([]byte{0x11}, 8192)), dtEOF())
		ts, rep, watch := dtStreamEnv(t, body, 16384)
		resp, err := dtHTTPClient.Get(ts.URL + "/artifactory/rk/dir/payload.bin")
		dtRequireNoError(t, "GET", err)
		got, readErr := dtReadBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (response must be committed)", resp.StatusCode)
		}
		if len(got) == 0 || int64(len(got)) >= 16384 {
			t.Fatalf("body length = %d, want 0 < n < 16384", len(got))
		}
		if readErr == nil {
			t.Fatalf("read error = nil, want truncated-transfer error")
		}
		// 先等处理器退出（晚于产品内的延迟 Close），再检查计数与报告。
		// Wait for handler completion (after the product's deferred close)
		// before inspecting counters and reports.
		dtWaitChan(t, "handler completion", watch.handlerDone)
		if got := body.closeCount(); got != 1 {
			t.Fatalf("closes = %d, want exactly 1", got)
		}
		if !rep.only(httpserver.DownloadFailureStream) {
			t.Fatalf("reported categories = %v, want only stream", rep.snapshot())
		}
	})
	t.Run("longRead", func(t *testing.T) {
		// 第一块恰好等于声明长度：先冲刷成功头；随后超读在提交后处置。
		// 也接纳"头部前 EOF"：实现可以在首个写之前就检测总量超出并中止，
		// 此时客户端只见传输错误，线上 200 不做假定。
		// The first chunk equals the declared length so success headers can
		// flush; the excess is handled post-commit. An early EOF before
		// headers is equally accepted: an implementation may detect the total
		// overrun before its first write and abort, leaving the client with a
		// bare transport error, so wire 200 is not assumed.
		steps := make(chan dtStep, 3)
		body := &dtBody{steps: steps}
		steps <- dtData(bytes.Repeat([]byte{0x22}, 8192))
		steps <- dtData(bytes.Repeat([]byte{0x23}, 32*1024))
		steps <- dtEOF()
		ts, rep, watch := dtStreamEnv(t, body, 8192)
		// 注册在 server 之后（LIFO）：释放先于 server.Close。
		// Registered after the server (LIFO): release precedes server.Close.
		t.Cleanup(dtReleaseSteps(steps))
		resp, err := dtHTTPClient.Get(ts.URL + "/artifactory/rk/dir/payload.bin")
		if err == nil {
			got, _ := dtReadBody(t, resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 when headers reached the wire", resp.StatusCode)
			}
			// 客户端按声明长度最多读 8192 字节；超读的处置在服务端完成。
			// The client reads at most the declared 8192 bytes; handling the
			// excess is the server's job.
			if len(got) > 8192 {
				t.Fatalf("body length = %d, want <= 8192", len(got))
			}
		}
		dtWaitChan(t, "handler completion", watch.handlerDone)
		if got := body.closeCount(); got != 1 {
			t.Fatalf("closes = %d, want exactly 1", got)
		}
		if !rep.only(httpserver.DownloadFailureStream) {
			t.Fatalf("reported categories = %v, want only stream", rep.snapshot())
		}
	})
	t.Run("errorAfterBytes", func(t *testing.T) {
		body := dtScriptBody(nil,
			dtData(bytes.Repeat([]byte{0x33}, 8192)),
			dtFail(errors.New("dt: synthetic read failure")),
		)
		ts, rep, watch := dtStreamEnv(t, body, 16384)
		resp, err := dtHTTPClient.Get(ts.URL + "/artifactory/rk/dir/payload.bin")
		dtRequireNoError(t, "GET", err)
		got, readErr := dtReadBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (response must be committed)", resp.StatusCode)
		}
		if len(got) == 0 || int64(len(got)) >= 16384 {
			t.Fatalf("body length = %d, want 0 < n < 16384", len(got))
		}
		if readErr == nil {
			t.Fatalf("read error = nil, want truncated-transfer error")
		}
		dtWaitChan(t, "handler completion", watch.handlerDone)
		if got := body.closeCount(); got != 1 {
			t.Fatalf("closes = %d, want exactly 1", got)
		}
		if !rep.only(httpserver.DownloadFailureStream) {
			t.Fatalf("reported categories = %v, want only stream", rep.snapshot())
		}
	})
}

// TestDownloadTransportCloseFailureAfterCopy 证明完整拷贝后的关闭错误只
// 报告 close 并中断；已提交字节是否可见不做断言（不能保证客户端看到
// 关闭失败）。
// TestDownloadTransportCloseFailureAfterCopy proves a close error after a
// complete copy reports close and aborts; visibility of already-committed
// bytes is deliberately not asserted (the client is not guaranteed to see the
// close failure).
func TestDownloadTransportCloseFailureAfterCopy(t *testing.T) {
	payload := bytes.Repeat([]byte{0x44}, 32*1024)
	body := dtScriptBody(errors.New("dt: synthetic close failure"),
		dtData(payload), dtEOF())
	ts, rep, watch := dtStreamEnv(t, body, int64(len(payload)))
	resp, err := dtHTTPClient.Get(ts.URL + "/artifactory/rk/dir/payload.bin")
	dtRequireNoError(t, "GET", err)
	got, _ := dtReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(got) > len(payload) {
		t.Fatalf("body length = %d, want <= %d", len(got), len(payload))
	}
	// 尾部缓冲可能丢失，仅要求绝大多数字节已经送达。
	// The buffered tail may be lost; require only the vast majority.
	if len(got) < len(payload)-8192 {
		t.Fatalf("body length = %d, want >= %d", len(got), len(payload)-8192)
	}
	// 关闭失败报告与 Close 都发生在处理器退出之前，先等退出事件。
	// Both the close report and Body.Close precede handler exit; wait for
	// the completion event first.
	dtWaitChan(t, "handler completion", watch.handlerDone)
	if got := body.closeCount(); got != 1 {
		t.Fatalf("closes = %d, want exactly 1", got)
	}
	if !rep.only(httpserver.DownloadFailureClose) {
		t.Fatalf("reported categories = %v, want only close", rep.snapshot())
	}
}

// TestDownloadTransportRequestCancellation 覆盖授权阶段与流阶段的取消：
// 报告 cancel、不伪造默认 200、流恰好关闭一次；头部前中断时客户端拿到
// 传输错误（EOF 先于头部同样有效）。
// TestDownloadTransportRequestCancellation covers cancellation during auth and
// mid-stream: cancel is reported, no fabricated default 200 appears, the body
// closes exactly once; an abort before headers surfaces as a transport error
// (EOF before headers is equally valid).
func TestDownloadTransportRequestCancellation(t *testing.T) {
	t.Run("duringAuth", func(t *testing.T) {
		entered := make(chan struct{})
		auth := &dtAuth{result: dtAuthAllow}
		auth.hook = func(ctx context.Context, _ *http.Request, _ repository.Path) (httpserver.DownloadAuthResult, error) {
			close(entered)
			// 即便被测实现传入了不会取消的 ctx，钩子自身也有界，避免
			// server.Close 永久等待卡死的提供者。
			// The hook itself stays bounded even if the implementation under
			// test passes a never-cancelled ctx, so server.Close never waits
			// forever on a stuck provider.
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
			return dtAuthAllow, nil
		}
		src := dtValidSource("dir", "payload.bin", 16, 0)
		opener := &dtContent{}
		store := &dtStoreLike{src: src, found: true}
		rep := &dtReporter{}
		watch := dtNewWatch()
		ts := dtNewServer(t, watch.wrap(dtNewHandler(t, auth, store, opener, rep)))

		ctx, cancel := context.WithCancel(context.Background())
		// 注册在 server 之后（LIFO）：失败路径先释放请求，再关 server。
		// Registered after the server (LIFO): failure paths release the
		// request before the server closes.
		t.Cleanup(cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/artifactory/rk/dir/payload.bin", nil)
		dtRequireNoError(t, "build request", err)
		type result struct {
			resp *http.Response
			err  error
		}
		done := make(chan result, 1)
		go func() {
			resp, err := dtHTTPClient.Do(req)
			done <- result{resp: resp, err: err}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("auth was not called within timeout")
		}
		cancel()
		var res result
		select {
		case res = <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("client did not finish within timeout")
		}
		if res.err == nil {
			res.resp.Body.Close()
			t.Fatalf("client error = nil, want transport error (EOF before headers is valid)")
		}
		// 客户端错误不构成服务端完成边：等处理器退出再查计数与报告。
		// A client error is not a server completion edge: wait for handler
		// exit before inspecting counters and reports.
		dtWaitChan(t, "handler completion", watch.handlerDone)
		if got := store.lookupCount(); got != 0 {
			t.Fatalf("lookups = %d, want 0 after cancellation", got)
		}
		if !rep.only(httpserver.DownloadFailureCancel) {
			t.Fatalf("reported categories = %v, want only cancel", rep.snapshot())
		}
	})
	t.Run("duringStream", func(t *testing.T) {
		first := bytes.Repeat([]byte{0x66}, 8192)
		steps := make(chan dtStep, 2)
		body := &dtBody{steps: steps}
		ts, rep, watch := dtStreamEnv(t, body, 65536)
		// 失败路径也要释放阻塞的合成 Read（EOF），server.Close 不悬挂。
		// Failure paths must also release the blocked synthetic Read (EOF)
		// so server.Close never hangs.
		t.Cleanup(dtReleaseSteps(steps))

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/artifactory/rk/dir/payload.bin", nil)
		dtRequireNoError(t, "build request", err)
		received := make(chan struct{}, 1)
		readResult := make(chan error, 1)
		go func() {
			resp, err := dtHTTPClient.Do(req)
			if err != nil {
				readResult <- err
				return
			}
			firstBuf := make([]byte, len(first))
			if _, err := io.ReadFull(resp.Body, firstBuf); err != nil {
				resp.Body.Close()
				readResult <- err
				return
			}
			received <- struct{}{}
			_, restErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			readResult <- restErr
		}()
		steps <- dtData(first)
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatalf("no bytes received within timeout")
		}
		cancel()
		// 用记录到的服务端 r.Context().Done 事件确认取消已传播，再喂读
		// 取错误；不用睡眠推断。
		// Confirm propagation through the recorded server-side
		// r.Context().Done event before feeding the read error; no sleeping.
		dtWaitChan(t, "server request context cancellation", watch.ctxCancelled)
		steps <- dtFail(errors.New("dt: read failed after cancellation"))
		if err := dtRecvErr(t, "client read result", readResult); err == nil {
			t.Fatalf("client read error = nil, want transport error after cancellation")
		}
		dtWaitChan(t, "handler completion", watch.handlerDone)
		if got := body.closeCount(); got != 1 {
			t.Fatalf("closes = %d, want exactly 1", got)
		}
		if !rep.only(httpserver.DownloadFailureCancel) {
			t.Fatalf("reported categories = %v, want only cancel", rep.snapshot())
		}
	})
}

// dtServeDirect 直连调用处理器并捕获 panic；仅提交后与取消路径允许
// http.ErrAbortHandler。
// dtServeDirect invokes the handler directly and captures panics; only
// post-commit and cancellation paths may raise http.ErrAbortHandler.
func dtServeDirect(t *testing.T, h http.Handler, w *dtObservedWriter, method, target string) (recovered any) {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	func() {
		defer func() { recovered = recover() }()
		h.ServeHTTP(w, req)
	}()
	return recovered
}

// TestDownloadTransportDirectObservedWriter 用可观测写入器直连证明：
// 成功恰好一次 WriteHeader 且无 panic；写错误报告 stream、单次
// WriteHeader、恰好一次关闭并以 ErrAbortHandler 中止；HEAD 成功与错误
// 均绝不调用 Write。
// TestDownloadTransportDirectObservedWriter uses an observed writer directly:
// success writes the header exactly once without panic; a write error reports
// stream, keeps a single WriteHeader, closes exactly once and aborts with
// ErrAbortHandler; HEAD success and error paths never call Write.
func TestDownloadTransportDirectObservedWriter(t *testing.T) {
	payload := bytes.Repeat([]byte{0x77}, 32*1024)

	t.Run("successSingleWriteHeader", func(t *testing.T) {
		body := dtScriptBody(nil, dtData(payload), dtEOF())
		src := dtValidSource("dir", "payload.bin", int64(len(payload)), 1717171717000)
		opener := &dtContent{open: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
		store := &dtStoreLike{src: src, found: true}
		h := dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, &dtReporter{})
		w := dtObserve(httptest.NewRecorder())
		recovered := dtServeDirect(t, h, w, http.MethodGet, "/artifactory/rk/dir/payload.bin")
		if recovered != nil {
			t.Fatalf("panic = %v, want none on success", recovered)
		}
		headerCalls, writes, bytesWritten := w.stats()
		if headerCalls != 1 || w.Code != http.StatusOK {
			t.Fatalf("WriteHeader calls = %d, code = %d, want 1 and 200", headerCalls, w.Code)
		}
		if writes == 0 || bytesWritten != len(payload) {
			t.Fatalf("writes = %d, bytes = %d, want writes > 0 and %d bytes", writes, bytesWritten, len(payload))
		}
		if !bytes.Equal(w.Body.Bytes(), payload) {
			t.Fatalf("body length = %d, want %d", w.Body.Len(), len(payload))
		}
		if got := body.closeCount(); got != 1 {
			t.Fatalf("closes = %d, want exactly 1", got)
		}
	})
	t.Run("writeErrorAbortsAfterCommit", func(t *testing.T) {
		body := dtScriptBody(nil, dtData(payload), dtEOF())
		src := dtValidSource("dir", "payload.bin", int64(len(payload)), 1717171717000)
		opener := &dtContent{open: func(context.Context, string) (io.ReadCloser, error) { return body, nil }}
		store := &dtStoreLike{src: src, found: true}
		rep := &dtReporter{}
		h := dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, rep)
		w := dtObserve(httptest.NewRecorder())
		w.failWritesAfter = 4096
		w.writeErr = errors.New("dt: synthetic write failure")
		recovered := dtServeDirect(t, h, w, http.MethodGet, "/artifactory/rk/dir/payload.bin")
		if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("panic = %v, want http.ErrAbortHandler", recovered)
		}
		headerCalls, _, _ := w.stats()
		if headerCalls != 1 {
			t.Fatalf("WriteHeader calls = %d, want exactly 1 (no second header)", headerCalls)
		}
		if got := body.closeCount(); got != 1 {
			t.Fatalf("closes = %d, want exactly 1", got)
		}
		if !rep.only(httpserver.DownloadFailureStream) {
			t.Fatalf("reported categories = %v, want only stream", rep.snapshot())
		}
	})
	t.Run("headSuccessNeverWrites", func(t *testing.T) {
		src := dtValidSource("dir", "payload.bin", 4096, 1717171717000)
		opener := &dtContent{}
		store := &dtStoreLike{src: src, found: true}
		h := dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, &dtReporter{})
		w := dtObserve(httptest.NewRecorder())
		recovered := dtServeDirect(t, h, w, http.MethodHead, "/artifactory/rk/dir/payload.bin")
		if recovered != nil {
			t.Fatalf("panic = %v, want none", recovered)
		}
		headerCalls, writes, _ := w.stats()
		if headerCalls != 1 || w.Code != http.StatusOK {
			t.Fatalf("WriteHeader calls = %d, code = %d, want 1 and 200", headerCalls, w.Code)
		}
		if writes != 0 {
			t.Fatalf("Write calls = %d, want 0 for HEAD", writes)
		}
		if w.Body.Len() != 0 {
			t.Fatalf("body = %q, want empty for HEAD", w.Body.String())
		}
		if got := opener.openCount(); got != 0 {
			t.Fatalf("opens = %d, want 0 for HEAD", got)
		}
	})
	t.Run("headErrorNeverWrites", func(t *testing.T) {
		opener := &dtContent{}
		store := &dtStoreLike{}
		h := dtNewHandler(t, &dtAuth{result: dtAuthAllow}, store, opener, &dtReporter{})
		w := dtObserve(httptest.NewRecorder())
		recovered := dtServeDirect(t, h, w, http.MethodHead, "/artifactory/rk/dir/payload.bin")
		if recovered != nil {
			t.Fatalf("panic = %v, want none on precommit 404", recovered)
		}
		headerCalls, writes, _ := w.stats()
		if headerCalls != 1 || w.Code != http.StatusNotFound {
			t.Fatalf("WriteHeader calls = %d, code = %d, want 1 and 404", headerCalls, w.Code)
		}
		if writes != 0 {
			t.Fatalf("Write calls = %d, want 0 for HEAD error", writes)
		}
		if w.Body.Len() != 0 {
			t.Fatalf("body = %q, want empty for HEAD error", w.Body.String())
		}
	})
}

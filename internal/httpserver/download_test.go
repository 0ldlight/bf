// This file is the UNIT behavioral contract for the ordinary download
// adapter. It consumes only the frozen exported API of NewDownloadHandler
// and drives the handler through httptest recorder requests whose raw
// RequestURI is the sole identity input. Transport-level streaming,
// cancellation and real-network behavior belong to the separate transport
// and Postgres suites.
// 本文件是普通下载适配器的 UNIT 行为契约：只依赖冻结导出 API，通过
// httptest 记录器驱动处理器，raw RequestURI 是唯一身份输入。传输层流式、
// 取消与真实网络行为属于独立的 transport 与 Postgres 套件。
package httpserver_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// duRepoKey is the single configured repository key used across the suite.
const duRepoKey = "du.repo-1"

// duEmptySHA1 is the exact lowercase empty-content SHA-1.
const duEmptySHA1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"

// duAuth records the authorize call and replays a canned decision or error.
type duAuth struct {
	result    httpserver.DownloadAuthResult
	err       error
	calls     int
	lastPath  repository.Path
	lastReq   *http.Request
	lastCtxOK bool
}

func (a *duAuth) Authorize(ctx context.Context, r *http.Request, p repository.Path) (httpserver.DownloadAuthResult, error) {
	a.calls++
	a.lastPath = p
	a.lastReq = r
	a.lastCtxOK = ctx != nil && r != nil
	return a.result, a.err
}

// duAllowAuth always allows and is safe to share across requests.
type duAllowAuth struct{}

func (duAllowAuth) Authorize(context.Context, *http.Request, repository.Path) (httpserver.DownloadAuthResult, error) {
	return httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}, nil
}

// duStore records the single lookup and returns a canned source or error.
type duStore struct {
	source   repository.FileSnapshotSource
	err      error
	calls    int
	lastPath repository.Path
}

func (s *duStore) LookupSnapshotSource(_ context.Context, p repository.Path) (repository.FileSnapshotSource, error) {
	s.calls++
	s.lastPath = p
	return s.source, s.err
}

// duBody is a static reader the opener hands out; reads stop after the
// canned bytes without EOF surprises beyond the first one.
type duBody struct {
	data   []byte
	offset int
	closed bool
}

func (b *duBody) Read(p []byte) (int, error) {
	if b.offset >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.offset:])
	b.offset += n
	return n, nil
}

func (b *duBody) Close() error {
	b.closed = true
	return nil
}

// duOpener records opens and returns a canned body or error.
type duOpener struct {
	body    io.ReadCloser
	err     error
	opens   int
	lastKey string
}

func (o *duOpener) Open(_ context.Context, key string) (io.ReadCloser, error) {
	o.opens++
	o.lastKey = key
	if o.err != nil {
		return nil, o.err
	}
	return o.body, nil
}

// duReporter collects reported failure categories under a mutex because the
// injected report callback must tolerate any caller concurrency.
type duReporter struct {
	mu       sync.Mutex
	category []httpserver.DownloadFailure
}

func (r *duReporter) report(c httpserver.DownloadFailure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.category = append(r.category, c)
}

func (r *duReporter) categories() []httpserver.DownloadFailure {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]httpserver.DownloadFailure(nil), r.category...)
}

// duPath builds a file Path or fails the test on constructor rejection.
func duPath(t *testing.T, repo, parent, name string) repository.Path {
	t.Helper()
	p, err := repository.NewPath(repo, parent, name, true)
	if err != nil {
		t.Fatalf("NewPath(%q,%q,%q) error = %v", repo, parent, name, err)
	}
	return p
}

// duMeta assembles canonical lowercase checksum metadata.
func duMeta(sha1, md5, sha256 string, size int64) content.Metadata {
	return content.Metadata{SHA1: sha1, MD5: md5, SHA256: sha256, Size: size}
}

// duSource builds a fully valid snapshot source for the given path.
func duSource(path repository.Path, modMillis int64, m content.Metadata) repository.FileSnapshotSource {
	meta := repository.NewFileMetadata(repository.FileMetadataInput{ModifiedMillis: modMillis})
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID:       71,
			Path:     path,
			Content:  m,
			Metadata: &meta,
		},
		SHA1ActualValid:   true,
		MD5ActualValid:    true,
		SHA256ActualValid: true,
	}
}

// duHandler wires the frozen constructor with the suite repository key.
func duHandler(t *testing.T, auth httpserver.DownloadAuth, store localrepo.FileReadStore, opener localrepo.FileOpener, report func(httpserver.DownloadFailure)) http.Handler {
	t.Helper()
	h, err := httpserver.NewDownloadHandler(httpserver.DownloadDeps{
		RepoKey: duRepoKey,
		Auth:    auth,
		Store:   store,
		Content: opener,
		Report:  report,
	})
	if err != nil {
		t.Fatalf("NewDownloadHandler error = %v", err)
	}
	if h == nil {
		t.Fatal("NewDownloadHandler returned nil handler")
	}
	return h
}

// duRawRequest builds a recorder request carrying an exact raw RequestURI.
// The placeholder target only seeds a parsable URL; the handler must treat
// RequestURI as the sole identity source.
func duRawRequest(method, rawURI string) *http.Request {
	r := httptest.NewRequest(method, "http://du.test/seed", nil)
	r.RequestURI = rawURI
	return r
}

// duFixedBody is the fixed plain-text body for a status code.
func duFixedBody(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "bad request\n"
	case http.StatusUnauthorized:
		return "unauthorized\n"
	case http.StatusForbidden:
		return "forbidden\n"
	case http.StatusNotFound:
		return "not found\n"
	case http.StatusMethodNotAllowed:
		return "method not allowed\n"
	case http.StatusNotImplemented:
		return "not implemented\n"
	default:
		return "internal error\n"
	}
}

// duAssertFixedError checks one fixed error response. For HEAD the handler
// must never write a body yet still advertise the explicit error length.
func duAssertFixedError(t *testing.T, rec *httptest.ResponseRecorder, code int, head bool) {
	t.Helper()
	want := duFixedBody(code)
	if rec.Code != code {
		t.Fatalf("status = %d, want %d", rec.Code, code)
	}
	if head {
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD body = %q, want empty", rec.Body.String())
		}
	} else if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", ct, "text/plain; charset=utf-8")
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(want)) {
		t.Fatalf("Content-Length = %q, want %d", cl, len(want))
	}
}

// TestNewDownloadHandlerRejectsInvalidConfiguration verifies the fail-closed
// constructor: no dependency, no handler.
// 构造器失败即关闭：缺任一依赖即拒绝构造。
func TestNewDownloadHandlerRejectsInvalidConfiguration(t *testing.T) {
	validRepo := duRepoKey
	cases := []struct {
		name    string
		repoKey string
		auth    httpserver.DownloadAuth
		store   localrepo.FileReadStore
		content localrepo.FileOpener
	}{
		{"empty repo key", "", &duAuth{}, &duStore{}, &duOpener{}},
		{"dot repo key", ".", &duAuth{}, &duStore{}, &duOpener{}},
		{"double-dot repo key", "..", &duAuth{}, &duStore{}, &duOpener{}},
		{"multi-segment repo key", "a/b", &duAuth{}, &duStore{}, &duOpener{}},
		{"space in repo key", "a b", &duAuth{}, &duStore{}, &duOpener{}},
		{"colon in repo key", "a:b", &duAuth{}, &duStore{}, &duOpener{}},
		{"percent in repo key", "a%2f", &duAuth{}, &duStore{}, &duOpener{}},
		{"plus in repo key", "a+b", &duAuth{}, &duStore{}, &duOpener{}},
		{"unicode repo key", "répo", &duAuth{}, &duStore{}, &duOpener{}},
		{"nil auth", validRepo, nil, &duStore{}, &duOpener{}},
		{"nil store", validRepo, &duAuth{}, nil, &duOpener{}},
		{"nil content", validRepo, &duAuth{}, &duStore{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := httpserver.NewDownloadHandler(httpserver.DownloadDeps{
				RepoKey: tc.repoKey,
				Auth:    tc.auth,
				Store:   tc.store,
				Content: tc.content,
			})
			if !errors.Is(err, httpserver.ErrDownloadConfiguration) {
				t.Fatalf("error = %v, want ErrDownloadConfiguration", err)
			}
			if h != nil {
				t.Fatal("handler = non-nil, want nil")
			}
		})
	}
}

// TestNewDownloadHandlerAcceptsValidConfiguration covers accepted shapes,
// including an omitted optional report callback.
// 合法配置可构造；Report 可省略。
func TestNewDownloadHandlerAcceptsValidConfiguration(t *testing.T) {
	for _, repoKey := range []string{"a", duRepoKey, "A9._-x"} {
		h, err := httpserver.NewDownloadHandler(httpserver.DownloadDeps{
			RepoKey: repoKey,
			Auth:    &duAuth{},
			Store:   &duStore{},
			Content: &duOpener{},
		})
		if err != nil {
			t.Fatalf("repoKey %q error = %v, want nil", repoKey, err)
		}
		if h == nil {
			t.Fatalf("repoKey %q handler = nil", repoKey)
		}
	}
}

// TestDownloadMethodGate405: the method gate runs first, produces the fixed
// 405 body with a single Allow field and touches no dependency.
// 方法闸门最先执行：固定 405、单字段 Allow、零依赖调用。
func TestDownloadMethodGate405(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect} {
		t.Run(method, func(t *testing.T) {
			auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
			store := &duStore{}
			opener := &duOpener{}
			h := duHandler(t, auth, store, opener, nil)
			rec := httptest.NewRecorder()
			// Even an out-of-profile URI stays 405: method precedes the
			// raw-URI gate.
			// 域外 URI 仍为 405：方法闸门先于 URI 闸门。
			h.ServeHTTP(rec, duRawRequest(method, "/artifactory/"+duRepoKey+"/%zz"))

			duAssertFixedError(t, rec, http.StatusMethodNotAllowed, false)
			if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
				t.Fatalf("Allow = %q, want %q", allow, "GET, HEAD")
			}
			if auth.calls != 0 || store.calls != 0 || opener.opens != 0 {
				t.Fatalf("deps touched: auth=%d store=%d open=%d", auth.calls, store.calls, opener.opens)
			}
		})
	}
}

// TestDownloadRawURIGate400: out-of-profile raw URIs fail with 400 before
// any dependency is touched; the raw bytes are never decoded or cleaned into
// a supported request.
// 域外 raw URI 返回 400 且不触碰依赖；原始字节不得被解码/清洗成支持请求。
func TestDownloadRawURIGate400(t *testing.T) {
	cases := []struct {
		name   string
		rawURI string
	}{
		{"wrong prefix", "/download/" + duRepoKey + "/a.bin"},
		{"wrong repo", "/artifactory/other-repo/a.bin"},
		{"missing file", "/artifactory/" + duRepoKey},
		{"missing file trailing slash", "/artifactory/" + duRepoKey + "/"},
		{"empty segment", "/artifactory/" + duRepoKey + "//a.bin"},
		{"inner empty segment", "/artifactory/" + duRepoKey + "/x//a.bin"},
		{"trailing slash", "/artifactory/" + duRepoKey + "/a.bin/"},
		{"dot segment", "/artifactory/" + duRepoKey + "/./a.bin"},
		{"double-dot segment", "/artifactory/" + duRepoKey + "/../a.bin"},
		{"bare dot name", "/artifactory/" + duRepoKey + "/."},
		{"percent encoding", "/artifactory/" + duRepoKey + "/%70ayload.bin"},
		{"encoded slash", "/artifactory/" + duRepoKey + "/a%2Fb.bin"},
		{"plus in name", "/artifactory/" + duRepoKey + "/pay+load.bin"},
		{"colon in name", "/artifactory/" + duRepoKey + "/a:b.bin"},
		{"space in name", "/artifactory/" + duRepoKey + "/pay load.bin"},
		{"control byte", "/artifactory/" + duRepoKey + "/pay\x01load.bin"},
		{"unicode name", "/artifactory/" + duRepoKey + "/载荷.bin"},
		{"no extension", "/artifactory/" + duRepoKey + "/payload"},
		{"empty stem", "/artifactory/" + duRepoKey + "/.bin"},
		{"empty stem txt", "/artifactory/" + duRepoKey + "/.txt"},
		{"uppercase extension", "/artifactory/" + duRepoKey + "/payload.BIN"},
		{"mixed case extension", "/artifactory/" + duRepoKey + "/payload.Bin"},
		{"longer suffix", "/artifactory/" + duRepoKey + "/payload.binaries"},
		{"dot-suffix only", "/artifactory/" + duRepoKey + "/payload.bins"},
		{"metadata suffix", "/artifactory/" + duRepoKey + "/payload.bin:properties"},
		{"absolute form", "http://du.test/artifactory/" + duRepoKey + "/payload.bin"},
		{"authority form", "//du.test/artifactory/" + duRepoKey + "/payload.bin"},
		{"asterisk form", "*"},
		{"uri over 8192 bytes", "/artifactory/" + duRepoKey + "/" + strings.Repeat("a", 8192-4-len("/artifactory/"+duRepoKey+"/")) + ".bin?x"},
		{"uri over 8192 bytes plain", "/artifactory/" + duRepoKey + "/" + strings.Repeat("a", 8200) + ".bin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
			store := &duStore{}
			opener := &duOpener{}
			h := duHandler(t, auth, store, opener, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, duRawRequest(http.MethodGet, tc.rawURI))

			duAssertFixedError(t, rec, http.StatusBadRequest, false)
			if auth.calls != 0 || store.calls != 0 || opener.opens != 0 {
				t.Fatalf("deps touched: auth=%d store=%d open=%d", auth.calls, store.calls, opener.opens)
			}
		})
	}
}

// TestDownloadRawURIBoundaryAllowed: a valid URI of exactly 8192 bytes is
// inside the profile and must not be rejected by the length cap.
// 恰好 8192 字节的合法 URI 在界内，不得被长度上限拒绝。
func TestDownloadRawURIBoundaryAllowed(t *testing.T) {
	name := strings.Repeat("a", 8192-len("/artifactory/"+duRepoKey+"/")-len(".bin")) + ".bin"
	raw := "/artifactory/" + duRepoKey + "/" + name
	if len(raw) != 8192 {
		t.Fatalf("test URI length = %d, want 8192", len(raw))
	}
	path := duPath(t, duRepoKey, "", name)
	store := &duStore{source: duSource(path, 0, duMeta(duEmptySHA1, strings.Repeat("d", 32), strings.Repeat("e", 64), 0))}
	h := duHandler(t, duAllowAuth{}, store, &duOpener{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, duRawRequest(http.MethodGet, raw))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if store.calls != 1 || store.lastPath != path {
		t.Fatalf("lookup calls=%d path=%v, want exact authorized path", store.calls, store.lastPath)
	}
}

// TestDownloadQueryGate400: any query presence, including a bare trailing
// question mark, URL.RawQuery or ForceQuery, is rejected before auth.
// 任何 query 迹象（含裸问号、RawQuery、ForceQuery）都在授权前 400。
func TestDownloadQueryGate400(t *testing.T) {
	uriCases := []struct {
		name   string
		rawURI string
	}{
		{"value query", "/artifactory/" + duRepoKey + "/a.bin?x=1"},
		{"bare trailing question mark", "/artifactory/" + duRepoKey + "/a.bin?"},
		{"empty query value", "/artifactory/" + duRepoKey + "/a.bin?x="},
		{"encoded question inside", "/artifactory/" + duRepoKey + "/a.bin%3Fx=1"},
	}
	for _, tc := range uriCases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
			store := &duStore{}
			h := duHandler(t, auth, store, &duOpener{}, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, duRawRequest(http.MethodGet, tc.rawURI))

			duAssertFixedError(t, rec, http.StatusBadRequest, false)
			if auth.calls != 0 || store.calls != 0 {
				t.Fatalf("deps touched: auth=%d store=%d", auth.calls, store.calls)
			}
		})
	}
	t.Run("parsed RawQuery without raw marker", func(t *testing.T) {
		auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
		h := duHandler(t, auth, &duStore{}, &duOpener{}, nil)
		r := duRawRequest(http.MethodGet, "/artifactory/"+duRepoKey+"/a.bin")
		r.URL.RawQuery = "x=1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		duAssertFixedError(t, rec, http.StatusBadRequest, false)
		if auth.calls != 0 {
			t.Fatalf("auth calls = %d, want 0", auth.calls)
		}
	})
	t.Run("ForceQuery without raw marker", func(t *testing.T) {
		auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
		h := duHandler(t, auth, &duStore{}, &duOpener{}, nil)
		r := duRawRequest(http.MethodGet, "/artifactory/"+duRepoKey+"/a.bin")
		r.URL.ForceQuery = true
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		duAssertFixedError(t, rec, http.StatusBadRequest, false)
		if auth.calls != 0 {
			t.Fatalf("auth calls = %d, want 0", auth.calls)
		}
	})
}

// TestDownloadConditionalHeaderGate501: any Range or if- header key in any
// spelling or value state returns 501 before auth and lookup, revealing no
// existence information.
// 任意拼写的 Range 或 if- 头一律 501，先于授权与查询，不暴露存在性。
func TestDownloadConditionalHeaderGate501(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"canonical Range", "Range", "bytes=0-1"},
		{"lowercase Range", "range", "bytes=0-1"},
		{"mixed-case Range", "rAnGe", "bytes=0-1"},
		{"empty Range", "Range", ""},
		{"If-None-Match", "If-None-Match", "\"x\""},
		{"If-Match", "If-Match", "*"},
		{"If-Modified-Since", "If-Modified-Since", "Mon, 02 Jan 2006 15:04:05 GMT"},
		{"If-Unmodified-Since", "If-Unmodified-Since", "Mon, 02 Jan 2006 15:04:05 GMT"},
		{"If-Range", "If-Range", "\"x\""},
		{"lowercase if-none-match", "if-none-match", "x"},
		{"uppercase IF-MATCH", "IF-MATCH", "y"},
		{"bare if- key", "if-", ""},
		{"bare If- canonical shape", "If-", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &duAuth{result: httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}}
			store := &duStore{}
			opener := &duOpener{}
			h := duHandler(t, auth, store, opener, nil)
			r := duRawRequest(http.MethodGet, "/artifactory/"+duRepoKey+"/a.bin")
			// Insert the key verbatim so canonicalization cannot hide the
			// required EqualFold/prefix-insensitive matching.
			// 原样写入键，避免规范化掩盖 EqualFold/前缀匹配要求。
			r.Header[tc.key] = append(r.Header[tc.key], tc.val)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			duAssertFixedError(t, rec, http.StatusNotImplemented, false)
			if auth.calls != 0 || store.calls != 0 || opener.opens != 0 {
				t.Fatalf("deps touched: auth=%d store=%d open=%d", auth.calls, store.calls, opener.opens)
			}
		})
	}
}

// TestDownloadAuthFailures: the trusted three-value decision plus error
// contract; every failure ends before any lookup and hides existence.
// 可信三值决定加错误契约；所有失败都在查询前结束且隐藏存在性。
func TestDownloadAuthFailures(t *testing.T) {
	cases := []struct {
		name           string
		result         httpserver.DownloadAuthResult
		err            error
		wantCode       int
		wantChallenge  string
		wantAuthReport bool
	}{
		{
			name:           "authorize error",
			err:            errors.New("du: auth backend down"),
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:           "zero decision",
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:           "unknown decision",
			result:         httpserver.DownloadAuthResult{Decision: httpserver.DownloadAuthDecision(9)},
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:          "unauthenticated with challenge",
			result:        httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated, Challenge: `Basic realm="du"`},
			wantCode:      http.StatusUnauthorized,
			wantChallenge: `Basic realm="du"`,
		},
		{
			name:           "unauthenticated empty challenge",
			result:         httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated},
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:           "unauthenticated control character challenge",
			result:         httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated, Challenge: "a\x01b"},
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:           "unauthenticated DEL challenge",
			result:         httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated, Challenge: "a\x7fb"},
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:           "unauthenticated non-ASCII challenge",
			result:         httpserver.DownloadAuthResult{Decision: httpserver.DownloadUnauthenticated, Challenge: "réalm"},
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:     "denied with empty challenge",
			result:   httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied},
			wantCode: http.StatusForbidden,
		},
		{
			name:           "denied with challenge",
			result:         httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied, Challenge: `Basic realm="du"`},
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
		{
			name:           "allow with challenge",
			result:         httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow, Challenge: `Basic realm="du"`},
			wantCode:       http.StatusInternalServerError,
			wantAuthReport: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				auth := &duAuth{result: tc.result, err: tc.err}
				store := &duStore{}
				opener := &duOpener{}
				rep := &duReporter{}
				h := duHandler(t, auth, store, opener, rep.report)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, duRawRequest(method, "/artifactory/"+duRepoKey+"/a.bin"))

				head := method == http.MethodHead
				duAssertFixedError(t, rec, tc.wantCode, head)
				if tc.wantChallenge == "" {
					if got := rec.Header().Get("WWW-Authenticate"); got != "" {
						t.Fatalf("%s WWW-Authenticate = %q, want empty", method, got)
					}
				} else if got := rec.Header().Get("WWW-Authenticate"); got != tc.wantChallenge {
					t.Fatalf("%s WWW-Authenticate = %q, want %q", method, got, tc.wantChallenge)
				}
				if auth.calls != 1 {
					t.Fatalf("%s auth calls = %d, want 1", method, auth.calls)
				}
				if !auth.lastCtxOK {
					t.Fatalf("%s authorize received nil ctx/request", method)
				}
				if want := duPath(t, duRepoKey, "", "a.bin"); auth.lastPath != want {
					t.Fatalf("%s authorize path = %v, want authorized path", method, auth.lastPath)
				}
				if store.calls != 0 || opener.opens != 0 {
					t.Fatalf("%s deps touched: store=%d open=%d", method, store.calls, opener.opens)
				}
				got := rep.categories()
				if tc.wantAuthReport {
					if len(got) != 1 || got[0] != httpserver.DownloadFailureAuth {
						t.Fatalf("%s report = %v, want [auth]", method, got)
					}
				} else if len(got) != 0 {
					t.Fatalf("%s report = %v, want none", method, got)
				}
			}
		})
	}
}

// TestDownloadLookupErrors: only sql.ErrNoRows (direct or wrapped) is 404;
// every other lookup failure, including not-a-file rows, is a fixed 500 and
// never reaches Open.
// 仅 sql.ErrNoRows（含包裹）为 404；其余查询失败（含非文件行）固定 500
// 且不 Open。
func TestDownloadLookupErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantCode   int
		wantReport bool
	}{
		{"sql.ErrNoRows", sql.ErrNoRows, http.StatusNotFound, false},
		{"wrapped sql.ErrNoRows", fmt.Errorf("du: wrap: %w", sql.ErrNoRows), http.StatusNotFound, false},
		{"joined sql.ErrNoRows", errors.Join(errors.New("du: hint"), sql.ErrNoRows), http.StatusNotFound, false},
		{"generic store failure", errors.New("du: store down"), http.StatusInternalServerError, true},
		{"not-a-file row sentinel", errors.New("postgres nodes: stored node is not a file"), http.StatusInternalServerError, true},
		{"context deadline exceeded", context.DeadlineExceeded, http.StatusInternalServerError, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				store := &duStore{err: tc.err}
				opener := &duOpener{}
				rep := &duReporter{}
				h := duHandler(t, duAllowAuth{}, store, opener, rep.report)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, duRawRequest(method, "/artifactory/"+duRepoKey+"/a.bin"))

				duAssertFixedError(t, rec, tc.wantCode, method == http.MethodHead)
				if store.calls != 1 {
					t.Fatalf("%s lookup calls = %d, want 1", method, store.calls)
				}
				if opener.opens != 0 {
					t.Fatalf("%s opens = %d, want 0", method, opener.opens)
				}
				got := rep.categories()
				if tc.wantReport {
					if len(got) != 1 || got[0] != httpserver.DownloadFailureLookup {
						t.Fatalf("%s report = %v, want [lookup]", method, got)
					}
				} else if len(got) != 0 {
					t.Fatalf("%s report = %v, want none", method, got)
				}
			}
		})
	}
}

// TestDownloadProjectionRejections: an invalid stored source projection is
// rejected with 500 before any content Open; wrong stored identity never
// opens. Both GET and HEAD share the rule.
// 非法存储投影在 Open 前以 500 拒绝；身份不符绝不 Open。GET/HEAD 同规则。
func TestDownloadProjectionRejections(t *testing.T) {
	validPath := duPath(t, duRepoKey, "", "a.bin")
	otherParent, otherName := duPath(t, duRepoKey, "elsewhere", "a.bin"), duPath(t, duRepoKey, "", "b.bin")
	folderSameKey := repository.Path{}
	if p, err := repository.NewPath(duRepoKey, "", "a.bin", false); err != nil {
		t.Fatalf("folder path construction error = %v", err)
	} else {
		folderSameKey = p
	}
	validMeta := duMeta(strings.Repeat("a", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), 3)
	cases := []struct {
		name   string
		source repository.FileSnapshotSource
	}{
		{"zero ID", duIDSource(duSource(validPath, 22, validMeta), 0)},
		{"negative ID", duIDSource(duSource(validPath, 22, validMeta), -1)},
		{"stored parent mismatch", duPathSource(duSource(validPath, 22, validMeta), otherParent)},
		{"stored name mismatch", duPathSource(duSource(validPath, 22, validMeta), otherName)},
		{"stored folder path same key", duPathSource(duSource(validPath, 22, validMeta), folderSameKey)},
		{"nil metadata", duMetaNilSource(duSource(validPath, 22, validMeta))},
		{"SHA1 Actual NULL", duFlagSource(duSource(validPath, 22, validMeta), false, true, true)},
		{"MD5 Actual NULL", duFlagSource(duSource(validPath, 22, validMeta), true, false, true)},
		{"SHA256 Actual NULL", duFlagSource(duSource(validPath, 22, validMeta), true, true, false)},
		{"uppercase SHA1", duContentSource(duSource(validPath, 22, validMeta), duMeta(strings.Repeat("A", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), 3))},
		{"short SHA1", duContentSource(duSource(validPath, 22, validMeta), duMeta(strings.Repeat("a", 39), strings.Repeat("b", 32), strings.Repeat("c", 64), 3))},
		{"long MD5", duContentSource(duSource(validPath, 22, validMeta), duMeta(strings.Repeat("a", 40), strings.Repeat("b", 33), strings.Repeat("c", 64), 3))},
		{"short SHA256", duContentSource(duSource(validPath, 22, validMeta), duMeta(strings.Repeat("a", 40), strings.Repeat("b", 32), strings.Repeat("c", 63), 3))},
		{"non-hex SHA1", duContentSource(duSource(validPath, 22, validMeta), duMeta(strings.Repeat("g", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), 3))},
		{"non-hex MD5", duContentSource(duSource(validPath, 22, validMeta), duMeta(strings.Repeat("a", 40), strings.Repeat("z", 32), strings.Repeat("c", 64), 3))},
		{"negative size", duContentSource(duSource(validPath, 22, validMeta), duMeta(strings.Repeat("a", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), -1))},
		{"empty SHA1 with nonzero size", duContentSource(duSource(validPath, 22, validMeta), duMeta(duEmptySHA1, strings.Repeat("b", 32), strings.Repeat("c", 64), 1))},
		{"negative ModifiedMillis", duSource(validPath, -1, validMeta)},
		{"ModifiedMillis above bound", duSource(validPath, 253402300800000, validMeta)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				store := &duStore{source: tc.source}
				opener := &duOpener{body: io.NopCloser(strings.NewReader("xyz"))}
				rep := &duReporter{}
				h := duHandler(t, duAllowAuth{}, store, opener, rep.report)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, duRawRequest(method, "/artifactory/"+duRepoKey+"/a.bin"))

				duAssertFixedError(t, rec, http.StatusInternalServerError, method == http.MethodHead)
				if store.calls != 1 {
					t.Fatalf("%s lookup calls = %d, want 1", method, store.calls)
				}
				if opener.opens != 0 {
					t.Fatalf("%s opens = %d, want 0", method, opener.opens)
				}
				got := rep.categories()
				if len(got) != 1 || got[0] != httpserver.DownloadFailureProjection {
					t.Fatalf("%s report = %v, want [projection]", method, got)
				}
			}
		})
	}
}

// duIDSource returns a copy with a replaced node ID.
func duIDSource(s repository.FileSnapshotSource, id int64) repository.FileSnapshotSource {
	s.Reference.ID = id
	return s
}

// duPathSource returns a copy with a replaced stored path identity.
func duPathSource(s repository.FileSnapshotSource, p repository.Path) repository.FileSnapshotSource {
	s.Reference.Path = p
	return s
}

// duMetaNilSource returns a copy carrying no file metadata snapshot.
func duMetaNilSource(s repository.FileSnapshotSource) repository.FileSnapshotSource {
	s.Reference.Metadata = nil
	return s
}

// duFlagSource returns a copy with replaced Actual nullability flags.
func duFlagSource(s repository.FileSnapshotSource, sha1, md5, sha256 bool) repository.FileSnapshotSource {
	s.SHA1ActualValid, s.MD5ActualValid, s.SHA256ActualValid = sha1, md5, sha256
	return s
}

// duContentSource returns a copy with replaced canonical content metadata.
func duContentSource(s repository.FileSnapshotSource, m content.Metadata) repository.FileSnapshotSource {
	s.Reference.Content = m
	return s
}

// TestDownloadSuccessRepresentations: authorized valid sources render the
// exact shared representation on GET: node size length, fixed suffix MIME,
// unquoted SHA-1 ETag, three checksum headers, both filename forms, floored
// UTC Last-Modified and no Accept-Ranges advertisement.
// 授权且投影合法时 GET 输出精确共享表示：节点长度、固定后缀 MIME、未加引号
// SHA-1 ETag、三个校验和头、双 filename 形式、按秒取整的 UTC Last-Modified，
// 且不广告 Accept-Ranges。
func TestDownloadSuccessRepresentations(t *testing.T) {
	cases := []struct {
		name      string
		parent    string
		fileName  string
		modMillis int64
		meta      content.Metadata
		body      string
		wantMIME  string
		wantOpens int
	}{
		{
			name:      "binary root file",
			parent:    "",
			fileName:  "payload.bin",
			modMillis: 1761700000123,
			meta:      duMeta(strings.Repeat("a", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), 7),
			body:      "payload",
			wantMIME:  "application/octet-stream",
			wantOpens: 1,
		},
		{
			name:      "text nested file",
			parent:    "docs/sub",
			fileName:  "notes.txt",
			modMillis: 253402300799999,
			meta:      duMeta(strings.Repeat("d", 40), strings.Repeat("e", 32), strings.Repeat("f", 64), 5),
			body:      "notes",
			wantMIME:  "text/plain",
			wantOpens: 1,
		},
		{
			name:      "empty representation",
			parent:    "",
			fileName:  "empty.bin",
			modMillis: 0,
			meta:      duMeta(duEmptySHA1, "d41d8cd98f00b204e9800998ecf8427e", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", 0),
			wantMIME:  "application/octet-stream",
			wantOpens: 0,
		},
		{
			name:      "dotted stem keeps whole name",
			parent:    "",
			fileName:  "archive.v2.txt",
			modMillis: 1234,
			meta:      duMeta(strings.Repeat("1", 40), strings.Repeat("2", 32), strings.Repeat("3", 64), 0),
			body:      "",
			wantMIME:  "text/plain",
			wantOpens: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := duPath(t, duRepoKey, tc.parent, tc.fileName)
			store := &duStore{source: duSource(path, tc.modMillis, tc.meta)}
			opener := &duOpener{body: &duBody{data: []byte(tc.body)}}
			rep := &duReporter{}
			h := duHandler(t, duAllowAuth{}, store, opener, rep.report)
			rec := httptest.NewRecorder()
			raw := "/artifactory/" + duRepoKey
			if tc.parent != "" {
				raw += "/" + tc.parent
			}
			raw += "/" + tc.fileName
			h.ServeHTTP(rec, duRawRequest(http.MethodGet, raw))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if rec.Body.String() != tc.body {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.body)
			}
			duAssertRepresentationHeaders(t, rec, tc.fileName, tc.meta, tc.modMillis, tc.wantMIME)
			if store.calls != 1 || store.lastPath != path {
				t.Fatalf("lookup calls=%d path=%v, want one exact authorized lookup", store.calls, store.lastPath)
			}
			if opener.opens != tc.wantOpens {
				t.Fatalf("opens = %d, want %d", opener.opens, tc.wantOpens)
			}
			if tc.wantOpens == 1 && opener.lastKey != tc.meta.SHA1 {
				t.Fatalf("open key = %q, want actual SHA1 %q", opener.lastKey, tc.meta.SHA1)
			}
			if got := rep.categories(); len(got) != 0 {
				t.Fatalf("report = %v, want none", got)
			}
		})
	}
}

// duAssertRepresentationHeaders pins the shared success header set.
// duAssertRepresentationHeaders 固定共享成功头集合。
func duAssertRepresentationHeaders(t *testing.T, rec *httptest.ResponseRecorder, name string, m content.Metadata, modMillis int64, mime string) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != mime {
		t.Fatalf("Content-Type = %q, want %q", ct, mime)
	}
	if et := rec.Header().Get("ETag"); et != m.SHA1 {
		t.Fatalf("ETag = %q, want unquoted %q", et, m.SHA1)
	}
	if v := rec.Header().Get("X-Checksum-Sha1"); v != m.SHA1 {
		t.Fatalf("X-Checksum-Sha1 = %q, want %q", v, m.SHA1)
	}
	if v := rec.Header().Get("X-Checksum-Md5"); v != m.MD5 {
		t.Fatalf("X-Checksum-Md5 = %q, want %q", v, m.MD5)
	}
	if v := rec.Header().Get("X-Checksum-Sha256"); v != m.SHA256 {
		t.Fatalf("X-Checksum-Sha256 = %q, want %q", v, m.SHA256)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.FormatInt(m.Size, 10) {
		t.Fatalf("Content-Length = %q, want %d", cl, m.Size)
	}
	wantDisp := fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", name, name)
	if cd := rec.Header().Get("Content-Disposition"); cd != wantDisp {
		t.Fatalf("Content-Disposition = %q, want %q", cd, wantDisp)
	}
	wantLM := time.UnixMilli(modMillis / 1000 * 1000).UTC().Format(http.TimeFormat)
	if lm := rec.Header().Get("Last-Modified"); lm != wantLM {
		t.Fatalf("Last-Modified = %q, want %q", lm, wantLM)
	}
	if ar := rec.Header().Get("Accept-Ranges"); ar != "" {
		t.Fatalf("Accept-Ranges = %q, want absent", ar)
	}
}

// TestDownloadHeadParityZeroBodyZeroOpen: HEAD reuses the same single lookup
// and projection, returns GET's representation headers, writes no body and
// never opens content — even when the opener would fail.
// HEAD 复用同一次查询与投影：与 GET 表示头一致、零正文、绝不 Open——即使
// Open 必然失败。
func TestDownloadHeadParityZeroBodyZeroOpen(t *testing.T) {
	const name = "payload.bin"
	path := duPath(t, duRepoKey, "", name)
	meta := duMeta(strings.Repeat("a", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), 7)
	build := func(opener *duOpener) (http.Handler, *duStore) {
		store := &duStore{source: duSource(path, 1761700000123, meta)}
		return duHandler(t, duAllowAuth{}, store, opener, nil), store
	}

	getRec := httptest.NewRecorder()
	getStore := &duStore{source: duSource(path, 1761700000123, meta)}
	getOpener := &duOpener{body: &duBody{data: []byte("payload")}}
	hGet := duHandler(t, duAllowAuth{}, getStore, getOpener, nil)
	hGet.ServeHTTP(getRec, duRawRequest(http.MethodGet, "/artifactory/"+duRepoKey+"/"+name))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getRec.Code)
	}

	headOpener := &duOpener{err: errors.New("du: object absent")}
	hHead, headStore := build(headOpener)
	headRec := httptest.NewRecorder()
	hHead.ServeHTTP(headRec, duRawRequest(http.MethodHead, "/artifactory/"+duRepoKey+"/"+name))

	if headRec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", headRec.Code)
	}
	if headRec.Body.Len() != 0 {
		t.Fatalf("HEAD body = %q, want empty", headRec.Body.String())
	}
	if headStore.calls != 1 || headOpener.opens != 0 {
		t.Fatalf("HEAD lookup=%d opens=%d, want 1/0", headStore.calls, headOpener.opens)
	}
	for _, h := range []string{
		"Content-Type", "ETag", "X-Checksum-Sha1", "X-Checksum-Md5",
		"X-Checksum-Sha256", "Content-Length", "Content-Disposition", "Last-Modified",
	} {
		if g, w := headRec.Header().Get(h), getRec.Header().Get(h); g != w {
			t.Fatalf("HEAD %s = %q, want GET's %q", h, g, w)
		}
	}
	if ar := headRec.Header().Get("Accept-Ranges"); ar != "" {
		t.Fatalf("HEAD Accept-Ranges = %q, want absent", ar)
	}
}

// TestDownloadOpenFailureGET: a valid projection whose content open fails
// yields the fixed 500 without partial representation headers; HEAD over
// the same store still succeeds because it never opens.
// 投影合法但 Open 失败：GET 固定 500 且无部分表示头；同源 HEAD 因不 Open
// 仍为 200。
func TestDownloadOpenFailureGET(t *testing.T) {
	const name = "payload.bin"
	path := duPath(t, duRepoKey, "", name)
	meta := duMeta(strings.Repeat("a", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), 7)
	source := duSource(path, 1761700000123, meta)
	opener := &duOpener{err: errors.New("du: object absent")}
	rep := &duReporter{}
	h := duHandler(t, duAllowAuth{}, &duStore{source: source}, opener, rep.report)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, duRawRequest(http.MethodGet, "/artifactory/"+duRepoKey+"/"+name))

	duAssertFixedError(t, rec, http.StatusInternalServerError, false)
	if opener.opens != 1 || opener.lastKey != meta.SHA1 {
		t.Fatalf("opens=%d key=%q, want 1 with actual SHA1", opener.opens, opener.lastKey)
	}
	got := rep.categories()
	if len(got) != 1 || got[0] != httpserver.DownloadFailureOpen {
		t.Fatalf("report = %v, want [open]", got)
	}
}

// TestDownloadNilReportStillFunctions: an omitted report callback must not
// break request handling; the fixed default logging path stays internal.
// 省略 Report 回调不影响请求处理；默认日志路径保持内部细节。
func TestDownloadNilReportStillFunctions(t *testing.T) {
	store := &duStore{err: errors.New("du: store down")}
	h := duHandler(t, duAllowAuth{}, store, &duOpener{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, duRawRequest(http.MethodGet, "/artifactory/"+duRepoKey+"/a.bin"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// TestNewHandlerMountsNoDownloadRoute: the production router still knows
// nothing about download URLs; the adapter stays unmounted.
// 生产路由仍不认识下载 URL；适配器保持未挂接。
func TestNewHandlerMountsNoDownloadRoute(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		httpserver.NewHandler().ServeHTTP(rec, duRawRequest(method, "/artifactory/"+duRepoKey+"/a.bin"))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", method, rec.Code, http.StatusNotFound)
		}
	}
}

// Review regressions distinguish node absence from opener failure and retain
// a nonempty stem when only the final filename suffix is interpreted.
// 审查回归：打开失败不是节点缺席；只解释最后一个后缀。
func TestDownloadReviewRegressions(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		openErr    error
		status     int
	}{
		{"opener no rows is still failure", "a.bin", sql.ErrNoRows, 500},
		{"nonempty multi suffix stem", ".txt.bin", nil, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := duPath(t, duRepoKey, "dir", tc.file)
			store := &duStore{source: duSource(p, 0, duMeta(strings.Repeat("a", 40), strings.Repeat("b", 32), strings.Repeat("c", 64), 3))}
			opener := &duOpener{body: &duBody{data: []byte("abc")}, err: tc.openErr}
			report := &duReporter{}
			h := duHandler(t, duAllowAuth{}, store, opener, report.report)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, duRawRequest("GET", "/artifactory/"+duRepoKey+"/dir/"+tc.file))
			if rec.Code != tc.status {
				t.Fatalf("status=%d, want%d", rec.Code, tc.status)
			}
			if tc.status == 200 {
				if rec.Body.String() != "abc" || opener.opens != 1 {
					t.Fatalf("GET body=%q opens=%d", rec.Body.String(), opener.opens)
				}
			} else {
				duAssertFixedError(t, rec, 500, false)
				got := report.categories()
				if len(got) != 1 || got[0] != httpserver.DownloadFailureOpen {
					t.Fatalf("open classification=%v", got)
				}
			}
		})
	}
}

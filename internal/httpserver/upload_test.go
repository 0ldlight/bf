// This file is the UNIT behavioral contract for the ordinary upload (PUT)
// adapter. It consumes only the frozen exported API of NewUploadHandler and
// drives the handler through recorder requests whose raw RequestURI is the
// sole identity input. Real saver/database/transport integration belongs to
// the separate integration suites.
// 本文件是普通上传(PUT)适配器的 UNIT 行为契约：只依赖冻结导出 API，通过
// 记录器驱动处理器，raw RequestURI 是唯一身份输入。真实 saver/数据库/传输
// 集成属于独立的集成套件。
package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// upRepoKey is the single configured repository key used across the suite.
const upRepoKey = "up.repo-1"

// upOrigin is the trusted configured public origin; the request Host is never
// reflected into responses.
const upOrigin = "https://up.example"

// upMediaType is the fixed ItemCreated media type with UTF-8 charset.
const upMediaType = "application/vnd.org.jfrog.artifactory.storage.ItemCreated+json;charset=UTF-8"

// upSHA1/upMD5/upSHA256 are distinct canonical lowercase fixture digests.
const (
	upSHA1   = "abcdef0123456789abcdef0123456789abcdef01"
	upMD5    = "abcdef0123456789abcdef0123456789"
	upSHA256 = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

// upUnits decodes an ASCII fixture string into UTF-16 units.
func upUnits(s string) []uint16 {
	units := make([]uint16, len(s))
	for i := 0; i < len(s); i++ {
		units[i] = uint16(s[i])
	}
	return units
}

// upAuth records the authorize call and replays a canned decision or error.
type upAuth struct {
	result    httpserver.UploadAuthResult
	err       error
	calls     int
	lastPath  repository.Path
	cancelCtx func()
}

func (a *upAuth) Authorize(ctx context.Context, _ *http.Request, p repository.Path) (httpserver.UploadAuthResult, error) {
	a.calls++
	a.lastPath = p
	if a.cancelCtx != nil {
		a.cancelCtx()
	}
	return a.result, a.err
}

// upSaver records the single Save call; consume optionally drains the wrapped
// body reader and its returned error becomes the save error.
type upSaver struct {
	source     repository.FileSnapshotSource
	err        error
	consume    func(io.Reader) error
	calls      int
	lastInput  localrepo.UploadInput
	lastCtx    context.Context
	beforeSave func()
}

func (s *upSaver) Save(ctx context.Context, body io.Reader, in localrepo.UploadInput) (repository.FileSnapshotSource, error) {
	s.calls++
	s.lastInput = in
	s.lastCtx = ctx
	if s.beforeSave != nil {
		s.beforeSave()
	}
	if s.consume != nil {
		if err := s.consume(body); err != nil {
			return repository.FileSnapshotSource{}, err
		}
	}
	return s.source, s.err
}

// upBody records Close without failing; net/http owns the real body.
type upBody struct {
	r      io.Reader
	closed bool
}

func (b *upBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *upBody) Close() error               { b.closed = true; return nil }

// upErrReader always fails with the canned non-EOF error.
type upErrReader struct{ err error }

func (r upErrReader) Read([]byte) (int, error) { return 0, r.err }

// upSliceErr is a non-comparable error value (struct with a slice field).
type upSliceErr struct{ parts []string }

func (e upSliceErr) Error() string { return "slice error" }

// upCycle builds a two-node cyclic Unwrap chain.
type upCycle struct{ next error }

func (c *upCycle) Error() string { return "cycle" }
func (c *upCycle) Unwrap() error { return c.next }

// upMultiErr exposes a custom multi-leaf Unwrap list.
type upMultiErr struct{ children []error }

// 原始分支宽度（包含 nil）也占预算，不能先无界过滤再判客户端错误。
// Raw child width, including nil entries, consumes the error-walk budget.
func TestUploadWideErrorTreeRejectsBeforeFiltering(t *testing.T) {
	children := make([]error, 65)
	children[len(children)-1] = filestore.ErrChecksumMismatch
	saver := &upSaver{err: &upMultiErr{children: children}}
	rep := &upReport{}
	rec, aborted := upServe(t, upDeps(upAllowAuth(), saver, 8), rep, upPUT(nil, 3))
	if aborted || rec.Code != http.StatusInternalServerError || !rep.has("save") {
		t.Fatalf("wide tree: status=%d abort=%v reports=%v", rec.Code, aborted, rep.cats)
	}
}

func (e *upMultiErr) Error() string   { return "multi" }
func (e *upMultiErr) Unwrap() []error { return e.children }

// upReport collects reported failure categories.
type upReport struct {
	mu   sync.Mutex
	cats []string
}

func (r *upReport) report(f httpserver.UploadFailure) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cats = append(r.cats, string(f))
}

func (r *upReport) has(cat string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cats {
		if c == cat {
			return true
		}
	}
	return false
}

// upSource builds a valid snapshot source for the given input path, with
// optional SQL SHA1/MD5 originals (nil means absent).
func upSource(in localrepo.UploadInput, sha1Orig, md5Orig []uint16) repository.FileSnapshotSource {
	meta := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  1714500000123,
		ModifiedMillis: 1714500000456,
		UpdatedMillis:  1714500000789,
		CreatedBy:      repository.NewActor(upUnits("admin")),
		ModifiedBy:     repository.NewActor(upUnits("admin")),
		SHA1Original:   sha1Orig,
		MD5Original:    md5Orig,
	})
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID:   77,
			Path: in.Path,
			Content: content.Metadata{
				SHA1:   upSHA1,
				SHA256: upSHA256,
				MD5:    upMD5,
				Size:   3,
			},
			Metadata: &meta,
		},
		SHA1ActualValid:   true,
		MD5ActualValid:    true,
		SHA256ActualValid: true,
	}
}

// upPathSource builds a valid source whose Path identity matches rel.
func upPathSource(t *testing.T, rel string, sha1Orig, md5Orig []uint16) repository.FileSnapshotSource {
	t.Helper()
	path, err := localrepo.NewUploadPath(upRepoKey, rel)
	if err != nil {
		t.Fatalf("NewUploadPath: %v", err)
	}
	return upSource(localrepo.UploadInput{Path: path}, sha1Orig, md5Orig)
}

// upDeps assembles handler dependencies with recording doubles.
func upDeps(auth *upAuth, saver *upSaver, maxBody int64) httpserver.UploadDeps {
	return httpserver.UploadDeps{
		RepoKey:      upRepoKey,
		MaxBodyBytes: maxBody,
		PublicOrigin: upOrigin,
		Auth:         auth,
		Saver:        saver,
	}
}

// upAllowAuth is a reusable always-allow auth with a valid operator.
func upAllowAuth() *upAuth {
	return &upAuth{result: httpserver.UploadAuthResult{
		Decision:      httpserver.UploadAllow,
		OperatorUTF16: upUnits("alice"),
	}}
}

// upReq crafts a direct request with the raw URI as sole identity input.
func upReq(method, uri string, body io.ReadCloser, cl int64) *http.Request {
	r := &http.Request{
		Method:        method,
		RequestURI:    uri,
		Header:        http.Header{},
		Host:          "evil.example",
		Body:          body,
		ContentLength: cl,
	}
	if parsed, err := url.Parse(uri); err == nil {
		r.URL = parsed
	} else {
		r.URL = &url.URL{}
	}
	return r
}

// upPUT builds the default PUT request over the standard fixture path.
func upPUT(body io.ReadCloser, cl int64) *http.Request {
	if body == nil {
		body = io.NopCloser(strings.NewReader("abc"))
	}
	if cl < 0 {
		cl = 3
	}
	return upReq(http.MethodPut, "/artifactory/"+upRepoKey+"/a/f.bin", body, cl)
}

// upServe runs one request through a fresh handler and returns the recorder.
// A recovered http.ErrAbortHandler leaves wantAbort true without failing.
func upServe(t *testing.T, deps httpserver.UploadDeps, rep *upReport, r *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	if rep == nil {
		rep = &upReport{}
	}
	deps.Report = rep.report
	handler, err := httpserver.NewUploadHandler(deps)
	if err != nil {
		t.Fatalf("NewUploadHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	aborted := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered != http.ErrAbortHandler {
					t.Fatalf("unexpected panic: %v", recovered)
				}
				aborted = true
			}
		}()
		handler.ServeHTTP(rec, r)
	}()
	return rec, aborted
}

func TestNewUploadHandlerRejectsInvalidConfiguration(t *testing.T) {
	valid := upDeps(upAllowAuth(), &upSaver{}, 1<<20)
	cases := []struct {
		name string
		mut  func(d *httpserver.UploadDeps)
	}{
		{"nil auth", func(d *httpserver.UploadDeps) { d.Auth = nil }},
		{"nil saver", func(d *httpserver.UploadDeps) { d.Saver = nil }},
		{"empty repo", func(d *httpserver.UploadDeps) { d.RepoKey = "" }},
		{"dot repo", func(d *httpserver.UploadDeps) { d.RepoKey = "." }},
		{"slash repo", func(d *httpserver.UploadDeps) { d.RepoKey = "a/b" }},
		{"zero body", func(d *httpserver.UploadDeps) { d.MaxBodyBytes = 0 }},
		{"negative body", func(d *httpserver.UploadDeps) { d.MaxBodyBytes = -1 }},
		{"over 1GiB body", func(d *httpserver.UploadDeps) { d.MaxBodyBytes = 1<<30 + 1 }},
		{"empty origin", func(d *httpserver.UploadDeps) { d.PublicOrigin = "" }},
		{"ftp origin", func(d *httpserver.UploadDeps) { d.PublicOrigin = "ftp://h" }},
		{"missing host", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://" }},
		{"userinfo", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://u:p@h" }},
		{"query", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://h?q=1" }},
		{"bare query", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://h?" }},
		{"fragment", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://h#f" }},
		{"opaque", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http:opaque" }},
		{"raw path escape", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://h/%41" }},
		{"deep path", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://h/x" }},
		{"control char", func(d *httpserver.UploadDeps) { d.PublicOrigin = "http://h/\x01" }},
		{"non ascii", func(d *httpserver.UploadDeps) { d.PublicOrigin = "https://h/\xc3\xbc" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := valid
			tc.mut(&deps)
			handler, err := httpserver.NewUploadHandler(deps)
			if handler != nil || !errors.Is(err, httpserver.ErrUploadConfiguration) {
				t.Fatalf("want ErrUploadConfiguration and nil handler, got %v / %v", handler, err)
			}
		})
	}
}

func TestNewUploadHandlerAcceptsValidConfiguration(t *testing.T) {
	for _, origin := range []string{"http://h", "https://h/", "http://h:8080"} {
		deps := upDeps(upAllowAuth(), &upSaver{}, 8)
		deps.PublicOrigin = origin
		if _, err := httpserver.NewUploadHandler(deps); err != nil {
			t.Fatalf("origin %q: %v", origin, err)
		}
	}
	// A nil Report must be accepted; the fixed category logger is internal.
	if _, err := httpserver.NewUploadHandler(upDeps(upAllowAuth(), &upSaver{}, 8)); err != nil {
		t.Fatal(err)
	}
}

func TestUploadMethodGate405(t *testing.T) {
	auth, saver := upAllowAuth(), &upSaver{}
	for _, tc := range []struct {
		method string
		body   string
	}{
		{http.MethodGet, "method not allowed\n"},
		{http.MethodPost, "method not allowed\n"},
		{http.MethodHead, ""},
	} {
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, upReq(tc.method, "/artifactory/"+upRepoKey+"/a/f.bin", nil, 0))
		if aborted {
			t.Fatal("unexpected abort")
		}
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status %d", tc.method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "PUT" {
			t.Fatalf("Allow = %q", got)
		}
		if rec.Body.String() != tc.body {
			t.Fatalf("body = %q", rec.Body.String())
		}
		if rec.Header().Get("Content-Length") != "19" {
			t.Fatalf("Content-Length = %q", rec.Header().Get("Content-Length"))
		}
	}
	if auth.calls != 0 || saver.calls != 0 {
		t.Fatalf("auth=%d saver=%d calls", auth.calls, saver.calls)
	}
}

func TestUploadRawURIGate400(t *testing.T) {
	for _, uri := range []string{
		"",
		"/artifactory/" + upRepoKey,
		"/artifactory/" + upRepoKey + "/",
		"/artifactory/" + upRepoKey + "//f.bin",
		"/artifactory/" + upRepoKey + "/a//f.bin",
		"/artifactory/" + upRepoKey + "/a/./f.bin",
		"/artifactory/" + upRepoKey + "/a/../f.bin",
		"/artifactory/" + upRepoKey + "/a/f.bin/",
		"/artifactory/" + upRepoKey + "/a/f.bin?x=1",
		"/artifactory/" + upRepoKey + "/a/f.bin?",
		"/artifactory/" + upRepoKey + "/a/%66.bin",
		"/artifactory/" + upRepoKey + "/a/f.BIN",
		"/artifactory/" + upRepoKey + "/a/f.zip",
		"/artifactory/" + upRepoKey + "/a/.bin",
		"/artifactory/" + upRepoKey + "/a/f\x01.bin",
		"/artifactory/" + upRepoKey + "/a/f+.bin",
		"/artifactory/" + upRepoKey + "/a/f:.bin",
		"/artifactory/" + upRepoKey + "/a/ /ü/f.bin",
		"/other.repo/a/f.bin",
		"artifactory/" + upRepoKey + "/a/f.bin",
		"http://h/artifactory/" + upRepoKey + "/a/f.bin",
		strings.Repeat("/artifactory/"+upRepoKey+"/a/", 200) + "f.bin",
	} {
		auth, saver := upAllowAuth(), &upSaver{}
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, upReq(http.MethodPut, uri, nil, 0))
		if aborted {
			t.Fatalf("uri %q: unexpected abort", uri)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("uri %q: status %d", uri, rec.Code)
		}
		if rec.Body.String() != "bad request\n" {
			t.Fatalf("uri %q: body %q", uri, rec.Body.String())
		}
		if auth.calls != 0 || saver.calls != 0 {
			t.Fatalf("uri %q: auth=%d saver=%d", uri, auth.calls, saver.calls)
		}
	}
}

func TestUploadURLQueryFlagGate400(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  *url.URL
	}{
		{"raw query", &url.URL{Path: "/artifactory/" + upRepoKey + "/a/f.bin", RawQuery: "x=1"}},
		{"force query", &url.URL{Path: "/artifactory/" + upRepoKey + "/a/f.bin", ForceQuery: true}},
	} {
		auth, saver := upAllowAuth(), &upSaver{}
		r := upPUT(nil, 0)
		r.URL = tc.url
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != http.StatusBadRequest || saver.calls != 0 || auth.calls != 0 {
			t.Fatalf("%s: status %d auth=%d saver=%d", tc.name, rec.Code, auth.calls, saver.calls)
		}
	}
}

func TestUploadUnsupportedFraming501(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(r *http.Request)
	}{
		{"transfer encoding", func(r *http.Request) { r.Header.Set("Transfer-Encoding", "chunked") }},
		{"transfer encoding alias", func(r *http.Request) { r.Header["transfer-encoding"] = []string{"chunked"} }},
		{"te flag only", func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
		{"content encoding", func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{"content encoding empty", func(r *http.Request) { r.Header.Set("Content-Encoding", "") }},
		{"expect", func(r *http.Request) { r.Header.Set("Expect", "100-continue") }},
		{"expect false", func(r *http.Request) { r.Header.Set("Expect", "false") }},
		{"range", func(r *http.Request) { r.Header.Set("Range", "bytes=0-1") }},
		{"if match", func(r *http.Request) { r.Header.Set("If-Match", `"x"`) }},
		{"if none match alias", func(r *http.Request) { r.Header["if-none-match"] = []string{`"x"`} }},
		{"x checksum generic", func(r *http.Request) { r.Header.Set("X-Checksum", upSHA1) }},
		{"x checksum empty", func(r *http.Request) { r.Header.Set("X-Checksum", "") }},
		{"x checksum deploy", func(r *http.Request) { r.Header.Set("X-Checksum-Deploy", "true") }},
		{"x checksum deploy false", func(r *http.Request) { r.Header.Set("X-Checksum-Deploy", "false") }},
		{"x binary size", func(r *http.Request) { r.Header.Set("X-Binary-Size", "3") }},
		{"x check existence", func(r *http.Request) { r.Header.Set("X-Check-Binary-Existence-In-Filestore", "true") }},
		{"x explode archive", func(r *http.Request) { r.Header.Set("X-Explode-Archive-Atomic", "true") }},
		{"x artifactory anything", func(r *http.Request) { r.Header.Set("X-Artifactory-Anything", "1") }},
		{"last modified", func(r *http.Request) { r.Header.Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT") }},
		{"content range", func(r *http.Request) { r.Header.Set("Content-Range", "bytes 0-1/2") }},
		{"legacy artifactory ua", func(r *http.Request) { r.Header.Set("User-Agent", "ArtifactoryBuildClient/2.x") }},
		{"legacy artifactory ua mid", func(r *http.Request) { r.Header.Set("User-Agent", "proxy ArtifactoryBuildClient/3 y") }},
	} {
		auth, saver := upAllowAuth(), &upSaver{}
		r := upPUT(nil, 3)
		tc.set(r)
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != "not implemented\n" {
			t.Fatalf("%s: body %q", tc.name, rec.Body.String())
		}
		if auth.calls != 0 || saver.calls != 0 {
			t.Fatalf("%s: auth=%d saver=%d", tc.name, auth.calls, saver.calls)
		}
	}
}

func TestUploadContentLengthGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mut    func(r *http.Request)
		status int
		body   string
	}{
		{"unknown length", func(r *http.Request) { r.ContentLength = -1 }, http.StatusLengthRequired, "length required\n"},
		{"over max", func(r *http.Request) { r.ContentLength = 9 }, http.StatusRequestEntityTooLarge, "payload too large\n"},
		{"raw disagrees", func(r *http.Request) { r.Header.Set("Content-Length", "2") }, http.StatusBadRequest, "bad request\n"},
		{"raw non decimal", func(r *http.Request) { r.Header.Set("Content-Length", "3x") }, http.StatusBadRequest, "bad request\n"},
		{"raw empty", func(r *http.Request) { r.Header.Set("Content-Length", "") }, http.StatusBadRequest, "bad request\n"},
		{"raw plus", func(r *http.Request) { r.Header.Set("Content-Length", "+3") }, http.StatusBadRequest, "bad request\n"},
		{"raw multi value", func(r *http.Request) {
			r.Header["Content-Length"] = []string{"3", "3"}
		}, http.StatusBadRequest, "bad request\n"},
		{"raw alias multi", func(r *http.Request) {
			r.Header.Set("Content-Length", "3")
			r.Header["content-length"] = []string{"3"}
		}, http.StatusBadRequest, "bad request\n"},
	} {
		auth, saver := upAllowAuth(), &upSaver{}
		r := upPUT(nil, 3)
		tc.mut(r)
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != tc.status || rec.Body.String() != tc.body {
			t.Fatalf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		if saver.calls != 0 {
			t.Fatalf("%s: saver called", tc.name)
		}
	}
	// An agreeing raw header and the exact max boundary stay allowed.
	for _, tc := range []struct {
		name string
		mut  func(r *http.Request)
	}{
		{"agreeing raw", func(r *http.Request) { r.Header.Set("Content-Length", "3") }},
		{"exact max", func(r *http.Request) { r.ContentLength = 8; r.Body = io.NopCloser(strings.NewReader("12345678")) }},
	} {
		auth, saver := upAllowAuth(), &upSaver{}
		saver.source = upPathSource(t, "a/f.bin", nil, nil)
		r := upPUT(nil, 3)
		tc.mut(r)
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: expected later stage, got %d", tc.name, rec.Code)
		}
		if saver.calls != 1 {
			t.Fatalf("%s: saver=%d", tc.name, saver.calls)
		}
	}
}

func TestUploadChecksumHeaderParsing(t *testing.T) {
	sha1Only := func(r *http.Request) { r.Header.Set("X-Checksum-Sha1", upSHA1) }
	alias := func(r *http.Request) { r.Header["x-checksum-sha256"] = []string{upSHA256} }
	for _, tc := range []struct {
		name string
		set  func(r *http.Request)
	}{
		{"sha1 short", func(r *http.Request) { r.Header.Set("X-Checksum-Sha1", upSHA1[:39]) }},
		{"sha1 uppercase", func(r *http.Request) { r.Header.Set("X-Checksum-Sha1", strings.ToUpper(upSHA1)) }},
		{"sha1 empty", func(r *http.Request) { r.Header.Set("X-Checksum-Sha1", "") }},
		{"sha1 marker", func(r *http.Request) { r.Header.Set("X-Checksum-Sha1", "NO_ORIG") }},
		{"md5 bad length", func(r *http.Request) { r.Header.Set("X-Checksum-Md5", upMD5+"f") }},
		{"sha256 non hex", func(r *http.Request) { r.Header.Set("X-Checksum-Sha256", strings.Repeat("z", 64)) }},
		{"multi value", func(r *http.Request) {
			r.Header["X-Checksum-Sha1"] = []string{upSHA1, upSHA1}
		}},
		{"alias aggregate", func(r *http.Request) {
			r.Header.Set("X-Checksum-Sha1", upSHA1)
			r.Header["X-CHECKSUM-SHA1"] = []string{upSHA1}
		}},
	} {
		auth, saver := upAllowAuth(), &upSaver{}
		r := upPUT(nil, 3)
		tc.set(r)
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != http.StatusBadRequest || saver.calls != 0 || auth.calls != 0 {
			t.Fatalf("%s: status %d auth=%d saver=%d", tc.name, rec.Code, auth.calls, saver.calls)
		}
	}
	// Valid dedicated headers decode into UTF-16 originals for the saver.
	for _, tc := range []struct {
		name string
		set  func(r *http.Request)
		want repository.ClientFileChecksums
	}{
		{"sha1", sha1Only, repository.ClientFileChecksums{SHA1Original: upUnits(upSHA1)}},
		{"sha256 alias", alias, repository.ClientFileChecksums{SHA256Original: upUnits(upSHA256)}},
	} {
		auth, saver := upAllowAuth(), &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
		r := upPUT(nil, 3)
		tc.set(r)
		rep := &upReport{}
		rec, _ := upServe(t, upDeps(auth, saver, 8), rep, r)
		if rec.Code != http.StatusCreated || saver.calls != 1 {
			t.Fatalf("%s: status %d saver=%d", tc.name, rec.Code, saver.calls)
		}
		if got := saver.lastInput.Checksums; !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: checksums %+v", tc.name, got)
		}
	}
}

func TestUploadAuthFlow(t *testing.T) {
	operator := upUnits("alice")
	spaces := upUnits("   ")
	long := upUnits(strings.Repeat("a", 65))
	for _, tc := range []struct {
		name   string
		result httpserver.UploadAuthResult
		err    error
		status int
		body   string
		report string
		saverN int
	}{
		{"allow", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: operator}, nil, http.StatusCreated, "", "", 1},
		{"allow with challenge", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, Challenge: `Basic x`, OperatorUTF16: operator}, nil, 500, "internal error\n", "auth", 0},
		{"allow no operator", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow}, nil, 500, "internal error\n", "auth", 0},
		{"allow empty operator", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: []uint16{}}, nil, 500, "internal error\n", "auth", 0},
		{"allow all spaces", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: spaces}, nil, 500, "internal error\n", "auth", 0},
		{"allow long operator", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: long}, nil, 500, "internal error\n", "auth", 0},
		{"allow non ascii operator", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: []uint16{0x0101}}, nil, 500, "internal error\n", "auth", 0},
		{"unauthenticated", httpserver.UploadAuthResult{Decision: httpserver.UploadUnauthenticated, Challenge: `Basic realm="x"`}, nil, 401, "unauthorized\n", "", 0},
		{"unauthenticated bad challenge", httpserver.UploadAuthResult{Decision: httpserver.UploadUnauthenticated, Challenge: ""}, nil, 500, "internal error\n", "auth", 0},
		{"unauthenticated control challenge", httpserver.UploadAuthResult{Decision: httpserver.UploadUnauthenticated, Challenge: "x\x01"}, nil, 500, "internal error\n", "auth", 0},
		{"unauthenticated with operator", httpserver.UploadAuthResult{Decision: httpserver.UploadUnauthenticated, Challenge: `Basic x`, OperatorUTF16: operator}, nil, 500, "internal error\n", "auth", 0},
		{"denied", httpserver.UploadAuthResult{Decision: httpserver.UploadDenied}, nil, 403, "forbidden\n", "", 0},
		{"denied with challenge", httpserver.UploadAuthResult{Decision: httpserver.UploadDenied, Challenge: "x"}, nil, 500, "internal error\n", "auth", 0},
		{"denied with operator", httpserver.UploadAuthResult{Decision: httpserver.UploadDenied, OperatorUTF16: operator}, nil, 500, "internal error\n", "auth", 0},
		{"zero decision", httpserver.UploadAuthResult{OperatorUTF16: operator}, nil, 500, "internal error\n", "auth", 0},
		{"auth error", httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: operator}, errors.New("down"), 500, "internal error\n", "auth", 0},
	} {
		auth := &upAuth{result: tc.result, err: tc.err}
		saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
		rep := &upReport{}
		r := upPUT(nil, 3)
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != tc.status {
			t.Fatalf("%s: status %d", tc.name, rec.Code)
		}
		if tc.body != "" && rec.Body.String() != tc.body {
			t.Fatalf("%s: body %q", tc.name, rec.Body.String())
		}
		if tc.status == 401 && rec.Header().Get("WWW-Authenticate") != tc.result.Challenge {
			t.Fatalf("%s: challenge header %q", tc.name, rec.Header().Get("WWW-Authenticate"))
		}
		if tc.report != "" && !rep.has(tc.report) {
			t.Fatalf("%s: missing report %q in %v", tc.name, tc.report, rep.cats)
		}
		if tc.status != 401 && tc.status != 403 && rec.Header().Get("WWW-Authenticate") != "" {
			t.Fatalf("%s: unexpected challenge", tc.name)
		}
		if auth.calls != 1 {
			t.Fatalf("%s: auth calls %d", tc.name, auth.calls)
		}
		if saver.calls != tc.saverN {
			t.Fatalf("%s: saver calls %d", tc.name, saver.calls)
		}
	}
	// The allowed operator is defensively copied: mutating the auth slice
	// inside Save cannot change what the handler already captured.
	auth := upAllowAuth()
	saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
	saver.beforeSave = func() { auth.result.OperatorUTF16[0] = 'X' }
	rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, upPUT(nil, 3))
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	if got := saver.lastInput.OperatorUTF16; string(rune(got[0])) != "a" || len(got) != 5 {
		t.Fatalf("operator not copied: %v", got)
	}
}

func TestUploadSaverInputAndPathIdentity(t *testing.T) {
	auth, saver := upAllowAuth(), &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
	r := upPUT(nil, 3)
	r.Header.Set("X-Checksum-Sha1", upSHA1)
	rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, r)
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	want, err := localrepo.NewUploadPath(upRepoKey, "a/f.bin")
	if err != nil {
		t.Fatal(err)
	}
	if auth.calls != 1 || auth.lastPath.Key() != want.Key() {
		t.Fatal("auth path identity mismatch")
	}
	in := saver.lastInput
	if in.Path.Key() != want.Key() || in.DeclaredSize != 3 {
		t.Fatalf("input path/size mismatch: %+v", in)
	}
	if !reflect.DeepEqual(in.Checksums, repository.ClientFileChecksums{SHA1Original: upUnits(upSHA1)}) {
		t.Fatalf("input checksums mismatch: %+v", in.Checksums)
	}
	if saver.lastCtx == nil || saver.lastCtx != r.Context() {
		// A direct hand-made request has a nonnil background-derived ctx.
		t.Fatal("saver ctx mismatch")
	}
}

func TestUploadSuccess201(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sha1Orig []uint16
		md5Orig  []uint16
		origKeys []string
	}{
		{"no originals", nil, nil, []string{"sha256"}},
		{"sha1 original", upUnits(upSHA1), nil, []string{"sha1", "sha256"}},
		{"both originals", upUnits(upSHA1), upUnits(upMD5), []string{"sha1", "md5", "sha256"}},
	} {
		auth := upAllowAuth()
		saver := &upSaver{source: upPathSource(t, "a/f.bin", tc.sha1Orig, tc.md5Orig)}
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, upPUT(nil, 3))
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		body := rec.Body.Bytes()
		if got := rec.Header().Get("Content-Type"); got != upMediaType {
			t.Fatalf("%s: media type %q", tc.name, got)
		}
		if got := rec.Header().Get("Location"); got != upOrigin+"/artifactory/"+upRepoKey+"/a/f.bin" {
			t.Fatalf("%s: location %q", tc.name, got)
		}
		if got := rec.Header().Get("X-Checksum-Sha256"); got != upSHA256 {
			t.Fatalf("%s: sha256 header %q", tc.name, got)
		}
		if rec.Header().Get("X-Checksum-Sha1") != "" || rec.Header().Get("X-Checksum-Md5") != "" {
			t.Fatalf("%s: unexpected checksum headers", tc.name)
		}
		if got := rec.Header().Get("Content-Length"); got != fmt.Sprint(len(body)) {
			t.Fatalf("%s: content length %q vs body %d", tc.name, got, len(body))
		}
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("%s: json %v", tc.name, err)
		}
		if doc["repo"] != upRepoKey || doc["path"] != "/a/f.bin" {
			t.Fatalf("%s: repo/path %v %v", tc.name, doc["repo"], doc["path"])
		}
		created, _ := doc["created"].(string)
		if len(created) != 24 || !strings.HasSuffix(created, "Z") || !strings.Contains(created, "T") {
			t.Fatalf("%s: created %q", tc.name, created)
		}
		if doc["createdBy"] != "admin" || doc["mimeType"] != "application/octet-stream" {
			t.Fatalf("%s: createdBy/mime %v %v", tc.name, doc["createdBy"], doc["mimeType"])
		}
		if doc["size"] != "3" {
			t.Fatalf("%s: size %v", tc.name, doc["size"])
		}
		uri := upOrigin + "/artifactory/" + upRepoKey + "/a/f.bin"
		if doc["uri"] != uri || doc["downloadUri"] != uri || rec.Header().Get("Location") != uri {
			t.Fatalf("%s: uri %v", tc.name, doc["uri"])
		}
		checksums := doc["checksums"].(map[string]any)
		if checksums["sha1"] != upSHA1 || checksums["md5"] != upMD5 || checksums["sha256"] != upSHA256 {
			t.Fatalf("%s: checksums %v", tc.name, checksums)
		}
		originals := doc["originalChecksums"].(map[string]any)
		if len(originals) != len(tc.origKeys) || originals["sha256"] != upSHA256 {
			t.Fatalf("%s: originals %v", tc.name, originals)
		}
		for _, key := range tc.origKeys {
			if _, ok := originals[key]; !ok {
				t.Fatalf("%s: missing original key %q in %v", tc.name, key, originals)
			}
		}
		if strings.Contains(string(body), "updated") {
			t.Fatalf("%s: unexpected updated field", tc.name)
		}
		if len(rep.cats) != 0 {
			t.Fatalf("%s: unexpected reports %v", tc.name, rep.cats)
		}
	}
	// .txt maps to text/plain.
	auth := upAllowAuth()
	saver := &upSaver{source: upPathSource(t, "a/g.txt", nil, nil)}
	rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, upReq(http.MethodPut, "/artifactory/"+upRepoKey+"/a/g.txt", io.NopCloser(strings.NewReader("abc")), 3))
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"text/plain"`) == false {
		t.Fatalf("missing text/plain mime: %s", rec.Body.String())
	}
}

func TestUploadSourceValidationFailures(t *testing.T) {
	corrupt := func(f func(s *repository.FileSnapshotSource)) *upSaver {
		src := upPathSource(t, "a/f.bin", nil, nil)
		f(&src)
		return &upSaver{source: src}
	}
	for _, tc := range []struct {
		name  string
		saver *upSaver
	}{
		{"zero source", &upSaver{}},
		{"wrong path identity", corrupt(func(s *repository.FileSnapshotSource) {
			other, _ := localrepo.NewUploadPath(upRepoKey, "a/other.bin")
			s.Reference.Path = other
		})},
		{"nil metadata", corrupt(func(s *repository.FileSnapshotSource) { s.Reference.Metadata = nil })},
		{"nonpositive id", corrupt(func(s *repository.FileSnapshotSource) { s.Reference.ID = 0 })},
		{"invalid actual flag", corrupt(func(s *repository.FileSnapshotSource) { s.SHA1ActualValid = false })},
	} {
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(upAllowAuth(), tc.saver, 8), rep, upPUT(nil, 3))
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != http.StatusInternalServerError || rec.Body.String() != "internal error\n" {
			t.Fatalf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		if !rep.has("projection") {
			t.Fatalf("%s: reports %v", tc.name, rep.cats)
		}
	}
	// A nonnil error outranks any returned source (no forgery, no 201).
	mismatch := fmt.Errorf("filestore: sha1: %w", filestore.ErrChecksumMismatch)
	saver := &upSaver{source: upSource(localrepo.UploadInput{}, nil, nil), err: mismatch}
	rep := &upReport{}
	rec, aborted := upServe(t, upDeps(upAllowAuth(), saver, 8), rep, upPUT(nil, 3))
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("source forgery: status %d", rec.Code)
	}
}

func TestUploadSaveErrorClassification(t *testing.T) {
	readErr := errors.New("boom body")
	mismatch := fmt.Errorf("filestore: sha1: %w", filestore.ErrChecksumMismatch)
	removeErr := errors.New("filestore: cleanup staging: remove /x: permission denied")
	closeErr := errors.New("filestore: close staging: closed")
	var cycleA, cycleB upCycle
	cycleA.next = &cycleB
	cycleB.next = &cycleA
	deep := error(errors.New("leaf"))
	for i := 0; i < 40; i++ {
		deep = fmt.Errorf("wrap%d: %w", i, deep)
	}
	longBody := strings.NewReader(strings.Repeat("x", 64))
	for _, tc := range []struct {
		name    string
		err     error
		body    io.Reader
		consume func(io.Reader) error
		status  int
		report  string
	}{
		// B206-R1: only exact client-sentinel pure trees answer 409.
		{"client mismatch", repository.ErrClientChecksumMismatch, nil, nil, 409, "body"},
		{"wrapped checksum mismatch", mismatch, nil, nil, 409, "body"},
		{"single child join", errors.Join(mismatch, nil), nil, nil, 409, "body"},
		{"length mismatch", fmt.Errorf("size: %w", filestore.ErrLengthMismatch), nil, nil, 409, "body"},
		{"two sentinel leaves", errors.Join(repository.ErrClientChecksumMismatch, mismatch), nil, nil, 409, "body"},
		{"nested sentinel join", errors.Join(errors.Join(mismatch, nil), repository.ErrClientChecksumMismatch), nil, nil, 409, "body"},
		// Compound cleanup/close/extra-IO faults stay 500.
		{"mismatch plus remove", errors.Join(mismatch, removeErr), nil, nil, 500, "save"},
		{"mismatch plus close", errors.Join(mismatch, closeErr), nil, nil, 500, "save"},
		{"mismatch plus close plus remove", errors.Join(errors.Join(mismatch, closeErr), removeErr), nil, nil, 500, "save"},
		{"client mismatch plus io", errors.Join(repository.ErrClientChecksumMismatch, errors.New("disk")), nil, nil, 500, "save"},
		{"unknown leaf", errors.New("unknown"), nil, nil, 500, "save"},
		{"empty multi leaf", &upMultiErr{}, nil, nil, 500, "save"},
		{"nil children only", &upMultiErr{children: []error{nil, nil}}, nil, nil, 500, "save"},
		{"cyclic", &cycleA, nil, nil, 500, "save"},
		{"over budget depth", deep, nil, nil, 500, "save"},
		// Persistence outranks every client class before any sentinel.
		{"persistence wraps mismatch", errors.Join(fmt.Errorf("node: %w", localrepo.ErrUploadPersistence), mismatch), nil, nil, 500, "save"},
		{"persistence leaf", fmt.Errorf("commit: %w", localrepo.ErrUploadPersistence), nil, nil, 500, "save"},
		{"persistence deep", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", localrepo.ErrUploadPersistence)), nil, nil, 500, "save"},
		// Budget and read-error chains need a single nonnil cause path.
		{"budget direct", nil, longBody, func(rr io.Reader) error { return upDrain(rr) }, 413, "body"},
		{"budget wrapped", nil, longBody, func(rr io.Reader) error {
			if err := upDrain(rr); err != nil {
				return fmt.Errorf("filestore: hash source: %w", err)
			}
			return nil
		}, 413, "body"},
		{"budget plus cleanup", nil, longBody, func(rr io.Reader) error {
			if err := upDrain(rr); err != nil {
				return errors.Join(err, removeErr)
			}
			return nil
		}, 500, "save"},
		{"read error direct", nil, nil, func(rr io.Reader) error {
			_, err := rr.Read(make([]byte, 4))
			return err
		}, 400, "body"},
		{"read error wrapped", nil, nil, func(rr io.Reader) error {
			_, err := rr.Read(make([]byte, 4))
			if err != nil {
				return fmt.Errorf("filestore: hash source: %w", err)
			}
			return nil
		}, 400, "body"},
		{"read error plus sibling", nil, nil, func(rr io.Reader) error {
			_, err := rr.Read(make([]byte, 4))
			return errors.Join(err, removeErr)
		}, 500, "save"},
		// Dependency child deadline while the parent stays alive is 500.
		{"child deadline", fmt.Errorf("node timeout: %w", context.DeadlineExceeded), nil, nil, 500, "save"},
		// An unrelated read error in the tree never matches a recorded one.
		{"unrelated recorded error", readErr, nil, func(rr io.Reader) error {
			return errors.New("different fault")
		}, 500, "save"},
	} {
		auth := upAllowAuth()
		saver := &upSaver{err: tc.err, consume: tc.consume}
		if tc.consume == nil {
			saver.consume = func(rr io.Reader) error { return nil }
		}
		body := tc.body
		if body == nil {
			// The default body always fails with the recorded read error.
			body = upErrReader{err: readErr}
		}
		r := upPUT(io.NopCloser(body), 5)
		rep := &upReport{}
		rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
		if aborted {
			t.Fatalf("%s: unexpected abort", tc.name)
		}
		if rec.Code != tc.status {
			t.Fatalf("%s: status %d body %q", tc.name, rec.Code, rec.Body.String())
		}
		if !rep.has(tc.report) {
			t.Fatalf("%s: reports %v", tc.name, rep.cats)
		}
	}
	// A non-comparable recorded body error must not panic; conservative 500.
	sliceErr := upSliceErr{parts: []string{"a"}}
	auth := upAllowAuth()
	saver := &upSaver{consume: func(rr io.Reader) error {
		_, err := rr.Read(make([]byte, 4))
		return err
	}}
	rep := &upReport{}
	rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, upPUT(io.NopCloser(upErrReader{err: sliceErr}), 5))
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusInternalServerError || !rep.has("save") {
		t.Fatalf("non-comparable: status %d reports %v", rec.Code, rep.cats)
	}
}

// upDrain reads the wrapped body until the budget sentinel surfaces.
func upDrain(rr io.Reader) error {
	buf := make([]byte, 4)
	total := 0
	for {
		n, err := rr.Read(buf)
		total += n
		if err != nil {
			return err
		}
		if total > 128 {
			return errors.New("drain: no budget error")
		}
	}
}

func TestUploadBodyBudgetBoundary(t *testing.T) {
	// maxBody=8: reading the 9th byte yields the budget sentinel (413) even
	// though the declared length is smaller and lies.
	auth := upAllowAuth()
	saver := &upSaver{consume: func(rr io.Reader) error { return upDrain(rr) }}
	rep := &upReport{}
	r := upPUT(io.NopCloser(strings.NewReader(strings.Repeat("x", 64))), 5)
	rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, r)
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusRequestEntityTooLarge || rec.Body.String() != "payload too large\n" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	// Exactly max bytes then EOF stays a save-side answer, not a 413.
	auth2 := upAllowAuth()
	saver2 := &upSaver{consume: func(rr io.Reader) error {
		data, err := io.ReadAll(rr)
		if err != nil {
			return err
		}
		if len(data) != 8 {
			return fmt.Errorf("read %d bytes", len(data))
		}
		return nil
	}}
	r2 := upPUT(io.NopCloser(strings.NewReader("12345678")), 8)
	rec2, aborted2 := upServe(t, upDeps(auth2, saver2, 8), &upReport{}, r2)
	if aborted2 {
		t.Fatal("unexpected abort")
	}
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("exact budget: status %d", rec2.Code)
	}
}

func TestUploadCancellationSemantics(t *testing.T) {
	// Parent already cancelled at entry: transport abort, no status bytes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := upPUT(nil, 3)
	r = r.WithContext(ctx)
	rep := &upReport{}
	rec, aborted := upServe(t, upDeps(upAllowAuth(), &upSaver{}, 8), rep, r)
	if !aborted {
		t.Fatalf("expected abort, status %d", rec.Code)
	}
	if !rep.has("cancel") {
		t.Fatalf("reports %v", rep.cats)
	}
	// Parent cancelled inside the saver: abort outranks the save error.
	ctx2, cancel2 := context.WithCancel(context.Background())
	saver := &upSaver{err: repository.ErrClientChecksumMismatch}
	auth := &upAuth{result: httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: upUnits("alice")}, cancelCtx: cancel2}
	r2 := upPUT(nil, 3)
	r2 = r2.WithContext(ctx2)
	rep2 := &upReport{}
	rec2, aborted2 := upServe(t, upDeps(auth, saver, 8), rep2, r2)
	if !aborted2 {
		t.Fatalf("expected abort, status %d", rec2.Code)
	}
	if !rep2.has("cancel") {
		t.Fatalf("reports %v", rep2.cats)
	}
}

func TestUploadNoEarlySuccessAndBodyOwnership(t *testing.T) {
	rec := httptest.NewRecorder()
	auth := upAllowAuth()
	saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
	saver.beforeSave = func() {
		if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
			t.Error("response bytes committed before the saver returned")
		}
	}
	deps := upDeps(auth, saver, 8)
	deps.Report = (&upReport{}).report
	handler, err := httpserver.NewUploadHandler(deps)
	if err != nil {
		t.Fatal(err)
	}
	body := &upBody{r: strings.NewReader("abc")}
	r := upPUT(body, 3)
	handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	if body.closed {
		t.Fatal("handler must not close the server-owned body")
	}
	// The same ownership holds on the failure path.
	rec2 := httptest.NewRecorder()
	saver2 := &upSaver{err: errors.New("node down")}
	deps2 := upDeps(upAllowAuth(), saver2, 8)
	deps2.Report = (&upReport{}).report
	handler2, err := httpserver.NewUploadHandler(deps2)
	if err != nil {
		t.Fatal(err)
	}
	body2 := &upBody{r: strings.NewReader("abc")}
	handler2.ServeHTTP(rec2, upPUT(body2, 3))
	if rec2.Code != http.StatusInternalServerError || body2.closed {
		t.Fatalf("status %d closed %v", rec2.Code, body2.closed)
	}
}

func TestUploadWriteFailureAborts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		short bool
	}{
		{"write error", false},
		{"short write", true},
	} {
		auth := upAllowAuth()
		saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
		rep := &upReport{}
		deps := upDeps(auth, saver, 8)
		deps.Report = rep.report
		handler, err := httpserver.NewUploadHandler(deps)
		if err != nil {
			t.Fatal(err)
		}
		inner := httptest.NewRecorder()
		w := &upFailWriter{inner: inner, short: tc.short}
		aborted := false
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					if recovered != http.ErrAbortHandler {
						t.Fatalf("%s: panic %v", tc.name, recovered)
					}
					aborted = true
				}
			}()
			handler.ServeHTTP(w, upPUT(nil, 3))
		}()
		if !aborted {
			t.Fatalf("%s: expected abort", tc.name)
		}
		if inner.Code != http.StatusCreated {
			t.Fatalf("%s: code %d", tc.name, inner.Code)
		}
		if !rep.has("write") {
			t.Fatalf("%s: reports %v", tc.name, rep.cats)
		}
	}
}

// upFailWriter fails the first Write after headers, or short-writes it.
type upFailWriter struct {
	inner *httptest.ResponseRecorder
	short bool
}

func (w *upFailWriter) Header() http.Header  { return w.inner.Header() }
func (w *upFailWriter) WriteHeader(code int) { w.inner.WriteHeader(code) }
func (w *upFailWriter) Write(p []byte) (int, error) {
	if !w.short {
		return 0, errors.New("disk full")
	}
	if len(p) > 0 {
		n, err := w.inner.Write(p[:len(p)-1])
		return n, err
	}
	return 0, nil
}

func TestUploadNilBodyDirectCallSafety(t *testing.T) {
	// nil Body with CL 0 may run on http.NoBody.
	auth := upAllowAuth()
	saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
	saver.consume = func(rr io.Reader) error {
		if _, err := io.ReadAll(rr); err != nil {
			return err
		}
		return nil
	}
	rep := &upReport{}
	rec, aborted := upServe(t, upDeps(auth, saver, 8), rep, upReq(http.MethodPut, "/artifactory/"+upRepoKey+"/a/f.bin", nil, 0))
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	// nil Body with a positive declared length fails safely.
	rep2 := &upReport{}
	rec2, aborted2 := upServe(t, upDeps(upAllowAuth(), &upSaver{}, 8), rep2, upReq(http.MethodPut, "/artifactory/"+upRepoKey+"/a/f.bin", nil, 3))
	if aborted2 {
		t.Fatal("unexpected abort")
	}
	if rec2.Code != http.StatusBadRequest || rec2.Body.String() != "bad request\n" {
		t.Fatalf("status %d body %q", rec2.Code, rec2.Body.String())
	}
	// nil URL with a well-formed raw origin URI is still handled safely.
	r := upPUT(nil, 3)
	r.URL = nil
	rep3 := &upReport{}
	rec3, aborted3 := upServe(t, upDeps(upAllowAuth(), &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}, 8), rep3, r)
	if aborted3 {
		t.Fatal("unexpected abort")
	}
	if rec3.Code != http.StatusCreated {
		t.Fatalf("status %d", rec3.Code)
	}
}

func TestUploadHostNeverReflected(t *testing.T) {
	// The request Host is attacker-controlled and must never leak into the
	// Location header or the JSON body.
	auth := upAllowAuth()
	saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
	r := upPUT(nil, 3)
	r.Host = "evil.example:9"
	rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, r)
	if aborted {
		t.Fatal("unexpected abort")
	}
	if strings.Contains(rec.Body.String(), "evil.example") || strings.Contains(rec.Header().Get("Location"), "evil.example") {
		t.Fatal("request host reflected")
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), upOrigin+"/artifactory/") {
		t.Fatalf("location %q", rec.Header().Get("Location"))
	}
}

// upNestedErr is a statically comparable struct error whose interface field
// may hold a runtime-incomparable dynamic value (slice); comparing two such
// values with == panics even though reflect.Type.Comparable() is true.
type upNestedErr struct{ inner any }

func (e upNestedErr) Error() string { return "nested incomparable" }

func TestUploadOriginTrailingSlashSingleSeparator(t *testing.T) {
	// A validated origin ending in exactly one "/" must be normalized once
	// in the constructor so Location, uri and downloadUri all carry exactly
	// one separator before /artifactory (no "//artifactory" anywhere).
	d := upDeps(upAllowAuth(), &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}, 8)
	d.PublicOrigin = upOrigin + "/"
	rec, aborted := upServe(t, d, &upReport{}, upPUT(nil, 3))
	if aborted || rec.Code != http.StatusCreated {
		t.Fatalf("status %d abort %v", rec.Code, aborted)
	}
	want := upOrigin + "/artifactory/" + upRepoKey + "/a/f.bin"
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location=%q want %q", got, want)
	}
	var item struct {
		DownloadURI string `json:"downloadUri"`
		URI         string `json:"uri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("unmarshal: %v body %q", err, rec.Body.String())
	}
	if item.DownloadURI != want {
		t.Fatalf("downloadUri=%q want %q", item.DownloadURI, want)
	}
	if item.URI != want {
		t.Fatalf("uri=%q want %q", item.URI, want)
	}
	if strings.Contains(rec.Body.String(), "//artifactory/") {
		t.Fatalf("double separator in body %q", rec.Body.String())
	}
	// The same origin without any trailing slash keeps the same projection.
	d2 := upDeps(upAllowAuth(), &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}, 8)
	rec2, aborted2 := upServe(t, d2, &upReport{}, upPUT(nil, 3))
	if aborted2 || rec2.Code != http.StatusCreated || rec2.Header().Get("Location") != want {
		t.Fatalf("plain origin: status %d Location %q", rec2.Code, rec2.Header().Get("Location"))
	}
}

func TestUploadHeaderKeyPresenceRequiresExactlyOneValue(t *testing.T) {
	// A header key present in the map (any EqualFold spelling) with a nil
	// or empty value slice is PRESENT: it must never be silently treated as
	// absent, and presence requires exactly one aggregated value, so these
	// reject with 400 before auth and before the saver runs.
	for _, key := range []string{"X-Checksum-Sha1", "X-Checksum-Md5", "X-Checksum-Sha256", "Content-Length"} {
		for _, values := range [][]string{nil, {}} {
			name := key
			if values == nil {
				name += "-nil"
			} else {
				name += "-empty"
			}
			t.Run(name, func(t *testing.T) {
				auth := upAllowAuth()
				saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
				r := upPUT(nil, 3)
				r.Header[key] = values
				rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, r)
				if aborted || rec.Code != http.StatusBadRequest || auth.calls != 0 || saver.calls != 0 {
					t.Fatalf("status=%d auth=%d save=%d abort=%v", rec.Code, auth.calls, saver.calls, aborted)
				}
			})
		}
	}
	// Frozen aggregation rule: values aggregate across any-spelling aliases,
	// so a present nil alias plus one alias carrying a single valid value has
	// exactly one aggregated value and is accepted.
	t.Run("mixed alias nil plus one valid value accepted", func(t *testing.T) {
		for _, tc := range []struct {
			key   string
			alias string
			value string
		}{
			{"X-Checksum-Sha1", "x-checksum-sha1", upSHA1},
			{"X-Checksum-Sha256", "X-CheckSum-Sha256", upSHA256},
			{"Content-Length", "content-length", "3"},
		} {
			auth := upAllowAuth()
			saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
			r := upPUT(nil, 3)
			r.Header[tc.key] = nil
			r.Header.Set(tc.alias, tc.value)
			rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, r)
			if aborted || rec.Code != http.StatusCreated || auth.calls != 1 || saver.calls != 1 {
				t.Fatalf("%s: status=%d auth=%d save=%d abort=%v", tc.key, rec.Code, auth.calls, saver.calls, aborted)
			}
		}
	})
	// A present empty alias next to one invalid value still rejects.
	t.Run("mixed alias empty plus invalid value rejected", func(t *testing.T) {
		auth := upAllowAuth()
		saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
		r := upPUT(nil, 3)
		r.Header["X-Checksum-Sha1"] = []string{}
		r.Header.Set("x-checksum-sha1", "UPPERCASE")
		rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, r)
		if aborted || rec.Code != http.StatusBadRequest || auth.calls != 0 || saver.calls != 0 {
			t.Fatalf("status=%d auth=%d save=%d abort=%v", rec.Code, auth.calls, saver.calls, aborted)
		}
	})
	// An absent raw Content-Length key stays valid: net/http supplies the
	// parsed field for real requests.
	t.Run("absent content-length key allowed", func(t *testing.T) {
		auth := upAllowAuth()
		saver := &upSaver{source: upPathSource(t, "a/f.bin", nil, nil)}
		r := upPUT(nil, 3)
		delete(r.Header, "Content-Length")
		rec, aborted := upServe(t, upDeps(auth, saver, 8), &upReport{}, r)
		if aborted || rec.Code != http.StatusCreated {
			t.Fatalf("status=%d abort=%v", rec.Code, aborted)
		}
	})
}

func TestUploadIncomparableNestedReadErrorConservative(t *testing.T) {
	// The recorded reader error is a value struct whose type is statically
	// comparable but whose interface field holds a slice: identity comparison
	// must consult runtime value comparability and answer "not equal" without
	// panicking, so the save stays a conservative 500 (never a 400 here).
	nested := upNestedErr{inner: any([]byte("opaque"))}
	saver := &upSaver{consume: func(rr io.Reader) error {
		_, err := io.ReadAll(rr)
		return err
	}}
	rep := &upReport{}
	rec, aborted := upServe(t, upDeps(upAllowAuth(), saver, 8), rep, upPUT(io.NopCloser(upErrReader{err: nested}), 3))
	if aborted {
		t.Fatal("unexpected abort")
	}
	if rec.Code != http.StatusInternalServerError || !rep.has("save") {
		t.Fatalf("nested value: status %d reports %v", rec.Code, rep.cats)
	}
	// Pointer sentinel identity semantics stay intact: the same error behind
	// a pointer is runtime-comparable, matches by identity through the single
	// recorded read-error path and classifies as 400.
	ptr := &upNestedErr{inner: any([]byte("opaque"))}
	saver2 := &upSaver{consume: func(rr io.Reader) error {
		_, err := io.ReadAll(rr)
		return err
	}}
	rep2 := &upReport{}
	rec2, aborted2 := upServe(t, upDeps(upAllowAuth(), saver2, 8), rep2, upPUT(io.NopCloser(upErrReader{err: ptr}), 3))
	if aborted2 {
		t.Fatal("unexpected abort")
	}
	if rec2.Code != http.StatusBadRequest || !rep2.has("body") {
		t.Fatalf("pointer identity: status %d reports %v", rec2.Code, rep2.cats)
	}
}

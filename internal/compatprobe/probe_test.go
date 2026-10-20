package compatprobe_test

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/compatprobe"
)

// syntheticFixture builds deterministic non-secret bytes we authored for
// these tests; no raw capture or credential material is read.
// syntheticFixture 生成本测试自创的确定性字节，不读取任何原始抓包或凭据。
func syntheticFixture(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

const frozenDate = "Wed, 14 Jan 2026 09:00:00 GMT"

// frozenPlan is the server's intended response before mutations.
// frozenPlan 是变更前服务端准备好的响应。
type frozenPlan struct {
	status     int
	body       []byte
	headers    map[string]string
	clOverride string // Explicit Content-Length, e.g. for truncation / 显式覆盖 Content-Length。
	redirect   string // Non-empty: respond 302 to this URL / 非空则响应 302。
	truncateBy int    // Declare len(body)+truncateBy then write fewer bytes / 声明更长再截断。
	sleep      time.Duration
}

// frozenServer implements the observed 7.161.26 behaviors by hand; it must
// not delegate to net/http ServeContent because the frozen sample diverges
// from RFC semantics (star If-None-Match 200, unmatched If-Match 200,
// HEAD+Range 200).
// frozenServer 手工实现冻结的 7.161.26 行为，不使用 ServeContent 作为基准。
type frozenServer struct {
	fixture []byte
	mutate  func(id string, p *frozenPlan)

	mu              sync.Mutex
	gets, heads     int
	others          int
	authSeen        []string
	reqs            []reqSnap
	reflectAuthInto []string // Header names echoing the request Authorization / 回显凭据的响应头。
	reflectTokenTo  string   // Header echoing the bare token only / 仅回显裸令牌的头。
	reflectURLTo    string   // Header echoing the absolute request URL / 回显绝对请求 URL 的头。
}

// reqSnap captures the exact conditional/range request headers a frozen case
// sent, so tests can assert byte-exact inputs instead of classifications.
// reqSnap 记录冻结用例发出的精确条件/范围请求头，用例按字节断言而非仅分类。
type reqSnap struct {
	method       string
	inm, ims     string
	im, ifr, rng string
}

func newFrozenServer(t *testing.T, fixture []byte, mutate func(string, *frozenPlan)) (*frozenServer, string) {
	t.Helper()
	s := &frozenServer{fixture: fixture, mutate: mutate}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts.URL + "/generic/bin128-fixture.bin"
}

func (s *frozenServer) counts() (gets, heads, others, auth int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.heads, s.others, len(s.authSeen)
}

func (s *frozenServer) requests() []reqSnap {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]reqSnap{}, s.reqs...)
}

func (s *frozenServer) authValues() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.authSeen...)
}

func (s *frozenServer) setReflectAuth(names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reflectAuthInto = names
}

func (s *frozenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	switch r.Method {
	case http.MethodGet:
		s.gets++
	case http.MethodHead:
		s.heads++
	default:
		s.others++
	}
	auth := r.Header.Get("Authorization")
	if auth != "" {
		s.authSeen = append(s.authSeen, auth)
	}
	s.reqs = append(s.reqs, reqSnap{
		method: r.Method,
		inm:    r.Header.Get("If-None-Match"),
		ims:    r.Header.Get("If-Modified-Since"),
		im:     r.Header.Get("If-Match"),
		ifr:    r.Header.Get("If-Range"),
		rng:    r.Header.Get("Range"),
	})
	mutate, reflect, tokenTo, urlTo := s.mutate, s.reflectAuthInto, s.reflectTokenTo, s.reflectURLTo
	s.mu.Unlock()

	id := s.classify(r)
	p := s.plan(id)
	if mutate != nil {
		mutate(id, p)
	}
	for _, name := range reflect {
		p.headers[name] = auth
	}
	if tokenTo != "" {
		if _, token, found := strings.Cut(auth, " "); found {
			p.headers[tokenTo] = token
		}
	}
	if urlTo != "" {
		scheme := "http://"
		if r.TLS != nil {
			scheme = "https://"
		}
		p.headers[urlTo] = scheme + r.Host + r.URL.String()
	}
	if p.sleep > 0 {
		time.Sleep(p.sleep)
	}
	if p.redirect != "" {
		w.Header().Set("Location", p.redirect)
		w.WriteHeader(http.StatusFound)
		return
	}
	for k, v := range p.headers {
		if v != "" {
			w.Header().Set(k, v)
		}
	}
	if p.truncateBy > 0 {
		w.Header().Set("Content-Length", strconv.Itoa(len(p.body)+p.truncateBy))
	} else if p.clOverride != "" {
		w.Header().Set("Content-Length", p.clOverride)
	}
	w.WriteHeader(p.status)
	if r.Method == http.MethodHead {
		return
	}
	w.Write(p.body)
}

// classify maps an incoming request to the frozen case it must answer.
// classify 将请求映射到其应回答的冻结用例。
func (s *frozenServer) classify(r *http.Request) string {
	etag := s.etag()
	n := len(s.fixture)
	inm := r.Header.Get("If-None-Match")
	ims := r.Header.Get("If-Modified-Since")
	im := r.Header.Get("If-Match")
	ifr := r.Header.Get("If-Range")
	rng := r.Header.Get("Range")
	switch {
	case r.Method == http.MethodHead && rng == "bytes=0-3":
		return "head-range"
	case r.Method == http.MethodHead:
		return "head"
	case inm == "*":
		return "etag-any"
	case inm == etag:
		return "etag-match"
	case inm == "W/"+etag:
		return "etag-weak"
	case inm != "" && ims != "":
		return "conditional-precedence"
	case inm != "":
		return "etag-miss"
	case ims == frozenDate:
		return "modified-match"
	case ims != "":
		return "modified-old"
	case im == etag:
		return "if-match-hit"
	case im != "":
		return "if-match-miss"
	case ifr == etag && rng == "bytes=0-3":
		return "if-range-hit"
	case ifr != "" && rng == "bytes=0-3":
		return "if-range-miss"
	case rng == "bytes=0-3":
		return "range-prefix"
	case rng == "bytes=-4":
		return "range-suffix"
	case rng == "bytes=4-":
		return "range-open"
	case rng == fmt.Sprintf("bytes=%d-", n):
		return "range-unsatisfiable"
	default:
		return "get"
	}
}

func (s *frozenServer) etag() string {
	sum := sha1.Sum(s.fixture)
	return hex.EncodeToString(sum[:])
}

// plan builds the frozen response for one case.
// plan 构造一个用例的冻结响应。
func (s *frozenServer) plan(id string) *frozenPlan {
	f := s.fixture
	n := len(f)
	s1 := sha1.Sum(f)
	s256 := sha256.Sum256(f)
	common := func(extra map[string]string) map[string]string {
		h := map[string]string{
			"ETag":              hex.EncodeToString(s1[:]),
			"Last-Modified":     frozenDate,
			"X-Checksum-Sha1":   hex.EncodeToString(s1[:]),
			"X-Checksum-Sha256": hex.EncodeToString(s256[:]),
			"Accept-Ranges":     "bytes",
			"Content-Type":      "application/octet-stream",
		}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}
	full := func() *frozenPlan {
		return &frozenPlan{status: 200, body: f, headers: common(map[string]string{"Content-Length": strconv.Itoa(n)})}
	}
	head := func() *frozenPlan {
		return &frozenPlan{status: 200, headers: common(map[string]string{"Content-Length": strconv.Itoa(n)})}
	}
	part := func(start, end int) *frozenPlan {
		h := common(map[string]string{
			"Content-Length": strconv.Itoa(end - start + 1),
			"Content-Range":  fmt.Sprintf("bytes %d-%d/%d", start, end, n),
		})
		return &frozenPlan{status: 206, body: f[start : end+1], headers: h}
	}
	notModified := func() *frozenPlan {
		return &frozenPlan{status: 304, headers: map[string]string{"ETag": hex.EncodeToString(s1[:]), "Last-Modified": frozenDate}}
	}
	switch id {
	case "head", "head-range":
		return head()
	case "range-prefix":
		return part(0, 3)
	case "range-suffix":
		return part(n-4, n-1)
	case "range-open":
		return part(4, n-1)
	case "if-range-hit":
		return part(0, 3)
	case "etag-match", "etag-weak", "modified-match":
		return notModified()
	case "range-unsatisfiable":
		return &frozenPlan{status: 416, headers: map[string]string{"Content-Range": fmt.Sprintf("bytes */%d", n), "Content-Length": "0"}}
	default: // get, etag-any, etag-miss, modified-old, if-match-*, if-range-miss, conditional-precedence
		return full()
	}
}

// wantIDs is the frozen plan order from docs/GENERIC-DOWNLOAD-CONTRACT.md.
// wantIDs 是契约文档冻结的计划顺序。
var wantIDs = []string{
	"get", "head", "range-prefix", "range-suffix", "range-open",
	"range-unsatisfiable", "etag-match", "etag-any", "etag-weak", "etag-miss",
	"modified-match", "modified-old", "if-match-hit", "if-match-miss",
	"if-range-hit", "if-range-miss", "conditional-precedence", "head-range",
}

func byID(t *testing.T, rep compatprobe.Report, id string) compatprobe.CaseResult {
	t.Helper()
	for _, c := range rep.Cases {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("case %q missing from report", id)
	return compatprobe.CaseResult{}
}

func TestProfileConstants(t *testing.T) {
	if compatprobe.Profile != "artifactory-7.161.26-generic-download-v1" {
		t.Fatalf("profile = %q", compatprobe.Profile)
	}
	if compatprobe.CaseCount != 18 || compatprobe.MinFixtureBytes != 5 || compatprobe.MaxFixtureBytes != 65536 {
		t.Fatalf("frozen bounds changed: count=%d min=%d max=%d",
			compatprobe.CaseCount, compatprobe.MinFixtureBytes, compatprobe.MaxFixtureBytes)
	}
}

func TestValidateTable(t *testing.T) {
	good := compatprobe.Options{URL: "http://example.test/a.bin", Fixture: syntheticFixture(5)}
	badURLs := []string{
		"", "example.test/a.bin", "ftp://example.test/a.bin", "mailto:x@y",
		"http://user:pw@example.test/a.bin", "http://example.test/a.bin?q=1",
		"http://example.test/a.bin?", "http://example.test/a.bin#frag",
		"http://example.test/a.bin#", "http:opaque", "//example.test/a.bin",
		"http://", "http://:8080/a.bin", "http://exa mple.test/a.bin",
		"ht tp://example.test/a.bin",
	}
	for _, raw := range badURLs {
		opts := good
		opts.URL = raw
		if err := compatprobe.Validate(opts); err == nil {
			t.Errorf("url %d: expected rejection", len(raw))
		}
	}
	badOpts := map[string]func(compatprobe.Options) compatprobe.Options{
		"fixture-too-small": func(o compatprobe.Options) compatprobe.Options { o.Fixture = syntheticFixture(4); return o },
		"fixture-too-large": func(o compatprobe.Options) compatprobe.Options { o.Fixture = syntheticFixture(65537); return o },
		"auth-no-scheme":    func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Token abc"; return o },
		"auth-empty-value":  func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Basic "; return o },
		"auth-crlf":         func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Basic abc\r\nX: y"; return o },
		"auth-control":      func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Bearer a\x01b"; return o },
		"auth-space-token":  func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Basic YWJj IGJhZA"; return o },
		"auth-space-bearer": func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Bearer a b"; return o },
		"auth-space-only":   func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Basic  "; return o },
		"auth-non-ascii":    func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Bearer tøken"; return o },
		"auth-oversize": func(o compatprobe.Options) compatprobe.Options {
			o.Authorization = "Basic " + strings.Repeat("a", 4091)
			return o
		},
		"timeout-negative": func(o compatprobe.Options) compatprobe.Options { o.Timeout = -time.Second; return o },
		"timeout-over-max": func(o compatprobe.Options) compatprobe.Options { o.Timeout = 61 * time.Second; return o },
	}
	for name, mutate := range badOpts {
		if err := compatprobe.Validate(mutate(good)); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	accept := map[string]func(compatprobe.Options) compatprobe.Options{
		"fixture-min": func(o compatprobe.Options) compatprobe.Options { o.Fixture = syntheticFixture(5); return o },
		"fixture-max": func(o compatprobe.Options) compatprobe.Options { o.Fixture = syntheticFixture(65536); return o },
		"auth-basic":  func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Basic YWJj"; return o },
		"auth-bearer": func(o compatprobe.Options) compatprobe.Options { o.Authorization = "Bearer abc.def-ghi"; return o },
		"auth-max-length": func(o compatprobe.Options) compatprobe.Options {
			o.Authorization = "Basic " + strings.Repeat("a", 4090)
			return o
		},
		"timeout-zero":     func(o compatprobe.Options) compatprobe.Options { o.Timeout = 0; return o },
		"timeout-sixty":    func(o compatprobe.Options) compatprobe.Options { o.Timeout = 60 * time.Second; return o },
		"url-https-path":   func(o compatprobe.Options) compatprobe.Options { o.URL = "https://example.test/deep/a.bin"; return o },
		"url-encoded-hash": func(o compatprobe.Options) compatprobe.Options { o.URL = "http://example.test/a%23b.bin"; return o },
		"url-semicolon":    func(o compatprobe.Options) compatprobe.Options { o.URL = "http://example.test/a;b.bin"; return o },
	}
	for name, mutate := range accept {
		if err := compatprobe.Validate(mutate(good)); err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

func TestRunNilContextRefusedGracefully(t *testing.T) {
	rep, err := compatprobe.Run(nil, compatprobe.Options{URL: "http://example.test/a.bin", Fixture: syntheticFixture(16)})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
	if rep.Planned != 18 || len(rep.Cases) != 18 || rep.Executed != 0 || rep.Passed {
		t.Fatalf("unexpected report shape: %+v", rep)
	}
	for _, c := range rep.Cases {
		if c.State != compatprobe.StateSkipped {
			t.Fatalf("case %q state = %q, want skipped", c.ID, c.State)
		}
	}
}

func TestRunAllCasesPassAndCountRequests(t *testing.T) {
	fixture := syntheticFixture(65)
	srv, url := newFrozenServer(t, fixture, nil)
	const token = "Bearer bin128-ok-token"
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{
		URL: url, Fixture: fixture, Authorization: token,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Passed || rep.Planned != 18 || rep.Executed != 18 || len(rep.Cases) != 18 {
		t.Fatalf("report: passed=%v planned=%d executed=%d cases=%d", rep.Passed, rep.Planned, rep.Executed, len(rep.Cases))
	}
	for i, c := range rep.Cases {
		if c.ID != wantIDs[i] {
			t.Fatalf("case %d id = %q, want %q", i, c.ID, wantIDs[i])
		}
		if c.State != compatprobe.StatePassed {
			t.Fatalf("case %q state = %q failures=%v", c.ID, c.State, c.Failures)
		}
		if c.HTTPStatus == 0 || c.SHA256 == "" {
			t.Fatalf("case %q missing status/sha256", c.ID)
		}
	}
	gets, heads, others, auth := srv.counts()
	if gets != 16 || heads != 2 || others != 0 {
		t.Fatalf("request counts gets=%d heads=%d others=%d, want 16/2/0", gets, heads, others)
	}
	if auth != 18 {
		t.Fatalf("authorization seen on %d requests, want 18", auth)
	}
	for _, v := range srv.authValues() {
		if v != token {
			t.Fatalf("authorization header forwarded as %q", v)
		}
	}
	// The report itself never carries the target URL or auth fields.
	blob, _ := json.Marshal(rep)
	if strings.Contains(string(blob), url) || strings.Contains(string(blob), token) {
		t.Fatal("report leaks target URL or credential")
	}
}

func TestRunEdgeFixtureSizes(t *testing.T) {
	for _, n := range []int{5, 65536} {
		fixture := syntheticFixture(n)
		_, url := newFrozenServer(t, fixture, nil)
		rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
		if err != nil || !rep.Passed {
			t.Fatalf("size %d: err=%v passed=%v", n, err, rep.Passed)
		}
	}
}

// TestRunInitialGetFailureSkipsRest: a rejected first representation leaves
// the other 17 cases explicitly unexecuted, never silently passing.
// 首个表示被拒绝时，其余 17 项显式未执行，绝不静默算作通过。
func TestRunInitialGetFailureSkipsRest(t *testing.T) {
	fixture := syntheticFixture(65)
	cases := []struct {
		name   string
		mutate func(string, *frozenPlan)
	}{
		{"status", func(id string, p *frozenPlan) {
			if id == "get" {
				p.status = 500
			}
		}},
		{"body", func(id string, p *frozenPlan) {
			if id == "get" {
				p.body = append(append([]byte{}, fixture...), 'Z')
			}
		}},
		{"missing-etag", func(id string, p *frozenPlan) {
			if id == "get" {
				p.headers["ETag"] = ""
			}
		}},
		{"wrong-checksum", func(id string, p *frozenPlan) {
			if id == "get" {
				p.headers["X-Checksum-Sha256"] = strings.Repeat("0", 64)
			}
		}},
		{"bad-date", func(id string, p *frozenPlan) {
			if id == "get" {
				p.headers["Last-Modified"] = "not-a-date"
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, url := newFrozenServer(t, fixture, tc.mutate)
			rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), url) {
				t.Fatal("error echoes target URL")
			}
			if rep.Passed || rep.Executed != 1 || len(rep.Cases) != 18 {
				t.Fatalf("passed=%v executed=%d", rep.Passed, rep.Executed)
			}
			if rep.Cases[0].State != compatprobe.StateFailed || len(rep.Cases[0].Failures) == 0 {
				t.Fatalf("get case: %+v", rep.Cases[0])
			}
			for _, c := range rep.Cases[1:] {
				if c.State != compatprobe.StateSkipped {
					t.Fatalf("case %q state = %q, want skipped", c.ID, c.State)
				}
			}
		})
	}
}

// TestRunCaseMutationsFailAndContinue: a single later-case violation is
// recorded as failed while the remaining bounded cases still execute.
// 单个后续用例违规记为失败，其余受控用例继续执行。
func TestRunCaseMutationsFailAndContinue(t *testing.T) {
	fixture := syntheticFixture(65)
	cases := []struct {
		name        string // Subtest name / 子测试名。
		id          string // Frozen case that must fail / 必须失败的用例。
		mutate      func(string, *frozenPlan)
		wantFailure string
	}{
		{"etag-any", "etag-any", func(id string, p *frozenPlan) {
			if id == "etag-any" {
				p.status = 304
			}
		}, "status: got 304 want 200"},
		{"etag-miss", "etag-miss", func(id string, p *frozenPlan) {
			if id == "etag-miss" {
				p.body = append([]byte{}, p.body...)
				p.body[0] ^= 0xff
			}
		}, "body: differs from fixture"},
		{"etag-weak", "etag-weak", func(id string, p *frozenPlan) {
			if id == "etag-weak" {
				p.status, p.body = 200, fixture
				p.headers["Content-Length"] = strconv.Itoa(len(fixture))
			}
		}, "status: got 200 want 304"},
		{"etag-match", "etag-match", func(id string, p *frozenPlan) {
			if id == "etag-match" {
				p.headers["ETag"] = ""
			}
		}, "etag: missing or differs"},
		{"modified-match", "modified-match", func(id string, p *frozenPlan) {
			if id == "modified-match" {
				p.status = 200
			}
		}, "status: got 200 want 304"},
		{"modified-old", "modified-old", func(id string, p *frozenPlan) {
			if id == "modified-old" {
				p.clOverride = strconv.Itoa(len(fixture) - 1)
			}
		}, "content-length:"},
		{"if-match-hit", "if-match-hit", func(id string, p *frozenPlan) {
			if id == "if-match-hit" {
				p.headers["X-Checksum-Sha1"] = strings.Repeat("f", 40)
			}
		}, "checksum-sha1: mismatch"},
		{"if-range-miss", "if-range-miss", func(id string, p *frozenPlan) {
			if id == "if-range-miss" {
				p.status = 206
			}
		}, "status: got 206 want 200"},
		{"conditional-precedence", "conditional-precedence", func(id string, p *frozenPlan) {
			if id == "conditional-precedence" {
				p.status = 304
			}
		}, "status: got 304 want 200"},
		{"head", "head", func(id string, p *frozenPlan) {
			if id == "head" {
				p.clOverride = "7"
			}
		}, "content-length:"},
		{"head-range", "head-range", func(id string, p *frozenPlan) {
			if id == "head-range" {
				p.status = 206
				p.headers["Content-Length"] = "4"
				p.headers["Content-Range"] = "bytes 0-3/65"
			}
		}, "status: got 206 want 200"},
		{"range-prefix", "range-prefix", func(id string, p *frozenPlan) {
			if id == "range-prefix" {
				p.body = []byte("XXXX")
			}
		}, "body: range bytes differ"},
		{"range-suffix", "range-suffix", func(id string, p *frozenPlan) {
			if id == "range-suffix" {
				p.headers["Content-Range"] = "bytes 0-3/65"
			}
		}, "content-range:"},
		{"range-open", "range-open", func(id string, p *frozenPlan) {
			if id == "range-open" {
				p.clOverride = "3"
			}
		}, "content-length:"},
		{"range-unsatisfiable", "range-unsatisfiable", func(id string, p *frozenPlan) {
			if id == "range-unsatisfiable" {
				p.headers["ETag"] = "unexpected"
			}
		}, "etag: must be absent on 416"},
		{"validator-drift", "etag-any", func(id string, p *frozenPlan) {
			if id == "etag-any" {
				p.headers["ETag"] = strings.Repeat("ab", 20)
			}
		}, "etag: missing or differs"},
		{"etag-case-sensitive", "etag-any", func(id string, p *frozenPlan) {
			if id == "etag-any" {
				// Same hex value, different case: must still fail.
				// 相同十六进制值仅大小写不同：仍须失败。
				p.headers["ETag"] = strings.ToUpper(p.headers["ETag"])
			}
		}, "etag: missing or differs"},
		{"content-type-missing", "etag-any", func(id string, p *frozenPlan) {
			if id == "etag-any" {
				p.headers["Content-Type"] = ""
			}
		}, "content-type: missing or not application/octet-stream"},
		{"accept-ranges-wrong", "head", func(id string, p *frozenPlan) {
			if id == "head" {
				p.headers["Accept-Ranges"] = "none"
			}
		}, "accept-ranges: missing or not bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, url := newFrozenServer(t, fixture, tc.mutate)
			rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
			if err == nil {
				t.Fatal("expected error for failing case")
			}
			if rep.Passed || rep.Executed != 18 || len(rep.Cases) != 18 {
				t.Fatalf("passed=%v executed=%d", rep.Passed, rep.Executed)
			}
			failed := byID(t, rep, tc.id)
			if failed.State != compatprobe.StateFailed {
				t.Fatalf("case %q state = %q", tc.id, failed.State)
			}
			if !strings.Contains(strings.Join(failed.Failures, "; "), tc.wantFailure) {
				t.Fatalf("case %q failures = %v, want %q", tc.id, failed.Failures, tc.wantFailure)
			}
			for _, c := range rep.Cases {
				if c.ID != tc.id && c.State != compatprobe.StatePassed {
					t.Fatalf("case %q unexpectedly %q: %v", c.ID, c.State, c.Failures)
				}
			}
		})
	}
}

func TestRunRedirectRefusedBeforeSecondHost(t *testing.T) {
	var hits int
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer trap.Close()
	fixture := syntheticFixture(65)
	srv, url := newFrozenServer(t, fixture, func(id string, p *frozenPlan) {
		if id == "get" {
			p.redirect = trap.URL + "/steal-credentials"
		}
	})
	_ = srv
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "" && strings.Contains(err.Error(), trap.URL) {
		t.Fatal("error echoes redirect target")
	}
	first := rep.Cases[0]
	if first.State != compatprobe.StateFailed ||
		!strings.Contains(strings.Join(first.Failures, "; "), "redirect:") {
		t.Fatalf("get case: %+v", first)
	}
	if rep.Executed != 1 || rep.Passed {
		t.Fatalf("executed=%d passed=%v", rep.Executed, rep.Passed)
	}
	if hits != 0 {
		t.Fatalf("redirect trap contacted %d times", hits)
	}
}

func TestRunBodyOverLimitRejected(t *testing.T) {
	fixture := syntheticFixture(65)
	_, url := newFrozenServer(t, fixture, func(id string, p *frozenPlan) {
		if id == "get" {
			p.body = make([]byte, compatprobe.MaxFixtureBytes+2048)
			p.headers["Content-Length"] = "" // chunked / 分块发送。
		}
	})
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
	if err == nil {
		t.Fatal("expected error")
	}
	first := rep.Cases[0]
	if !strings.Contains(strings.Join(first.Failures, "; "), "body-limit") {
		t.Fatalf("get failures: %v", first.Failures)
	}
	if rep.Executed != 1 {
		t.Fatalf("executed=%d, want 1", rep.Executed)
	}
}

func TestRunTruncatedBodyRejected(t *testing.T) {
	fixture := syntheticFixture(65)
	_, url := newFrozenServer(t, fixture, func(id string, p *frozenPlan) {
		if id == "get" {
			p.truncateBy = 64 // Declare 129 bytes, send 65 / 声明 129 字节只发 65。
		}
	})
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(strings.Join(rep.Cases[0].Failures, "; "), "body: read error") {
		t.Fatalf("get failures: %v", rep.Cases[0].Failures)
	}
	if rep.Executed != 1 {
		t.Fatalf("executed=%d, want 1", rep.Executed)
	}
}

func TestRunPerRequestTimeout(t *testing.T) {
	fixture := syntheticFixture(65)
	_, url := newFrozenServer(t, fixture, func(id string, p *frozenPlan) {
		if id == "get" {
			p.sleep = 800 * time.Millisecond
		}
	})
	started := time.Now()
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture, Timeout: 150 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("run took %s, per-request timeout not honored", elapsed)
	}
	if !strings.Contains(strings.Join(rep.Cases[0].Failures, "; "), "network: timeout") {
		t.Fatalf("get failures: %v", rep.Cases[0].Failures)
	}
	if rep.Executed != 1 {
		t.Fatalf("executed=%d, want 1", rep.Executed)
	}
}

func TestRunCanceledContext(t *testing.T) {
	fixture := syntheticFixture(65)
	_, url := newFrozenServer(t, fixture, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := compatprobe.Run(ctx, compatprobe.Options{URL: url, Fixture: fixture})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(strings.Join(rep.Cases[0].Failures, "; "), "network: canceled") {
		t.Fatalf("get failures: %v", rep.Cases[0].Failures)
	}
	// The canceled GET still counts as attempted; the 17 later cases skip.
	// 被取消的 GET 仍算执行；后续 17 项跳过。
	if rep.Executed != 1 || rep.Passed {
		t.Fatalf("executed=%d passed=%v", rep.Executed, rep.Passed)
	}
}

// TestRunProxyEnvironmentIgnored has limited power: the target is loopback,
// which ProxyFromEnvironment also bypasses, so it cannot distinguish an owned
// Transport with Proxy nil from default proxy lookup. It only pins the
// observable behavior "proxy trap never contacted". The real guarantee is the
// explicit Proxy: nil in the owned transport, enforced by source review.
// TestRunProxyEnvironmentIgnored 覆盖有限：目标是回环地址，ProxyFromEnvironment
// 同样会绕过回环，故无法区分 Proxy nil 与默认代理查找。它仅固定“未访问代理陷阱”
// 的可见行为；真正的保证来自专属传输里显式的 Proxy nil，以源码审阅为准。
func TestRunProxyEnvironmentIgnored(t *testing.T) {
	var proxyHits int
	proxyTrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxyTrap.Close()
	t.Setenv("HTTP_PROXY", proxyTrap.URL)
	t.Setenv("http_proxy", proxyTrap.URL)
	t.Setenv("HTTPS_PROXY", proxyTrap.URL)
	t.Setenv("https_proxy", proxyTrap.URL)
	fixture := syntheticFixture(65)
	_, url := newFrozenServer(t, fixture, nil)
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
	if err != nil || !rep.Passed {
		t.Fatalf("err=%v passed=%v", err, rep.Passed)
	}
	if proxyHits != 0 {
		t.Fatalf("proxy environment consulted (%d hits)", proxyHits)
	}
}

// TestRunRedactsReflectedCredential: a server echoing the Authorization
// value into allowlisted headers must never see it stored verbatim.
// 服务器回显凭据时，报告与失败文本必须抹除字面值。
func TestRunRedactsReflectedCredential(t *testing.T) {
	const secret = "Bearer bin128-reflect-secret"
	fixture := syntheticFixture(65)
	srv, url := newFrozenServer(t, fixture, nil)
	srv.setReflectAuth("X-Checksum-Sha256", "ETag")
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture, Authorization: secret})
	if err == nil {
		t.Fatal("expected checksum/etag mismatches to fail the run")
	}
	if rep.Passed {
		t.Fatal("reflected credential must not validate as checksum")
	}
	blob, _ := json.Marshal(rep)
	if strings.Contains(string(blob), secret) {
		t.Fatal("report carries reflected credential verbatim")
	}
	if !strings.Contains(string(blob), "[redacted]") {
		t.Fatal("report does not mark redaction")
	}
	for _, c := range rep.Cases {
		for _, f := range c.Failures {
			if strings.Contains(f, secret) {
				t.Fatalf("failure text leaks credential: %q", f)
			}
		}
	}
}

func TestRunErrorsNeverEchoTargetOrSecretInputs(t *testing.T) {
	fixture := syntheticFixture(65)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	url := ts.URL + "/gone.bin"
	ts.Close() // Refused connections from now on / 此后连接被拒。
	_, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
	if err == nil {
		t.Fatal("expected network error")
	}
	if strings.Contains(err.Error(), url) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error echoes target: %v", err)
	}
	badSecret := "http://example.test/a.bin?token=supersecret"
	err = compatprobe.Validate(compatprobe.Options{URL: badSecret, Fixture: fixture})
	if err == nil || strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("validate error leaks input: %v", err)
	}
}

// TestRunFrozenMismatchHeadersExact pins the exact conditional inputs of the
// frozen capture, notably the quoted "binflow-missing" mismatch value, so no
// future edit silently substitutes a derived hash.
// TestRunFrozenMismatchHeadersExact 固定抓包中的精确条件输入，尤其是带引号的
// "binflow-missing" 不匹配值，防止后续改动换成派生哈希。
func TestRunFrozenMismatchHeadersExact(t *testing.T) {
	fixture := syntheticFixture(65)
	srv, url := newFrozenServer(t, fixture, nil)
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{URL: url, Fixture: fixture})
	if err != nil || !rep.Passed {
		t.Fatalf("err=%v passed=%v", err, rep.Passed)
	}
	etag := srv.etag()
	missing := `"binflow-missing"`
	count := func(match func(reqSnap) bool) int {
		n := 0
		for _, q := range srv.requests() {
			if match(q) {
				n++
			}
		}
		return n
	}
	checks := map[string]int{
		"INM bare etag":   count(func(q reqSnap) bool { return q.method == "GET" && q.inm == etag }),
		"INM star":        count(func(q reqSnap) bool { return q.method == "GET" && q.inm == "*" }),
		"INM weak etag":   count(func(q reqSnap) bool { return q.method == "GET" && q.inm == "W/"+etag }),
		"INM quoted miss": count(func(q reqSnap) bool { return q.method == "GET" && q.ims == "" && q.inm == missing }),
		"INM miss + IMS":  count(func(q reqSnap) bool { return q.method == "GET" && q.inm == missing && q.ims == frozenDate }),
		"If-Match etag":   count(func(q reqSnap) bool { return q.method == "GET" && q.im == etag }),
		"If-Match miss":   count(func(q reqSnap) bool { return q.method == "GET" && q.im == missing }),
		"If-Range etag":   count(func(q reqSnap) bool { return q.ifr == etag && q.rng == "bytes=0-3" }),
		"If-Range miss":   count(func(q reqSnap) bool { return q.ifr == missing && q.rng == "bytes=0-3" }),
		"IMS exact date":  count(func(q reqSnap) bool { return q.method == "GET" && q.ims == frozenDate && q.inm == "" }),
		"IMS epoch":       count(func(q reqSnap) bool { return q.method == "GET" && q.ims == "Thu, 01 Jan 1970 00:00:00 GMT" }),
		"HEAD plain":      count(func(q reqSnap) bool { return q.method == "HEAD" && q.rng == "" }),
		"HEAD with Range": count(func(q reqSnap) bool { return q.method == "HEAD" && q.rng == "bytes=0-3" }),
		"Range unsat N-":  count(func(q reqSnap) bool { return q.rng == "bytes=65-" }),
		"Range suffix -4": count(func(q reqSnap) bool { return q.rng == "bytes=-4" }),
		"Range open 4-":   count(func(q reqSnap) bool { return q.rng == "bytes=4-" }),
	}
	for name, want := range checks {
		if want < 1 {
			t.Errorf("%s: no request observed", name)
		}
	}
	if got := checks["INM quoted miss"]; got != 1 {
		t.Fatalf("quoted mismatch If-None-Match seen %d times, want exactly 1 (etag-miss)", got)
	}
	if got := checks["INM miss + IMS"]; got != 1 {
		t.Fatalf("quoted mismatch precedence pair seen %d times, want exactly 1", got)
	}
	if got := checks["If-Match miss"]; got != 1 {
		t.Fatalf("quoted mismatch If-Match seen %d times, want exactly 1", got)
	}
	if got := checks["If-Range miss"]; got != 1 {
		t.Fatalf("quoted mismatch If-Range seen %d times, want exactly 1", got)
	}
}

// TestRunReflectedTokenAndURLRedacted: reflecting the bare token (without
// scheme) or the literal target URL must also be scrubbed from reports.
// 单独回显裸令牌（无方案前缀）或字面目标 URL 时同样必须抹除。
func TestRunReflectedTokenAndURLRedacted(t *testing.T) {
	const token = "bin128-fake-token-only"
	fixture := syntheticFixture(65)
	srv, url := newFrozenServer(t, fixture, nil)
	srv.mu.Lock()
	srv.reflectTokenTo = "X-Checksum-Sha256"
	srv.reflectURLTo = "Content-Type"
	srv.mu.Unlock()
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{
		URL: url, Fixture: fixture, Authorization: "Bearer " + token,
	})
	if err == nil || rep.Passed {
		t.Fatal("reflected values must fail validation")
	}
	blob, _ := json.Marshal(rep)
	for _, secret := range []string{token, "Bearer " + token, url} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("report leaks %q", secret)
		}
	}
	if !strings.Contains(string(blob), "[redacted]") {
		t.Fatal("report lacks redaction marker")
	}
}

// TestRunClosesIdleConnections: every exit path must drop the owned
// transport's keep-alive sockets so repeated runs cannot accumulate them.
// 每条退出路径都须释放专属传输的空闲连接，避免多次运行累积套接字。
func TestRunClosesIdleConnections(t *testing.T) {
	fixture := syntheticFixture(65)
	var open atomic.Int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Config.ConnState = func(c net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			open.Add(1)
		case http.StateClosed:
			open.Add(-1)
		}
	}
	ts.Start()
	defer ts.Close()
	// A run against a 404 server fails fast on assertions and returns early.
	_, err := compatprobe.Run(context.Background(), compatprobe.Options{
		URL: ts.URL + "/missing.bin", Fixture: fixture,
	})
	if err == nil {
		t.Fatal("expected assertion failure")
	}
	deadline := time.Now().Add(3 * time.Second)
	for open.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := open.Load(); n != 0 {
		t.Fatalf("%d idle connection(s) left open after Run returned", n)
	}
}

// TestRunShortTokenDoesNotCorruptWireValidators: a legal short synthetic
// token ("a") overlaps the fixture SHA-1 hex, so the report copy of the
// first GET's ETag is redacted — but the wire validators for the 17 later
// cases must stay the original raw SHA-1 and date. Redacted report copies
// are never used as an oracle.
// 合法的超短令牌 "a" 与制品 SHA-1 十六进制重叠，报告副本中的 ETag 会被抹除；
// 但后续 17 个用例的线上 validator 必须仍是原始 SHA-1 与日期，绝不用抹除副本当 oracle。
func TestRunShortTokenDoesNotCorruptWireValidators(t *testing.T) {
	fixture := syntheticFixture(65)
	rawSHA1 := func(b []byte) string {
		sum := sha1.Sum(b)
		return hex.EncodeToString(sum[:])
	}
	// Pick a deterministic size whose SHA-1 hex really contains "a" so the
	// overlap precondition of this regression holds.
	// 选一个 SHA-1 十六进制确实含 "a" 的确定性长度，确保回归前提成立。
	for len(rawSHA1(fixture)) > 0 && !strings.Contains(rawSHA1(fixture), "a") {
		fixture = syntheticFixture(len(fixture) + 1)
	}
	etag := rawSHA1(fixture)
	if !strings.Contains(etag, "a") {
		t.Fatal("could not build overlapping fixture")
	}
	srv, url := newFrozenServer(t, fixture, nil)
	rep, err := compatprobe.Run(context.Background(), compatprobe.Options{
		URL: url, Fixture: fixture, Authorization: "Bearer a",
	})
	if err != nil || !rep.Passed || rep.Executed != 18 {
		t.Fatalf("err=%v passed=%v executed=%d; later cases must pass against a valid server",
			err, rep.Passed, rep.Executed)
	}
	// Wire requests must carry the original validators, never redacted text.
	// 线上请求必须携带原始 validator，绝不携带抹除文本。
	for _, q := range srv.requests() {
		for _, v := range []string{q.inm, q.im, q.ifr} {
			if strings.Contains(v, "[redacted]") {
				t.Fatal("redacted literal sent on the wire")
			}
		}
		if q.inm != "" && q.inm != "*" && !strings.HasPrefix(q.inm, "W/") && q.inm != `"binflow-missing"` && q.inm != etag {
			t.Fatalf("If-None-Match sent %q, want the raw first-GET etag", q.inm)
		}
	}
	// The report copy of the first GET's ETag IS redacted (overlap exists).
	// 报告副本中的 ETag 确实被抹除（重叠存在），仅用于展示。
	first := byID(t, rep, "get")
	if !strings.Contains(first.Headers["ETag"], "[redacted]") {
		t.Fatalf("expected redacted report ETag, got %q", first.Headers["ETag"])
	}
	// Reflected unique secrets stay redacted (regression guard for fix).
	// 回显的唯一敏感值仍被抹除（回归护栏）。
	const secret = "bin128-unique-long-token"
	srv2, url2 := newFrozenServer(t, fixture, nil)
	srv2.mu.Lock()
	srv2.reflectTokenTo = "X-Checksum-Sha256"
	srv2.mu.Unlock()
	rep2, err2 := compatprobe.Run(context.Background(), compatprobe.Options{
		URL: url2, Fixture: fixture, Authorization: "Bearer " + secret,
	})
	if err2 == nil || rep2.Passed {
		t.Fatal("reflected token must fail the run")
	}
	blob, _ := json.Marshal(rep2)
	if strings.Contains(string(blob), secret) {
		t.Fatal("reflected unique token leaked into report")
	}
}

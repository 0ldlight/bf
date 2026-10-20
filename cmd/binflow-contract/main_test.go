package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/0ldlight/bf/internal/compatprobe"
)

const cliFrozenDate = "Wed, 14 Jan 2026 09:00:00 GMT"

// syntheticFixture mirrors the core tests' self-authored bytes; no raw
// capture or real credential is read anywhere in this file.
// syntheticFixture 为本文件自创字节；不读取任何原始抓包或真实凭据。
func syntheticFixture(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return b
}

// cliTarget is an independent implementation of the frozen 18-case profile
// used only through the CLI surface. It deliberately repeats the frozen
// semantics (including star If-None-Match 200 and unmatched If-Match 200)
// instead of importing test helpers or delegating to ServeContent.
// cliTarget 是独立的冻结 18 用例实现，仅经 CLI 表面使用，不复用核心测试助手。
type cliTarget struct {
	fixture []byte
	hits    atomic.Int32
	auth    atomic.Pointer[string]
}

func (c *cliTarget) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.hits.Add(1)
	if v := r.Header.Get("Authorization"); v != "" {
		c.auth.Store(&v)
	}
	f := c.fixture
	n := len(f)
	s1, s256 := sha1.Sum(f), sha256.Sum256(f)
	etag := hex.EncodeToString(s1[:])
	common := func(extra map[string]string) map[string]string {
		h := map[string]string{
			"ETag": etag, "Last-Modified": cliFrozenDate,
			"X-Checksum-Sha1": etag, "X-Checksum-Sha256": hex.EncodeToString(s256[:]),
			"Accept-Ranges": "bytes", "Content-Type": "application/octet-stream",
		}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}
	inm := r.Header.Get("If-None-Match")
	ims := r.Header.Get("If-Modified-Since")
	im := r.Header.Get("If-Match")
	ifr := r.Header.Get("If-Range")
	rng := r.Header.Get("Range")
	write := func(status int, body []byte, headers map[string]string) {
		for k, v := range headers {
			if v != "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			w.Write(body)
		}
	}
	switch {
	case r.Method == http.MethodHead:
		write(200, nil, common(map[string]string{"Content-Length": strconv.Itoa(n)}))
	case inm == "*" || (inm != "" && inm != etag && inm != "W/"+etag):
		write(200, f, common(map[string]string{"Content-Length": strconv.Itoa(n)}))
	case inm == etag || inm == "W/"+etag || ims == cliFrozenDate:
		write(304, nil, map[string]string{"ETag": etag, "Last-Modified": cliFrozenDate})
	case im != "":
		write(200, f, common(map[string]string{"Content-Length": strconv.Itoa(n)}))
	case ifr != "" && ifr != etag:
		write(200, f, common(map[string]string{"Content-Length": strconv.Itoa(n)}))
	case rng == fmt.Sprintf("bytes=%d-", n):
		write(416, nil, map[string]string{"Content-Range": fmt.Sprintf("bytes */%d", n), "Content-Length": "0"})
	case rng == "bytes=0-3":
		write(206, f[0:4], common(map[string]string{"Content-Length": "4", "Content-Range": fmt.Sprintf("bytes 0-3/%d", n)}))
	case rng == "bytes=-4":
		write(206, f[n-4:], common(map[string]string{"Content-Length": "4", "Content-Range": fmt.Sprintf("bytes %d-%d/%d", n-4, n-1, n)}))
	case rng == "bytes=4-":
		write(206, f[4:], common(map[string]string{"Content-Length": strconv.Itoa(n - 4), "Content-Range": fmt.Sprintf("bytes 4-%d/%d", n-1, n)}))
	default:
		write(200, f, common(map[string]string{"Content-Length": strconv.Itoa(n)}))
	}
}

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// runCLI captures run() output for assertions / runCLI 捕获 run 输出便于断言。
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRunHelpAndArgumentErrors(t *testing.T) {
	dir := t.TempDir()
	fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(16)), 0o600)
	report := filepath.Join(dir, "r.json")
	cases := []struct {
		name string
		args []string
	}{
		{"help", []string{"-h"}},
		{"missing-all", []string{}},
		{"missing-url", []string{"-fixture-file", fixture, "-report", report}},
		{"missing-report", []string{"-url", "http://example.test/a.bin", "-fixture-file", fixture}},
		{"unknown-flag", []string{"-proxy", "socks5://x", "-url", "http://example.test/a.bin", "-fixture-file", fixture, "-report", report}},
		{"positional", []string{"extra", "-url", "http://example.test/a.bin", "-fixture-file", fixture, "-report", report}},
		{"bad-duration", []string{"-timeout=soon", "-url", "http://example.test/a.bin", "-fixture-file", fixture, "-report", report}},
		{"timeout-zero", []string{"-timeout=0s", "-url", "http://example.test/a.bin", "-fixture-file", fixture, "-report", report}},
		{"timeout-over-max", []string{"-timeout=61s", "-url", "http://example.test/a.bin", "-fixture-file", fixture, "-report", report}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(t, tc.args...)
			if tc.name == "help" {
				if code != 0 || !strings.Contains(stdout, "binflow-contract:") {
					t.Fatalf("help: code=%d stdout=%q", code, stdout)
				}
				return
			}
			if code != 2 {
				t.Fatalf("code=%d, want 2", code)
			}
			for _, banned := range []string{fixture, report, "example.test", "/tmp"} {
				if strings.Contains(stdout+stderr, banned) {
					t.Fatalf("output interpolates %q: %q %q", banned, stdout, stderr)
				}
			}
		})
	}
}

func TestRunValidProbeEndToEnd(t *testing.T) {
	target := &cliTarget{fixture: syntheticFixture(65)}
	ts := httptest.NewServer(target)
	defer ts.Close()
	dir := t.TempDir()
	fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(65)), 0o600)
	auth := writeFile(t, dir, "auth.txt", "Bearer fake-test-bearer-token\n", 0o600)
	report := filepath.Join(dir, "report.json")
	code, stdout, stderr := runCLI(t,
		"-url", ts.URL+"/generic/a.bin", "-fixture-file", fixture,
		"-authorization-file", auth, "-report", report)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if !strings.Contains(stdout, "passed 18 failed 0 skipped 0") {
		t.Fatalf("stdout counts: %q", stdout)
	}
	for _, banned := range []string{ts.URL, "fake-test-bearer-token", fixture, auth} {
		if strings.Contains(stdout+stderr, banned) {
			t.Fatalf("output leaks %q", banned)
		}
	}
	if target.hits.Load() != 18 {
		t.Fatalf("target hits=%d, want 18", target.hits.Load())
	}
	st, err := os.Stat(report)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("report mode=%o, want 600", perm)
	}
	blob, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var rep compatprobe.Report
	if err := json.Unmarshal(blob, &rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Passed || rep.Executed != 18 || len(rep.Cases) != 18 {
		t.Fatalf("report: passed=%v executed=%d cases=%d", rep.Passed, rep.Executed, len(rep.Cases))
	}
	if v := target.auth.Load(); v == nil || *v != "Bearer fake-test-bearer-token" {
		t.Fatal("authorization header not forwarded to target")
	}
}

func TestRunFixtureBoundaries(t *testing.T) {
	ts := httptest.NewServer(&cliTarget{fixture: syntheticFixture(16)})
	defer ts.Close()
	for _, size := range []int{4, 65537} {
		dir := t.TempDir()
		fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(size)), 0o600)
		report := filepath.Join(dir, "r.json")
		code, _, stderr := runCLI(t, "-url", ts.URL+"/a.bin", "-fixture-file", fixture, "-report", report)
		if code != 2 {
			t.Fatalf("size %d: code=%d stderr=%q", size, code, stderr)
		}
		if _, err := os.Stat(report); !os.IsNotExist(err) {
			t.Fatalf("size %d: report created before validation", size)
		}
	}
}

func TestRunFixtureRejectsNonRegularInputs(t *testing.T) {
	ts := httptest.NewServer(&cliTarget{fixture: syntheticFixture(16)})
	defer ts.Close()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	linked := filepath.Join(dir, "real.bin")
	writeFile(t, dir, "real.bin", string(syntheticFixture(16)), 0o600)
	symlink := filepath.Join(dir, "link.bin")
	if err := os.Symlink(linked, symlink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	cases := []struct {
		name string
		path string
	}{{"fifo", fifo}, {"symlink", symlink}, {"directory", dir}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := filepath.Join(dir, "r-"+tc.name+".json")
			code, _, stderr := runCLI(t, "-url", ts.URL+"/a.bin", "-fixture-file", tc.path, "-report", report)
			if code != 2 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			if strings.Contains(stderr, tc.path) {
				t.Fatalf("stderr interpolates path: %q", stderr)
			}
		})
	}
}

func TestRunAuthFileValidation(t *testing.T) {
	target := &cliTarget{fixture: syntheticFixture(16)}
	ts := httptest.NewServer(target)
	defer ts.Close()
	dir := t.TempDir()
	base := []string{"-url", ts.URL + "/a.bin"}
	ok := func(name, content string, mode os.FileMode) string {
		return writeFile(t, dir, name, content, mode)
	}
	cases := []struct {
		name    string
		path    string
		wantTwo bool // true: expect rejection exit 2 / true 表示期望退出码 2。
	}{
		{"mode-0600", ok("a1.txt", "Bearer fake-token-one\n", 0o600), false},
		{"mode-0400", ok("a2.txt", "Bearer fake-token-two", 0o400), false},
		{"mode-0644", ok("a3.txt", "Bearer fake-token-three\n", 0o644), true},
		{"mode-0660", ok("a4.txt", "Bearer fake-token-four\n", 0o660), true},
		{"crlf", ok("a5.txt", "Bearer fake-token-five\r\n", 0o600), true},
		{"two-lines", ok("a6.txt", "Bearer fake-token-six\nBearer other\n", 0o600), true},
		{"space-token", ok("a7.txt", "Bearer fake token seven\n", 0o600), true},
		{"leading-space", ok("a8.txt", " Bearer fake-token-eight\n", 0o600), true},
		{"oversize", ok("a9.txt", "Bearer "+strings.Repeat("f", 4100)+"\n", 0o600), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := filepath.Join(dir, "r-"+tc.name+".json")
			args := append(append([]string{}, base...),
				"-fixture-file", writeFile(t, dir, "fix-"+tc.name+".bin", string(syntheticFixture(16)), 0o600),
				"-authorization-file", tc.path, "-report", rep)
			code, _, stderr := runCLI(t, args...)
			if tc.wantTwo && code != 2 {
				t.Fatalf("code=%d stderr=%q, want 2", code, stderr)
			}
			if !tc.wantTwo && code != 0 {
				t.Fatalf("code=%d stderr=%q, want 0", code, stderr)
			}
		})
	}
	// Symlinked auth file / 符号链接的凭据文件。
	real := ok("real-auth.txt", "Bearer fake-real\n", 0o600)
	link := filepath.Join(dir, "auth-link.txt")
	if err := os.Symlink(real, link); err == nil {
		code, _, _ := runCLI(t, "-url", ts.URL+"/a.bin",
			"-fixture-file", writeFile(t, dir, "fix-l.bin", string(syntheticFixture(16)), 0o600),
			"-authorization-file", link, "-report", filepath.Join(dir, "rl.json"))
		if code != 2 {
			t.Fatalf("symlinked auth accepted: code=%d", code)
		}
	}
	// Inside a .git-carrying tree / 位于含 .git 的目录树。
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(repo, "auth-inside.txt")
	if err := os.WriteFile(inside, []byte("Bearer fake-inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runCLI(t, "-url", ts.URL+"/a.bin",
		"-fixture-file", writeFile(t, dir, "fix-g.bin", string(syntheticFixture(16)), 0o600),
		"-authorization-file", inside, "-report", filepath.Join(dir, "rg.json"))
	if code != 2 {
		t.Fatalf("in-repo auth accepted: code=%d", code)
	}
}

func TestRunReportReservedExclusivelyBeforeNetwork(t *testing.T) {
	target := &cliTarget{fixture: syntheticFixture(16)}
	ts := httptest.NewServer(target)
	defer ts.Close()
	dir := t.TempDir()
	fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(16)), 0o600)
	existing := writeFile(t, dir, "existing.json", "keep", 0o600)
	linkReport := filepath.Join(dir, "link.json")
	if err := os.Symlink(existing, linkReport); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, report := range []string{existing, linkReport} {
		code, _, stderr := runCLI(t, "-url", ts.URL+"/a.bin", "-fixture-file", fixture, "-report", report)
		if code != 2 {
			t.Fatalf("report %s accepted: code=%d stderr=%q", filepath.Base(report), code, stderr)
		}
	}
	if target.hits.Load() != 0 {
		t.Fatalf("network contacted before report reservation: %d hits", target.hits.Load())
	}
	if blob, _ := os.ReadFile(existing); string(blob) != "keep" {
		t.Fatalf("existing report mutated: %q", blob)
	}
}

func TestRunNetworkFailurePersistsStructuredReport(t *testing.T) {
	ts := httptest.NewServer(&cliTarget{fixture: syntheticFixture(16)})
	url := ts.URL + "/gone.bin"
	ts.Close() // All later connections fail / 此后连接全部失败。
	dir := t.TempDir()
	fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(16)), 0o600)
	report := filepath.Join(dir, "r.json")
	code, stdout, stderr := runCLI(t, "-url", url, "-fixture-file", fixture, "-report", report)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	for _, banned := range []string{url, "127.0.0.1", fixture} {
		if strings.Contains(stdout+stderr, banned) {
			t.Fatalf("output leaks %q: %q %q", banned, stdout, stderr)
		}
	}
	blob, err := os.ReadFile(report)
	if err != nil {
		t.Fatal("failed run did not persist report")
	}
	var rep compatprobe.Report
	if err := json.Unmarshal(blob, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Passed || rep.Executed != 1 || len(rep.Cases) != 18 || rep.Cases[0].State != compatprobe.StateFailed {
		t.Fatalf("report: passed=%v executed=%d", rep.Passed, rep.Executed)
	}
	if !strings.Contains(stdout, "skipped 17") {
		t.Fatalf("stdout counts missing skips: %q", stdout)
	}
	if strings.Contains(string(blob), url) || strings.Contains(string(blob), "127.0.0.1") {
		t.Fatal("report leaks target")
	}
}

// TestRunSecretsNeverReflected: a hostile target echoing the credential,
// bare token or URL into allowlisted headers must not get them stored.
// 恶意目标回显凭据、裸令牌或 URL 时，任何输出都不得出现原文。
func TestRunSecretsNeverReflected(t *testing.T) {
	const token = "Bearer fake-reflect-cli-token"
	target := &cliTarget{fixture: syntheticFixture(16)}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", r.Header.Get("Authorization"))
		w.Header().Set("X-Checksum-Sha256", strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "http://"+r.Host+r.URL.String())
		w.WriteHeader(http.StatusOK)
		w.Write(target.fixture)
	}))
	defer ts.Close()
	dir := t.TempDir()
	fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(16)), 0o600)
	auth := writeFile(t, dir, "auth.txt", token+"\n", 0o600)
	report := filepath.Join(dir, "r.json")
	code, stdout, stderr := runCLI(t, "-url", ts.URL+"/a.bin", "-fixture-file", fixture,
		"-authorization-file", auth, "-report", report)
	if code != 1 {
		t.Fatalf("code=%d, want 1 (assertion failures expected)", code)
	}
	blob, _ := os.ReadFile(report)
	for _, secret := range []string{token, strings.TrimPrefix(token, "Bearer "), ts.URL} {
		if strings.Contains(stdout+stderr+string(blob), secret) {
			t.Fatalf("leak of %q in outputs or report", secret)
		}
	}
	if !strings.Contains(string(blob), "[redacted]") {
		t.Fatal("report lacks redaction marker")
	}
}

// TestRunRejectedAuthInputsNeverContactNetwork: an explicitly supplied but
// invalid authorization input (empty value, lone LF, mode 0700) must be
// refused before any HTTP request and before report reservation.
// 显式提供但非法的凭据输入（空值、单个 LF、0700 模式）必须在任何 HTTP 请求与
// 报告预约之前被拒绝。
func TestRunRejectedAuthInputsNeverContactNetwork(t *testing.T) {
	target := &cliTarget{fixture: syntheticFixture(16)}
	ts := httptest.NewServer(target)
	defer ts.Close()
	dir := t.TempDir()
	fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(16)), 0o600)
	empty := writeFile(t, dir, "empty.txt", "", 0o600)
	lfOnly := writeFile(t, dir, "lf.txt", "\n", 0o600)
	execBit := writeFile(t, dir, "exec.txt", "Bearer fake-exec\n", 0o700)
	for name, auth := range map[string]string{
		"empty-file": empty,
		"lf-only":    lfOnly,
		"mode-0700":  execBit,
	} {
		t.Run(name, func(t *testing.T) {
			report := filepath.Join(dir, "r-"+name+".json")
			code, _, stderr := runCLI(t, "-url", ts.URL+"/a.bin", "-fixture-file", fixture,
				"-authorization-file", auth, "-report", report)
			if code != 2 {
				t.Fatalf("code=%d stderr=%q, want 2", code, stderr)
			}
			if target.hits.Load() != 0 {
				t.Fatalf("network contacted: %d hits", target.hits.Load())
			}
			if _, err := os.Stat(report); !os.IsNotExist(err) {
				t.Fatalf("report created for rejected input")
			}
		})
	}
}

// TestRunRepositoryAncestry: the auth file must live outside repository
// trees no matter whether the path reaches them directly, through a worktree
// .git file marker, or through a symlink alias into a nested directory.
// 凭据文件必须位于仓库树之外：直接路径、worktree .git 文件标记、或经符号链接
// 别名进入嵌套目录均不允许。
func TestRunRepositoryAncestry(t *testing.T) {
	target := &cliTarget{fixture: syntheticFixture(16)}
	ts := httptest.NewServer(target)
	defer ts.Close()
	dir := t.TempDir()
	fixture := writeFile(t, dir, "fix.bin", string(syntheticFixture(16)), 0o600)
	reportSeq := 0
	runAuth := func(auth string) int {
		t.Helper()
		reportSeq++
		code, _, _ := runCLI(t, "-url", ts.URL+"/a.bin", "-fixture-file", fixture,
			"-authorization-file", auth, "-report", filepath.Join(dir, fmt.Sprintf("r-%d.json", reportSeq)))
		return code
	}
	// Plain checkout with a nested auth file / 普通检出内的嵌套凭据文件。
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(checkout, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(checkout, "nested", "auth0600")
	if err := os.WriteFile(inside, []byte("Bearer fake-inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runAuth(inside); code != 2 {
		t.Fatalf("direct in-repo auth accepted: code=%d", code)
	}
	// Worktree marker: .git is a file / worktree 标记：.git 是文件。
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	wtAuth := filepath.Join(worktree, "auth0600")
	if err := os.WriteFile(wtAuth, []byte("Bearer fake-worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runAuth(wtAuth); code != 2 {
		t.Fatalf("worktree-marker auth accepted: code=%d", code)
	}
	// Outside symlink alias into the nested checkout directory / 指向嵌套目录的外部别名。
	alias := filepath.Join(t.TempDir(), "outside-alias")
	if err := os.Symlink(filepath.Join(checkout, "nested"), alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	aliasAuth := filepath.Join(alias, "auth0600")
	if code := runAuth(aliasAuth); code != 2 {
		t.Fatalf("symlink alias into repo accepted: code=%d", code)
	}
	// Genuinely outside regular file still works / 真正外部普通文件仍可用。
	outside := writeFile(t, dir, "outside-auth.txt", "Bearer fake-outside\n", 0o600)
	if code := runAuth(outside); code != 0 {
		t.Fatalf("valid outside auth rejected: code=%d", code)
	}
	if target.hits.Load() != 18 {
		t.Fatalf("hits=%d after accepted run, want 18", target.hits.Load())
	}
}

// TestFinishReportsProbeAndWriteFailuresIndependently: when the probe and
// the report write both fail, both static categories appear.
// 探针与报告写入同时失败时，两个静态类别都要出现。
func TestFinishReportsProbeAndWriteFailuresIndependently(t *testing.T) {
	report := compatprobe.Report{Profile: compatprobe.Profile, Planned: compatprobe.CaseCount, Cases: make([]compatprobe.CaseResult, 18)}
	var stdout, stderr strings.Builder
	code := finish(&stdout, &stderr, report,
		errors.New("compatprobe: assert: one or more cases failed"),
		errors.New("write failed"), nil)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "probe failed") {
		t.Fatalf("stderr missing probe failure: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "report write failed") {
		t.Fatalf("stderr missing write failure: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "planned 18") {
		t.Fatalf("stdout missing counts: %q", stdout.String())
	}
}

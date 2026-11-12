package security

// 本文件覆盖下载入口剩余未测行为：构造错误的固定文案表示、启动期各次
// ctx 检查、默认 Report 的仅类别日志、请求取消/认证取消/目录取消的
// ErrAbortHandler、非 UTF-8 凭据、入口仓库键解析边界、授权桥守卫、失败
// 门对已阻断条目的防御性保持，以及凭据分段长度拆分拒绝。
// This file covers the remaining untested download-entry behaviors: the
// fixed text of constructor errors, each startup ctx check, the
// category-only default Report logging, the ErrAbortHandler panics for
// request/authentication/catalog cancellation, non-UTF-8 credentials, the
// entry repository-key parsing bounds, the authorization bridge guards, the
// failure gate's defensive retention of blocked entries, and the credential
// segment-length split rejection.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/repository"
)

// egValidateCancel 在 Validate 期间取消 ctx 的后端包装。
// egValidateCancel wraps the backend canceling ctx inside Validate.
type egValidateCancel struct {
	*suBackend
	cancel context.CancelFunc
}

func (b *egValidateCancel) Validate(ctx context.Context) error {
	b.cancel()
	return b.suBackend.Validate(ctx)
}

// TestSecurityEntryConstructorContextChecks 断言构造失败的固定文案表示
// 与启动期各次 ctx 检查：入口预取消、Validate 后取消与目录查询后取消都
// 返回包装 ErrConfiguration 与 context.Canceled 的阶段错误。
// TestSecurityEntryConstructorContextChecks asserts the fixed constructor
// error text plus each startup ctx check: precancelled entry, post-Validate
// and post-catalog cancellations all return the stage error wrapping both
// ErrConfiguration and context.Canceled.
func TestSecurityEntryConstructorContextChecks(t *testing.T) {
	t.Run("precanceled context text", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h, err := NewAuthenticatedDownload(ctx, suDefaultConfig(), suDefaultBackend(), &suCounterStore{}, &suCounterOpener{})
		if h != nil {
			t.Fatal("precanceled ctx returned a handler")
		}
		if !errors.Is(err, ErrConfiguration) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want ErrConfiguration and Canceled", err)
		}
		if want := ErrConfiguration.Error() + ": " + entryStageValidate; err.Error() != want {
			t.Fatalf("error text = %q, want %q", err.Error(), want)
		}
	})

	t.Run("cancellation after validate", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		backend := &egValidateCancel{suBackend: suDefaultBackend(), cancel: cancel}
		h, err := NewAuthenticatedDownload(ctx, suDefaultConfig(), backend, &suCounterStore{}, &suCounterOpener{})
		if h != nil {
			t.Fatal("canceled startup returned a handler")
		}
		if !errors.Is(err, ErrConfiguration) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want ErrConfiguration and Canceled", err)
		}
		if want := ErrConfiguration.Error() + ": " + entryStageValidate; err.Error() != want {
			t.Fatalf("error text = %q, want %q", err.Error(), want)
		}
	})

	t.Run("cancellation after catalog", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		backend := suDefaultBackend()
		backend.lookupFn = func(string) (RepositoryInfo, error) {
			cancel()
			return RepositoryInfo{Key: suRepoKey, Kind: "local", PackageType: "generic", Revision: 4}, nil
		}
		h, err := NewAuthenticatedDownload(ctx, suDefaultConfig(), backend, &suCounterStore{}, &suCounterOpener{})
		if h != nil {
			t.Fatal("canceled startup returned a handler")
		}
		if !errors.Is(err, ErrConfiguration) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want ErrConfiguration and Canceled", err)
		}
		if want := ErrConfiguration.Error() + ": " + entryStageCatalog; err.Error() != want {
			t.Fatalf("error text = %q, want %q", err.Error(), want)
		}
	})
}

// TestSecurityEntryDefaultReportLogsCategoryOnly 断言未配置 Report 时默认
// 报告只输出固定类别行：无请求路径、无用户名、无内部错误。
// TestSecurityEntryDefaultReportLogsCategoryOnly asserts the default report
// (with Report unset) logs exactly the fixed category line: no request path,
// no username and no internal error.
func TestSecurityEntryDefaultReportLogsCategoryOnly(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	backend := suDefaultBackend()
	backend.authErr = errors.New("internal-kaboom")
	h, err := NewAuthenticatedDownload(context.Background(), suDefaultConfig(), backend, &suCounterStore{}, &suCounterOpener{})
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", nil)
	r.SetBasicAuth("attacker-name", "guess")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	want := "security: download failure: " + string(httpserver.DownloadFailureAuth) + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("default report log = %q, want %q", got, want)
	}
}

// TestSecurityEntryCancellationPanics 断言三类取消路径都以 ErrAbortHandler
// 中止：进入前已取消的请求、认证失败伴随取消、目录查询期间取消。
// TestSecurityEntryCancellationPanics asserts all three cancellation paths
// abort with ErrAbortHandler: an already canceled request on entry,
// authentication failure accompanied by cancellation, and cancellation during
// the catalog lookup.
func TestSecurityEntryCancellationPanics(t *testing.T) {
	expectAbort := func(t *testing.T, name string, serve func()) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("%s: no panic", name)
			}
			if r != http.ErrAbortHandler {
				t.Fatalf("%s: panic value = %v, want http.ErrAbortHandler", name, r)
			}
		}()
		serve()
		t.Fatalf("%s: no panic during serve", name)
	}

	t.Run("precanceled request", func(t *testing.T) {
		e := suNewDefault(t)
		r := httptest.NewRequest(http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", nil)
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		expectAbort(t, "precanceled", func() { e.handler.ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx)) })
	})

	t.Run("authentication failure with cancellation", func(t *testing.T) {
		e := suNewDefault(t)
		r := httptest.NewRequest(http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", nil)
		r.SetBasicAuth(suUser, suPassword)
		ctx, cancel := context.WithCancel(r.Context())
		e.backend.authFn = func(string, string) (Principal, error) {
			cancel()
			return Principal{}, errors.New("canceled-auth-boom")
		}
		expectAbort(t, "auth-cancel", func() { e.handler.ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx)) })
	})

	t.Run("catalog lookup cancellation", func(t *testing.T) {
		e := suNewDefault(t)
		r := httptest.NewRequest(http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", nil)
		r.SetBasicAuth(suUser, suPassword)
		ctx, cancel := context.WithCancel(r.Context())
		r = r.WithContext(ctx)
		lookups := 0
		e.backend.lookupFn = func(string) (RepositoryInfo, error) {
			lookups++
			if lookups >= 1 {
				cancel()
			}
			return RepositoryInfo{Key: suRepoKey, Kind: "local", PackageType: "generic", Revision: 4}, nil
		}
		expectAbort(t, "catalog-cancel", func() { e.handler.ServeHTTP(httptest.NewRecorder(), r) })
	})
}

// TestSecurityEntryInvalidUTF8CredentialRejected 断言 Basic 解码出的非
// UTF-8 凭据按格式拒绝：401 + challenge，后端零调用。
// TestSecurityEntryInvalidUTF8CredentialRejected asserts a Basic credential
// decoding to invalid UTF-8 is rejected as malformed: 401 with the challenge
// and zero backend calls.
func TestSecurityEntryInvalidUTF8CredentialRejected(t *testing.T) {
	e := suNewDefault(t)
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte(suUser+":pass\xff"))
	r := httptest.NewRequest(http.MethodGet, "/artifactory/"+suRepoKey+"/x.bin", nil)
	r.Header.Set("Authorization", header)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); got != suChallenge {
		t.Fatalf("challenge = %q, want %q", got, suChallenge)
	}
	if authCalls, _, _, _ := e.backend.snapshot(); authCalls != 0 {
		t.Fatalf("backend authenticate calls = %d, want 0", authCalls)
	}
}

// TestSecurityEntryResolveRepoKeyBoundaries 直接钉死入口仓库键解析边界：
// 空/超长 URI、非前缀路径、查询串截断、无效键段与恰键无尾斜杠形式。
// TestSecurityEntryResolveRepoKeyBoundaries pins the entry repository-key
// parsing bounds directly: empty/overlong URIs, a nonmatching prefix, query
// truncation, invalid key segments and the bare-key form without a trailing
// slash.
func TestSecurityEntryResolveRepoKeyBoundaries(t *testing.T) {
	valid := "/artifactory/su.repo-1/dir/file.bin"
	cases := []struct {
		name   string
		rawURI string
		want   string
		ok     bool
	}{
		{"empty", "", "", false},
		{"overlong", "/" + strings.Repeat("a", 9000), "", false},
		{"missing prefix", "/other/su.repo-1/file.bin", "", false},
		{"query truncated", valid + "?download=1&x=%2f", "su.repo-1", true},
		{"empty key segment", "/artifactory//file.bin", "", false},
		{"dot key", "/artifactory/./file.bin", "", false},
		{"bare key without slash", "/artifactory/su.repo-1", "su.repo-1", true},
		{"valid", valid, "su.repo-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := entryResolveRepoKey(tc.rawURI)
			if ok != tc.ok || key != tc.want {
				t.Fatalf("entryResolveRepoKey(%q) = (%q,%v), want (%q,%v)", tc.rawURI, key, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestSecurityBridgeAuthorizeGuards 断言授权桥的内部守卫：缺失主体、零主
// 体、缺失目录事实与路径不符都以固定阶段错误拒绝，绝不触发 CanRead。
// TestSecurityBridgeAuthorizeGuards asserts the bridge's internal guards: a
// missing principal, zero principal, missing catalog fact and a mismatched
// path all reject with the fixed stage errors without ever calling CanRead.
func TestSecurityBridgeAuthorizeGuards(t *testing.T) {
	backend := suDefaultBackend()
	bridge := &downloadAuthBridge{backend: backend}
	path, err := repository.NewPath(suRepoKey, "dir", "file.bin", true)
	if err != nil {
		t.Fatalf("path fixture: %v", err)
	}
	mismatchPath, err := repository.NewPath("su.other-2", "dir", "file.bin", true)
	if err != nil {
		t.Fatalf("mismatch path: %v", err)
	}
	repo := RepositoryInfo{Key: suRepoKey, Kind: "local", PackageType: "generic", Revision: 4}
	plainReq := httptest.NewRequest(http.MethodGet, "/artifactory/"+suRepoKey+"/dir/file.bin", nil)
	principalReq := plainReq.WithContext(withPrincipal(plainReq.Context(), Principal{id: suUserID, username: suUser}))
	zeroReq := plainReq.WithContext(withPrincipal(plainReq.Context(), Principal{}))
	factsReq := principalReq.WithContext(context.WithValue(principalReq.Context(), repositoryContextKey{}, repo))

	for _, tc := range []struct {
		name    string
		request *http.Request
		path    repository.Path
		stage   string
	}{
		{"missing principal", plainReq, path, entryStagePrincipal},
		{"zero principal", zeroReq, path, entryStagePrincipal},
		{"missing catalog fact", principalReq, path, entryStageBridge},
		{"path mismatch", factsReq, mismatchPath, entryStageBridge},
	} {
		result, err := bridge.Authorize(context.Background(), tc.request, tc.path)
		if result.Decision != 0 {
			t.Fatalf("%s: decision = %v, want zero", tc.name, result.Decision)
		}
		if err == nil || !strings.Contains(err.Error(), tc.stage) {
			t.Fatalf("%s: error = %v, want stage %q", tc.name, err, tc.stage)
		}
	}
	if _, _, readCalls, _ := backend.snapshot(); readCalls != 0 {
		t.Fatalf("guards reached CanRead: %d calls", readCalls)
	}
}

// TestSecurityGateRecordFailureKeepsBlockedEntry 断言防御性分支：对仍被
// 阻断的条目再次记录失败不改变阻断也不报错（阻断条目本不应到达此处）。
// TestSecurityGateRecordFailureKeepsBlockedEntry asserts the defensive
// branch: recording a failure for a still-blocked entry changes neither the
// block nor the outcome (blocked entries should never reach this path).
func TestSecurityGateRecordFailureKeepsBlockedEntry(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	gate := newFailureGate(gateLimits{threshold: 2, window: time.Hour, block: time.Hour, maxUsers: 4})
	blockedUntil := now.Add(30 * time.Minute)
	gate.entries["victim"] = &failureEntry{windowStart: now, failures: 2, blockedUntil: blockedUntil}
	if err := gate.recordFailure("victim", now.Add(time.Minute)); err != nil {
		t.Fatalf("blocked-entry record error = %v", err)
	}
	entry := gate.entries["victim"]
	if entry.failures != 2 || !entry.blockedUntil.Equal(blockedUntil) {
		t.Fatalf("blocked entry mutated: failures=%d blockedUntil=%v", entry.failures, entry.blockedUntil)
	}
	if !gate.blocked("victim", now.Add(time.Minute)) {
		t.Fatal("victim lost its block")
	}
}

// TestSecurityCheckCredentialSegmentSplitRejected 断言总长与前缀都正确但
// 两段长度拆分错误（22/43 之外的组合）同样拒绝为 ErrCredentialEncoding。
// TestSecurityCheckCredentialSegmentSplitRejected asserts that a correct
// total length and prefix with a wrong segment split (anything but 22/43)
// still rejects with ErrCredentialEncoding.
func TestSecurityCheckCredentialSegmentSplitRejected(t *testing.T) {
	const valid = "X1U4bV8v22ZJgV14BJcGqQ$ZPpzcjYPjTxQLP+fnwXyoKQ9eR0qW2iNdrjLutEzABo"
	salt, key, _ := strings.Cut(valid, "$")
	// 盐 21 + 钥 44：总长保持 102、前缀完整，仅分段长度错位。
	// Salt 21 + key 44: total stays 102 with the prefix intact while only
	// the segment lengths shift.
	mangled := suArgonPrefix + salt[:len(salt)-1] + "$" + key[:1] + key
	if len(mangled) != len(suArgonPrefix)+len(valid) {
		t.Fatalf("mangled length = %d, want %d", len(mangled), len(suArgonPrefix)+len(valid))
	}
	ok, err := CheckCredential("Controlled-ASCII-123!", mangled)
	if ok || !errors.Is(err, ErrCredentialEncoding) {
		t.Fatalf("split-mangled credential = (%v,%v), want (false,ErrCredentialEncoding)", ok, err)
	}
}

package security

// 本文件直接组装真实 uploadBinder，覆盖 BindUpload 的授权分支：畸形凭据
// 的未认证表示、失败门阻断先于后端、认证期间取消、禁用账户不计入失败
// 门、其他认证错误与非法主体的 ErrConfiguration、目录缺席/错误与预检错误
// 的传播，以及门容量耗尽的运行错误。
// This file composes the real uploadBinder directly and covers BindUpload's
// authorization branches: the unauthenticated representation for malformed
// credentials, the failure gate blocking before the backend, cancellation
// during authentication, the disabled account staying uncounted, other
// authentication errors and noncanonical principals mapping to
// ErrConfiguration, catalog absence/error and preflight error propagation,
// and the operational error when the gate capacity is exhausted.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/repository"
)

// ubBinder 以真实组件组装 uploadBinder；失败门参数取自配置。
// ubBinder assembles the real uploadBinder with its failure gate limits
// taken from the configuration.
func ubBinder(backend UploadBackend, mutate func(*UploadHTTPConfig)) *uploadBinder {
	cfg := uploadHTTPConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	return &uploadBinder{
		backend: backend, nodes: &uploadReadStore{}, saver: &uploadHTTPSaver{}, cfg: cfg,
		gate: newFailureGate(gateLimits{
			threshold: cfg.FailureThreshold, window: cfg.FailureWindow,
			block: cfg.BlockDuration, maxUsers: cfg.MaxTrackedUsers,
		}),
	}
}

// ubRequest 构造带 Basic 凭据的 PUT 请求。
// ubRequest builds a PUT request carrying Basic credentials.
func ubRequest(user, password string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/artifactory/repoA/file.bin", nil)
	r.SetBasicAuth(user, password)
	return r
}

// ubAuthBackend 以可注入的认证函数包装真实后端。
// ubAuthBackend wraps the real backend with an injectable authenticator.
type ubAuthBackend struct {
	*uploadHTTPBackend
	auth func(context.Context, string, string) (Principal, error)
}

func (b *ubAuthBackend) Authenticate(ctx context.Context, username, password string) (Principal, error) {
	if b.auth != nil {
		return b.auth(ctx, username, password)
	}
	return b.uploadHTTPBackend.Authenticate(ctx, username, password)
}

// ubPath 构造目标文件路径；失败即终止测试。
// ubPath builds the target file path, failing the test on error.
func ubPath(t *testing.T) repository.Path {
	t.Helper()
	path, err := repository.NewPath("repoA", "dir", "file.bin", true)
	if err != nil {
		t.Fatalf("path fixture: %v", err)
	}
	return path
}

// TestSecurityUploadBinderMalformedCredentials 断言缺失 Authorization 时
// BindUpload 返回带 challenge 的未认证结果且绝不触碰后端/授权/存储。
// TestSecurityUploadBinderMalformedCredentials asserts a missing
// Authorization yields the unauthenticated result with the challenge while
// the backend, grants and store stay untouched.
func TestSecurityUploadBinderMalformedCredentials(t *testing.T) {
	backend := newUploadHTTPBackend()
	b := ubBinder(backend, nil)
	path := ubPath(t)
	request := httptest.NewRequest(http.MethodPut, "/artifactory/repoA/file.bin", nil)
	result, saver, err := b.BindUpload(context.Background(), request, path, repository.ClientFileChecksums{})
	if err != nil {
		t.Fatalf("malformed credentials error = %v", err)
	}
	if saver != nil {
		t.Fatal("malformed credentials returned a saver")
	}
	if result.Decision != httpserver.UploadUnauthenticated || result.Challenge != b.cfg.Challenge {
		t.Fatalf("result = %+v, want unauthenticated with configured challenge", result)
	}
	if backend.authCalls != 0 || backend.catalogCalls != 0 || len(backend.calls) != 0 {
		t.Fatalf("malformed credentials touched backend: auth=%d catalog=%d grants=%v",
			backend.authCalls, backend.catalogCalls, backend.calls)
	}
}

// TestSecurityUploadBinderGateBlocksBeforeBackend 断言达到阈值的失败键在
// 后端认证之前被拒：阻断请求不产生新的认证调用。
// TestSecurityUploadBinderGateBlocksBeforeBackend asserts a key reaching the
// threshold is rejected before backend authentication: a blocked request
// performs no new authentication call.
func TestSecurityUploadBinderGateBlocksBeforeBackend(t *testing.T) {
	backend := newUploadHTTPBackend()
	b := ubBinder(backend, nil)
	path := ubPath(t)
	for i := 0; i < b.cfg.FailureThreshold; i++ {
		result, _, err := b.BindUpload(context.Background(), ubRequest("reader", "wrong"), path, repository.ClientFileChecksums{})
		if err != nil || result.Decision != httpserver.UploadUnauthenticated {
			t.Fatalf("threshold failure %d = (%+v,%v)", i, result, err)
		}
	}
	if backend.authCalls != int(b.cfg.FailureThreshold) {
		t.Fatalf("auth calls before block = %d", backend.authCalls)
	}
	result, saver, err := b.BindUpload(context.Background(), ubRequest("reader", "test-only-password"), path, repository.ClientFileChecksums{})
	if err != nil || saver != nil || result.Decision != httpserver.UploadDenied {
		t.Fatalf("blocked request = (%+v, %v, %v), want denied without saver", result, saver, err)
	}
	if backend.authCalls != int(b.cfg.FailureThreshold) {
		t.Fatalf("blocked request authenticated again: auth=%d", backend.authCalls)
	}
}

// TestSecurityUploadBinderAuthenticateOutcomes 断言认证阶段的其他结局：
// 取消传播 ctx 错误、禁用账户回答未认证且不计入失败门、任意其他错误原样
// 上抛、非规范主体拒绝为 ErrConfiguration。
// TestSecurityUploadBinderAuthenticateOutcomes asserts the remaining
// authentication outcomes: cancellation propagates the ctx error, the
// disabled account answers unauthenticated without counting toward the gate,
// any other error passes through, and a noncanonical principal rejects with
// ErrConfiguration.
func TestSecurityUploadBinderAuthenticateOutcomes(t *testing.T) {
	path := ubPath(t)

	t.Run("cancellation during authentication", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		backend := &ubAuthBackend{uploadHTTPBackend: newUploadHTTPBackend()}
		backend.auth = func(c context.Context, username, password string) (Principal, error) {
			cancel()
			return backend.uploadHTTPBackend.Authenticate(c, username, password)
		}
		b := ubBinder(backend, nil)
		result, saver, err := b.BindUpload(ctx, ubRequest("reader", "test-only-password"), path, repository.ClientFileChecksums{})
		if saver != nil {
			t.Fatal("canceled authentication returned a saver")
		}
		if result.Decision != 0 || result.Challenge != "" {
			t.Fatalf("canceled result = %+v, want zero", result)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled error = %v, want context.Canceled", err)
		}
	})

	t.Run("disabled account stays uncounted", func(t *testing.T) {
		backend := &ubAuthBackend{uploadHTTPBackend: newUploadHTTPBackend()}
		calls := 0
		backend.auth = func(_ context.Context, username, password string) (Principal, error) {
			calls++
			if password == "disabled-password" {
				return Principal{}, ErrAccountDisabled
			}
			return backend.uploadHTTPBackend.Authenticate(context.Background(), username, password)
		}
		b := ubBinder(backend, nil)
		for i := 0; i < 3; i++ {
			result, _, err := b.BindUpload(context.Background(), ubRequest("reader", "disabled-password"), path, repository.ClientFileChecksums{})
			if err != nil || result.Decision != httpserver.UploadUnauthenticated {
				t.Fatalf("disabled attempt %d = (%+v,%v)", i, result, err)
			}
		}
		if calls != 3 {
			t.Fatalf("disabled auth calls = %d, want 3", calls)
		}
		// 三次禁用尝试均不计入失败门：正确凭据仍能继续（此处随后因无
		// deploy 授权而被拒，但不再是未认证）。
		// None of the three disabled attempts counted: valid credentials
		// still proceed (denied here only for lacking the deploy grant).
		result, _, err := b.BindUpload(context.Background(), ubRequest("reader", "test-only-password"), path, repository.ClientFileChecksums{})
		if err != nil || result.Decision != httpserver.UploadDenied {
			t.Fatalf("post-disabled valid credentials = (%+v,%v), want policy denial", result, err)
		}
	})

	t.Run("other authentication error passes through", func(t *testing.T) {
		cause := errors.New("auth-boom")
		backend := &ubAuthBackend{uploadHTTPBackend: newUploadHTTPBackend()}
		backend.auth = func(context.Context, string, string) (Principal, error) { return Principal{}, cause }
		b := ubBinder(backend, nil)
		result, saver, err := b.BindUpload(context.Background(), ubRequest("reader", "x"), path, repository.ClientFileChecksums{})
		if saver != nil || result.Decision != 0 {
			t.Fatalf("failed result = (%+v,%v), want zero", result, saver)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("error = %v, want passthrough %v", err, cause)
		}
	})

	t.Run("noncanonical principal rejected", func(t *testing.T) {
		backend := &ubAuthBackend{uploadHTTPBackend: newUploadHTTPBackend()}
		backend.auth = func(context.Context, string, string) (Principal, error) {
			return Principal{id: 7, username: "Reader"}, nil
		}
		b := ubBinder(backend, nil)
		result, saver, err := b.BindUpload(context.Background(), ubRequest("reader", "x"), path, repository.ClientFileChecksums{})
		if saver != nil || result.Decision != 0 {
			t.Fatalf("noncanonical result = (%+v,%v), want zero", result, saver)
		}
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("error = %v, want ErrConfiguration", err)
		}
		if backend.catalogCalls != 0 {
			t.Fatalf("noncanonical principal reached catalog: %d", backend.catalogCalls)
		}
	})
}

// TestSecurityUploadBinderCatalogAndPreflight 断言目录与预检结局：目录缺
// 席回答拒绝且不做授权查询、目录错误与 deploy 查询错误原样上抛。
// TestSecurityUploadBinderCatalogAndPreflight asserts the catalog and
// preflight outcomes: catalog absence answers denial without grant queries
// while catalog errors and the deploy-lookup error pass through unchanged.
func TestSecurityUploadBinderCatalogAndPreflight(t *testing.T) {
	path := ubPath(t)

	t.Run("catalog absent denies without grants", func(t *testing.T) {
		backend := newUploadHTTPBackend()
		backend.catalogErr = ErrRepositoryAbsent
		b := ubBinder(backend, nil)
		result, saver, err := b.BindUpload(context.Background(), ubRequest("reader", "test-only-password"), path, repository.ClientFileChecksums{})
		if err != nil || saver != nil || result.Decision != httpserver.UploadDenied {
			t.Fatalf("absent catalog = (%+v,%v,%v), want denial", result, saver, err)
		}
		if len(backend.calls) != 0 {
			t.Fatalf("absent catalog ran grants %v", backend.calls)
		}
	})

	t.Run("catalog error passes through", func(t *testing.T) {
		cause := errors.New("catalog-boom")
		backend := newUploadHTTPBackend()
		backend.catalogErr = cause
		b := ubBinder(backend, nil)
		result, saver, err := b.BindUpload(context.Background(), ubRequest("reader", "test-only-password"), path, repository.ClientFileChecksums{})
		if saver != nil || result.Decision != 0 {
			t.Fatalf("catalog error result = (%+v,%v), want zero", result, saver)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("error = %v, want passthrough %v", err, cause)
		}
	})

	t.Run("preflight error passes through", func(t *testing.T) {
		cause := errors.New("deploy-boom")
		backend := newUploadHTTPBackend()
		backend.deployErr = cause
		b := ubBinder(backend, nil)
		result, saver, err := b.BindUpload(context.Background(), ubRequest("reader", "test-only-password"), path, repository.ClientFileChecksums{})
		if saver != nil || result.Decision != 0 {
			t.Fatalf("preflight result = (%+v,%v), want zero", result, saver)
		}
		if !errors.Is(err, ErrSecurityStore) || !errors.Is(err, cause) {
			t.Fatalf("error = %v, want store error with cause %v", err, cause)
		}
	})
}

// TestSecurityUploadBinderGateCapacityExhausted 断言失败门容量耗尽时新
// 负向条目无法记录：BindUpload 返回运行错误而不是绕过阻断。
// TestSecurityUploadBinderGateCapacityExhausted asserts that with the
// failure-gate capacity exhausted no new negative entry can be recorded:
// BindUpload answers an operational error instead of bypassing the block.
func TestSecurityUploadBinderGateCapacityExhausted(t *testing.T) {
	backend := newUploadHTTPBackend()
	b := ubBinder(backend, func(c *UploadHTTPConfig) { c.MaxTrackedUsers = 1 })
	path := ubPath(t)
	result, _, err := b.BindUpload(context.Background(), ubRequest("first", "wrong"), path, repository.ClientFileChecksums{})
	if err != nil || result.Decision != httpserver.UploadUnauthenticated {
		t.Fatalf("first failure = (%+v,%v)", result, err)
	}
	result, saver, err := b.BindUpload(context.Background(), ubRequest("second", "wrong"), path, repository.ClientFileChecksums{})
	if saver != nil || result.Decision != 0 {
		t.Fatalf("capacity result = (%+v,%v), want zero", result, saver)
	}
	if err == nil {
		t.Fatal("exhausted gate capacity returned nil error")
	}
	if backend.authCalls != 2 {
		t.Fatalf("auth calls = %d, want 2", backend.authCalls)
	}
}

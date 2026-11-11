package security

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

type uploadHTTPBackend struct {
	uploadGrants
	repo          RepositoryInfo
	validateErr   error
	catalogErr    error
	afterCatalog  func()
	validateCalls int
	catalogCalls  int
	authCalls     int
}

func (b *uploadHTTPBackend) Validate(context.Context) error {
	b.validateCalls++
	return b.validateErr
}

func (b *uploadHTTPBackend) LookupRepository(context.Context, string) (RepositoryInfo, error) {
	b.catalogCalls++
	if b.afterCatalog != nil {
		b.afterCatalog()
	}
	return b.repo, b.catalogErr
}

func (b *uploadHTTPBackend) Authenticate(_ context.Context, username, password string) (Principal, error) {
	b.authCalls++
	if username != "reader" || password != "test-only-password" {
		return Principal{}, ErrInvalidCredentials
	}
	return Principal{id: 7, username: "reader"}, nil
}

type uploadHTTPBody struct{ reads int }

func (b *uploadHTTPBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (*uploadHTTPBody) Close() error               { return nil }

type uploadHTTPSaver struct{ calls int }

func (s *uploadHTTPSaver) SaveAuthorized(_ context.Context, body io.Reader, in localrepo.UploadInput, p Principal, repo RepositoryInfo, _ bool) (repository.FileSnapshotSource, error) {
	s.calls++
	if _, err := io.Copy(io.Discard, body); err != nil {
		return repository.FileSnapshotSource{}, err
	}
	metadata := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedBy: repository.NewActor(sha1Units(p.Username())), ModifiedBy: repository.NewActor(sha1Units(p.Username())),
	})
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID: 1, Path: in.Path, Metadata: &metadata,
			Content: content.Metadata{
				SHA1: "a9993e364706816aba3e25717850c26c9cd0d89d", MD5: "900150983cd24fb0d6963f7d28e17f72",
				SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Size: 3,
			},
		},
		SHA1ActualValid: true, MD5ActualValid: true, SHA256ActualValid: true,
	}, nil
}

func uploadHTTPConfig() UploadHTTPConfig {
	return UploadHTTPConfig{
		HTTPConfig: HTTPConfig{
			RepoKey: "repoA", Challenge: `Basic realm="BinFlow"`, FailureThreshold: 2,
			FailureWindow: time.Hour, BlockDuration: time.Hour, MaxTrackedUsers: 4,
		},
		MaxBodyBytes: 1024, PublicOrigin: "https://uploads.example.test",
		ReportUpload: func(httpserver.UploadFailure) {},
	}
}

func newUploadHTTPBackend() *uploadHTTPBackend {
	return &uploadHTTPBackend{repo: RepositoryInfo{Key: "repoA", Kind: "local", PackageType: "generic", Revision: 1}}
}

func uploadHTTPRejectedRequest(t *testing.T, h http.Handler, password string, status int) {
	t.Helper()
	body := &uploadHTTPBody{}
	r := httptest.NewRequest(http.MethodPut, "/artifactory/repoA/file.bin", nil)
	r.Body, r.ContentLength = body, 3
	r.SetBasicAuth("reader", password)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status || body.reads != 0 {
		t.Fatalf("status=%d body reads=%d, want status=%d without reading body", w.Code, body.reads, status)
	}
	challenge := ""
	if status == http.StatusUnauthorized {
		challenge = uploadHTTPConfig().Challenge
	}
	if got := w.Header().Get("WWW-Authenticate"); got != challenge {
		t.Fatalf("challenge=%q, want %q", got, challenge)
	}
}

// 达到阈值的失败仍为401，后续请求被阻断且不读取正文。
// The threshold failure remains 401; subsequent requests block before body reads.
func TestAuthenticatedUploadFailureGate(t *testing.T) {
	b, nodes, saver := newUploadHTTPBackend(), &uploadReadStore{}, &uploadHTTPSaver{}
	h, err := NewAuthenticatedUpload(context.Background(), uploadHTTPConfig(), b, nodes, saver)
	if err != nil {
		t.Fatal(err)
	}
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusForbidden)
	uploadHTTPRejectedRequest(t, h, "test-only-password", http.StatusForbidden)
	if b.authCalls != 2 || b.catalogCalls != 1 || len(b.calls) != 0 || len(nodes.queries) != 0 || saver.calls != 0 {
		t.Fatalf("auth=%d catalog=%d grants=%v nodes=%d saves=%d", b.authCalls, b.catalogCalls, b.calls, len(nodes.queries), saver.calls)
	}
}

// 有效凭据清除先前失败，即使随后因 deploy 权限拒绝上传。
// Valid credentials reset failures even when deploy permission later rejects the upload.
func TestAuthenticatedUploadCorrectCredentialsResetFailureGate(t *testing.T) {
	b, nodes, saver := newUploadHTTPBackend(), &uploadReadStore{}, &uploadHTTPSaver{}
	h, err := NewAuthenticatedUpload(context.Background(), uploadHTTPConfig(), b, nodes, saver)
	if err != nil {
		t.Fatal(err)
	}
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	uploadHTTPRejectedRequest(t, h, "test-only-password", http.StatusForbidden)
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	uploadHTTPRejectedRequest(t, h, "test-only-password", http.StatusForbidden)
	if b.authCalls != 4 || len(b.calls) != 1 || len(nodes.queries) != 0 || saver.calls != 0 {
		t.Fatalf("auth=%d grants=%v nodes=%d saves=%d", b.authCalls, b.calls, len(nodes.queries), saver.calls)
	}
}

func TestAuthenticatedUploadSuccessfulPUTResetsFailureGate(t *testing.T) {
	b, nodes, saver := newUploadHTTPBackend(), &uploadReadStore{}, &uploadHTTPSaver{}
	b.deploy, b.del = true, true
	// 缺席节点走新建路径。An absent node takes the create path.
	nodes.err = sql.ErrNoRows
	h, err := NewAuthenticatedUpload(context.Background(), uploadHTTPConfig(), b, nodes, saver)
	if err != nil {
		t.Fatal(err)
	}
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	r := httptest.NewRequest(http.MethodPut, "/artifactory/repoA/file.bin", strings.NewReader("abc"))
	r.SetBasicAuth("reader", "test-only-password")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated || saver.calls != 1 {
		t.Fatalf("status=%d saves=%d, want 201 and one save", w.Code, saver.calls)
	}
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	uploadHTTPRejectedRequest(t, h, "wrong", http.StatusUnauthorized)
	if b.authCalls != 4 {
		t.Fatalf("authentication calls=%d, want 4", b.authCalls)
	}
}

func TestNewAuthenticatedUploadConfigurationFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*UploadHTTPConfig)
		cause  error
	}{
		{"repository key", func(c *UploadHTTPConfig) { c.RepoKey = "../repo" }, nil},
		{"challenge", func(c *UploadHTTPConfig) { c.Challenge = "invalid\r\nchallenge" }, nil},
		{"threshold", func(c *UploadHTTPConfig) { c.FailureThreshold = 0 }, nil},
		{"window", func(c *UploadHTTPConfig) { c.FailureWindow = 0 }, nil},
		{"block duration", func(c *UploadHTTPConfig) { c.BlockDuration = 0 }, nil},
		{"tracked users", func(c *UploadHTTPConfig) { c.MaxTrackedUsers = 0 }, nil},
		{"body limit", func(c *UploadHTTPConfig) { c.MaxBodyBytes = 0 }, httpserver.ErrUploadConfiguration},
		{"origin", func(c *UploadHTTPConfig) { c.PublicOrigin = "invalid" }, httpserver.ErrUploadConfiguration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, b := uploadHTTPConfig(), newUploadHTTPBackend()
			tc.mutate(&cfg)
			h, err := NewAuthenticatedUpload(context.Background(), cfg, b, &uploadReadStore{}, &uploadHTTPSaver{})
			if h != nil || !errors.Is(err, ErrConfiguration) || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("handler=%v error=%v, want nil and configuration error with cause %v", h, err, tc.cause)
			}
			if b.validateCalls != 0 || b.catalogCalls != 0 {
				t.Fatalf("invalid configuration reached backend: validate=%d catalog=%d", b.validateCalls, b.catalogCalls)
			}
		})
	}
	for _, name := range []string{"context", "backend", "nodes", "saver"} {
		t.Run("nil "+name, func(t *testing.T) {
			var ctx context.Context = context.Background()
			var backend UploadBackend = newUploadHTTPBackend()
			var nodes localrepo.FileReadStore = &uploadReadStore{}
			var saver AuthorizedUploadSaver = &uploadHTTPSaver{}
			switch name {
			case "context":
				ctx = nil
			case "backend":
				backend = nil
			case "nodes":
				nodes = nil
			case "saver":
				saver = nil
			}
			h, err := NewAuthenticatedUpload(ctx, uploadHTTPConfig(), backend, nodes, saver)
			if h != nil || !errors.Is(err, ErrConfiguration) {
				t.Fatalf("handler=%v error=%v, want nil and ErrConfiguration", h, err)
			}
		})
	}
}

// 启动失败与取消必须保留原因，不能返回可用处理器。
// Startup failures and cancellation preserve their cause and never return a usable handler.
func TestNewAuthenticatedUploadStartupFailuresAndCancellation(t *testing.T) {
	failure := errors.New("test backend failure")
	for _, tc := range []struct {
		name         string
		setup        func(*uploadHTTPBackend, context.CancelFunc)
		cause        error
		wantValidate int
		wantCatalog  int
	}{
		{"pre-canceled", func(_ *uploadHTTPBackend, cancel context.CancelFunc) { cancel() }, context.Canceled, 0, 0},
		{"validation error", func(b *uploadHTTPBackend, _ context.CancelFunc) { b.validateErr = failure }, failure, 1, 0},
		{"catalog error", func(b *uploadHTTPBackend, _ context.CancelFunc) { b.catalogErr = failure }, failure, 1, 1},
		{"catalog absent", func(b *uploadHTTPBackend, _ context.CancelFunc) { b.catalogErr = ErrRepositoryAbsent }, ErrRepositoryAbsent, 1, 1},
		{"catalog canceled", func(b *uploadHTTPBackend, cancel context.CancelFunc) { b.afterCatalog = cancel }, context.Canceled, 1, 1},
		{"wrong repository", func(b *uploadHTTPBackend, _ context.CancelFunc) { b.repo.Key = "other" }, nil, 1, 1},
		{"remote repository", func(b *uploadHTTPBackend, _ context.CancelFunc) { b.repo.Kind = "remote" }, nil, 1, 1},
		{"unsupported package", func(b *uploadHTTPBackend, _ context.CancelFunc) { b.repo.PackageType = "maven" }, nil, 1, 1},
		{"missing revision", func(b *uploadHTTPBackend, _ context.CancelFunc) { b.repo.Revision = 0 }, nil, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := newUploadHTTPBackend()
			tc.setup(b, cancel)
			h, err := NewAuthenticatedUpload(ctx, uploadHTTPConfig(), b, &uploadReadStore{}, &uploadHTTPSaver{})
			if h != nil || !errors.Is(err, ErrConfiguration) || (tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("handler=%v error=%v, want nil and configuration error with cause %v", h, err, tc.cause)
			}
			if b.validateCalls != tc.wantValidate || b.catalogCalls != tc.wantCatalog || b.authCalls != 0 {
				t.Fatalf("validate=%d catalog=%d auth=%d, want %d/%d/0", b.validateCalls, b.catalogCalls, b.authCalls, tc.wantValidate, tc.wantCatalog)
			}
		})
	}
}

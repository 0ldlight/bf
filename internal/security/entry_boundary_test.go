package security

import (
	"context"
	"database/sql"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/repository"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type rbBackend struct {
	calls     int
	absent    bool
	principal Principal
	cancel    context.CancelFunc
	revision  int64
}

func (*rbBackend) Validate(context.Context) error { return nil }
func (b *rbBackend) Authenticate(context.Context, string, string) (Principal, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return b.principal, nil
}
func (b *rbBackend) LookupRepository(context.Context, string) (RepositoryInfo, error) {
	b.calls++
	if b.absent && b.calls > 1 {
		return RepositoryInfo{}, ErrRepositoryAbsent
	}
	return RepositoryInfo{Key: "generic-local", Kind: "local", PackageType: "generic", Revision: int64(b.calls)}, nil
}
func (b *rbBackend) CanRead(_ context.Context, _ Principal, r RepositoryInfo, _ repository.Path) (bool, error) {
	b.revision = r.Revision
	return true, nil
}

type rbNodes struct{ calls int }

func (n *rbNodes) LookupSnapshotSource(context.Context, repository.Path) (repository.FileSnapshotSource, error) {
	n.calls++
	return repository.FileSnapshotSource{}, sql.ErrNoRows
}

type rbContent struct{}

func (rbContent) Open(context.Context, string) (io.ReadCloser, error) { panic("unexpected Open") }
func rbHandler(t *testing.T, b *rbBackend) (http.Handler, *rbNodes) {
	t.Helper()
	n := &rbNodes{}
	h, e := NewAuthenticatedDownload(context.Background(), HTTPConfig{RepoKey: "generic-local", Challenge: `Basic realm="BinFlow"`, FailureThreshold: 1, FailureWindow: time.Second, BlockDuration: time.Minute, MaxTrackedUsers: 4, Report: func(httpserver.DownloadFailure) {}}, b, n, rbContent{})
	if e != nil {
		t.Fatal(e)
	}
	return h, n
}
func rbRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/artifactory/generic-local/a.bin", nil)
	r.SetBasicAuth("reader", "Controlled-passphrase-123!")
	return r
}

// 目录缺席、同次目录事实、非法主体与成功后的取消不能被外层吞掉。
// Catalog absence, same-request facts, invalid principals and post-success cancellation remain distinct.
func TestSecurityEntryReviewedBoundaries(t *testing.T) {
	t.Run("catalog absent", func(t *testing.T) {
		b := &rbBackend{absent: true, principal: Principal{id: 1, username: "reader"}}
		h, n := rbHandler(t, b)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, rbRequest())
		if w.Code != 404 || n.calls != 0 {
			t.Fatalf("status=%d nodes=%d", w.Code, n.calls)
		}
	})
	t.Run("fresh catalog revision", func(t *testing.T) {
		b := &rbBackend{principal: Principal{id: 1, username: "reader"}}
		h, _ := rbHandler(t, b)
		h.ServeHTTP(httptest.NewRecorder(), rbRequest())
		if b.revision != 2 {
			t.Fatalf("authorization used revision %d, want request revision 2", b.revision)
		}
	})
	t.Run("noncanonical principal", func(t *testing.T) {
		b := &rbBackend{principal: Principal{id: 1, username: "READER"}}
		h, n := rbHandler(t, b)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, rbRequest())
		if w.Code != 500 || n.calls != 0 {
			t.Fatalf("status=%d nodes=%d", w.Code, n.calls)
		}
	})
	t.Run("cancel after success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		b := &rbBackend{principal: Principal{id: 1, username: "reader"}, cancel: cancel}
		h, _ := rbHandler(t, b)
		r := rbRequest().WithContext(ctx)
		r.RequestURI = "/invalid"
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Error("successful backend cancellation did not abort")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), r)
	})
}
func TestSecurityGateThresholdOneAndIndependentBlock(t *testing.T) {
	g := newFailureGate(gateLimits{threshold: 1, window: time.Second, block: time.Minute, maxUsers: 1})
	now := time.Unix(100, 0)
	if e := g.recordFailure("reader", now); e != nil {
		t.Fatal(e)
	}
	if !g.blocked("reader", now.Add(2*time.Second)) {
		t.Fatal("active block lost before BlockDuration")
	}
	if e := g.recordFailure("other", now.Add(2*time.Second)); e == nil {
		t.Fatal("active block evicted at capacity")
	}
	if g.blocked("reader", now.Add(time.Minute)) {
		t.Fatal("expired block retained")
	}
}

package filestore

// 并发与取消：持有源、发布前不可见、staging 关闭后取消、八实例并发首发。
// Concurrency and cancellation: held source, pre-publication invisibility,
// cancel after staging close, eight-instance concurrent first writes.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
)

// hangGuard 防止早期错误导致测试挂死；不用于同步。
// hangGuard avoids hangs on early errors; never used for synchronization.
const hangGuard = 5 * time.Second

// gatedReader 首次 Read 阻塞到 gate 关闭，然后一次给足 "abc"。
// gatedReader blocks its first Read until gate closes, then serves "abc".
type gatedReader struct {
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
	served  bool
	closes  int
}

func (g *gatedReader) Read(p []byte) (int, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.gate
	if g.served {
		return 0, io.EOF
	}
	g.served = true
	return copy(p, "abc"), io.EOF
}

func (g *gatedReader) Close() error { g.closes++; return nil }

// closeCountSource 统计调用方是否误关 source。
// closeCountSource detects accidental closing of the caller's source.
type closeCountSource struct {
	r      io.Reader
	closes int
}

func (c *closeCountSource) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *closeCountSource) Close() error               { c.closes++; return nil }

// cancelOnClose 在真实 Close 前取消 context。
// cancelOnClose cancels the context just before the real Close.
type cancelOnClose struct {
	f      io.WriteCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Write(p []byte) (int, error) { return c.f.Write(p) }
func (c *cancelOnClose) Close() error {
	c.cancel()
	return c.f.Close()
}

func waitErr(t *testing.T, res <-chan error) error {
	t.Helper()
	select {
	case err := <-res:
		return err
	case <-time.After(hangGuard):
		t.Fatal("Put did not return within hang guard")
		return nil
	}
}

// 持有发布：EOF 前其他实例 Open 必须 ErrNotExist，放行后完整可见。
// Held publication: Open from another instance must report ErrNotExist
// before EOF; after release the full object is visible.
func TestHeldPublicationNotVisible(t *testing.T) {
	s1, root := newTestStore(t)
	s2, err := New(root)
	if err != nil {
		t.Fatalf("New second: %v", err)
	}
	defer s2.Close()
	src := &gatedReader{gate: make(chan struct{}), entered: make(chan struct{})}
	res := make(chan error)
	go func() {
		_, err := s1.Put(t.Context(), src, Expected{})
		res <- err
	}()
	select {
	case <-src.entered:
	case <-time.After(hangGuard):
		t.Fatal("source Read never started")
	}
	if _, err := s2.Open(t.Context(), abcSHA1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-EOF Open err = %v, want os.ErrNotExist", err)
	}
	close(src.gate)
	if err := waitErr(t, res); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if src.closes != 0 {
		t.Fatalf("source closed %d times, want 0", src.closes)
	}
	rc, err := s2.Open(t.Context(), abcSHA1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(b) != "abc" {
		t.Fatalf("bytes = %q, want %q", b, "abc")
	}
}

// staging 关闭后取消：不发布、清理 staging、source 不被关闭。
// Cancel after staging close: no publication, staging cleaned, source
// never closed.
func TestCancelAfterStagingClose(t *testing.T) {
	s, root := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	orig := s.stageOpen
	s.stageOpen = func(name string) (io.WriteCloser, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		return &cancelOnClose{f: f, cancel: cancel}, nil
	}
	src := &closeCountSource{r: strings.NewReader("abc")}
	meta, err := s.Put(ctx, src, Expected{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if meta.SHA1 != "" {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	noObject(t, root)
	stagingEmpty(t, root)
	if src.closes != 0 {
		t.Fatalf("source closed %d times, want 0", src.closes)
	}
}

// 八个独立 Store 实例并发写同内容：全部成功且元数据一致，读回完整。
// Eight independent Store instances concurrently write identical bytes:
// all succeed with identical metadata and full readback.
func TestEightConcurrentPutMultipleStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	const n = 8
	stores := make([]*Store, n)
	for i := range stores {
		s, err := New(root)
		if err != nil {
			t.Fatalf("New %d: %v", i, err)
		}
		stores[i] = s
		defer s.Close()
	}
	metas := make([]content.Metadata, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			metas[i], errs[i] = stores[i].Put(t.Context(), strings.NewReader("abc"), Expected{SHA1: abcSHA1})
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(hangGuard):
		t.Fatal("concurrent Puts did not return within hang guard")
	}
	for i := range stores {
		if errs[i] != nil {
			t.Fatalf("Put %d: %v", i, errs[i])
		}
		if metas[i].SHA1 != abcSHA1 || metas[i].Size != 3 {
			t.Fatalf("meta %d = %+v, want abc vector", i, metas[i])
		}
	}
	rc, err := stores[0].Open(t.Context(), abcSHA1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil || string(b) != "abc" {
		t.Fatalf("readback = %q err=%v, want %q", b, err, "abc")
	}
}

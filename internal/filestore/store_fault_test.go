package filestore

// 故障注入回归：逐 store 钩子失败路径，主因/关闭/清理错误都要保留，
// Close 恰好一次，清理只碰本次 staging。
// Fault-injection regressions: per-store hook failures keep the primary,
// close and cleanup causes; Close runs exactly once; cleanup touches only
// this attempt's staging.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

var (
	errCreate = errors.New("test: create staging")
	errWrite  = errors.New("test: write staging")
	errClose  = errors.New("test: close staging")
	errRename = errors.New("test: rename publish")
	errRemove = errors.New("test: remove staging")
)

// fakeWriter 可注入写/关失败并统计 Close 次数。
// fakeWriter injects write/close failures and counts Close calls.
type fakeWriter struct {
	writeErr error
	closeErr error
	closes   int
}

func (f *fakeWriter) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *fakeWriter) Close() error {
	f.closes++
	if f.closeErr != nil {
		return f.closeErr
	}
	return nil
}

// countWC 包装真实句柄统计 Close 次数。
// countWC wraps a real handle and counts Close calls.
type countWC struct {
	f      io.WriteCloser
	closes *int
}

func (c *countWC) Write(p []byte) (int, error) { return c.f.Write(p) }
func (c *countWC) Close() error                { *c.closes++; return c.f.Close() }

func noObject(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(objectPath(root, abcSHA1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object stat err = %v, want ErrNotExist", err)
	}
}

func TestFaultCreateStaging(t *testing.T) {
	s, root := newTestStore(t)
	s.stageOpen = func(string) (io.WriteCloser, error) { return nil, errCreate }
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{})
	if !errors.Is(err, errCreate) {
		t.Fatalf("err = %v, want errCreate", err)
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	noObject(t, root)
	stagingEmpty(t, root)
}

// 主因（写）+ 关闭 + 清理三个错误都保留，Close 恰好一次。
// Primary (write), close and cleanup errors all preserved, one Close.
func TestFaultWriteCloseRemoveCombined(t *testing.T) {
	s, root := newTestStore(t)
	fw := &fakeWriter{writeErr: errWrite, closeErr: errClose}
	s.stageOpen = func(string) (io.WriteCloser, error) { return fw, nil }
	s.removeFile = func(string) error { return errRemove }
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{})
	for _, want := range []error{errWrite, errClose, errRemove} {
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want contains %v", err, want)
		}
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	if fw.closes != 1 {
		t.Fatalf("closes = %d, want 1", fw.closes)
	}
	noObject(t, root)
	stagingEmpty(t, root)
}

// 关闭失败必须阻止发布，且错误不被丢弃。
// A close failure must prevent publication without dropping the error.
func TestFaultClosePreventsPublish(t *testing.T) {
	s, root := newTestStore(t)
	fw := &fakeWriter{closeErr: errClose}
	s.stageOpen = func(string) (io.WriteCloser, error) { return fw, nil }
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{})
	if !errors.Is(err, errClose) {
		t.Fatalf("err = %v, want errClose", err)
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	if fw.closes != 1 {
		t.Fatalf("closes = %d, want 1", fw.closes)
	}
	noObject(t, root)
	stagingEmpty(t, root)
}

func TestFaultRename(t *testing.T) {
	s, root := newTestStore(t)
	s.renameFile = func(string, string) error { return errRename }
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{})
	if !errors.Is(err, errRename) {
		t.Fatalf("err = %v, want errRename", err)
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	noObject(t, root)
	stagingEmpty(t, root)
}

// 清理失败：上报错误，本次 staging 残留，绝不碰最终对象与无关文件。
// Cleanup failure: report it, keep this attempt's leftover, never touch
// the final object or unrelated files.
func TestFaultRemoveLeftoverOwnOnly(t *testing.T) {
	s, root := newTestStore(t)
	marker := filepath.Join(root, ".staging", "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.removeFile = func(string) error { return errRemove }
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{SHA1: emptySHA1})
	if !errors.Is(err, ErrChecksumMismatch) || !errors.Is(err, errRemove) {
		t.Fatalf("err = %v, want ErrChecksumMismatch+errRemove", err)
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	noObject(t, root)
	ents, err := os.ReadDir(filepath.Join(root, ".staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 { // 本次残留 + 无关 marker / leftover + marker
		t.Fatalf("staging entries = %d, want 2", len(ents))
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "keep" {
		t.Fatalf("marker = %q err=%v, want intact", b, err)
	}
}

// 校验失败与成功路径都只 Close 一次。
// Both the mismatch and success paths Close exactly once.
func TestCloseExactlyOnce(t *testing.T) {
	s, root := newTestStore(t)
	fw := &fakeWriter{}
	s.stageOpen = func(string) (io.WriteCloser, error) { return fw, nil }
	if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{SHA1: emptySHA1}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if fw.closes != 1 {
		t.Fatalf("mismatch path closes = %d, want 1", fw.closes)
	}

	s2, _ := newTestStore(t)
	orig := s2.stageOpen
	var closes int
	s2.stageOpen = func(name string) (io.WriteCloser, error) {
		f, err := orig(name)
		if err != nil {
			return nil, err
		}
		return &countWC{f: f, closes: &closes}, nil
	}
	if _, err := s2.Put(t.Context(), strings.NewReader("abc"), Expected{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if closes != 1 {
		t.Fatalf("success path closes = %d, want 1", closes)
	}
	rc, err := s2.Open(t.Context(), abcSHA1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rc.Close()
	_ = root
}

// 无关 staging marker 在启动与失败 Put 后都保留。
// An unrelated staging marker survives startup and failed Puts.
func TestUnrelatedStagingMarkerSurvives(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, ".staging"), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, ".staging", "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatalf("New with marker: %v", err)
	}
	defer s.Close()
	if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{SHA1: emptySHA1}); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "keep" {
		t.Fatalf("marker = %q err=%v, want intact", b, err)
	}
	ents, err := os.ReadDir(filepath.Join(root, ".staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "marker" {
		t.Fatalf("staging entries = %+v, want only marker", ents)
	}
}

func TestPutPreCancelled(t *testing.T) {
	s, root := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	meta, err := s.Put(ctx, strings.NewReader("abc"), Expected{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	stagingEmpty(t, root)
}

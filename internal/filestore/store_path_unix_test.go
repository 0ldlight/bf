//go:build unix

package filestore

// FIFO final 被 Put/Open 拒绝且不被删除；非阻塞语义避免挂起。
// A FIFO final is rejected by Put and Open and never removed; the
// nonblocking open semantics avoid hanging on it.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestFinalFIFORejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fanDir := filepath.Join(root, "a9")
	if err := os.Mkdir(fanDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(fanDir, abcSHA1)
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{}); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Put err = %v, want ErrUnsafePath", err)
	}
	stagingEmpty(t, root)
	if _, err := s.Open(t.Context(), abcSHA1); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Open err = %v, want ErrUnsafePath", err)
	}
	if fi, err := os.Lstat(fifo); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("fifo after rejection = %+v err=%v, want still present", fi, err)
	}
}

package filestore

// 本文件覆盖 Put/Open 的剩余故障分支：nil source 守卫、复用既存对象时
// 清理失败的零结果与最终对象/inode 保持、发布原语对非法键的拒绝，以及
// Open 的预取消守卫。
// This file covers the remaining Put/Open fault branches: the nil-source
// guard, the zero result with the final object and its inode preserved when
// reuse-time cleanup fails, the publish primitive's invalid-key rejection,
// and Open's precancellation guard.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

// TestPutNilSourceRejected 断言 nil source 在任何 staging 之前拒绝。
// TestPutNilSourceRejected asserts a nil source rejects before any staging.
func TestPutNilSourceRejected(t *testing.T) {
	s, root := newTestStore(t)
	meta, err := s.Put(t.Context(), nil, Expected{})
	if meta.SHA1 != "" || err == nil || err.Error() != "filestore: nil source" {
		t.Fatalf("Put(nil) = (%+v,%v), want zero metadata and the fixed nil-source error", meta, err)
	}
	stagingEmpty(t, root)
}

// TestPutReuseCleanupFailureKeepsObject 断言复用既存对象时本次 staging
// 清理失败按错误上报零结果：最终对象原样保留且 inode 不变，残留仅本次
// staging。
// TestPutReuseCleanupFailureKeepsObject asserts that when reusing an
// existing object a cleanup failure of this attempt's staging reports an
// error with a zero result: the final object stays with its inode unchanged
// and the only leftover is this attempt's staging.
func TestPutReuseCleanupFailureKeepsObject(t *testing.T) {
	s, root := newTestStore(t)
	if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{}); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	object := objectPath(root, abcSHA1)
	inode := inodeOf(t, object)
	s.removeFile = func(string) error { return errRemove }
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{})
	if !errors.Is(err, errRemove) {
		t.Fatalf("second Put err = %v, want errRemove", err)
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("second Put meta = %+v, want zero", meta)
	}
	if got := inodeOf(t, object); got != inode {
		t.Fatalf("object inode changed: %d → %d", inode, got)
	}
	ents, readErr := os.ReadDir(filepath.Join(root, ".staging"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(ents) != 1 {
		t.Fatalf("staging entries = %d, want exactly this attempt's leftover", len(ents))
	}
}

// TestPublishRejectsInvalidKey 断言发布原语在任何文件系统操作之前拒绝
// 非规范键（ErrInvalidKey），staging 不被触碰。
// TestPublishRejectsInvalidKey asserts the publish primitive rejects a
// noncanonical key (ErrInvalidKey) before any filesystem operation, leaving
// staging untouched.
func TestPublishRejectsInvalidKey(t *testing.T) {
	s, root := newTestStore(t)
	reused, err := s.publish(".staging/x", "NOT-A-SHA1")
	if reused || !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("publish(bad key) = (%v,%v), want false and ErrInvalidKey", reused, err)
	}
	if !strings.Contains(err.Error(), "filestore: publish") {
		t.Fatalf("error text = %q, want the publish attribution", err)
	}
	stagingEmpty(t, root)
}

// TestOpenPreCancelledRejected 断言预取消 ctx 在任何路径解析之前拒绝。
// TestOpenPreCancelledRejected asserts a precanceled ctx rejects before any
// path resolution.
func TestOpenPreCancelledRejected(t *testing.T) {
	s, root := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rc, err := s.Open(ctx, abcSHA1)
	if rc != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Open(canceled) = (%v,%v), want nil reader and context.Canceled", rc, err)
	}
	stagingEmpty(t, root)
}

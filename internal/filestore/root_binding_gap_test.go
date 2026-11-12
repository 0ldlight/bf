package filestore

// 本文件覆盖 New 根绑定的剩余故障分支：空路径、根是普通文件、路径解析
// 出现非"不存在"错误（组件名超长）与祖先为悬空符号链接时的创建失败。
// 权限拒绝类分支（EACCES）依赖非 root 运行环境，按包内先例保持不测。
// This file covers the remaining New root-binding fault branches: an empty
// path, a regular-file root, a path resolution error other than "not exist"
// (an overlong component) and the create failure when an ancestor is a
// dangling symlink. Permission-denial branches (EACCES) depend on a non-root
// environment and stay untested per package precedent.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestNewEmptyRootRejected 断言空路径以固定错误拒绝，不触文件系统。
// TestNewEmptyRootRejected asserts the empty path rejects with the fixed
// error without touching the filesystem.
func TestNewEmptyRootRejected(t *testing.T) {
	s, err := New("")
	if s != nil || err == nil || err.Error() != "filestore: empty root" {
		t.Fatalf("New(\"\") = (%v,%v), want nil store and the fixed empty-root error", s != nil, err)
	}
}

// TestNewRootRegularFileRejected 断言存在但非目录的根以 ErrUnsafePath
// 拒绝且不产生任何副作用。
// TestNewRootRegularFileRejected asserts an existing non-directory root
// rejects with ErrUnsafePath and no side effects.
func TestNewRootRegularFileRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file-root")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if s != nil || !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("New(file) = (%v,%v), want nil store and ErrUnsafePath", s != nil, err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error text = %q, want the not-a-directory attribution", err)
	}
}

// TestNewRootResolutionErrorRejected 断言 Lstat 的非"不存在"错误（组件名
// 超长 → ENAMETOOLONG）原样进入检查失败分支，错误包装保留原因。
// TestNewRootResolutionErrorRejected asserts an Lstat error other than
// "not exist" (an overlong component → ENAMETOOLONG) takes the
// inspect-root branch with the cause preserved.
func TestNewRootResolutionErrorRejected(t *testing.T) {
	overlong := filepath.Join(t.TempDir(), strings.Repeat("a", 512))
	s, err := New(overlong)
	if s != nil || err == nil {
		t.Fatalf("New(overlong) = (%v,%v), want nil store and an error", s != nil, err)
	}
	if !strings.Contains(err.Error(), "inspect root") {
		t.Fatalf("error text = %q, want the inspect-root attribution", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlong component misclassified as not-exist: %v", err)
	}
}

// TestNewConcurrentSameRoot 断言并发初始化同一全新根全部成功：任一方
// 的 .staging ErrExist 冲突都经复查接受，最终 staging 为 0700 目录。
// TestNewConcurrentSameRoot asserts concurrent initialization of one fresh
// root always succeeds: any .staging ErrExist collision is accepted through
// the recheck, leaving a 0700 staging directory.
func TestNewConcurrentSameRoot(t *testing.T) {
	const concurrent = 16
	root := filepath.Join(t.TempDir(), "root")
	stores := make([]*Store, concurrent)
	errs := make([]error, concurrent)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			stores[i], errs[i] = New(root)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < concurrent; i++ {
		if errs[i] != nil || stores[i] == nil {
			t.Fatalf("concurrent New[%d] = (%v,%v)", i, stores[i] != nil, errs[i])
		}
		stores[i].Close()
	}
	fi, err := os.Lstat(filepath.Join(root, ".staging"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("staging after concurrent init = (%v,%v), want directory", fi, err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Fatalf("staging permissions = %o, want 0700", perm)
	}
}

// TestNewRootUnderDanglingSymlinkCreateFails 断言祖先为悬空符号链接时
// Lstat 报不存在、创建阶段失败：错误来自 MkdirAll 且根仍未建立。
// TestNewRootUnderDanglingSymlinkCreateFails asserts that with a dangling
// symlink ancestor Lstat reports not-exist while the create stage fails:
// the error comes from MkdirAll and the root is still absent.
func TestNewRootUnderDanglingSymlinkCreateFails(t *testing.T) {
	base := t.TempDir()
	if err := os.Symlink(filepath.Join(base, "missing"), filepath.Join(base, "dangling")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "dangling", "child")
	s, err := New(root)
	if s != nil || err == nil {
		t.Fatalf("New(dangling/child) = (%v,%v), want nil store and an error", s != nil, err)
	}
	if !strings.Contains(err.Error(), "create root") {
		t.Fatalf("error text = %q, want the create-root attribution", err)
	}
	if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("root stat = %v, want ErrNotExist (never created)", statErr)
	}
}

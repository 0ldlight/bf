package filestore

// 路径与权限安全表：root 别名/符号链接、staging/fanout/final 异常对象，
// 以及服务私有目录前提（本地安全策略，非目标权限对齐）。
// Path and permission safety tables: root aliases/symlinks, abnormal
// staging/fanout/final objects, and the service-private precondition (an
// explicit local safety policy, not target permissions parity).

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本地安全策略：root 不得被组/其他写（0022）；已存在目录不改权限。
// Local safety policy: the root must not be group/other writable (0022);
// existing directories are never chmodded.
func TestNewRootPermissionPolicy(t *testing.T) {
	cases := []struct {
		perm   os.FileMode
		reject bool
	}{
		{0o777, true},
		{0o770, true},
		{0o707, true},
		{0o755, false},
		{0o750, false},
		{0o700, false},
	}
	for _, tc := range cases {
		base := t.TempDir()
		root := filepath.Join(base, "root")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		// umask 会掩掉 Mkdir 模式，显式 chmod 设定策略输入。
		// umask masks Mkdir modes; chmod explicitly to set the input.
		if err := os.Chmod(root, tc.perm); err != nil {
			t.Fatal(err)
		}
		_, err := New(root)
		if tc.reject && !errors.Is(err, ErrUnsafePath) {
			t.Errorf("perm %o: err = %v, want ErrUnsafePath", tc.perm, err)
		}
		if !tc.reject && err != nil {
			t.Errorf("perm %o: err = %v, want accepted", tc.perm, err)
		}
		if fi, serr := os.Stat(root); serr == nil && fi.Mode().Perm() != tc.perm {
			t.Errorf("perm %o: root chmodded to %o", tc.perm, fi.Mode().Perm())
		}
	}
}

// 本地安全策略：.staging 不得有任何组/其他访问位（0077）。
// Local safety policy: .staging must carry no group/other access bits (0077).
func TestNewStagingPermissionPolicy(t *testing.T) {
	cases := []struct {
		perm   os.FileMode
		reject bool
	}{
		{0o777, true},
		{0o755, true},
		{0o750, true},
		{0o700, false},
	}
	for _, tc := range cases {
		base := t.TempDir()
		root := filepath.Join(base, "root")
		stage := filepath.Join(root, ".staging")
		if err := os.MkdirAll(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stage, tc.perm); err != nil { // 绕开 umask / bypass umask
			t.Fatal(err)
		}
		_, err := New(root)
		if tc.reject && !errors.Is(err, ErrUnsafePath) {
			t.Errorf("staging perm %o: err = %v, want ErrUnsafePath", tc.perm, err)
		}
		if !tc.reject && err != nil {
			t.Errorf("staging perm %o: err = %v, want accepted", tc.perm, err)
		}
	}
}

// fanout 异常：符号链接与组/其他可写的目录（本地策略 0022，与 root 一致）都
// 被 Put/Open 拒绝。
// Abnormal fanouts: symlinks and group/other-writable directories (local
// policy 0022, matching the root) are rejected by both Put and Open.
func TestFanoutUnsafe(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		base := t.TempDir()
		target := filepath.Join(base, "t")
		if err := os.WriteFile(target, []byte("t"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, root := newTestStore(t)
		if err := os.Symlink(target, filepath.Join(root, "a9")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Put err = %v, want ErrUnsafePath", err)
		}
		stagingEmpty(t, root)
		if _, err := s.Open(t.Context(), abcSHA1); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Open err = %v, want ErrUnsafePath", err)
		}
	})
	t.Run("perms", func(t *testing.T) {
		for _, tc := range []struct {
			perm   os.FileMode
			reject bool
		}{
			{0o777, true},
			{0o770, true},
			{0o706, true},
			{0o755, false},
			{0o700, false},
		} {
			s, root := newTestStore(t)
			if err := os.Mkdir(filepath.Join(root, "a9"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(root, "a9"), tc.perm); err != nil { // 绕开 umask / bypass umask
				t.Fatal(err)
			}
			_, perr := s.Put(t.Context(), strings.NewReader("abc"), Expected{})
			_, oerr := s.Open(t.Context(), abcSHA1)
			if tc.reject {
				if !errors.Is(perr, ErrUnsafePath) || !errors.Is(oerr, ErrUnsafePath) {
					t.Errorf("fanout perm %o: put=%v open=%v, want ErrUnsafePath both", tc.perm, perr, oerr)
				}
				stagingEmpty(t, root)
			} else if perr != nil {
				t.Errorf("fanout perm %o: Put = %v, want accepted", tc.perm, perr)
			}
			s.Close()
		}
	})
}

// final 异常：符号链接与目录被 Put/Open 拒绝，staging 清理，目标不被替换。
// Abnormal finals: symlinks and directories are rejected by Put and Open;
// staging cleaned and the link target never replaced.
func TestFinalObjectUnsafe(t *testing.T) {
	cases := map[string]func(base, fanDir string) error{
		"symlink": func(base, fanDir string) error {
			target := filepath.Join(base, "target")
			if err := os.WriteFile(target, []byte("target-bytes"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, filepath.Join(fanDir, abcSHA1))
		},
		"directory": func(_, fanDir string) error {
			return os.Mkdir(filepath.Join(fanDir, abcSHA1), 0o700)
		},
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
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
			if err := fixture(base, fanDir); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Put err = %v, want ErrUnsafePath", err)
			}
			stagingEmpty(t, root)
			if _, err := s.Open(t.Context(), abcSHA1); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Open err = %v, want ErrUnsafePath", err)
			}
			if b, rerr := os.ReadFile(filepath.Join(base, "target")); name == "symlink" && (rerr != nil || string(b) != "target-bytes") {
				t.Fatalf("symlink target = %q err=%v, want unchanged", b, rerr)
			}
		})
	}
}

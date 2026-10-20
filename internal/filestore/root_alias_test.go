package filestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// root 别名（尾部 "/"、"/."、"//"）不得绕过 root 符号链接拒绝策略。
// Root aliases (trailing "/", "/.", "//") must not bypass the root symlink
// rejection policy; the outside target stays untouched.
func TestNewRootSymlinkAliases(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(real, "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{link, link + "/", link + "/.", link + "//"} {
		if _, err := New(p); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("New(%q) err = %v, want ErrUnsafePath", p, err)
		}
	}
	ents, err := os.ReadDir(real)
	if err != nil || len(ents) != 1 || ents[0].Name() != "sentinel" {
		t.Fatalf("target after rejections = %+v err=%v, want only sentinel", ents, err)
	}
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "sentinel-bytes" {
		t.Fatalf("sentinel = %q err=%v, want unchanged", b, err)
	}
	for _, p := range []string{real, real + "/", real + "/."} {
		s, err := New(p)
		if err != nil {
			t.Fatalf("New(%q): %v", p, err)
		}
		s.Close()
	}
}

package filestore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

// 固定向量：空内容的三摘要与长度（与 checks_test 的 abc 向量互补）。
// Fixed vectors for empty content, complementing the abc vectors.
const (
	emptySHA1   = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	emptyMD5    = "d41d8cd98f00b204e9800998ecf8427e"
)

// newTestStore 在 t.TempDir() 里开一个 provider 并返回磁盘 root 路径。
// newTestStore opens a provider inside t.TempDir() and returns the on-disk root.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	s, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, root
}

// stagingEmpty 断言 .staging 里没有残留条目。
// stagingEmpty asserts no entries remain under .staging.
func stagingEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".staging"))
	if err != nil {
		t.Fatalf("ReadDir staging: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging has %d leftover entries, want 0", len(entries))
	}
}

func objectPath(root, sha1 string) string {
	return filepath.Join(root, sha1[:2], sha1)
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected FileInfo system data %T", fi.Sys())
	}
	return st.Ino
}

// errSource 总是返回错误，模拟调用方读取失败。
// errSource always fails, simulating a caller-side read error.
type errSource struct{}

func (errSource) Read([]byte) (int, error) { return 0, errors.New("test: source boom") }

func TestPutOpenFixedVectors(t *testing.T) {
	s, root := newTestStore(t)
	cases := []struct {
		name    string
		data    string
		sha1    string
		sha256  string
		md5     string
		extra   Expected
		wantObj string
	}{
		{
			name: "empty", data: "", sha1: emptySHA1, sha256: emptySHA256, md5: emptyMD5,
			wantObj: objectPath(root, emptySHA1),
		},
		{
			name: "abc", data: "abc", sha1: abcSHA1, sha256: abcSHA256, md5: abcMD5,
			wantObj: objectPath(root, abcSHA1),
		},
	}
	for _, tc := range cases {
		meta, err := s.Put(t.Context(), strings.NewReader(tc.data), tc.extra)
		if err != nil {
			t.Fatalf("%s: Put: %v", tc.name, err)
		}
		if meta.SHA1 != tc.sha1 || meta.SHA256 != tc.sha256 || meta.MD5 != tc.md5 {
			t.Fatalf("%s: meta = %+v, want digests %s/%s/%s", tc.name, meta, tc.sha1, tc.sha256, tc.md5)
		}
		if meta.Size != int64(len(tc.data)) {
			t.Fatalf("%s: size = %d, want %d", tc.name, meta.Size, len(tc.data))
		}
		got, err := os.ReadFile(tc.wantObj)
		if err != nil {
			t.Fatalf("%s: ReadFile %s: %v", tc.name, tc.wantObj, err)
		}
		if string(got) != tc.data {
			t.Fatalf("%s: object bytes = %q, want %q", tc.name, got, tc.data)
		}
		// Open 必须读回相同字节 / Open must read back identical bytes.
		rc, err := s.Open(t.Context(), tc.sha1)
		if err != nil {
			t.Fatalf("%s: Open: %v", tc.name, err)
		}
		back, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("%s: read back: %v", tc.name, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("%s: close: %v", tc.name, err)
		}
		if string(back) != tc.data {
			t.Fatalf("%s: Open bytes = %q, want %q", tc.name, back, tc.data)
		}
	}
}

// 大写期望摘要按 ChecksumInfo 规则归一后接受。
// Uppercase expected digests are accepted after normalization.
func TestPutUppercaseExpected(t *testing.T) {
	s, _ := newTestStore(t)
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{
		SHA1:   strings.ToUpper(abcSHA1),
		SHA256: strings.ToUpper(abcSHA256),
		MD5:    strings.ToUpper(abcMD5),
		Size:   sizePtr(3),
	})
	if err != nil {
		t.Fatalf("Put uppercase: %v", err)
	}
	if meta.SHA1 != abcSHA1 {
		t.Fatalf("meta.SHA1 = %s, want %s", meta.SHA1, abcSHA1)
	}
}

// 重新打开同一 root 后读取相同字节。
// Reopening the same root reads identical bytes.
func TestReopenReadsSameBytes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	s1, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s1.Put(t.Context(), strings.NewReader("abc"), Expected{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := New(root)
	if err != nil {
		t.Fatalf("New again: %v", err)
	}
	defer s2.Close()
	rc, err := s2.Open(t.Context(), abcSHA1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	back, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	rc.Close()
	if string(back) != "abc" {
		t.Fatalf("reopened bytes = %q, want %q", back, "abc")
	}
}

// 期望不匹配：零结果、无对象、staging 清空。
// Mismatched expectation: zero result, no object, empty staging.
func TestPutInvalidExpected(t *testing.T) {
	s, root := newTestStore(t)
	meta, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{SHA1: emptySHA1})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	if _, err := os.Stat(objectPath(root, abcSHA1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object stat err = %v, want ErrNotExist", err)
	}
	stagingEmpty(t, root)
}

// 源读取错误：零结果、无对象、staging 清空。
// Source read error: zero result, no object, empty staging.
func TestPutSourceError(t *testing.T) {
	s, root := newTestStore(t)
	meta, err := s.Put(t.Context(), errSource{}, Expected{})
	if err == nil {
		t.Fatal("err = nil, want source error")
	}
	if meta != (content.Metadata{}) {
		t.Fatalf("meta = %+v, want zero", meta)
	}
	if _, err := os.Stat(objectPath(root, abcSHA1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object stat err = %v, want ErrNotExist", err)
	}
	stagingEmpty(t, root)
}

// 顺序 Put 同内容复用已存在对象且保留 inode。
// Sequential same-content Put reuses the object and retains its inode.
func TestSequentialReuseRetainsInode(t *testing.T) {
	s, root := newTestStore(t)
	if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{}); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	path := objectPath(root, abcSHA1)
	ino1 := inodeOf(t, path)
	if _, err := s.Put(t.Context(), strings.NewReader("abc"), Expected{}); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if got := inodeOf(t, path); got != ino1 {
		t.Fatalf("inode = %d after reuse, want %d", got, ino1)
	}
	stagingEmpty(t, root)
}

// 非法 Open 键被拒绝且不触碰文件系统。
// Invalid Open keys are rejected.
func TestOpenInvalidKey(t *testing.T) {
	s, _ := newTestStore(t)
	for _, key := range []string{"", "short", strings.ToUpper(abcSHA1), "z" + abcSHA1[1:]} {
		if _, err := s.Open(t.Context(), key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Open(%q) err = %v, want ErrInvalidKey", key, err)
		}
	}
}

// 缺失对象返回 os.ErrNotExist。
// Missing objects report os.ErrNotExist.
func TestOpenMissing(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.Open(t.Context(), emptySHA1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

// New 拒绝符号链接 root。
// New rejects a symlinked root.
func TestNewRejectsSymlinkRoot(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(link); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want ErrUnsafePath", err)
	}
}

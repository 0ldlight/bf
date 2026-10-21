package repository

import (
	"errors"
	"strings"
	"testing"
)

// 拒绝必须在归一化之前发生：原始非空白 parent 加空白 name 仍然非法，
// 即使 parent 归一化后会变为空。失败返回零 Path 与固定文案，不含输入。
// Rejection must happen before normalization: a raw nonblank parent with a
// blank name stays invalid even if the parent would normalize to empty.
// Failures return a zero Path with fixed text that never echoes input.
func TestNewPathRejectsBeforeNormalize(t *testing.T) {
	invalid := [][4]string{
		{"", "a/b", "c", ""},               // Java null/empty repo / 原始 null 仓库
		{" \t\r\u001c\u001f ", "", "", ""}, // all-blank repo / 全空白仓库
		{"\u2002", "x", "", ""},            // Unicode blank repo / Unicode 空白仓库
		{"repo", "/", "", ""},              // '/' parent, empty name / 斜杠父与空名
		{"repo", " /x/ ", "", ""},          // nonblank raw parent / 原始非空白父
		{"repo", "\u00a0", "", ""},         // NBSP parent is NOT blank / NBSP 父非空白
		{"repo", "\u0085", "", ""},         // NEL parent is NOT blank / NEL 父非空白
	}
	for _, tc := range invalid {
		repo, parent, name := tc[0], tc[1], tc[2]
		file := tc[3] != ""
		got, err := NewPath(repo, parent, name, file)
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("NewPath(%q,%q,%q,%v) error = %v, want ErrInvalidPath", repo, parent, name, file, err)
		}
		if got != (Path{}) {
			t.Errorf("NewPath(%q,%q,%q,%v) = %+v, want zero Path", repo, parent, name, file, got)
		}
	}
	long := strings.Repeat("x", 4096)
	_, err := NewPath("", long, long, true)
	if msg := err.Error(); strings.Contains(msg, long) || strings.Contains(msg, "x") {
		t.Errorf("error text %q leaks input bytes", msg)
	}
}

// 归一化仅限：Java String.trim（两端码元 <= U+0020）+ 父路径边界 '/'，
// 保留内部双斜杠、点段、反斜杠、百分号与名字中的斜杠；repo 字节不变。
// Normalization is only Java String.trim plus boundary '/' removal on the
// parent; internal double slashes, dot segments, backslashes, percent runs
// and name slashes survive, and repo bytes are retained verbatim.
func TestNewPathNormalization(t *testing.T) {
	cases := []struct {
		repo, parent, name   string
		file                 bool
		wantParent, wantName string
	}{
		{" repo ", "\t\n a/b \u0000", "\u001f c \r", true, "a/b", "c"},
		{"repo", "/a/b/", "c", false, "a/b", "c"},
		{"repo", "///", "c", true, "", "c"},
		{"repo", "a//b", "c", false, "a//b", "c"},
		{"repo", "./a/../b", "c", true, "./a/../b", "c"},
		{"repo", `a\b`, "c", false, `a\b`, "c"},
		{"repo", "a%2Fb", "c%20", true, "a%2Fb", "c%20"},
		{"repo", "a/b", "c/d", false, "a/b", "c/d"},
		{"repo", "\u00a0a\u00a0", "\u0085b\u0085", true, "\u00a0a\u00a0", "\u0085b\u0085"},
		{"repo", "\u2002a\u2002", "\u1680b\u1680", false, "\u2002a\u2002", "\u1680b\u1680"},
		{"repo", "a b/c d", " e/f ", true, "a b/c d", "e/f"},
	}
	for _, tc := range cases {
		p, err := NewPath(tc.repo, tc.parent, tc.name, tc.file)
		if err != nil {
			t.Fatalf("NewPath(%q,%q,%q,%v) unexpected error %v", tc.repo, tc.parent, tc.name, tc.file, err)
		}
		if p.Parent() != tc.wantParent {
			t.Errorf("Parent() = %q, want %q (input parent %q)", p.Parent(), tc.wantParent, tc.parent)
		}
		if p.Name() != tc.wantName {
			t.Errorf("Name() = %q, want %q (input name %q)", p.Name(), tc.wantName, tc.name)
		}
		if p.Repo() != tc.repo {
			t.Errorf("Repo() = %q, want repo bytes retained %q", p.Repo(), tc.repo)
		}
		if p.IsFile() != tc.file || p.IsFolder() == tc.file {
			t.Errorf("kind flags wrong for %v", tc.file)
		}
	}
}

// Key 排除 file/folder；同三个归一化字符串产生相同 Key；
// 仓库与组件区分大小写，且不受冒号/斜杠拼接碰撞影响。
// Key excludes file/folder; identical normalized components produce the
// same Key; identity is case sensitive and immune to colon/slash joins.
func TestKeyIdentity(t *testing.T) {
	filePath, err := NewPath("Repo", " a/b ", " c ", true)
	if err != nil {
		t.Fatal(err)
	}
	folderPath, err := NewPath("Repo", "a/b", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	if filePath.Key() != folderPath.Key() {
		t.Error("file and folder with the same components must share a Key")
	}
	otherCase, _ := NewPath("repo", "a/b", "c", false)
	if otherCase.Key() == folderPath.Key() {
		t.Error("repo identity must be case sensitive")
	}
	// 组件分隔：冒号/斜杠拼接不产生相同身份。
	// Component separation: colon/slash joins never collide.
	a, _ := NewPath("a:b", "c", "d", true)
	b, _ := NewPath("a", "b:c", "d", true)
	if a.Key() == b.Key() {
		t.Error("colon in different components must not collide")
	}
	c, _ := NewPath("a", "b", "c/d", true)
	d, _ := NewPath("a", "b/c", "d", true)
	if c.Key() == d.Key() {
		t.Error("parent/name slash split must not collide")
	}
}

// 根判定对归一化后的父与名使用 Java-blank；Kind 不影响根。
// IsRoot uses Java-blank on the normalized parent and name; kind is irrelevant.
func TestRootAndPathName(t *testing.T) {
	rootFile, err := NewPath("repo", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	rootBlank, err := NewPath("repo", "\u2001", "\u3000", false)
	if err != nil {
		t.Fatal(err)
	}
	if !rootFile.IsRoot() || !rootBlank.IsRoot() {
		t.Error("both blank parent+name combinations must be root")
	}
	// Unicode 空白保留：根组件原样保留，归一化不清除。
	// Unicode blanks are retained verbatim; normalization does not strip them.
	if got := rootBlank.Name(); got != "\u3000" {
		t.Errorf("root name = %q, want retained U+3000", got)
	}
	if got := rootBlank.PathName(); got != "\u3000" {
		t.Errorf("root PathName = %q, want name alone", got)
	}
	// 空串根与保留 Unicode 空白的根都 IsRoot，但 Key 不同：归一化不折叠
	// Unicode 空白身份。
	// The empty-string root and the retained Unicode-blank root are both
	// IsRoot yet carry different Keys: normalization never collapses
	// Unicode blank identity.
	if rootFile.Key() == rootBlank.Key() {
		t.Error("empty-string root and Unicode-blank root must have different Keys")
	}
	nested, _ := NewPath("repo", "/a/b/", "c", false)
	if nested.IsRoot() {
		t.Error("nonblank parent cannot be root")
	}
	if got, want := nested.PathName(), "a/b/c"; got != want {
		t.Errorf("PathName() = %q, want %q", got, want)
	}
	nb, _ := NewPath("repo", "\u00a0", "x", true)
	if nb.IsRoot() {
		t.Error("NBSP parent is not blank, so not root")
	}
	if got, want := nb.PathName(), "\u00a0/x"; got != want {
		t.Errorf("PathName() = %q, want %q", got, want)
	}
}

// 深度：根 0；空白父 1；否则归一化父内斜杠计数 +2。
// 仅 Depth 检查 32767 上界；构造器接受过深的值。
// Depth: root 0; blank parent 1; otherwise slash count of the normalized
// parent plus 2. Only Depth enforces the 32767 bound; the constructor
// accepts deeper values.
func TestDepth(t *testing.T) {
	root, _ := NewPath("repo", "", "", true)
	if d, err := root.Depth(); d != 0 || err != nil {
		t.Errorf("root Depth() = %d, %v", d, err)
	}
	blankParent, _ := NewPath("repo", "\u2001", "x", false)
	if d, err := blankParent.Depth(); d != 1 || err != nil {
		t.Errorf("blank-parent Depth() = %d, %v", d, err)
	}
	slashTrimmed, _ := NewPath("repo", "////", "x", false)
	if d, err := slashTrimmed.Depth(); d != 1 || err != nil {
		t.Errorf("boundary-slash parent Depth() = %d, %v", d, err)
	}
	nested, _ := NewPath("repo", "/a//b/c/", "d/e", true)
	if d, err := nested.Depth(); d != 5 || err != nil {
		t.Errorf("Depth() = %d, %v, want 5 (3 internal slashes + 2)", d, err)
	}
	atBound, err := NewPath("repo", strings.Repeat("a/", 32765)+"a", "z", true)
	if err != nil {
		t.Fatalf("constructor must accept a depth-32767 parent: %v", err)
	}
	if d, err := atBound.Depth(); d != 32767 || err != nil {
		t.Errorf("Depth() = %d, %v, want 32767", d, err)
	}
	overBound, err := NewPath("repo", strings.Repeat("a/", 32766)+"a", "z", true)
	if err != nil {
		t.Fatalf("constructor must accept a too-deep parent: %v", err)
	}
	if d, err := overBound.Depth(); d != 0 || !errors.Is(err, ErrDepthExceeded) {
		t.Errorf("Depth() = %d, %v, want 0, ErrDepthExceeded", d, err)
	}
	if msg := ErrDepthExceeded.Error(); strings.Contains(msg, "a/") {
		t.Errorf("depth error text %q leaks input", msg)
	}
}

// 零值不是合法构造值：构造器出错返回零值，但 Go var/字面量同样能写出
// 零值，语言本身不阻止；契约要求消费者使用成功构造的 Path。
// Zero values are invalid constructed values: constructors return zero on
// error, yet Go var/literals also produce them and the language does not
// prevent it; the contract requires successfully constructed paths.
func TestZeroValues(t *testing.T) {
	var zero Path
	if !zero.IsRoot() {
		t.Error("zero Path is blank-rooted; consumers must not rely on this")
	}
	var zeroKey Key
	// 成功构造的值（含空父路径）也不等于零 Key：repo 非空白即可构造。
	// Even a successfully constructed blank-parent value differs from the
	// zero Key: a nonblank repo alone is constructible.
	blankParent, err := NewPath("r", "", "x", true)
	if err != nil {
		t.Fatal(err)
	}
	if blankParent.Key() == zeroKey {
		t.Error("constructed Key must differ from the zero Key")
	}
	// 失败构造按契约返回零值，与语言零值一致。
	// Failed construction returns the zero value, identical to the
	// language zero.
	failed, err := NewPath("", "a", "b", true)
	if err == nil || failed != zero {
		t.Error("failed construction must return the zero Path with an error")
	}
}

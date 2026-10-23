package repository

import "testing"

// 构造器画像（2 参 RepoPath 构造器）：repo 记 java trim（code unit ≤
// U+0020）后不得为空；path 先 java trim 再剥首尾斜杠；folder 标记取自
// 原始 path 的尾斜杠（trim 之前）。
// Constructor profile (two-argument RepoPath constructor): repo records
// the Java trim (code units ≤ U+0020) and must not become empty; path is
// Java-trimmed then boundary-slash-stripped; the folder flag comes from
// the RAW path's trailing slash, before any trimming.
func TestNewRawRepoPathConstructorProfile(t *testing.T) {
	cases := []struct {
		name       string
		repo, path string
		wantOK     bool
		wantRepo   string
		wantPath   string
		wantFolder bool
	}{
		{"plain", "repo", "a/b", true, "repo", "a/b", false},
		{"repo trimmed both sides", " repo ", "f", true, "repo", "f", false},
		{"repo boundary nul trimmed", "\x00repo\x00", "f", true, "repo", "f", false},
		{"repo inner nul kept", "re\x00po", "f", true, "re\x00po", "f", false},
		{"repo u2003 kept", " repo", "f", true, " repo", "f", false},
		{"repo nbsp kept", " repo", "f", true, " repo", "f", false},
		{"repo tab nul space trims empty", "\t\x00 ", "f", false, "", "", false},
		{"repo empty", "", "f", false, "", "", false},
		{"path leading slash", "repo", "/a/b", true, "repo", "a/b", false},
		{"path trailing slash", "repo", "a/b/", true, "repo", "a/b", true},
		{"path trimmed then slash stripped", "repo", " /a/ ", true, "repo", "a", false},
		{"path boundary nul trimmed", "repo", "\x00a\x00", true, "repo", "a", false},
		{"path inner nul kept", "repo", "a\x00b", true, "repo", "a\x00b", false},
		{"path only slashes", "repo", "///", true, "repo", "", true},
		{"path only spaces", "repo", "   ", true, "repo", "", false},
		{"path empty", "repo", "", true, "repo", "", false},
		{"folder flag from raw before trim", "repo", "a//", true, "repo", "a", true},
		{"folder flag kept when inner trailing", "repo", "a/ /", true, "repo", "a/ ", true},
	}
	for _, tc := range cases {
		got, ok := newRawRepoPath(tc.repo, tc.path)
		if ok != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !tc.wantOK {
			continue
		}
		if got.repoKey != tc.wantRepo || got.path != tc.wantPath || got.folder != tc.wantFolder {
			t.Errorf("%s: got {repo %q path %q folder %v}, want {repo %q path %q folder %v}",
				tc.name, got.repoKey, got.path, got.folder, tc.wantRepo, tc.wantPath, tc.wantFolder)
		}
	}
}

// force-file 重建（toFileRepoPath）：带 folder 标志的值（含根 folder，
// 如 ' /'）用 (repoKey, path) 走两参构造器重建——规范化 path 不以
// '/' 结尾，重建后的标志恒为 file；已是 file 的值（含根 file）原样
// 保留同一值。
// Force-file rebuild (toFileRepoPath): a folder-flagged value (root
// folders included, e.g. ' /') rebuilds through the two-argument
// constructor with (repoKey, path) — a normalized path never ends with
// '/', so the rebuilt flag is always file; an already-file value (root
// files included) keeps the same value untouched.
func TestRawRepoPathForceFile(t *testing.T) {
	folder, _ := newRawRepoPath("repo", "a/b/")
	forced := folder.forceFile()
	if forced.repoKey != "repo" || forced.path != "a/b" || forced.folder {
		t.Errorf("folder forced: got {repo %q path %q folder %v}", forced.repoKey, forced.path, forced.folder)
	}
	sameObj, _ := newRawRepoPath("repo", "a/b ")
	if got := sameObj.forceFile(); got != sameObj {
		t.Errorf("file path must keep same value, got %+v", got)
	}
	root, _ := newRawRepoPath("repo", " /")
	if got := root.forceFile(); got.repoKey != "repo" || got.path != "" || got.folder {
		t.Errorf("root folder must rebuild into a root file, got %+v", got)
	}
	spaceFolder, _ := newRawRepoPath("repo", "/ a/")
	if got := spaceFolder.forceFile(); got.path != "a" {
		t.Errorf("'/ a/' forced: path %q, want %q", got.path, "a")
	}
}

// getName（RepoPath.getName = stripMetadataFromPath(getFileName(path))）：
// 分隔符同时认 '/' 与 '\\'；NUL 在整串任意位置即拒绝（第二个返回值
// false）；剥离只发生在最后一个 ':' 且整体受 properties/statistics/
// maven-metadata 守卫；不递归。
// getName (RepoPath.getName = stripMetadataFromPath(getFileName(path))): both
// '/' and '\\' act as separators; a NUL anywhere in the string rejects
// (second return false); stripping happens only at the last ':' and only
// under the properties/statistics/maven-metadata guard; never recursive.
func TestRepoPathGetName(t *testing.T) {
	cases := []struct {
		path   string
		want   string
		wantOK bool
	}{
		{"f", "f", true},
		{"a/b", "b", true},
		{`a\b`, "b", true},
		{`a/b\c`, "c", true},
		{"a/ b", " b", true},
		{"a//b", "b", true},
		{"a\\b/", "b", true},
		{"a/\x00b", "", false},
		{"\x00a/b", "", false},
		{"a/f:properties", "f", true},
		{"a/f:statistics", "f", true},
		{"a/f:properties:statistics", "f:properties", true},
		{"a/f::properties", "f:", true},
		{"a/f:meta", "f:meta", true},
		{"a/f:Properties", "f:Properties", true},
		{"a/f:maven-metadata.xml", "f", true},
		{"maven-metadata.xml", "maven-metadata.xml", true},
		{"a/maven-metadata.xml", "maven-metadata.xml", true},
		{"a/f:maven-metadata.xml.sha1", "f:maven-metadata.xml.sha1", true},
		{"a/:properties", "", true},
		{"f:properties ", "f:properties ", true},
		{"a/", "a", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, ok := repoPathGetName(tc.path)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("path %q: got (%q, %v), want (%q, %v)", tc.path, got, ok, tc.want, tc.wantOK)
		}
	}
	// 边界 NUL 已被构造器 trim；getName 内不会遇到它们。
	// Boundary NULs are already trimmed by the constructor and never reach
	// getName.
}

// getFileName 语义（PathUtils.getFileName）：剥尾部 '/'+'\\' 后按两种
// 分隔符取最后段；NUL 在剥尾分隔符后对剥后整串判定。
// getFileName semantics (PathUtils.getFileName): strip trailing '/'+'\\'
// then take the last segment over both separators; NUL is judged over the
// whole stripped string after trailing-separator removal.
func TestPathUtilsGetFileName(t *testing.T) {
	cases := []struct {
		path   string
		want   string
		wantOK bool
	}{
		{"f", "f", true},
		{"a/b", "b", true},
		{`a\b`, "b", true},
		{"a/b/", "b", true},
		{"a/b\\", "b", true},
		{`/`, "", true},
		{"//", "", true},
		{"a/\x00b", "", false},
		{"\x00a", "", false},
		{"a/\x00", "", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got, ok := pathUtilsGetFileName(tc.path)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("path %q: got (%q, %v), want (%q, %v)", tc.path, got, ok, tc.want, tc.wantOK)
		}
	}
}

// getParent（java.io.File Unix 语义 + formatPath）：'//' 折叠、尾斜杠剥
// 离、反斜杠转正斜杠后再折叠；前导斜杠父为 '/'；不解析点段；无父得空。
// getParent (java.io.File Unix semantics + formatPath): '//' collapses, the
// trailing slash drops, backslashes become slashes before folding; a
// leading slash leaves parent '/'; dot segments stay unresolved; no
// parent yields empty.
func TestPathUtilsGetParent(t *testing.T) {
	// 期望值来自冻结 JVM 语料（helperParent 锚点行）与本机 JDK
	// java.io.File 观测：'//' 折叠、尾 '/' 剥离、'.'/末段点保留、
	// 反斜杠在 getParent 之后才由 formatPath 转正斜杠。
	// Expectations come from the frozen JVM corpus (helperParent anchor
	// rows) plus local JDK java.io.File observation: '//' collapses, a
	// trailing '/' drops, dot segments stay unresolved, and backslashes
	// become '/' only afterwards in formatPath.
	cases := []struct {
		path string
		want string
	}{
		{"f", ""},
		{"a/b", "a"},
		{"a/b/c", "a/b"},
		{"a//b", "a"},
		{"a//b//c", "a/b"},
		{`a\b`, ""},
		{`a\b/c`, "a/b"},
		{`a/b\c`, "a"},
		{`a\\b`, ""},
		{"//a/b", "/a"},
		{"/a", "/"},
		{"a/", ""},
		{"//", ""},
		{"/", ""},
		{"", ""},
		{"a/./b", "a/."},
		{"a/../b", "a/.."},
		{"a/.", "a"},
		{"a/..", "a"},
		{"/./f", "/."},
		{"./f", "."},
		{"../f", ".."},
		{"a/..x/b", "a/..x"},
		{"a:properties/f", "a:properties"},
		{"a/ b", "a"},
		{"a /b", "a "},
	}
	for _, tc := range cases {
		if got := pathUtilsGetParent(tc.path); got != tc.want {
			t.Errorf("path %q: parent %q, want %q", tc.path, got, tc.want)
		}
	}
}

// 映射身份：GetName/getParent 的输出直接喂给既有 NewPath(repo,parent,
// name,file)，映射语义与 NodePath 构造一致；空 name 且非空 path 由
// NewPath 拒绝，属于取 ID 之后的阶段。
// Mapping identity: getName/getParent outputs feed the existing
// NewPath(repo, parent, name, file) directly, matching the NodePath
// constructor; an empty name with a nonempty path is rejected by NewPath
// and belongs to the post-allocation stage.
func TestHelperOutputsMapThroughNewPath(t *testing.T) {
	type want struct {
		repo, parent, name string
		ok                 bool
	}
	cases := []struct {
		rawPath string
		want    want
	}{
		{"f", want{"repo", "", "f", true}},
		{"a/f:properties", want{"repo", "a", "f", true}},
		{"a/f:maven-metadata.xml", want{"repo", "a", "f", true}},
		{"a///b/c", want{"repo", "a/b", "c", true}},
		{"a/:properties", want{"repo", "", "", false}},
	}
	for _, tc := range cases {
		raw, ok := newRawRepoPath("repo", tc.rawPath)
		if !ok {
			t.Fatalf("path %q: constructor rejected", tc.rawPath)
		}
		forced := raw.forceFile()
		name, nok := repoPathGetName(forced.path)
		if !nok {
			t.Fatalf("path %q: getName rejected", tc.rawPath)
		}
		parent := pathUtilsGetParent(forced.path)
		_, err := NewPath(forced.repoKey, parent, name, true)
		if tc.want.ok && err != nil {
			t.Errorf("path %q: NewPath rejected: %v", tc.rawPath, err)
		}
		if !tc.want.ok && err == nil {
			t.Errorf("path %q: NewPath accepted, want rejection", tc.rawPath)
		}
		if err == nil {
			p, _ := NewPath(forced.repoKey, parent, name, true)
			if p.Parent() != tc.want.parent || p.Name() != tc.want.name {
				t.Errorf("path %q: mapped %q/%q, want %q/%q", tc.rawPath, p.Parent(), p.Name(), tc.want.parent, tc.want.name)
			}
		}
	}
}

// Package repository 普通文件创建的路径侧内部 helper：复刻目标
// RepoPath 构造、force-file、getName/getParent 与受守卫的 metadata
// 剥离语义；语义锚点来自冻结的 javap 反汇编与 135 行 JVM 语料（含
// java.io.File 在 macOS Unix JVM 上的观测），本文件不复制目标源码。
// Package repository — internal path-side helpers for ordinary file
// creation: they replicate the target RepoPath construction, force-file,
// getName/getParent and the guarded metadata strip semantics; the anchors
// come from the frozen javap disassembly and the 135-row JVM corpus
// (including java.io.File observation on the macOS Unix JVM). No target
// source is copied into this file.
package repository

import "strings"

// rawRepoPath 是目标两参 RepoPath 构造器产物：repoKey 已 trim、path 已
// trim 且剥边界斜杠、folder 标志取自原始 path 的尾斜杠。
// rawRepoPath is the two-argument RepoPath constructor result: repoKey
// trimmed, path trimmed with boundary slashes stripped, and the folder
// flag taken from the RAW path's trailing slash.
type rawRepoPath struct {
	repoKey string
	path    string
	folder  bool
}

// newRawRepoPath 复刻两参构造器：repo 经 Java String.trim 后不得为空；
// path 先 Java trim 再仅剥边界 '/'；folder 标志在一切 trim 之前按原始
// path 是否以 '/' 结尾记录。第二返回值 false 对应构造期拒绝。
// newRawRepoPath replicates the two-argument constructor: repo must be
// nonempty after the Java String.trim; path is Java-trimmed then
// boundary-'/'-stripped only; the folder flag is recorded from the RAW
// path's trailing slash before any trimming. A false second return marks
// the construction-time rejection.
func newRawRepoPath(repo, rawPath string) (rawRepoPath, bool) {
	key := javaTrim(repo)
	if key == "" {
		return rawRepoPath{}, false
	}
	return rawRepoPath{
		repoKey: key,
		path:    trimSlashes(javaTrim(rawPath)),
		folder:  strings.HasSuffix(rawPath, "/"),
	}, true
}

// forceFile 复刻 toFileRepoPath 的 source 顺序语义：file 值（含根 file）
// 原样保留；folder 值用 (repoKey, path) 走两参构造器重建——规范化 path
// 不以 '/' 结尾，重建结果恒为 file，且第二次 trim 还会吸收内层暴露出的
// 边界空白（语料行 18/32：'/ a/' 与 '/a /' 重建为 'a'）。
// forceFile replicates the toFileRepoPath source-order semantics: file
// values (root files included) stay as-is; folder values rebuild through
// the two-argument constructor with (repoKey, path) — a normalized path
// never ends with '/', so the rebuilt flag is always file, and the second
// trim also absorbs boundary whitespace newly exposed inside (corpus rows
// 18/32: '/ a/' and '/a /' rebuild into 'a').
func (r rawRepoPath) forceFile() rawRepoPath {
	if !r.folder {
		return r
	}
	forced, ok := newRawRepoPath(r.repoKey, r.path)
	if !ok {
		// repoKey 已非空且 javaTrim 幂等，重建不可能失败；防御性保底。
		// repoKey is already nonempty and javaTrim is idempotent, so the
		// rebuild cannot fail; kept as a defensive fallback.
		return rawRepoPath{repoKey: r.repoKey, path: r.path}
	}
	return forced
}

// pathUtilsGetFileName 复刻 PathUtils.getFileName：先剥尾部 '/'+'\\'
// 码元，再走 commons-io FilenameUtils.getName —— 整串任意位置含 NUL 即
// 拒绝（第二返回值 false），随后取 '/' 与 '\\' 两种分隔符的最后一段。
// pathUtilsGetFileName replicates PathUtils.getFileName: strip trailing
// '/'+'\\' code units first, then commons-io FilenameUtils.getName — a
// NUL anywhere in the (stripped) string rejects (second return false),
// and the last segment over both separators wins.
func pathUtilsGetFileName(p string) (string, bool) {
	end := len(p)
	for end > 0 && (p[end-1] == '/' || p[end-1] == '\\') {
		end--
	}
	stripped := p[:end]
	if strings.IndexByte(stripped, 0) >= 0 {
		return "", false
	}
	idx := strings.LastIndexByte(stripped, '/')
	if back := strings.LastIndexByte(stripped, '\\'); back > idx {
		idx = back
	}
	return stripped[idx+1:], true
}

// repoPathGetName 复刻 RepoPath.getName =
// stripMetadataFromPath(PathUtils.getFileName(path))；NUL 拒绝由文件名
// helper 传出。
// repoPathGetName replicates RepoPath.getName =
// stripMetadataFromPath(PathUtils.getFileName(path)); the NUL rejection
// propagates from the file-name helper.
func repoPathGetName(p string) (string, bool) {
	name, ok := pathUtilsGetFileName(p)
	if !ok {
		return "", false
	}
	return stripMetadataFromPath(name), true
}

// stripMetadataFromPath 复刻 NamingUtils.stripMetadataFromPath：只在
// 最后一个 ':' 处切一刀，且仅当整串被判定为 properties/statistics/
// Maven metadata 时；不递归（'f:properties:statistics' 得 'f:properties'）。
// stripMetadataFromPath replicates NamingUtils.stripMetadataFromPath: a
// single cut at the LAST colon, applied only when the whole string is
// classified as properties/statistics/Maven metadata; never recursive
// ('f:properties:statistics' yields 'f:properties').
func stripMetadataFromPath(s string) string {
	i := strings.LastIndexByte(s, ':')
	if i >= 0 && isPropertiesOrStatisticsOrMavenMetadata(s) {
		return s[:i]
	}
	return s
}

// isPropertiesOrStatisticsOrMavenMetadata 复刻同名私有守卫：空文件名不
// 判定；无 ':' 不判定；否则文件名须以 ':properties'/':statistics' 结尾
// 或为 Maven metadata 文件名。NUL 输入在上层 getName 已被拒绝，此处不
// 会见到（守卫内的 getFileName 抛错不可达）。
// isPropertiesOrStatisticsOrMavenMetadata replicates the same private
// guard: an empty file name never classifies; without a colon never;
// otherwise the file name must end with ':properties'/':statistics' or be
// a Maven metadata file name. NUL input is already rejected at the outer
// getName layer, so the guard's own getFileName throw is unreachable here.
func isPropertiesOrStatisticsOrMavenMetadata(path string) bool {
	fileName, ok := pathUtilsGetFileName(path)
	if !ok || fileName == "" {
		return false
	}
	if !strings.Contains(fileName, ":") {
		return false
	}
	return strings.HasSuffix(fileName, ":properties") ||
		strings.HasSuffix(fileName, ":statistics") ||
		isMavenMetadataFileName(fileName)
}

// isMavenMetadataFileName 复刻 MavenNaming.isMavenMetadataFileName：等于
// "maven-metadata.xml" 或以 ":maven-metadata.xml" 结尾。
// isMavenMetadataFileName replicates MavenNaming.isMavenMetadataFileName:
// equal to "maven-metadata.xml" or ending with ":maven-metadata.xml".
func isMavenMetadataFileName(f string) bool {
	return f == "maven-metadata.xml" || strings.HasSuffix(f, ":maven-metadata.xml")
}

// pathUtilsGetParent 复刻 PathUtils.getParent =
// formatPath(java.io.File(path).getParent())：先按 java.io.File（macOS
// Unix JVM 观测）归一化并取父，再由 formatPath 把 '\' 转正斜杠并折叠
// 斜杠重复；无父得空串。
// pathUtilsGetParent replicates PathUtils.getParent =
// formatPath(java.io.File(path).getParent()): normalize and take the
// java.io.File parent first (observed on the macOS Unix JVM), then
// formatPath turns '\' into '/' and collapses repeated slashes; no parent
// yields the empty string.
func pathUtilsGetParent(p string) string {
	parent, ok := javaFileGetParent(p)
	if !ok {
		return ""
	}
	return formatRepoParent(parent)
}

// javaFileGetParent 复刻观测到的 java.io.File Unix 语义：'//' 重复折叠、
// 尾 '/' 剥离（整串 '/' 除外）；'/' 自身无父；最后一个 '/' 之前为父——
// '/x' 的父是 '/'，'./x' 的父是 '.'。
// javaFileGetParent replicates the observed java.io.File Unix semantics:
// '//' runs collapse, a trailing '/' drops (unless the whole string is
// '/'); '/' itself has no parent; the parent is the prefix before the
// last '/' — the parent of '/x' is '/' and of './x' is '.'.
func javaFileGetParent(p string) (string, bool) {
	n := javaFileNormalize(p)
	if n == "/" {
		return "", false
	}
	idx := strings.LastIndexByte(n, '/')
	if idx < 0 {
		return "", false
	}
	if idx == 0 {
		if n[0] == '/' {
			return "/", true
		}
		return ".", true
	}
	return n[:idx], true
}

// javaFileNormalize 复刻观测到的 java.io.File 路径归一化：'//' 折叠为
// '/'，再剥一个尾 '/'。
// javaFileNormalize replicates the observed java.io.File path
// normalization: '//' runs collapse into '/', then a single trailing '/'
// drops.
func javaFileNormalize(p string) string {
	if p == "" {
		return ""
	}
	out := collapseSlashRuns(p)
	if len(out) > 1 && out[len(out)-1] == '/' {
		out = out[:len(out)-1]
	}
	return out
}

// formatRepoParent 复刻 PathUtils.formatPath：blank 输入得空串；否则
// '\' 转正 '/' 后折叠 '/+' 重复。
// formatRepoParent replicates PathUtils.formatPath: blank input yields
// the empty string; otherwise '\' becomes '/' and '/+' runs collapse.
func formatRepoParent(s string) string {
	if javaBlank(s) {
		return ""
	}
	return collapseSlashRuns(strings.ReplaceAll(s, "\\", "/"))
}

// collapseSlashRuns 把每个 '/' 重复段折叠为单个 '/'。
// collapseSlashRuns folds every run of '/' into a single '/'.
func collapseSlashRuns(s string) string {
	if !strings.Contains(s, "//") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '/' && prev == '/' {
			continue
		}
		b.WriteByte(c)
		prev = c
	}
	return b.String()
}

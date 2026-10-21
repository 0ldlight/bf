// Package repository 提供仓库节点路径的身份值模型。
// Package repository provides the identity value model for repository
// node paths.
package repository

import (
	"errors"
	"strings"
	"unicode"
)

// maxDepth 对齐目标 NodePath.getDepth 的延迟上界（Short.MAX_VALUE）。
// maxDepth mirrors the deferred bound of the target NodePath.getDepth.
const maxDepth = 32767

// ErrInvalidPath 是构造器拒绝输入的固定哨兵错误，文本不含任何输入字节。
// ErrInvalidPath is the exported sentinel returned by NewPath; its fixed
// text never echoes input bytes.
var ErrInvalidPath = errors.New("repository: invalid node path")

// ErrDepthExceeded 是 Depth 超过上界时的固定哨兵错误。
// ErrDepthExceeded is the fixed sentinel for Depth beyond the bound.
var ErrDepthExceeded = errors.New("repository: node path depth exceeds the supported bound")

// Key 是节点路径的身份：仓库、归一化父路径与归一化名字三个字符串。
// 字段不导出且 Key 可比较；file/folder 不参与身份，同三个归一化字符串
// 的两种 Kind 得到相同 Key。区分大小写，不做拼接串身份，避免冒号/斜杠
// 分隔符碰撞。零 Key 不是合法构造结果。
// Key is the node path identity: repo, normalized parent and normalized
// name. Fields are unexported and Key is comparable; file/folder is excluded
// so both kinds share one Key for the same three normalized strings. It is
// case sensitive, uses no joined-string identity (no colon/slash delimiter
// collisions), and the zero Key is not a valid construction result.
type Key struct {
	repo   string
	parent string
	name   string
}

// Path 是带 Kind 的节点路径，持有未导出的 Key 与 file 标志。
// 零 Path/Key 不是合法构造值：构造器出错返回零值；Go 的 var/字面量同样
// 能写出零值，语言本身并不阻止，消费者必须使用 NewPath 成功构造的值。
// 本切片没有运行期持久化消费者。构造器不提供认证、安全或路径穿越保证。
// Path is a kind-carrying node path holding the unexported Key and file
// flag. Zero Path/Key values are invalid constructed values: constructors
// return zero on error, yet Go var/literals also produce zero values — the
// language does not prevent them — so consumers must use values from
// successful NewPath calls. This slice has no runtime persistence
// consumer. The constructor provides no auth, security or path-traversal
// guarantee.
type Path struct {
	key  Key
	file bool
}

// NewPath 在归一化之前拒绝 Java-blank 的 repo，或 Java-blank 的 name 且
// parent 非 Java-blank 的输入，返回零 Path 与 ErrInvalidPath。repo 字节
// 原样保留；parent 先做 Java String.trim（仅移除两端码元 <= U+0020），
// 再仅移除边界 '/'；name 只做同样的 Java trim。除此之外不做其他校验：
// 无 path.Clean、无 Go TrimSpace、无 URL 解码、无斜杠折叠、无小写化。
// Go 空串表示 Java null/空串边界，不用指针。畸形 UTF-8 不在目标 Java
// String 等价范围内，本层不额外拒绝。
// NewPath rejects, before normalization, a Java-blank repo, or a Java-blank
// name with a non-Java-blank parent, returning a zero Path and
// ErrInvalidPath. Repo bytes are retained verbatim; the parent gets Java
// String.trim (only code units <= U+0020 off both ends) and then boundary
// '/' removal only; the name gets the same Java trim only. No other
// validation exists at this internal layer: no path.Clean, no Go
// TrimSpace, no URL decoding, no slash collapsing, no lowercasing. A Go
// empty string represents the Java null/empty boundary without pointers.
// Malformed UTF-8 is outside the target Java String equivalence and is not
// additionally rejected here.
func NewPath(repo, parent, name string, file bool) (Path, error) {
	if javaBlank(repo) || (javaBlank(name) && !javaBlank(parent)) {
		return Path{}, ErrInvalidPath
	}
	return Path{key: Key{repo: repo, parent: trimSlashes(javaTrim(parent)), name: javaTrim(name)}, file: file}, nil
}

// Repo 返回原样保留的仓库标识。/ Repo returns the verbatim repo identity.
func (p Path) Repo() string { return p.key.repo }

// Parent 返回归一化后的父路径。/ Parent returns the normalized parent.
func (p Path) Parent() string { return p.key.parent }

// Name 返回归一化后的名字。/ Name returns the normalized name.
func (p Path) Name() string { return p.key.name }

// Key 返回与 Kind 无关的身份。/ Key returns the kind-independent identity.
func (p Path) Key() Key { return p.key }

// IsFile 报告文件 Kind。/ IsFile reports the file kind.
func (p Path) IsFile() bool { return p.file }

// IsFolder 报告文件夹 Kind。/ IsFolder reports the folder kind.
func (p Path) IsFolder() bool { return !p.file }

// IsRoot 报告归一化父路径与名字是否都为 Java-blank；Kind 不影响根。
// IsRoot reports whether the normalized parent and name are both Java-blank;
// the kind never affects rootness.
func (p Path) IsRoot() bool { return javaBlank(p.key.parent) && javaBlank(p.key.name) }

// PathName 在父路径 Java-blank 时返回名字，否则返回 parent+'/'+name。
// 有意不提供自定义 String()（Go 的 fmt 仍会默认打印结构体字段）：
// Path/Key 不是脱敏日志容器，诊断输出必须按信任边界过滤路径；固定错误
// 文案保持有界且不含输入。
// PathName returns the name alone when the parent is Java-blank, otherwise
// parent+'/'+name. No custom String() is provided (Go's fmt still prints
// struct fields by default): Path/Key are not redacted logging containers,
// diagnostics must filter paths according to the trust boundary; the fixed
// error texts remain bounded and input-free.
func (p Path) PathName() string {
	if javaBlank(p.key.parent) {
		return p.key.name
	}
	return p.key.parent + "/" + p.key.name
}

// Depth 返回路径深度：根为 0；父路径 Java-blank 为 1；否则为归一化父
// 路径（边界斜杠已移除）内 '/' 计数 +2。只有 Depth 检查 32767 上界：
// 构造器接受过深的值，Depth 对溢出返回 0 与 ErrDepthExceeded。
// Depth returns 0 for the root, 1 for a Java-blank parent, otherwise the
// count of '/' in the boundary-trimmed parent plus 2. Only Depth enforces
// the 32767 bound: the constructor accepts too-deep values and Depth
// returns 0 with ErrDepthExceeded on overflow.
func (p Path) Depth() (int16, error) {
	if p.IsRoot() {
		return 0, nil
	}
	if javaBlank(p.key.parent) {
		return 1, nil
	}
	depth := strings.Count(p.key.parent, "/") + 2
	if depth > maxDepth {
		return 0, ErrDepthExceeded
	}
	return int16(depth), nil
}

// javaTrim 复刻 Java String.trim：仅移除两端码元 <= U+0020 的字符；对
// 合法 UTF-8 与 UTF-16 码元判定等价（多字节序列的首字节均 >= 0x80）。
// javaTrim replicates Java String.trim: only code units <= U+0020 leave
// both ends; for valid UTF-8 this matches the UTF-16 unit test because
// multibyte sequences always start with a byte >= 0x80.
func javaTrim(s string) string {
	start, end := 0, len(s)
	for start < end && s[start] <= 0x20 {
		start++
	}
	for end > start && s[end-1] <= 0x20 {
		end--
	}
	return s[start:end]
}

// trimSlashes 仅移除两端 '/'，等价目标 PathUtils.trimSlashes。
// trimSlashes removes only boundary '/' runes, mirroring the target's
// PathUtils.trimSlashes.
func trimSlashes(s string) string {
	start, end := 0, len(s)
	for start < end && s[start] == '/' {
		start++
	}
	for end > start && s[end-1] == '/' {
		end--
	}
	return s[start:end]
}

// javaBlank 报告 s 是否为空或全为 Java Character.isWhitespace 空白：
// ASCII 9..13、28..31 与 Zs/Zl/Zp，但排除 NBSP(U+00A0)/U+2007/U+202F；
// NEL(U+0085) 不是空白。不使用 unicode.IsSpace。
// javaBlank reports whether s is empty or entirely Java
// Character.isWhitespace: ASCII 9..13, 28..31 and Zs/Zl/Zp minus the
// nonbreaking U+00A0/U+2007/U+202F; NEL U+0085 is not blank.
// unicode.IsSpace is deliberately not used.
func javaBlank(s string) bool {
	for _, r := range s {
		if !javaBlankRune(r) {
			return false
		}
	}
	return true
}

func javaBlankRune(r rune) bool {
	if r >= '\t' && r <= '\r' || r >= 0x1c && r <= 0x1f {
		return true
	}
	if unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp) {
		return r != 0xa0 && r != 0x2007 && r != 0x202f
	}
	return false
}

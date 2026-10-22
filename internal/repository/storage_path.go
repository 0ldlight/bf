package repository

import (
	"errors"
	"unicode/utf8"
)

// ErrInvalidStoragePath 是存储路径检查拒绝输入的固定哨兵错误，文本不含
// 任何输入字节、被拒字符或目标异常文案。
// ErrInvalidStoragePath is the fixed sentinel returned when the storage
// path checker rejects an input; its text never echoes input bytes, the
// rejected character or target exception texts.
var ErrInvalidStoragePath = errors.New("repository: invalid storage path")

// ValidateStoragePath 检查未来保存消费者选定的序列化存储路径
// （repoKey+'/'+path，非根文件夹末尾带 '/'）：接受返回 nil，拒绝返回固定
// 哨兵 ErrInvalidStoragePath。它只做目标存储名校定，不构造也不归一化
// RepoPath，NewPath/Key 身份构造器保持不变。规则：'/' 与字面 U+FFFF 是
// 分隔符转移（重复接受，EOF 执行同一终结检查）；分隔符处若当前待定状态
// 是空白/点/与号则拒绝；分隔符后紧跟 Java 空白拒绝；段中空白仅置空白
// 待定；'.'/'&' 在起点或分隔符后建立点/与号待定，其余位置保持原状态
// （粘滞）；'"'、'*'、'?'、'\\'、'|' 无条件拒绝；其余字符（含 ':'、'<'、
// '>'、'#'、';'、'%'、NUL 等控制字符、百分号串与增补平面字符）按普通
// 字符接受。不清洗、不归一化、不解码、不折叠斜杠。无效 UTF-8 属本地
// Go 边界策略显式拒绝（目标 Java String 等价范围之外）。接受不代表文件
// 系统安全、认证或防穿越保证。
// ValidateStoragePath checks the serialized storage path chosen by the
// future save consumer (repoKey+'/'+path, plus a trailing '/' for
// non-root folders): nil on accept, the fixed sentinel
// ErrInvalidStoragePath on rejection. It only judges the storage name; it
// neither builds nor normalizes a RepoPath, and the NewPath/Key identity
// constructor stays unchanged. Rules: '/' and the literal U+FFFF are
// delimiter transitions (repeats accepted, EOF performs the same final
// check); a delimiter rejects when the pending status is space, dot or
// ampersand; a Java whitespace char right after a delimiter rejects;
// whitespace inside a segment only makes space pending; '.'/'&' establish
// dot/ampersand pending at input start or right after a delimiter and
// otherwise LEAVE the previous status unchanged (sticky); '"', '*', '?',
// '\\', '|' reject unconditionally; every other character (':', '<', '>',
// '#', ';', '%', NUL and other controls, percent runs and supplementary
// plane characters) is ordinary. No cleaning, normalization, decoding or
// slash collapsing. Invalid UTF-8 is explicitly rejected as a LOCAL Go
// boundary policy (outside target Java String equivalence). Acceptance is
// not a filesystem-safety, auth or traversal guarantee.
func ValidateStoragePath(raw string) error {
	if !utf8.ValidString(raw) {
		return ErrInvalidStoragePath
	}
	// 扫描状态：起点 / 分隔符后 / 普通字符 / 空白·点·与号待定。
	// Scan status: input start / after delimiter / ordinary char /
	// pending space, dot or ampersand.
	const (
		atStart    = iota // 仅位置 0 / position 0 only
		afterDelim        // 分隔符后 / right after '/' or U+FFFF
		ordinary          // 段内普通字符 / ordinary char inside a segment
		pendingSpace
		pendingDot
		pendingAmp
	)
	status := atStart
	for _, r := range raw {
		switch {
		case r == '/' || r == 0xffff: // 分隔符转移 / delimiter transition
			if status == pendingSpace || status == pendingDot || status == pendingAmp {
				return ErrInvalidStoragePath
			}
			status = afterDelim
		case r == '"' || r == '*' || r == '?' || r == '\\' || r == '|':
			return ErrInvalidStoragePath
		case r == '.' || r == '&':
			if status == atStart || status == afterDelim {
				if r == '.' {
					status = pendingDot
				} else {
					status = pendingAmp
				}
			} // 其余位置粘滞不变 / else sticky, unchanged
		case javaBlankRune(r):
			if status == afterDelim { // 分隔符后紧跟空白 / blank right after delimiter
				return ErrInvalidStoragePath
			}
			status = pendingSpace
		default:
			status = ordinary
		}
	}
	// EOF 执行同一终结检查：待定或空输入拒绝。
	// EOF performs the same final check: pending status or empty input rejects.
	if status == pendingSpace || status == pendingDot || status == pendingAmp || status == atStart {
		return ErrInvalidStoragePath
	}
	return nil
}

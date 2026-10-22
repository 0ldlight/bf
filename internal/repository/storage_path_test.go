package repository

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateStoragePathTable 覆盖基础接受/拒绝边界：空/空白、重复分隔符、
// 粘滞点/与号/空白状态、绝对禁字符、上下文相关字符与序列化分隔符。
// TestValidateStoragePathTable covers base accept/reject boundaries:
// blank inputs, repeated delimiters, sticky dot/ampersand/space status,
// unconditionally forbidden characters, context-dependent characters and
// the serialized delimiters.
func TestValidateStoragePathTable(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool // want accept / 期望接受
	}{
		{"empty", "", false},
		{"space only", " ", false},
		{"slash root", "/", true},
		{"double slash", "//", true},
		{"triple slash", "///", true},
		{"plain", "a", true},
		{"repo file", "repo/file.bin", true},
		{"repo dot segment", "repo/.", false},
		{"repo dotdot segment", "repo/..", false},
		{"hidden name", "repo/.hidden", true},
		{"trailing dot kept", "repo/a.", true},
		{"inner dotdot kept", "repo/a..", true},
		{"leading dotdot name", "repo/..a", true},
		{"inner space", "repo/a b", true},
		{"space before slash", "repo/a /b", false},
		{"space after slash", "repo/ a", false},
		{"leading space text", " a", true},
		{"amp segment", "repo/&", false},
		{"ampamp segment", "repo/&&", false},
		{"amp dot segment", "repo/&.", false},
		{"dot amp segment", "repo/.&", false},
		{"dot amp text", "repo/.&a", true},
		{"trailing space amp", "repo/a &", false},
		{"trailing space dot", "repo/a .", false},
		{"space amp text", "repo/a &b", true},
		{"space dot text", "repo/a .b", true},
		{"amp text", "repo/&a", true},
		{"trailing amp kept", "repo/a&", true},
		{"question", "repo/a?", false},
		{"star", "repo/a*", false},
		{"pipe", "repo/a|", false},
		{"backslash", "repo/a\\b", false},
		{"double quote", "repo/a\"", false},
		{"angle brackets", "repo/a<b>", true},
		{"colon", "repo/a:b", true},
		{"percent not decoded", "repo/%2e%2e", true},
		{"dot segment then more", "repo/./x", false},
		{"dotdot mid path", "repo/a/../b", false},
		{"inner double slash kept", "repo/a//b", true},
		{"NUL accepted", "repo/\x00", true},
		{"literal U+FFFF root", "repo/\uffff", true},
		{"literal U+FFFF then name", "repo/a\uffffb", true},
		{"literal U+FFFF then space", "repo/a\uffff b", false},
		{"dot then U+FFFF", "repo/.\uffffb", false},
		{"U+FFFF then dot", "repo/\uffff.b", true},
		{"supplementary U+1FFFF", "repo/\U0001ffff", true},
		{"max supplementary U+10FFFF", "repo/\U0010ffff", true},
		{"astral emoji", "repo/😀", true},
		{"cjk", "repo/中文", true},
		{"combining", "repo/é", true},
		{"lone dot", ".", false},
		{"lone amp", "&", false},
		{"bare literal U+FFFF", "\uffff", true},
		{"bare question", "?", false},
		{"two chars", "aa", true},
		{"trailing slash file", "a/", true},
		{"dot after text", "a.", true},
		{"uppercase colon name", "repo/C:/x:y", true},
		{"forbidden quote in repo", "\"repo/x\"", false},
		{"forbidden star leading", "*repo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStoragePath(tc.raw)
			if tc.want && err != nil {
				t.Fatalf("ValidateStoragePath(%q) = %v, want nil", tc.raw, err)
			}
			if !tc.want && !errors.Is(err, ErrInvalidStoragePath) {
				t.Fatalf("ValidateStoragePath(%q) = %v, want ErrInvalidStoragePath", tc.raw, err)
			}
		})
	}
}

// javaWhitespaceRunes 是本 JVM 上 Character.isWhitespace 的精确 25 字符集
// （0009..000d、001c..0020、1680、2000..2006、2008..200a、2028、2029、
// 205f、3000）。
// javaWhitespaceRunes is the exact 25-character Character.isWhitespace set
// on this JVM (0009..000d, 001c..0020, 1680, 2000..2006, 2008..200a, 2028,
// 2029, 205f, 3000).
var javaWhitespaceRunes = []rune{
	'\t', '\n', '\v', '\f', '\r',
	0x1c, 0x1d, 0x1e, 0x1f, ' ',
	0x1680,
	0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006,
	0x2008, 0x2009, 0x200a,
	0x2028, 0x2029, 0x205f, 0x3000,
}

// TestValidateStoragePathJavaWhitespace 对全部 25 个 Java 空白字符验证
// 三个方向的拒绝与段内一个方向的接受。
// TestValidateStoragePathJavaWhitespace verifies, for all 25 Java
// whitespace characters, rejection in three positions and acceptance when
// surrounded by ordinary text inside one segment.
func TestValidateStoragePathJavaWhitespace(t *testing.T) {
	for _, r := range javaWhitespaceRunes {
		ws := string(r)
		if err := ValidateStoragePath(ws); err == nil {
			t.Errorf("blank-only %U accepted, want reject", r)
		}
		if err := ValidateStoragePath("repo/" + ws + "x"); err == nil {
			t.Errorf("whitespace %U right after delimiter accepted, want reject", r)
		}
		if err := ValidateStoragePath("repo/x" + ws); err == nil {
			t.Errorf("trailing whitespace %U accepted, want reject", r)
		}
		if err := ValidateStoragePath("repo/x" + ws + "y"); err != nil {
			t.Errorf("inner whitespace %U rejected, want accept: %v", r, err)
		}
	}
}

// TestValidateStoragePathNonBreakingNotWhitespace 验证目标中不是 Java
// 空白的字符：NBSP、U+2007、U+202F、NEL U+0085、U+180E 在任何位置都接受。
// TestValidateStoragePathNonBreakingNotWhitespace verifies characters that
// are NOT Java whitespace in the target: NBSP, U+2007, U+202F, NEL U+0085
// and U+180E are accepted in every position.
func TestValidateStoragePathNonBreakingNotWhitespace(t *testing.T) {
	for _, r := range []rune{0xa0, 0x2007, 0x202f, 0x85, 0x180e} {
		for _, raw := range []string{string(r), "repo/" + string(r) + "x", "repo/x" + string(r), "repo/x" + string(r) + "y"} {
			if err := ValidateStoragePath(raw); err != nil {
				t.Errorf("non-whitespace %U in %q rejected, want accept: %v", r, raw, err)
			}
		}
	}
}

// TestValidateStoragePathStickySymbols 验证点/与号状态的粘滞迁移规则。
// TestValidateStoragePathStickySymbols verifies the sticky transition rules
// of the dot/ampersand pending status.
func TestValidateStoragePathStickySymbols(t *testing.T) {
	reject := []string{".", "..", "&", "&&", ".&", "&.", "a .", "a &", "a\u1680.", "&\u3000."}
	for _, raw := range reject {
		if err := ValidateStoragePath(raw); !errors.Is(err, ErrInvalidStoragePath) {
			t.Errorf("ValidateStoragePath(%q) = %v, want ErrInvalidStoragePath", raw, err)
		}
	}
	accept := []string{".a", "&a", "a.", "a&", "a .b", "a &b", "a.b", "a&b", ".a.&b", "x.y", "x&y"}
	for _, raw := range accept {
		if err := ValidateStoragePath(raw); err != nil {
			t.Errorf("ValidateStoragePath(%q) = %v, want nil", raw, err)
		}
	}
}

// TestValidateStoragePathControlsWithinSegment 验证非空白的控制字符（含
// NUL 与 U+FFFE）按普通字符接受，不引入本地平台文件名策略。
// TestValidateStoragePathControlsWithinSegment verifies that non-whitespace
// control characters (including NUL and U+FFFE) are accepted as ordinary
// characters; no local platform filename policy is introduced.
func TestValidateStoragePathControlsWithinSegment(t *testing.T) {
	for _, r := range []rune{0x00, 0x01, 0x08, 0x0e, 0x1b, 0x7f, 0xfffe} {
		for _, raw := range []string{string(r), "repo/" + string(r), "repo/x" + string(r) + "y"} {
			if err := ValidateStoragePath(raw); err != nil {
				t.Errorf("control %U in %q rejected, want accept: %v", r, raw, err)
			}
		}
	}
}

// TestValidateStoragePathNoDecodingOrNormalization 验证不做百分号解码、
// 不做 Unicode 归一化、不做斜杠折叠：同一序列化串按原字节判定。
// TestValidateStoragePathNoDecodingOrNormalization verifies that no percent
// decoding, Unicode normalization or slash collapsing happens: the input is
// judged exactly as serialized.
func TestValidateStoragePathNoDecodingOrNormalization(t *testing.T) {
	accept := []string{
		"repo/%2e%2e", "repo/%2F", "repo/a%zz", "repo/%",
		"repo/a\u0041\u0300", "repo/aé", "repo/a\u0301", // 未归一化组合 / unnormalized combining
		"repo//a//b//",
	}
	for _, raw := range accept {
		if err := ValidateStoragePath(raw); err != nil {
			t.Errorf("ValidateStoragePath(%q) = %v, want nil", raw, err)
		}
	}
	if err := ValidateStoragePath("repo/a/b/."); !errors.Is(err, ErrInvalidStoragePath) {
		t.Errorf("ValidateStoragePath(%q) = %v, want ErrInvalidStoragePath", "repo/a/b/.", err)
	}
}

// TestValidateStoragePathInvalidUTF8 验证本地 Go 边界策略：无效 UTF-8
// 一律拒绝（目标 Java String 等价范围之外）。
// TestValidateStoragePathInvalidUTF8 verifies the local Go boundary policy:
// invalid UTF-8 is always rejected (outside target Java String equivalence).
func TestValidateStoragePathInvalidUTF8(t *testing.T) {
	invalid := []string{
		"\xff", "a/\xff", "\xc3", "\xc3\x28",
		"\xed\xa0\x80", // 代理项编码 / encoded surrogate half
		"\xc0\x80",     // 过长 NUL / overlong NUL
		"\xf5\x80\x80\x80",
		"a\x80b", "\xE0\x80\x80",
	}
	for _, raw := range invalid {
		if err := ValidateStoragePath(raw); !errors.Is(err, ErrInvalidStoragePath) {
			t.Errorf("ValidateStoragePath(%q) = %v, want ErrInvalidStoragePath", raw, err)
		}
	}
}

// TestValidateStoragePathFixedSentinel 验证错误是固定哨兵、errors.Is 可用
// 且文案不回显输入或被拒字符。
// TestValidateStoragePathFixedSentinel verifies the fixed sentinel, that
// errors.Is works and the text never echoes the input or the rejected
// character.
func TestValidateStoragePathFixedSentinel(t *testing.T) {
	if got, want := ErrInvalidStoragePath.Error(), "repository: invalid storage path"; got != want {
		t.Fatalf("ErrInvalidStoragePath.Error() = %q, want %q", got, want)
	}
	rejected := []string{"repo/a?", "repo/..", "\xff", "repo/\t", "repo/.", "zqxjv-wkv\""}
	for _, raw := range rejected {
		err := ValidateStoragePath(raw)
		if err == nil {
			t.Fatalf("ValidateStoragePath(%q) = nil, want sentinel", raw)
		}
		if err.Error() != ErrInvalidStoragePath.Error() {
			t.Errorf("error text %q differs from fixed sentinel", err.Error())
		}
		if strings.Contains(ErrInvalidStoragePath.Error(), raw) {
			t.Errorf("fixed sentinel text contains rejected input")
		}
	}
}

// TestValidateStoragePathDelimiters 验证 '/' 与字面 U+FFFF 是等价的分隔符
// 转移，重复分隔符接受，EOF 执行同一终结检查。
// TestValidateStoragePathDelimiters verifies that '/' and the literal
// U+FFFF are equivalent delimiter transitions, repeated delimiters are
// accepted and EOF performs the same final check.
func TestValidateStoragePathDelimiters(t *testing.T) {
	accept := []string{
		"\uffff", "\uffff\uffff", "\uffff/\uffff", "a\uffff", "a/\uffff", "\uffffa", "/\uffff", "\uffff/",
	}
	for _, raw := range accept {
		if err := ValidateStoragePath(raw); err != nil {
			t.Errorf("ValidateStoragePath(%q) = %v, want nil", raw, err)
		}
	}
	reject := []string{
		"a\uffff.", "a\uffff..", "a/.", "a/..", "a/ ", "a\uffff ", "a\uffff&", "a /",
	}
	for _, raw := range reject {
		if err := ValidateStoragePath(raw); !errors.Is(err, ErrInvalidStoragePath) {
			t.Errorf("ValidateStoragePath(%q) = %v, want ErrInvalidStoragePath", raw, err)
		}
	}
}

// TestValidateStoragePathSupplementaryNotTruncated 验证增补平面字符
// U+1FFFF/U+10FFFF 是普通字符，不会被截断为 16 位而误判为分隔符。
// TestValidateStoragePathSupplementaryNotTruncated verifies supplementary
// plane characters U+1FFFF/U+10FFFF are ordinary characters and are never
// truncated to 16 bits into a delimiter.
func TestValidateStoragePathSupplementaryNotTruncated(t *testing.T) {
	for _, r := range []rune{0x1ffff, 0x10ffff, 0x10000, 0x2ffff} {
		for _, raw := range []string{string(r), "repo/" + string(r), string(r) + "/x", "a" + string(r) + "/b"} {
			if err := ValidateStoragePath(raw); err != nil {
				t.Errorf("supplementary %U in %q rejected, want accept: %v", r, raw, err)
			}
		}
	}
}

// TestValidateStoragePathSupplementaryLow16Bits 锚定增补平面字符的低 16 位
// 绝不与 BMP 中的 '/'、U+FFFF、空白、'.'、'?'、'\\'、'|' 角色混淆：目标
// oracle 证据（root-extra-cases.json，8 例全部接受）表明 "repo/.<增补>/"
// 全部接受，而把码点截断为 uint16 的分类器会对其中每一例误拒（空白/点
// 待定、分隔符或禁字符）。每个增补接受例配对同一低 16 位 BMP 字符的相应
// 拒绝，构成可杀死截断变异的永久回归。
// TestValidateStoragePathSupplementaryLow16Bits anchors that the low 16
// bits of a supplementary rune never impersonate the BMP roles '/', U+FFFF,
// whitespace, '.', '?', '\\', '|': target-oracle evidence
// (root-extra-cases.json, all eight accepted) shows "repo/.<supp>/" is
// always accepted, while a classifier truncating code points to uint16
// falsely rejects every one of them (pending space/dot, delimiter or
// forbidden char). Each supplementary acceptance is paired with the
// corresponding rejection for the BMP rune carrying the same low 16 bits,
// forming a permanent regression that kills the truncation mutant.
func TestValidateStoragePathSupplementaryLow16Bits(t *testing.T) {
	// 增补接受：低 16 位分别是 0020 空白、002e '.'、002f '/'、003f '?'、
	// 005c '\\'、007c '|'、以及两个字面 FFFF 低位的边界码点。
	// Supplementary accepts: low16 bits 0020 whitespace, 002e '.', 002f
	// '/', 003f '?', 005c '\\', 007c '|', plus both FFFF-low boundary
	// code points.
	for _, r := range []rune{
		'\U00010020', '\U0001002e', '\U0001002f', '\U0001003f',
		'\U0001005c', '\U0001007c', '\U0001ffff', '\U0010ffff',
	} {
		for _, raw := range []string{
			"repo/." + string(r) + "/", // oracle 例 / oracle case
			"." + string(r) + "/",
		} {
			if err := ValidateStoragePath(raw); err != nil {
				t.Errorf("ValidateStoragePath(%q) = %v, want nil (supplementary U+%04X must not truncate)", raw, err, r)
			}
		}
	}
	// 分隔符后跟增补字符再跟空白：目标接受；截断后 U+FFFF 低位会先变
	// 分隔符、空白随即紧跟分隔符而误拒。
	// Supplementary after a name then whitespace: the target accepts;
	// truncation turns the FFFF-low rune into a delimiter so the trailing
	// whitespace falsely rejects.
	for _, r := range []rune{'\U0001ffff', '\U0010ffff'} {
		if err := ValidateStoragePath("repo/a" + string(r) + " b"); err != nil {
			t.Errorf("ValidateStoragePath(%q) = %v, want nil (U+%04X not a delimiter)", "repo/a"+string(r)+" b", err, r)
		}
	}
	// 配对 BMP 拒绝：同低 16 位字符在相同位置必须拒绝。
	// Paired BMP rejections: the rune carrying the same low 16 bits must
	// reject in the same positions.
	for _, bmp := range []rune{' ', '.', '/', '?', '\\', '|', '\uffff'} {
		for _, raw := range []string{
			"repo/." + string(bmp) + "/",
			"." + string(bmp) + "/",
		} {
			if err := ValidateStoragePath(raw); !errors.Is(err, ErrInvalidStoragePath) {
				t.Errorf("ValidateStoragePath(%q) = %v, want ErrInvalidStoragePath (BMP U+%04X)", raw, err, bmp)
			}
		}
	}
	if err := ValidateStoragePath("repo/a\uffff b"); !errors.Is(err, ErrInvalidStoragePath) {
		t.Errorf("ValidateStoragePath(%q) = %v, want ErrInvalidStoragePath (literal U+FFFF is a delimiter)", "repo/a\uffff b", err)
	}
}

// TestNewPathUnchangedByStorageValidator 验证 NewPath 身份归一化构造器
// 行为保持不变：它不做存储名检查，也不因存储级拒绝字符而拒绝。
// TestNewPathUnchangedByStorageValidator verifies the NewPath identity
// normalization constructor is unchanged: it performs no storage-name
// check and never rejects characters the storage checker rejects.
func TestNewPathUnchangedByStorageValidator(t *testing.T) {
	p, err := NewPath("repo", "/parent/", "name ", true)
	if err != nil {
		t.Fatalf("NewPath unexpected error: %v", err)
	}
	if p.Parent() != "parent" || p.Name() != "name" {
		t.Fatalf("NewPath normalization changed: parent=%q name=%q", p.Parent(), p.Name())
	}
	// 存储级禁字符不进入构造器 / storage-forbidden chars stay out of the constructor.
	for _, name := range []string{"a?", "a*", "a\"", "a\\b", "a|"} {
		if _, err := NewPath("repo", "p", name, true); err != nil {
			t.Errorf("NewPath rejected %q: %v", name, err)
		}
	}
	if _, err := NewPath(" ", "p", "n", true); err == nil {
		t.Errorf("NewPath accepted blank repo")
	}
}

// Package filestore 提供本地文件内容存储的校验与布局基础。
// Package filestore provides validation and layout primitives for the
// local file content store.
package filestore

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/0ldlight/bf/internal/content"
)

// Expected 是调用方传入的期望摘要与长度；空白字段视为缺省，不做修剪。
// Expected carries caller-supplied digests and size; blank fields mean
// absent and nonblank values are never trimmed.
type Expected struct {
	SHA1   string
	SHA256 string
	MD5    string
	Size   *int64
}

var (
	// ErrInvalidKey 表示键不是规范的 40 位小写十六进制 SHA-1。
	// ErrInvalidKey means the key is not a canonical 40-char lowercase hex SHA-1.
	ErrInvalidKey = errors.New("filestore: invalid key")
	// ErrChecksumMismatch 表示期望摘要与实际内容摘要不一致。
	// ErrChecksumMismatch means an expected digest differs from the content.
	ErrChecksumMismatch = errors.New("filestore: checksum mismatch")
	// ErrLengthMismatch 表示期望长度与实际内容长度不一致（含负数）。
	// ErrLengthMismatch means the expected size differs from the actual size.
	ErrLengthMismatch = errors.New("filestore: length mismatch")
	// ErrUnsafePath 表示解析出的物理路径越界或非常规对象。
	// ErrUnsafePath means a resolved physical path escapes the store root.
	ErrUnsafePath = errors.New("filestore: unsafe path")
)

const (
	sha1HexLen   = 40
	sha256HexLen = 64
	md5HexLen    = 32
)

// keyPath 把规范 SHA-1 键映射为 "<前2位>/<完整摘要>" 的相对物理路径。
// keyPath maps a canonical SHA-1 key to the "<first2>/<full digest>"
// relative physical path; anything else wraps ErrInvalidKey.
func keyPath(sha1 string) (string, error) {
	// 错误不携带原始键：键是调用方输入，可能任意大；只报告字段与哨兵。
	// Errors never embed the raw key: it is caller input and may be huge;
	// report only the field and sentinel.
	if len(sha1) != sha1HexLen {
		return "", fmt.Errorf("filestore: key (len %d): %w", len(sha1), ErrInvalidKey)
	}
	for i := 0; i < len(sha1); i++ {
		c := sha1[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", fmt.Errorf("filestore: key (bad hex at %d): %w", i, ErrInvalidKey)
		}
	}
	return sha1[:2] + "/" + sha1, nil
}

// blank 报告 s 是否为空或全为 Java Character.isWhitespace 空白。
// blank reports whether s is empty or entirely Java-style whitespace:
// ASCII 9..13 and 28..32 (via Zs) plus Unicode Zs/Zl/Zp, excluding the
// nonbreaking U+00A0/U+2007/U+202F; NEL U+0085 is not blank.
func blank(s string) bool {
	for _, r := range s {
		if !blankRune(r) {
			return false
		}
	}
	return true
}

func blankRune(r rune) bool {
	if '\t' <= r && r <= '\r' || 0x1c <= r && r <= 0x1f {
		return true
	}
	if unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp) {
		return r != 0xa0 && r != 0x2007 && r != 0x202f
	}
	return false
}

func asciiHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// validateExpected 先校验期望长度（nil 表示缺省，负数必然报错），再按
// SHA-1、SHA-256、MD5 顺序逐字比较非空白期望：长度正确的 ASCII 十六进制
// 先转小写，其余原样比较；失败错误分别包装 ErrLengthMismatch 或
// ErrChecksumMismatch。
// validateExpected checks the expected size first (nil means absent,
// negative always fails), then compares nonblank digest expectations in
// SHA-1, SHA-256, MD5 order: correct-length ASCII hex is lowercased first,
// anything else is compared verbatim. Failures wrap ErrLengthMismatch or
// ErrChecksumMismatch respectively.
func validateExpected(meta content.Metadata, expected Expected) error {
	if expected.Size != nil {
		if *expected.Size < 0 || *expected.Size != meta.Size {
			return fmt.Errorf("filestore: size: want %d got %d: %w", *expected.Size, meta.Size, ErrLengthMismatch)
		}
	}
	fields := []struct {
		name   string
		want   string
		got    string
		hexLen int
	}{
		{"sha1", expected.SHA1, meta.SHA1, sha1HexLen},
		{"sha256", expected.SHA256, meta.SHA256, sha256HexLen},
		{"md5", expected.MD5, meta.MD5, md5HexLen},
	}
	for _, f := range fields {
		if blank(f.want) {
			continue
		}
		norm := f.want
		if len(norm) == f.hexLen && asciiHex(norm) {
			norm = strings.ToLower(norm)
		}
		if norm != f.got {
			// 错误不携带期望/实际摘要原文：可能任意大；只报字段与哨兵。
			// Errors never embed the raw expected/actual digest: they may be
			// huge; report only the field and sentinel.
			return fmt.Errorf("filestore: %s: want (len %d) got (len %d): %w", f.name, len(f.want), len(f.got), ErrChecksumMismatch)
		}
	}
	return nil
}

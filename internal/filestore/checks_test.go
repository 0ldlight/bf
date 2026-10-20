package filestore

import (
	"errors"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

// 固定向量：内容 "abc" 的三摘要与长度，独立于 content.Calculate。
// Fixed vectors for the content "abc", independent of content.Calculate.
const (
	abcSHA1   = "a9993e364706816aba3e25717850c26c9cd0d89d"
	abcSHA256 = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	abcMD5    = "900150983cd24fb0d6963f7d28e17f72"
)

func abcMeta() content.Metadata {
	return content.Metadata{SHA1: abcSHA1, SHA256: abcSHA256, MD5: abcMD5, Size: 3}
}

func sizePtr(n int64) *int64 { return &n }

func TestKeyPathCanonical(t *testing.T) {
	digest := "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	got, err := keyPath(digest)
	if err != nil {
		t.Fatalf("keyPath(%q) unexpected error: %v", digest, err)
	}
	if want := "da/" + digest; got != want {
		t.Fatalf("keyPath(%q) = %q, want %q", digest, got, want)
	}
}

func TestKeyPathInvalid(t *testing.T) {
	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"39 chars", abcSHA1[:39]},
		{"41 chars", abcSHA1 + "0"},
		{"uppercase hex", strings.ToUpper(abcSHA1)},
		{"mixed case hex", "A" + abcSHA1[1:]},
		{"non-hex letters", strings.Repeat("g", 40)},
		{"traversal body", "../" + strings.Repeat("a", 37)},
		{"dot slash segment", "ab/..cd" + strings.Repeat("a", 33)},
		{"NUL byte", "ab\x00" + strings.Repeat("a", 37)},
		{"newline inside", "ab\ncd" + strings.Repeat("a", 35)},
	}
	for _, tc := range cases {
		got, err := keyPath(tc.key)
		if err == nil {
			t.Errorf("%s: keyPath(%q) = %q, want error wrapping ErrInvalidKey", tc.name, tc.key, got)
			continue
		}
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("%s: keyPath(%q) error = %v, want ErrInvalidKey", tc.name, tc.key, err)
		}
	}
}

// 攻击者可控的超大无效输入不得被完整拼进错误文本。
// Attacker-sized invalid input must not be copied into error text.
func TestKeyPathErrorBounded(t *testing.T) {
	key := strings.Repeat("z", 1<<20) // 1MiB invalid key
	_, err := keyPath(key)
	if err == nil {
		t.Fatal("keyPath(1MiB invalid key) = nil error, want ErrInvalidKey")
	}
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("keyPath(1MiB invalid key) error = %v, want ErrInvalidKey", err)
	}
	if len(err.Error()) >= 256 {
		t.Errorf("keyPath error text length = %d, want < 256", len(err.Error()))
	}
}

func TestValidateExpectedErrorBounded(t *testing.T) {
	meta := abcMeta()
	err := validateExpected(meta, Expected{SHA256: strings.Repeat("z", 1<<20)})
	if err == nil {
		t.Fatal("validateExpected(1MiB invalid sha256) = nil error, want ErrChecksumMismatch")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("validateExpected(1MiB invalid sha256) error = %v, want ErrChecksumMismatch", err)
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("error %q does not mention sha256", err)
	}
	if len(err.Error()) >= 256 {
		t.Errorf("validateExpected error text length = %d, want < 256", len(err.Error()))
	}
}

func TestBlank(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{" ", true},
		{" \t\n\v\f\r", true},
		{"\x1c\x1d\x1e\x1f", true},
		{"\u2003\u2009\u200a", true}, // em/thin/hair space
		{"\u2028\u2029", true},       // line/paragraph separator
		{"\u1680\u205f\u3000", true}, // ogham/medium/ideographic space
		{"\u00a0", false},            // NBSP is not blank
		{"\u2007", false},            // figure space is not blank
		{"\u202f", false},            // narrow NBSP is not blank
		{"\u0085", false},            // NEL is not blank
		{"a", false},
		{"\u2003a", false},
	}
	for _, tc := range cases {
		if got := blank(tc.in); got != tc.want {
			t.Errorf("blank(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidateExpectedMatch(t *testing.T) {
	meta := abcMeta()
	cases := []struct {
		name     string
		expected Expected
	}{
		{"empty expected", Expected{}},
		{"all match", Expected{SHA1: abcSHA1, SHA256: abcSHA256, MD5: abcMD5, Size: sizePtr(3)}},
		{"digests only", Expected{SHA1: abcSHA1}},
		{"uppercase sha1", Expected{SHA1: strings.ToUpper(abcSHA1)}},
		{"uppercase sha256", Expected{SHA256: strings.ToUpper(abcSHA256)}},
		{"uppercase md5", Expected{MD5: strings.ToUpper(abcMD5)}},
		{"all uppercase", Expected{SHA1: strings.ToUpper(abcSHA1), SHA256: strings.ToUpper(abcSHA256), MD5: strings.ToUpper(abcMD5)}},
		{"blank digest fields", Expected{SHA1: "\u2003", SHA256: " \t\u2028\u2029", MD5: ""}},
		{"size only match", Expected{Size: sizePtr(3)}},
	}
	for _, tc := range cases {
		if err := validateExpected(meta, tc.expected); err != nil {
			t.Errorf("%s: validateExpected(%+v) = %v, want nil", tc.name, tc.expected, err)
		}
	}
}

func TestValidateExpectedSize(t *testing.T) {
	meta := abcMeta()
	cases := []struct {
		name string
		size int64
	}{
		{"larger", 4},
		{"smaller", 2},
		{"zero", 0},
		{"negative", -1},
		{"very negative", -9223372036854775808},
	}
	for _, tc := range cases {
		err := validateExpected(meta, Expected{Size: sizePtr(tc.size)})
		if !errors.Is(err, ErrLengthMismatch) {
			t.Errorf("%s: size %d: error = %v, want ErrLengthMismatch", tc.name, tc.size, err)
		}
	}
	// 长度错误优先于摘要错误 / the size error wins over digest errors.
	err := validateExpected(meta, Expected{SHA1: strings.Repeat("0", 40), Size: sizePtr(4)})
	if !errors.Is(err, ErrLengthMismatch) {
		t.Errorf("size+digest conflict: error = %v, want ErrLengthMismatch", err)
	}
}

func TestValidateExpectedChecksum(t *testing.T) {
	meta := abcMeta()
	cases := []struct {
		name      string
		expected  Expected
		wantField string
	}{
		{"sha1 mismatch", Expected{SHA1: strings.Repeat("0", 40)}, "sha1"},
		{"sha256 mismatch", Expected{SHA256: strings.Repeat("0", 64)}, "sha256"},
		{"md5 mismatch", Expected{MD5: strings.Repeat("0", 32)}, "md5"},
		{"sha1 wrong length", Expected{SHA1: abcSHA1[:39]}, "sha1"},
		{"sha1 spaces around", Expected{SHA1: " " + abcSHA1 + " "}, "sha1"},
		{"sha1 NBSP around", Expected{SHA1: "\u00a0" + abcSHA1 + "\u00a0"}, "sha1"},
		{"sha1 sole NBSP", Expected{SHA1: "\u00a0"}, "sha1"},
		{"md5 sole NEL", Expected{MD5: "\u0085"}, "md5"},
		{"sha256 junk right length", Expected{SHA256: strings.Repeat("z", 64)}, "sha256"},
	}
	for _, tc := range cases {
		err := validateExpected(meta, tc.expected)
		if !errors.Is(err, ErrChecksumMismatch) {
			t.Errorf("%s: error = %v, want ErrChecksumMismatch", tc.name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantField) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.wantField)
		}
	}
	// SHA-1 期望先于 SHA-256 报告 / the SHA-1 expectation is reported first.
	err := validateExpected(meta, Expected{SHA1: strings.Repeat("0", 40), SHA256: strings.Repeat("0", 64)})
	if err == nil || !strings.Contains(err.Error(), "sha1") {
		t.Errorf("order: error = %v, want sha1 reported first", err)
	}
}

package repository

import (
	"reflect"
	"testing"
)

// BIN-208 静态契约衍生用例：纯决策，无目标差分。
// 语义: missing→false；present 且双请求摘要为 nil→true（不依赖 flag）；
// 否则 flag OR (reqSHA1!=nil && 不等) OR (reqSHA256!=nil && 不等)。
func TestDecideFileOverwrite(t *testing.T) {
	sha1A := []uint16{'a', 'b', 'c'}
	sha1B := []uint16{'a', 'b', 'd'} // 大小写/内容差
	sha256A := []uint16{'0', '1'}
	sha256B := []uint16{'0', '2'}

	tests := []struct {
		name  string
		facts OverwriteFacts
		want  bool
	}{
		// 零值/缺失：missing 一票否决，其余事实无关。
		{name: "zero facts", facts: OverwriteFacts{}, want: false},
		{name: "missing no headers flag on", facts: OverwriteFacts{EnforceOnIdenticalChecksums: true}, want: false},
		{name: "missing digest flag off", facts: OverwriteFacts{RequestSHA1: sha1A}, want: false},
		{name: "missing dominates flag and diff", facts: OverwriteFacts{
			EnforceOnIdenticalChecksums: true, RequestSHA1: sha1B, StoredSHA1: sha1A,
			RequestSHA256: sha256B, StoredSHA256: sha256A}, want: false},

		// present 且双请求摘要均为 nil：true，flag 开/关均不参与。
		{name: "present both request nil flag off", facts: OverwriteFacts{FilePresent: true}, want: true},
		{name: "present both request nil flag on", facts: OverwriteFacts{FilePresent: true,
			EnforceOnIdenticalChecksums: true, StoredSHA1: sha1A, StoredSHA256: sha256A}, want: true},

		// SHA1 独立驱动：SHA256 缺席。
		{name: "sha1 equal flag off", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: sha1A, StoredSHA1: sha1A}, want: false},
		{name: "sha1 differ flag off", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: sha1B, StoredSHA1: sha1A}, want: true},

		// SHA256 独立驱动：SHA1 缺席。
		{name: "sha256 equal flag off", facts: OverwriteFacts{FilePresent: true,
			RequestSHA256: sha256A, StoredSHA256: sha256A}, want: false},
		{name: "sha256 differ flag off", facts: OverwriteFacts{FilePresent: true,
			RequestSHA256: sha256B, StoredSHA256: sha256A}, want: true},

		// 双摘要：任一不等即可；全等则依赖 flag。
		{name: "both equal flag off", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: sha1A, StoredSHA1: sha1A, RequestSHA256: sha256A, StoredSHA256: sha256A}, want: false},
		{name: "both equal flag on", facts: OverwriteFacts{FilePresent: true,
			EnforceOnIdenticalChecksums: true,
			RequestSHA1:                 sha1A, StoredSHA1: sha1A, RequestSHA256: sha256A, StoredSHA256: sha256A}, want: true},
		{name: "sha1 equal sha256 differ", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: sha1A, StoredSHA1: sha1A, RequestSHA256: sha256B, StoredSHA256: sha256A}, want: true},
		{name: "sha1 differ sha256 equal", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: sha1B, StoredSHA1: sha1A, RequestSHA256: sha256A, StoredSHA256: sha256A}, want: true},

		// nil 与非 nil 空互异（双向）：请求侧与存储侧。
		{name: "request empty vs stored nil", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{}, StoredSHA1: nil}, want: true},
		{name: "request empty vs stored empty", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{}, StoredSHA1: []uint16{}}, want: false},
		{name: "sha256 empty vs stored nil", facts: OverwriteFacts{FilePresent: true,
			RequestSHA256: []uint16{}}, want: true},
		{name: "sha256 empty vs stored empty", facts: OverwriteFacts{FilePresent: true,
			RequestSHA256: []uint16{}, StoredSHA256: []uint16{}}, want: false},
		{name: "absent sha1 ignores nonempty stored", facts: OverwriteFacts{FilePresent: true,
			StoredSHA1: sha1A, RequestSHA256: sha256A, StoredSHA256: sha256A}, want: false},
		{name: "absent sha256 ignores nonempty stored", facts: OverwriteFacts{FilePresent: true,
			StoredSHA256: sha256A, RequestSHA1: sha1A, StoredSHA1: sha1A}, want: false},
		{name: "request sha1 units vs stored empty", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: sha1A, StoredSHA1: []uint16{}}, want: true},
		{name: "request sha256 nil vs stored empty other nil digest equal", facts: OverwriteFacts{
			FilePresent: true, RequestSHA256: nil, StoredSHA256: []uint16{}}, want: true},
		{name: "sha1 nil skips to sha256 equal", facts: OverwriteFacts{FilePresent: true,
			RequestSHA256: sha256A, StoredSHA256: sha256A}, want: false},

		// 原始码元相等：无大小写折叠/空白裁剪/NUL 处理/代理项规范化。
		{name: "case fold not applied", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{'A'}, StoredSHA1: []uint16{'a'}}, want: true},
		{name: "trailing space significant", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{'a', ' '}, StoredSHA1: []uint16{'a'}}, want: true},
		{name: "NUL unit significant", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{0x0000}, StoredSHA1: []uint16{}}, want: true},
		{name: "NUL vs space differ", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{0x0000}, StoredSHA1: []uint16{' '}}, want: true},
		{name: "unpaired surrogate raw differ", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{0xD800}, StoredSHA1: []uint16{0xDC00}}, want: true},
		{name: "unpaired surrogate raw equal", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{0xD800, 0xD800}, StoredSHA1: []uint16{0xD800, 0xD800}}, want: false},
		{name: "surrogate pair vs reversed differ", facts: OverwriteFacts{FilePresent: true,
			RequestSHA1: []uint16{0xD800, 0xDC00}, StoredSHA1: []uint16{0xDC00, 0xD800}}, want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := DecideFileOverwrite(tt.facts); got != tt.want {
				t.Fatalf("DecideFileOverwrite(%+v) = %v, want %v", tt.facts, got, tt.want)
			}
		})
	}
}

// 参数不可变性：调用前后入参切片内容与 nil 性保持不变（无写入/无保留影响可观测面）。
func TestDecideFileOverwriteArgumentImmutability(t *testing.T) {
	req1 := []uint16{'x', 'y'}
	req256 := []uint16{'z'}
	st1 := []uint16{'x', 'y'}
	st256 := []uint16{'w'}
	before := OverwriteFacts{FilePresent: true, RequestSHA1: req1, RequestSHA256: req256,
		StoredSHA1: st1, StoredSHA256: st256}
	snapshot := func() OverwriteFacts {
		f := OverwriteFacts{FilePresent: before.FilePresent, EnforceOnIdenticalChecksums: before.EnforceOnIdenticalChecksums}
		f.RequestSHA1 = append([]uint16(nil), req1...)
		f.RequestSHA256 = append([]uint16(nil), req256...)
		f.StoredSHA1 = append([]uint16(nil), st1...)
		f.StoredSHA256 = append([]uint16(nil), st256...)
		return f
	}
	pre := snapshot()
	for i := 0; i < 3; i++ {
		DecideFileOverwrite(before)
	}
	post := snapshot()
	if !reflect.DeepEqual(pre, post) {
		t.Fatalf("arguments mutated: pre=%+v post=%+v", pre, post)
	}
	// 下一次调用读取调用方当前提供的码元。
	req1[0] = 'X'
	if got := DecideFileOverwrite(OverwriteFacts{FilePresent: true, RequestSHA1: req1, StoredSHA1: st1}); !got {
		t.Fatalf("mutated visible unit should differ: got %v", got)
	}
}

// 七列元数据私有编解码/绑定/行形状的单元契约：只覆盖保证不触库的纯转换；
// 真实 SQL 行为由 integration/postgres 的真实 PostgreSQL 门禁验证。
// Unit contract for the private seven-column metadata codec, argument
// binding and row-shape classification: guaranteed no-DB pure conversions
// only; real SQL behavior belongs to the real-PostgreSQL integration gate.
package postgres

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/repository"
)

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

// u16 构造测试码元（输入保证非空时与 utf16.Encode 一致）。
// u16 builds test units (identical to utf16.Encode for the nonempty inputs
// used here).
func u16(s string) []uint16 {
	units := make([]uint16, 0, len(s))
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
			continue
		}
		units = append(units, uint16(r))
	}
	return units
}

func TestEncodeMetadataUnitsSQLBoundary(t *testing.T) {
	// nil 与空串不同：nil 参数即 SQL NULL，空串是显式空字符串。
	// nil differs from empty: the nil argument is SQL NULL while the empty
	// slice is an explicit empty string.
	if got := encodeMetadataUnits(nil); got != nil {
		t.Errorf("encode(nil) = %#v, want nil (SQL NULL)", got)
	}
	if got := encodeMetadataUnits([]uint16{}); got != "" {
		t.Errorf("encode(empty) = %#v, want nonnil empty string", got)
	}
	cases := []struct {
		name  string
		units []uint16
		want  string
	}{
		{"ascii verbatim", u16("admin"), "admin"},
		// 有效代理对（星面表情）正常编码为合法 UTF-8。
		// A valid surrogate pair (an astral emoji) encodes as valid UTF-8.
		{"valid pair", []uint16{0xD83D, 0xDE00}, "\U0001F600"},
		{"pair then ascii", []uint16{0xD83D, 0xDE00, '!'}, "\U0001F600!"},
		// 每个不成对代理码元变为 ASCII '?'，绝不替换为 U+FFFD。
		// Every unpaired surrogate unit becomes ASCII '?', never U+FFFD.
		{"lone high", []uint16{0xD83D}, "?"},
		{"lone low", []uint16{0xDE00}, "?"},
		{"reversed pair", []uint16{0xDE00, 0xD83D}, "??"},
		{"trailing lone high", []uint16{'a', 0xD83D}, "a?"},
		{"two lone highs", []uint16{0xD83D, 0xD83D}, "??"},
		// 字面 U+FFFD 与字面 '?' 保持自身，不与替换规则混淆。
		// A literal U+FFFD and a literal '?' stay themselves, never
		// confused with the replacement rule.
		{"literal replacement char", []uint16{0xFFFD}, "�"},
		{"literal question mark", []uint16{'?'}, "?"},
		{"replacement between pairs", []uint16{0xD83D, 0xDE00, 0xFFFD, 0xD83D, 0xDE00}, "\U0001F600�\U0001F600"},
		// NUL 逐字进入字符串（数据库裁决），不截断/不替换。
		// NUL enters the string verbatim (the database decides), never
		// truncated or replaced.
		{"nul verbatim", []uint16{'a', 0, 'b'}, "a\x00b"},
		{"only nul", []uint16{0}, "\x00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := encodeMetadataUnits(tc.units).(string)
			if !ok {
				t.Fatalf("encode = %#v, want string", got)
			}
			if got != tc.want {
				t.Fatalf("encode units %04x = %q (%04x), want %q (%04x)",
					tc.units, got, []rune(got), tc.want, []rune(tc.want))
			}
		})
	}
}

func TestDecodeStoredUnits(t *testing.T) {
	// NULL → nil；空串 → 非 nil 零长度；nil != 空。
	// NULL → nil; the empty string → a nonnil zero length; nil != empty.
	if got := decodeStoredUnits(sql.NullString{}); got != nil {
		t.Errorf("decode(NULL) = %v, want nil", got)
	}
	if got := decodeStoredUnits(nullStr("")); got == nil || len(got) != 0 {
		t.Errorf("decode(\"\") = %#v, want nonnil empty", got)
	}
	if got := decodeStoredUnits(nullStr("admin")); string(runesOf(got)) != "admin" {
		t.Errorf("decode(ascii) = %04x, want admin", got)
	}
	// 星面字符重建为代理对，字面 U+FFFD 保持。
	// Astral characters rebuild as surrogate pairs; a literal U+FFFD stays.
	if got := decodeStoredUnits(nullStr("\U0001F600")); len(got) != 2 || got[0] != 0xD83D || got[1] != 0xDE00 {
		t.Errorf("decode(emoji) = %04x, want surrogate pair", got)
	}
	if got := decodeStoredUnits(nullStr("�")); len(got) != 1 || got[0] != 0xFFFD {
		t.Errorf("decode(U+FFFD) = %04x, want literal U+FFFD", got)
	}
	// 往返：编码产物解码回原码元（除不成对代理已变为 '?'）。
	// Round trip: an encoded value decodes back to its units (unpaired
	// surrogates already became '?').
	units := u16("NO_ORIG 100%å\U0001F600�?")
	if got := decodeStoredUnits(nullStr(encodeMetadataUnits(units).(string))); !equalUnits(got, units) {
		t.Errorf("round trip = %04x, want %04x", got, units)
	}
}

func runesOf(units []uint16) []rune {
	var rs []rune
	for i := 0; i < len(units); i++ {
		u := units[i]
		if u >= 0xD800 && u < 0xDC00 && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] < 0xE000 {
			rs = append(rs, ((rune(u)-0xD800)<<10|(rune(units[i+1])-0xDC00))+0x10000)
			i++
			continue
		}
		rs = append(rs, rune(u))
	}
	return rs
}

func equalUnits(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDecodeStoredActorReadSideAbbreviation(t *testing.T) {
	if a := decodeStoredActor(sql.NullString{}); !a.IsNull() {
		t.Errorf("decode actor NULL = nonnull actor, want null actor")
	}
	if a := decodeStoredActor(nullStr("")); a.IsNull() || len(a.UTF16()) != 0 {
		t.Errorf("decode actor \"\" = %v, want present empty actor", a.UTF16())
	}
	// 存量行可能携带超过 64 个 UTF-16 码元的 actor（VARCHAR(64) 按字符计，
	// 64 个星面字符即 128 码元）：读取侧经 NewActor 缩写为首 61 码元加三
	// 个 0x002e。
	// A stored row may carry an actor above 64 UTF-16 units (VARCHAR(64)
	// counts characters: 64 astral characters are 128 units): read-side
	// NewActor abbreviates to the first 61 units plus three 0x002e.
	long := strings.Repeat("\U0001F600", 64)
	a := decodeStoredActor(nullStr(long))
	units := a.UTF16()
	if len(units) != 64 {
		t.Fatalf("abbreviated actor length = %d units, want 64 (61 kept + 3 dots)", len(units))
	}
	want := u16(long)
	for i := 0; i < 61; i++ {
		if units[i] != want[i] {
			t.Fatalf("kept unit %d = %04x, want %04x", i, units[i], want[i])
		}
	}
	if units[61] != 0x002e || units[62] != 0x002e || units[63] != 0x002e {
		t.Fatalf("abbreviation suffix = %04x, want three 0x002e", units[61:64])
	}
}

func TestMetadataArgs(t *testing.T) {
	// nil 快照 → 七个 nil（全 NULL）。
	// A nil snapshot yields seven nils (all NULL).
	if args := metadataArgs(nil); len(args) != 7 {
		t.Fatalf("nil args length = %d, want 7", len(args))
	} else {
		for i, a := range args {
			if a != nil {
				t.Errorf("nil args[%d] = %#v, want nil", i, a)
			}
		}
	}
	snap := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  -9223372036854775808,
		ModifiedMillis: 9223372036854775807,
		UpdatedMillis:  0,
		CreatedBy:      repository.NewActor(u16("admin")),
		ModifiedBy:     repository.NewActor(nil),
		SHA1Original:   u16("NO_ORIG"),
		MD5Original:    []uint16{},
	})
	args := metadataArgs(&snap)
	if len(args) != 7 {
		t.Fatalf("args length = %d, want 7", len(args))
	}
	if args[0] != int64(-9223372036854775808) || args[1] != int64(9223372036854775807) || args[2] != int64(0) {
		t.Fatalf("time args = %v/%v/%v, want int64 extremes verbatim", args[0], args[1], args[2])
	}
	if args[3] != "admin" {
		t.Errorf("created_by arg = %#v, want \"admin\"", args[3])
	}
	if args[4] != nil {
		t.Errorf("modified_by arg = %#v, want nil (null actor)", args[4])
	}
	if args[5] != "NO_ORIG" {
		t.Errorf("sha1_original arg = %#v, want NO_ORIG", args[5])
	}
	if args[6] != "" {
		t.Errorf("md5_original arg = %#v, want empty string (not nil)", args[6])
	}
}

func nullInt64Of(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

func TestStoredMetadataSnapshotShapes(t *testing.T) {
	// 全 NULL → nil 快照（与显式零值快照不同）。
	// All NULL → a nil snapshot (distinct from an explicit zero snapshot).
	m, err := (storedMetadata{}).snapshot()
	if err != nil || m != nil {
		t.Fatalf("all-NULL snapshot = (%v, %v), want (nil, nil)", m, err)
	}
	// created+modified 已知、updated NULL → updated 映射 0。
	// created and modified known with a NULL updated → updated maps to 0.
	full := storedMetadata{created: nullInt64Of(-5), modified: nullInt64Of(1 << 62)}
	m, err = full.snapshot()
	if err != nil || m == nil {
		t.Fatalf("known-times snapshot = (%v, %v), want nonnil snapshot", m, err)
	}
	if m.CreatedMillis() != -5 || m.ModifiedMillis() != 1<<62 || m.UpdatedMillis() != 0 {
		t.Fatalf("snapshot times = %d/%d/%d, want -5/%d/0", m.CreatedMillis(), m.ModifiedMillis(), m.UpdatedMillis(), int64(1<<62))
	}
	if !m.CreatedBy().IsNull() || !m.ModifiedBy().IsNull() {
		t.Fatalf("absent actors must decode as null actors")
	}
	if m.SHA1OriginalUTF16() != nil || m.MD5OriginalUTF16() != nil {
		t.Fatalf("absent originals must decode as nil originals")
	}
	// 完整七列 → 快照带全部字段。
	// All seven columns → a snapshot carrying every field.
	seven := storedMetadata{
		created: nullInt64Of(0), modified: nullInt64Of(0), updated: nullInt64Of(0),
		createdBy: nullStr("c"), modifiedBy: nullStr(""),
		sha1Original: nullStr("s"), md5Original: nullStr(""),
	}
	m, err = seven.snapshot()
	if err != nil || m == nil {
		t.Fatalf("seven-column snapshot = (%v, %v), want nonnil", m, err)
	}
	if m.CreatedMillis() != 0 || m.ModifiedMillis() != 0 || m.UpdatedMillis() != 0 {
		t.Fatalf("zero snapshot times lost: %d/%d/%d", m.CreatedMillis(), m.ModifiedMillis(), m.UpdatedMillis())
	}
	if string(runesOf(m.CreatedBy().UTF16())) != "c" || m.ModifiedBy().IsNull() || len(m.ModifiedBy().UTF16()) != 0 {
		t.Fatalf("actor columns lost: %v / %v", m.CreatedBy().UTF16(), m.ModifiedBy().UTF16())
	}
	if string(runesOf(m.SHA1OriginalUTF16())) != "s" || m.MD5OriginalUTF16() == nil || len(m.MD5OriginalUTF16()) != 0 {
		t.Fatalf("original columns lost: %v / %v", m.SHA1OriginalUTF16(), m.MD5OriginalUTF16())
	}
	// 快照独立：两次 Lookup 各自分配。
	// Snapshot independence: two lookups allocate separately.
	m2, _ := seven.snapshot()
	if m == m2 {
		t.Fatalf("snapshot must be independently allocated per lookup")
	}
	// 其他任何部分形状 → 有界 stageLookup 错误且保留 ErrInvalidReference。
	// Any other partial shape → a bounded stageLookup error preserving
	// ErrInvalidReference.
	partials := []storedMetadata{
		{created: nullInt64Of(1)},
		{modified: nullInt64Of(1)},
		{updated: nullInt64Of(1)},
		{createdBy: nullStr("x")},
		{created: nullInt64Of(1), updated: nullInt64Of(2), sha1Original: nullStr("x")},
		{modified: nullInt64Of(1), modifiedBy: nullStr("x")},
		{sha1Original: nullStr("x"), md5Original: nullStr("y")},
	}
	for i, psm := range partials {
		m, err := psm.snapshot()
		if err == nil || m != nil {
			t.Fatalf("partial shape %d = (%v, %v), want (nil, error)", i, m, err)
		}
		if !errors.Is(err, ErrInvalidReference) {
			t.Errorf("partial shape %d error must wrap ErrInvalidReference, got %v", i, err)
		}
		wrapped := &opError{op: stageLookup, cause: err}
		if s := wrapped.Error(); s != "postgres nodes: lookup file reference" || len(s) > 200 {
			t.Errorf("partial shape %d Error() = %q, want bounded stageLookup text", i, s)
		}
	}
}

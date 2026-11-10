package repository

import (
	"errors"
	"reflect"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

// 构造合法 Source：正 ID + 文件 Path + Content 摘要由用例覆盖。
// Build a valid source: positive ID, file Path; Content digests per case.
func overwriteSourceTestSource(t *testing.T, sha1, sha256 string, sha1Valid, sha256Valid bool) *FileSnapshotSource {
	t.Helper()
	p, err := NewPath("rk", "dir", "a.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	return &FileSnapshotSource{Reference: FileReference{
		ID: 7, Path: p,
		Content: content.Metadata{SHA1: sha1, SHA256: sha256, MD5: "ignored"},
	}, SHA1ActualValid: sha1Valid, SHA256ActualValid: sha256Valid}
}

func overwriteSourceTestUnits(s string) []uint16 {
	u := make([]uint16, len(s))
	for i := range u {
		u[i] = uint16(s[i])
	}
	return u
}

func overwriteSourceTestError(t *testing.T, src *FileSnapshotSource, enforce bool, req1, req256 []uint16) {
	t.Helper()
	got, err := DecideFileOverwriteFromSource(src, enforce, req1, req256)
	if !errors.Is(err, ErrInvalidFileSnapshotSource) {
		t.Fatalf("error=%v, want %v", err, ErrInvalidFileSnapshotSource)
	}
	if got {
		t.Fatal("error case returned true")
	}
}

// nil Source：可信调用方已确认缺失，不做查找/错误分类，一票否决。
func TestDecideFileOverwriteFromSourceNilSource(t *testing.T) {
	for _, enforce := range []bool{false, true} {
		got, err := DecideFileOverwriteFromSource(nil, enforce,
			overwriteSourceTestUnits("abc"), overwriteSourceTestUnits("def"))
		if err != nil || got {
			t.Fatalf("nil source enforce=%v: got (%v,%v), want (false,nil)", enforce, got, err)
		}
	}
}

// 非法 Source：非正 ID 或非文件 Path 固定失败。
func TestDecideFileOverwriteFromSourceInvalidSource(t *testing.T) {
	folder, err := NewPath("rk", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	overwriteSourceTestError(t, &FileSnapshotSource{}, false, nil, nil)
	overwriteSourceTestError(t, &FileSnapshotSource{Reference: FileReference{ID: 0, Path: folder}}, true, nil, nil)
	overwriteSourceTestError(t, &FileSnapshotSource{Reference: FileReference{ID: -1, Path: folder}},
		false, overwriteSourceTestUnits("aa"), nil)
	// 有 ID 但零值 Path（非文件）。
	p, _ := NewPath("rk", "dir", "a.bin", true)
	overwriteSourceTestError(t, &FileSnapshotSource{Reference: FileReference{ID: 5}}, false, nil, nil)
	// 文件 Path 但非正 ID。
	overwriteSourceTestError(t, &FileSnapshotSource{Reference: FileReference{ID: 0, Path: p}}, false, nil, nil)
}

// 空性矛盾与畸形 UTF-8：任一槽位独立失败，即使请求省略该槽或 flag 会短路。
func TestDecideFileOverwriteFromSourceInvalidSlots(t *testing.T) {
	malformed := "\xff\xfe"
	// SHA1 槽矛盾：SHA256 合法、请求只给 SHA256、flag 亦不豁免。
	overwriteSourceTestError(t, overwriteSourceTestSource(t, "abc", "zz", false, true), true,
		nil, overwriteSourceTestUnits("zz"))
	// SHA256 槽矛盾：请求省略 SHA256。
	overwriteSourceTestError(t, overwriteSourceTestSource(t, "abc", "zz", true, false), false,
		overwriteSourceTestUnits("abc"), nil)
	// SHA1 畸形 UTF-8：双请求摘要均为 nil 时仍失败。
	overwriteSourceTestError(t, overwriteSourceTestSource(t, malformed, "zz", true, true), false, nil, nil)
	// SHA256 畸形 UTF-8：flag 开亦失败。
	overwriteSourceTestError(t, overwriteSourceTestSource(t, "abc", malformed, true, true), true,
		overwriteSourceTestUnits("abc"), overwriteSourceTestUnits("dd"))
}

// NULL/空/文本三态投影与决策复用。
func TestDecideFileOverwriteFromSourceDecisions(t *testing.T) {
	sha1Text := "s1"
	sha256Text := "s2"
	tests := []struct {
		name    string
		src     *FileSnapshotSource
		enforce bool
		req1    []uint16
		req256  []uint16
		want    bool
	}{
		{name: "both request nil", src: overwriteSourceTestSource(t, sha1Text, sha256Text, true, true), want: true},
		{name: "sha1 equal null stored sha256", src: overwriteSourceTestSource(t, sha1Text, "", true, false),
			req1: overwriteSourceTestUnits(sha1Text), want: false},
		{name: "sha1 equal empty stored", src: overwriteSourceTestSource(t, "", sha256Text, true, true),
			req1: []uint16{}, want: false},
		{name: "sha1 empty request differs from null", src: overwriteSourceTestSource(t, "", sha256Text, false, true),
			req1: []uint16{}, want: true},
		{name: "sha256 equal empty stored", src: overwriteSourceTestSource(t, sha1Text, "", true, true),
			req256: []uint16{}, want: false},
		{name: "sha256 empty request differs from null", src: overwriteSourceTestSource(t, sha1Text, "", true, false),
			req256: []uint16{}, want: true},
		{name: "sha256 exact utf16", src: overwriteSourceTestSource(t, sha1Text, " Aé😀\x00", true, true),
			req256: []uint16{' ', 'A', 0x00e9, 0xd83d, 0xde00, 0}, want: false},
		{name: "sha1 differ", src: overwriteSourceTestSource(t, sha1Text, sha256Text, true, true),
			req1: overwriteSourceTestUnits("XX"), want: true},
		{name: "sha256 differ sha1 absent", src: overwriteSourceTestSource(t, sha1Text, sha256Text, true, true),
			req256: overwriteSourceTestUnits("YY"), want: true},
		{name: "both equal enforce off", src: overwriteSourceTestSource(t, sha1Text, sha256Text, true, true),
			req1: overwriteSourceTestUnits(sha1Text), req256: overwriteSourceTestUnits(sha256Text), want: false},
		{name: "both equal enforce on", src: overwriteSourceTestSource(t, sha1Text, sha256Text, true, true),
			enforce: true, req1: overwriteSourceTestUnits(sha1Text), req256: overwriteSourceTestUnits(sha256Text), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecideFileOverwriteFromSource(tt.src, tt.enforce, tt.req1, tt.req256)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// 输入不可变：调用前后 Source 与请求切片内容/nil 性不变。
func TestDecideFileOverwriteFromSourceImmutability(t *testing.T) {
	src := overwriteSourceTestSource(t, "abc", "def", true, true)
	req1 := overwriteSourceTestUnits("xyz")
	req256 := overwriteSourceTestUnits("uvw")
	wantSrc := *src
	wantReq1 := append([]uint16(nil), req1...)
	wantReq256 := append([]uint16(nil), req256...)
	for i := 0; i < 2; i++ {
		if _, err := DecideFileOverwriteFromSource(src, true, req1, req256); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(*src, wantSrc) {
		t.Fatalf("source mutated: %+v want %+v", *src, wantSrc)
	}
	if !reflect.DeepEqual(req1, wantReq1) || !reflect.DeepEqual(req256, wantReq256) {
		t.Fatal("request slices mutated")
	}
}

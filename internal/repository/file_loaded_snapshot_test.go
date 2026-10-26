package repository

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

func loadedSnapshotTestASCII(s string) []uint16 {
	u := make([]uint16, len(s))
	for i := range u {
		u[i] = uint16(s[i])
	}
	return u
}

func loadedSnapshotTestRepeat(u uint16, n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = u
	}
	return out
}

// 每次构造独立 Source；既有值构造器仅用于输入，不用于计算输出期望。
// Fresh source per call; existing value constructors build inputs, never expectations.
func loadedSnapshotTestSource(t *testing.T, sha1, md5 []uint16) FileSnapshotSource {
	t.Helper()
	p, err := NewPath(" rk ", "dir", "a.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	m := NewFileMetadata(FileMetadataInput{
		CreatedMillis: 11, ModifiedMillis: 22, UpdatedMillis: 33,
		CreatedBy: NewActor(loadedSnapshotTestASCII("creator")), ModifiedBy: NewActor(loadedSnapshotTestASCII("modifier")),
		SHA1Original: sha1, MD5Original: md5,
	})
	return FileSnapshotSource{Reference: FileReference{
		ID: -9, Path: p, Content: contentMetadataForLoadedSnapshotTest(), Metadata: &m,
	}, MD5ActualValid: true, SHA256ActualValid: true}
}

// 固定字符串大小写/长度故意不规范；组装不是保存接受检查。
// Deliberately noncanonical content: assembly is not save acceptance.
func contentMetadataForLoadedSnapshotTest() content.Metadata {
	return content.Metadata{SHA256: "bad", Size: 42}
}

func loadedSnapshotTestWant() FileInfoSnapshotInput {
	u := loadedSnapshotTestASCII
	return FileInfoSnapshotInput{
		RepoKeyUTF16: u("rk"), RepoPathUTF16: u("dir/a.bin"), NameUTF16: u("a.bin"),
		CreatedMillis: 11, LastModifiedMillis: 22, LastUpdatedMillis: 33,
		CreatedByUTF16: u("creator"), ModifiedByUTF16: u("modifier"), SizeBytes: 42,
		Checksums: []ChecksumEntryInput{
			{Kind: ChecksumSHA1}, {Kind: ChecksumMD5, Actual: []uint16{}},
			{Kind: ChecksumSHA256, Actual: u("bad"), Original: u("NO_ORIG")},
		},
	}
}

// 核对所有即时字段；校验和按键比较并拒绝重复，不冻结 Fields 的次序。
// Verify every immediate field, keyed checksum values/count and uniqueness.
func loadedSnapshotTestCheck(t *testing.T, s FileInfoSnapshot, want FileInfoSnapshotInput) {
	t.Helper()
	got := s.Fields()
	gc, wc := got.Checksums, want.Checksums
	got.Checksums, want.Checksums = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("immediate fields got %+v want %+v", got, want)
	}
	byKind := make(map[ChecksumKind]ChecksumEntryInput)
	for _, c := range gc {
		if _, exists := byKind[c.Kind]; exists {
			t.Errorf("duplicate checksum kind %d", c.Kind)
		}
		byKind[c.Kind] = c
	}
	if len(gc) != 3 || len(byKind) != 3 {
		t.Errorf("checksum entries=%d unique=%d, want exactly three", len(gc), len(byKind))
	}
	for _, w := range wc {
		g, ok := byKind[w.Kind]
		if !ok || !reflect.DeepEqual(g, w) {
			t.Errorf("checksum kind %d got %+v present=%v want %+v", w.Kind, g, ok, w)
		}
	}
}

func loadedSnapshotTestCall(t *testing.T, src FileSnapshotSource) FileInfoSnapshot {
	t.Helper()
	s, err := LoadedFileSnapshot(src)
	if err != nil {
		t.Fatalf("LoadedFileSnapshot: %v", err)
	}
	return s
}

func loadedSnapshotTestError(t *testing.T, src FileSnapshotSource, want error) {
	t.Helper()
	s, err := LoadedFileSnapshot(src)
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("error=%v, want fixed %v", err, want)
	}
	if !reflect.DeepEqual(s.Fields(), FileInfoSnapshotInput{}) {
		t.Error("failure returned partial snapshot fields")
	}
	if _, zerr := s.IsIdentical(s, false); !errors.Is(zerr, ErrInvalidFileInfoSnapshot) {
		t.Error("failure returned a valid rather than zero snapshot")
	}
}

func loadedSnapshotTestActual(src *FileSnapshotSource, slot int, text string, valid bool) {
	switch slot {
	case 0:
		src.Reference.Content.SHA1, src.SHA1ActualValid = text, valid
	case 1:
		src.Reference.Content.MD5, src.MD5ActualValid = text, valid
	case 2:
		src.Reference.Content.SHA256, src.SHA256ActualValid = text, valid
	}
}

func TestLoadedFileSnapshotCompleteImmediateState(t *testing.T) {
	src := loadedSnapshotTestSource(t, loadedSnapshotTestRepeat('A', 40), loadedSnapshotTestRepeat('F', 32))
	want := loadedSnapshotTestWant()
	want.Checksums[0].Original, want.Checksums[1].Original = loadedSnapshotTestRepeat('a', 40), loadedSnapshotTestRepeat('f', 32)
	for _, id := range []int64{-9, 0, 7} {
		src.Reference.ID = id
		loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, src), want)
	}
	for _, row := range []struct{ nodeName, rawPath, cachedName string }{
		{"f:properties:statistics", "f:properties:statistics", "f:properties"},
		{"", "", ""},
	} {
		p, err := NewPath("rk", "", row.nodeName, true)
		if err != nil {
			t.Fatal(err)
		}
		src.Reference.Path = p
		want.RepoPathUTF16, want.NameUTF16 = loadedSnapshotTestASCII(row.rawPath), loadedSnapshotTestASCII(row.cachedName)
		loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, src), want)
	}
}

func TestLoadedFileSnapshotOriginalSupportedProfile(t *testing.T) {
	u, rep := loadedSnapshotTestASCII, loadedSnapshotTestRepeat
	rows := []struct {
		name                         string
		sha1, md5, wantSHA1, wantMD5 []uint16
	}{
		{"null", nil, nil, nil, nil},
		{"empty", []uint16{}, []uint16{}, []uint16{}, []uint16{}},
		{"marker", u("NO_ORIG"), u("NO_ORIG"), u("NO_ORIG"), u("NO_ORIG")},
		{"mixed hex", u(strings.Repeat("ABCDEF0123456789", 3)[:40]), u(strings.Repeat("FEdcBA9876543210", 2)), u(strings.Repeat("abcdef0123456789", 3)[:40]), u(strings.Repeat("fedcba9876543210", 2))},
		{"short raw capitals", rep('I', 39), rep('I', 31), rep('I', 39), rep('I', 31)},
		{"long raw unicode", append([]uint16{0x0130}, rep('A', 40)...), append([]uint16{0x0130}, rep('A', 32)...), append([]uint16{0x0130}, rep('A', 40)...), append([]uint16{0x0130}, rep('A', 32)...)},
		{"swapped type lengths", rep('A', 32), rep('F', 40), rep('A', 32), rep('F', 40)},
		{"raw nul and surrogates", []uint16{'X', 0, 0xd800}, []uint16{0xdc00, 'Y'}, []uint16{'X', 0, 0xd800}, []uint16{0xdc00, 'Y'}},
		{"utf8 byte length is not unit length", rep(0x00e9, 20), rep(0x00e9, 16), rep(0x00e9, 20), rep(0x00e9, 16)},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			want := loadedSnapshotTestWant()
			want.Checksums[0].Original, want.Checksums[1].Original = row.wantSHA1, row.wantMD5
			loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, loadedSnapshotTestSource(t, row.sha1, row.md5)), want)
		})
	}
}

// 目标可成功但本地画像不支持；不能把此错误称为目标摘要校验拒绝。
// Target-success states outside the local profile, not target checksum rejection.
func TestLoadedFileSnapshotExactLengthUnsupportedOriginal(t *testing.T) {
	for slot, n := range []int{40, 32} {
		for _, bad := range []uint16{'G', 'I', 0, 0x0130, 0xd800} {
			src := loadedSnapshotTestSource(t, nil, nil)
			m := FileMetadataInput{CreatedMillis: 11, ModifiedMillis: 22, UpdatedMillis: 33}
			units := append(loadedSnapshotTestRepeat('A', n-1), bad)
			if slot == 0 {
				m.SHA1Original = units
			} else {
				m.MD5Original = units
			}
			metadata := NewFileMetadata(m)
			src.Reference.Metadata = &metadata
			loadedSnapshotTestError(t, src, ErrUnsupportedLoadedOriginal)
		}
		// 恰长代理对按 UTF-16 计数，不能因 rune 数减半而误判为原样域。
		// Exact-length pairs count as UTF-16, not half as many runes.
		pairs := make([]uint16, 0, n)
		for len(pairs) < n {
			pairs = append(pairs, 0xd83d, 0xde00)
		}
		src := loadedSnapshotTestSource(t, nil, nil)
		if slot == 0 {
			src = loadedSnapshotTestSource(t, pairs, nil)
		} else {
			src = loadedSnapshotTestSource(t, nil, pairs)
		}
		loadedSnapshotTestError(t, src, ErrUnsupportedLoadedOriginal)
	}
}

func TestLoadedFileSnapshotActualNullEmptyAndNoncanonical(t *testing.T) {
	for slot := 0; slot < 3; slot++ {
		for _, row := range []struct {
			valid bool
			text  string
			want  []uint16
		}{
			{false, "", nil}, {true, "", []uint16{}},
			{true, " N\x00\U0001f600", []uint16{' ', 'N', 0, 0xd83d, 0xde00}},
		} {
			src := loadedSnapshotTestSource(t, nil, nil)
			loadedSnapshotTestActual(&src, slot, row.text, row.valid)
			want := loadedSnapshotTestWant()
			want.Checksums[slot].Actual = row.want
			loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, src), want)
		}
	}
}

func TestLoadedFileSnapshotSourceErrorsAndPathPriority(t *testing.T) {
	src := loadedSnapshotTestSource(t, nil, nil)
	src.Reference.Metadata = nil
	loadedSnapshotTestError(t, src, ErrInvalidFileSnapshotSource)
	src.Reference.Path = Path{}
	loadedSnapshotTestError(t, src, ErrInvalidPath)
	folder, err := NewPath("rk", "dir", "a.bin", false)
	if err != nil {
		t.Fatal(err)
	}
	src.Reference.Path = folder
	loadedSnapshotTestError(t, src, ErrInvalidPath)
	for slot := 0; slot < 3; slot++ {
		for _, row := range []struct {
			valid bool
			text  string
		}{{false, "contradiction"}, {true, "\xff"}} {
			src := loadedSnapshotTestSource(t, loadedSnapshotTestRepeat('G', 40), nil)
			loadedSnapshotTestActual(&src, slot, row.text, row.valid)
			loadedSnapshotTestError(t, src, ErrInvalidFileSnapshotSource)
		}
	}
	src = loadedSnapshotTestSource(t, nil, nil)
	zero := FileMetadata{}
	src.Reference.Metadata = &zero
	want := loadedSnapshotTestWant()
	want.CreatedMillis, want.LastModifiedMillis, want.LastUpdatedMillis = 0, 0, 0
	want.CreatedByUTF16, want.ModifiedByUTF16 = nil, nil
	loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, src), want)
}

func TestLoadedFileSnapshotTimesAndRawActors(t *testing.T) {
	for _, row := range []struct{ c, m, u, size, wantModified int64 }{
		{11, 0, 33, 42, 11}, {-7, -1, 0, -3, -7},
		{-9223372036854775808, 0, 9223372036854775807, -9223372036854775808, -9223372036854775808},
		{9223372036854775807, 1, -9223372036854775808, 9223372036854775807, 1},
	} {
		src := loadedSnapshotTestSource(t, nil, nil)
		m := NewFileMetadata(FileMetadataInput{CreatedMillis: row.c, ModifiedMillis: row.m, UpdatedMillis: row.u,
			CreatedBy: NewActor([]uint16{}), ModifiedBy: NewActor([]uint16{0xd800, 'm'})})
		src.Reference.Metadata, src.Reference.Content.Size = &m, row.size
		want := loadedSnapshotTestWant()
		want.CreatedMillis, want.LastModifiedMillis, want.LastUpdatedMillis, want.SizeBytes = row.c, row.wantModified, row.u, row.size
		want.CreatedByUTF16, want.ModifiedByUTF16 = []uint16{}, []uint16{0xd800, 'm'}
		loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, src), want)
	}
	src := loadedSnapshotTestSource(t, nil, nil)
	m := NewFileMetadata(FileMetadataInput{CreatedMillis: 11, ModifiedMillis: 22, UpdatedMillis: 33,
		CreatedBy: NewActor(loadedSnapshotTestRepeat('u', 65))})
	src.Reference.Metadata = &m
	want := loadedSnapshotTestWant()
	want.CreatedByUTF16, want.ModifiedByUTF16 = append(loadedSnapshotTestRepeat('u', 61), '.', '.', '.'), nil
	loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, src), want)
}

func loadedSnapshotTestOverwrite(f FileInfoSnapshotInput) {
	units := [][]uint16{f.RepoKeyUTF16, f.RepoPathUTF16, f.NameUTF16, f.CreatedByUTF16, f.ModifiedByUTF16, f.MimeTypeUTF16}
	for _, c := range f.Checksums {
		units = append(units, c.Actual, c.Original)
	}
	for _, u := range units {
		for i := range u {
			u[i] = 0xffff
		}
	}
	for i := range f.Checksums {
		f.Checksums[i].Kind = ChecksumKind(99)
	}
}

func TestLoadedFileSnapshotOwnershipAndConcurrentReads(t *testing.T) {
	sha, md5 := []uint16{'X', 0xd800}, []uint16{'Y', 0, 0xdc00}
	src := loadedSnapshotTestSource(t, sha, md5)
	want := loadedSnapshotTestWant()
	want.Checksums[0].Original, want.Checksums[1].Original = []uint16{'X', 0xd800}, []uint16{'Y', 0, 0xdc00}
	a, b := loadedSnapshotTestCall(t, src), loadedSnapshotTestCall(t, src)
	sha[0], md5[0] = 0xffff, 0xffff
	loadedSnapshotTestOverwrite(a.Fields())
	changed := src
	changed.Reference.Path, changed.Reference.Content.Size = Path{}, -1
	*changed.Reference.Metadata = FileMetadata{}
	loadedSnapshotTestCheck(t, a, want)
	loadedSnapshotTestCheck(t, b, want)
	// 并发输入重新独立构造；只读 DTO，写的只有各次 Fields 副本。
	// Fresh immutable source for concurrency; only returned copies mutate.
	src = loadedSnapshotTestSource(t, []uint16{'X', 0xd800}, []uint16{'Y', 0, 0xdc00})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				s, err := LoadedFileSnapshot(src)
				if err != nil {
					t.Errorf("concurrent assembly: %v", err)
					continue
				}
				loadedSnapshotTestCheck(t, s, want)
				loadedSnapshotTestOverwrite(s.Fields())
				loadedSnapshotTestCheck(t, s, want)
			}
		}()
	}
	wg.Wait()
	loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, src), want)
}

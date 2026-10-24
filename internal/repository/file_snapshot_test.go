// FileInfoSnapshot 验收套件：冻结的纯值 + IsIdentical 比较器契约，原始
// UTF-16 操作数、显式 ignoreCreated、ErrInvalidFileInfoSnapshot 拒绝。辅助
// 函数带 snapshot 前缀避免同包冲突；Fields() 校验和条目次序不是契约，
// 一律按键比较；nil 与非 nil 空码元序列严格区分。用例 ID 前缀（ACT、CK、
// PT、CR、MM、TS、AL、GR、LOCAL）对应评审清单。
// Acceptance suite for the frozen pure-value FileInfoSnapshot + IsIdentical
// comparator contract: raw UTF-16 operands, explicit ignoreCreated and
// ErrInvalidFileInfoSnapshot rejections. Helpers are snapshot-prefixed;
// Fields() checksum order is not a contract and is always compared by kind;
// nil and non-nil empty unit sequences stay strictly distinct. Case-id
// prefixes map to the reviewed case list.
package repository

import (
	"errors"
	"strings"
	"testing"
)

// snapshotUnits builds UTF-16 units from an ASCII test literal.
func snapshotUnits(s string) []uint16 {
	r := []rune(s)
	out := make([]uint16, len(r))
	for i, c := range r {
		out[i] = uint16(c)
	}
	return out
}

func snapshotHexUnits(n int) []uint16 { return snapshotUnits(strings.Repeat("a", n)) }

// snapshotUnitsEqual compares UTF-16 unit sequences with nil and non-nil
// empty kept distinct (nil = Java null, empty = empty string).
func snapshotUnitsEqual(a, b []uint16) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
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

// snapshotChecksumsByKey indexes entries by kind for order-free comparison.
// Duplicate or unknown kinds make the side invalid (ok=false): a duplicate
// entry must never be silently folded away into a false-green comparison.
func snapshotChecksumsByKey(cs []ChecksumEntryInput) (map[ChecksumKind]ChecksumEntryInput, bool) {
	m := make(map[ChecksumKind]ChecksumEntryInput, len(cs))
	for _, c := range cs {
		if c.Kind != ChecksumSHA1 && c.Kind != ChecksumMD5 && c.Kind != ChecksumSHA256 {
			return nil, false
		}
		if _, dup := m[c.Kind]; dup {
			return nil, false
		}
		m[c.Kind] = c
	}
	return m, true
}

// snapshotInputsEqual compares two snapshot DTOs: scalars directly, slices
// with nil/empty distinction, checksums by keyed contents with entry counts
// also compared. Checksum slice order is deliberately NOT compared.
func snapshotInputsEqual(got, want FileInfoSnapshotInput) bool {
	if got.CreatedMillis != want.CreatedMillis ||
		got.LastModifiedMillis != want.LastModifiedMillis ||
		got.LastUpdatedMillis != want.LastUpdatedMillis ||
		got.SizeBytes != want.SizeBytes {
		return false
	}
	if !snapshotUnitsEqual(got.RepoKeyUTF16, want.RepoKeyUTF16) ||
		!snapshotUnitsEqual(got.RepoPathUTF16, want.RepoPathUTF16) ||
		!snapshotUnitsEqual(got.NameUTF16, want.NameUTF16) ||
		!snapshotUnitsEqual(got.CreatedByUTF16, want.CreatedByUTF16) ||
		!snapshotUnitsEqual(got.ModifiedByUTF16, want.ModifiedByUTF16) ||
		!snapshotUnitsEqual(got.MimeTypeUTF16, want.MimeTypeUTF16) {
		return false
	}
	ga, gaOK := snapshotChecksumsByKey(got.Checksums)
	gb, gbOK := snapshotChecksumsByKey(want.Checksums)
	if !gaOK || !gbOK || len(got.Checksums) != len(want.Checksums) || len(ga) != len(gb) {
		return false
	}
	for k, e := range gb {
		oe, ok := ga[k]
		if !ok || !snapshotUnitsEqual(oe.Actual, e.Actual) ||
			!snapshotUnitsEqual(oe.Original, e.Original) {
			return false
		}
	}
	return true
}

func snapshotBaseInput() FileInfoSnapshotInput {
	return FileInfoSnapshotInput{
		RepoKeyUTF16:       snapshotUnits("repo"),
		RepoPathUTF16:      snapshotUnits("a/f"),
		NameUTF16:          snapshotUnits("f"),
		CreatedMillis:      1000,
		LastModifiedMillis: 2000,
		LastUpdatedMillis:  3000,
		CreatedByUTF16:     snapshotUnits("alice"),
		ModifiedByUTF16:    snapshotUnits("bob"),
		SizeBytes:          42,
		MimeTypeUTF16:      snapshotUnits("application/octet-stream"),
		Checksums: []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: snapshotHexUnits(40), Original: nil},
			{Kind: ChecksumSHA256, Actual: snapshotHexUnits(64), Original: snapshotUnits("NO_ORIG")},
		},
	}
}

func snapshotMust(t *testing.T, in FileInfoSnapshotInput) FileInfoSnapshot {
	t.Helper()
	s, err := NewFileInfoSnapshot(in)
	if err != nil {
		t.Fatalf("NewFileInfoSnapshot: unexpected error: %v", err)
	}
	return s
}

func snapshotWantInvalid(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidFileInfoSnapshot) {
		t.Errorf("%s: error = %v, want ErrInvalidFileInfoSnapshot", name, err)
	}
}

func snapshotPair(t *testing.T, mutA, mutB func(*FileInfoSnapshotInput)) (FileInfoSnapshot, FileInfoSnapshot) {
	t.Helper()
	a := snapshotBaseInput()
	b := snapshotBaseInput()
	if mutA != nil {
		mutA(&a)
	}
	if mutB != nil {
		mutB(&b)
	}
	return snapshotMust(t, a), snapshotMust(t, b)
}

func snapshotCheck(t *testing.T, name string, a, b FileInfoSnapshot, ignoreCreated, want bool) {
	t.Helper()
	got, err := a.IsIdentical(b, ignoreCreated)
	if err != nil {
		t.Errorf("%s: IsIdentical error = %v, want nil", name, err)
		return
	}
	if got != want {
		t.Errorf("%s: IsIdentical(ignoreCreated=%v) = %v, want %v", name, ignoreCreated, got, want)
	}
}

// ---- GroupValidation (LOCAL-1, LOCAL-2): constructor rejections ----

func TestNewRequiredFieldsNonNil(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*FileInfoSnapshotInput)
	}{
		{"nil repoKey", func(in *FileInfoSnapshotInput) { in.RepoKeyUTF16 = nil }},
		{"nil repoPath", func(in *FileInfoSnapshotInput) { in.RepoPathUTF16 = nil }},
		{"nil name", func(in *FileInfoSnapshotInput) { in.NameUTF16 = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := snapshotBaseInput()
			tc.mut(&in)
			_, err := NewFileInfoSnapshot(in)
			snapshotWantInvalid(t, tc.name, err)
		})
	}
	in := snapshotBaseInput()
	s := snapshotMust(t, in)
	if got := s.Fields(); !snapshotInputsEqual(got, in) {
		t.Errorf("Fields() round-trip (keyed): got %v, want input", got)
	}
}

func TestNewChecksumKindsValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		cs   []ChecksumEntryInput
	}{
		{"duplicate kind", []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: snapshotHexUnits(40)},
			{Kind: ChecksumSHA1, Actual: snapshotHexUnits(40)},
		}},
		{"unknown kind zero", []ChecksumEntryInput{{Kind: 0, Actual: snapshotHexUnits(40)}}},
		{"unknown kind four", []ChecksumEntryInput{{Kind: 4, Actual: snapshotHexUnits(40)}}},
		{"unknown kind 255", []ChecksumEntryInput{{Kind: 255, Actual: snapshotHexUnits(40)}}},
		// Four entries exceed the three-kind universe: rejected up front,
		// before any checksum map capacity is allocated from input length.
		{"four entries beyond kind universe", []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: snapshotHexUnits(40)},
			{Kind: ChecksumMD5, Actual: snapshotHexUnits(32)},
			{Kind: ChecksumSHA256, Actual: snapshotHexUnits(64)},
			{Kind: ChecksumSHA1, Actual: snapshotHexUnits(40)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := snapshotBaseInput()
			in.Checksums = tc.cs
			_, err := NewFileInfoSnapshot(in)
			snapshotWantInvalid(t, tc.name, err)
		})
	}
	// Boundary control: all three known kinds at once are the valid maximum.
	in := snapshotBaseInput()
	in.Checksums = []ChecksumEntryInput{
		{Kind: ChecksumSHA1, Actual: snapshotHexUnits(40)},
		{Kind: ChecksumMD5, Actual: snapshotHexUnits(32)},
		{Kind: ChecksumSHA256, Actual: snapshotHexUnits(64), Original: snapshotUnits("NO_ORIG")},
	}
	s := snapshotMust(t, in)
	if got := s.Fields(); !snapshotInputsEqual(got, in) {
		t.Errorf("three-kind Fields() round-trip (keyed): got %v, want input", got)
	}
}

// ---- GroupZero (LOCAL-3): zero snapshot is invalid ----

func TestZeroSnapshotInvalid(t *testing.T) {
	var zero FileInfoSnapshot
	if got := zero.Fields(); !snapshotInputsEqual(got, FileInfoSnapshotInput{}) {
		t.Errorf("zero Fields() = %v, want zero input", got)
	}
	valid := snapshotMust(t, snapshotBaseInput())
	for _, tc := range []struct {
		name          string
		a, b          FileInfoSnapshot
		ignoreCreated bool
	}{
		{"zero vs valid", zero, valid, false},
		{"valid vs zero", valid, zero, true},
		{"zero vs zero", zero, zero, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.a.IsIdentical(tc.b, tc.ignoreCreated)
			snapshotWantInvalid(t, tc.name, err)
		})
	}
}

// ---- GroupChecksumsContainer (CK-3 + proposal): nil/empty both empty graph ----

func TestChecksumsNilAndEmptyEmptyGraph(t *testing.T) {
	inNil := snapshotBaseInput()
	inNil.Checksums = nil
	sNil := snapshotMust(t, inNil) // must NOT error: nil = valid empty graph

	inEmpty := snapshotBaseInput()
	inEmpty.Checksums = []ChecksumEntryInput{}
	sEmpty := snapshotMust(t, inEmpty)

	for _, ig := range []bool{false, true} {
		if got, err := sNil.IsIdentical(sEmpty, ig); err != nil || got != true {
			t.Errorf("nil vs empty (ignoreCreated=%v): got (%v,%v), want (true,nil)", ig, got, err)
		}
		if got, err := sEmpty.IsIdentical(sNil, ig); err != nil || got != true {
			t.Errorf("empty vs nil (ignoreCreated=%v): got (%v,%v), want (true,nil)", ig, got, err)
		}
	}
	if got := sNil.Fields().Checksums; len(got) != 0 {
		t.Errorf("nil-input Fields().Checksums len = %d, want 0", len(got))
	}
	if got := sEmpty.Fields().Checksums; len(got) != 0 {
		t.Errorf("empty-input Fields().Checksums len = %d, want 0", len(got))
	}
}

// TestFileInfoSnapshotComparatorBasics checks the explicit created policy.
// 基础比较检查：只有 created 字段可由显式参数跳过。
func TestFileInfoSnapshotComparatorBasics(t *testing.T) {
	a := snapshotMust(t, snapshotBaseInput())
	in := snapshotBaseInput()
	in.CreatedMillis++
	b := snapshotMust(t, in)
	snapshotCheck(t, "created compared", a, b, false, false)
	snapshotCheck(t, "created skipped", a, b, true, true)
	in.NameUTF16 = snapshotUnits("other")
	c := snapshotMust(t, in)
	snapshotCheck(t, "name still compared", a, c, true, false)
	snapshotCheck(t, "self", a, a, false, true)
}

// 文件快照边界与所有权回归；基础夹具见 file_snapshot_test.go。
// Snapshot boundary and ownership regressions; shared fixtures live in file_snapshot_test.go.
package repository

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
)

// ---- GroupCopyOwnership (AL-1, AL-2, OWN-1): deep copies both directions ----

func TestFieldsDefensiveCopy(t *testing.T) {
	s := snapshotMust(t, snapshotBaseInput())
	want := snapshotBaseInput() // independent backing: must not alias snapshot storage
	f := s.Fields()
	// Mutate every nested slice of the returned DTO.
	for _, sl := range []*[]uint16{
		&f.RepoKeyUTF16, &f.RepoPathUTF16, &f.NameUTF16,
		&f.CreatedByUTF16, &f.ModifiedByUTF16, &f.MimeTypeUTF16,
	} {
		if len(*sl) > 0 {
			(*sl)[0] = 'X'
		}
	}
	for i := range f.Checksums {
		f.Checksums[i].Kind = ChecksumMD5
		if len(f.Checksums[i].Actual) > 0 {
			f.Checksums[i].Actual[0] = 'X'
		}
		if len(f.Checksums[i].Original) > 0 {
			f.Checksums[i].Original[0] = 'X'
		}
	}
	if got := s.Fields(); !snapshotInputsEqual(got, want) {
		t.Errorf("Fields() after mutating returned copy: snapshot changed")
	}
}

func TestInputMutationIsolation(t *testing.T) {
	// Every input slice gets its own backing store; several deliberately
	// overlap one another to exercise shared-backing capture.
	shared := snapshotUnits("shared") // name + repoKey overlap
	forty := snapshotHexUnits(40)     // sha1 actual + MIME overlap
	sixtyFour := snapshotHexUnits(64) // sha256 actual + modifiedBy overlap
	in := snapshotBaseInput()
	in.RepoKeyUTF16 = shared
	in.NameUTF16 = shared
	in.MimeTypeUTF16 = forty
	in.Checksums[0].Actual = forty
	in.Checksums[0].Original = snapshotUnits("original")
	in.Checksums[1].Actual = sixtyFour
	in.ModifiedByUTF16 = sixtyFour
	// Independent expected fixture (fresh literals, zero sharing).
	want := snapshotBaseInput()
	want.RepoKeyUTF16 = snapshotUnits("shared")
	want.NameUTF16 = snapshotUnits("shared")
	want.MimeTypeUTF16 = snapshotHexUnits(40)
	want.Checksums[0].Actual = snapshotHexUnits(40)
	want.Checksums[0].Original = snapshotUnits("original")
	want.Checksums[1].Actual = snapshotHexUnits(64)
	want.ModifiedByUTF16 = snapshotHexUnits(64)

	s := snapshotMust(t, in)
	// Mutate EVERY constructor input backing store after capture.
	shared[0] = 'X'
	forty[3] = 'X'
	sixtyFour[5] = 'X'
	in.RepoPathUTF16[1] = 'X'
	in.CreatedByUTF16[0] = 'X'
	in.Checksums[0].Original[0] = 'X'
	in.Checksums[0].Original = snapshotUnits("ZZZZ") // rebinding cannot matter
	in.Checksums[0].Kind = ChecksumMD5
	in.Checksums[1].Original[0] = 'X'
	in.SizeBytes = -1
	if got := s.Fields(); !snapshotInputsEqual(got, want) {
		t.Errorf("Fields() after mutating every input backing store: snapshot changed")
	}

	// Present-empty high-capacity slice: caller growth must not leak in.
	pe := make([]uint16, 0, 8)
	in2 := snapshotBaseInput()
	in2.MimeTypeUTF16 = pe
	s2 := snapshotMust(t, in2)
	_ = append(pe, 'X')
	if got := s2.Fields().MimeTypeUTF16; got == nil || len(got) != 0 {
		t.Errorf("present-empty MIME = %v (nil=%v), want nonnil empty", got, got == nil)
	}
}

func TestConcurrentFieldsReaders(t *testing.T) {
	s := snapshotMust(t, snapshotBaseInput())
	want := snapshotBaseInput() // independent backing
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				f := s.Fields()
				if len(f.CreatedByUTF16) > 0 {
					f.CreatedByUTF16[0] = 'X'
				}
				if len(f.RepoKeyUTF16) > 0 {
					f.RepoKeyUTF16[0] = 'X'
				}
				if len(f.Checksums) > 0 && len(f.Checksums[0].Actual) > 0 {
					f.Checksums[0].Actual[0] = 'X'
				}
			}
		}()
	}
	wg.Wait()
	if got := s.Fields(); !snapshotInputsEqual(got, want) {
		t.Errorf("Fields() after concurrent readers mutating their own copies: snapshot changed")
	}
}

// ---- GroupPreservation: nil vs non-nil empty through Fields ----

func TestFieldsPreservesNilAndEmpty(t *testing.T) {
	// Every nullable unit field keeps nil vs non-nil empty exactly.
	unitFields := []struct {
		name string
		set  func(*FileInfoSnapshotInput, []uint16)
		get  func(FileInfoSnapshotInput) []uint16
	}{
		{"createdBy", func(in *FileInfoSnapshotInput, v []uint16) { in.CreatedByUTF16 = v },
			func(f FileInfoSnapshotInput) []uint16 { return f.CreatedByUTF16 }},
		{"modifiedBy", func(in *FileInfoSnapshotInput, v []uint16) { in.ModifiedByUTF16 = v },
			func(f FileInfoSnapshotInput) []uint16 { return f.ModifiedByUTF16 }},
		{"mimeType", func(in *FileInfoSnapshotInput, v []uint16) { in.MimeTypeUTF16 = v },
			func(f FileInfoSnapshotInput) []uint16 { return f.MimeTypeUTF16 }},
	}
	for _, fld := range unitFields {
		for _, tc := range []struct {
			name    string
			v       []uint16
			wantNil bool
		}{
			{"nil", nil, true},
			{"non-nil empty", []uint16{}, false},
		} {
			t.Run(fld.name+"-"+tc.name, func(t *testing.T) {
				in := snapshotBaseInput()
				fld.set(&in, tc.v)
				got := snapshotMust(t, in).Fields()
				g := fld.get(got)
				if tc.wantNil && g != nil {
					t.Errorf("%s nil not preserved: %v", fld.name, g)
				}
				if !tc.wantNil && (g == nil || len(g) != 0) {
					t.Errorf("%s non-nil empty not preserved: %v", fld.name, g)
				}
			})
		}
	}
	// Required fields keep non-nil emptiness too (valid zero-content state
	// component; full-state coverage lives in TestValidEmptyRequiredState).
	for _, fld := range []struct {
		name string
		set  func(*FileInfoSnapshotInput)
		get  func(FileInfoSnapshotInput) []uint16
	}{
		{"repoKey", func(in *FileInfoSnapshotInput) { in.RepoKeyUTF16 = []uint16{} },
			func(f FileInfoSnapshotInput) []uint16 { return f.RepoKeyUTF16 }},
		{"repoPath", func(in *FileInfoSnapshotInput) { in.RepoPathUTF16 = []uint16{} },
			func(f FileInfoSnapshotInput) []uint16 { return f.RepoPathUTF16 }},
		{"name", func(in *FileInfoSnapshotInput) { in.NameUTF16 = []uint16{} },
			func(f FileInfoSnapshotInput) []uint16 { return f.NameUTF16 }},
	} {
		t.Run("required-"+fld.name+"-non-nil-empty", func(t *testing.T) {
			in := snapshotBaseInput()
			fld.set(&in)
			g := fld.get(snapshotMust(t, in).Fields())
			if g == nil || len(g) != 0 {
				t.Errorf("required %s non-nil empty not preserved: %v", fld.name, g)
			}
		})
	}
	// Each kind's actual and original keep nil vs non-nil empty through Fields.
	// The count guard fatals BEFORE indexing: a wrong entry count is a clean
	// behavioral failure, never an indexing panic mistaken for a catch.
	for _, kind := range []ChecksumKind{ChecksumSHA1, ChecksumMD5, ChecksumSHA256} {
		for _, slot := range []struct {
			name string
			set  func(*ChecksumEntryInput, []uint16)
			get  func(ChecksumEntryInput) []uint16
		}{
			{"actual", func(e *ChecksumEntryInput, v []uint16) { e.Actual = v },
				func(e ChecksumEntryInput) []uint16 { return e.Actual }},
			{"original", func(e *ChecksumEntryInput, v []uint16) { e.Original = v },
				func(e ChecksumEntryInput) []uint16 { return e.Original }},
		} {
			for _, tc := range []struct {
				name    string
				v       []uint16
				wantNil bool
			}{
				{"nil", nil, true},
				{"non-nil empty", []uint16{}, false},
			} {
				t.Run(kindName(kind)+"-"+slot.name+"-"+tc.name, func(t *testing.T) {
					in := snapshotBaseInput()
					in.Checksums = []ChecksumEntryInput{{Kind: kind}}
					slot.set(&in.Checksums[0], tc.v)
					f := snapshotMust(t, in).Fields()
					if len(f.Checksums) != 1 {
						t.Fatalf("Fields checksum entries = %d, want 1", len(f.Checksums))
					}
					if f.Checksums[0].Kind != kind {
						t.Fatalf("Fields checksum kind = %v, want %v", f.Checksums[0].Kind, kind)
					}
					g := slot.get(f.Checksums[0])
					if tc.wantNil && g != nil {
						t.Errorf("%s %s nil not preserved: %v", kindName(kind), slot.name, g)
					}
					if !tc.wantNil && (g == nil || len(g) != 0) {
						t.Errorf("%s %s non-nil empty not preserved: %v", kindName(kind), slot.name, g)
					}
				})
			}
		}
	}
}

func kindName(k ChecksumKind) string {
	switch k {
	case ChecksumSHA1:
		return "sha1"
	case ChecksumMD5:
		return "md5"
	case ChecksumSHA256:
		return "sha256"
	}
	return "unknown"
}

// TestValidEmptyRequiredState: three non-nil empty required slices, zero
// scalars, nil actors/MIME and an empty checksum graph construct a VALID
// zero-content snapshot — distinct from the invalid unconstructed zero value.
func TestValidEmptyRequiredState(t *testing.T) {
	in := FileInfoSnapshotInput{
		RepoKeyUTF16:  []uint16{},
		RepoPathUTF16: []uint16{},
		NameUTF16:     []uint16{},
		Checksums:     []ChecksumEntryInput{},
	}
	s, err := NewFileInfoSnapshot(in)
	if err != nil {
		t.Fatalf("valid empty-required construction rejected: %v", err)
	}
	f := s.Fields()
	for _, g := range []struct {
		name string
		v    []uint16
	}{
		{"repoKey", f.RepoKeyUTF16}, {"repoPath", f.RepoPathUTF16}, {"name", f.NameUTF16},
	} {
		if g.v == nil || len(g.v) != 0 {
			t.Errorf("Fields %s = %v, want non-nil empty", g.name, g.v)
		}
	}
	if f.CreatedMillis != 0 || f.LastModifiedMillis != 0 || f.LastUpdatedMillis != 0 || f.SizeBytes != 0 {
		t.Errorf("Fields scalars = %+v, want all zero", f)
	}
	if len(f.Checksums) != 0 {
		t.Errorf("Fields checksum entries = %d, want 0", len(f.Checksums))
	}
	for _, ig := range []bool{false, true} {
		if got, err := s.IsIdentical(s, ig); err != nil || got != true {
			t.Errorf("self compare (ignoreCreated=%v) = (%v,%v), want (true,nil)", ig, got, err)
		}
	}
	var zero FileInfoSnapshot
	if _, err := zero.IsIdentical(zero, false); !errors.Is(err, ErrInvalidFileInfoSnapshot) {
		t.Errorf("unconstructed zero self compare err = %v, want ErrInvalidFileInfoSnapshot", err)
	}
}

// TestFieldsExactInt64Preservation: each int64 field reads back EXACTLY from
// Fields at both extremes and negative/zero values — not merely "unequal".
func TestFieldsExactInt64Preservation(t *testing.T) {
	fields := []struct {
		name string
		set  func(*FileInfoSnapshotInput, int64)
		get  func(FileInfoSnapshotInput) int64
	}{
		{"created", func(in *FileInfoSnapshotInput, v int64) { in.CreatedMillis = v },
			func(f FileInfoSnapshotInput) int64 { return f.CreatedMillis }},
		{"lastModified", func(in *FileInfoSnapshotInput, v int64) { in.LastModifiedMillis = v },
			func(f FileInfoSnapshotInput) int64 { return f.LastModifiedMillis }},
		{"lastUpdated", func(in *FileInfoSnapshotInput, v int64) { in.LastUpdatedMillis = v },
			func(f FileInfoSnapshotInput) int64 { return f.LastUpdatedMillis }},
		{"size", func(in *FileInfoSnapshotInput, v int64) { in.SizeBytes = v },
			func(f FileInfoSnapshotInput) int64 { return f.SizeBytes }},
	}
	for _, fld := range fields {
		for _, v := range []int64{math.MinInt64, -1, 0, 1, math.MaxInt64} {
			t.Run(fmt.Sprintf("%s=%d", fld.name, v), func(t *testing.T) {
				in := snapshotBaseInput()
				fld.set(&in, v)
				if got := fld.get(snapshotMust(t, in).Fields()); got != v {
					t.Errorf("Fields %s = %d, want exact %d", fld.name, got, v)
				}
			})
		}
	}
}

// ---- GroupActors (ACT-1..ACT-5): full raw untruncated actors ----

func TestIsIdenticalActorsRaw(t *testing.T) {
	raw64 := func(tail string) []uint16 {
		return append(snapshotUnits(strings.Repeat("a", 61)), snapshotUnits(tail)...)
	}
	rows := []struct {
		name string
		a, b []uint16
		want bool
	}{
		{"ACT-1 both null", nil, nil, true},
		{"ACT-1 both empty", []uint16{}, []uint16{}, true},
		{"ACT-2 null vs empty", nil, []uint16{}, false},
		{"ACT-3 case sensitive", snapshotUnits("Admin"), snapshotUnits("admin"), false},
		{"ACT-4 diverge beyond unit 61", raw64("AAAA"), raw64("BBBB"), false},
		{"ACT-4 equal beyond unit 61", raw64("AAAA"), raw64("AAAA"), true},
		{"ACT-5 surrogate vs 0x3F", []uint16{0xD800}, []uint16{0x003F}, false},
		{"ACT-5 equal unpaired surrogate", []uint16{0xD800}, []uint16{0xD800}, true},
	}
	for _, r := range rows {
		t.Run("modifiedBy/"+r.name, func(t *testing.T) {
			a, b := snapshotPair(t,
				func(in *FileInfoSnapshotInput) { in.ModifiedByUTF16 = r.a },
				func(in *FileInfoSnapshotInput) { in.ModifiedByUTF16 = r.b })
			snapshotCheck(t, r.name, a, b, false, r.want)
		})
	}
	// Raw storage guard: 65 units captured verbatim, never abbreviated.
	in := snapshotBaseInput()
	in.CreatedByUTF16 = raw64("AAAA")
	s := snapshotMust(t, in)
	f := s.Fields()
	if len(f.CreatedByUTF16) != 65 || f.CreatedByUTF16[64] != 'A' {
		t.Errorf("actor stored len=%d, want 65 untruncated units", len(f.CreatedByUTF16))
	}
}

// TestIsIdenticalCreatedByOnly: created-by is an independent compared operand.
func TestIsIdenticalCreatedByOnly(t *testing.T) {
	rows := []struct {
		name string
		a, b []uint16
		want bool
	}{
		{"createdby differ", snapshotUnits("alice"), snapshotUnits("carol"), false},
		{"createdby case", snapshotUnits("Alice"), snapshotUnits("alice"), false},
		{"createdby null vs empty", nil, []uint16{}, false},
		{"createdby equal", snapshotUnits("alice"), snapshotUnits("alice"), true},
		{"createdby differ after unit64", snapshotUnits(strings.Repeat("a", 64) + "x"), snapshotUnits(strings.Repeat("a", 64) + "y"), false},
		{"createdby lone surrogate", []uint16{0xd800}, []uint16{'?'}, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t,
				func(in *FileInfoSnapshotInput) { in.CreatedByUTF16 = r.a },
				func(in *FileInfoSnapshotInput) { in.CreatedByUTF16 = r.b })
			snapshotCheck(t, r.name, a, b, false, r.want)
			snapshotCheck(t, r.name+" (ignoreCreated)", a, b, true, r.want)
		})
	}
}

// ---- GroupNullable (CK-5..CK-10, MM-1..MM-3): null/empty/NO_ORIG tri-state ----

func TestIsIdenticalNullableFields(t *testing.T) {
	rows := []struct {
		name string
		a, b []uint16
		want bool
	}{
		{"CK-5 sha1 original null==null", nil, nil, true},
		{"CK-6 sha1 original null vs NO_ORIG", nil, snapshotUnits("NO_ORIG"), false},
		{"CK-7 sha1 original empty vs null", []uint16{}, nil, false},
		{"CK-7 sha1 original null vs empty", nil, []uint16{}, false},
		{"CK-10 sha1 actual null vs empty", nil, []uint16{}, false},
		{"CK-10 sha1 actual empty==empty", []uint16{}, []uint16{}, true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t,
				func(in *FileInfoSnapshotInput) { in.Checksums[0].Original = r.a },
				func(in *FileInfoSnapshotInput) { in.Checksums[0].Original = r.b })
			snapshotCheck(t, r.name, a, b, false, r.want)
		})
		t.Run("actual:"+r.name, func(t *testing.T) {
			a, b := snapshotPair(t,
				func(in *FileInfoSnapshotInput) { in.Checksums[0].Actual = r.a },
				func(in *FileInfoSnapshotInput) { in.Checksums[0].Actual = r.b })
			snapshotCheck(t, r.name, a, b, false, r.want)
		})
	}
	mime := []struct {
		name string
		a, b []uint16
		want bool
	}{
		{"MM-1 null==null with extension", nil, nil, true},
		{"MM-2 null vs resolved", nil, snapshotUnits("text/plain"), false},
		{"MM-3 empty vs null", []uint16{}, nil, false},
		{"mime case sensitive", snapshotUnits("Text/Plain"), snapshotUnits("text/plain"), false},
	}
	for _, r := range mime {
		t.Run(r.name, func(t *testing.T) {
			setA := func(x *FileInfoSnapshotInput) { x.MimeTypeUTF16 = r.a; x.RepoPathUTF16 = snapshotUnits("a/f.txt") }
			setB := func(x *FileInfoSnapshotInput) { x.MimeTypeUTF16 = r.b; x.RepoPathUTF16 = snapshotUnits("a/f.txt") }
			a, b := snapshotPair(t, setA, setB)
			snapshotCheck(t, r.name, a, b, false, r.want)
		})
	}
}

// ---- GroupPerKindActual: independent actual differences per kind ----

func TestIsIdenticalPerKindActual(t *testing.T) {
	only := func(k ChecksumKind, actual []uint16) func(*FileInfoSnapshotInput) {
		return func(in *FileInfoSnapshotInput) {
			in.Checksums = []ChecksumEntryInput{{Kind: k, Actual: actual}}
		}
	}
	lower40 := snapshotUnits(strings.Repeat("b", 40))
	upper40 := snapshotUnits(strings.Repeat("B", 40))
	lower32 := snapshotUnits(strings.Repeat("c", 32))
	upper32 := snapshotUnits(strings.Repeat("C", 32))
	lower64 := snapshotUnits(strings.Repeat("d", 64))
	upper64 := snapshotUnits(strings.Repeat("D", 64))
	for _, r := range []struct {
		name string
		kind ChecksumKind
		a, b []uint16
		want bool
	}{
		{"sha1 actual differ", ChecksumSHA1, lower40, snapshotUnits(strings.Repeat("e", 40)), false},
		{"sha1 actual case", ChecksumSHA1, lower40, upper40, false},
		{"sha1 actual equal", ChecksumSHA1, lower40, lower40, true},
		{"md5 actual differ", ChecksumMD5, lower32, snapshotUnits(strings.Repeat("f", 32)), false},
		{"md5 actual case", ChecksumMD5, lower32, upper32, false},
		{"md5 actual equal", ChecksumMD5, lower32, lower32, true},
		{"sha256 actual differ", ChecksumSHA256, lower64, snapshotUnits(strings.Repeat("g", 64)), false},
		{"sha256 actual case", ChecksumSHA256, lower64, upper64, false},
		{"sha256 actual equal", ChecksumSHA256, lower64, lower64, true},
	} {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t, only(r.kind, r.a), only(r.kind, r.b))
			snapshotCheck(t, r.name, a, b, false, r.want)
		})
	}
}

// ---- GroupPerKindOriginal: independent original differences per kind ----

// TestIsIdenticalPerKindOriginal: every kind's original is an independent
// compared operand, including MD5 (inequality plus equal control per kind).
func TestIsIdenticalPerKindOriginal(t *testing.T) {
	only := func(k ChecksumKind, original []uint16) func(*FileInfoSnapshotInput) {
		return func(in *FileInfoSnapshotInput) {
			in.Checksums = []ChecksumEntryInput{{Kind: k, Original: original}}
		}
	}
	o40a, o40b := snapshotUnits(strings.Repeat("a", 40)), snapshotUnits(strings.Repeat("b", 40))
	o40A := snapshotUnits(strings.Repeat("A", 40))
	o32a, o32b := snapshotUnits(strings.Repeat("c", 32)), snapshotUnits(strings.Repeat("d", 32))
	o32C := snapshotUnits(strings.Repeat("C", 32))
	o64a, o64b := snapshotUnits(strings.Repeat("e", 64)), snapshotUnits(strings.Repeat("f", 64))
	o64E := snapshotUnits(strings.Repeat("E", 64))
	for _, r := range []struct {
		name string
		kind ChecksumKind
		a, b []uint16
		want bool
	}{
		{"sha1 original differ", ChecksumSHA1, o40a, o40b, false},
		{"sha1 original case", ChecksumSHA1, o40a, o40A, false},
		{"sha1 original equal", ChecksumSHA1, o40a, o40a, true},
		{"md5 original differ", ChecksumMD5, o32a, o32b, false},
		{"md5 original case", ChecksumMD5, o32a, o32C, false},
		{"md5 original equal", ChecksumMD5, o32a, o32a, true},
		{"sha256 original differ", ChecksumSHA256, o64a, o64b, false},
		{"sha256 original case", ChecksumSHA256, o64a, o64E, false},
		{"sha256 original equal", ChecksumSHA256, o64a, o64a, true},
	} {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t, only(r.kind, r.a), only(r.kind, r.b))
			snapshotCheck(t, r.name, a, b, false, r.want)
		})
	}
}

// ---- GroupSHA256Original (CK-13): SHA-256 original is a compared operand ----

func TestIsIdenticalSHA256Original(t *testing.T) {
	rows := []struct {
		name string
		a, b []uint16
		want bool
	}{
		{"CK-8 control NO_ORIG==NO_ORIG", snapshotUnits("NO_ORIG"), snapshotUnits("NO_ORIG"), true},
		{"CK-13 null vs empty", nil, []uint16{}, false},
		{"CK-13 NO_ORIG vs distinct literal", snapshotUnits("NO_ORIG"), snapshotHexUnits(64), false},
		{"CK-13 null vs NO_ORIG", nil, snapshotUnits("NO_ORIG"), false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t,
				func(in *FileInfoSnapshotInput) { in.Checksums[1].Original = r.a },
				func(in *FileInfoSnapshotInput) { in.Checksums[1].Original = r.b })
			snapshotCheck(t, r.name, a, b, false, r.want)
		})
	}
}

// ---- GroupKeyedPresence (CK-11, CK-12, CK-14): keyed by kind, not order ----

func TestIsIdenticalKeyedPresence(t *testing.T) {
	sha1 := func(actual []uint16) []ChecksumEntryInput {
		return []ChecksumEntryInput{{Kind: ChecksumSHA1, Actual: actual}}
	}
	t.Run("CK-11 input order irrelevant", func(t *testing.T) {
		a, b := snapshotPair(t, nil, nil) // base order sha1,sha256 both sides
		snapshotCheck(t, "same order", a, b, false, true)
		in := snapshotBaseInput()
		in.Checksums = []ChecksumEntryInput{in.Checksums[1], in.Checksums[0]}
		swapped := snapshotMust(t, in)
		snapshotCheck(t, "swapped order", a, swapped, false, true)
	})
	t.Run("CK-12 extra kind one side", func(t *testing.T) {
		a, b := snapshotPair(t,
			func(in *FileInfoSnapshotInput) { in.Checksums = in.Checksums[:1] },
			nil)
		snapshotCheck(t, "CK-12", a, b, false, false)
	})
	t.Run("CK-14 same count same shapes different kinds", func(t *testing.T) {
		a, b := snapshotPair(t,
			func(in *FileInfoSnapshotInput) { in.Checksums = sha1(snapshotHexUnits(40)) },
			func(in *FileInfoSnapshotInput) {
				in.Checksums = []ChecksumEntryInput{{Kind: ChecksumMD5, Actual: snapshotHexUnits(40)}}
			})
		snapshotCheck(t, "CK-14 sha1-only vs md5-only", a, b, false, false)
	})
	t.Run("presence distinct from null actual", func(t *testing.T) {
		a, b := snapshotPair(t,
			func(in *FileInfoSnapshotInput) { in.Checksums = sha1(nil) },
			func(in *FileInfoSnapshotInput) { in.Checksums = nil })
		snapshotCheck(t, "entry-with-null-actual vs no entry", a, b, false, false)
	})
}

// ---- GroupNamePath (PT-1..PT-5): independent name/path, verbatim capture ----

func TestIsIdenticalNamePathIndependent(t *testing.T) {
	rows := []struct {
		name  string
		pathA string
		pathB string
		nameA string
		nameB string
		want  bool
	}{
		{"PT-1 path differ name equal", "a/f", "a/g", "f", "f", false},
		{"PT-2 name differ path equal", "a/f", "a/f", "f", "g", false},
		{"PT-3 metadata suffix verbatim equal", "a/f:properties", "a/f:properties", "f", "f", true},
		{"PT-4 raw vs already-mapped path", "a/f:properties", "a/f", "f", "f", false},
		{"PT-5 path case sensitive", "A/F", "a/f", "f", "f", false},
		{"PT-5 trailing slash significant", "a/f/", "a/f", "f", "f", false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t,
				func(in *FileInfoSnapshotInput) {
					in.RepoPathUTF16 = snapshotUnits(r.pathA)
					in.NameUTF16 = snapshotUnits(r.nameA)
				},
				func(in *FileInfoSnapshotInput) {
					in.RepoPathUTF16 = snapshotUnits(r.pathB)
					in.NameUTF16 = snapshotUnits(r.nameB)
				})
			snapshotCheck(t, r.name, a, b, false, r.want)
			snapshotCheck(t, r.name+" (ignoreCreated)", a, b, true, r.want)
		})
	}
	t.Run("PT-5 repoKey differ", func(t *testing.T) {
		a, b := snapshotPair(t,
			func(in *FileInfoSnapshotInput) { in.RepoKeyUTF16 = snapshotUnits("repo") },
			func(in *FileInfoSnapshotInput) { in.RepoKeyUTF16 = snapshotUnits("repo2") })
		snapshotCheck(t, "PT-5 repoKey", a, b, false, false)
	})
}

// ---- GroupCreatedToggle (CR-1, CR-2): explicit ignoreCreated flag ----

func TestIsIdenticalCreatedToggle(t *testing.T) {
	a, b := snapshotPair(t,
		func(in *FileInfoSnapshotInput) { in.CreatedMillis = 1 },
		func(in *FileInfoSnapshotInput) { in.CreatedMillis = 2 })
	snapshotCheck(t, "CR-1 compare created", a, b, false, false)
	snapshotCheck(t, "CR-2 skip created", a, b, true, true)
	c, d := snapshotPair(t,
		func(in *FileInfoSnapshotInput) { in.CreatedMillis = math.MinInt64 },
		func(in *FileInfoSnapshotInput) { in.CreatedMillis = math.MinInt64 })
	snapshotCheck(t, "equal created control", c, d, false, true)
}

// TestIsIdenticalIgnoreCreatedOnlySkipsCreated: ignoreCreated=true must NOT
// mask any other differing operand.
func TestIsIdenticalIgnoreCreatedOnlySkipsCreated(t *testing.T) {
	rows := []struct {
		name string
		mutB func(*FileInfoSnapshotInput)
	}{
		{"lastModified differ", func(in *FileInfoSnapshotInput) { in.LastModifiedMillis++ }},
		{"lastUpdated differ", func(in *FileInfoSnapshotInput) { in.LastUpdatedMillis++ }},
		{"repoKey differ", func(in *FileInfoSnapshotInput) { in.RepoKeyUTF16 = snapshotUnits("repo2") }},
		{"path differ", func(in *FileInfoSnapshotInput) { in.RepoPathUTF16 = snapshotUnits("a/g") }},
		{"name differ", func(in *FileInfoSnapshotInput) { in.NameUTF16 = snapshotUnits("g") }},
		{"createdBy differ", func(in *FileInfoSnapshotInput) { in.CreatedByUTF16 = snapshotUnits("carol") }},
		{"modifiedBy differ", func(in *FileInfoSnapshotInput) { in.ModifiedByUTF16 = snapshotUnits("carol") }},
		{"size differ", func(in *FileInfoSnapshotInput) { in.SizeBytes++ }},
		{"mime differ", func(in *FileInfoSnapshotInput) { in.MimeTypeUTF16 = snapshotUnits("text/plain") }},
		{"sha1 actual differ", func(in *FileInfoSnapshotInput) { in.Checksums[0].Actual = snapshotHexUnits(39) }},
		{"sha1 original differ", func(in *FileInfoSnapshotInput) { in.Checksums[0].Original = snapshotUnits("NO_ORIG") }},
		{"sha256 original differ", func(in *FileInfoSnapshotInput) { in.Checksums[1].Original = nil }},
		{"extra checksum kind", func(in *FileInfoSnapshotInput) {
			in.Checksums = append(in.Checksums, ChecksumEntryInput{Kind: ChecksumMD5, Actual: snapshotHexUnits(32)})
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t, nil, r.mutB)
			snapshotCheck(t, r.name, a, b, true, false)
		})
	}
}

// ---- GroupInt64 (TS-1..TS-3): signed int64 operands, zero is plain ----

func TestIsIdenticalSignedInt64(t *testing.T) {
	rows := []struct {
		name string
		mutA func(*FileInfoSnapshotInput)
		mutB func(*FileInfoSnapshotInput)
		ig   bool
		want bool
	}{
		{"TS-1 lastUpdated differ", func(in *FileInfoSnapshotInput) { in.LastUpdatedMillis = 1 },
			func(in *FileInfoSnapshotInput) { in.LastUpdatedMillis = 2 }, false, false},
		{"TS-2 lastModified differ", func(in *FileInfoSnapshotInput) { in.LastModifiedMillis = -5 },
			func(in *FileInfoSnapshotInput) { in.LastModifiedMillis = 0 }, false, false},
		{"TS-2 size differ", func(in *FileInfoSnapshotInput) { in.SizeBytes = 0 },
			func(in *FileInfoSnapshotInput) { in.SizeBytes = 1 }, false, false},
		{"TS-3 created min vs max compared", func(in *FileInfoSnapshotInput) { in.CreatedMillis = math.MinInt64 },
			func(in *FileInfoSnapshotInput) { in.CreatedMillis = math.MaxInt64 }, false, false},
		{"TS-3 created min vs max ignored", func(in *FileInfoSnapshotInput) { in.CreatedMillis = math.MinInt64 },
			func(in *FileInfoSnapshotInput) { in.CreatedMillis = math.MaxInt64 }, true, true},
		{"TS-3 lastModified min==min", func(in *FileInfoSnapshotInput) { in.LastModifiedMillis = math.MinInt64 },
			func(in *FileInfoSnapshotInput) { in.LastModifiedMillis = math.MinInt64 }, false, true},
		{"TS-3 lastUpdated max==max", func(in *FileInfoSnapshotInput) { in.LastUpdatedMillis = math.MaxInt64 },
			func(in *FileInfoSnapshotInput) { in.LastUpdatedMillis = math.MaxInt64 }, false, true},
		{"TS-3 size min vs max", func(in *FileInfoSnapshotInput) { in.SizeBytes = math.MinInt64 },
			func(in *FileInfoSnapshotInput) { in.SizeBytes = math.MaxInt64 }, false, false},
		{"TS-2 zero size==zero", func(in *FileInfoSnapshotInput) { in.SizeBytes = 0 },
			func(in *FileInfoSnapshotInput) { in.SizeBytes = 0 }, false, true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a, b := snapshotPair(t, r.mutA, r.mutB)
			snapshotCheck(t, r.name, a, b, r.ig, r.want)
		})
	}
}

// ---- GroupControl (AL-4, full-equal): green-by-design controls ----

func TestIsIdenticalFullEqualControls(t *testing.T) {
	s := snapshotMust(t, snapshotBaseInput())
	t2 := snapshotMust(t, snapshotBaseInput())
	snapshotCheck(t, "fresh equal inputs", s, t2, false, true)
	snapshotCheck(t, "fresh equal inputs ignoreCreated", s, t2, true, true)
	copyVal := s // value copy shares immutable storage (proposal)
	snapshotCheck(t, "value copy", s, copyVal, false, true)
}

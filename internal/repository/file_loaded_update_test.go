package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
)

func loadedUpdateUnits(s string) []uint16 { return utf16.Encode([]rune(s)) }

// 独立显式期望，不用 loader/materializer 计算被测基线。
// Expectations are explicit, independent of the loader/materializer.
func loadedUpdateFixture(t *testing.T) (FileSnapshotSource, FileInfoSnapshotInput, FileMimeCatalog) {
	t.Helper()
	p, err := NewPath("rk", "dir", "file.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	m := NewFileMetadata(FileMetadataInput{CreatedMillis: 11, ModifiedMillis: 22, UpdatedMillis: 33,
		CreatedBy: NewActor(loadedUpdateUnits("creator")), ModifiedBy: NewActor(loadedUpdateUnits("modifier")),
		SHA1Original: loadedUpdateUnits(strings.Repeat("A", 40))})
	s := FileSnapshotSource{Reference: FileReference{ID: 37, Path: p, Metadata: &m,
		Content: content.Metadata{Size: 7, SHA1: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64)}},
		SHA1ActualValid: true, SHA256ActualValid: true}
	f := FileInfoSnapshotInput{RepoKeyUTF16: loadedUpdateUnits("rk"), RepoPathUTF16: loadedUpdateUnits("dir/file.txt"),
		NameUTF16: loadedUpdateUnits("file.txt"), CreatedMillis: 11, LastModifiedMillis: 22, LastUpdatedMillis: 33,
		CreatedByUTF16: loadedUpdateUnits("creator"), ModifiedByUTF16: loadedUpdateUnits("modifier"), SizeBytes: 7,
		MimeTypeUTF16: loadedUpdateUnits("text/plain"), Checksums: []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: loadedUpdateUnits(strings.Repeat("a", 40)), Original: loadedUpdateUnits(strings.Repeat("a", 40))},
			{Kind: ChecksumMD5}, {Kind: ChecksumSHA256, Actual: loadedUpdateUnits(strings.Repeat("b", 64)), Original: loadedUpdateUnits("NO_ORIG")}}}
	c, err := NewFileMimeCatalog([]FileMimeDefinition{{Type: "text/plain", Extensions: "txt"}}, FileMimeCaseRootASCII)
	if err != nil {
		t.Fatal(err)
	}
	return s, f, c
}

func loadedUpdateSnapshot(t *testing.T, f FileInfoSnapshotInput) FileInfoSnapshot {
	t.Helper()
	s, err := NewFileInfoSnapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUpdateLoadedFileDerivedBaselineAndRawDifferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*FileSnapshotSource, *FileInfoSnapshotInput)
	}{
		{"no-op", func(*FileSnapshotSource, *FileInfoSnapshotInput) {}},
		{"MIME null", func(_ *FileSnapshotSource, f *FileInfoSnapshotInput) { f.MimeTypeUTF16 = nil }},
		{"MIME empty", func(_ *FileSnapshotSource, f *FileInfoSnapshotInput) { f.MimeTypeUTF16 = []uint16{} }},
		{"raw Original case", func(_ *FileSnapshotSource, f *FileInfoSnapshotInput) {
			f.Checksums[0].Original = loadedUpdateUnits(strings.Repeat("A", 40))
		}},
		{"Original null-empty", func(_ *FileSnapshotSource, f *FileInfoSnapshotInput) { f.Checksums[1].Original = []uint16{} }},
		{"created only", func(_ *FileSnapshotSource, f *FileInfoSnapshotInput) { f.CreatedMillis = 12 }},
		{"modified zero", func(_ *FileSnapshotSource, f *FileInfoSnapshotInput) { f.LastModifiedMillis = 0 }},
		{"Actual null-empty", func(_ *FileSnapshotSource, f *FileInfoSnapshotInput) { f.Checksums[1].Actual = []uint16{} }},
		{"raw actor before abbreviation", func(s *FileSnapshotSource, f *FileInfoSnapshotInput) {
			m := *s.Reference.Metadata
			m.createdBy = NewActor(loadedUpdateUnits(strings.Repeat("a", 70)))
			s.Reference.Metadata = &m
			f.CreatedByUTF16 = loadedUpdateUnits(strings.Repeat("a", 70))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, c := loadedUpdateFixture(t)
			tc.edit(&s, &f)
			desired := loadedUpdateSnapshot(t, f)
			calls := 0
			result, err := UpdateLoadedFileIfChanged(context.Background(), LoadedFileUpdateInput{
				Source: s, Desired: desired, CreatedPolicy: PreserveLoadedFileCreated}, c,
				func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					if ref.ID != 37 || ref.Path.Repo() != "rk" || ref.Path.PathName() != "dir/file.txt" ||
						ref.Content.Size != 7 || ref.Content.SHA1 != strings.Repeat("a", 40) || ref.Content.MD5 != "" {
						t.Error("write operands must come from Desired and the ID from Source")
					}
					if ref.Metadata == nil || ref.Metadata.CreatedMillis() != f.CreatedMillis || ref.Metadata.ModifiedMillis() != f.LastModifiedMillis ||
						!reflect.DeepEqual(ref.Metadata.SHA1OriginalUTF16(), f.Checksums[0].Original) ||
						!reflect.DeepEqual(ref.Metadata.MD5OriginalUTF16(), f.Checksums[1].Original) {
						t.Error("Desired timestamps and raw Originals changed during mapping")
					}
					if tc.name == "raw actor before abbreviation" && !reflect.DeepEqual(ref.Metadata.CreatedBy().UTF16(), loadedUpdateUnits(strings.Repeat("a", 61)+"...")) {
						t.Error("expected existing actor projection after dirty comparison")
					}
					return 1, nil
				})
			want := FileDirtyUpdateResult{Changed: true, Count: 1}
			wantCalls := 1
			if tc.name == "no-op" {
				want, wantCalls = FileDirtyUpdateResult{}, 0
			}
			if err != nil || result != want || calls != wantCalls {
				t.Fatalf("result=%v err=%v calls=%d; want %v/%d", result, err, calls, want, wantCalls)
			}
			if !reflect.DeepEqual(desired.Fields(), f) {
				t.Error("Desired was normalized or mutated")
			}
		})
	}
}

func TestUpdateLoadedFileNonpositiveTimesAndDotlessBypass(t *testing.T) {
	s, f, _ := loadedUpdateFixture(t)
	p, err := NewPath("rk", "dir", "README", true)
	if err != nil {
		t.Fatal(err)
	}
	s.Reference.Path = p
	m := *s.Reference.Metadata
	m.createdMillis, m.modifiedMillis = -7, -9
	s.Reference.Metadata = &m
	f.RepoPathUTF16, f.NameUTF16 = loadedUpdateUnits("dir/README"), loadedUpdateUnits("README")
	f.CreatedMillis, f.LastModifiedMillis = -7, -7
	f.MimeTypeUTF16 = loadedUpdateUnits("application/octet-stream")
	for _, dirty := range []bool{false, true} {
		f.LastModifiedMillis = -7
		if dirty {
			f.LastModifiedMillis = -8
		}
		calls := 0
		got, err := UpdateLoadedFileIfChanged(context.Background(), LoadedFileUpdateInput{
			Source: s, Desired: loadedUpdateSnapshot(t, f), CreatedPolicy: PreserveLoadedFileCreated}, FileMimeCatalog{},
			func(_ context.Context, ref FileReference) (int64, error) {
				calls++
				if ref.Metadata.ModifiedMillis() != -8 || ref.Metadata.CreatedMillis() != -7 {
					t.Error("negative Desired times must remain verbatim")
				}
				return 1, nil
			})
		if err != nil || got.Changed != dirty || calls != map[bool]int{false: 0, true: 1}[dirty] {
			t.Fatalf("dirty=%v result=%v err=%v calls=%d", dirty, got, err, calls)
		}
	}
}

func TestUpdateLoadedFileFailurePrecedence(t *testing.T) {
	for _, name := range []string{"cancel", "nil delegate", "bad ID", "zero policy", "unknown policy", "missing metadata", "Actual contradiction", "unsupported Original", "bad catalog", "MIME before path", "nonASCII MIME", "path before skip", "exposed leading space", "exposed trailing space", "invalid Desired", "unrepresentable Desired", "invalid Desired path"} {
		t.Run(name, func(t *testing.T) {
			s, f, c := loadedUpdateFixture(t)
			ctx := context.Background()
			policy := PreserveLoadedFileCreated
			want := error(ErrInvalidFileSnapshotSource)
			calls := 0
			update := func(context.Context, FileReference) (int64, error) { calls++; return 1, nil }
			switch name {
			case "cancel":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, update, s.Reference.ID, policy = cancelled, nil, 0, 0
				want = context.Canceled
			case "nil delegate":
				update, s.Reference.ID, policy = nil, 0, 0
				want = errNilDirtyUpdateDependency
			case "bad ID":
				s.Reference.ID, policy, s.Reference.Metadata = 0, 0, nil
				want = ErrInvalidUpdateID
			case "zero policy", "unknown policy":
				policy, s.Reference.Metadata = 0, nil
				if name == "unknown policy" {
					policy = 255
				}
				want = ErrUnsupportedLoadedFileCreatedPolicy
			case "missing metadata":
				s.Reference.Metadata = nil
			case "Actual contradiction":
				s.SHA1ActualValid = false
			case "unsupported Original":
				m := *s.Reference.Metadata
				m.sha1Original = loadedUpdateUnits(strings.Repeat("I", 40))
				s.Reference.Metadata = &m
				want = ErrUnsupportedLoadedOriginal
			case "bad catalog":
				c, want = FileMimeCatalog{}, ErrInvalidFileMimeCatalog
			case "exposed leading space", "exposed trailing space":
				// 去掉边界斜杠后暴露的空白不可再 trim 后验证。
				// Slash removal exposes whitespace: never trim it again for validation.
				leaf, raw := "/ a/", " a"
				if name == "exposed trailing space" {
					leaf, raw = "/a /", "a "
				}
				p, err := NewPath("rk", "", leaf, true)
				if err != nil {
					t.Fatal(err)
				}
				s.Reference.Path = p
				f.RepoPathUTF16, f.NameUTF16 = loadedUpdateUnits(raw), loadedUpdateUnits(raw)
				f.MimeTypeUTF16 = loadedUpdateUnits("application/octet-stream")
				c, want = FileMimeCatalog{}, ErrInvalidStoragePath
			case "MIME before path", "path before skip", "nonASCII MIME":
				leaf := "file?.txt"
				if name == "nonASCII MIME" {
					leaf = "file.é"
				}
				p, err := NewPath("rk", "dir", leaf, true)
				if err != nil {
					t.Fatal(err)
				}
				s.Reference.Path = p
				f.RepoPathUTF16, f.NameUTF16 = loadedUpdateUnits("dir/"+leaf), loadedUpdateUnits(leaf)
				want = ErrInvalidStoragePath
				if name == "MIME before path" {
					c, err = NewFileMimeCatalog([]FileMimeDefinition{{Type: "a", Extensions: "txt"}, {Type: "b", Extensions: "txt"}}, FileMimeCaseRootASCII)
					if err != nil {
						t.Fatal(err)
					}
					want = ErrAmbiguousFileMimeExtension
				}
				if name == "nonASCII MIME" {
					want = ErrUnsupportedFileMimeExtension
				}
			case "invalid Desired":
				want = ErrInvalidFileInfoSnapshot
			case "unrepresentable Desired":
				f.RepoKeyUTF16, want = []uint16{0xd800}, ErrUnrepresentableFileSnapshot
			case "invalid Desired path":
				f.RepoPathUTF16, want = loadedUpdateUnits("bad?"), ErrInvalidStoragePath
			}
			desired := loadedUpdateSnapshot(t, f)
			if name == "invalid Desired" {
				desired = FileInfoSnapshot{}
			}
			if name == "missing metadata" {
				desired = FileInfoSnapshot{}
				c = FileMimeCatalog{}
			}
			got, err := UpdateLoadedFileIfChanged(ctx, LoadedFileUpdateInput{Source: s, Desired: desired, CreatedPolicy: policy}, c, update)
			if got != (FileDirtyUpdateResult{}) || !errors.Is(err, want) || calls != 0 {
				t.Fatalf("result=%v err=%v want=%v calls=%d", got, err, want, calls)
			}
		})
	}
}

type loadedUpdateSecondCancel struct {
	context.Context
	checks int
}

func (c *loadedUpdateSecondCancel) Err() error {
	c.checks++
	if c.checks >= 2 {
		return context.Canceled
	}
	return nil
}

// 装配后的旧gate也必须检查取消，不能以no-op提前绕过。
// The existing gate must recheck cancellation even for an assembled no-op.
func TestUpdateLoadedFileCancellationBeforeEqualitySkip(t *testing.T) {
	s, f, c := loadedUpdateFixture(t)
	ctx := &loadedUpdateSecondCancel{Context: context.Background()}
	calls := 0
	got, err := UpdateLoadedFileIfChanged(ctx, LoadedFileUpdateInput{Source: s,
		Desired: loadedUpdateSnapshot(t, f), CreatedPolicy: PreserveLoadedFileCreated}, c,
		func(context.Context, FileReference) (int64, error) { calls++; return 1, nil })
	if got != (FileDirtyUpdateResult{}) || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
	}
}

func TestUpdateLoadedFileCountErrorAndReadOwnership(t *testing.T) {
	s, f, c := loadedUpdateFixture(t)
	f.CreatedMillis++
	desired := loadedUpdateSnapshot(t, f)
	beforeSource, beforeDesired := *s.Reference.Metadata, desired.Fields()
	marker := errors.New("delegate marker")
	for _, fail := range []bool{false, true} {
		calls := 0
		got, err := UpdateLoadedFileIfChanged(context.Background(), LoadedFileUpdateInput{Source: s, Desired: desired, CreatedPolicy: PreserveLoadedFileCreated}, c,
			func(_ context.Context, ref FileReference) (int64, error) {
				calls++
				// 委托只能影响自己的投影，不能改Source或Desired。
				// The delegate may mutate its projection, never the source state.
				*ref.Metadata = FileMetadata{}
				if fail {
					return 9, marker
				}
				return 0, nil
			})
		if calls != 1 || (!fail && (err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 0}))) ||
			(fail && (!errors.Is(err, marker) || got != (FileDirtyUpdateResult{}))) {
			t.Fatalf("fail=%v result=%v err=%v calls=%d", fail, got, err, calls)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				got, err := UpdateLoadedFileIfChanged(context.Background(), LoadedFileUpdateInput{Source: s, Desired: desired, CreatedPolicy: PreserveLoadedFileCreated}, c,
					func(context.Context, FileReference) (int64, error) { return 1, nil })
				if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
					t.Errorf("concurrent result=%v err=%v", got, err)
				}
				fields := desired.Fields()
				fields.MimeTypeUTF16[0] = 'X'
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(*s.Reference.Metadata, beforeSource) || !reflect.DeepEqual(desired.Fields(), beforeDesired) {
		t.Fatal("source or Desired changed through delegation/read copies")
	}
}

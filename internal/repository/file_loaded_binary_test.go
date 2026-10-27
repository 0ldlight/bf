package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

func loadedBinaryA() content.Metadata {
	return content.Metadata{SHA1: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64), MD5: strings.Repeat("e", 32), Size: 7}
}

func loadedBinaryB() content.Metadata {
	return content.Metadata{SHA1: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64), MD5: strings.Repeat("f", 32), Size: 19}
}

// 复用已冻结Source输入；期望Published与元数据独立写明，不用新producer求值。
// Reuse the frozen source fixture, not the producer to calculate expectations.
func loadedBinaryFixture(t *testing.T) (FileSnapshotSource, FileMimeCatalog) {
	t.Helper()
	s, _, c := loadedUpdateFixture(t)
	s.Reference.Content, s.MD5ActualValid = loadedBinaryA(), true
	return s, c
}

func TestUpdateLoadedFileBinaryReplacementPreservesOriginalAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		original []uint16
		want     []uint16
	}{
		{"hex already normalized by load", loadedUpdateUnits(strings.Repeat("A", 40)), loadedUpdateUnits(strings.Repeat("a", 40))},
		{"trusted marker", loadedUpdateUnits("NO_ORIG"), loadedUpdateUnits("NO_ORIG")},
		{"null", nil, nil},
		{"empty", []uint16{}, []uint16{}},
		{"wrong length raw", []uint16{'M', 0xd800, 'X'}, []uint16{'M', 0xd800, 'X'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			m := *s.Reference.Metadata
			m.sha1Original, m.md5Original = tc.original, []uint16{}
			s.Reference.Metadata, s.Reference.ID = &m, 93
			before := *s.Reference.Metadata
			wantMetadata := NewFileMetadata(FileMetadataInput{CreatedMillis: 11, ModifiedMillis: 22, UpdatedMillis: 33,
				CreatedBy: NewActor(loadedUpdateUnits("creator")), ModifiedBy: NewActor(loadedUpdateUnits("modifier")),
				SHA1Original: tc.want, MD5Original: []uint16{}})
			published := loadedBinaryB()
			calls := 0
			got, err := UpdateLoadedFileBinary(context.Background(), LoadedFileBinaryInput{
				Source: s, Published: published, CreatedPolicy: PreserveLoadedFileCreated}, c,
				func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					if ref.ID != 93 || ref.Path.Repo() != "rk" || ref.Path.PathName() != "dir/file.txt" ||
						ref.Content != (content.Metadata{SHA1: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64), MD5: strings.Repeat("f", 32), Size: 19}) ||
						ref.Metadata == nil || !reflect.DeepEqual(*ref.Metadata, wantMetadata) {
						t.Error("only published actuals/size may change; source ID, identity and seven metadata fields must survive")
					}
					return 1, nil
				})
			if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) || calls != 1 {
				t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
			}
			if !reflect.DeepEqual(*s.Reference.Metadata, before) || published != loadedBinaryB() || s.Reference.Content != loadedBinaryA() {
				t.Error("input metadata, Published or source content mutated")
			}
		})
	}
}

func TestUpdateLoadedFileBinarySameBinarySkipsAndOldActualCanBeRepaired(t *testing.T) {
	for _, state := range []string{"same", "upper", "null", "empty", "malformed"} {
		t.Run(state, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			published := loadedBinaryB()
			switch state {
			case "same":
				published = loadedBinaryA()
			case "upper":
				s.Reference.Content.SHA1, s.Reference.Content.SHA256, s.Reference.Content.MD5 = strings.Repeat("A", 40), strings.Repeat("B", 64), strings.Repeat("E", 32)
			case "null":
				s.Reference.Content.SHA1, s.Reference.Content.SHA256, s.Reference.Content.MD5 = "", "", ""
				s.SHA1ActualValid, s.SHA256ActualValid, s.MD5ActualValid = false, false, false
			case "empty":
				s.Reference.Content.SHA1, s.Reference.Content.SHA256, s.Reference.Content.MD5 = "", "", ""
			case "malformed":
				s.Reference.Content.SHA1, s.Reference.Content.SHA256, s.Reference.Content.MD5 = "bad", "?", "wrong"
			}
			calls := 0
			got, err := UpdateLoadedFileBinary(context.Background(), LoadedFileBinaryInput{
				Source: s, Published: published, CreatedPolicy: PreserveLoadedFileCreated}, c,
				func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					if ref.Content != loadedBinaryB() {
						t.Error("must replace every old Actual without revalidation")
					}
					return 1, nil
				})
			want, wantCalls := (FileDirtyUpdateResult{Changed: true, Count: 1}), 1
			if state == "same" {
				want, wantCalls = FileDirtyUpdateResult{}, 0
			}
			if err != nil || got != want || calls != wantCalls {
				t.Fatalf("result=%v err=%v calls=%d want=%v/%d", got, err, calls, want, wantCalls)
			}
		})
	}
}

func TestUpdateLoadedFileBinaryPublishedGuardsBeforeSkip(t *testing.T) {
	for _, name := range []string{"zero", "SHA1 uppercase equal source", "SHA1 short", "SHA1 nonhex", "SHA256 uppercase", "SHA256 short", "SHA256 nonhex", "MD5 uppercase", "MD5 short", "MD5 nonhex", "negative size"} {
		t.Run(name, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			p := loadedBinaryA()
			switch name {
			case "zero":
				p = content.Metadata{}
			case "SHA1 uppercase equal source":
				p.SHA1 = strings.Repeat("A", 40)
				s.Reference.Content.SHA1 = p.SHA1
			case "SHA1 short":
				p.SHA1 = "a"
			case "SHA1 nonhex":
				p.SHA1 = strings.Repeat("z", 40)
			case "SHA256 uppercase":
				p.SHA256 = strings.Repeat("B", 64)
			case "SHA256 short":
				p.SHA256 = "b"
			case "SHA256 nonhex":
				p.SHA256 = strings.Repeat("g", 64)
			case "MD5 uppercase":
				p.MD5 = strings.Repeat("E", 32)
			case "MD5 short":
				p.MD5 = "e"
			case "MD5 nonhex":
				p.MD5 = strings.Repeat("x", 32)
			case "negative size":
				p.Size = -1
			}
			calls := 0
			got, err := UpdateLoadedFileBinary(context.Background(), LoadedFileBinaryInput{
				Source: s, Published: p, CreatedPolicy: PreserveLoadedFileCreated}, c,
				func(context.Context, FileReference) (int64, error) { calls++; return 1, nil })
			if got != (FileDirtyUpdateResult{}) || !errors.Is(err, ErrInvalidPublishedFileMetadata) || calls != 0 {
				t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestUpdateLoadedFileBinaryPreparationPrecedesPublished(t *testing.T) {
	for _, name := range []string{"cancel", "nil delegate", "ID", "policy zero", "policy unknown", "metadata", "Actual contradiction", "loaded Original", "catalog", "MIME ambiguity", "MIME nonASCII", "source path"} {
		t.Run(name, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			ctx := context.Background()
			policy := PreserveLoadedFileCreated
			calls := 0
			update := func(context.Context, FileReference) (int64, error) { calls++; return 1, nil }
			want := error(ErrInvalidFileSnapshotSource)
			switch name {
			case "cancel":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, update, s.Reference.ID, policy = cancelled, nil, 0, 0
				want = context.Canceled
			case "nil delegate":
				update, s.Reference.ID, policy = nil, 0, 0
				want = errNilDirtyUpdateDependency
			case "ID":
				s.Reference.ID, policy = 0, 0
				want = ErrInvalidUpdateID
			case "policy zero", "policy unknown":
				policy, s.Reference.Metadata = 0, nil
				if name == "policy unknown" {
					policy = 255
				}
				want = ErrUnsupportedLoadedFileCreatedPolicy
			case "metadata":
				s.Reference.Metadata = nil
			case "Actual contradiction":
				s.SHA1ActualValid = false
			case "loaded Original":
				m := *s.Reference.Metadata
				m.sha1Original = loadedUpdateUnits(strings.Repeat("I", 40))
				s.Reference.Metadata = &m
				want = ErrUnsupportedLoadedOriginal
			case "catalog":
				c, want = FileMimeCatalog{}, ErrInvalidFileMimeCatalog
			case "MIME ambiguity":
				var err error
				c, err = NewFileMimeCatalog([]FileMimeDefinition{{Type: "a", Extensions: "txt"}, {Type: "b", Extensions: "txt"}}, FileMimeCaseRootASCII)
				if err != nil {
					t.Fatal(err)
				}
				want = ErrAmbiguousFileMimeExtension
			case "MIME nonASCII", "source path":
				leaf := "file?.txt"
				if name == "MIME nonASCII" {
					leaf = "file.é"
				}
				p, err := NewPath("rk", "dir", leaf, true)
				if err != nil {
					t.Fatal(err)
				}
				s.Reference.Path = p
				want = ErrInvalidStoragePath
				if name == "MIME nonASCII" {
					want = ErrUnsupportedFileMimeExtension
				}
			}
			got, err := UpdateLoadedFileBinary(ctx, LoadedFileBinaryInput{Source: s, Published: content.Metadata{}, CreatedPolicy: policy}, c, update)
			if got != (FileDirtyUpdateResult{}) || !errors.Is(err, want) || calls != 0 {
				t.Fatalf("result=%v err=%v want=%v calls=%d", got, err, want, calls)
			}
		})
	}
}

func TestUpdateLoadedFileBinaryDotlessZeroSizeAndDelegateResults(t *testing.T) {
	s, c := loadedBinaryFixture(t)
	p, err := NewPath("rk", "dir", "README", true)
	if err != nil {
		t.Fatal(err)
	}
	s.Reference.Path = p
	published := loadedBinaryB()
	published.Size = 0
	marker := errors.New("binary update marker")
	for _, fail := range []bool{false, true} {
		calls := 0
		got, err := UpdateLoadedFileBinary(context.Background(), LoadedFileBinaryInput{Source: s, Published: published, CreatedPolicy: PreserveLoadedFileCreated}, FileMimeCatalog{},
			func(_ context.Context, ref FileReference) (int64, error) {
				calls++
				if ref.Content.Size != 0 {
					t.Error("zero size is supported")
				}
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
	// 保持同一目录可读复用，非并发写/锁保证。
	// Exercise shared read ownership, not concurrent writer isolation.
	p, err = NewPath("rk", "dir", "file.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	s.Reference.Path = p
	before := *s.Reference.Metadata
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				got, err := UpdateLoadedFileBinary(context.Background(), LoadedFileBinaryInput{Source: s, Published: published, CreatedPolicy: PreserveLoadedFileCreated}, c,
					func(_ context.Context, ref FileReference) (int64, error) {
						*ref.Metadata = FileMetadata{}
						return 1, nil
					})
				if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
					t.Errorf("result=%v err=%v", got, err)
				}
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(*s.Reference.Metadata, before) || s.Reference.Content != loadedBinaryA() || published.Size != 0 {
		t.Fatal("projected callback mutated source or Published input")
	}
}

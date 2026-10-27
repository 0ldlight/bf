package repository

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/content"
)

func noClientInput(s FileSnapshotSource) LocalFileNoClientInput {
	return LocalFileNoClientInput{Source: s, Published: loadedBinaryB(), ResourceModifiedMillis: 44,
		UpdatedMillis: 55, CurrentUserUTF16: loadedUpdateUnits("uploader"),
		CreatedPolicy: PreserveLoadedFileCreated, ChecksumPolicy: LocalFileChecksumClient}
}

// 仅复用已有Source fixture；字段期望独立写明，不调用producer计算。
// Only the source fixture is shared; expectations do not call the producer.
func TestLocalFileNoClientFieldsAndPolicy(t *testing.T) {
	for _, policy := range []LocalFileChecksumPolicy{LocalFileChecksumClient, LocalFileChecksumServer} {
		t.Run(map[LocalFileChecksumPolicy]string{LocalFileChecksumClient: "client", LocalFileChecksumServer: "server"}[policy], func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			m := *s.Reference.Metadata
			m.sha1Original, m.md5Original = loadedUpdateUnits("NO_ORIG"), []uint16{}
			s.Reference.Metadata, s.Reference.ID = &m, 81
			in := noClientInput(s)
			in.ChecksumPolicy = policy
			calls := 0
			got, err := UpdateLocalFileWithoutClientChecksums(context.Background(), in, c,
				func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					want := NewFileMetadata(FileMetadataInput{CreatedMillis: 11, ModifiedMillis: 44, UpdatedMillis: 55,
						CreatedBy: NewActor(loadedUpdateUnits("creator")), ModifiedBy: NewActor(loadedUpdateUnits("uploader"))})
					if ref.ID != 81 || ref.Path.Repo() != "rk" || ref.Path.PathName() != "dir/file.txt" ||
						ref.Content != (content.Metadata{SHA1: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64), MD5: strings.Repeat("f", 32), Size: 19}) ||
						ref.Metadata == nil || !reflect.DeepEqual(*ref.Metadata, want) {
						t.Error("want Source ID/identity/created/by, new binary, stamps and null Originals")
					}
					return 1, nil
				})
			if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) || calls != 1 {
				t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
	if LocalFileChecksumClient != 1 || LocalFileChecksumServer != 2 {
		t.Fatal("policy values must be explicit 1/2")
	}
}

func TestLocalFileNoClientSameWinnerStillDirtyWithFreshSource(t *testing.T) {
	// fresh Source的SHA256 Original每次均由loader生产NO_ORIG，Desired清为nil。
	// Each fresh load restores NO_ORIG; this is not a same-binary no-op profile.
	for i := 0; i < 3; i++ {
		s, c := loadedBinaryFixture(t)
		m := *s.Reference.Metadata
		m.sha1Original, m.md5Original = nil, nil
		s.Reference.Metadata = &m
		in := noClientInput(s)
		in.Published, in.ResourceModifiedMillis, in.UpdatedMillis = loadedBinaryA(), 22, 33
		in.CurrentUserUTF16 = loadedUpdateUnits("modifier")
		calls := 0
		got, err := UpdateLocalFileWithoutClientChecksums(context.Background(), in, c,
			func(_ context.Context, ref FileReference) (int64, error) {
				calls++
				if ref.Content != loadedBinaryA() || ref.Metadata.SHA1OriginalUTF16() != nil || ref.Metadata.MD5OriginalUTF16() != nil {
					t.Error("same winner must still clear Originals")
				}
				return 1, nil
			})
		if err != nil || calls != 1 || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
			t.Fatalf("fresh iteration %d: result=%v err=%v calls=%d", i, got, err, calls)
		}
	}
}

func TestLocalFileNoClientOldActualValidation(t *testing.T) {
	for _, state := range []string{"uppercase", "null", "SHA1 empty", "SHA256 empty", "MD5 empty", "SHA1 bad", "SHA256 bad", "MD5 bad"} {
		t.Run(state, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			in := noClientInput(s)
			valid := state == "uppercase" || state == "null"
			switch state {
			case "uppercase":
				s.Reference.Content.SHA1, s.Reference.Content.SHA256, s.Reference.Content.MD5 = strings.Repeat("A", 40), strings.Repeat("B", 64), strings.Repeat("E", 32)
			case "null":
				s.Reference.Content.SHA1, s.Reference.Content.SHA256, s.Reference.Content.MD5 = "", "", ""
				s.SHA1ActualValid, s.SHA256ActualValid, s.MD5ActualValid = false, false, false
			case "SHA1 empty":
				s.Reference.Content.SHA1 = ""
			case "SHA256 empty":
				s.Reference.Content.SHA256 = ""
			case "MD5 empty":
				s.Reference.Content.MD5 = ""
			case "SHA1 bad":
				s.Reference.Content.SHA1 = strings.Repeat("z", 40)
			case "SHA256 bad":
				s.Reference.Content.SHA256 = strings.Repeat("x", 64)
			case "MD5 bad":
				s.Reference.Content.MD5 = "short"
			}
			in.Source = s
			if !valid {
				in.Published, in.ChecksumPolicy = content.Metadata{}, 0
			}
			calls := 0
			got, err := UpdateLocalFileWithoutClientChecksums(context.Background(), in, c,
				func(context.Context, FileReference) (int64, error) { calls++; return 1, nil })
			if valid {
				if err != nil || calls != 1 || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
					t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
				}
			} else if !errors.Is(err, ErrInvalidLoadedActual) || calls != 0 || got != (FileDirtyUpdateResult{}) {
				t.Fatalf("old Actual must fail before Published/policy: result=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestLocalFileNoClientPreparationPublishedPolicyPrecedence(t *testing.T) {
	for _, name := range []string{"cancel", "delegate", "ID", "created policy", "metadata", "Actual nullness", "Original", "catalog", "MIME", "path", "Published SHA1", "Published SHA256", "Published MD5", "Published size", "policy zero", "policy unknown"} {
		t.Run(name, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			in := noClientInput(s)
			ctx := context.Background()
			calls := 0
			update := func(context.Context, FileReference) (int64, error) { calls++; return 1, nil }
			want := error(ErrInvalidPublishedFileMetadata)
			in.Published, in.ChecksumPolicy = content.Metadata{}, 0
			switch name {
			case "cancel":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, update, in.Source.Reference.ID, in.CreatedPolicy = cancelled, nil, 0, 0
				want = context.Canceled
			case "delegate":
				update, in.Source.Reference.ID = nil, 0
				want = errNilDirtyUpdateDependency
			case "ID":
				in.Source.Reference.ID, in.CreatedPolicy = 0, 0
				want = ErrInvalidUpdateID
			case "created policy":
				in.CreatedPolicy, in.Source.Reference.Metadata = 0, nil
				want = ErrUnsupportedLoadedFileCreatedPolicy
			case "metadata":
				in.Source.Reference.Metadata = nil
				want = ErrInvalidFileSnapshotSource
			case "Actual nullness":
				in.Source.SHA1ActualValid = false
				want = ErrInvalidFileSnapshotSource
			case "Original":
				m := *s.Reference.Metadata
				m.sha1Original = loadedUpdateUnits(strings.Repeat("Z", 40))
				in.Source.Reference.Metadata = &m
				want = ErrUnsupportedLoadedOriginal
			case "catalog":
				c, want = FileMimeCatalog{}, ErrInvalidFileMimeCatalog
			case "MIME":
				var err error
				c, err = NewFileMimeCatalog([]FileMimeDefinition{{Type: "a", Extensions: "txt"}, {Type: "b", Extensions: "txt"}}, FileMimeCaseRootASCII)
				if err != nil {
					t.Fatal(err)
				}
				want = ErrAmbiguousFileMimeExtension
			case "path":
				p, err := NewPath("rk", "dir", "file?.txt", true)
				if err != nil {
					t.Fatal(err)
				}
				in.Source.Reference.Path = p
				want = ErrInvalidStoragePath
			case "Published SHA1":
				in.Published = loadedBinaryB()
				in.Published.SHA1 = strings.Repeat("C", 40)
			case "Published SHA256":
				in.Published = loadedBinaryB()
				in.Published.SHA256 = "bad"
			case "Published MD5":
				in.Published = loadedBinaryB()
				in.Published.MD5 = strings.Repeat("x", 32)
			case "Published size":
				in.Published = loadedBinaryB()
				in.Published.Size = -1
			case "policy zero", "policy unknown":
				in.Published = loadedBinaryB()
				want = ErrUnsupportedLocalFileChecksumPolicy
				if name == "policy unknown" {
					in.ChecksumPolicy = 255
				}
			}
			got, err := UpdateLocalFileWithoutClientChecksums(ctx, in, c, update)
			if got != (FileDirtyUpdateResult{}) || !errors.Is(err, want) || calls != 0 {
				t.Fatalf("result=%v err=%v want=%v calls=%d", got, err, want, calls)
			}
		})
	}
}

func TestLocalFileNoClientSignedTimesAndUserProjection(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		created, request, modified, updated int64
		user, wantUser                      []uint16
	}{
		{"nil", 11, 44, 44, 55, nil, nil},
		{"empty zero fallback", 0, 0, 0, -1, []uint16{}, []uint16{}},
		{"long negative fallback", -7, -9, -7, math.MinInt64, loadedUpdateUnits(strings.Repeat("a", 70)), loadedUpdateUnits(strings.Repeat("a", 61) + "...")},
		{"surrogate full signed fallback", math.MinInt64, math.MinInt64, math.MinInt64, math.MaxInt64, []uint16{0xd800, 'x', 0xdc00}, []uint16{0xd800, 'x', 0xdc00}},
		{"maximum modified", -7, math.MaxInt64, math.MaxInt64, 0, loadedUpdateUnits("u"), loadedUpdateUnits("u")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			m := *s.Reference.Metadata
			m.createdMillis = tc.created
			s.Reference.Metadata = &m
			in := noClientInput(s)
			in.ResourceModifiedMillis, in.UpdatedMillis, in.CurrentUserUTF16 = tc.request, tc.updated, tc.user
			calls := 0
			got, err := UpdateLocalFileWithoutClientChecksums(context.Background(), in, c,
				func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					if ref.Metadata.CreatedMillis() != tc.created || ref.Metadata.ModifiedMillis() != tc.modified || ref.Metadata.UpdatedMillis() != tc.updated ||
						!reflect.DeepEqual(ref.Metadata.ModifiedBy().UTF16(), tc.wantUser) || !reflect.DeepEqual(ref.Metadata.CreatedBy().UTF16(), loadedUpdateUnits("creator")) {
						t.Error("want exact signed times, created preservation and existing actor projection")
					}
					return 1, nil
				})
			if err != nil || calls != 1 || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
				t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestLocalFileNoClientDelegateAndReadOwnership(t *testing.T) {
	s, c := loadedBinaryFixture(t)
	in := noClientInput(s)
	sourceBefore, userBefore := *s.Reference.Metadata, append([]uint16{}, in.CurrentUserUTF16...)
	marker := errors.New("no-client delegate marker")
	for _, fail := range []bool{false, true} {
		calls := 0
		got, err := UpdateLocalFileWithoutClientChecksums(context.Background(), in, c,
			func(_ context.Context, ref FileReference) (int64, error) {
				calls++
				*ref.Metadata = FileMetadata{}
				if fail {
					return 9, marker
				}
				return 0, nil
			})
		if calls != 1 || (!fail && (err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 0}))) ||
			(fail && (!errors.Is(err, marker) || got != (FileDirtyUpdateResult{}))) {
			t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				got, err := UpdateLocalFileWithoutClientChecksums(context.Background(), in, c,
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
	if !reflect.DeepEqual(*s.Reference.Metadata, sourceBefore) || !reflect.DeepEqual(in.CurrentUserUTF16, userBefore) || in.Published != loadedBinaryB() || s.Reference.Content != loadedBinaryA() {
		t.Fatal("consumer/callback mutated Source, Published or raw user")
	}
}

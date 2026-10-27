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

func TestLocalClientChecksumsMatchingPublishedAndFieldProjection(t *testing.T) {
	s, c := loadedBinaryFixture(t)
	in := LocalFileWriteInput(noClientInput(s))
	in.Source.Reference.ID = 117
	checks := ClientFileChecksums{SHA1Original: loadedUpdateUnits(strings.Repeat("C", 40)),
		SHA256Original: loadedUpdateUnits(strings.Repeat("D", 64)), MD5Original: loadedUpdateUnits(strings.Repeat("F", 32))}
	before := *s.Reference.Metadata
	calls := 0
	got, err := UpdateLocalFileWithClientChecksums(context.Background(), in, checks, c,
		func(_ context.Context, ref FileReference) (int64, error) {
			calls++
			want := NewFileMetadata(FileMetadataInput{CreatedMillis: 11, ModifiedMillis: 44, UpdatedMillis: 55,
				CreatedBy: NewActor(loadedUpdateUnits("creator")), ModifiedBy: NewActor(loadedUpdateUnits("uploader")),
				SHA1Original: loadedUpdateUnits(strings.Repeat("c", 40)), MD5Original: loadedUpdateUnits(strings.Repeat("f", 32))})
			if ref.ID != 117 || ref.Path.Repo() != "rk" || ref.Path.PathName() != "dir/file.txt" ||
				ref.Content != (content.Metadata{SHA1: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64), MD5: strings.Repeat("f", 32), Size: 19}) ||
				ref.Metadata == nil || !reflect.DeepEqual(*ref.Metadata, want) {
				t.Error("want new Published tuple, normalized valid Originals, source identity/created and exact request stamps")
			}
			return 1, nil
		})
	// 新Published与旧Actual不同；若先与旧Actual验证，正常匹配请求会误拒绝。
	// New Published differs from old Actuals: never verify against old content.
	if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) || calls != 1 {
		t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
	}
	if !reflect.DeepEqual(*s.Reference.Metadata, before) || checks.SHA1Original[0] != 'C' || checks.MD5Original[0] != 'F' {
		t.Fatal("input metadata or Original facts were normalized in place")
	}
}

func TestLocalClientChecksumsPolicyAndRawOriginals(t *testing.T) {
	for _, tc := range []struct {
		name              string
		checks            ClientFileChecksums
		wantSHA1, wantMD5 []uint16
		clientAccept      bool
	}{
		{"null", ClientFileChecksums{}, nil, nil, true},
		{"empty", ClientFileChecksums{SHA1Original: []uint16{}, MD5Original: []uint16{}}, []uint16{}, []uint16{}, true},
		{"Java blank", ClientFileChecksums{SHA1Original: []uint16{0x2003, '\t'}, MD5Original: []uint16{0x1c}}, []uint16{0x2003, '\t'}, []uint16{0x1c}, true},
		{"trusted exact marker", ClientFileChecksums{SHA1Original: loadedUpdateUnits("NO_ORIG")}, loadedUpdateUnits("NO_ORIG"), nil, true},
		{"marker case sensitive", ClientFileChecksums{SHA1Original: loadedUpdateUnits("no_orig")}, loadedUpdateUnits("no_orig"), nil, false},
		{"NBSP not blank", ClientFileChecksums{SHA1Original: []uint16{0xa0}}, []uint16{0xa0}, nil, false},
		{"NUL not blank", ClientFileChecksums{SHA1Original: []uint16{0}}, []uint16{0}, nil, false},
		{"surrogate not blank", ClientFileChecksums{SHA1Original: []uint16{0xd800}}, []uint16{0xd800}, nil, false},
		{"nonhex I40 stays uppercase", ClientFileChecksums{SHA1Original: loadedUpdateUnits(strings.Repeat("I", 40))}, loadedUpdateUnits(strings.Repeat("I", 40)), nil, false},
		{"wrong length raw uppercase", ClientFileChecksums{SHA1Original: loadedUpdateUnits(strings.Repeat("C", 39))}, loadedUpdateUnits(strings.Repeat("C", 39)), nil, false},
		{"SHA1 mismatch", ClientFileChecksums{SHA1Original: loadedUpdateUnits(strings.Repeat("a", 40))}, loadedUpdateUnits(strings.Repeat("a", 40)), nil, false},
		{"SHA256 only mismatch", ClientFileChecksums{SHA256Original: loadedUpdateUnits(strings.Repeat("b", 64))}, nil, nil, false},
		{"MD5 only mismatch", ClientFileChecksums{MD5Original: loadedUpdateUnits(strings.Repeat("e", 32))}, nil, loadedUpdateUnits(strings.Repeat("e", 32)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, policy := range []LocalFileChecksumPolicy{LocalFileChecksumClient, LocalFileChecksumServer} {
				s, c := loadedBinaryFixture(t)
				in := LocalFileWriteInput(noClientInput(s))
				in.ChecksumPolicy = policy
				calls := 0
				got, err := UpdateLocalFileWithClientChecksums(context.Background(), in, tc.checks, c,
					func(_ context.Context, ref FileReference) (int64, error) {
						calls++
						if ref.Content != loadedBinaryB() || !reflect.DeepEqual(ref.Metadata.SHA1OriginalUTF16(), tc.wantSHA1) || !reflect.DeepEqual(ref.Metadata.MD5OriginalUTF16(), tc.wantMD5) {
							t.Error("SERVER/accepted CLIENT must preserve raw invalid/blank/null/empty Originals")
						}
						return 1, nil
					})
				accept := policy == LocalFileChecksumServer || tc.clientAccept
				if accept {
					if err != nil || calls != 1 || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
						t.Fatalf("policy=%v result=%v err=%v calls=%d", policy, got, err, calls)
					}
				} else if !errors.Is(err, ErrClientChecksumMismatch) || calls != 0 || got != (FileDirtyUpdateResult{}) {
					t.Fatalf("policy=%v result=%v err=%v calls=%d want mismatch", policy, got, err, calls)
				}
			}
		})
	}
}

func TestLocalClientChecksumsTrustedSHA256NoOpAndRawActorDirty(t *testing.T) {
	for _, mode := range []string{"exact no-op", "SERVER lowercase marker dirty", "raw actor dirty", "no-client still dirty"} {
		t.Run(mode, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			m := *s.Reference.Metadata
			m.sha1Original, m.md5Original = nil, nil
			if mode == "raw actor dirty" {
				m.modifiedBy = NewActor(loadedUpdateUnits(strings.Repeat("a", 70)))
			}
			s.Reference.Metadata = &m
			in := LocalFileWriteInput(noClientInput(s))
			in.Published, in.ResourceModifiedMillis, in.UpdatedMillis = loadedBinaryA(), 22, 33
			in.CurrentUserUTF16 = loadedUpdateUnits("modifier")
			checks := ClientFileChecksums{SHA256Original: loadedUpdateUnits("NO_ORIG")}
			if mode == "SERVER lowercase marker dirty" {
				checks.SHA256Original = loadedUpdateUnits("no_orig")
				in.ChecksumPolicy = LocalFileChecksumServer
			}
			if mode == "raw actor dirty" {
				in.CurrentUserUTF16 = loadedUpdateUnits(strings.Repeat("a", 70))
			}
			calls := 0
			update := func(_ context.Context, ref FileReference) (int64, error) {
				calls++
				if ref.Content != loadedBinaryA() || !reflect.DeepEqual(ref.Metadata.ModifiedBy().UTF16(), m.ModifiedBy().UTF16()) {
					t.Error("projected actor/content must equal Source despite raw comparator difference")
				}
				return 1, nil
			}
			var got FileDirtyUpdateResult
			var err error
			if mode == "no-client still dirty" {
				got, err = UpdateLocalFileWithoutClientChecksums(context.Background(), in, c, update)
			} else {
				got, err = UpdateLocalFileWithClientChecksums(context.Background(), in, checks, c, update)
			}
			want, wantCalls := (FileDirtyUpdateResult{Changed: true, Count: 1}), 1
			if mode == "exact no-op" {
				want, wantCalls = FileDirtyUpdateResult{}, 0
			}
			if err != nil || got != want || calls != wantCalls {
				t.Fatalf("result=%v err=%v calls=%d want=%v/%d", got, err, calls, want, wantCalls)
			}
		})
	}
}

func TestLocalClientChecksumsUTF16BlankBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unit  uint16
		blank bool
	}{
		{"vertical tab", 0x0b, true}, {"line separator", 0x2028, true}, {"ideographic space", 0x3000, true},
		{"NEL", 0x85, false}, {"figure space", 0x2007, false}, {"narrow NBSP", 0x202f, false}, {"zero width space", 0x200b, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			in := LocalFileWriteInput(noClientInput(s))
			checks := ClientFileChecksums{SHA1Original: []uint16{tc.unit}, SHA256Original: []uint16{tc.unit}, MD5Original: []uint16{tc.unit}}
			calls := 0
			got, err := UpdateLocalFileWithClientChecksums(context.Background(), in, checks, c,
				func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					if !reflect.DeepEqual(ref.Metadata.SHA1OriginalUTF16(), []uint16{tc.unit}) || !reflect.DeepEqual(ref.Metadata.MD5OriginalUTF16(), []uint16{tc.unit}) {
						t.Error("blank Originals skip policy checks but must not be trimmed or dropped")
					}
					return 1, nil
				})
			if tc.blank {
				if err != nil || calls != 1 || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
					t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
				}
			} else if !errors.Is(err, ErrClientChecksumMismatch) || calls != 0 || got != (FileDirtyUpdateResult{}) {
				t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestLocalClientChecksumsPrefixErrorsBeatMismatch(t *testing.T) {
	for _, name := range []string{"cancel", "delegate", "ID", "created policy", "metadata", "Actual nullness", "Original", "catalog", "MIME", "path", "old Actual", "Published", "local policy"} {
		t.Run(name, func(t *testing.T) {
			s, c := loadedBinaryFixture(t)
			in := LocalFileWriteInput(noClientInput(s))
			checks := ClientFileChecksums{SHA256Original: loadedUpdateUnits(strings.Repeat("0", 64))}
			ctx := context.Background()
			calls := 0
			update := func(context.Context, FileReference) (int64, error) { calls++; return 1, nil }
			want := error(ErrInvalidFileSnapshotSource)
			switch name {
			case "cancel":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, update, in.Source.Reference.ID = cancelled, nil, 0
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
			case "Actual nullness":
				in.Source.SHA1ActualValid = false
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
			case "old Actual":
				in.Source.Reference.Content.SHA256 = ""
				in.Published = content.Metadata{}
				in.ChecksumPolicy = 0
				want = ErrInvalidLoadedActual
			case "Published":
				in.Published = content.Metadata{}
				in.ChecksumPolicy = 0
				want = ErrInvalidPublishedFileMetadata
			case "local policy":
				in.ChecksumPolicy = 0
				want = ErrUnsupportedLocalFileChecksumPolicy
			}
			got, err := UpdateLocalFileWithClientChecksums(ctx, in, checks, c, update)
			if got != (FileDirtyUpdateResult{}) || !errors.Is(err, want) || calls != 0 {
				t.Fatalf("result=%v err=%v want=%v calls=%d", got, err, want, calls)
			}
		})
	}
}

func TestLocalClientChecksumsCallbackResultsAndOwnership(t *testing.T) {
	s, c := loadedBinaryFixture(t)
	in := LocalFileWriteInput(noClientInput(s))
	checks := ClientFileChecksums{SHA1Original: loadedUpdateUnits(strings.Repeat("C", 40)), SHA256Original: loadedUpdateUnits("NO_ORIG"), MD5Original: []uint16{}}
	sourceBefore := *s.Reference.Metadata
	sourceOriginalBefore := s.Reference.Metadata.SHA1OriginalUTF16()
	checksBefore := ClientFileChecksums{SHA1Original: append([]uint16{}, checks.SHA1Original...), SHA256Original: append([]uint16{}, checks.SHA256Original...), MD5Original: []uint16{}}
	userBefore := append([]uint16{}, in.CurrentUserUTF16...)
	marker := errors.New("client checksum delegate marker")
	for _, fail := range []bool{false, true} {
		calls := 0
		got, err := UpdateLocalFileWithClientChecksums(context.Background(), in, checks, c,
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
				got, err := UpdateLocalFileWithClientChecksums(context.Background(), in, checks, c,
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
	if !reflect.DeepEqual(checks, checksBefore) || !reflect.DeepEqual(*s.Reference.Metadata, sourceBefore) ||
		!reflect.DeepEqual(s.Reference.Metadata.SHA1OriginalUTF16(), sourceOriginalBefore) || !reflect.DeepEqual(in.CurrentUserUTF16, userBefore) || in.Published != loadedBinaryB() {
		t.Fatal("producer mutated client/source/user/Published facts")
	}
}

func TestLocalClientChecksumsCancellationBeforeNoOp(t *testing.T) {
	s, c := loadedBinaryFixture(t)
	in := LocalFileWriteInput(noClientInput(s))
	in.Published, in.ResourceModifiedMillis, in.UpdatedMillis = loadedBinaryA(), 22, 33
	in.CurrentUserUTF16 = loadedUpdateUnits("modifier")
	checks := ClientFileChecksums{SHA1Original: loadedUpdateUnits(strings.Repeat("a", 40)), SHA256Original: loadedUpdateUnits("NO_ORIG")}
	ctx := &loadedUpdateSecondCancel{Context: context.Background()}
	calls := 0
	got, err := UpdateLocalFileWithClientChecksums(ctx, in, checks, c,
		func(context.Context, FileReference) (int64, error) { calls++; return 1, nil })
	if got != (FileDirtyUpdateResult{}) || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("result=%v err=%v calls=%d", got, err, calls)
	}
}

package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func originalCaseCheckError(t *testing.T, source FileSnapshotSource, profile LoadedOriginalCase, want error) {
	t.Helper()
	got, err := LoadedFileSnapshotWithOriginalCase(source, profile)
	if !errors.Is(err, want) || err.Error() != want.Error() || !reflect.DeepEqual(got.Fields(), FileInfoSnapshotInput{}) {
		t.Fatalf("assembly = (%+v,%v), want zero/%v", got.Fields(), err, want)
	}
}

// 字面 ASCII 配对与 dotless-i 期望，不使用产品 normalizer 计算答案。
// Literal ASCII pairs and dotless-i expectations, independent of the normalizer.
func TestLoadedOriginalCaseEveryUnitAndDotlessClosure(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		for slot, length := range []int{40, 32} {
			for code := 0; code <= 128; code++ {
				unit := uint16(code)
				if code == 128 {
					unit = 0x0131
				}
				for _, position := range []int{0, length / 2, length - 1} {
					t.Run(fmt.Sprintf("case%d/slot%d/unit%04x/position%d", profile, slot, unit, position), func(t *testing.T) {
						original, want := loadedSnapshotTestRepeat('G', length), loadedSnapshotTestRepeat('g', length)
						original[position], want[position] = unit, unit
						if unit < 128 {
							want[position] = loadedASCIIWantUnit(unit)
						}
						if unit == 'I' && profile == LoadedOriginalCaseTurkic {
							want[position] = 0x0131
						}
						snapshot, err := LoadedFileSnapshotWithOriginalCase(loadedASCIISource(t, slot, original), profile)
						if err != nil {
							t.Fatal(err)
						}
						loadedSnapshotTestCheck(t, snapshot, loadedASCIIWant(slot, want))
						// 经持久化值域再次加载仍成功，不能拒绝自己产生的 U+0131。
						// Reload accepts the output domain, including produced U+0131.
						again, err := LoadedFileSnapshotWithOriginalCase(loadedASCIISource(t, slot, want), profile)
						if err != nil {
							t.Fatal(err)
						}
						loadedSnapshotTestCheck(t, again, loadedASCIIWant(slot, want))
					})
				}
			}
		}
	}
}

func TestLoadedOriginalCaseMultipleIAndOwnership(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		wantUnit := uint16('i')
		if profile == LoadedOriginalCaseTurkic {
			wantUnit = 0x0131
		}
		source := loadedSnapshotTestSource(t, loadedSnapshotTestRepeat('I', 40), loadedSnapshotTestRepeat('I', 32))
		want := loadedSnapshotTestWant()
		want.Checksums[0].Original, want.Checksums[1].Original = loadedSnapshotTestRepeat(wantUnit, 40), loadedSnapshotTestRepeat(wantUnit, 32)
		first, err := LoadedFileSnapshotWithOriginalCase(source, profile)
		if err != nil {
			t.Fatal(err)
		}
		loadedSnapshotTestOverwrite(first.Fields())
		second, err := LoadedFileSnapshotWithOriginalCase(source, profile)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(source.Reference.Metadata.SHA1OriginalUTF16(), loadedSnapshotTestRepeat('I', 40)) {
			t.Fatal("source normalized in place")
		}
		*source.Reference.Metadata = FileMetadata{}
		loadedSnapshotTestCheck(t, first, want)
		loadedSnapshotTestCheck(t, second, want)
	}
}

func TestLoadedOriginalCaseLengthAndUnsupportedBoundaries(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		for slot, length := range []int{40, 32} {
			for _, size := range []int{0, length - 1, length + 1} {
				u := loadedSnapshotTestRepeat('I', size)
				if size > 0 {
					copy(u, []uint16{0x0130, 0xd800, 0x0131, 'I'})
				}
				got, err := LoadedFileSnapshotWithOriginalCase(loadedASCIISource(t, slot, u), profile)
				if err != nil {
					t.Fatal(err)
				}
				loadedSnapshotTestCheck(t, got, loadedASCIIWant(slot, u))
			}
			for _, bad := range []uint16{0x0100, 0x0128, 0x03a3, 0xd800, 0xdc00} {
				u := loadedSnapshotTestRepeat('G', length)
				u[length-1] = bad
				originalCaseCheckError(t, loadedASCIISource(t, slot, u), profile, ErrUnsupportedLoadedOriginal)
			}
			for _, sensitive := range []uint16{'I', 0x0131} {
				s := loadedASCIISource(t, slot, loadedSnapshotTestRepeat(sensitive, length))
				originalCaseCheckError(t, s, LoadedOriginalCaseInvariant, ErrUnsupportedLoadedOriginal)
				loadedSnapshotTestError(t, s, ErrUnsupportedLoadedOriginal)
			}
		}
	}
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseInvariant, LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		got, err := LoadedFileSnapshotWithOriginalCase(loadedSnapshotTestSource(t, nil, nil), profile)
		if err != nil {
			t.Fatal(err)
		}
		loadedSnapshotTestCheck(t, got, loadedSnapshotTestWant())
	}
}

func TestLoadedOriginalCaseUnknownAndGuardOrder(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{3, 255} {
		for _, originals := range [][]uint16{nil, {}, loadedSnapshotTestRepeat(0x0130, 40)} {
			originalCaseCheckError(t, loadedSnapshotTestSource(t, originals, nil), profile, ErrUnsupportedLoadedOriginalCase)
		}
		s := loadedSnapshotTestSource(t, nil, nil)
		s.Reference.Path, s.Reference.Metadata = Path{}, nil
		originalCaseCheckError(t, s, profile, ErrInvalidPath)
		s = loadedSnapshotTestSource(t, nil, nil)
		s.Reference.Metadata = nil
		originalCaseCheckError(t, s, profile, ErrInvalidFileSnapshotSource)
		for slot := 0; slot < 3; slot++ {
			for _, invalid := range []struct {
				value string
				valid bool
			}{{"contradiction", false}, {"\xff", true}} {
				s = loadedSnapshotTestSource(t, nil, nil)
				loadedSnapshotTestActual(&s, slot, invalid.value, invalid.valid)
				originalCaseCheckError(t, s, profile, ErrInvalidFileSnapshotSource)
			}
		}
	}
}

// 四个实际消费者共用输入选择；Desired 的期望来自独立字面字段，不由加载器生成。
// Exercise all four consumers; Desired expectations are literal, not loader-derived.
func originalCaseConsumer(t *testing.T, name string, ctx context.Context, source FileSnapshotSource, profile LoadedOriginalCase, created LoadedFileCreatedPolicy, catalog FileMimeCatalog, desired FileInfoSnapshot, update func(context.Context, FileReference) (int64, error)) (FileDirtyUpdateResult, error) {
	t.Helper()
	switch name {
	case "snapshot":
		return UpdateLoadedFileIfChanged(ctx, LoadedFileUpdateInput{Source: source, Desired: desired, CreatedPolicy: created, OriginalCase: profile}, catalog, update)
	case "binary":
		return UpdateLoadedFileBinary(ctx, LoadedFileBinaryInput{Source: source, Published: loadedBinaryA(), CreatedPolicy: created, OriginalCase: profile}, catalog, update)
	default:
		in := noClientInput(source)
		in.OriginalCase, in.CreatedPolicy, in.Published = profile, created, loadedBinaryA()
		if name == "no-client" {
			return UpdateLocalFileWithoutClientChecksums(ctx, in, catalog, update)
		}
		in.ChecksumPolicy = LocalFileChecksumServer
		return UpdateLocalFileWithClientChecksums(ctx, in, ClientFileChecksums{SHA1Original: loadedSnapshotTestRepeat('I', 40)}, catalog, update)
	}
}

func TestLoadedOriginalCaseConsumerWiringAndRawDesired(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		for _, name := range []string{"snapshot", "binary", "no-client", "client"} {
			t.Run(fmt.Sprintf("case%d/%s", profile, name), func(t *testing.T) {
				s, c := loadedBinaryFixture(t)
				m := *s.Reference.Metadata
				m.sha1Original, m.md5Original = loadedSnapshotTestRepeat('I', 40), loadedSnapshotTestRepeat('I', 32)
				s.Reference.Metadata = &m
				_, fields, _ := loadedUpdateFixture(t)
				fields.SizeBytes = 7
				unit := uint16('i')
				if profile == LoadedOriginalCaseTurkic {
					unit = 0x0131
				}
				fields.Checksums[0].Original = loadedSnapshotTestRepeat(unit, 40)
				fields.Checksums[1].Actual, fields.Checksums[1].Original = loadedUpdateUnits(strings.Repeat("e", 32)), loadedSnapshotTestRepeat(unit, 32)
				calls := 0
				update := func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					if ref.Metadata == nil {
						t.Fatal("missing projected metadata")
					}
					want := []uint16(nil)
					if name == "client" || name == "snapshot" {
						want = loadedSnapshotTestRepeat('I', 40)
					}
					if !reflect.DeepEqual(ref.Metadata.SHA1OriginalUTF16(), want) {
						t.Error("write-side raw Original must not use load casing")
					}
					return 1, nil
				}
				got, err := originalCaseConsumer(t, name, context.Background(), s, profile, PreserveLoadedFileCreated, c, loadedUpdateSnapshot(t, fields), update)
				changed := name == "no-client" || name == "client"
				want := FileDirtyUpdateResult{}
				if changed {
					want = FileDirtyUpdateResult{Changed: true, Count: 1}
				}
				if err != nil || got != want || calls != int(want.Count) {
					t.Fatalf("result=%+v error=%v calls=%d", got, err, calls)
				}
				if name == "snapshot" {
					fields.Checksums[0].Original = loadedSnapshotTestRepeat('I', 40)
					got, err = originalCaseConsumer(t, name, context.Background(), s, profile, PreserveLoadedFileCreated, c, loadedUpdateSnapshot(t, fields), update)
					if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) || calls != 1 {
						t.Fatalf("raw Desired result=%+v error=%v calls=%d", got, err, calls)
					}
				}
			})
		}
	}
}

func TestLoadedOriginalCaseConsumerFailureOrder(t *testing.T) {
	for _, name := range []string{"snapshot", "binary", "no-client", "client"} {
		for _, failure := range []string{"cancel", "delegate", "id", "created", "case-before-MIME", "invariant"} {
			t.Run(name+"/"+failure, func(t *testing.T) {
				s, c := loadedBinaryFixture(t)
				m := *s.Reference.Metadata
				m.sha1Original = loadedSnapshotTestRepeat('I', 40)
				s.Reference.Metadata = &m
				ctx, created, profile := context.Background(), PreserveLoadedFileCreated, LoadedOriginalCase(255)
				calls := 0
				cb := func(context.Context, FileReference) (int64, error) { calls++; return 1, nil }
				want := error(ErrUnsupportedLoadedOriginalCase)
				switch failure {
				case "cancel":
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx, cb, s.Reference.ID, created, want = cancelled, nil, 0, 0, context.Canceled
				case "delegate":
					cb, s.Reference.ID, created, want = nil, 0, 0, errNilDirtyUpdateDependency
				case "id":
					s.Reference.ID, created, want = 0, 0, ErrInvalidUpdateID
				case "created":
					created, want = 0, ErrUnsupportedLoadedFileCreatedPolicy
				case "case-before-MIME":
					c = FileMimeCatalog{}
				case "invariant":
					profile, want = LoadedOriginalCaseInvariant, ErrUnsupportedLoadedOriginal
				}
				got, err := originalCaseConsumer(t, name, ctx, s, profile, created, c, FileInfoSnapshot{}, cb)
				if !errors.Is(err, want) || got != (FileDirtyUpdateResult{}) || calls != 0 {
					t.Fatalf("result=%+v error=%v calls=%d want=%v", got, err, calls, want)
				}
			})
		}
	}
}

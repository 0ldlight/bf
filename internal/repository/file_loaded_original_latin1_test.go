package repository

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// 字面字母配对表，不调用 Unicode lowercase 或产品 normalizer 计算期望。
// Literal alphabet pairs provide expectations, independent of Unicode/product casing.
func latin1WantUnit(unit uint16, profile LoadedOriginalCase) uint16 {
	upper := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZÀÁÂÃÄÅÆÇÈÉÊËÌÍÎÏÐÑÒÓÔÕÖØÙÚÛÜÝÞ")
	lower := []rune("abcdefghijklmnopqrstuvwxyzàáâãäåæçèéêëìíîïðñòóôõöøùúûüýþ")
	if profile == LoadedOriginalCaseTurkic {
		if unit == 'I' {
			return 0x0131
		}
		if unit == 0x0130 {
			return 'i'
		}
	}
	for i, candidate := range upper {
		if rune(unit) == candidate {
			return uint16(lower[i])
		}
	}
	return unit
}

// 两规则、两摘要、三位置与全部 Latin-1/两 dot-I 码元，包括拒绝的 lt 特例。
// Both modes/lengths/three positions cover all Latin-1 and dotted/dotless I units.
func TestLoadedOriginalLatin1EveryUnitAndClosure(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		for slot, length := range []int{40, 32} {
			for code := 0; code < 258; code++ {
				unit := uint16(code)
				if code >= 256 {
					unit = uint16(0x0130 + code - 256)
				}
				for _, position := range []int{0, length / 2, length - 1} {
					t.Run(fmt.Sprintf("case%d/slot%d/unit%04x/position%d", profile, slot, unit, position), func(t *testing.T) {
						original := loadedSnapshotTestRepeat('G', length)
						original[position] = unit
						if profile == LoadedOriginalCaseNonTurkic && (unit == 0x00cc || unit == 0x00cd || unit == 0x0130) {
							originalCaseCheckError(t, loadedASCIISource(t, slot, original), profile, ErrUnsupportedLoadedOriginal)
							return
						}
						want := loadedSnapshotTestRepeat('g', length)
						want[position] = latin1WantUnit(unit, profile)
						for _, input := range [][]uint16{original, want} {
							got, err := LoadedFileSnapshotWithOriginalCase(loadedASCIISource(t, slot, input), profile)
							if err != nil {
								t.Fatal(err)
							}
							loadedSnapshotTestCheck(t, got, loadedASCIIWant(slot, want))
						}
					})
				}
			}
		}
	}
}

func latin1RepeatPattern(pattern string, length int) []uint16 {
	runes := []rune(pattern)
	out := make([]uint16, length)
	for i := range out {
		out[i] = uint16(runes[i%len(runes)])
	}
	return out
}

func TestLoadedOriginalLatin1MixedOwnershipAndLengthBranches(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		for slot, length := range []int{40, 32} {
			original, want := latin1RepeatPattern("Ò×Ó÷ßµÿØÞ", length), latin1RepeatPattern("ò×ó÷ßµÿøþ", length)
			source := loadedASCIISource(t, slot, original)
			first, err := LoadedFileSnapshotWithOriginalCase(source, profile)
			if err != nil {
				t.Fatal(err)
			}
			loadedSnapshotTestOverwrite(first.Fields())
			original[0] = 0xd800
			second, err := LoadedFileSnapshotWithOriginalCase(source, profile)
			if err != nil {
				t.Fatal(err)
			}
			*source.Reference.Metadata = FileMetadata{}
			loadedSnapshotTestCheck(t, first, loadedASCIIWant(slot, want))
			loadedSnapshotTestCheck(t, second, loadedASCIIWant(slot, want))
			for _, size := range []int{0, length - 1, length + 1} {
				raw := latin1RepeatPattern("ÌÍİΣ\x00", size)
				if size > 0 {
					raw[size-1] = 0xd800
				}
				got, err := LoadedFileSnapshotWithOriginalCase(loadedASCIISource(t, slot, raw), profile)
				if err != nil {
					t.Fatal(err)
				}
				loadedSnapshotTestCheck(t, got, loadedASCIIWant(slot, raw))
			}
		}
	}
	for slot, length := range []int{40, 32} {
		original, want := latin1RepeatPattern("İIÌÍ", length), latin1RepeatPattern("iıìí", length)
		got, err := LoadedFileSnapshotWithOriginalCase(loadedASCIISource(t, slot, original), LoadedOriginalCaseTurkic)
		if err != nil {
			t.Fatal(err)
		}
		loadedSnapshotTestCheck(t, got, loadedASCIIWant(slot, want))
		originalCaseCheckError(t, loadedASCIISource(t, slot, original), LoadedOriginalCaseNonTurkic, ErrUnsupportedLoadedOriginal)
	}
}

func TestLoadedOriginalLatin1UnsupportedAndInvariantBoundaries(t *testing.T) {
	for slot, length := range []int{40, 32} {
		for _, unit := range []uint16{0x0100, 0x0128, 0x0307, 0x039f, 0x03a3, 0xd800, 0xdc00, 0xffff} {
			for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
				originalCaseCheckError(t, loadedASCIISource(t, slot, loadedSnapshotTestRepeat(unit, length)), profile, ErrUnsupportedLoadedOriginal)
			}
		}
		for code := 128; code < 256; code++ {
			source := loadedASCIISource(t, slot, loadedSnapshotTestRepeat(uint16(code), length))
			originalCaseCheckError(t, source, LoadedOriginalCaseInvariant, ErrUnsupportedLoadedOriginal)
			loadedSnapshotTestError(t, source, ErrUnsupportedLoadedOriginal)
		}
		pair := append(loadedSnapshotTestRepeat('G', length-2), 0xd83d, 0xde00)
		originalCaseCheckError(t, loadedASCIISource(t, slot, pair), LoadedOriginalCaseTurkic, ErrUnsupportedLoadedOriginal)
	}
}

func TestLoadedOriginalLatin1UnknownAndExistingGuardOrder(t *testing.T) {
	s := loadedSnapshotTestSource(t, loadedSnapshotTestRepeat(0x00d2, 40), nil)
	originalCaseCheckError(t, s, 255, ErrUnsupportedLoadedOriginalCase)
	s.Reference.Path = Path{}
	originalCaseCheckError(t, s, 255, ErrInvalidPath)
	s = loadedSnapshotTestSource(t, nil, nil)
	s.Reference.Metadata = nil
	originalCaseCheckError(t, s, 255, ErrInvalidFileSnapshotSource)
	for slot := 0; slot < 3; slot++ {
		s = loadedSnapshotTestSource(t, loadedSnapshotTestRepeat(0x00d2, 40), nil)
		loadedSnapshotTestActual(&s, slot, "contradiction", false)
		originalCaseCheckError(t, s, 255, ErrInvalidFileSnapshotSource)
	}
}

// 新接受值进入四条实际消费者；写侧 raw 值与 Desired 不被加载规则重写。
// Newly supported values reach all four consumers without rewriting raw write inputs.
func TestLoadedOriginalLatin1ConsumerWiringAndWritePhases(t *testing.T) {
	for _, profile := range []LoadedOriginalCase{LoadedOriginalCaseNonTurkic, LoadedOriginalCaseTurkic} {
		for _, name := range []string{"snapshot", "binary", "no-client", "client"} {
			t.Run(fmt.Sprintf("%d/%s", profile, name), func(t *testing.T) {
				s, catalog := loadedBinaryFixture(t)
				m := *s.Reference.Metadata
				m.sha1Original, m.md5Original = loadedSnapshotTestRepeat(0x00d2, 40), loadedSnapshotTestRepeat(0x00d1, 32)
				s.Reference.Metadata = &m
				_, fields, _ := loadedUpdateFixture(t)
				fields.Checksums[0].Original = loadedSnapshotTestRepeat(0x00f2, 40)
				fields.Checksums[1].Actual, fields.Checksums[1].Original = loadedUpdateUnits("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"), loadedSnapshotTestRepeat(0x00f1, 32)
				calls := 0
				var wantSHA1, wantMD5 []uint16
				if name == "client" {
					wantSHA1, wantMD5 = m.sha1Original, m.md5Original
				}
				update := func(_ context.Context, ref FileReference) (int64, error) {
					calls++
					if ref.Metadata == nil || !reflect.DeepEqual(ref.Metadata.SHA1OriginalUTF16(), wantSHA1) || !reflect.DeepEqual(ref.Metadata.MD5OriginalUTF16(), wantMD5) {
						t.Fatal("wrong projected Original phase")
					}
					return 1, nil
				}
				var got FileDirtyUpdateResult
				var err error
				if name == "client" {
					in := noClientInput(s)
					in.OriginalCase, in.ChecksumPolicy = profile, LocalFileChecksumServer
					got, err = UpdateLocalFileWithClientChecksums(context.Background(), in, ClientFileChecksums{SHA1Original: m.sha1Original, MD5Original: m.md5Original}, catalog, update)
				} else {
					got, err = originalCaseConsumer(t, name, context.Background(), s, profile, PreserveLoadedFileCreated, catalog, loadedUpdateSnapshot(t, fields), update)
				}
				changed := name == "no-client" || name == "client"
				wantCalls := 0
				if changed {
					wantCalls = 1
				}
				if err != nil || got.Changed != changed || got.Count != int64(wantCalls) || calls != wantCalls {
					t.Fatalf("result=%+v err=%v calls=%d", got, err, calls)
				}
				if name == "snapshot" {
					fields.Checksums[0].Original, wantSHA1, wantMD5 = m.sha1Original, m.sha1Original, loadedSnapshotTestRepeat(0x00f1, 32)
					got, err = originalCaseConsumer(t, name, context.Background(), s, profile, PreserveLoadedFileCreated, catalog, loadedUpdateSnapshot(t, fields), update)
					if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) || calls != 1 {
						t.Fatalf("raw Desired = %+v/%v calls=%d", got, err, calls)
					}
				}
				if name == "binary" {
					wantSHA1, wantMD5 = loadedSnapshotTestRepeat(0x00f2, 40), loadedSnapshotTestRepeat(0x00f1, 32)
					got, err = UpdateLoadedFileBinary(context.Background(), LoadedFileBinaryInput{Source: s, Published: loadedBinaryB(), CreatedPolicy: PreserveLoadedFileCreated, OriginalCase: profile}, catalog, update)
					if err != nil || got != (FileDirtyUpdateResult{Changed: true, Count: 1}) || calls != 1 {
						t.Fatalf("binary write = %+v/%v calls=%d", got, err, calls)
					}
				}
			})
		}
	}
}

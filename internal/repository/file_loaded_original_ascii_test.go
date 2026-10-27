package repository

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// 期望表是固定 ASCII 字母对；不调用产品归一化器或 Unicode lowercase。
// A literal ASCII alphabet pair supplies expectations, not the product normalizer.
func loadedASCIIWantUnit(unit uint16) uint16 {
	const upper, lower = "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz"
	if index := strings.IndexByte(upper, byte(unit)); index >= 0 {
		return uint16(lower[index])
	}
	return unit
}

func loadedASCIISource(t *testing.T, slot int, original []uint16) FileSnapshotSource {
	t.Helper()
	if slot == 0 {
		return loadedSnapshotTestSource(t, original, nil)
	}
	return loadedSnapshotTestSource(t, nil, original)
}

func loadedASCIIWant(slot int, original []uint16) FileInfoSnapshotInput {
	want := loadedSnapshotTestWant()
	want.Checksums[slot].Original = original
	return want
}

// 两个长度、所有 ASCII 码元及首/中/末位置均通过实际组装出口；大写 I
// 是显式保留的 Locale 边界，NUL/控制符的纯值支持不宣称 SQL 可接受。
// Exercise both lengths and every ASCII unit at first/middle/last positions
// through assembly. Capital I stays unsupported; pure NUL support is not SQL support.
func TestLoadedOriginalASCIIEveryUnit(t *testing.T) {
	for slot, length := range []int{40, 32} {
		for unit := uint16(0); unit < 128; unit++ {
			for _, position := range []int{0, length / 2, length - 1} {
				t.Run(fmt.Sprintf("slot%d/unit%02x/position%d", slot, unit, position), func(t *testing.T) {
					original := loadedSnapshotTestRepeat('G', length)
					original[position] = unit
					source := loadedASCIISource(t, slot, original)
					if unit == 'I' {
						loadedSnapshotTestError(t, source, ErrUnsupportedLoadedOriginal)
						return
					}
					wantOriginal := loadedSnapshotTestRepeat('g', length)
					wantOriginal[position] = loadedASCIIWantUnit(unit)
					loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, source), loadedASCIIWant(slot, wantOriginal))
					var sourceOriginal []uint16
					if slot == 0 {
						sourceOriginal = source.Reference.Metadata.SHA1OriginalUTF16()
					} else {
						sourceOriginal = source.Reference.Metadata.MD5OriginalUTF16()
					}
					if !reflect.DeepEqual(sourceOriginal, original) {
						t.Error("assembly changed source Original")
					}
				})
			}
		}
	}
}

// ASCII 整串与非 hex Original 都按加载规则处理，不能误用 client 的
// validate=true 规则，也不能 trim 或把空白 Original 替换为 null。
// Whole ASCII strings use loader rules, not validating-client hex rules or trimming.
func TestLoadedOriginalASCIIWholeStrings(t *testing.T) {
	for slot, length := range []int{40, 32} {
		for _, row := range []struct {
			name string
			unit uint16
			want uint16
		}{
			{"nonhex uppercase G", 'G', 'g'}, {"nonhex uppercase Z", 'Z', 'z'},
			{"uppercase J", 'J', 'j'}, {"lowercase i", 'i', 'i'},
			{"space", ' ', ' '}, {"tab", '\t', '\t'}, {"nul", 0, 0},
			{"punctuation", '[', '['}, {"delete", 127, 127},
		} {
			t.Run(fmt.Sprintf("slot%d/%s", slot, row.name), func(t *testing.T) {
				source := loadedASCIISource(t, slot, loadedSnapshotTestRepeat(row.unit, length))
				loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, source), loadedASCIIWant(slot, loadedSnapshotTestRepeat(row.want, length)))
			})
		}
	}
}

// 恰长非 ASCII（含未配对 UTF-16 和有效代理对）仍是明确不支持画像。
// Exact-length non-ASCII, including isolated and paired surrogates, remains unsupported.
func TestLoadedOriginalASCIIRetainsUnsupportedBoundary(t *testing.T) {
	for slot, length := range []int{40, 32} {
		for _, unit := range []uint16{128, 0x00a0, 0x0130, 0x03a3, 0xd800, 0xdc00, 0xffff} {
			for _, position := range []int{0, length / 2, length - 1} {
				t.Run(fmt.Sprintf("slot%d/unit%04x/position%d", slot, unit, position), func(t *testing.T) {
					original := loadedSnapshotTestRepeat('G', length)
					original[position] = unit
					loadedSnapshotTestError(t, loadedASCIISource(t, slot, original), ErrUnsupportedLoadedOriginal)
				})
			}
		}
		pair := append(loadedSnapshotTestRepeat('G', length-2), 0xd83d, 0xde00)
		loadedSnapshotTestError(t, loadedASCIISource(t, slot, pair), ErrUnsupportedLoadedOriginal)
	}
}

// 长度分支仍按 UTF-16 码元；不恰长时 I、非 ASCII、代理与大小写逐字保留。
// Non-exact UTF-16 lengths preserve I, non-ASCII, surrogates and case verbatim.
func TestLoadedOriginalASCIINonExactLengthsRemainRaw(t *testing.T) {
	for slot, length := range []int{40, 32} {
		for _, size := range []int{length - 1, length + 1} {
			original := loadedSnapshotTestRepeat('G', size)
			copy(original, []uint16{'I', 0x0130, 0xd800, 0, 0xdc00})
			loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, loadedASCIISource(t, slot, original)), loadedASCIIWant(slot, original))
		}
	}
}

func TestLoadedOriginalASCIIOwnership(t *testing.T) {
	sha1, md5 := loadedSnapshotTestRepeat('G', 40), loadedSnapshotTestRepeat('Z', 32)
	source := loadedSnapshotTestSource(t, sha1, md5)
	want := loadedSnapshotTestWant()
	want.Checksums[0].Original, want.Checksums[1].Original = loadedSnapshotTestRepeat('g', 40), loadedSnapshotTestRepeat('z', 32)
	first, second := loadedSnapshotTestCall(t, source), loadedSnapshotTestCall(t, source)
	sha1[0], md5[0] = 'I', 0xffff
	loadedSnapshotTestOverwrite(first.Fields())
	loadedSnapshotTestCheck(t, first, want)
	loadedSnapshotTestCheck(t, second, want)
	// 同一不可变 Source 重复组装不应受先前结果副本修改影响。
	// Reassembling the same immutable source is independent of returned copies.
	loadedSnapshotTestCheck(t, loadedSnapshotTestCall(t, source), want)
	*source.Reference.Metadata = FileMetadata{}
	loadedSnapshotTestCheck(t, first, want)
	loadedSnapshotTestCheck(t, second, want)
}

// 画像扩展不前移 Original 检查，既有路径/元数据/Actual 失败仍先发生。
// Expanding the profile does not move Original checks ahead of existing guards.
func TestLoadedOriginalASCIIExistingFailureOrder(t *testing.T) {
	newSource := func() FileSnapshotSource {
		return loadedSnapshotTestSource(t, loadedSnapshotTestRepeat('I', 40), loadedSnapshotTestRepeat('I', 32))
	}
	source := newSource()
	source.Reference.Path = Path{}
	source.Reference.Metadata = nil
	loadedSnapshotTestError(t, source, ErrInvalidPath)
	source = newSource()
	source.Reference.Metadata = nil
	loadedSnapshotTestError(t, source, ErrInvalidFileSnapshotSource)
	for slot := 0; slot < 3; slot++ {
		source = newSource()
		loadedSnapshotTestActual(&source, slot, "contradiction", false)
		loadedSnapshotTestError(t, source, ErrInvalidFileSnapshotSource)
		source = newSource()
		loadedSnapshotTestActual(&source, slot, string([]byte{0xff}), true)
		loadedSnapshotTestError(t, source, ErrInvalidFileSnapshotSource)
	}
	loadedSnapshotTestError(t, newSource(), ErrUnsupportedLoadedOriginal)
}

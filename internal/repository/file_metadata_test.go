package repository_test

import (
	"math"
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/repository"
)

// 零值快照：三个时间为 0、两个 Actor 为 null、两个 original 为 nil。
// The zero snapshot: zero times, null actors and nil originals.
func TestFileMetadataZeroValue(t *testing.T) {
	var zero repository.FileMetadata
	if zero.CreatedMillis() != 0 || zero.ModifiedMillis() != 0 || zero.UpdatedMillis() != 0 {
		t.Errorf("zero snapshot times = %d/%d/%d, want 0/0/0",
			zero.CreatedMillis(), zero.ModifiedMillis(), zero.UpdatedMillis())
	}
	if !zero.CreatedBy().IsNull() || !zero.ModifiedBy().IsNull() {
		t.Error("zero snapshot actors are not null")
	}
	if zero.SHA1OriginalUTF16() != nil || zero.MD5OriginalUTF16() != nil {
		t.Errorf("zero snapshot originals = %v/%v, want nil/nil",
			zero.SHA1OriginalUTF16(), zero.MD5OriginalUTF16())
	}
}

// 三个时间完整 signed int64 范围逐字复制且相互独立：负数、0、±2^53 邻域
// 与 long 极值都原样保留，不排序、不强迫相等、不取当前时间。
// Times copy verbatim and independently across the full signed int64 range:
// negatives, zero, the ±2^53 neighborhood and the long extremes survive
// with no ordering, equality or clock substitution.
func TestFileMetadataTimesFullSignedRange(t *testing.T) {
	p53 := int64(1) << 53
	triples := [][3]int64{
		{math.MinInt64, math.MaxInt64, 0},
		{math.MaxInt64, math.MinInt64, -1},
		{-1, 0, 1},
		{1, -1, 0},
		{p53 - 1, p53, p53 + 1},
		{-p53 + 1, -p53, -p53 - 1},
		{0, 0, math.MinInt64},
		{100, 100, 2},
		{-7, 0, 3},
	}
	for i, tr := range triples {
		m := repository.NewFileMetadata(repository.FileMetadataInput{
			CreatedMillis:  tr[0],
			ModifiedMillis: tr[1],
			UpdatedMillis:  tr[2],
		})
		if m.CreatedMillis() != tr[0] || m.ModifiedMillis() != tr[1] || m.UpdatedMillis() != tr[2] {
			t.Errorf("triple %d times = %d/%d/%d, want %d/%d/%d verbatim", i,
				m.CreatedMillis(), m.ModifiedMillis(), m.UpdatedMillis(), tr[0], tr[1], tr[2])
		}
	}
}

// Actor 组合：输入 Actor 值（null、空、原样、缩写）按值保存并返回，无额
// 外截断或转换；期望用测试内独立构造，不镜像生产常量。
// Actor composition: input Actor values (null, empty, verbatim, abbreviated)
// are stored and returned by value with no extra truncation or conversion;
// expectations are built independently here, never from production constants.
func TestFileMetadataActorComposition(t *testing.T) {
	longUnits := join(fill(80, 'Q'), []uint16{0x4e2d, 0x6587})
	cases := []struct {
		name string
		raw  []uint16
	}{
		{"null", nil},
		{"empty", []uint16{}},
		{"verbatim 64", fill(64, 'A')},
		{"abbreviated above 64", longUnits},
	}
	for _, tc := range cases {
		m := repository.NewFileMetadata(repository.FileMetadataInput{
			CreatedBy:  repository.NewActor(tc.raw),
			ModifiedBy: repository.NewActor(tc.raw),
		})
		for name, actor := range map[string]repository.Actor{
			"CreatedBy":  m.CreatedBy(),
			"ModifiedBy": m.ModifiedBy(),
		} {
			want := repository.NewActor(tc.raw)
			if actor.IsNull() != want.IsNull() {
				t.Errorf("%s/%s: IsNull() = %v, want %v", tc.name, name, actor.IsNull(), want.IsNull())
				continue
			}
			if got := actor.UTF16(); !equalUnits(got, want.UTF16()) {
				t.Errorf("%s/%s: UTF16() = %04x, want %04x", tc.name, name, got, want.UTF16())
			}
		}
	}
	// 缩写形状的独立期望：首 61 码元加三个句点，确认组合后仍是该形状。
	// Independent abbreviation expectation: first 61 units plus three dots.
	m := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedBy: repository.NewActor(longUnits),
	})
	if got := m.CreatedBy().UTF16(); !equalUnits(got, join(fill(61, 'Q'), dots())) {
		t.Errorf("abbreviated actor through FileMetadata = %04x, want 61 units plus dots", got)
	}
}

// original 的 null 与非 nil 空互不相同：nil 输入返回 nil，空输入返回非 nil
// 零长度；大容量空子切片也不保留后备容量。
// Originals keep null and nonnil empty distinct: nil returns nil, empty
// returns a nonnil zero length; even a large-capacity empty subslice
// retains no backing capacity.
func TestFileMetadataOriginalNullEmptyFidelity(t *testing.T) {
	largeCapEmpty := make([]uint16, 0, 4096)
	m := repository.NewFileMetadata(repository.FileMetadataInput{
		SHA1Original: largeCapEmpty,
		MD5Original:  nil,
	})
	if got := m.SHA1OriginalUTF16(); got == nil {
		t.Error("SHA1OriginalUTF16() = nil for nonnil empty input, want nonnil")
	} else if len(got) != 0 {
		t.Errorf("SHA1OriginalUTF16() length = %d, want 0", len(got))
	} else if cap(got) != 0 {
		t.Errorf("SHA1OriginalUTF16() capacity = %d, want 0 (no retained backing store)", cap(got))
	}
	if got := m.MD5OriginalUTF16(); got != nil {
		t.Errorf("MD5OriginalUTF16() = %v, want nil", got)
	}

	largeCapEmpty = append(largeCapEmpty, 0xffff)
	if got := m.SHA1OriginalUTF16(); got == nil || len(got) != 0 {
		t.Errorf("after caller append SHA1OriginalUTF16() = %v, want nonnil empty", got)
	}
}

// original 逐码元保留全部原始 UTF-16 码元：大小写、NO_ORIG 标记、空白、
// 组合码元、NUL、孤立/反序/重复/有效代理、全部 65536 单码元与超长值。
// Originals keep every raw UTF-16 unit verbatim: case, the NO_ORIG marker,
// whitespace, combining units, NUL, lone/reversed/repeated/valid surrogates,
// all 65536 single units and long values.
// ascii 把 ASCII 字面量逐码元转为 UTF-16 切片（测试期望的独立构造）。
// ascii turns an ASCII literal into a UTF-16 unit slice (an independent
// test-side construction of the expected value).
func ascii(s string) []uint16 {
	out := make([]uint16, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = uint16(s[i])
	}
	return out
}

func TestFileMetadataOriginalsVerbatim(t *testing.T) {
	allUnits := make([]uint16, 65536)
	for i := range allUnits {
		allUnits[i] = uint16(i)
	}
	shapes := [][]uint16{
		ascii("NO_ORIG"),
		join(fill(39, 'F'), []uint16{'0'}),
		join(fill(31, 'a'), []uint16{'9', 'B', 'c', 'D', 'e', 'F', '0', '1'}),
		[]uint16{' ', 0x0009, 0x0000, 0x0301, 0x00a0},
		join(fill(30, 'x'), []uint16{0xd83d}),
		join(fill(30, 'x'), []uint16{0xde00}),
		join(fill(30, 'x'), []uint16{0xde00, 0xd83d}),
		join(fill(30, 'x'), []uint16{0xd83d, 0xd83d}),
		join(fill(30, 'x'), []uint16{0xd83d, 0xde00}, fill(9, 'z')),
		allUnits,
		join(allUnits, fill(1, 0x0000), allUnits), // > 65536 码元 / units
	}
	for i, shape := range shapes {
		m := repository.NewFileMetadata(repository.FileMetadataInput{
			SHA1Original: shape,
			MD5Original:  shape,
		})
		if got := m.SHA1OriginalUTF16(); !equalUnits(got, shape) {
			t.Errorf("shape %d: SHA1OriginalUTF16() = %04x (len %d), want verbatim %04x (len %d)",
				i, got, len(got), shape, len(shape))
		}
		if got := m.MD5OriginalUTF16(); !equalUnits(got, shape) {
			t.Errorf("shape %d: MD5OriginalUTF16() = %04x (len %d), want verbatim %04x (len %d)",
				i, got, len(got), shape, len(shape))
		}
	}
}

// original 不做 Actor 式 64 码元缩写：任意长度原样保留，本身长得像缩写
// 结果的输入也不被改写；也不做小写化或标记替换。
// Originals get no Actor-style 64-unit abbreviation: any length survives
// verbatim, an input already shaped like an abbreviation stays as-is, and
// there is no lowercasing or marker substitution.
func TestFileMetadataNoAbbreviation(t *testing.T) {
	shapes := [][]uint16{
		fill(65, 'A'),
		fill(64, 'A'),
		join(fill(61, 'A'), dots()), // 已是缩写形状，仍原样 / already abbreviated, kept verbatim
		join(fill(61, 'A'), dots(), fill(500, 'z')),
		fill(1000, 0x4e2d),
		join(fill(20, 'A'), ascii("NO_ORIG"), fill(20, 'B')),
	}
	for i, shape := range shapes {
		m := repository.NewFileMetadata(repository.FileMetadataInput{SHA1Original: shape})
		got := m.SHA1OriginalUTF16()
		if !equalUnits(got, shape) {
			head := got
			if len(head) > 8 {
				head = head[:8]
			}
			t.Errorf("shape %d: SHA1OriginalUTF16() = %04x..., want full verbatim len %d",
				i, head, len(shape))
		}
	}
}

// 所有权（输入侧）：构造不改输入；构造后修改输入切片、重赋输入结构体字
// 段都不改变快照。
// Ownership (input side): construction mutates no input; later input-slice
// writes or input-struct reassignment never change the snapshot.
func TestFileMetadataOwnershipInput(t *testing.T) {
	sha1 := fill(70, 'S')
	md5 := fill(80, 'M')
	before := join(sha1, md5)
	in := repository.FileMetadataInput{
		CreatedMillis:  -7,
		ModifiedMillis: 0,
		UpdatedMillis:  math.MaxInt64,
		CreatedBy:      repository.NewActor(fill(70, 'Q')),
		ModifiedBy:     repository.NewActor(nil),
		SHA1Original:   sha1,
		MD5Original:    md5,
	}
	m := repository.NewFileMetadata(in)
	if !equalUnits(join(sha1, md5), before) {
		t.Error("constructor changed the input slices")
	}

	for i := range sha1 {
		sha1[i] = 0x0000
	}
	in.SHA1Original = nil
	in.CreatedMillis = 12345
	in = repository.FileMetadataInput{}
	if got := m.SHA1OriginalUTF16(); !equalUnits(got, fill(70, 'S')) {
		t.Errorf("after input writes/reassignment SHA1OriginalUTF16() len = %d, want 70", len(got))
	}
	if m.CreatedMillis() != -7 || m.UpdatedMillis() != math.MaxInt64 {
		t.Errorf("after input reassignment times = %d/%d, want -7/%d",
			m.CreatedMillis(), m.UpdatedMillis(), math.MaxInt64)
	}
}

// 所有权（读取侧）：getter 返回防御性副本，两次读取互不别名；修改返回
// 切片不影响快照与值副本；非空副本不留多余容量。
// Ownership (read side): getters return defensive copies that never alias
// each other; mutating a returned slice changes neither the snapshot nor
// its value copies; nonempty copies retain no excess capacity.
func TestFileMetadataOwnershipGetterAndValueCopy(t *testing.T) {
	shape := join(fill(30, 'x'), []uint16{0xd83d, 0xde00}, fill(9, 'z'))
	m := repository.NewFileMetadata(repository.FileMetadataInput{
		SHA1Original: shape,
		MD5Original:  shape,
	})

	first := m.SHA1OriginalUTF16()
	if cap(first) != len(first) {
		t.Errorf("SHA1OriginalUTF16() capacity = %d, want exactly len %d", cap(first), len(first))
	}
	for i := range first {
		first[i] = 0xffff
	}
	if second := m.SHA1OriginalUTF16(); !equalUnits(second, shape) {
		t.Error("mutating a returned SHA1 copy changed the snapshot")
	}
	// 两个 original 字段的 getter 结果互不别名。
	// Getter results of the two original fields never alias each other.
	sha1Copy := m.SHA1OriginalUTF16()
	for i := range sha1Copy {
		sha1Copy[i] = 0x0000
	}
	if md5Copy := m.MD5OriginalUTF16(); !equalUnits(md5Copy, shape) {
		t.Error("mutating the SHA1 getter copy changed the MD5 view")
	}

	clone := m
	for i := range first {
		first[i] = 0x0000
	}
	if got := clone.SHA1OriginalUTF16(); !equalUnits(got, shape) {
		t.Error("value copy changed after mutating the original's returned slice")
	}
	if got := m.SHA1OriginalUTF16(); !equalUnits(got, shape) {
		t.Error("original changed after value-copy reads")
	}
	if clone.CreatedMillis() != m.CreatedMillis() || !clone.ModifiedBy().IsNull() {
		t.Error("value copy lost times or actor state")
	}
}

// 两个 original 互为同一数组的重叠子切片时也完全隔离：外部写任一区域都
// 不能改变快照。
// Overlapping subslices of one backing array stay fully isolated: external
// writes to any region never change the snapshot.
func TestFileMetadataOverlappingFields(t *testing.T) {
	backing := join(fill(40, 'S'), fill(40, 'M'), fill(40, 'D'))
	sha1 := backing[20:60] // 'S'×20 + 'M'×20
	md5 := backing[40:100] // 'M'×40 + 'D'×20（与 sha1 重叠 20 个 'M'）
	wantSHA1 := join(fill(20, 'S'), fill(20, 'M'))
	wantMD5 := join(fill(40, 'M'), fill(20, 'D'))
	m := repository.NewFileMetadata(repository.FileMetadataInput{
		SHA1Original: sha1,
		MD5Original:  md5,
	})
	for i := range backing {
		backing[i] = 0x0000
	}
	if got := m.SHA1OriginalUTF16(); !equalUnits(got, wantSHA1) {
		t.Errorf("after backing writes SHA1OriginalUTF16() len = %d, want %d", len(got), len(wantSHA1))
	}
	if got := m.MD5OriginalUTF16(); !equalUnits(got, wantMD5) {
		t.Errorf("after backing writes MD5OriginalUTF16() len = %d, want %d", len(got), len(wantMD5))
	}
}

// 并发读者各自修改自己拿到的副本，互不影响既有快照；从不并发写构造输入
// （该行为未被承诺）。
// Concurrent readers mutate only their own returned copies without affecting
// the shared snapshot; a constructor is never raced against caller writes
// (not promised).
func TestFileMetadataConcurrentReaders(t *testing.T) {
	shape := join(fill(30, 'x'), []uint16{0xd83d}, fill(63, 'y'), []uint16{0xde00}, fill(31, 'z'))
	m := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  math.MinInt64,
		ModifiedMillis: -1,
		UpdatedMillis:  math.MaxInt64,
		CreatedBy:      repository.NewActor(fill(70, 'Q')),
		ModifiedBy:     repository.NewActor([]uint16{}),
		SHA1Original:   shape,
		MD5Original:    shape,
	})
	wantActor := join(fill(61, 'Q'), dots())

	const readers = 8
	const rounds = 25
	var wg sync.WaitGroup
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				sha1 := m.SHA1OriginalUTF16()
				md5 := m.MD5OriginalUTF16()
				for j := range sha1 {
					sha1[j] = uint16(r + i + j)
				}
				for j := range md5 {
					md5[j] = uint16(r + i + j)
				}
				if fresh := m.SHA1OriginalUTF16(); !equalUnits(fresh, shape) {
					t.Errorf("reader %d round %d: SHA1 len changed to %d", r, i, len(fresh))
					return
				}
				if fresh := m.MD5OriginalUTF16(); !equalUnits(fresh, shape) {
					t.Errorf("reader %d round %d: MD5 len changed to %d", r, i, len(fresh))
					return
				}
				if m.CreatedMillis() != math.MinInt64 || m.UpdatedMillis() != math.MaxInt64 {
					t.Errorf("reader %d round %d: times changed", r, i)
					return
				}
				if got := m.CreatedBy().UTF16(); !equalUnits(got, wantActor) {
					t.Errorf("reader %d round %d: actor len changed to %d", r, i, len(got))
					return
				}
				if m.ModifiedBy().IsNull() {
					t.Errorf("reader %d round %d: empty actor became null", r, i)
					return
				}
			}
		}(r)
	}
	wg.Wait()
}

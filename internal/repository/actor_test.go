package repository_test

import (
	"sync"
	"testing"

	"github.com/0ldlight/bf/internal/repository"
)

// null 与空串在模型边界上不同：nil 输入是 null（零 Actor 同样是 null），
// 非 nil 空切片是 present-empty；UTF16 对 null 返回 nil、对空返回非 nil
// 零长度。
// Null and empty differ at the model boundary: nil input is null (the zero
// Actor is null too), a nonnil empty slice is present-empty; UTF16 returns
// nil for null and a nonnil zero length for empty.
func TestActorNullEmptyZero(t *testing.T) {
	var zero repository.Actor
	if !zero.IsNull() {
		t.Error("zero Actor IsNull() = false, want true")
	}
	if got := zero.UTF16(); got != nil {
		t.Errorf("zero Actor UTF16() = %v, want nil", got)
	}

	null := repository.NewActor(nil)
	if !null.IsNull() {
		t.Error("NewActor(nil) IsNull() = false, want true")
	}
	if got := null.UTF16(); got != nil {
		t.Errorf("NewActor(nil) UTF16() = %v, want nil", got)
	}

	empty := repository.NewActor([]uint16{})
	if empty.IsNull() {
		t.Error("NewActor(nonnil empty) IsNull() = true, want false")
	}
	got := empty.UTF16()
	if got == nil {
		t.Error("NewActor(nonnil empty) UTF16() = nil, want nonnil")
	}
	if len(got) != 0 {
		t.Errorf("NewActor(nonnil empty) UTF16() length = %d, want 0", len(got))
	}
}

// 构造器不得保留调用方可变输入，也不得改动输入；返回切片是防御性副本，
// 修改它不影响既有 Actor；值副本同样稳定。
// The constructor retains no caller-mutable input and never mutates the
// input; UTF16 returns a defensive copy whose mutation cannot change an
// existing Actor; value copies are equally stable.
func TestActorOwnershipBasics(t *testing.T) {
	input := []uint16{'a', 'b', 'c'}
	before := []uint16{'a', 'b', 'c'}
	actor := repository.NewActor(input)
	want := []uint16{'a', 'b', 'c'}

	if !equalUnits(input, before) {
		t.Errorf("constructor changed input to %04x, original %04x", input, before)
	}

	input[0] = 0xFFFF
	if got := actor.UTF16(); !equalUnits(got, want) {
		t.Errorf("after input mutation UTF16() = %04x, want %04x", got, want)
	}

	got := actor.UTF16()
	got[1] = 0x0000
	if got = actor.UTF16(); !equalUnits(got, want) {
		t.Errorf("after mutating returned slice UTF16() = %04x, want %04x", got, want)
	}

	clone := actor
	input[2] = 0x0000
	if got = clone.UTF16(); !equalUnits(got, want) {
		t.Errorf("value copy after input mutation UTF16() = %04x, want %04x", got, want)
	}
	if got = actor.UTF16(); !equalUnits(got, want) {
		t.Errorf("original after value-copy reads UTF16() = %04x, want %04x", got, want)
	}
}

func equalUnits(got, want []uint16) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// fill 构造 n 个同一码元的切片。
// fill builds a slice of n copies of one unit.
func fill(n int, unit uint16) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = unit
	}
	return out
}

// dots 是缩写后缀的独立字面量，不复用生产常量。
// dots is an independent literal for the abbreviation suffix; production
// constants are never mirrored.
func dots() []uint16 { return []uint16{0x002e, 0x002e, 0x002e} }

func join(parts ...[]uint16) []uint16 {
	var out []uint16
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// 边界 63/64 原样保留：ASCII、BMP、NUL、空白、组合码元、孤立高低代理、
// 反序与重复代理一律不做 trim/归一化/替换。
// The 63/64 boundary keeps units verbatim: ASCII, BMP, NUL, whitespace,
// combining units, lone high/low surrogates, reversed and repeated
// surrogates all pass with no trim/normalization/substitution.
func TestActorWithinBoundPreserved(t *testing.T) {
	shapes := [][]uint16{
		fill(63, 'A'),
		fill(64, 'A'),
		fill(64, 0x4e2d),
		join(fill(62, 'x'), []uint16{0x0000}),
		join(fill(62, 'x'), []uint16{0x0020, 0x0009}),
		join(fill(62, 'x'), []uint16{0x0301}),
		join(fill(63, 'x'), []uint16{0xd83d}),
		join(fill(63, 'x'), []uint16{0xde00}),
		join(fill(62, 'x'), []uint16{0xde00, 0xd83d}),
		join(fill(62, 'x'), []uint16{0xd83d, 0xd83d}),
	}
	for i, shape := range shapes {
		actor := repository.NewActor(shape)
		if actor.IsNull() {
			t.Fatalf("shape %d: IsNull() = true, want false", i)
		}
		if got := actor.UTF16(); !equalUnits(got, shape) {
			t.Errorf("shape %d: UTF16() = %04x, want verbatim %04x", i, got, shape)
		}
	}
}

// 超过 64 个 UTF-16 码元缩写为首 61 加三个 0x002e；代理对跨界拆分时按
// 原始码元规则保留孤立码元；有效对整体落在保留区内则完整保留。
// Above 64 UTF-16 units the value keeps the first 61 plus three 0x002e; a
// surrogate pair split at the boundary leaves a lone unit by the raw-unit
// rule; a pair fully inside the retained prefix survives whole.
func TestActorAbbreviation(t *testing.T) {
	cases := []struct {
		name  string
		input []uint16
		want  []uint16
	}{
		{"65 ascii", fill(65, 'A'), join(fill(61, 'A'), dots())},
		{"65 bmp", fill(65, 0x4e2d), join(fill(61, 0x4e2d), dots())},
		{
			"split pair at boundary",
			join(fill(60, 'A'), []uint16{0xd83d, 0xde00}, fill(10, 'z')),
			join(fill(60, 'A'), []uint16{0xd83d}, dots()),
		},
		{
			"full pair inside prefix",
			join(fill(59, 'A'), []uint16{0xd83d, 0xde00}, fill(12, 'z')),
			join(fill(59, 'A'), []uint16{0xd83d, 0xde00}, dots()),
		},
		{
			"lone low inside prefix then tail",
			join(fill(60, 'A'), []uint16{0xde00}, fill(6, 'z')),
			join(fill(60, 'A'), []uint16{0xde00}, dots()),
		},
		{
			"unit past the prefix is dropped",
			join(fill(61, 'A'), []uint16{0xde00}, fill(5, 'z')),
			join(fill(61, 'A'), dots()),
		},
		{
			"nul units truncated",
			join(fill(70, 0x0000)),
			join(fill(61, 0x0000), dots()),
		},
	}
	for _, tc := range cases {
		actor := repository.NewActor(tc.input)
		if actor.IsNull() {
			t.Fatalf("%s: IsNull() = true, want false", tc.name)
		}
		if got := actor.UTF16(); !equalUnits(got, tc.want) {
			t.Errorf("%s: UTF16() = %04x, want %04x", tc.name, got, tc.want)
		}
	}
}

// 缩写输入同样满足所有权：构造不改输入、不保留可变后备存储、返回副本可
// 独立修改、值副本稳定。
// Truncated inputs keep the ownership rules too: construction changes no
// input, retains no mutable backing store, returned copies are independently
// mutable, and value copies stay stable.
func TestActorTruncatedOwnership(t *testing.T) {
	input := fill(200, 'Q')
	before := fill(200, 'Q')
	actor := repository.NewActor(input)
	want := join(fill(61, 'Q'), dots())

	if !equalUnits(input, before) {
		t.Error("constructor changed the truncated input")
	}
	for i := range input {
		input[i] = 0xffff
	}
	if got := actor.UTF16(); !equalUnits(got, want) {
		t.Errorf("after input mutation UTF16() = %04x, want %04x", got, want)
	}
	got := actor.UTF16()
	for i := range got {
		got[i] = 0x0000
	}
	if got = actor.UTF16(); !equalUnits(got, want) {
		t.Errorf("after mutating returned slice UTF16() = %04x, want %04x", got, want)
	}
	clone := actor
	if got = clone.UTF16(); !equalUnits(got, want) {
		t.Errorf("value copy UTF16() = %04x, want %04x", got, want)
	}

	emptyIn := []uint16{}
	empty := repository.NewActor(emptyIn)
	emptyIn = append(emptyIn, 0xffff)
	if empty.IsNull() || len(empty.UTF16()) != 0 {
		t.Error("empty actor changed after caller append to the original slice header storage")
	}
}

// 大输入产生有界输出：无论输入多大，Actor 保留的码元不超过 64；多次
// 读取互不影响。
// Large inputs yield bounded output: whatever the input size, an Actor
// retains at most 64 units; repeated reads stay independent.
func TestActorBoundedOutput(t *testing.T) {
	input := fill(100000, 0x0051)
	want := join(fill(61, 0x0051), dots())
	actor := repository.NewActor(input)
	first := actor.UTF16()
	if len(first) > 64 {
		t.Errorf("UTF16() length = %d, want <= 64", len(first))
	}
	if !equalUnits(first, want) {
		t.Errorf("UTF16() = %04x..., want 61 units plus dots", first)
	}
	first[0] = 0x0000
	if second := actor.UTF16(); !equalUnits(second, want) {
		t.Error("second UTF16() read affected by mutating the first returned slice")
	}
}

// 并发读者各自修改自己拿到的副本，互不影响既有 Actor；从不并发写构造
// 输入（该行为未被承诺）。
// Concurrent readers mutate only their own returned copies without affecting
// the shared Actor; a constructor is never raced against caller writes (not
// promised).
func TestActorConcurrentReaders(t *testing.T) {
	input := join(fill(60, 'A'), []uint16{0xd83d, 0xde00}, fill(9, 'z'))
	actor := repository.NewActor(input)
	want := join(fill(60, 'A'), []uint16{0xd83d}, dots())

	const readers = 8
	const rounds = 25
	var wg sync.WaitGroup
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				got := actor.UTF16()
				for j := range got {
					got[j] = uint16(r + i + j)
				}
				if fresh := actor.UTF16(); !equalUnits(fresh, want) {
					t.Errorf("reader %d round %d observed %04x, want %04x", r, i, fresh, want)
					return
				}
			}
		}(r)
	}
	wg.Wait()
	if got := actor.UTF16(); !equalUnits(got, want) {
		t.Errorf("final UTF16() = %04x, want %04x", got, want)
	}
}

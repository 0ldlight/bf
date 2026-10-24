// file_snapshot_metadata_test.go — FileInfoSnapshot.NodeMetadata 行为测试。
// 七字段投影契约：三时间逐字、双 actor 走既有 64 码元缩写、SHA1/MD5
// original 逐字（缺席类型→nil 为本地扩域；present 条目不受 actual 空白
// 影响）；SHA256 original/MIME/身份/size/actual 与条目次序不进投影。
// 所有期望独立声明，绝不用被测实现或 NewActor 计算。
// Behavioral tests for FileInfoSnapshot.NodeMetadata. Seven-field
// projection contract: three times verbatim, both actors through the
// existing 64-unit abbreviation, SHA1/MD5 originals verbatim (absent
// kind -> nil is a local extension; a present entry ignores actual
// blankness); SHA256 original/MIME/identity/size/actuals and entry order
// never enter the projection. Every expectation is declared
// independently, never computed via the implementation or NewActor.
package repository

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

// 输入/期望构造助手，全部独立于被测代码。/ Input and expectation
// helpers, independent of the code under test. Every factory call
// allocates a fresh slice; tests never share mutable fixture state.
func bin170ASCII(s string) []uint16 {
	u := make([]uint16, 0, len(s))
	for _, r := range s {
		u = append(u, uint16(r)) // 仅 BMP 字面量；代理对用显式单元。
	}
	return u
}

func bin170RepF(u uint16, n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = u
	}
	return out
}

// bin170DotsAfter 是测试侧独立缩写构造：首 61 码元 + 三个句点。
// bin170DotsAfter is a test-side abbreviation construct: the first 61
// units plus three dots. Always allocates a fresh slice.
func bin170DotsAfter(u []uint16) []uint16 {
	return append(append([]uint16{}, u[:61]...), 0x2e, 0x2e, 0x2e)
}

// 以下夹具全部为函数：每次调用返回全新独占切片，杜绝全局可变别名。
// The fixtures below are functions: every call returns a fresh exclusive
// slice, eliminating mutable global aliasing.
func bin170A40() []uint16    { return bin170RepF('A', 40) }
func bin170B32() []uint16    { return bin170RepF('b', 32) }
func bin170NoOrig() []uint16 { return bin170ASCII("NO_ORIG") }

// bin170Want：独立声明的七字段期望。/ The independently declared
// seven-field expectation.
type bin170Want struct {
	created, modified, updated int64
	createdBy, modifiedBy      []uint16
	sha1Orig, md5Orig          []uint16
}

// bin170EqU 比较 UTF-16 码元，nil 与非 nil 空互异。
// bin170EqU compares UTF-16 units; nil and nonnil empty differ.
func bin170EqU(a, b []uint16) bool { return reflect.DeepEqual(a, b) }

func bin170ActorEq(a Actor, want []uint16) bool {
	if want == nil {
		return a.IsNull()
	}
	return !a.IsNull() && bin170EqU(a.UTF16(), want)
}

// bin170CheckMeta 以公共 getter 核对全部七字段。
// bin170CheckMeta verifies all seven fields via public getters.
func bin170CheckMeta(t *testing.T, m FileMetadata, w bin170Want) {
	t.Helper()
	if m.CreatedMillis() != w.created || m.ModifiedMillis() != w.modified || m.UpdatedMillis() != w.updated {
		t.Errorf("times got %d/%d/%d want %d/%d/%d",
			m.CreatedMillis(), m.ModifiedMillis(), m.UpdatedMillis(), w.created, w.modified, w.updated)
	}
	if !bin170ActorEq(m.CreatedBy(), w.createdBy) {
		t.Errorf("createdBy got %v want %v", m.CreatedBy().UTF16(), w.createdBy)
	}
	if !bin170ActorEq(m.ModifiedBy(), w.modifiedBy) {
		t.Errorf("modifiedBy got %v want %v", m.ModifiedBy().UTF16(), w.modifiedBy)
	}
	if !bin170EqU(m.SHA1OriginalUTF16(), w.sha1Orig) {
		t.Errorf("sha1Original got %v want %v", m.SHA1OriginalUTF16(), w.sha1Orig)
	}
	if !bin170EqU(m.MD5OriginalUTF16(), w.md5Orig) {
		t.Errorf("md5Original got %v want %v", m.MD5OriginalUTF16(), w.md5Orig)
	}
}

// bin170Snap：默认 rk/a/b/nm、100/200/300、size 42、MIME nil 的输入骨架。
// bin170Snap: default input scaffold rk/a/b/nm, 100/200/300, size 42, nil
// MIME. Shared slices passed by callers are deep-copied on construction.
func bin170Snap(cby, mby []uint16, cks ...ChecksumEntryInput) FileInfoSnapshotInput {
	return FileInfoSnapshotInput{
		RepoKeyUTF16: bin170ASCII("rk"), RepoPathUTF16: bin170ASCII("a/b"), NameUTF16: bin170ASCII("nm"),
		CreatedMillis: 100, LastModifiedMillis: 200, LastUpdatedMillis: 300,
		CreatedByUTF16: cby, ModifiedByUTF16: mby, SizeBytes: 42, MimeTypeUTF16: nil, Checksums: cks,
	}
}

func bin170S1(actual, original []uint16) ChecksumEntryInput {
	return ChecksumEntryInput{Kind: ChecksumSHA1, Actual: actual, Original: original}
}

func bin170S2(actual, original []uint16) ChecksumEntryInput {
	return ChecksumEntryInput{Kind: ChecksumMD5, Actual: actual, Original: original}
}

func bin170S256X(original []uint16) ChecksumEntryInput {
	return ChecksumEntryInput{Kind: ChecksumSHA256, Actual: bin170RepF('D', 64), Original: original}
}

// bin170WantDefault：默认输入（actor nil、MD5 缺席）的期望。
// bin170WantDefault: want for the default input (nil actors, absent MD5).
func bin170WantDefault(sha1 []uint16) bin170Want {
	return bin170Want{100, 200, 300, nil, nil, sha1, nil}
}

func bin170MustSnap(t *testing.T, in FileInfoSnapshotInput) FileInfoSnapshot {
	t.Helper()
	s, err := NewFileInfoSnapshot(in)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	return s
}

// bin170Mutate 把切片全部码元改为 0xFFFF（nil 安全）。
// bin170Mutate overwrites every unit with 0xFFFF; nil-safe.
func bin170Mutate(u []uint16) {
	for i := range u {
		u[i] = 0xFFFF
	}
}

// bin170CheckRaw 逐字段核对完整 Fields() 原始状态：保留 nil/空差异；
// 校验和按 Kind 比较数量、重复、未知、Actual 与 Original，无次序断言。
// bin170CheckRaw verifies the full Fields() raw state field by field,
// keeping nil/empty distinctions; checksums are compared by kind for
// count, duplicates, unknown kinds, Actual and Original with no order
// assertions.
func bin170CheckRaw(t *testing.T, got, exp FileInfoSnapshotInput) {
	t.Helper()
	if !bin170EqU(got.RepoKeyUTF16, exp.RepoKeyUTF16) ||
		!bin170EqU(got.RepoPathUTF16, exp.RepoPathUTF16) || !bin170EqU(got.NameUTF16, exp.NameUTF16) {
		t.Errorf("identity got %v/%v/%v want %v/%v/%v",
			got.RepoKeyUTF16, got.RepoPathUTF16, got.NameUTF16, exp.RepoKeyUTF16, exp.RepoPathUTF16, exp.NameUTF16)
	}
	if got.CreatedMillis != exp.CreatedMillis || got.LastModifiedMillis != exp.LastModifiedMillis ||
		got.LastUpdatedMillis != exp.LastUpdatedMillis {
		t.Errorf("times got %d/%d/%d want %d/%d/%d", got.CreatedMillis, got.LastModifiedMillis,
			got.LastUpdatedMillis, exp.CreatedMillis, exp.LastModifiedMillis, exp.LastUpdatedMillis)
	}
	if !bin170EqU(got.CreatedByUTF16, exp.CreatedByUTF16) || !bin170EqU(got.ModifiedByUTF16, exp.ModifiedByUTF16) {
		t.Errorf("actors got %v/%v want %v/%v",
			got.CreatedByUTF16, got.ModifiedByUTF16, exp.CreatedByUTF16, exp.ModifiedByUTF16)
	}
	if got.SizeBytes != exp.SizeBytes {
		t.Errorf("size got %d want %d", got.SizeBytes, exp.SizeBytes)
	}
	if !bin170EqU(got.MimeTypeUTF16, exp.MimeTypeUTF16) {
		t.Errorf("mime got %v want %v", got.MimeTypeUTF16, exp.MimeTypeUTF16)
	}
	gotCK, dupCK := map[ChecksumKind]ChecksumEntryInput{}, map[ChecksumKind]bool{}
	for _, c := range got.Checksums {
		if _, ok := gotCK[c.Kind]; ok {
			dupCK[c.Kind] = true
		}
		gotCK[c.Kind] = c
	}
	for k := range dupCK {
		t.Errorf("duplicate checksum kind %d", k)
	}
	if len(gotCK) != len(exp.Checksums) {
		t.Errorf("checksum kind count got %d want %d", len(gotCK), len(exp.Checksums))
	}
	for _, w := range exp.Checksums {
		g, ok := gotCK[w.Kind]
		if !ok {
			t.Errorf("missing checksum kind %d", w.Kind)
			continue
		}
		if !bin170EqU(g.Actual, w.Actual) || !bin170EqU(g.Original, w.Original) {
			t.Errorf("checksum kind %d got actual=%v original=%v want actual=%v original=%v",
				w.Kind, g.Actual, g.Original, w.Actual, w.Original)
		}
	}
}

func TestBIN170ZeroSnapshotReturnsExistingError(t *testing.T) {
	// 零值快照 -> 零 FileMetadata + 既有哨兵。
	// Zero snapshot -> zero FileMetadata + the existing sentinel.
	var s FileInfoSnapshot
	m, err := s.NodeMetadata()
	if !errors.Is(err, ErrInvalidFileInfoSnapshot) {
		t.Errorf("zero snapshot: err=%v want ErrInvalidFileInfoSnapshot", err)
	}
	if m.CreatedMillis() != 0 || m.ModifiedMillis() != 0 || m.UpdatedMillis() != 0 ||
		!m.CreatedBy().IsNull() || !m.ModifiedBy().IsNull() ||
		m.SHA1OriginalUTF16() != nil || m.MD5OriginalUTF16() != nil {
		t.Errorf("zero snapshot: metadata must be the zero value")
	}
}

func TestBIN170ValidEmptyStatesSucceed(t *testing.T) {
	// 三个必填字段非 nil 空、三时间与 size 全 0、可空字段 nil、空校验和
	// 图（nil 或非 nil 空）均为有效快照：成功投影；全空输入投影零样七
	// 字段，这与零值快照的既有错误不同。
	// Required-three nonnil empty, all-zero times and size, nil nullable
	// fields and an empty checksum map (nil or nonnil empty) are valid
	// snapshots that project successfully; the all-empty input projects
	// the zero seven fields, distinct from the zero-snapshot error.
	rows := []struct {
		name string
		in   FileInfoSnapshotInput
		want bin170Want
	}{
		{"nil checksums", bin170Snap(nil, nil), bin170WantDefault(nil)},
		{"empty checksum map", bin170Snap(nil, nil, []ChecksumEntryInput{}...), bin170WantDefault(nil)},
		{"required empty all zero", FileInfoSnapshotInput{
			RepoKeyUTF16: []uint16{}, RepoPathUTF16: []uint16{}, NameUTF16: []uint16{},
			Checksums: []ChecksumEntryInput{},
		}, bin170Want{0, 0, 0, nil, nil, nil, nil}},
	}
	for _, r := range rows {
		m, err := bin170MustSnap(t, r.in).NodeMetadata()
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		bin170CheckMeta(t, m, r.want)
	}
}

func TestBIN170TimesVerbatimExactInt64(t *testing.T) {
	// 三时间逐字独立：极值/负/零/2^53 邻域精确回读，无浮点。
	// Three times verbatim, independent: extremes/negatives/zero and the
	// 2^53 neighborhood with exact integer readback, no floats.
	rows := [][3]int64{
		{-9223372036854775808, 9223372036854775807, 0},
		{9223372036854775807, -9223372036854775808, -1},
		{-7, 0, 3},
		{9007199254740991, 9007199254740993, -9007199254740993},
		{0, 0, 0},
		{1, -1, 9223372036854775807},
	}
	for _, r := range rows {
		in := bin170Snap(nil, nil, bin170S1(bin170A40(), nil))
		in.CreatedMillis, in.LastModifiedMillis, in.LastUpdatedMillis = r[0], r[1], r[2]
		m, err := bin170MustSnap(t, in).NodeMetadata()
		if err != nil {
			t.Fatalf("row %v: %v", r, err)
		}
		bin170CheckMeta(t, m, bin170Want{r[0], r[1], r[2], nil, nil, nil, nil})
	}
}

func TestBIN170ActorsRawThenExistingAbbreviation(t *testing.T) {
	// 两 actor 各自走既有 64 缩写：null/空及其交叉、64 原样、61+三点、
	// 跨界拆分保留孤立高位代理、孤立代理原样、BMP 按码元计数。
	// Both actors take the existing abbreviation independently: null and
	// empty with both cross combinations, 64 verbatim, 61+dots, a split
	// pair keeps the lone high unit, lone surrogates pass, BMP counts
	// code units.
	split := append(bin170RepF('x', 60), 0xD83D, 0xDE00, 'z', 'z', 'z')
	rows := []struct{ cIn, mIn, cWant, mWant []uint16 }{
		{nil, nil, nil, nil},
		{[]uint16{}, bin170ASCII("mb"), []uint16{}, bin170ASCII("mb")},
		{nil, []uint16{}, nil, []uint16{}},
		{[]uint16{}, nil, []uint16{}, nil},
		{bin170RepF('a', 64), bin170RepF('b', 65), bin170RepF('a', 64), bin170DotsAfter(bin170RepF('b', 65))},
		{bin170RepF('a', 65), bin170RepF('b', 64), bin170DotsAfter(bin170RepF('a', 65)), bin170RepF('b', 64)},
		{split, []uint16{0xD800}, append(bin170RepF('x', 60), 0xD83D, 0x2e, 0x2e, 0x2e), []uint16{0xD800}},
		{bin170RepF(0x4E2D, 65), bin170RepF(0x4E2D, 33), bin170DotsAfter(bin170RepF(0x4E2D, 65)), bin170RepF(0x4E2D, 33)},
	}
	for _, r := range rows {
		m, err := bin170MustSnap(t, bin170Snap(r.cIn, r.mIn, bin170S1(bin170A40(), nil))).NodeMetadata()
		if err != nil {
			t.Fatalf("row: %v", err)
		}
		bin170CheckMeta(t, m, bin170Want{100, 200, 300, r.cWant, r.mWant, nil, nil})
	}
}

func TestBIN170OriginalsVerbatimAllVariants(t *testing.T) {
	// SHA1/MD5 Original 逐字：null、空、NO_ORIG、原始大小写、任意码元
	//（NUL/孤立低代理）与 >64 长度一律不缩写；缺席的另一类型为 nil。
	// SHA1/MD5 originals verbatim: null, empty, NO_ORIG, raw case,
	// arbitrary units (NUL/lone low surrogate) and >64 lengths never
	// abbreviate; the other absent kind projects nil.
	mixed70 := append(append(bin170RepF('o', 60), 0xDC00, 0x0000), bin170RepF('o', 8)...)
	mixed65 := append(append(bin170RepF('m', 61), 0xD83D), bin170RepF('m', 3)...)
	for name, orig := range map[string][]uint16{
		"nil": nil, "empty": []uint16{}, "noOrig": bin170NoOrig(),
		"rawUpper64": bin170RepF('X', 64), "rawUpper40": bin170RepF('U', 40),
		"mixed70": mixed70, "mixed65": mixed65,
	} {
		s1 := bin170MustSnap(t, bin170Snap(nil, nil, bin170S1(bin170A40(), orig)))
		if m, err := s1.NodeMetadata(); err != nil {
			t.Fatalf("%s: %v", name, err)
		} else {
			bin170CheckMeta(t, m, bin170WantDefault(orig))
		}
		// MD5 存在、SHA1 缺席（本地扩域）。/ MD5 present, SHA1 absent.
		s2 := bin170MustSnap(t, bin170Snap(nil, nil, bin170S2(bin170B32(), orig)))
		if m, err := s2.NodeMetadata(); err != nil {
			t.Fatalf("%s md5: %v", name, err)
		} else {
			bin170CheckMeta(t, m, bin170Want{100, 200, 300, nil, nil, nil, orig})
		}
	}
}

func TestBIN170AbsentKindsProjectNil(t *testing.T) {
	// 缺席类型 -> 对应 original nil。/ Absent kind -> its original nil.
	cases := []struct {
		cks  []ChecksumEntryInput
		want bin170Want
	}{
		{[]ChecksumEntryInput{bin170S2(nil, bin170NoOrig())}, bin170Want{100, 200, 300, nil, nil, nil, bin170NoOrig()}},
		{nil, bin170WantDefault(nil)},
		{[]ChecksumEntryInput{bin170S1(bin170A40(), bin170NoOrig())}, bin170WantDefault(bin170NoOrig())},
	}
	for _, c := range cases {
		m, err := bin170MustSnap(t, bin170Snap(nil, nil, c.cks...)).NodeMetadata()
		if err != nil {
			t.Fatalf("case: %v", err)
		}
		bin170CheckMeta(t, m, c.want)
	}
}

func TestBIN170BlankActualStillKeepsOriginal(t *testing.T) {
	// 条目存在时 Actual null/空/空白不抑制其 Original（本地规则）。
	// A present entry keeps its original despite a null/empty/blank
	// actual (local rule).
	cases := []struct {
		cks             []ChecksumEntryInput
		sha1Got, md5Got []uint16
	}{
		{[]ChecksumEntryInput{bin170S1(nil, bin170NoOrig())}, bin170NoOrig(), nil},
		{[]ChecksumEntryInput{bin170S1([]uint16{}, bin170RepF('X', 64))}, bin170RepF('X', 64), nil},
		{[]ChecksumEntryInput{bin170S1(bin170ASCII(" "), bin170NoOrig())}, bin170NoOrig(), nil},
		{[]ChecksumEntryInput{bin170S1(bin170ASCII("\t"), []uint16{})}, []uint16{}, nil},
		{[]ChecksumEntryInput{bin170S2(nil, bin170NoOrig())}, nil, bin170NoOrig()},
		{[]ChecksumEntryInput{bin170S2(bin170ASCII(" "), bin170RepF('Y', 32))}, nil, bin170RepF('Y', 32)},
	}
	for _, c := range cases {
		m, err := bin170MustSnap(t, bin170Snap(nil, nil, c.cks...)).NodeMetadata()
		if err != nil {
			t.Fatalf("case: %v", err)
		}
		bin170CheckMeta(t, m, bin170Want{100, 200, 300, nil, nil, c.sha1Got, c.md5Got})
	}
}

func TestBIN170ExcludedFieldsAndOrderNeverEnter(t *testing.T) {
	// SHA256 original、MIME、身份、size、actual 与条目次序不进七字段。
	// SHA256 original, MIME, identity, size, actuals and entry order
	// never enter the seven fields.
	mk := func(flip bool) FileInfoSnapshotInput {
		s1 := bin170S1(bin170A40(), nil)
		s256 := bin170S256X(bin170RepF('X', 64))
		cks := []ChecksumEntryInput{s1, s256}
		if flip {
			cks = []ChecksumEntryInput{bin170S256X(bin170RepF('x', 64)), bin170S1(bin170RepF('C', 40), nil)}
		}
		in := bin170Snap(nil, nil, cks...)
		in.SizeBytes, in.MimeTypeUTF16 = 7, bin170ASCII("app")
		in.RepoKeyUTF16, in.RepoPathUTF16, in.NameUTF16 = bin170ASCII("new"), bin170ASCII("x"), bin170ASCII("q")
		return in
	}
	for _, flip := range []bool{false, true} {
		m, err := bin170MustSnap(t, mk(flip)).NodeMetadata()
		if err != nil {
			t.Fatalf("flip=%v: %v", flip, err)
		}
		bin170CheckMeta(t, m, bin170WantDefault(nil))
	}
}

func TestBIN170ProjectionEqualNotIdentical(t *testing.T) {
	// 投影相等 != IsIdentical：>64 actor 首 61 相同对与 SHA256 original
	// 大小写对投影相同而原始比较双向不同（ignoreCreated 两种取值）。
	// Projection equality != IsIdentical: a >64-actor pair sharing the
	// first 61 units and a SHA256-original case pair project equal while
	// the raw comparison differs in BOTH directions under both
	// ignoreCreated switch values.
	tail2 := append(append(bin170RepF('a', 61), 'b'), bin170RepF('a', 3)...)
	actorPair := [2]FileInfoSnapshotInput{
		bin170Snap(bin170RepF('a', 65), nil, bin170S1(bin170A40(), bin170NoOrig())),
		bin170Snap(tail2, nil, bin170S1(bin170A40(), bin170NoOrig())),
	}
	shaPair := [2]FileInfoSnapshotInput{
		bin170Snap(nil, nil, bin170S1(bin170A40(), nil), bin170S256X(bin170RepF('X', 64))),
		bin170Snap(nil, nil, bin170S1(bin170A40(), nil), bin170S256X(bin170RepF('x', 64))),
	}
	pairs := []struct {
		name string
		in   [2]FileInfoSnapshotInput
		want bin170Want
	}{
		{"actor61prefix", actorPair, bin170Want{100, 200, 300, bin170DotsAfter(bin170RepF('a', 65)), nil, bin170NoOrig(), nil}},
		{"sha256origCase", shaPair, bin170WantDefault(nil)},
	}
	for _, p := range pairs {
		a, b := bin170MustSnap(t, p.in[0]), bin170MustSnap(t, p.in[1])
		ma, ea := a.NodeMetadata()
		mb, eb := b.NodeMetadata()
		if ea != nil || eb != nil {
			t.Fatalf("%s: %v %v", p.name, ea, eb)
		}
		bin170CheckMeta(t, ma, p.want)
		bin170CheckMeta(t, mb, p.want)
		for _, ig := range []bool{false, true} {
			if same, err := a.IsIdentical(b, ig); err != nil || same {
				t.Errorf("%s: a.IsIdentical(b,%v) got (%v,%v) want (false,nil)", p.name, ig, same, err)
			}
			if same, err := b.IsIdentical(a, ig); err != nil || same {
				t.Errorf("%s: b.IsIdentical(a,%v) got (%v,%v) want (false,nil)", p.name, ig, same, err)
			}
		}
	}
}

func TestBIN170OwnershipObservableIsolation(t *testing.T) {
	// S2 隔离：期望的原始快照与七字段元数据先于一切构造从全新字面量
	// 工厂独立建立；可变输入另行独立分配，绝不共享期望切片或全局夹具。
	// 突变构造输入、Fields() 返回与 M1 getter 返回后，逐字段复核快照
	// 原始状态与 M1/M2/新 M3。仅验证可观察隔离，允许私有只读存储共享，
	// 不做地址/布局断言。
	// S2 isolation: the expected raw snapshot and the seven-field metadata
	// are built from fresh literal factories BEFORE any construction; the
	// mutable input is allocated separately and never shares an expected
	// slice or global fixture. After mutating the constructor inputs,
	// Fields() returns and M1 getter returns, the raw snapshot state and
	// M1/M2/fresh M3 are rechecked field by field. Observable isolation
	// only: private read-only storage sharing is allowed and no
	// address/layout assertions are made.
	a70 := func() []uint16 { return bin170RepF('a', 70) }
	b66 := func() []uint16 { return bin170RepF('b', 66) }
	m70 := func() []uint16 { return append(append(bin170RepF('o', 60), 0xDC00, 0x0000), bin170RepF('o', 8)...) }
	m65 := func() []uint16 { return append(append(bin170RepF('m', 61), 0xD83D), bin170RepF('m', 3)...) }
	// 期望先行：raw W_exp 与 metadata W_exp 互不依赖被测实现。
	// Expectations first: raw W_exp and metadata W_exp never derive from
	// the code under test.
	expRaw := FileInfoSnapshotInput{
		RepoKeyUTF16: bin170ASCII("rk"), RepoPathUTF16: bin170ASCII("a/b"), NameUTF16: bin170ASCII("nm"),
		CreatedMillis: 100, LastModifiedMillis: 200, LastUpdatedMillis: 300,
		CreatedByUTF16: a70(), ModifiedByUTF16: b66(), SizeBytes: 42, MimeTypeUTF16: bin170ASCII("m"),
		Checksums: []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: bin170A40(), Original: m70()},
			{Kind: ChecksumMD5, Actual: bin170B32(), Original: m65()},
		},
	}
	expMeta := bin170Want{100, 200, 300, bin170DotsAfter(a70()), bin170DotsAfter(b66()), m70(), m65()}
	// 独立可变输入 W_in：全部切片重新构造。/ Independent mutable W_in:
	// every slice is freshly allocated.
	in := FileInfoSnapshotInput{
		RepoKeyUTF16: bin170ASCII("rk"), RepoPathUTF16: bin170ASCII("a/b"), NameUTF16: bin170ASCII("nm"),
		CreatedMillis: 100, LastModifiedMillis: 200, LastUpdatedMillis: 300,
		CreatedByUTF16: a70(), ModifiedByUTF16: b66(), SizeBytes: 42, MimeTypeUTF16: bin170ASCII("m"),
		Checksums: []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: bin170A40(), Original: m70()},
			{Kind: ChecksumMD5, Actual: bin170B32(), Original: m65()},
		},
	}
	s := bin170MustSnap(t, in)
	m1, e1 := s.NodeMetadata()
	m2, e2 := s.NodeMetadata()
	if e1 != nil || e2 != nil {
		t.Fatalf("project: %v %v", e1, e2)
	}
	// 突变一切外部可变切片与条目描述符。/ Mutate every external
	// mutable slice and entry descriptor.
	bin170Mutate(in.CreatedByUTF16)
	bin170Mutate(in.ModifiedByUTF16)
	bin170Mutate(in.MimeTypeUTF16)
	for i := range in.Checksums {
		bin170Mutate(in.Checksums[i].Actual)
		bin170Mutate(in.Checksums[i].Original)
	}
	f := s.Fields()
	bin170Mutate(f.RepoKeyUTF16)
	bin170Mutate(f.RepoPathUTF16)
	bin170Mutate(f.NameUTF16)
	bin170Mutate(f.CreatedByUTF16)
	bin170Mutate(f.ModifiedByUTF16)
	bin170Mutate(f.MimeTypeUTF16)
	for i := range f.Checksums {
		bin170Mutate(f.Checksums[i].Actual)
		bin170Mutate(f.Checksums[i].Original)
	}
	bin170Mutate(m1.CreatedBy().UTF16())
	bin170Mutate(m1.ModifiedBy().UTF16())
	bin170Mutate(m1.SHA1OriginalUTF16())
	bin170Mutate(m1.MD5OriginalUTF16())
	// 复核：完整原始快照与三份元数据。/ Recheck: the full raw snapshot
	// and three metadata values.
	bin170CheckRaw(t, s.Fields(), expRaw)
	bin170CheckMeta(t, m1, expMeta)
	bin170CheckMeta(t, m2, expMeta)
	m3, e3 := s.NodeMetadata()
	if e3 != nil {
		t.Fatalf("reproject: %v", e3)
	}
	bin170CheckMeta(t, m3, expMeta)
}

func TestBIN170ConcurrentReadersAndGetterCopies(t *testing.T) {
	// 并发读者：每次投影都必须成功且七字段等于独立期望，然后才允许写
	// 自己的 getter 副本；两个 original 均非空以覆盖两条复制路径；访问
	// Fields 校验和前先做存在性/重复检查，不盲目下标。
	// Concurrent readers: every projection must succeed with all seven
	// fields equal to the independent expectation before the goroutine
	// may mutate its own getter copies; both originals are nonnull to
	// cover both copy paths; checksum access through Fields is guarded
	// for existence and duplicates instead of blind indexing.
	m70 := func() []uint16 { return append(append(bin170RepF('o', 60), 0xDC00), bin170RepF('o', 9)...) }
	m65 := func() []uint16 { return append(bin170RepF('m', 62), 0xD83D, 0x0002) }
	s := bin170MustSnap(t, bin170Snap(bin170RepF('a', 70), bin170RepF('b', 65),
		bin170S1(bin170A40(), m70()), bin170S2(bin170B32(), m65())))
	want := bin170Want{100, 200, 300,
		bin170DotsAfter(bin170RepF('a', 70)), bin170DotsAfter(bin170RepF('b', 65)), m70(), m65()}
	m1, e1 := s.NodeMetadata()
	if e1 != nil {
		t.Fatalf("project: %v", e1)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 64; i++ {
				m, err := s.NodeMetadata()
				if err != nil {
					t.Errorf("concurrent project: %v", err)
					continue
				}
				bin170CheckMeta(t, m, want)
				bin170Mutate(m.CreatedBy().UTF16())
				bin170Mutate(m.ModifiedBy().UTF16())
				bin170Mutate(m.SHA1OriginalUTF16())
				bin170Mutate(m.MD5OriginalUTF16())
				bin170Mutate(m1.ModifiedBy().UTF16())
				bin170Mutate(m1.SHA1OriginalUTF16())
				bin170Mutate(m1.MD5OriginalUTF16())
				f := s.Fields()
				for j := range f.Checksums {
					bin170Mutate(f.Checksums[j].Actual)
					bin170Mutate(f.Checksums[j].Original)
				}
			}
		}()
	}
	wg.Wait()
	bin170CheckMeta(t, m1, want)
	m2, err := s.NodeMetadata()
	if err != nil {
		t.Fatalf("reproject: %v", err)
	}
	bin170CheckMeta(t, m2, want)
}

// 控制组（允许 GREEN）：NewActor 在此是被测对象而非 oracle，证明同包
// 模型值的既有契约仍成立。/ Control (may stay green): NewActor is the
// testee here, not an oracle; it proves the same-package model contracts
// still hold.
func TestBIN170ControlCopiedValuesContract(t *testing.T) {
	if got := NewActor(bin170RepF('a', 65)).UTF16(); !bin170EqU(got, bin170DotsAfter(bin170RepF('a', 65))) {
		t.Errorf("control actor: %v", got)
	}
	orig := append(append(bin170RepF('o', 60), 0xDC00), bin170RepF('o', 9)...)
	if got := NewFileMetadata(FileMetadataInput{SHA1Original: orig}).SHA1OriginalUTF16(); !bin170EqU(got, orig) {
		t.Errorf("control original: %v", got)
	}
}

// 基础变化门回归：固定优先级、原始差异和 Desired 参数来源。
// Basic dirty-gate regressions: priority, raw differences and Desired operands.
package repository

import (
	"context"
	"errors"
	"github.com/0ldlight/bf/internal/content"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---- 测试侧独立助手（不复用其他测试文件的夹具）。----
// ---- Test-side independent helpers (no fixtures shared with other test files). ----

// bin174U 把 BMP 字面串转码元；代理对须用显式单元构造。
// bin174U converts a BMP literal to units; surrogate pairs need explicit units.
func bin174U(s string) []uint16 {
	u := make([]uint16, 0, len(s))
	for _, r := range s {
		u = append(u, uint16(r))
	}
	return u
}

func bin174Rep(u uint16, n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = u
	}
	return out
}

func bin174MustSnap(t *testing.T, in FileInfoSnapshotInput) FileInfoSnapshot {
	t.Helper()
	s, err := NewFileInfoSnapshot(in)
	if err != nil {
		t.Fatalf("construct snapshot: %v", err)
	}
	return s
}

// bin174Snap：默认 rk/a/b/nm、100/200/300、size 42、actor/MIME nil。
// bin174Snap: defaults rk/a/b/nm, 100/200/300, size 42, nil actors/MIME.
func bin174Snap(cks ...ChecksumEntryInput) FileInfoSnapshotInput {
	return FileInfoSnapshotInput{
		RepoKeyUTF16: bin174U("rk"), RepoPathUTF16: bin174U("a/b"), NameUTF16: bin174U("nm"),
		CreatedMillis: 100, LastModifiedMillis: 200, LastUpdatedMillis: 300,
		SizeBytes: 42, Checksums: cks,
	}
}

func bin174S1(actual, original []uint16) ChecksumEntryInput {
	return ChecksumEntryInput{Kind: ChecksumSHA1, Actual: actual, Original: original}
}

func bin174S256(actual, original []uint16) ChecksumEntryInput {
	return ChecksumEntryInput{Kind: ChecksumSHA256, Actual: actual, Original: original}
}

func bin174SMD5(actual, original []uint16) ChecksumEntryInput {
	return ChecksumEntryInput{Kind: ChecksumMD5, Actual: actual, Original: original}
}

// bin174Recorder：委托 spy，记录调用次数与收到的引用。
// bin174Recorder: delegate spy recording call count and the received reference.
type bin174Recorder struct {
	calls   int
	lastRef FileReference
	update  func(context.Context, FileReference) (int64, error)
}

func (r *bin174Recorder) doUpdate(ctx context.Context, ref FileReference) (int64, error) {
	r.calls++
	r.lastRef = ref
	if r.update == nil {
		return 1, nil
	}
	return r.update(ctx, ref)
}

// bin174CancelOnErrCheck 在第 trigger 次 Err() 检查时取消底层真实 context；
// 沿用既有 create/update 测试的确定性检查点手法——无 sleep、无额外
// goroutine、无产品钩子。
// bin174CancelOnErrCheck cancels the underlying real context at the
// trigger-th Err() check; the deterministic-checkpoint technique of the
// existing create/update tests — no sleeps, no extra goroutines, no product
// hooks.
type bin174CancelOnErrCheck struct {
	context.Context
	trigger int
	calls   int
	cancel  context.CancelFunc
}

func (c *bin174CancelOnErrCheck) Err() error {
	c.calls++
	if c.calls == c.trigger {
		c.cancel()
	}
	return c.Context.Err()
}

// bin174EqUnits：nil 与非 nil 空互异的码元比较。
// bin174EqUnits compares units keeping nil/nonnil-empty distinct.
func bin174EqUnits(a, b []uint16) bool { return reflect.DeepEqual(a, b) }

// bin174StrRepeat：期望值用的测试侧字符串重复（不经被测实现）。
// bin174StrRepeat: test-side string repeat for expectations (never via the
// implementation).
func bin174StrRepeat(c byte, n int) string { return strings.Repeat(string(c), n) }

// bin174Call：以显式参数在 Background 上调用被测 gate（表格行的公共形）；
// 自定义 context 的行保持内联调用。
// bin174Call calls the gate on Background with explicit arguments (the shared
// row shape); rows with custom contexts stay inline.
func bin174Call(existing, desired FileInfoSnapshot, ignoreCreated bool, id int64, update func(context.Context, FileReference) (int64, error)) (FileDirtyUpdateResult, error) {
	return UpdateFileIfChanged(context.Background(), FileDirtyUpdateInput{
		Existing: existing, Desired: desired, IgnoreCreated: ignoreCreated, ID: id,
	}, update)
}

// ---- P1 固定优先顺序与无效操作数。----
// ---- P1 fixed priority order and invalid operands. ----

// 预取消 → nil 委托 → 非正 ID → 快照有效性；零委托调用，原因 errors.Is
// 可达（含 DeadlineExceeded）。
// Pre-cancel -> nil callback -> nonpositive ID -> snapshot validity; zero
// delegate calls, causes reachable via errors.Is (DeadlineExceeded included).
func TestBIN174GateOrderPreconditionsPriority(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	existing := bin174MustSnap(t, bin174Snap())
	desired := bin174MustSnap(t, bin174Snap())

	// 预取消压过非正 ID 与无效快照（打包次序对；nil 委托随后单独覆盖）。
	// Pre-cancel beats the nonpositive ID and invalid snapshots (packed order
	// pair; the nil callback pair follows separately).
	rec := &bin174Recorder{}
	res, err := UpdateFileIfChanged(cancelled,
		FileDirtyUpdateInput{Existing: desired, Desired: FileInfoSnapshot{}, ID: 0}, rec.doUpdate)
	if res != (FileDirtyUpdateResult{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel over nil/ID/snapshot: got (%+v, %v), want zero result wrapping context.Canceled", res, err)
	}
	if rec.calls != 0 {
		t.Fatalf("pre-cancel: delegate called %d times", rec.calls)
	}
	// 预取消 + nil 委托：取消胜出且零结果。
	// Pre-cancel + nil callback: the cancellation wins with the zero result.
	res, err = UpdateFileIfChanged(cancelled,
		FileDirtyUpdateInput{Existing: existing, Desired: desired, ID: 7}, nil)
	if res != (FileDirtyUpdateResult{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel must precede nil callback: got (%+v, %v), want zero result wrapping context.Canceled", res, err)
	}
	// DeadlineExceeded 原因可达且零结果。
	// DeadlineExceeded cause reachable with the zero result.
	deadlineCtx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	dcancel()
	if res, err := UpdateFileIfChanged(deadlineCtx,
		FileDirtyUpdateInput{Existing: existing, Desired: desired, ID: 7}, rec.doUpdate); res != (FileDirtyUpdateResult{}) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: got (%+v, %v), want zero result wrapping DeadlineExceeded", res, err)
	}

	// nil 委托：固定私有配置错误，先于非正 ID 与无效快照。
	// nil callback: the fixed private configuration error precedes nonpositive
	// ID and snapshot validity.
	res, err = UpdateFileIfChanged(context.Background(),
		FileDirtyUpdateInput{Existing: existing, Desired: desired, ID: 0}, nil)
	if res != (FileDirtyUpdateResult{}) || err == nil || err.Error() != errNilDirtyUpdateDependency.Error() {
		t.Fatalf("nil callback + ID<=0: got (%+v, %v), want zero result and fixed %q", res, err, errNilDirtyUpdateDependency.Error())
	}
	res, err = UpdateFileIfChanged(context.Background(),
		FileDirtyUpdateInput{Existing: FileInfoSnapshot{}, Desired: FileInfoSnapshot{}, ID: 0}, nil)
	if res != (FileDirtyUpdateResult{}) || err == nil || err.Error() != errNilDirtyUpdateDependency.Error() {
		t.Fatalf("nil callback + invalid snapshots: got (%+v, %v), want zero result and fixed %q", res, err, errNilDirtyUpdateDependency.Error())
	}

	// 非正 ID：包裹既有 ErrInvalidUpdateID、固定文案、零调用，先于快照
	// 有效性。
	// Nonpositive ID: wraps the existing ErrInvalidUpdateID, fixed text, zero
	// calls, before snapshot validity.
	for _, id := range []int64{0, -7} {
		rec := &bin174Recorder{}
		res, err := bin174Call(existing, desired, false, id, rec.doUpdate)
		if res != (FileDirtyUpdateResult{}) || !errors.Is(err, ErrInvalidUpdateID) {
			t.Fatalf("ID=%d: got (%+v, %v), want zero result wrapping ErrInvalidUpdateID", id, res, err)
		}
		if err.Error() != stageDirtyUpdateInvalidID {
			t.Fatalf("ID=%d: stage text %q, want fixed %q", id, err.Error(), stageDirtyUpdateInvalidID)
		}
		if rec.calls != 0 {
			t.Fatalf("ID=%d: delegate called %d times", id, rec.calls)
		}
	}
	rec = &bin174Recorder{}
	res, err = UpdateFileIfChanged(context.Background(),
		FileDirtyUpdateInput{Existing: FileInfoSnapshot{}, Desired: FileInfoSnapshot{}, ID: 0}, rec.doUpdate)
	if res != (FileDirtyUpdateResult{}) || !errors.Is(err, ErrInvalidUpdateID) {
		t.Fatalf("ID<=0 must precede snapshot validity: got (%+v, %v), want zero result wrapping ErrInvalidUpdateID", res, err)
	}

	// 无效操作数：任一侧无效均返回既有 ErrInvalidFileInfoSnapshot，零调用。
	// Any invalid operand yields the existing ErrInvalidFileInfoSnapshot with
	// zero delegate calls.
	for name, in := range map[string]FileDirtyUpdateInput{
		"invalid desired":  {Existing: existing, Desired: FileInfoSnapshot{}, ID: 7},
		"invalid existing": {Existing: FileInfoSnapshot{}, Desired: desired, ID: 7},
		"both invalid":     {Existing: FileInfoSnapshot{}, Desired: FileInfoSnapshot{}, ID: 7},
	} {
		rec := &bin174Recorder{}
		res, err := UpdateFileIfChanged(context.Background(), in, rec.doUpdate)
		if res != (FileDirtyUpdateResult{}) || !errors.Is(err, ErrInvalidFileInfoSnapshot) {
			t.Errorf("%s: got (%+v, %v), want zero result wrapping ErrInvalidFileInfoSnapshot", name, res, err)
		}
		if rec.calls != 0 {
			t.Errorf("%s: delegate called %d times", name, rec.calls)
		}
	}
}

// ---- P2 相同跳过：零调用、零映射、无追加取消检查。----
// ---- P2 identical skip: zero calls, zero mapping, no appended cancellation check. ----

// 相同即跳过：同一输入、全零有效快照、nil/非 nil 空校验和图、含孤立
// 代理的相同快照（守卫不进相同路径）；每种 {false,0},nil 零调用。
// Identical pairs skip (same input twice, all-zero valid snapshots, nil vs
// nonnil-empty checksum maps, lone-surrogate fields on both sides): each
// yields {false,0},nil with zero delegate calls.
func TestBIN174GateIdenticalSkipsWithoutCalls(t *testing.T) {
	loneHighRepo := bin174Snap()
	loneHighRepo.RepoKeyUTF16 = append(bin174U("rk"), 0xD800)
	loneLowSHA1 := bin174Snap(bin174S1(append(bin174U("a"), 0xDC00), nil))
	rows := []struct {
		name              string
		existing, desired FileInfoSnapshotInput
		ignoreCreated     bool
	}{
		{"same input twice", bin174Snap(), bin174Snap(), false},
		{"same input ignore created", bin174Snap(), bin174Snap(), true},
		{"all zero valid",
			FileInfoSnapshotInput{RepoKeyUTF16: []uint16{}, RepoPathUTF16: []uint16{}, NameUTF16: []uint16{}},
			FileInfoSnapshotInput{RepoKeyUTF16: []uint16{}, RepoPathUTF16: []uint16{}, NameUTF16: []uint16{}}, false},
		{"nil vs empty checksum map", bin174Snap(), bin174WithChecksums(bin174Snap(), []ChecksumEntryInput{}), false},
		{"identical lone-high repoKey", loneHighRepo, loneHighRepo, false},
		{"identical lone-low sha1 actual", loneLowSHA1, loneLowSHA1, true},
	}
	for _, r := range rows {
		rec := &bin174Recorder{}
		res, err := bin174Call(bin174MustSnap(t, r.existing), bin174MustSnap(t, r.desired), r.ignoreCreated, 9, rec.doUpdate)
		if err != nil || res != (FileDirtyUpdateResult{Changed: false, Count: 0}) {
			t.Errorf("%s: got (%+v, %v), want ({false 0}, nil)", r.name, res, err)
		}
		if rec.calls != 0 {
			t.Errorf("%s: delegate called %d times on identical skip", r.name, rec.calls)
		}
	}
	// 确定性检查点：相同分支只有入口一次 Err() 检查；若追加比较后取消
	// 检查，trigger=2 会真的取消并翻转结果。无 sleep。
	// Deterministic checkpoint: exactly one entry Err() check on this branch;
	// an appended post-compare check would let trigger=2 cancel and flip.
	inner, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &bin174CancelOnErrCheck{Context: inner, trigger: 2, cancel: cancel}
	rec := &bin174Recorder{}
	res, err := UpdateFileIfChanged(ctx, FileDirtyUpdateInput{
		Existing: bin174MustSnap(t, bin174Snap()), Desired: bin174MustSnap(t, bin174Snap()), ID: 9,
	}, rec.doUpdate)
	if err != nil || res != (FileDirtyUpdateResult{Changed: false, Count: 0}) {
		t.Fatalf("identical + hypothetical appended cancel: got (%+v, %v)", res, err)
	}
	if ctx.calls != 1 {
		t.Fatalf("identical branch Err() checks = %d, want exactly 1 (entry only)", ctx.calls)
	}
	if rec.calls != 0 {
		t.Fatalf("identical branch delegate called %d times", rec.calls)
	}
}

// bin174WithChecksums 返回替换校验和切片后的副本。
// bin174WithChecksums returns the input with the checksum slice replaced.
func bin174WithChecksums(in FileInfoSnapshotInput, cks []ChecksumEntryInput) FileInfoSnapshotInput {
	in.Checksums = cks
	return in
}

// ---- P3 created 开关两个方向。----
// ---- P3 created switch in both directions. ----

// 仅 created 不同的对：IgnoreCreated=false 判脏并更新；true 跳过。
// A pair differing only in created: IgnoreCreated=false is dirty and updates;
// true skips.
func TestBIN174GateCreatedSwitchBothDirections(t *testing.T) {
	existing := bin174MustSnap(t, bin174Snap())
	desiredIn := bin174Snap()
	desiredIn.CreatedMillis = 999
	desired := bin174MustSnap(t, desiredIn)
	recSkip := &bin174Recorder{}
	res, err := bin174Call(existing, desired, true, 5, recSkip.doUpdate)
	if err != nil || res != (FileDirtyUpdateResult{Changed: false, Count: 0}) {
		t.Fatalf("ignoreCreated=true: got (%+v, %v), want ({false 0}, nil)", res, err)
	}
	if recSkip.calls != 0 {
		t.Fatalf("ignoreCreated=true: delegate called %d times", recSkip.calls)
	}
	recDirty := &bin174Recorder{}
	res, err = bin174Call(existing, desired, false, 5, recDirty.doUpdate)
	if err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
		t.Fatalf("ignoreCreated=false: got (%+v, %v), want ({true 1}, nil)", res, err)
	}
	if recDirty.calls != 1 {
		t.Fatalf("ignoreCreated=false: delegate called %d times, want exactly 1", recDirty.calls)
	}
}

// ---- P4 原始不同但投影相同仍更新。----
// ---- P4 raw difference despite equal projection still updates. ----

// 四对投影相同而原始不同的快照都必须进入更新分支（前提经独立投影比较
// 自证）。
// Four pairs with equal projections but raw differences must all update
// (premise self-checked via an independent projection comparison).
func TestBIN174GateRawDiffDespiteEqualProjection(t *testing.T) {
	actorA := bin174Snap()
	actorA.CreatedByUTF16 = bin174Rep('a', 65)
	actorB := bin174Snap()
	actorB.CreatedByUTF16 = append(bin174Rep('a', 61), 'b', 'b', 'b', 'b')
	shaX := bin174Snap(bin174S1(bin174A40(t), nil), bin174S256(bin174Rep('D', 64), bin174Rep('X', 64)))
	shaXCase := bin174Snap(bin174S1(bin174A40(t), nil), bin174S256(bin174Rep('D', 64), bin174Rep('x', 64)))
	mimeNil := bin174Snap()
	mimeSet := bin174Snap()
	mimeSet.MimeTypeUTF16 = bin174U("app/bin")
	nameA := bin174Snap()
	nameB := bin174Snap()
	nameB.NameUTF16 = bin174U("nm2")
	pairs := []struct {
		name              string
		existing, desired FileInfoSnapshotInput
	}{
		{"actor tail beyond 61", actorA, actorB},
		{"sha256 original case", shaX, shaXCase},
		{"mime nil vs value", mimeNil, mimeSet},
		{"cached name only", nameA, nameB},
	}
	for _, p := range pairs {
		// 前提自证：两投影确实相等。/ Premise: both projections are equal.
		ea, eb := bin174MustSnap(t, p.existing), bin174MustSnap(t, p.desired)
		ma, errA := ea.NodeMetadata()
		mb, errB := eb.NodeMetadata()
		if errA != nil || errB != nil || !bin174MetaEqual(ma, mb) {
			t.Fatalf("%s: fixture projections differ (%v %v), want equal", p.name, errA, errB)
		}
		rec := &bin174Recorder{}
		res, err := bin174Call(ea, eb, false, 5, rec.doUpdate)
		if err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
			t.Errorf("%s: got (%+v, %v), want ({true 1}, nil)", p.name, res, err)
		}
		if rec.calls != 1 {
			t.Errorf("%s: delegate called %d times, want exactly 1", p.name, rec.calls)
		}
	}
}

func bin174A40(t *testing.T) []uint16 {
	t.Helper()
	return bin174Rep('A', 40)
}

// bin174MetaEqual 独立比较七字段投影（测试侧，不经被测实现）。
// bin174MetaEqual independently compares the seven-field projection
// (test-side, never via the implementation).
func bin174MetaEqual(a, b FileMetadata) bool {
	if a.CreatedMillis() != b.CreatedMillis() || a.ModifiedMillis() != b.ModifiedMillis() || a.UpdatedMillis() != b.UpdatedMillis() {
		return false
	}
	if !bin174EqUnits(a.CreatedBy().UTF16(), b.CreatedBy().UTF16()) ||
		!bin174EqUnits(a.ModifiedBy().UTF16(), b.ModifiedBy().UTF16()) {
		return false
	}
	return bin174EqUnits(a.SHA1OriginalUTF16(), b.SHA1OriginalUTF16()) &&
		bin174EqUnits(a.MD5OriginalUTF16(), b.MD5OriginalUTF16())
}

// ---- P5 完整委托载荷全部来自 Desired。----
// ---- P5 the full delegate payload derives from Desired. ----

// ID 原样来自输入 ID、路径身份来自 Desired（名字经既有 helper 从 RepoPath
// 导出，不用独立 cached name），content 三串+size 逐字透传（含非规范摘要
// 与负 size），Metadata 非 nil 且七字段符合 Desired 投影。
// The ID comes verbatim from the input ID and the path identity from Desired
// (name re-derived from RepoPath via the existing helper, never the cached
// name); content strings and size pass through verbatim (noncanonical digests
// and negative size included) and Metadata is nonnil with the Desired
// projection.
func TestBIN174GateDelegatePayloadAllFromDesired(t *testing.T) {
	t.Run("identity and path derived not cached name", func(t *testing.T) {
		desiredIn := bin174Snap()
		desiredIn.RepoKeyUTF16 = bin174U("rk2")
		desiredIn.RepoPathUTF16 = bin174U("p/q/f.bin")
		desiredIn.NameUTF16 = bin174U("totally-different-cached-name")
		desiredIn.LastModifiedMillis = 201
		rec := &bin174Recorder{}
		res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 777, rec.doUpdate)
		if err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
			t.Fatalf("got (%+v, %v), want ({true 1}, nil)", res, err)
		}
		if rec.lastRef.ID != 777 {
			t.Errorf("delegate ID = %d, want 777 verbatim", rec.lastRef.ID)
		}
		if got := rec.lastRef.Path.Repo(); got != "rk2" {
			t.Errorf("delegate repo = %q, want Desired repoKey %q", got, "rk2")
		}
		if got := rec.lastRef.Path.Parent(); got != "p/q" {
			t.Errorf("delegate parent = %q, want %q", got, "p/q")
		}
		if got := rec.lastRef.Path.Name(); got != "f.bin" {
			t.Errorf("delegate name = %q, want path-derived %q (cached name must not be written)", got, "f.bin")
		}
		if !rec.lastRef.Path.IsFile() {
			t.Errorf("delegate path must stay a file")
		}
	})
	t.Run("write path still goes through existing helper mapping", func(t *testing.T) {
		desiredIn := bin174Snap()
		desiredIn.RepoPathUTF16 = bin174U("a/f:properties:statistics")
		desiredIn.LastModifiedMillis = 201
		rec := &bin174Recorder{}
		if _, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate); err != nil {
			t.Fatalf("helper mapping row: %v", err)
		}
		if got := rec.lastRef.Path.Name(); got != "f:properties" {
			t.Errorf("metadata suffix row: name = %q, want helper-mapped %q", got, "f:properties")
		}
		if got := rec.lastRef.Path.Parent(); got != "a" {
			t.Errorf("metadata suffix row: parent = %q, want %q", got, "a")
		}
	})
	t.Run("seven fields modifiedBy and md5 original from desired", func(t *testing.T) {
		existingIn := bin174Snap(bin174S1(bin174A40(t), nil), bin174SMD5(bin174Rep('x', 32), nil))
		desiredIn := bin174Snap(bin174S1(bin174A40(t), bin174U("ORIG-NEW")), bin174SMD5(bin174Rep('m', 32), bin174U("NO_ORIG")))
		desiredIn.CreatedMillis, desiredIn.LastModifiedMillis, desiredIn.LastUpdatedMillis = -1, 1<<40, 0
		desiredIn.ModifiedByUTF16 = bin174U("modifier-77")
		rec := &bin174Recorder{}
		if _, err := bin174Call(bin174MustSnap(t, existingIn), bin174MustSnap(t, desiredIn), false, 777, rec.doUpdate); err != nil {
			t.Fatalf("distinct modifiedBy/md5Original row: %v", err)
		}
		m := rec.lastRef.Metadata
		if m == nil {
			t.Fatalf("distinct row: metadata pointer is nil")
		}
		if m.CreatedMillis() != -1 || m.ModifiedMillis() != 1<<40 || m.UpdatedMillis() != 0 {
			t.Errorf("distinct row times = %d/%d/%d, want -1/%d/0", m.CreatedMillis(), m.ModifiedMillis(), m.UpdatedMillis(), int64(1<<40))
		}
		if got := m.ModifiedBy().UTF16(); !bin174EqUnits(got, bin174U("modifier-77")) {
			t.Errorf("modifiedBy = %v, want Desired value (Existing was null)", got)
		}
		if got := m.MD5OriginalUTF16(); !bin174EqUnits(got, bin174U("NO_ORIG")) {
			t.Errorf("md5Original = %v, want Desired NO_ORIG (Existing was nil)", got)
		}
		if got := m.SHA1OriginalUTF16(); !bin174EqUnits(got, bin174U("ORIG-NEW")) {
			t.Errorf("sha1Original = %v, want Desired value verbatim", got)
		}
		if rec.lastRef.ID != 777 {
			t.Errorf("distinct row delegate ID = %d, want 777 verbatim", rec.lastRef.ID)
		}
	})
	t.Run("content strings and size verbatim incl noncanonical and negative", func(t *testing.T) {
		existingIn := bin174Snap(bin174S1(bin174Rep('x', 3), nil), bin174S256(bin174Rep('y', 4), nil), bin174SMD5(bin174Rep('z', 5), nil))
		existingIn.SizeBytes = 7
		desiredIn := bin174Snap(bin174S1(bin174U("AbC"), nil), bin174S256(bin174U(" Not-Canonical "), nil), bin174SMD5(bin174U("\x00z"), nil))
		desiredIn.SizeBytes = -5
		rec := &bin174Recorder{}
		if _, err := bin174Call(bin174MustSnap(t, existingIn), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate); err != nil {
			t.Fatalf("noncanonical row: %v", err)
		}
		want := content.Metadata{SHA1: "AbC", SHA256: " Not-Canonical ", MD5: "\x00z", Size: -5}
		if rec.lastRef.Content != want {
			t.Errorf("delegate content = %+v, want Desired-derived %+v", rec.lastRef.Content, want)
		}
	})
	t.Run("seven fields actor truncation and original nil vs empty", func(t *testing.T) {
		desiredIn := bin174Snap(bin174S1(bin174A40(t), []uint16{}), bin174SMD5(bin174Rep('m', 32), nil))
		desiredIn.CreatedMillis, desiredIn.LastModifiedMillis, desiredIn.LastUpdatedMillis = -7, 0, 1<<40
		desiredIn.CreatedByUTF16 = bin174Rep('u', 100)
		rec := &bin174Recorder{}
		if _, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate); err != nil {
			t.Fatalf("seven fields row: %v", err)
		}
		m := rec.lastRef.Metadata
		if m == nil {
			t.Fatalf("delegate metadata pointer is nil, want explicit nonnil snapshot")
		}
		if m.CreatedMillis() != -7 || m.ModifiedMillis() != 0 || m.UpdatedMillis() != 1<<40 {
			t.Errorf("metadata times = %d/%d/%d, want -7/0/%d verbatim", m.CreatedMillis(), m.ModifiedMillis(), m.UpdatedMillis(), int64(1<<40))
		}
		if got := m.CreatedBy().UTF16(); !bin174EqUnits(got, append(bin174Rep('u', 61), '.', '.', '.')) {
			t.Errorf("createdBy = %d units, want existing 64-unit abbreviation of 100", len(got))
		}
		if !m.ModifiedBy().IsNull() {
			t.Errorf("modifiedBy must project null from nil input")
		}
		if sha := m.SHA1OriginalUTF16(); sha == nil || len(sha) != 0 {
			t.Errorf("sha1Original must stay present-empty, got %v (nil=%v)", sha, sha == nil)
		}
		if md5 := m.MD5OriginalUTF16(); md5 != nil {
			t.Errorf("md5Original must project nil for a present entry with nil original, got %v", md5)
		}
	})
}

// ---- P6 Actual 缺席/null/空：门前区分、门后折叠为空串。----
// ---- P6 Actual absence/null/empty: distinct before the gate, collapsed after. ----

// 原始比较区分缺席/present-null/present-empty/非空；变化分支内前三者均
// 折叠为 Go 空串，非空值逐字透传。
// The raw comparison distinguishes absence/null/empty/nonempty; inside the
// dirty gate the first three collapse to the Go empty string while nonempty
// values pass through verbatim.
func TestBIN174GateActualAbsenceCollapsesAfterGate(t *testing.T) {
	existing := bin174MustSnap(t, bin174Snap(bin174S1(bin174Rep('a', 40), nil), bin174SMD5(bin174Rep('c', 9), nil)))
	rows := []struct {
		name        string
		desired     FileInfoSnapshotInput
		wantContent content.Metadata
	}{
		{"sha1 absent", bin174Snap(bin174SMD5(bin174Rep('c', 9), nil)),
			content.Metadata{MD5: bin174StrRepeat('c', 9)}},
		{"sha1 present null actual", bin174Snap(bin174S1(nil, nil), bin174SMD5(bin174Rep('c', 9), nil)),
			content.Metadata{MD5: bin174StrRepeat('c', 9)}},
		{"sha1 present empty actual", bin174Snap(bin174S1([]uint16{}, nil), bin174SMD5(bin174Rep('c', 9), nil)),
			content.Metadata{MD5: bin174StrRepeat('c', 9)}},
		{"md5 absent keeps sha1 passthrough", bin174Snap(bin174S1(bin174Rep('a', 40), nil)),
			content.Metadata{SHA1: bin174StrRepeat('a', 40)}},
		{"nonempty passthrough control", bin174Snap(bin174S1(bin174U("Zed"), nil), bin174SMD5(bin174Rep('c', 9), nil)),
			content.Metadata{SHA1: "Zed", MD5: bin174StrRepeat('c', 9)}},
	}
	for _, r := range rows {
		rec := &bin174Recorder{}
		res, err := bin174Call(existing, bin174MustSnap(t, r.desired), false, 7, rec.doUpdate)
		if err != nil || res.Changed != true || rec.calls != 1 {
			t.Errorf("%s: got (%+v, %v) calls=%d, want ({true 1}, nil) calls=1", r.name, res, err, rec.calls)
			continue
		}
		if rec.lastRef.Content.SHA1 != r.wantContent.SHA1 {
			t.Errorf("%s: sha1 = %q, want %q", r.name, rec.lastRef.Content.SHA1, r.wantContent.SHA1)
		}
		if rec.lastRef.Content.SHA256 != r.wantContent.SHA256 {
			t.Errorf("%s: sha256 = %q, want %q", r.name, rec.lastRef.Content.SHA256, r.wantContent.SHA256)
		}
		if rec.lastRef.Content.MD5 != r.wantContent.MD5 {
			t.Errorf("%s: md5 = %q, want %q", r.name, rec.lastRef.Content.MD5, r.wantContent.MD5)
		}
	}
	// 判别行：三个折叠态彼此在原始比较中不同——先折叠再比较的错误实现会
	// 误跳过；其余 raw 状态两侧完全一致，期望槽位投影仍为空串。
	// Discriminating rows: the three collapsed states differ pairwise in the
	// raw comparison — an implementation collapsing before comparing would
	// wrongly skip; every other raw field is identical and the varied slot
	// still projects to the empty string.
	pairs := []struct {
		name        string
		existingCks []ChecksumEntryInput
		desiredCks  []ChecksumEntryInput
		read        func(content.Metadata) string
	}{
		{"sha1 absent vs present null", nil, []ChecksumEntryInput{bin174S1(nil, nil)},
			func(c content.Metadata) string { return c.SHA1 }},
		{"sha1 present null vs present empty", []ChecksumEntryInput{bin174S1(nil, nil)}, []ChecksumEntryInput{bin174S1([]uint16{}, nil)},
			func(c content.Metadata) string { return c.SHA1 }},
		{"sha256 absent vs present null", nil, []ChecksumEntryInput{bin174S256(nil, nil)},
			func(c content.Metadata) string { return c.SHA256 }},
		{"md5 present null vs present empty", []ChecksumEntryInput{bin174SMD5(nil, nil)}, []ChecksumEntryInput{bin174SMD5([]uint16{}, nil)},
			func(c content.Metadata) string { return c.MD5 }},
	}
	for _, r := range pairs {
		rec := &bin174Recorder{}
		res, err := bin174Call(bin174MustSnap(t, bin174Snap(r.existingCks...)),
			bin174MustSnap(t, bin174Snap(r.desiredCks...)), false, 7, rec.doUpdate)
		if err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 1}) || rec.calls != 1 {
			t.Errorf("%s: got (%+v, %v) calls=%d, want ({true 1}, nil) calls=1 (raw states differ)", r.name, res, err, rec.calls)
			continue
		}
		if got := r.read(rec.lastRef.Content); got != "" {
			t.Errorf("%s: collapsed slot = %q, want empty string", r.name, got)
		}
	}
	// 控制组：同一 raw 状态重复构造仍跳过。
	// Control: repeating one identical raw state still skips.
	rec := &bin174Recorder{}
	sameNull := bin174MustSnap(t, bin174Snap(bin174S1(nil, nil)))
	if res, err := bin174Call(sameNull, sameNull, false, 7, rec.doUpdate); err != nil || res != (FileDirtyUpdateResult{Changed: false, Count: 0}) || rec.calls != 0 {
		t.Errorf("identical raw state repeat: got (%+v, %v) calls=%d, want ({false 0}, nil) calls=0", res, err, rec.calls)
	}
}

package repository

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// ---- P7 五个转换字段的 UTF-16 无损语义。----
// ---- P7 lossless UTF-16 semantics of the five converted fields. ----

// bin174FieldCase 把特殊码元注入一个转换字段并从 spy 引用读回其 Go 字符串
// 投影。五字段共享同一哨兵与固定文案，黑盒无法据此区分内部先后；本表不
// 声称五字段相对顺序，仅证明每字段都受守卫。
// bin174FieldCase injects units into one converted field and reads the Go
// string projection from the spy reference. The five fields share one
// sentinel and fixed text, so the black box cannot order them; this table
// claims no relative order, only that every field is guarded.
type bin174FieldCase struct {
	name   string
	inject func(*FileInfoSnapshotInput, []uint16)
	read   func(FileReference) string
}

var bin174Fields = []bin174FieldCase{
	{"repoKey", func(in *FileInfoSnapshotInput, v []uint16) { in.RepoKeyUTF16 = v },
		func(r FileReference) string { return r.Path.Repo() }},
	{"repoPath", func(in *FileInfoSnapshotInput, v []uint16) { in.RepoPathUTF16 = v },
		func(r FileReference) string { return r.Path.Name() }},
	{"sha1Actual", func(in *FileInfoSnapshotInput, v []uint16) { in.Checksums = append(in.Checksums, bin174S1(v, nil)) },
		func(r FileReference) string { return r.Content.SHA1 }},
	{"sha256Actual", func(in *FileInfoSnapshotInput, v []uint16) { in.Checksums = append(in.Checksums, bin174S256(v, nil)) },
		func(r FileReference) string { return r.Content.SHA256 }},
	{"md5Actual", func(in *FileInfoSnapshotInput, v []uint16) { in.Checksums = append(in.Checksums, bin174SMD5(v, nil)) },
		func(r FileReference) string { return r.Content.MD5 }},
}

// 每个转换字段：孤立高/低代理、反序对、高代理+BMP 均拒绝为
// ErrUnrepresentableFileSnapshot（不误称 ErrInvalidFileInfoSnapshot），
// 零结果零委托、固定文案。
// Every converted field rejects lone high/low, reversed and high+BMP units
// as ErrUnrepresentableFileSnapshot (never mislabeled), zero result, zero
// delegate calls, fixed stage text.
func TestBIN174GateFiveFieldsRejectUnrepresentableUnits(t *testing.T) {
	for _, v := range []struct {
		name  string
		units []uint16
	}{
		{"lone high", []uint16{0xD800}},
		{"lone low", []uint16{0xDC00}},
		{"reversed pair", []uint16{0xDC00, 0xD800}},
		{"high then bmp", []uint16{0xD83D, 'x'}},
	} {
		for _, f := range bin174Fields {
			desiredIn := bin174Snap()
			f.inject(&desiredIn, v.units)
			rec := &bin174Recorder{}
			res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate)
			if res != (FileDirtyUpdateResult{}) || !errors.Is(err, ErrUnrepresentableFileSnapshot) {
				t.Errorf("%s/%s: got (%+v, %v), want zero result wrapping ErrUnrepresentableFileSnapshot", v.name, f.name, res, err)
				continue
			}
			if err.Error() != stageDirtyUpdateUnrepresentable {
				t.Errorf("%s/%s: stage text %q, want fixed %q", v.name, f.name, err.Error(), stageDirtyUpdateUnrepresentable)
			}
			if errors.Is(err, ErrInvalidFileInfoSnapshot) {
				t.Errorf("%s/%s: structurally valid snapshot mislabeled ErrInvalidFileInfoSnapshot", v.name, f.name)
			}
			if rec.calls != 0 {
				t.Errorf("%s/%s: delegate called %d times before conversion rejection", v.name, f.name, rec.calls)
			}
		}
	}
}

// 每个转换字段：合法代理对保留同一 Unicode 标量、字面 U+FFFD 保持字面、
// 混合串逐段无损——绝不静默替换。
// Every converted field: valid pairs keep one scalar, the literal U+FFFD
// stays literal, mixed strings stay lossless — never silently replaced.
func TestBIN174GateFiveFieldsPreserveValidPairsAndLiteralFFFD(t *testing.T) {
	for _, v := range []struct {
		name  string
		units []uint16
		want  string
	}{
		{"valid pair", []uint16{0xD83D, 0xDE00}, "\U0001F600"},
		{"literal FFFD", []uint16{0xFFFD}, "�"},
		{"pair fffd bmp mix", []uint16{0xD83D, 0xDE00, 0xFFFD, 'k'}, "\U0001F600�k"},
	} {
		for _, f := range bin174Fields {
			desiredIn := bin174Snap()
			f.inject(&desiredIn, v.units)
			rec := &bin174Recorder{}
			res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate)
			if err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
				t.Errorf("%s/%s: got (%+v, %v), want ({true 1}, nil)", v.name, f.name, res, err)
				continue
			}
			if got := f.read(rec.lastRef); got != v.want {
				t.Errorf("%s/%s: converted value = %q, want lossless %q", v.name, f.name, got, v.want)
			}
		}
	}
}

// 孤立代理出现在 cached Name、actor、MIME 与 SHA1/MD5 Original 时不受新
// 守卫约束：仍判脏、委托到达、这些字段按原始码元进入元数据。
// Lone surrogates in the cached Name, actors, MIME and SHA1/MD5 originals
// stay outside the new guard: still dirty, delegate reached, raw units kept.
func TestBIN174GateUnconvertedFieldsKeepRawLoneUnits(t *testing.T) {
	rawSHA1Orig := append(bin174U("NO_ORIG"), 0xDC00)
	desiredIn := bin174Snap(bin174S1(bin174A40(t), rawSHA1Orig), bin174SMD5(bin174Rep('m', 32), []uint16{0xD800}))
	desiredIn.NameUTF16 = append(bin174U("nm"), 0xD800)
	desiredIn.CreatedByUTF16 = []uint16{0xD800, 'a'}
	desiredIn.ModifiedByUTF16 = []uint16{'m', 0xDC00}
	desiredIn.MimeTypeUTF16 = []uint16{'m', 0xD800}
	desiredIn.LastModifiedMillis = 201
	rec := &bin174Recorder{}
	res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate)
	if err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 1}) {
		t.Fatalf("unconverted fields row: got (%+v, %v), want ({true 1}, nil)", res, err)
	}
	if rec.calls != 1 {
		t.Fatalf("unconverted fields row: delegate called %d times, want exactly 1", rec.calls)
	}
	m := rec.lastRef.Metadata
	if m == nil {
		t.Fatalf("unconverted fields row: metadata pointer is nil")
	}
	if got := m.CreatedBy().UTF16(); !bin174EqUnits(got, []uint16{0xD800, 'a'}) {
		t.Errorf("createdBy = %v, want raw lone-high units verbatim", got)
	}
	if got := m.ModifiedBy().UTF16(); !bin174EqUnits(got, []uint16{'m', 0xDC00}) {
		t.Errorf("modifiedBy = %v, want raw lone-low units verbatim", got)
	}
	if got := m.SHA1OriginalUTF16(); !bin174EqUnits(got, rawSHA1Orig) {
		t.Errorf("sha1Original = %v, want raw units verbatim", got)
	}
	if got := m.MD5OriginalUTF16(); !bin174EqUnits(got, []uint16{0xD800}) {
		t.Errorf("md5Original = %v, want raw lone-high unit verbatim", got)
	}
}

// ---- P8 转换错误先于既有路径校验；入口后的取消由 UpdateFile 观察。----
// ---- P8 conversion errors precede the existing path validator; a
// cancellation arriving after the entry checkpoint is observed by UpdateFile. ----

// 路径注定被既有 helper/storage 拒绝（'*' 与 NUL），但 Actual 转换错误先
// 胜出；同路径 Actual 合法时既有错误原样浮现。
// A path doomed to fail the existing helper/storage checks ('*' and NUL)
// still loses to an Actual conversion error; with legal actuals the existing
// error surfaces unchanged.
func TestBIN174GateConversionPrecedesHelperValidation(t *testing.T) {
	rows := []struct {
		name     string
		path     []uint16
		badKind  ChecksumKind
		badUnits []uint16
	}{
		{"storage-invalid path loses to lone-high sha1", bin174U("a*b"), ChecksumSHA1, []uint16{0xD800}},
		{"nul path loses to lone-low md5", append(bin174U("a"), 0x0000, 'b'), ChecksumMD5, []uint16{0xDC00}},
	}
	for _, r := range rows {
		desiredIn := bin174Snap()
		desiredIn.RepoPathUTF16 = r.path
		desiredIn.Checksums = append(desiredIn.Checksums,
			ChecksumEntryInput{Kind: r.badKind, Actual: r.badUnits})
		rec := &bin174Recorder{}
		res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate)
		if res != (FileDirtyUpdateResult{}) || !errors.Is(err, ErrUnrepresentableFileSnapshot) {
			t.Errorf("%s: got (%+v, %v), want zero result wrapping ErrUnrepresentableFileSnapshot", r.name, res, err)
			continue
		}
		if errors.Is(err, ErrInvalidStoragePath) || errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s: helper path error %v must not beat the conversion error", r.name, err)
		}
		if rec.calls != 0 {
			t.Errorf("%s: delegate called %d times", r.name, rec.calls)
		}
	}
	// 对照：同路径、Actual 合法 → 既有 ErrInvalidStoragePath 原样返回。
	// Control: same path, legal actuals -> the existing error, unchanged.
	desiredIn := bin174Snap()
	desiredIn.RepoPathUTF16 = bin174U("a*b")
	rec := &bin174Recorder{}
	res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate)
	if res != (FileDirtyUpdateResult{}) || !errors.Is(err, ErrInvalidStoragePath) {
		t.Errorf("control storage-invalid path: got (%+v, %v), want zero result wrapping ErrInvalidStoragePath", res, err)
	}
	if rec.calls != 0 {
		t.Errorf("control storage-invalid path: delegate called %d times", rec.calls)
	}
}

// 入口检查后才发生的取消由既有 UpdateFile 入口检查观察：转换成功、委托
// 零调用、Canceled 可达。
// A cancellation arriving after the entry checkpoint is observed by the
// existing UpdateFile entry check: conversion succeeds, zero delegate calls,
// context.Canceled reachable.
func TestBIN174GateLaterCancellationObservedByUpdateFile(t *testing.T) {
	inner, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &bin174CancelOnErrCheck{Context: inner, trigger: 2, cancel: cancel}
	desiredIn := bin174Snap()
	desiredIn.LastModifiedMillis = 201
	rec := &bin174Recorder{}
	res, err := UpdateFileIfChanged(ctx, FileDirtyUpdateInput{
		Existing: bin174MustSnap(t, bin174Snap()), Desired: bin174MustSnap(t, desiredIn), ID: 7,
	}, rec.doUpdate)
	if res != (FileDirtyUpdateResult{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("later cancellation: got (%+v, %v), want zero result wrapping context.Canceled", res, err)
	}
	if rec.calls != 0 {
		t.Fatalf("later cancellation: delegate called %d times", rec.calls)
	}
}

// ---- P9 计数直通与错误零结果。----
// ---- P9 count passthrough and the error zero result. ----

// 计数 0 与 2 原样直通（0 行成功仍 Changed=true，不转创建）；错误优先于
// 非零计数：零结果、marker 保留、updateOpError 包裹链可达。
// Counts 0 and 2 pass through verbatim (a zero-row success stays Changed=true,
// never a create); an error beats a nonzero count: zero result, marker cause
// kept, updateOpError chain reachable.
func TestBIN174GateCountPassthroughAndErrorZeroResult(t *testing.T) {
	desiredIn := bin174Snap()
	desiredIn.LastModifiedMillis = 201
	for _, row := range []struct {
		name  string
		count int64
		want  FileDirtyUpdateResult
	}{
		{"count zero success", 0, FileDirtyUpdateResult{Changed: true, Count: 0}},
		{"count two passthrough", 2, FileDirtyUpdateResult{Changed: true, Count: 2}},
	} {
		rec := &bin174Recorder{update: func(context.Context, FileReference) (int64, error) {
			return row.count, nil
		}}
		res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate)
		if err != nil || res != row.want {
			t.Errorf("%s: got (%+v, %v), want (%+v, nil)", row.name, res, err, row.want)
		}
		if rec.calls != 1 {
			t.Errorf("%s: delegate called %d times, want exactly 1", row.name, rec.calls)
		}
	}
	marker := errors.New("bin174-delegate-marker")
	rec := &bin174Recorder{update: func(context.Context, FileReference) (int64, error) {
		return 5, marker
	}}
	res, err := bin174Call(bin174MustSnap(t, bin174Snap()), bin174MustSnap(t, desiredIn), false, 7, rec.doUpdate)
	if res != (FileDirtyUpdateResult{}) || !errors.Is(err, marker) {
		t.Fatalf("error + nonzero count: got (%+v, %v), want zero result keeping the marker cause", res, err)
	}
	var opErr *updateOpError
	if !errors.As(err, &opErr) {
		t.Fatalf("error + nonzero count: existing updateOpError wrapping chain lost in %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("error + nonzero count: delegate called %d times, want exactly 1", rec.calls)
	}
}

// ---- P10 委托内取消后的成功不被追加取消检查翻转。----
// ---- P10 in-callback cancellation never flips a returned success. ----

func TestBIN174GateCallbackCancelThenSuccessPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &bin174Recorder{update: func(context.Context, FileReference) (int64, error) {
		cancel()
		return 3, nil
	}}
	desiredIn := bin174Snap()
	desiredIn.LastModifiedMillis = 201
	res, err := UpdateFileIfChanged(ctx, FileDirtyUpdateInput{
		Existing: bin174MustSnap(t, bin174Snap()), Desired: bin174MustSnap(t, desiredIn), ID: 7,
	}, rec.doUpdate)
	if err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 3}) {
		t.Fatalf("post-success cancel must not flip passthrough: got (%+v, %v)", res, err)
	}
}

// ---- P11 getter 副本所有权与并发读者（有限）。----
// ---- P11 getter-copy ownership and concurrent readers (finite). ----

// spy 修改全部 getter 副本后，重新投影的七字段与关键原始字段仍等于独立
// 期望：getter 所有权保持不可变。
// After the spy mutates every getter copy, a fresh projection and key raw
// fields still equal the independent expectations: getter ownership stays
// immutable.
func TestBIN174GateGetterCopyOwnership(t *testing.T) {
	rawSHA1Orig := append(bin174Rep('o', 60), 0xDC00, 0x0000)
	rawMD5Orig := []uint16{0xD800, 'm'}
	desiredIn := bin174Snap(bin174S1(bin174A40(t), rawSHA1Orig), bin174SMD5(bin174Rep('m', 32), rawMD5Orig))
	desiredIn.CreatedByUTF16 = bin174Rep('u', 100)
	desiredIn.LastModifiedMillis = 201
	desired := bin174MustSnap(t, desiredIn)
	rec := &bin174Recorder{update: func(_ context.Context, ref FileReference) (int64, error) {
		bin174Overwrite(ref.Metadata.CreatedBy().UTF16())
		bin174Overwrite(ref.Metadata.ModifiedBy().UTF16())
		bin174Overwrite(ref.Metadata.SHA1OriginalUTF16())
		bin174Overwrite(ref.Metadata.MD5OriginalUTF16())
		return 1, nil
	}}
	if _, err := bin174Call(bin174MustSnap(t, bin174Snap()), desired, false, 7, rec.doUpdate); err != nil {
		t.Fatalf("ownership row: %v", err)
	}
	m, err := desired.NodeMetadata()
	if err != nil {
		t.Fatalf("reproject: %v", err)
	}
	if got := m.CreatedBy().UTF16(); !bin174EqUnits(got, append(bin174Rep('u', 61), '.', '.', '.')) {
		t.Errorf("reprojected createdBy = %d units, want stable 64-unit abbreviation", len(got))
	}
	if got := m.SHA1OriginalUTF16(); !bin174EqUnits(got, rawSHA1Orig) {
		t.Errorf("reprojected sha1Original = %v, want raw units stable", got)
	}
	if got := m.MD5OriginalUTF16(); !bin174EqUnits(got, rawMD5Orig) {
		t.Errorf("reprojected md5Original = %v, want raw units stable", got)
	}
	f := desired.Fields()
	if !bin174EqUnits(f.RepoKeyUTF16, bin174U("rk")) || !bin174EqUnits(f.CreatedByUTF16, bin174Rep('u', 100)) {
		t.Errorf("desired raw fields altered after the call: %v / %v", f.RepoKeyUTF16, f.CreatedByUTF16)
	}
}

func bin174Overwrite(u []uint16) {
	for i := range u {
		u[i] = 0xFFFF
	}
}

// 并发读者：共享不可变快照上相同对跳过、变化对 {true,1}，各 goroutine
// 独立 recorder，join 后统一断言。
// Concurrent readers over shared immutable snapshots: identical skips, dirty
// yields {true,1}, per-goroutine recorders, assertions after the join.
func TestBIN174GateConcurrentReads(t *testing.T) {
	existing := bin174MustSnap(t, bin174Snap())
	dirtyIn := bin174Snap()
	dirtyIn.LastModifiedMillis = 201
	desiredDirty := bin174MustSnap(t, dirtyIn)
	var failures int32
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			equal := g%2 == 0
			rec := &bin174Recorder{}
			for i := 0; i < 16; i++ {
				rec.calls = 0
				var res FileDirtyUpdateResult
				var err error
				if equal {
					res, err = UpdateFileIfChanged(context.Background(), FileDirtyUpdateInput{
						Existing: existing, Desired: existing, ID: 9}, rec.doUpdate)
				} else {
					res, err = UpdateFileIfChanged(context.Background(), FileDirtyUpdateInput{
						Existing: existing, Desired: desiredDirty, ID: 9}, rec.doUpdate)
				}
				if equal && (err != nil || res != (FileDirtyUpdateResult{Changed: false, Count: 0}) || rec.calls != 0) {
					atomic.AddInt32(&failures, 1)
				}
				if !equal && (err != nil || res != (FileDirtyUpdateResult{Changed: true, Count: 1}) || rec.calls != 1) {
					atomic.AddInt32(&failures, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	if n := atomic.LoadInt32(&failures); n != 0 {
		t.Errorf("concurrent reads: %d failing iterations", n)
	}
}

// 原始图的三种空状态两两仍不同，写侧投影才合并为空串。
// The three empty raw states differ pairwise before string projection.
func TestBIN174GateEmptyActualStatesPairwise(t *testing.T) {
	states := []FileInfoSnapshotInput{bin174Snap(), bin174Snap(bin174S1(nil, nil)), bin174Snap(bin174S1([]uint16{}, nil))}
	for i := range states {
		for j := range states {
			rec := &bin174Recorder{}
			got, err := UpdateFileIfChanged(context.Background(), FileDirtyUpdateInput{Existing: bin174MustSnap(t, states[i]), Desired: bin174MustSnap(t, states[j]), ID: 7}, rec.doUpdate)
			if err != nil || got.Changed != (i != j) || rec.calls != map[bool]int{true: 1, false: 0}[i != j] || rec.lastRef.Content.SHA1 != "" {
				t.Errorf("states %d->%d result=%+v err=%v calls=%d", i, j, got, err, rec.calls)
			}
		}
	}
}

// 两个先前同为空的字段也必须来自 Desired，不能误取 Existing。
// Formerly equal fields must also derive from Desired rather than Existing.
func TestBIN174GateDesiredModifiedByAndMD5Original(t *testing.T) {
	before := bin174Snap(bin174SMD5(bin174U("actual"), bin174U("OLD")))
	before.ModifiedByUTF16 = bin174U("old-actor")
	after := bin174Snap(bin174SMD5(bin174U("actual"), bin174U("NEW")))
	after.ModifiedByUTF16 = bin174U("new-actor")
	rec := &bin174Recorder{}
	got, err := UpdateFileIfChanged(context.Background(), FileDirtyUpdateInput{Existing: bin174MustSnap(t, before), Desired: bin174MustSnap(t, after), ID: 7}, rec.doUpdate)
	if err != nil || !got.Changed || rec.calls != 1 || rec.lastRef.Metadata == nil {
		t.Fatalf("result=%+v err=%v calls=%d", got, err, rec.calls)
	}
	if !bin174EqUnits(rec.lastRef.Metadata.ModifiedBy().UTF16(), bin174U("new-actor")) || !bin174EqUnits(rec.lastRef.Metadata.MD5OriginalUTF16(), bin174U("NEW")) {
		t.Fatal("metadata did not derive from Desired")
	}
}

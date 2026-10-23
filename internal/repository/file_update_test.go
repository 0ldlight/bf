package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
)

// updateRecorder 记录委托调用次数与收到的引用；update 字段注入受控行为
// （错误、异常计数、取消）。
// updateRecorder counts delegate calls and captures the received reference;
// the update field injects controlled outcomes (errors, odd counts,
// cancellation).
type updateRecorder struct {
	updateCalls int
	lastRef     FileReference
	update      func(context.Context, FileReference) (int64, error)
}

func (r *updateRecorder) doUpdate(ctx context.Context, ref FileReference) (int64, error) {
	r.updateCalls++
	r.lastRef = ref
	if r.update == nil {
		return 1, nil
	}
	return r.update(ctx, ref)
}

// validUpdateInput 是合法最小输入：正 ID、可接受路径、非 nil 零快照。
// validUpdateInput is the minimal legal input: positive ID, acceptable
// path, nonnil zero snapshot.
func validUpdateInput() FileUpdateInput {
	return FileUpdateInput{ID: 7, Repo: "repo", Path: "f", Content: canonicalContent(), Metadata: &FileMetadata{}}
}

// 预取消在一切之前：委托零调用，(0, error) 且 context.Canceled 经
// errors.Is 可达；取消优先于 nil 委托与 nil Metadata 配置错误。
// Pre-cancellation wins over everything: zero delegate calls, (0, error)
// with context.Canceled reachable via errors.Is; cancellation beats both
// the nil-callback and nil-snapshot configuration errors.
func TestUpdateFilePreCancelledRejectsWithoutCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &updateRecorder{}
	count, err := UpdateFile(ctx, validUpdateInput(), rec.doUpdate)
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v does not wrap context.Canceled", err)
	}
	if rec.updateCalls != 0 {
		t.Fatalf("delegate called %d times after pre-cancel", rec.updateCalls)
	}
	in := validUpdateInput()
	in.Metadata = nil
	if _, err := UpdateFile(ctx, in, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel must precede nil callback and nil snapshot checks, got %v", err)
	}
}

// nil 委托先于 nil Metadata：固定配置错误文本、零委托调用；nil Metadata
// 是独立固定契约错误，也零调用；nil Metadata 优先于非法路径画像。
// The nil callback precedes the nil snapshot: a fixed configuration error
// text with zero delegate calls; nil Metadata is a distinct fixed contract
// error with zero calls too, and it beats the raw-path profile.
func TestUpdateFileRejectsNilCallbackBeforeNilMetadata(t *testing.T) {
	rec := &updateRecorder{}
	if count, err := UpdateFile(context.Background(), validUpdateInput(), nil); count != 0 || err == nil {
		t.Fatalf("nil callback: count=%d err=%v, want 0 and error", count, err)
	} else if err.Error() != errNilUpdateDependency.Error() {
		t.Fatalf("nil callback error text %q, want fixed %q", err.Error(), errNilUpdateDependency.Error())
	}
	if rec.updateCalls != 0 {
		t.Fatalf("delegate called %d times with nil callback", rec.updateCalls)
	}
	// nil 委托 + nil 快照：委托错误必须胜出（次序判别对）。
	// nil callback + nil snapshot: the callback error must win (order pair).
	in := validUpdateInput()
	in.Metadata = nil
	if _, err := UpdateFile(context.Background(), in, nil); err.Error() != errNilUpdateDependency.Error() {
		t.Fatalf("nil callback must precede nil snapshot, got %v", err)
	}
	// nil 快照单独拒绝：固定文本、零委托调用。
	// nil snapshot alone: fixed text, zero delegate calls.
	rec2 := &updateRecorder{}
	if count, err := UpdateFile(context.Background(), in, rec2.doUpdate); count != 0 || err == nil {
		t.Fatalf("nil snapshot: count=%d err=%v, want 0 and error", count, err)
	} else if err.Error() != errMissingUpdateSnapshot.Error() {
		t.Fatalf("nil snapshot error text %q, want fixed %q", err.Error(), errMissingUpdateSnapshot.Error())
	}
	if rec2.updateCalls != 0 {
		t.Fatalf("delegate called %d times with nil snapshot", rec2.updateCalls)
	}
	// nil 快照 + 非法 UTF-8 路径：快照缺失胜出（次序判别对）。
	// nil snapshot + invalid UTF-8 path: the missing snapshot wins (order
	// pair).
	in.Path = "a/\xffb"
	if _, err := UpdateFile(context.Background(), in, rec2.doUpdate); err.Error() != errMissingUpdateSnapshot.Error() {
		t.Fatalf("nil snapshot must precede raw profile, got %v", err)
	}
}

// 原始画像拒绝都发生在委托之前：无效 UTF-8、构造后空 repo、构造后原始
// path 内部 NUL（含父段 NUL 与仅原始名含 NUL 而 force-file 结果安全的输
// 入）。全部零委托调用，原因 ErrInvalidPath 可达；非法路径优先于非正 ID
// （载荷形状先于身份拒绝）。
// Raw-profile rejections all precede the delegate: invalid UTF-8, a repo
// blank after the construction trim, and NUL inside the original
// constructed path (parent-segment NUL plus NUL surviving only in the
// original name while the forced path itself would be safe). All make zero
// delegate calls with ErrInvalidPath reachable; an invalid path also beats
// a nonpositive ID (payload shape before identity rejection).
func TestUpdateFileRejectsRawProfileBeforeDelegate(t *testing.T) {
	cases := []struct {
		name       string
		repo, path string
	}{
		{"invalid utf8 repo", "\xff\xfe", "f"},
		{"invalid utf8 path", "repo", "a/\xffb"},
		{"empty repo", "", "f"},
		{"blank repo", " \t\r ", "f"},
		{"repo blank after nul trim", "\t\x00 ", "f"},
		{"nul mid name", "repo", "a\x00b"},
		{"nul in parent segment", "repo", "a\x00/b"},
		{"nul only in original name", "repo", "/\x00a/"},
		{"nul inside properties suffix", "repo", "a/f\x00:properties"},
	}
	for _, tc := range cases {
		in := validUpdateInput()
		in.Repo, in.Path = tc.repo, tc.path
		rec := &updateRecorder{}
		count, err := UpdateFile(context.Background(), in, rec.doUpdate)
		if count != 0 {
			t.Errorf("%s: count = %d, want 0", tc.name, count)
		}
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s: error %v does not wrap ErrInvalidPath", tc.name, err)
		}
		if rec.updateCalls != 0 {
			t.Errorf("%s: delegate called %d times on raw-profile rejection", tc.name, rec.updateCalls)
		}
	}
	// 次序判别对：非法 UTF-8 路径 + 非正 ID → 路径错误胜出。
	// Order pair: an invalid UTF-8 path with a nonpositive ID yields the
	// path error, not ErrInvalidUpdateID.
	in := validUpdateInput()
	in.Path = "a/\xffb"
	in.ID = 0
	if _, err := UpdateFile(context.Background(), in, (&updateRecorder{}).doUpdate); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("raw profile must precede the positive-ID guard, got %v", err)
	}
}

// 构造 trim 先于 NUL 判定：边界 NUL 被吸收后照常成功并映射出正确名字，
// 不能对 raw 字节中的 NUL 一刀切提前拒绝。
// The constructor trim precedes the NUL verdict: boundary NULs are absorbed
// and the update succeeds with the correctly mapped name, so a blanket
// raw-NUL early rejection is wrong.
func TestUpdateFileAcceptsConstructorTrimmedBoundaryNUL(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		wantParent string
		wantName   string
	}{
		{"trailing nul", "f\x00", "", "f"},
		{"trailing nul with parent", "a/b\x00", "a", "b"},
	}
	for _, tc := range cases {
		in := validUpdateInput()
		in.Path = tc.path
		rec := &updateRecorder{}
		count, err := UpdateFile(context.Background(), in, rec.doUpdate)
		if err != nil || count != 1 {
			t.Errorf("%s: got (%d, %v), want (1, nil)", tc.name, count, err)
			continue
		}
		if rec.lastRef.Path.Parent() != tc.wantParent || rec.lastRef.Path.Name() != tc.wantName {
			t.Errorf("%s: mapped parent/name = %q/%q, want %q/%q",
				tc.name, rec.lastRef.Path.Parent(), rec.lastRef.Path.Name(), tc.wantParent, tc.wantName)
		}
	}
}

// 校验器看到的是 force-file 后的 file toPath：'/ a/' 重建后按 'repo/a'
// 接受且映射名 'a'，反斜杠与斜杠前空白拒绝且零委托调用；校验先于非正 ID
// （次序判别对：非法存储路径 + ID<=0 → 存储路径错误胜出）。
// The validator sees the forced file toPath: '/ a/' is rebuilt, accepted as
// 'repo/a' and mapped to name 'a'; backslashes and whitespace before a
// slash reject with zero delegate calls; validation precedes the
// positive-ID guard (order pair: invalid storage path + nonpositive ID
// yields the storage error).
func TestUpdateFileValidatesForcedFileToPathBeforeIDGuard(t *testing.T) {
	rejected := []string{"a /b", "a/ b", `a\b`, "/ a/ ", "a/.", "a/.."}
	for _, path := range rejected {
		in := validUpdateInput()
		in.Path = path
		rec := &updateRecorder{}
		count, err := UpdateFile(context.Background(), in, rec.doUpdate)
		if count != 0 {
			t.Errorf("path %q: count = %d, want 0", path, count)
		}
		if !errors.Is(err, ErrInvalidStoragePath) {
			t.Errorf("path %q: error %v does not wrap ErrInvalidStoragePath", path, err)
		}
		if rec.updateCalls != 0 {
			t.Errorf("path %q: delegate called %d times on validator rejection", path, rec.updateCalls)
		}
	}
	in := validUpdateInput()
	in.Path = "/ a/"
	rec := &updateRecorder{}
	if count, err := UpdateFile(context.Background(), in, rec.doUpdate); err != nil || count != 1 {
		t.Errorf("forced '/ a/' must validate as repo/a and pass through, got (%d, %v)", count, err)
	}
	if rec.lastRef.Path.Name() != "a" || rec.lastRef.Path.Parent() != "" {
		t.Errorf("forced '/ a/' mapped parent/name = %q/%q, want %q/%q",
			rec.lastRef.Path.Parent(), rec.lastRef.Path.Name(), "", "a")
	}
	// 次序判别对：非法存储路径 + 非正 ID → 存储路径错误胜出。
	// Order pair: an invalid storage path with a nonpositive ID yields the
	// storage error, not ErrInvalidUpdateID.
	inBad := validUpdateInput()
	inBad.Path = "a /b"
	inBad.ID = -2
	if _, err := UpdateFile(context.Background(), inBad, (&updateRecorder{}).doUpdate); !errors.Is(err, ErrInvalidStoragePath) {
		t.Fatalf("validation must precede the positive-ID guard, got %v", err)
	}
}

// 成功路径上 UpdateFile 依次做 3 次 Err 检查：入口(1)、校验器后/ID 守卫前
// (2)、映射后/委托前(3)。复用 create 测试的 cancelOnErrCheck：第 2 次检查
// 取消 → 委托零调用；第 3 次取消 → 委托仍零调用且检查计数恰为 3。
// UpdateFile performs three Err checks in order on the success path: entry
// (1), after the validator/before the ID guard (2), after mapping/before
// the delegate (3). Reusing the create test's cancelOnErrCheck: cancelling
// at check 2 leaves the delegate at zero; cancelling at check 3 also leaves
// zero delegate calls with the check counter at exactly 3.
func TestUpdateFileCancellationCheckpointsBeforeIDGuardAndDelegate(t *testing.T) {
	inner1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx1 := &cancelOnErrCheck{Context: inner1, trigger: 2, cancel: cancel1}
	rec1 := &updateRecorder{}
	count, err := UpdateFile(ctx1, validUpdateInput(), rec1.doUpdate)
	if count != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before ID guard: got (%d, %v), want (0, cancelled)", count, err)
	}
	if rec1.updateCalls != 0 {
		t.Fatalf("cancel before ID guard: delegate called %d times", rec1.updateCalls)
	}
	if ctx1.calls != 2 {
		t.Fatalf("cancel before ID guard: Err checks = %d, want exactly 2 (entry + validator)", ctx1.calls)
	}

	inner2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ctx2 := &cancelOnErrCheck{Context: inner2, trigger: 3, cancel: cancel2}
	rec2 := &updateRecorder{}
	count, err = UpdateFile(ctx2, validUpdateInput(), rec2.doUpdate)
	if count != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before delegate: got (%d, %v), want (0, cancelled)", count, err)
	}
	if rec2.updateCalls != 0 {
		t.Fatalf("cancel before delegate: delegate called %d times", rec2.updateCalls)
	}
	if ctx2.calls != 3 {
		t.Fatalf("cancel before delegate: Err checks = %d, want exactly 3 (entry+validator+mapping)", ctx2.calls)
	}
}

// 非正 ID 以新导出 ErrInvalidUpdateID 拒绝：零委托调用、不重试、无查找或
// 分配；ID 守卫先于映射（次序判别对：空 metadata 名路径 + ID<=0 → ID 错误
// 胜出）。
// A nonpositive ID rejects with the newly exported ErrInvalidUpdateID: zero
// delegate calls, no retry, no lookup and no allocation; the guard
// precedes mapping (order pair: an empty-metadata-name path with a
// nonpositive ID yields the ID error).
func TestUpdateFileRejectsNonPositiveIDAfterValidation(t *testing.T) {
	for _, id := range []int64{0, -7} {
		in := validUpdateInput()
		in.ID = id
		rec := &updateRecorder{}
		count, err := UpdateFile(context.Background(), in, rec.doUpdate)
		if count != 0 || !errors.Is(err, ErrInvalidUpdateID) {
			t.Fatalf("ID=%d: got (%d, %v), want (0, ErrInvalidUpdateID)", id, count, err)
		}
		if rec.updateCalls != 0 {
			t.Fatalf("ID=%d: delegate called %d times", id, rec.updateCalls)
		}
	}
	// 次序判别对：映射期失败路径 + 非正 ID → ID 错误胜出。
	// Order pair: a mapping-failure path with a nonpositive ID yields the
	// ID error, not the mapping error.
	in := validUpdateInput()
	in.Path = "a/:properties"
	in.ID = 0
	if _, err := UpdateFile(context.Background(), in, (&updateRecorder{}).doUpdate); !errors.Is(err, ErrInvalidUpdateID) {
		t.Fatalf("positive-ID guard must precede mapping, got %v", err)
	}
}

// 映射复用既有 helper 语义与 NewPath：父路径折叠重复斜杠、名字受守卫剥离
// metadata 后缀、强制 file；剥离后空名字在 ID 守卫之后才拒绝（ErrInvalidPath）
// 且委托零调用。
// Mapping reuses the existing helper semantics and NewPath: the parent
// folds repeated slashes, the name strips guarded metadata suffixes and is
// forced to file; an empty post-strip name rejects only after the ID guard
// with ErrInvalidPath and zero delegate calls.
func TestUpdateFileMapsWithHelperSemantics(t *testing.T) {
	cases := []struct {
		name                 string
		path                 string
		wantParent, wantName string
		wantDepth            int16
	}{
		{"plain root file", "f", "", "f", 1},
		{"parent preserved verbatim", "a:properties/f", "a:properties", "f", 2},
		{"metadata name stripped once", "a/f:properties", "a", "f", 2},
		{"strip only last recognized suffix", "a/f:properties:statistics", "a", "f:properties", 2},
		{"plain colon kept", "a/f:meta", "a", "f:meta", 2},
		{"forced folder second trim", "/ a/", "", "a", 1},
		{"duplicate slashes folded in parent only", "a///b/c", "a/b", "c", 3},
	}
	for _, tc := range cases {
		in := validUpdateInput()
		in.Path = tc.path
		rec := &updateRecorder{}
		count, err := UpdateFile(context.Background(), in, rec.doUpdate)
		if err != nil || count != 1 {
			t.Errorf("%s: got (%d, %v), want (1, nil)", tc.name, count, err)
			continue
		}
		if got := rec.lastRef.Path.Parent(); got != tc.wantParent {
			t.Errorf("%s: parent = %q, want %q", tc.name, got, tc.wantParent)
		}
		if got := rec.lastRef.Path.Name(); got != tc.wantName {
			t.Errorf("%s: name = %q, want %q", tc.name, got, tc.wantName)
		}
		if depth, derr := rec.lastRef.Path.Depth(); derr != nil || depth != tc.wantDepth {
			t.Errorf("%s: depth = (%d, %v), want %d", tc.name, depth, derr, tc.wantDepth)
		}
		if !rec.lastRef.Path.IsFile() {
			t.Errorf("%s: mapped path must be a file", tc.name)
		}
	}
	// 剥离后空名字在映射处拒绝：ErrInvalidPath、委托零调用。
	// An empty post-strip name rejects at mapping: ErrInvalidPath with zero
	// delegate calls.
	in := validUpdateInput()
	in.Path = "a/:properties"
	rec := &updateRecorder{}
	if count, err := UpdateFile(context.Background(), in, rec.doUpdate); count != 0 || !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("empty metadata name: got (%d, %v), want (0, ErrInvalidPath)", count, err)
	}
	if rec.updateCalls != 0 {
		t.Fatalf("empty metadata name: delegate called %d times", rec.updateCalls)
	}
}

// 委托恰好一次：错误优先于计数并保留原因；计数原样直通——0 返回 (0,nil)、
// 2（假委托，真实 Store 因 PK 结构性不可达）返回 (2,nil)；(1, 错误) 必须
// 返回 (0, 错误)。
// The delegate runs exactly once: an error beats the count and keeps its
// cause; counts pass through verbatim — 0 yields (0, nil) and 2 (a fake
// delegate; structurally unreachable in the real Store because of the PK)
// yields (2, nil); (1, error) must return (0, error).
func TestUpdateFileDelegateOnceErrorWinsAndCountPassthrough(t *testing.T) {
	boom := errors.New("boom-update")
	rec := &updateRecorder{update: func(context.Context, FileReference) (int64, error) {
		return 0, boom
	}}
	if count, err := UpdateFile(context.Background(), validUpdateInput(), rec.doUpdate); count != 0 || !errors.Is(err, boom) {
		t.Fatalf("delegate error: got (%d, %v), want (0, err wrapping boom)", count, err)
	}
	if rec.updateCalls != 1 {
		t.Fatalf("delegate calls after error = %d, want exactly 1 (no retry)", rec.updateCalls)
	}
	// 错误优先于计数：(1, boom) 不得因计数 1 提前成功。
	// The error beats the count: (1, boom) must not return success early
	// just because the count is one.
	boomCountOne := errors.New("boom-count-one")
	recOne := &updateRecorder{update: func(context.Context, FileReference) (int64, error) {
		return 1, boomCountOne
	}}
	if count, err := UpdateFile(context.Background(), validUpdateInput(), recOne.doUpdate); count != 0 || !errors.Is(err, boomCountOne) {
		t.Fatalf("delegate (1, error): got (%d, %v), want (0, err wrapping boom-count-one)", count, err)
	}
	if recOne.updateCalls != 1 {
		t.Fatalf("delegate (1, error): calls = %d, want exactly 1", recOne.updateCalls)
	}
	for _, count := range []int64{0, 1, 2} {
		rec := &updateRecorder{update: func(context.Context, FileReference) (int64, error) {
			return count, nil
		}}
		got, err := UpdateFile(context.Background(), validUpdateInput(), rec.doUpdate)
		if err != nil || got != count {
			t.Fatalf("count=%d: got (%d, %v), want (%d, nil) verbatim passthrough", count, got, err, count)
		}
		if rec.updateCalls != 1 {
			t.Fatalf("count=%d: delegate calls = %d, want exactly 1", count, rec.updateCalls)
		}
	}
}

// 成功后不追加取消检查：委托内部取消 ctx 后返回计数，仍原样返回。
// No cancellation check follows success: a delegate cancelling the ctx and
// returning a count still passes it through verbatim.
func TestUpdateFileNoPostSuccessCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &updateRecorder{update: func(context.Context, FileReference) (int64, error) {
		cancel()
		return 3, nil
	}}
	if count, err := UpdateFile(ctx, validUpdateInput(), rec.doUpdate); count != 3 || err != nil {
		t.Fatalf("post-success cancel must not flip the passthrough: got (%d, %v)", count, err)
	}
}

// ID 原样直通（无分配/查找）、repo 经构造 trim、Content 按值透传（消费者
// 不复制旧 Store 的 canonical 校验）、Metadata 指针原样且逐字段不变；显式
// 零快照合法。
// The ID passes verbatim (no allocation, no lookup), the repo goes through
// the construction trim, Content passes by value (the consumer copies no
// legacy-Store canonical validation), and the Metadata pointer passes
// verbatim with every field unchanged; an explicit zero snapshot is valid.
func TestUpdateFilePassesIDContentMetadataVerbatim(t *testing.T) {
	meta := NewFileMetadata(FileMetadataInput{
		CreatedMillis:  -12,
		ModifiedMillis: 0,
		UpdatedMillis:  1 << 40,
		CreatedBy:      NewActor([]uint16{'a', 0xD800}),
		ModifiedBy:     NewActor(nil),
		SHA1Original:   []uint16{},
		MD5Original:    []uint16{'N', 'O', '_', 'O', 'R', 'I', 'G', 0xDC00},
	})
	// 非规范摘要：更新消费者不提前拒绝，原样到达委托（canonical 边界留
	// 在旧 Store）。
	// Noncanonical digests: the update consumer does not reject early and
	// they reach the delegate verbatim (the canonical boundary stays with
	// the legacy Store).
	nonCanonical := content.Metadata{SHA1: strings.Repeat("A", 40), SHA256: strings.Repeat("0", 64), MD5: strings.Repeat("0", 32), Size: 1}
	in := FileUpdateInput{ID: 777, Repo: " repo ", Path: "a/b", Content: nonCanonical, Metadata: &meta}
	rec := &updateRecorder{}
	if count, err := UpdateFile(context.Background(), in, rec.doUpdate); err != nil || count != 1 {
		t.Fatalf("got (%d, %v), want (1, nil)", count, err)
	}
	got := rec.lastRef
	if got.ID != 777 {
		t.Errorf("delegate ID = %d, want 777 verbatim (no allocator)", got.ID)
	}
	if got.Path.Repo() != "repo" {
		t.Errorf("delegate repo = %q, want construction-trimmed %q", got.Path.Repo(), "repo")
	}
	if got.Content != nonCanonical {
		t.Errorf("content not passed verbatim: %+v", got.Content)
	}
	if got.Metadata != &meta {
		t.Fatalf("metadata pointer changed: %p != %p", got.Metadata, &meta)
	}
	if got.Metadata.CreatedMillis() != -12 || got.Metadata.ModifiedMillis() != 0 || got.Metadata.UpdatedMillis() != 1<<40 {
		t.Errorf("times altered: %d %d %d", got.Metadata.CreatedMillis(), got.Metadata.ModifiedMillis(), got.Metadata.UpdatedMillis())
	}
	if created := got.Metadata.CreatedBy().UTF16(); len(created) != 2 || created[0] != 'a' || created[1] != 0xD800 {
		t.Errorf("created actor altered: %v", created)
	}
	if sha := got.Metadata.SHA1OriginalUTF16(); sha == nil || len(sha) != 0 {
		t.Errorf("sha1 original must stay present-empty, got %v (nil=%v)", sha, sha == nil)
	}
	if md5 := got.Metadata.MD5OriginalUTF16(); len(md5) != 8 || md5[7] != 0xDC00 {
		t.Errorf("md5 original altered: %v", md5)
	}
	// 显式零快照合法：指针非 nil、字段为零值。
	// An explicit zero snapshot is valid: nonnil pointer, zero fields.
	zero := &FileMetadata{}
	inZero := validUpdateInput()
	inZero.Metadata = zero
	recZero := &updateRecorder{}
	if count, err := UpdateFile(context.Background(), inZero, recZero.doUpdate); err != nil || count != 1 {
		t.Fatalf("zero snapshot: got (%d, %v), want (1, nil)", count, err)
	}
	if recZero.lastRef.Metadata != zero {
		t.Fatalf("zero snapshot pointer replaced: %p != %p", recZero.lastRef.Metadata, zero)
	}
	if recZero.lastRef.Metadata.CreatedMillis() != 0 || !recZero.lastRef.Metadata.ModifiedBy().IsNull() {
		t.Errorf("zero snapshot fields altered: %+v", recZero.lastRef.Metadata)
	}
}

// 错误文案固定有界：各阶段失败样本的 Error() 都落在固定集合内，绝不回显
// 路径/摘要；DeadlineExceeded 经 errors.Is 可达。
// Error texts stay fixed and bounded: every sampled failure stage lands in
// the fixed set and never echoes paths or digests; DeadlineExceeded is
// reachable via errors.Is.
func TestUpdateFileErrorsAreFixedAndBounded(t *testing.T) {
	secret := "s" + strings.Repeat("e", 512) + "cret/\x00mid"
	fixed := map[string]bool{
		"repository: update file cancelled":            true,
		errNilUpdateDependency.Error():                 true,
		errMissingUpdateSnapshot.Error():               true,
		"repository: update file invalid input path":   true,
		"repository: update file invalid storage path": true,
		"repository: update file invalid id":           true,
		"repository: update file map node path":        true,
		"repository: update file delegate":             true,
	}
	check := func(name string, err error) {
		t.Helper()
		if err == nil || !fixed[err.Error()] {
			t.Errorf("%s: error text %q outside fixed set", name, err)
		}
		if err != nil && strings.Contains(err.Error(), secret[:6]) {
			t.Errorf("%s: error text leaks input prefix", name)
		}
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := UpdateFile(cancelledCtx, validUpdateInput(), nil)
	check("pre-cancel", err)
	_, err = UpdateFile(context.Background(), validUpdateInput(), nil)
	check("nil callback", err)
	in := validUpdateInput()
	in.Metadata = nil
	_, err = UpdateFile(context.Background(), in, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("nil snapshot", err)
	in = validUpdateInput()
	in.Repo = secret
	in.Path = secret
	_, err = UpdateFile(context.Background(), in, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("invalid path", err)
	in = validUpdateInput()
	in.Path = "a /b"
	_, err = UpdateFile(context.Background(), in, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("validator", err)
	in = validUpdateInput()
	in.ID = 0
	_, err = UpdateFile(context.Background(), in, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("invalid id", err)
	in = validUpdateInput()
	in.Path = "a/:properties"
	_, err = UpdateFile(context.Background(), in, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("map", err)
	in = validUpdateInput()
	_, err = UpdateFile(context.Background(), in, func(context.Context, FileReference) (int64, error) { return 1, errors.New(secret) })
	check("delegate", err)
	deadlineCtx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	dcancel()
	_, err = UpdateFile(deadlineCtx, validUpdateInput(), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline cause not reachable: %v", err)
	}
}

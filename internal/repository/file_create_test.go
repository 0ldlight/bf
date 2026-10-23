package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
)

// createRecorder 记录窄依赖的调用次数与 insert 收到的引用；两个 func 字段
// 允许每个用例注入受控行为（错误、部分 ID、异常计数、取消）。
// createRecorder counts the narrow dependency calls and captures the
// reference handed to insert; the two func fields let each case inject
// controlled outcomes (errors, partial IDs, odd counts, cancellation).
type createRecorder struct {
	nextIDCalls int
	insertCalls int
	lastRef     FileReference
	nextID      func(context.Context) (int64, error)
	insert      func(context.Context, FileReference) (int64, error)
}

func (r *createRecorder) next(ctx context.Context) (int64, error) {
	r.nextIDCalls++
	if r.nextID == nil {
		return 41, nil
	}
	return r.nextID(ctx)
}

func (r *createRecorder) doInsert(ctx context.Context, ref FileReference) (int64, error) {
	r.insertCalls++
	r.lastRef = ref
	if r.insert == nil {
		return 1, nil
	}
	return r.insert(ctx, ref)
}

// canonicalContent 是满足旧 Store canonical 前置的固定内容元数据。
// canonicalContent is fixed content metadata meeting the legacy Store
// canonical preconditions.
func canonicalContent() content.Metadata {
	return content.Metadata{
		SHA1:   strings.Repeat("0", 40),
		SHA256: strings.Repeat("0", 64),
		MD5:    strings.Repeat("0", 32),
		Size:   7,
	}
}

// 预取消在一切之前：两个依赖零调用，(0,error) 且 context.Canceled 经
// errors.Is 可达；取消优先于 nil 依赖配置错误。
// Pre-cancellation wins over everything: zero dependency calls, (0, error)
// with context.Canceled reachable via errors.Is; cancellation also wins
// over the nil-dependency configuration error.
func TestCreateFilePreCancelledRejectsWithoutCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &createRecorder{}
	id, err := CreateFile(ctx, FileCreateInput{Repo: "repo", Path: "f"}, rec.next, rec.doInsert)
	if id != 0 {
		t.Fatalf("id = %d, want 0", id)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v does not wrap context.Canceled", err)
	}
	if rec.nextIDCalls != 0 || rec.insertCalls != 0 {
		t.Fatalf("dependency calls after pre-cancel: next=%d insert=%d", rec.nextIDCalls, rec.insertCalls)
	}
	if _, err := CreateFile(ctx, FileCreateInput{Repo: "repo", Path: "f"}, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel must precede nil dependency check, got %v", err)
	}
}

// nil nextID/insert 是固定配置错误，不调用任何依赖；文本固定。
// nil nextID/insert is a fixed configuration error with zero dependency
// calls and fixed text.
func TestCreateFileRejectsNilDependencies(t *testing.T) {
	rec := &createRecorder{}
	if id, err := CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "f"}, nil, rec.doInsert); id != 0 || err == nil {
		t.Fatalf("nil nextID: id=%d err=%v, want 0 and error", id, err)
	} else if err.Error() != errNilCreateDependency.Error() {
		t.Fatalf("nil nextID error text %q, want fixed %q", err.Error(), errNilCreateDependency.Error())
	}
	if rec.insertCalls != 0 {
		t.Fatalf("insert called %d times with nil nextID", rec.insertCalls)
	}
	rec2 := &createRecorder{}
	if id, err := CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "f"}, rec2.next, nil); id != 0 || err == nil {
		t.Fatalf("nil insert: id=%d err=%v, want 0 and error", id, err)
	}
	if rec2.nextIDCalls != 0 {
		t.Fatalf("nextID called %d times with nil insert", rec2.nextIDCalls)
	}
}

// 原始画像拒绝都发生在取 ID 之前：无效 UTF-8、构造后空 repo、构造后
// path 内部 NUL（含父段 NUL 与仅原始 name 含 NUL 而 force-file 结果安全
// 的输入）。全部零依赖调用，原因 ErrInvalidPath 可达。
// Raw-profile rejections all precede ID allocation: invalid UTF-8, a repo
// blank after construction trim, and NUL inside the constructed path
// (parent-segment NUL and NUL surviving only in the original name while
// the forced path itself would be safe). All make zero dependency calls
// with ErrInvalidPath reachable as the cause.
func TestCreateFileRejectsRawProfileBeforeAllocation(t *testing.T) {
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
		{"nul after separator", "repo", "a/\x00b"},
		{"nul only in original name", "repo", "/\x00a/"},
		{"nul only in original name with space", "repo", "/ \x00a/"},
		{"nul inside properties suffix", "repo", "a/f\x00:properties"},
		{"nul inside statistics suffix", "repo", "a/f:sta\x00tistics"},
		{"nul inside maven metadata suffix", "repo", "a/f\x00:maven-metadata.xml"},
	}
	for _, tc := range cases {
		rec := &createRecorder{}
		id, err := CreateFile(context.Background(),
			FileCreateInput{Repo: tc.repo, Path: tc.path, Content: canonicalContent()},
			rec.next, rec.doInsert)
		if id != 0 {
			t.Errorf("%s: id = %d, want 0", tc.name, id)
		}
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("%s: error %v does not wrap ErrInvalidPath", tc.name, err)
		}
		if rec.nextIDCalls != 0 || rec.insertCalls != 0 {
			t.Errorf("%s: dependency calls before allocation guard: next=%d insert=%d",
				tc.name, rec.nextIDCalls, rec.insertCalls)
		}
	}
}

// 构造 trim 先于 NUL 判定：边界 NUL 被 Java trim 移除后照常成功，不能
// 对 raw 字节中的 NUL 一刀切提前拒绝。
// Constructor trim precedes the NUL check: boundary NULs removed by the
// Java trim succeed normally, so a blanket raw-NUL early rejection is
// wrong.
func TestCreateFileAcceptsConstructorTrimmedBoundaryNUL(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		wantParent string
		wantName   string
	}{
		{"leading nul", "\x00f", "", "f"},
		{"trailing nul", "f\x00", "", "f"},
		{"trailing nul with parent", "a/b\x00", "a", "b"},
		{"nul after metadata suffix", "a/f:properties\x00", "a", "f"},
	}
	for _, tc := range cases {
		rec := &createRecorder{}
		id, err := CreateFile(context.Background(),
			FileCreateInput{Repo: "repo", Path: tc.path, Content: canonicalContent()},
			rec.next, rec.doInsert)
		if err != nil || id != 41 {
			t.Errorf("%s: got (%d, %v), want (41, nil)", tc.name, id, err)
			continue
		}
		if rec.lastRef.Path.Parent() != tc.wantParent || rec.lastRef.Path.Name() != tc.wantName {
			t.Errorf("%s: mapped parent/name = %q/%q, want %q/%q",
				tc.name, rec.lastRef.Path.Parent(), rec.lastRef.Path.Name(), tc.wantParent, tc.wantName)
		}
	}
}

// 校验器看到的是 force-file 后的 file RepoPath.toPath：'/ a/' 重建后按
// 'repo/a' 接受，而 '/ a/ ' 不重建按 'repo/ a' 拒绝；反斜杠与斜杠后空白
// 均拒绝。全部零 nextID 调用，原因 ErrInvalidStoragePath 可达。
// The validator sees the forced file RepoPath.toPath: '/ a/' is rebuilt and
// accepted as 'repo/a' while '/ a/ ' is not rebuilt and rejected as
// 'repo/ a'; backslashes and whitespace before a slash reject. All cases
// make zero nextID calls with ErrInvalidStoragePath reachable.
func TestCreateFileValidatesForcedFileToPathBeforeAllocation(t *testing.T) {
	// "a/f\x00" 的尾 NUL 被构造 trim 吸收后照常到达并通过校验器（成功
	// 路径），"/\x00a/" 在原始 getName 预检即被拒——二者都不属于校验器
	// 拒绝表。
	// The trailing NUL of "a/f\x00" is absorbed by the constructor trim
	// and the path then reaches and passes the validator (success path),
	// while "/\x00a/" is rejected at the original getName precheck —
	// neither belongs in the validator-rejection table.
	rejected := []string{"a /b", "a/ b", `a\b`, "/ a/ ", " / a/ ", "/\ta/ ",
		"a/./b", "a/../b", "a/.", "a/.."}
	for _, path := range rejected {
		rec := &createRecorder{}
		id, err := CreateFile(context.Background(),
			FileCreateInput{Repo: "repo", Path: path, Content: canonicalContent()},
			rec.next, rec.doInsert)
		if id != 0 {
			t.Errorf("path %q: id = %d, want 0", path, id)
		}
		if !errors.Is(err, ErrInvalidStoragePath) {
			t.Errorf("path %q: error %v does not wrap ErrInvalidStoragePath", path, err)
		}
		if rec.nextIDCalls != 0 || rec.insertCalls != 0 {
			t.Errorf("path %q: dependency calls on validator rejection: next=%d insert=%d",
				path, rec.nextIDCalls, rec.insertCalls)
		}
	}
	rec := &createRecorder{}
	if id, err := CreateFile(context.Background(),
		FileCreateInput{Repo: "repo", Path: "/ a/", Content: canonicalContent()},
		rec.next, rec.doInsert); err != nil || id != 41 {
		t.Errorf("forced '/ a/' must validate as repo/a and succeed, got (%d, %v)", id, err)
	}
	if rec.lastRef.Path.Name() != "a" {
		t.Errorf("forced '/ a/' mapped name %q, want %q", rec.lastRef.Path.Name(), "a")
	}
	// 反斜杠 repo 同样在校验器处拒绝（语料行 121）。
	// A backslash repo also rejects at the validator (corpus row 121).
	recBS := &createRecorder{}
	if id, err := CreateFile(context.Background(),
		FileCreateInput{Repo: `a\b`, Path: "f", Content: canonicalContent()},
		recBS.next, recBS.doInsert); id != 0 || !errors.Is(err, ErrInvalidStoragePath) {
		t.Errorf("backslash repo: got (%d, %v), want (0, ErrInvalidStoragePath)", id, err)
	}
}

// nextID 恰好一次：错误优先于任何返回 ID 并保留原因；非正 ID 以
// ErrInvalidAllocatedID 拒绝且不重取、不触 insert。
// nextID exactly once: an error beats any returned ID and keeps its cause;
// a nonpositive ID rejects with ErrInvalidAllocatedID without re-fetching
// or touching insert.
func TestCreateFileAllocatesOnceAndValidatesID(t *testing.T) {
	boom := errors.New("boom-next")
	rec := &createRecorder{nextID: func(context.Context) (int64, error) { return 55, boom }}
	id, err := CreateFile(context.Background(),
		FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
		rec.next, rec.doInsert)
	if id != 0 || !errors.Is(err, boom) {
		t.Fatalf("nextID error: got (%d, %v), want (0, err wrapping boom)", id, err)
	}
	if rec.nextIDCalls != 1 {
		t.Fatalf("nextID calls after (partialID, error) = %d, want exactly 1", rec.nextIDCalls)
	}
	if rec.insertCalls != 0 {
		t.Fatalf("insert called after nextID error")
	}
	for _, bad := range []int64{0, -3} {
		rec := &createRecorder{nextID: func(context.Context) (int64, error) { return bad, nil }}
		id, err := CreateFile(context.Background(),
			FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
			rec.next, rec.doInsert)
		if id != 0 || !errors.Is(err, ErrInvalidAllocatedID) {
			t.Fatalf("nextID=%d: got (%d, %v), want (0, ErrInvalidAllocatedID)", bad, id, err)
		}
		if rec.nextIDCalls != 1 {
			t.Fatalf("nextID=%d: nextID calls = %d, want exactly 1 (no re-fetch)", bad, rec.nextIDCalls)
		}
		if rec.insertCalls != 0 {
			t.Fatalf("nextID=%d: insert called %d times", bad, rec.insertCalls)
		}
	}
}

// 取消检查点在成功取得 ID 之后：ID 不返还、insert 零调用。
// The cancellation checkpoint after a successful ID allocation drops the
// ID with zero insert calls.
func TestCreateFileCancellationAfterAllocationDropsID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &createRecorder{nextID: func(context.Context) (int64, error) {
		cancel()
		return 77, nil
	}}
	id, err := CreateFile(ctx, FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
		rec.next, rec.doInsert)
	if id != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("got (%d, %v), want (0, cancelled)", id, err)
	}
	if rec.nextIDCalls != 1 {
		t.Fatalf("nextID calls = %d, want 1", rec.nextIDCalls)
	}
	if rec.insertCalls != 0 {
		t.Fatalf("insert called %d times after post-allocation cancel", rec.insertCalls)
	}
}

// 映射使用目标 helper 语义：父路径经 java.io.File 折叠重复斜杠与反斜杠
// 转正斜杠、不解析点段；名字经 NamingUtils 受守卫的 metadata 剥离；空
// metadata 名字在取 ID 之后才拒绝并留下 ID 空洞。
// Mapping follows the target helper semantics: the parent folds repeated
// slashes through java.io.File and turns backslashes into slashes without
// resolving dot segments; the name goes through the guarded NamingUtils
// metadata strip; an empty metadata name rejects only after the ID was
// allocated, leaving an ID hole.
func TestCreateFileMapsAfterAllocationWithHelperSemantics(t *testing.T) {
	cases := []struct {
		name                     string
		path                     string
		wantParent, wantName     string
		wantDepth                int16
		wantMappingRejectAfterID bool
	}{
		{"plain file", "f", "", "f", 1, false},
		{"parent preserved verbatim", "a:properties/f", "a:properties", "f", 2, false},
		{"metadata name stripped once", "a/f:properties", "a", "f", 2, false},
		{"strip only last recognized suffix", "a/f:properties:statistics", "a", "f:properties", 2, false},
		{"double colon keeps prefix", "a/f::properties", "a", "f:", 2, false},
		{"plain colon kept", "a/f:meta", "a", "f:meta", 2, false},
		{"case sensitive suffixes", "a/f:Properties", "a", "f:Properties", 2, false},
		{"maven metadata via colon", "a/f:maven-metadata.xml", "a", "f", 2, false},
		{"maven checksum suffix kept", "a/f:maven-metadata.xml.sha1", "a", "f:maven-metadata.xml.sha1", 2, false},
		{"no colon no strip", "a/maven-metadata.xml", "a", "maven-metadata.xml", 2, false},
		{"forced folder second trim", "/ a/", "", "a", 1, false},
		{"duplicate slashes folded in parent only", "a///b/c", "a/b", "c", 3, false},
		{"dot kept when not before slash", "a/..x/b", "a/..x", "b", 3, false},
		{"empty metadata name rejected after id", "a/:properties", "", "", 0, true},
		{"empty maven name rejected after id", "a/:maven-metadata.xml", "", "", 0, true},
	}
	for _, tc := range cases {
		rec := &createRecorder{}
		id, err := CreateFile(context.Background(),
			FileCreateInput{Repo: "repo", Path: tc.path, Content: canonicalContent()},
			rec.next, rec.doInsert)
		if tc.wantMappingRejectAfterID {
			if id != 0 || !errors.Is(err, ErrInvalidPath) {
				t.Errorf("%s: got (%d, %v), want (0, ErrInvalidPath)", tc.name, id, err)
			}
			if rec.nextIDCalls != 1 {
				t.Errorf("%s: nextID calls = %d, want 1 (mapping failure after allocation)", tc.name, rec.nextIDCalls)
			}
			if rec.insertCalls != 0 {
				t.Errorf("%s: insert called %d times on mapping failure", tc.name, rec.insertCalls)
			}
			continue
		}
		if err != nil || id != 41 {
			t.Errorf("%s: got (%d, %v), want (41, nil)", tc.name, id, err)
			continue
		}
		if got := rec.lastRef.Path.Parent(); got != tc.wantParent {
			t.Errorf("%s: parent = %q, want %q", tc.name, got, tc.wantParent)
		}
		if got := rec.lastRef.Path.Name(); got != tc.wantName {
			t.Errorf("%s: name = %q, want %q", tc.name, got, tc.wantName)
		}
		depth, derr := rec.lastRef.Path.Depth()
		if derr != nil || depth != tc.wantDepth {
			t.Errorf("%s: depth = (%d, %v), want %d", tc.name, depth, derr, tc.wantDepth)
		}
	}
}

// insert 恰好一次：错误保留原始原因；计数必须严格等于 1，0 或 2 都是
// ErrUnexpectedCreateCount；成功后不再追加取消检查（insert 内取消仍返回
// 成功，且成功只在调用方事务内临时成立）。
// insert exactly once: errors keep their original cause; the count must be
// strictly 1 — 0 or 2 both yield ErrUnexpectedCreateCount; no cancellation
// check is appended after success (an insert-time cancel still returns
// success, which stays provisional inside the caller transaction).
func TestCreateFileInsertOnceStrictCountNoRetry(t *testing.T) {
	boom := errors.New("boom-insert")
	rec := &createRecorder{insert: func(context.Context, FileReference) (int64, error) {
		return 0, boom
	}}
	if id, err := CreateFile(context.Background(),
		FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
		rec.next, rec.doInsert); id != 0 || !errors.Is(err, boom) {
		t.Fatalf("insert error: got (%d, %v), want (0, err wrapping boom)", id, err)
	}
	if rec.insertCalls != 1 {
		t.Fatalf("insert calls after error = %d, want exactly 1 (no retry)", rec.insertCalls)
	}
	// 负计数与 0/2 一样属于严格意外：必须同样拒绝且不重试。
	// A negative count is exactly as unexpected as 0 or 2: the same
	// strict rejection applies with no retry.
	for _, count := range []int64{0, 2, -1} {
		rec := &createRecorder{insert: func(context.Context, FileReference) (int64, error) {
			return count, nil
		}}
		id, err := CreateFile(context.Background(),
			FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
			rec.next, rec.doInsert)
		if id != 0 || !errors.Is(err, ErrUnexpectedCreateCount) {
			t.Fatalf("count=%d: got (%d, %v), want (0, ErrUnexpectedCreateCount)", count, id, err)
		}
		if rec.insertCalls != 1 {
			t.Fatalf("count=%d: insert calls = %d, want exactly 1 (no upsert/lookup path)", count, rec.insertCalls)
		}
	}
	// insert 返回 (1, 错误)：错误优先于计数——ID 必须为 0、原因保留、两个
	// 回调各恰好一次。专门杀死"count==1 即先成功返回"的错误实现。
	// insert returns (1, error): the error beats the count — the ID must
	// be 0, the cause survives, and both callbacks run exactly once. This
	// specifically kills a wrong implementation returning success as soon
	// as count==1, before consulting the callback error.
	boomCountOne := errors.New("boom-count-one")
	recOne := &createRecorder{insert: func(context.Context, FileReference) (int64, error) {
		return 1, boomCountOne
	}}
	if id, err := CreateFile(context.Background(),
		FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
		recOne.next, recOne.doInsert); id != 0 || !errors.Is(err, boomCountOne) {
		t.Fatalf("insert (1, error): got (%d, %v), want (0, err wrapping boom-count-one)", id, err)
	}
	if recOne.nextIDCalls != 1 || recOne.insertCalls != 1 {
		t.Fatalf("insert (1, error): next=%d insert=%d, want 1/1", recOne.nextIDCalls, recOne.insertCalls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	recOk := &createRecorder{insert: func(context.Context, FileReference) (int64, error) {
		cancel()
		return 1, nil
	}}
	if id, err := CreateFile(ctx, FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
		recOk.next, recOk.doInsert); id != 41 || err != nil {
		t.Fatalf("post-success cancel must not flip success: got (%d, %v)", id, err)
	}
}

// cancelOnErrCheck 在第 trigger 次 Err() 检查时取消底层真实 context；
// Done/Err 全程委托真实 context，取消后二者自然一致。这让两个此前只能
// 靠读源码确认的批准取消检查点（校验器后/nextID 前、映射后/insert 前）
// 可以在同一 goroutine 内同步、确定性地触发——无 sleep、无额外
// goroutine、无产品钩子。
// cancelOnErrCheck cancels the underlying real context at the trigger-th
// Err() check; Done/Err always delegate to the real context, so the two
// stay consistent once cancelled. This triggers the two approved
// cancellation checkpoints previously visible only by reading the source
// (after the validator/before nextID and after mapping/before insert)
// synchronously and deterministically on one goroutine — no sleeps, no
// extra goroutines, no product hooks.
type cancelOnErrCheck struct {
	context.Context
	trigger int
	calls   int
	cancel  context.CancelFunc
}

func (c *cancelOnErrCheck) Err() error {
	c.calls++
	if c.calls == c.trigger {
		c.cancel()
	}
	return c.Context.Err()
}

// 成功路径上 CreateFile 依次做 4 次 Err 检查：入口(1)、校验器后/nextID
// 前(2)、取 ID 后/映射前(3)、映射后/insert 前(4)。本测试分别在第 2 次
// 与第 4 次检查时取消：前者两个回调都必须保持零调用；后者 nextID 恰好
// 消费一次而 insert 零调用。既有的预取消/取 ID 后/成功后用例保留。
// CreateFile performs four Err checks in order on the success path:
// entry (1), after the validator/before nextID (2), after the allocated
// ID/before mapping (3), after mapping/before insert (4). This test
// cancels at the 2nd and the 4th check respectively: the former must
// leave both callbacks at zero; the latter must consume nextID exactly
// once while insert stays at zero. The existing pre-cancel, post-ID and
// post-success cases remain in place.
func TestCreateFileCancellationCheckpointsBeforeNextIDAndInsert(t *testing.T) {
	inner1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx1 := &cancelOnErrCheck{Context: inner1, trigger: 2, cancel: cancel1}
	rec1 := &createRecorder{}
	id, err := CreateFile(ctx1, FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
		rec1.next, rec1.doInsert)
	if id != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before nextID: got (%d, %v), want (0, cancelled)", id, err)
	}
	if rec1.nextIDCalls != 0 || rec1.insertCalls != 0 {
		t.Fatalf("cancel before nextID: next=%d insert=%d, want 0/0", rec1.nextIDCalls, rec1.insertCalls)
	}
	if ctx1.calls != 2 {
		t.Fatalf("cancel before nextID: Err checks = %d, want exactly 2 (entry + validator)", ctx1.calls)
	}

	inner2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ctx2 := &cancelOnErrCheck{Context: inner2, trigger: 4, cancel: cancel2}
	rec2 := &createRecorder{}
	id, err = CreateFile(ctx2, FileCreateInput{Repo: "repo", Path: "f", Content: canonicalContent()},
		rec2.next, rec2.doInsert)
	if id != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before insert: got (%d, %v), want (0, cancelled)", id, err)
	}
	if rec2.nextIDCalls != 1 || rec2.insertCalls != 0 {
		t.Fatalf("cancel before insert: next=%d insert=%d, want 1/0", rec2.nextIDCalls, rec2.insertCalls)
	}
	if ctx2.calls != 4 {
		t.Fatalf("cancel before insert: Err checks = %d, want exactly 4 (entry+validator+id+mapping)", ctx2.calls)
	}
}

// Metadata 原样透传：nil 保持 nil（区别于零快照）；非 nil 传同一指针且
// 三个时间、两个 Actor 与两个 original 逐字段不变；Content 按值复制。
// Metadata passes through verbatim: nil stays nil (distinct from a zero
// snapshot); a nonnil value keeps the same pointer with the three times,
// two actors and two originals unchanged field by field; Content copies
// by value.
func TestCreateFilePassesMetadataAndContentVerbatim(t *testing.T) {
	meta := NewFileMetadata(FileMetadataInput{
		CreatedMillis:  -12,
		ModifiedMillis: 0,
		UpdatedMillis:  1 << 40,
		CreatedBy:      NewActor([]uint16{'a', 0xD800}),
		ModifiedBy:     NewActor(nil),
		SHA1Original:   []uint16{},
		MD5Original:    []uint16{'N', 'O', '_', 'O', 'R', 'I', 'G', 0xDC00},
	})
	want := content.Metadata{
		SHA1:   strings.Repeat("a", 40),
		SHA256: strings.Repeat("b", 64),
		MD5:    strings.Repeat("c", 32),
		Size:   -0,
	}
	rec := &createRecorder{}
	if id, err := CreateFile(context.Background(),
		FileCreateInput{Repo: "repo", Path: "f", Content: want, Metadata: &meta},
		rec.next, rec.doInsert); err != nil || id != 41 {
		t.Fatalf("got (%d, %v)", id, err)
	}
	got := rec.lastRef
	if got.Content != want {
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
	if !got.Metadata.ModifiedBy().IsNull() {
		t.Errorf("modified actor altered: %+v", got.Metadata.ModifiedBy())
	}
	if sha := got.Metadata.SHA1OriginalUTF16(); sha == nil || len(sha) != 0 {
		t.Errorf("sha1 original must stay present-empty, got %v (nil=%v)", sha, sha == nil)
	}
	wantMD5 := []uint16{'N', 'O', '_', 'O', 'R', 'I', 'G', 0xDC00}
	if md5 := got.Metadata.MD5OriginalUTF16(); len(md5) != len(wantMD5) {
		t.Errorf("md5 original altered: %v", md5)
	} else {
		for i, u := range md5 {
			if u != wantMD5[i] {
				t.Errorf("md5 original altered at %d: %v", i, md5)
				break
			}
		}
	}
	recNil := &createRecorder{}
	if _, err := CreateFile(context.Background(),
		FileCreateInput{Repo: "repo", Path: "f", Content: want},
		recNil.next, recNil.doInsert); err != nil {
		t.Fatalf("nil metadata case: %v", err)
	}
	if recNil.lastRef.Metadata != nil {
		t.Errorf("nil metadata became nonnil %v", recNil.lastRef.Metadata)
	}
}

// canonical 三摘要前置留给旧 Store：非规范 Content 仍先取 ID，仅在
// insert（真实 Store 边界）处失败，错误保留且不重试。
// The canonical three-digest precondition stays with the legacy Store:
// noncanonical Content still allocates the ID first and fails only at
// insert (the real Store boundary), preserving the cause without retry.
func TestCreateFileLeavesCanonicalChecksToStoreAfterID(t *testing.T) {
	nonCanonical := content.Metadata{SHA1: strings.Repeat("A", 40), SHA256: strings.Repeat("0", 64), MD5: strings.Repeat("0", 32), Size: 1}
	storeReject := errors.New("store-canonical-reject")
	rec := &createRecorder{insert: func(context.Context, FileReference) (int64, error) {
		return 0, storeReject
	}}
	id, err := CreateFile(context.Background(),
		FileCreateInput{Repo: "repo", Path: "f", Content: nonCanonical},
		rec.next, rec.doInsert)
	if id != 0 || !errors.Is(err, storeReject) {
		t.Fatalf("got (%d, %v), want (0, store cause preserved)", id, err)
	}
	if rec.nextIDCalls != 1 {
		t.Fatalf("nextID calls = %d, want 1: canonical must not move before allocation", rec.nextIDCalls)
	}
	if rec.insertCalls != 1 || rec.lastRef.Content != nonCanonical {
		t.Fatalf("insert calls = %d, content %+v: Store boundary must receive the reference once", rec.insertCalls, rec.lastRef.Content)
	}
}

// 错误文案固定有界：多阶段失败样本的 Error() 都落在固定集合内，绝不
// 回显路径/摘要/actor；DeadlineExceeded 经 errors.Is 可达。
// Error texts stay fixed and bounded: every sampled failure stage lands in
// the fixed set and never echoes paths/digests/actors; DeadlineExceeded is
// reachable via errors.Is.
func TestCreateFileErrorsAreFixedAndBounded(t *testing.T) {
	secret := "s" + strings.Repeat("e", 512) + "cret/\x00mid"
	fixed := map[string]bool{
		"repository: create file cancelled":               true,
		errNilCreateDependency.Error():                    true,
		"repository: create file invalid input path":      true,
		"repository: create file invalid storage path":    true,
		"repository: create file allocate node id":        true,
		"repository: create file invalid allocated id":    true,
		"repository: create file map node path":           true,
		"repository: create file insert":                  true,
		"repository: create file unexpected insert count": true,
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
	_, err := CreateFile(cancelledCtx, FileCreateInput{Repo: secret, Path: "f"}, nil, nil)
	check("pre-cancel", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: secret, Path: "f"}, nil, nil)
	check("nil dep", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: secret, Path: secret}, func(context.Context) (int64, error) { return 1, nil }, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("invalid path", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "a /b"},
		func(context.Context) (int64, error) { return 1, nil }, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("validator", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "f"},
		func(context.Context) (int64, error) { return 9, errors.New(secret) }, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("allocate", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "f"},
		func(context.Context) (int64, error) { return 0, nil }, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("allocated id", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "a/:properties"},
		func(context.Context) (int64, error) { return 5, nil }, func(context.Context, FileReference) (int64, error) { return 1, nil })
	check("map", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "f"},
		func(context.Context) (int64, error) { return 5, nil }, func(context.Context, FileReference) (int64, error) { return 0, errors.New(secret) })
	check("insert", err)
	_, err = CreateFile(context.Background(), FileCreateInput{Repo: "repo", Path: "f"},
		func(context.Context) (int64, error) { return 5, nil }, func(context.Context, FileReference) (int64, error) { return 3, nil })
	check("count", err)
	deadlineCtx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	dcancel()
	_, err = CreateFile(deadlineCtx, FileCreateInput{Repo: "repo", Path: "f"}, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline cause not reachable: %v", err)
	}
}

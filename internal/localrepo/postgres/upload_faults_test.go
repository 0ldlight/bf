// 懒 PG 上传保存缝的故障路径单元契约：权威回读失配、提交故障、父 ctx
// 提交后取消、节点写入/号段读取故障与负时钟采样；每个失败返回零 Source、
// 固定文案并保留原因链。
// Fault-path unit contract for the lazy PG upload save seam: authoritative
// re-read mismatches, commit faults, parent cancellation after commit, node
// write and id-read faults plus a negative clock sample; every failure
// returns the zero Source, a fixed text and a preserved cause chain.
package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

var errUploadFakeCommit = errors.New("postgres upload test: commit fault")
var errUploadFakeIDRead = errors.New("postgres upload test: id read fault")
var errUploadFakeIDWrite = errors.New("postgres upload test: id write fault")
var errUploadFakeNodeWrite = errors.New("postgres upload test: node write fault")
var errUploadFakeReread = errors.New("postgres upload test: authoritative lookup fault")

// uploadCreateQueryHook 返回按参数可变的节点回读行；第一查缺行。
// uploadCreateQueryHook serves a mutable authoritative node row; the first
// node lookup misses.
func uploadCreateQueryHook(path repository.Path, row func() []driver.Value, idErr error) func(string, []driver.NamedValue) ([]string, [][]driver.Value, error) {
	nodeCalls := 0
	return func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM unique_ids"):
			if idErr != nil {
				return nil, nil, idErr
			}
			return uploadIDCols, [][]driver.Value{{int64(5)}}, nil
		case strings.Contains(query, "FROM nodes"):
			nodeCalls++
			if nodeCalls == 1 {
				return uploadNodeCols, nil, nil
			}
			return uploadNodeCols, [][]driver.Value{row()}, nil
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
}

func uploadCreateExecHook(nodeResult func() (int64, error), idResult func() (int64, error)) func(string, []driver.NamedValue) (int64, error) {
	return func(query string, _ []driver.NamedValue) (int64, error) {
		switch {
		case strings.Contains(query, "INSERT INTO binaries"):
			return 1, nil
		case strings.Contains(query, "UPDATE unique_ids"):
			if idResult != nil {
				return idResult()
			}
			return 1, nil
		case strings.Contains(query, "INSERT INTO nodes"):
			if nodeResult != nil {
				return nodeResult()
			}
			return 1, nil
		default:
			return 0, errUploadFakeExec
		}
	}
}

func TestSaveAuthoritativeMismatchRejected(t *testing.T) {
	stamp := int64(1_700_000_000_000)
	cases := []struct {
		name     string
		row      func(fresh content.Metadata, path repository.Path) []driver.Value
		declared func(len int64) int64
		binaries func(fresh content.Metadata) [][]driver.Value
		want     string
	}{
		{
			name: "identity mismatch",
			row: func(fresh content.Metadata, path repository.Path) []driver.Value {
				return uploadNodeRow(7, path, fresh.Size, fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)
			},
			declared: func(n int64) int64 { return n },
			want:     "postgres upload: authoritative identity mismatch",
		},
		{
			name: "content mismatch",
			row: func(fresh content.Metadata, path repository.Path) []driver.Value {
				return uploadNodeRow(6, path, fresh.Size, fresh.SHA1, strings.Repeat("e", 32), fresh.SHA256, stamp, stamp, stamp)
			},
			declared: func(n int64) int64 { return n },
			want:     "postgres upload: authoritative content mismatch",
		},
		{
			name: "declared size mismatch",
			row: func(fresh content.Metadata, path repository.Path) []driver.Value {
				// 回读与权威内容一致；不一致的是声明大小与登记返回值。
				// The re-read agrees with the authoritative content;
				// the declared size disagrees with the registration.
				return uploadNodeRow(6, path, int64(42), fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)
			},
			declared: func(n int64) int64 { return n },
			binaries: func(fresh content.Metadata) [][]driver.Value {
				return [][]driver.Value{{fresh.SHA1, fresh.MD5, int64(42), fresh.SHA256}}
			},
			want: "postgres upload: declared size mismatch",
		},
		{
			name: "source invalid actor",
			row: func(fresh content.Metadata, path repository.Path) []driver.Value {
				row := uploadNodeRow(6, path, fresh.Size, fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)
				row[12] = nil // created_by SQL NULL → null Actor。
				return row
			},
			declared: func(n int64) int64 { return n },
			want:     "postgres upload: authoritative source invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &uploadFakeHub{}
			saver := uploadSuccessSaver(t, h, stamp)
			body := "mismatch probe body\n"
			fresh := uploadFreshCalculation(t, body)
			input := uploadTestInput(t, tc.declared(int64(len(body))))
			row := tc.row
			h.query = uploadCreateQueryHook(input.Path, func() []driver.Value {
				return row(fresh, input.Path)
			}, nil)
			if tc.binaries != nil {
				binaries := tc.binaries(fresh)
				base := h.query
				h.query = func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
					if strings.Contains(query, "FROM binaries") {
						return uploadBinCols, binaries, nil
					}
					return base(query, args)
				}
			}
			h.exec = uploadCreateExecHook(nil, nil)

			source, err := saver.Save(context.Background(), strings.NewReader(body), input)
			if err == nil {
				t.Fatal("authoritative mismatch must fail the save")
			}
			if err.Error() != tc.want {
				t.Fatalf("Error() = %q, want %q", err.Error(), tc.want)
			}
			if !errors.Is(err, localrepo.ErrUploadPersistence) {
				t.Fatalf("mismatch error = %v, want ErrUploadPersistence marker", err)
			}
			if source != (repository.FileSnapshotSource{}) {
				t.Fatalf("failed save returned nonzero source %+v", source)
			}
			if _, _, rollbacks := h.snapshot(); rollbacks != 1 {
				t.Fatalf("rollbacks = %d, want 1 (node tx rolled back)", rollbacks)
			}
		})
	}
}

func TestSaveCommitFaultFailsWithPersistenceMark(t *testing.T) {
	h := &uploadFakeHub{}
	stamp := int64(1_700_000_000_000)
	saver := uploadSuccessSaver(t, h, stamp)
	body := "commit fault body\n"
	fresh := uploadFreshCalculation(t, body)
	input := uploadTestInput(t, int64(len(body)))
	uploadCreateHooks(h, input.Path, body, fresh, stamp, 6)
	// 只让第三次提交（节点事务）失败：登记与号段预留必须正常提交。
	// Fail only the third commit (the node tx): the registry and the
	// reservation commits must both succeed first.
	h.mu.Lock()
	h.onCommit = func() {
		if h.commitCount() == 2 {
			h.mu.Lock()
			h.commitErr = errUploadFakeCommit
			h.mu.Unlock()
		}
	}
	h.mu.Unlock()

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err == nil {
		t.Fatal("commit fault must fail the save")
	}
	if err.Error() != "postgres upload: node commit" {
		t.Fatalf("Error() = %q, want the fixed commit text", err.Error())
	}
	if !errors.Is(err, errUploadFakeCommit) || !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("commit fault cause lost: %v", err)
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
	// Commit 已尝试：事务完结，回滚为 ErrTxDone 空操作。
	// The commit was attempted: the tx is finalized and the rollback is
	// a no-op ErrTxDone.
	if _, _, rollbacks := h.snapshot(); rollbacks != 0 {
		t.Fatalf("rollbacks = %d, want 0 (tx finalized after commit attempt)", rollbacks)
	}
}

func TestSaveParentCancelledAfterCommitReturnsZeroSource(t *testing.T) {
	h := &uploadFakeHub{}
	stamp := int64(1_700_000_000_000)
	saver := uploadSuccessSaver(t, h, stamp)
	body := "parent cancel body\n"
	fresh := uploadFreshCalculation(t, body)
	input := uploadTestInput(t, int64(len(body)))
	uploadCreateHooks(h, input.Path, body, fresh, stamp, 6)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 在驱动层提交成功的那一刻取消父 ctx：提交仍算成功，但父 ctx 已死。
	// Cancel the parent exactly when the driver commit succeeds: the
	// commit still counts, but the parent is already dead.
	h.mu.Lock()
	h.onCommit = func() {
		if h.commitCount() == 3 {
			cancel()
		}
	}
	h.mu.Unlock()

	source, err := saver.Save(ctx, strings.NewReader(body), input)
	if err == nil {
		t.Fatal("parent cancellation after commit must fail the save")
	}
	if err.Error() != "postgres upload: parent cancelled after commit" {
		t.Fatalf("Error() = %q, want the parent-after-commit text", err.Error())
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cause = %v, want context.Canceled", err)
	}
	if errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatal("parent cancellation must not be classified as a persistence fault")
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("cancelled save returned nonzero source %+v", source)
	}
	if _, _, rollbacks := h.snapshot(); rollbacks != 0 {
		t.Fatalf("rollbacks = %d, want 0 (committed tx never rolled back)", rollbacks)
	}
	if commits := h.commitCount(); commits != 3 {
		t.Fatalf("commits = %d, want 3 (all three transactions committed)", commits)
	}
}

func TestSaveNodeWriteFaultsMarkedPersistence(t *testing.T) {
	stamp := int64(1_700_000_000_000)
	cases := []struct {
		name       string
		updateFlow bool
		nodeExec   func() (int64, error)
		want       string
	}{
		{
			name:     "create write fault",
			nodeExec: func() (int64, error) { return 0, errUploadFakeNodeWrite },
			want:     "postgres upload: node create",
		},
		{
			name:       "update write fault",
			updateFlow: true,
			nodeExec:   func() (int64, error) { return 0, errUploadFakeNodeWrite },
			want:       "postgres upload: node update",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &uploadFakeHub{}
			saver := uploadSuccessSaver(t, h, stamp)
			body := "node write fault body\n"
			fresh := uploadFreshCalculation(t, body)
			old := uploadFreshCalculation(t, "old fault body\n")
			input := uploadTestInput(t, int64(len(body)))
			if tc.updateFlow {
				h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
					switch {
					case strings.Contains(query, "FROM binaries"):
						return uploadBinCols, nil, nil
					case strings.Contains(query, "FROM nodes"):
						return uploadNodeCols, [][]driver.Value{uploadNodeRow(31, input.Path, old.Size,
							old.SHA1, old.MD5, old.SHA256, stamp, stamp, stamp)}, nil
					default:
						return nil, nil, errUploadFakeQuery
					}
				}
				h.exec = func(query string, _ []driver.NamedValue) (int64, error) {
					if strings.Contains(query, "INSERT INTO binaries") {
						return 1, nil
					}
					if strings.Contains(query, "UPDATE nodes") {
						return tc.nodeExec()
					}
					return 0, errUploadFakeExec
				}
			} else {
				h.query = uploadCreateQueryHook(input.Path, func() []driver.Value {
					return uploadNodeRow(6, input.Path, fresh.Size, fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)
				}, nil)
				h.exec = uploadCreateExecHook(tc.nodeExec, nil)
			}

			source, err := saver.Save(context.Background(), strings.NewReader(body), input)
			if err == nil {
				t.Fatal("node write fault must fail the save")
			}
			// 外层是 localrepo 的节点保存文案；适配器阶段文案保留在链条内。
			// The outer text is localrepo's node-save attribution; the
			// adapter's stage text stays in the chain.
			if !uploadChainContains(err, tc.want) {
				t.Fatalf("stage text %q lost from the chain: %v", tc.want, err)
			}
			if !errors.Is(err, errUploadFakeNodeWrite) || !errors.Is(err, localrepo.ErrUploadPersistence) {
				t.Fatalf("node write fault cause lost: %v", err)
			}
			if source != (repository.FileSnapshotSource{}) {
				t.Fatalf("failed save returned nonzero source %+v", source)
			}
			if _, _, rollbacks := h.snapshot(); rollbacks != 1 {
				t.Fatalf("rollbacks = %d, want 1", rollbacks)
			}
		})
	}
}

func TestSaveAuthoritativeLookupFault(t *testing.T) {
	h := &uploadFakeHub{}
	stamp := int64(1_700_000_000_000)
	saver := uploadSuccessSaver(t, h, stamp)
	body := "reread fault body\n"
	input := uploadTestInput(t, int64(len(body)))
	nodeCalls := 0
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM unique_ids"):
			return uploadIDCols, [][]driver.Value{{int64(5)}}, nil
		case strings.Contains(query, "FROM nodes"):
			nodeCalls++
			if nodeCalls == 1 {
				return uploadNodeCols, nil, nil
			}
			return nil, nil, errUploadFakeReread
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	h.exec = uploadCreateExecHook(nil, nil)

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err == nil {
		t.Fatal("authoritative lookup fault must fail the save")
	}
	if err.Error() != "postgres upload: authoritative lookup" {
		t.Fatalf("Error() = %q, want the authoritative lookup text", err.Error())
	}
	if !errors.Is(err, errUploadFakeReread) || !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("authoritative lookup cause lost: %v", err)
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
}

// uploadChainContains 报告错误树的某层是否出现精确文案（阶段归因在多层
// 包装下必须原样保留）。
// uploadChainContains reports whether an exact stage text survives anywhere
// in the wrapped error tree (multi-layer attribution stays verbatim).
func uploadChainContains(err error, want string) bool {
	if err == nil {
		return false
	}
	if err.Error() == want {
		return true
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range multi.Unwrap() {
			if uploadChainContains(child, want) {
				return true
			}
		}
		return false
	}
	return uploadChainContains(errors.Unwrap(err), want)
}

func TestSaveIDAllocationFaults(t *testing.T) {
	stamp := int64(1_700_000_000_000)
	cases := []struct {
		name      string
		idQuery   error
		idExec    func() (int64, error)
		cause     error
		want      string
		rollbacks int
	}{
		{
			name:      "current read fault",
			idQuery:   errUploadFakeIDRead,
			cause:     errUploadFakeIDRead,
			want:      "postgres upload: id current bound",
			rollbacks: 1,
		},
		{
			name:   "reserve write fault",
			idExec: func() (int64, error) { return 0, errUploadFakeIDWrite },
			cause:  errUploadFakeIDWrite,
			want:   "postgres upload: id range reserve",
			// 预留事务自身的兜底回滚 + 节点事务回滚。
			// The reservation tx's own safety-net rollback plus the
			// node tx rollback.
			rollbacks: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &uploadFakeHub{}
			saver := uploadSuccessSaver(t, h, stamp)
			body := "id fault body\n"
			fresh := uploadFreshCalculation(t, body)
			input := uploadTestInput(t, int64(len(body)))
			h.query = uploadCreateQueryHook(input.Path, func() []driver.Value {
				return uploadNodeRow(6, input.Path, fresh.Size, fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)
			}, tc.idQuery)
			var idResult func() (int64, error)
			if tc.idExec != nil {
				idResult = tc.idExec
			}
			h.exec = uploadCreateExecHook(nil, idResult)

			source, err := saver.Save(context.Background(), strings.NewReader(body), input)
			if err == nil {
				t.Fatal("id allocation fault must fail the save")
			}
			// 外层是 localrepo 的节点保存文案；适配器阶段文案必须在链条内保留。
			// The outer text is localrepo's node-save attribution; the
			// adapter's own stage text must survive in the chain.
			if !uploadChainContains(err, tc.want) {
				t.Fatalf("stage text %q lost from the chain: %v", tc.want, err)
			}
			if !errors.Is(err, tc.cause) || !errors.Is(err, localrepo.ErrUploadPersistence) {
				t.Fatalf("id fault cause lost: %v", err)
			}
			if source != (repository.FileSnapshotSource{}) {
				t.Fatalf("failed save returned nonzero source %+v", source)
			}
			if _, _, rollbacks := h.snapshot(); rollbacks != tc.rollbacks {
				t.Fatalf("rollbacks = %d, want %d", rollbacks, tc.rollbacks)
			}
		})
	}
}

func TestSaveNegativeClockStampRejectedBeforeReading(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	clock := &uploadStubClock{value: -1}
	saver := uploadTestSaver(t, db, clock, 5*time.Second)
	body := "negative clock body\n"
	reader := &uploadProofReader{r: strings.NewReader(body), beginCount: h.beginCount, clockCallCount: clock.callCount}

	_, err := saver.Save(context.Background(), reader, uploadTestInput(t, int64(len(body))))
	if err == nil {
		t.Fatal("negative clock sample must fail")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("negative clock error = %v, want ErrUploadPersistence", err)
	}
	if reader.reads != 0 {
		t.Fatalf("body read %d times after clock rejection, want 0", reader.reads)
	}
	if connects, begins, _ := h.snapshot(); connects != 0 || begins != 0 {
		t.Fatalf("negative clock touched DB (connects=%d begins=%d), want 0/0", connects, begins)
	}
}

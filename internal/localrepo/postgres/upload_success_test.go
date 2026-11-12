// 懒 PG 上传保存缝的成功路径单元契约：fake driver 完整驱动“登记事务 →
// 节点事务 → 号段预留事务”三段真实流程，覆盖新建、更新与零变更跳过分支
// 及其参数绑定；真实 PostgreSQL 行为归 integration/postgres。
// Success-path unit contract for the lazy PG upload save seam: the fake
// driver drives the full three-transaction flow (registry, node and id
// reservation) covering the create, update and zero-change skip branches
// with their parameter bindings; real PostgreSQL behavior belongs to
// integration/postgres.
package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// uploadFreshCalculation 计算正文的真实内容元数据。
// uploadFreshCalculation computes the body's real content metadata.
func uploadFreshCalculation(t *testing.T, body string) content.Metadata {
	t.Helper()
	fresh, err := content.Calculate(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatalf("content.Calculate: %v", err)
	}
	return fresh
}

// uploadNodeRow 按输入路径构造节点查询行：空父路径按 SQL 点哨兵存储，
// original 保持 SQL NULL。
// uploadNodeRow builds one node lookup row for the input path: an empty
// parent is stored as the SQL dot sentinel and originals stay SQL NULL.
func uploadNodeRow(id int64, path repository.Path, length int64, sha1, md5, sha256 string, created, modified, updated int64) []driver.Value {
	parent := path.Parent()
	if parent == "" {
		parent = "."
	}
	return []driver.Value{id, int64(1), path.Repo(), parent, path.Name(), length, sha1, md5, sha256,
		created, modified, updated, "op-user", "op-user", nil, nil}
}

// uploadSuccessSaver 返回固定时钟的成功路径 saver。
// uploadSuccessSaver returns a success-path saver with a fixed clock.
func uploadSuccessSaver(t *testing.T, h *uploadFakeHub, stamp int64) *UploadSaver {
	t.Helper()
	db := uploadFakeDB(t, h, 2)
	clock := &uploadStubClock{value: stamp}
	return uploadTestSaver(t, db, clock, 5*time.Second)
}

// uploadCreateHooks 配置新建分支钩子：登记缺行、号段 5→6、节点缺行后回读新行。
// uploadCreateHooks wires the create branch: a registry miss, the 5→6 id
// reservation, a node miss and then the authoritative re-read of the new row.
func uploadCreateHooks(h *uploadFakeHub, path repository.Path, body string, fresh content.Metadata, stamp, reserved int64) {
	nodeCalls := 0
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM unique_ids"):
			return uploadIDCols, [][]driver.Value{{int64(reserved - 1)}}, nil
		case strings.Contains(query, "FROM nodes"):
			nodeCalls++
			if nodeCalls == 1 {
				return uploadNodeCols, nil, nil
			}
			return uploadNodeCols, [][]driver.Value{uploadNodeRow(reserved, path, int64(len(body)),
				fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)}, nil
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	h.exec = func(query string, args []driver.NamedValue) (int64, error) {
		switch {
		case strings.Contains(query, "INSERT INTO binaries"):
			return 1, nil
		case strings.Contains(query, "UPDATE unique_ids"):
			h.mu.Lock()
			h.lastIDUpdateArgs = append([]driver.NamedValue(nil), args...)
			h.mu.Unlock()
			return 1, nil
		case strings.Contains(query, "INSERT INTO nodes"):
			h.mu.Lock()
			h.lastNodeExecArgs = append([]driver.NamedValue(nil), args...)
			h.mu.Unlock()
			return 1, nil
		default:
			return 0, errUploadFakeExec
		}
	}
}

func TestSaveCreateBranchCommitsNodeTransactionOnce(t *testing.T) {
	h := &uploadFakeHub{}
	stamp := int64(1_700_000_000_000)
	saver := uploadSuccessSaver(t, h, stamp)
	body := "create branch body\n"
	fresh := uploadFreshCalculation(t, body)
	input := uploadTestInput(t, int64(len(body)))
	reserved := int64(6)
	uploadCreateHooks(h, input.Path, body, fresh, stamp, reserved)

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err != nil {
		t.Fatalf("Save create branch: %v", err)
	}
	if source.Reference.ID != reserved {
		t.Fatalf("created source ID = %d, want the reserved first id %d", source.Reference.ID, reserved)
	}
	if source.Reference.Path.Key() != input.Path.Key() {
		t.Fatalf("created source path = %q, want %q", source.Reference.Path.Key(), input.Path.Key())
	}
	if source.Reference.Content != fresh {
		t.Fatalf("created content = %+v, want published %+v", source.Reference.Content, fresh)
	}
	if !source.SHA1ActualValid || !source.MD5ActualValid || !source.SHA256ActualValid {
		t.Fatalf("created actual validity bits = %+v, want all true", source)
	}
	meta := source.Reference.Metadata
	if meta == nil {
		t.Fatal("created source metadata = nil, want the stamped snapshot")
	}
	if meta.CreatedMillis() != stamp || meta.ModifiedMillis() != stamp || meta.UpdatedMillis() != stamp {
		t.Fatalf("created metadata times = %d/%d/%d, want the single clock stamp",
			meta.CreatedMillis(), meta.ModifiedMillis(), meta.UpdatedMillis())
	}
	if got := string(utf16.Decode(meta.CreatedBy().UTF16())); got != "op-user" {
		t.Fatalf("created actor = %q, want op-user", got)
	}
	if _, begins, rollbacks := h.snapshot(); begins != 3 || rollbacks != 0 {
		t.Fatalf("begins/rollbacks = %d/%d, want 3/0 (registry, node, reservation)", begins, rollbacks)
	}
	if commits := h.commitCount(); commits != 3 {
		t.Fatalf("commits = %d, want 3 (registry, reservation, node)", commits)
	}
	if args := h.nodeExecArgs(); len(args) != 16 {
		t.Fatalf("node INSERT args = %d values, want 16", len(args))
	} else {
		wantDepth, derr := input.Path.Depth()
		if derr != nil {
			t.Fatalf("input depth: %v", derr)
		}
		checks := []struct {
			pos  int
			want any
		}{
			{0, reserved}, {1, "repo-x"}, {2, "a"}, {3, "b.bin"}, {4, int64(wantDepth)},
			{5, int64(len(body))}, {6, fresh.SHA1}, {7, fresh.MD5}, {8, fresh.SHA256},
			{9, stamp}, {10, stamp}, {11, stamp}, {12, "op-user"}, {13, "op-user"}, {14, nil}, {15, nil},
		}
		for _, c := range checks {
			if args[c.pos].Value != c.want {
				t.Fatalf("node INSERT arg $%d = %v (%T), want %v", c.pos+1, args[c.pos].Value, args[c.pos].Value, c.want)
			}
		}
	}
	if idArgs := h.idUpdateArgs(); len(idArgs) != 3 || idArgs[0].Value != reserved || idArgs[1].Value != "general" || idArgs[2].Value != reserved-1 {
		t.Fatalf("reserve args = %+v, want [next bound %d, general, expected %d]", idArgs, reserved, reserved-1)
	}
	if len(saver.gate) != 0 {
		t.Fatal("node gate still held after a successful save")
	}
}

func TestSaveUpdateBranchWritesUpdatedProjection(t *testing.T) {
	h := &uploadFakeHub{}
	stamp := int64(1_700_000_000_000)
	created := int64(1_600_000_000_000)
	saver := uploadSuccessSaver(t, h, stamp)
	body := "replacement body v2\n"
	old := uploadFreshCalculation(t, "stale body v1\n")
	fresh := uploadFreshCalculation(t, body)
	input := uploadTestInput(t, int64(len(body)))
	const nodeID = int64(9)
	nodeCalls := 0
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM unique_ids"):
			return nil, nil, errUploadFakeQuery // 更新分支绝不分配号段。
		case strings.Contains(query, "FROM nodes"):
			nodeCalls++
			if nodeCalls == 1 {
				return uploadNodeCols, [][]driver.Value{uploadNodeRow(nodeID, input.Path, int64(len("stale body v1\n")),
					old.SHA1, old.MD5, old.SHA256, created, created, created)}, nil
			}
			return uploadNodeCols, [][]driver.Value{uploadNodeRow(nodeID, input.Path, int64(len(body)),
				fresh.SHA1, fresh.MD5, fresh.SHA256, created, stamp, stamp)}, nil
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	h.exec = func(query string, args []driver.NamedValue) (int64, error) {
		switch {
		case strings.Contains(query, "INSERT INTO binaries"):
			return 1, nil
		case strings.Contains(query, "UPDATE nodes"):
			h.mu.Lock()
			h.lastNodeExecArgs = append([]driver.NamedValue(nil), args...)
			h.mu.Unlock()
			return 1, nil
		default:
			return 0, errUploadFakeExec
		}
	}

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err != nil {
		t.Fatalf("Save update branch: %v", err)
	}
	if source.Reference.ID != nodeID {
		t.Fatalf("updated source ID = %d, want the existing node %d", source.Reference.ID, nodeID)
	}
	if source.Reference.Content != fresh {
		t.Fatalf("updated content = %+v, want published %+v", source.Reference.Content, fresh)
	}
	if meta := source.Reference.Metadata; meta == nil || meta.CreatedMillis() != created {
		t.Fatalf("updated created time not preserved: %+v", meta)
	}
	if _, begins, rollbacks := h.snapshot(); begins != 2 || rollbacks != 0 {
		t.Fatalf("begins/rollbacks = %d/%d, want 2/0 (registry and node only)", begins, rollbacks)
	}
	if commits := h.commitCount(); commits != 2 {
		t.Fatalf("commits = %d, want 2 (registry and node)", commits)
	}
	args := h.nodeExecArgs()
	if len(args) != 16 {
		t.Fatalf("node UPDATE args = %d values, want 16", len(args))
	}
	if args[0].Value != nodeID || args[5].Value != int64(len(body)) || args[6].Value != fresh.SHA1 {
		t.Fatalf("node UPDATE projection args = %+v, want id/length/sha1 updated", args)
	}
	if args[9].Value != created || args[10].Value != stamp {
		t.Fatalf("node UPDATE metadata args = created %v modified %v, want %v/%v",
			args[9].Value, args[10].Value, created, stamp)
	}
}

// 相同字节仍走 dirty 更新：上传输入画像无法表达加载侧 original 三态
// （nil/空/NO_ORIG），desired 的 original 永远与基线不同（见 repository 包
// UpdateLocalFileWithoutClientChecksums 契约“相同字节仍走 dirty 更新”）。
// 这里验证该语义：即使字面完全相同也执行一次 UPDATE。
// Identical bytes still take the dirty update: the upload input profile
// cannot express the loaded original tri-state (nil/empty/NO_ORIG), so the
// desired originals always differ from the baseline (the repository package
// documents "even identical bytes stay dirty"). This verifies exactly that
// semantic: one UPDATE runs even for byte-identical content.
func TestSaveIdenticalBytesStillRewriteNode(t *testing.T) {
	h := &uploadFakeHub{}
	stamp := int64(1_700_000_000_000)
	saver := uploadSuccessSaver(t, h, stamp)
	body := "identical body\n"
	fresh := uploadFreshCalculation(t, body)
	input := uploadTestInput(t, int64(len(body)))
	const nodeID = int64(12)
	stored := uploadNodeRow(nodeID, input.Path, int64(len(body)), fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM nodes"):
			return uploadNodeCols, [][]driver.Value{stored}, nil
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	h.exec = func(query string, args []driver.NamedValue) (int64, error) {
		switch {
		case strings.Contains(query, "INSERT INTO binaries"):
			return 1, nil
		case strings.Contains(query, "UPDATE nodes"):
			h.mu.Lock()
			h.lastNodeExecArgs = append([]driver.NamedValue(nil), args...)
			h.mu.Unlock()
			return 1, nil
		default:
			return 0, errUploadFakeExec
		}
	}

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err != nil {
		t.Fatalf("Save identical bytes: %v", err)
	}
	if source.Reference.ID != nodeID {
		t.Fatalf("identical-bytes source ID = %d, want the existing node %d", source.Reference.ID, nodeID)
	}
	if source.Reference.Content != fresh {
		t.Fatalf("identical-bytes content = %+v, want published %+v", source.Reference.Content, fresh)
	}
	if args := h.nodeExecArgs(); len(args) != 16 || args[0].Value != nodeID || args[6].Value != fresh.SHA1 {
		t.Fatalf("identical-bytes UPDATE args = %d values, want the 16-column rewrite of node %d", len(args), nodeID)
	}
	if _, begins, rollbacks := h.snapshot(); begins != 2 || rollbacks != 0 {
		t.Fatalf("begins/rollbacks = %d/%d, want 2/0", begins, rollbacks)
	}
	if commits := h.commitCount(); commits != 2 {
		t.Fatalf("commits = %d, want 2 (registry and node)", commits)
	}
}

// UPDATE 影响 0 行时 validateSavedResult 拒绝该节点保存结果。
// An UPDATE affecting zero rows makes validateSavedResult reject the node
// save result.
func TestSaveUpdateZeroRowsRejectedAsInvalidResult(t *testing.T) {
	h := &uploadFakeHub{}
	stamp := int64(1_700_000_000_000)
	saver := uploadSuccessSaver(t, h, stamp)
	body := "zero row update body\n"
	old := uploadFreshCalculation(t, "previous body\n")
	input := uploadTestInput(t, int64(len(body)))
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM nodes"):
			return uploadNodeCols, [][]driver.Value{uploadNodeRow(21, input.Path, int64(len("previous body\n")),
				old.SHA1, old.MD5, old.SHA256, stamp, stamp, stamp)}, nil
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	h.exec = func(query string, _ []driver.NamedValue) (int64, error) {
		switch {
		case strings.Contains(query, "INSERT INTO binaries"):
			return 1, nil
		case strings.Contains(query, "UPDATE nodes"):
			return 0, nil // 行消失：0 行受影响。
		default:
			return 0, errUploadFakeExec
		}
	}

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err == nil {
		t.Fatal("zero-row update must fail the save")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("zero-row update error = %v, want ErrUploadPersistence", err)
	}
	if err.Error() != "postgres upload: invalid node save result" {
		t.Fatalf("zero-row update Error() = %q, want the invalid-result text", err.Error())
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
	if _, _, rollbacks := h.snapshot(); rollbacks != 1 {
		t.Fatalf("rollbacks = %d, want 1 (node tx rolled back)", rollbacks)
	}
}

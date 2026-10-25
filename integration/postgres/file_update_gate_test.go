// 快照更新的真实 PostgreSQL 接缝：零写、完整 Desired 提交与调用方回滚。
// 薄委托直接调用真实 Store；独立 SQL 验证落库，事务内读值验证回滚前变化。
// Real-PostgreSQL snapshot update seams: no-op, Desired-driven commit and caller rollback.
// Thin delegates call the real Store; independent SQL and in-transaction reads verify outcomes.
package postgresit

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
)

// guOldSnapshot 构造与已提交旧行互为真实来源的 Existing 快照：其
// NodeMetadata 投影逐字等于 ufOldSnapshot 的七字面值（ufRequireOldNode
// 因此可直接复用核对旧行），校验和 Actual 取已发布内容、SHA1 Original
// 非空、MD5/SHA256 Original 为 null。
// guOldSnapshot builds the Existing snapshot authentic to the committed old
// row: its NodeMetadata projection equals the ufOldSnapshot seven literals
// verbatim (so ufRequireOldNode verifies the old row as-is), with published
// content actuals, a nonnull SHA1 original and null MD5/SHA256 originals.
func guOldSnapshot(t *testing.T, repo string, winner content.Metadata) repository.FileInfoSnapshot {
	t.Helper()
	snap, err := repository.NewFileInfoSnapshot(repository.FileInfoSnapshotInput{
		RepoKeyUTF16:       mdUnits(repo),
		RepoPathUTF16:      mdUnits("old/a.bin"),
		NameUTF16:          mdUnits("a.bin"),
		CreatedMillis:      1111111111111,
		LastModifiedMillis: 2222222222222,
		LastUpdatedMillis:  3333333333333,
		CreatedByUTF16:     mdUnits("old-creator"),
		ModifiedByUTF16:    nil,
		SizeBytes:          winner.Size,
		MimeTypeUTF16:      mdUnits("application/octet-stream"),
		Checksums: []repository.ChecksumEntryInput{
			{Kind: repository.ChecksumSHA1, Actual: mdUnits(winner.SHA1), Original: mdUnits("old-orig-1")},
			{Kind: repository.ChecksumMD5, Actual: mdUnits(winner.MD5)},
			{Kind: repository.ChecksumSHA256, Actual: mdUnits(winner.SHA256)},
		},
	})
	if err != nil {
		t.Fatalf("old snapshot construction: %v", err)
	}
	return snap
}

// guDirtySnapshot 构造与旧快照在全部比较字段上刻意互异的 Desired：三个
// 时间、两 actor（含 70 码元 modifiedBy 触发 61+三点有界缩写）、null↔非
// null 翻转的 SHA1/MD5 Original、仅原始域的 SHA256 Original 与 MIME、
// 新内容 actuals 与 size、独立缓存 name（只参与比较）、经既有 helper 重
// 构且单次元数据后缀剥离的 RepoPath（moved/f:properties:statistics →
// parent=moved、name=f:properties）。
// guDirtySnapshot builds a Desired deliberately distinct from the old
// snapshot in every compared field: the three times, both actors (a
// 70-unit modifiedBy exercising the 61-units-plus-dots bounded
// abbreviation), the null/nonnull flips of the SHA1/MD5 originals, the
// raw-only SHA256 original and MIME, the new content actuals and size, the
// independent cached name (comparison only) and the RepoPath reconstructed
// by the existing helper with the single metadata-suffix strip
// (moved/f:properties:statistics → parent=moved, name=f:properties).
func guDirtySnapshot(t *testing.T, repo string, winner content.Metadata) repository.FileInfoSnapshot {
	t.Helper()
	snap, err := repository.NewFileInfoSnapshot(repository.FileInfoSnapshotInput{
		RepoKeyUTF16:       mdUnits(repo),
		RepoPathUTF16:      mdUnits("moved/f:properties:statistics"),
		NameUTF16:          mdUnits("cached-name-only.bin"),
		CreatedMillis:      4444444444444,
		LastModifiedMillis: 5555555555555,
		LastUpdatedMillis:  6666666666666,
		CreatedByUTF16:     mdUnits("new-creator"),
		ModifiedByUTF16:    mdUnits(strings.Repeat("m", 70)),
		SizeBytes:          winner.Size,
		MimeTypeUTF16:      mdUnits("text/plain"),
		Checksums: []repository.ChecksumEntryInput{
			{Kind: repository.ChecksumSHA1, Actual: mdUnits(winner.SHA1)},
			{Kind: repository.ChecksumMD5, Actual: mdUnits(winner.MD5), Original: mdUnits("new-md5-orig")},
			{Kind: repository.ChecksumSHA256, Actual: mdUnits(winner.SHA256), Original: mdUnits("sha256-orig-raw-only")},
		},
	})
	if err != nil {
		t.Fatalf("dirty snapshot construction: %v", err)
	}
	return snap
}

// 场景 GU1：同一真实快照作 Existing 与 Desired（该快照的 NodeMetadata
// 投影即已提交旧行），变化门必须显式返回 {false,0} 且委托在缝上恰零次
// ——零调用由委托自身的计数证明，行相等不能替代；随后调用方回滚恰
// nil，独立 SQL 全行（投影+内容四列+七列）与已发布内容、bytes、上界
// 全部原样。
// Scenario GU1: one authentic snapshot serves as both Existing and Desired
// (its NodeMetadata projection IS the committed old row), and the gate must
// return an explicit {false,0} with the delegate firing ZERO times at the
// seam — the zero-call fact is proven by the delegate's own counter because
// row equality alone cannot substitute; the caller rollback then returns
// exactly nil with the independent-SQL full row (projection, four content
// columns, seven metadata columns), the published content, its bytes and
// the bound all intact.
func TestUpdateFileIfChangedIdenticalSkipZeroDelegateAndRowIntact(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	svc, store, root := cfBinaryService(t, db)

	payload := "binflow gate update-file regression payload A"
	winner, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add: %v", err)
	}
	ufRequireBinaryFields(t, adm, schema, winner)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files = %d, want exactly 1 after the Add", n)
	}

	oldSnap := guOldSnapshot(t, "gateA", winner)
	oldMD, err := oldSnap.NodeMetadata()
	if err != nil {
		t.Fatalf("old snapshot NodeMetadata: %v", err)
	}
	nodeID := ufCommitNodeWithSnapshot(t, db, 2401, "gateA", "old", "a.bin", winner, &oldMD)
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after seed = %d, want 1", n)
	}

	tx, st := nodeTx(t, db)
	d := &ufDelegates{st: st}
	res, err := repository.UpdateFileIfChanged(ctx, repository.FileDirtyUpdateInput{
		Existing: oldSnap, Desired: oldSnap, IgnoreCreated: false, ID: nodeID,
	}, d.updateRef)
	if err != nil || res.Changed || res.Count != 0 {
		t.Fatalf("UpdateFileIfChanged(identical) = ({%v,%d}, %v), want ({false,0}, nil)", res.Changed, res.Count, err)
	}
	// 零调用必须在缝上证明：即使一次等值重写也会让行保持相等，只有委托
	// 计数能区分"跳过"与"写了相同值"。
	// The zero-call fact must be proven at the seam: an equal-value rewrite
	// would leave the row equal too, and only the delegate counter
	// distinguishes "skipped" from "wrote the same values".
	if d.update != 0 {
		t.Fatalf("update delegate calls = %d, want exactly 0 (identical snapshots must skip the node write)", d.update)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback must return nil, got %v", err)
	}

	ufRequireOldNode(t, adm, schema, nodeID, "gateA", "old", "a.bin", winner)
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after the no-op = %d, want 1", n)
	}
	ufRequireBinaryFields(t, adm, schema, winner)
	cfRequireObjectBytes(t, store, winner.SHA1, payload)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files after the no-op = %d, want exactly 1", n)
	}
	cfRequireBound(t, adm, schema, 0)
	cfRequireCurrent(t, schema, 0)
}

// 场景 GU2：全字段互异的 Desired 经变化门提交。委托恰一次收到仅由
// Desired 派生的操作数：无分配 ID、映射身份（RepoPath 派生 name
// f:properties，绝非缓存 name）、actuals+size 内容与 NodeMetadata 七
// 列；提交前独立连接仍只见证旧已提交行（更新真实落在调用方事务上），
// 提交恰 nil 后独立 SELECT 精确核对新身份（parent=moved/name=
// f:properties/depth=2）、内容四列与七元数据列（含 61 码元+三点的有界
// 缩写、SHA1 Original 翻转为 NULL、MD5 Original 翻转为非空），两条
// binary 与 bytes、上界全部保留。二进制前置：新内容 actuals 必须先真
// 实发布（外键目标存在），否则委托内真实 Store.Update 结构性失败。
// Scenario GU2: a Desired distinct in every compared field commits through
// the gate. The delegate fires exactly once with operands derived from
// Desired only: the unallocated-ID reference, the mapped identity (the
// RepoPath-derived name f:properties, never the cached name), the
// actuals+size content and the NodeMetadata seven columns; before the
// commit the independent pool still sees only the committed old row (the
// update really rides the caller transaction), and after the exactly-nil
// commit an independent SELECT verifies the exact new identity
// (parent=moved, name=f:properties, depth=2), the four content columns and
// the seven metadata columns (the 61-units-plus-dots bounded abbreviation,
// the SHA1 original flipped to NULL, the MD5 original flipped nonnull),
// with both binaries, both payloads' bytes and the bound preserved. Binary
// prerequisite: the new content actuals must be really published first (the
// FK target exists), otherwise the real Store.Update inside the delegate
// fails structurally.
func TestUpdateFileIfChangedDirtyCommitPersistsDerivedRow(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	svc, store, root := cfBinaryService(t, db)

	payloadA := "binflow gate update-file regression payload B"
	winnerA, err := svc.Add(ctx, strings.NewReader(payloadA), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(A): %v", err)
	}
	payloadB := "binflow gate update-file regression payload B-even-longer"
	winnerB, err := svc.Add(ctx, strings.NewReader(payloadB), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(B): %v", err)
	}
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)
	if n := cfCountFiles(t, root); n != 2 {
		t.Fatalf("object files = %d, want exactly 2 after the two Adds", n)
	}

	oldSnap := guOldSnapshot(t, "gateB", winnerA)
	oldMD, err := oldSnap.NodeMetadata()
	if err != nil {
		t.Fatalf("old snapshot NodeMetadata: %v", err)
	}
	nodeID := ufCommitNodeWithSnapshot(t, db, 2501, "gateB", "old", "a.bin", winnerA, &oldMD)
	wantSnap := guDirtySnapshot(t, "gateB-next", winnerB)
	wantMD, err := wantSnap.NodeMetadata()
	if err != nil {
		t.Fatalf("dirty snapshot NodeMetadata: %v", err)
	}

	tx, st := nodeTx(t, db)
	d := &ufDelegates{st: st}
	res, err := repository.UpdateFileIfChanged(ctx, repository.FileDirtyUpdateInput{
		Existing: oldSnap, Desired: wantSnap, IgnoreCreated: false, ID: nodeID,
	}, d.updateRef)
	if err != nil || !res.Changed || res.Count != 1 {
		t.Fatalf("UpdateFileIfChanged(dirty) = ({%v,%d}, %v), want ({true,1}, nil)", res.Changed, res.Count, err)
	}
	d.wantUpdateOnce(t)
	// 委托操作数只来自 Desired：偷换 Existing/旧行值在此失配。
	// The delegate operands come from Desired only: substituting Existing
	// or old-row values mismatches here.
	if d.saved.ID != nodeID {
		t.Fatalf("delegate received id %d, want the lookup id %d (no allocation)", d.saved.ID, nodeID)
	}
	if p := d.saved.Path; !p.IsFile() || p.Repo() != "gateB-next" || p.Parent() != "moved" || p.Name() != "f:properties" {
		t.Fatalf("delegate path = repo %q parent %q name %q file=%v, want the RepoPath-derived moved/f:properties identity",
			p.Repo(), p.Parent(), p.Name(), p.IsFile())
	}
	if d.saved.Path.Name() == "cached-name-only.bin" {
		t.Fatal("delegate name must derive from RepoPath, never the comparison-only cached name")
	}
	if d.saved.Content != winnerB {
		t.Fatalf("delegate content = %+v, want the Desired actuals+size %+v", d.saved.Content, winnerB)
	}
	equalSnapshot(t, d.saved.Metadata, wantMD)
	cfRequireBound(t, adm, schema, 0)

	// 提交前独立观察：未提交更新不可见，旧行仍按已提交状态可见。
	// Independent observation before the commit: the uncommitted update
	// stays invisible and the old row shows its committed state.
	ufRequireOldNode(t, adm, schema, nodeID, "gateB", "old", "a.bin", winnerA)
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("independent nodes = %d, want 1 before the commit", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("caller commit must return nil, got %v", err)
	}

	// 提交后独立 SELECT：精确新身份/新内容/七列（含 61+三点有界缩写与
	// null 翻转；SHA256 Original 与 MIME 为仅原始域字段，永不入列）。
	// Independent SELECT after the commit: the exact new identity, content
	// and seven columns (the 61-plus-dots bounded abbreviation and the
	// nullness flips; the SHA256 original and MIME are raw-only fields that
	// never reach the columns).
	qctx, qcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer qcancel()
	var nodeType, depth int16
	var repo, nodePath, nodeName, sha1Actual, md5Actual, sha256Actual string
	var binLength int64
	if err := adm.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT node_type, repo, node_path, node_name, depth, bin_length, sha1_actual, md5_actual, sha256 "+
			"FROM %s.nodes WHERE node_id = $1", schema), nodeID).
		Scan(&nodeType, &repo, &nodePath, &nodeName, &depth, &binLength, &sha1Actual, &md5Actual, &sha256Actual); err != nil {
		t.Fatalf("independent new-node read: %v", err)
	}
	if nodeType != 1 || repo != "gateB-next" || nodePath != "moved" || nodeName != "f:properties" || depth != 2 ||
		binLength != winnerB.Size || sha1Actual != winnerB.SHA1 || md5Actual != winnerB.MD5 || sha256Actual != winnerB.SHA256 {
		t.Fatalf("new projection = (%d %q %q %q %d %d %q %q %q), want the Desired-derived identity and content",
			nodeType, repo, nodePath, nodeName, depth, binLength, sha1Actual, md5Actual, sha256Actual)
	}
	created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig := metadataRowOf(t, adm, schema, nodeID)
	if !created.Valid || created.Int64 != 4444444444444 ||
		!modified.Valid || modified.Int64 != 5555555555555 ||
		!updated.Valid || updated.Int64 != 6666666666666 ||
		!createdBy.Valid || createdBy.String != "new-creator" ||
		!modifiedBy.Valid || modifiedBy.String != strings.Repeat("m", 61)+"..." ||
		sha1Orig.Valid || !md5Orig.Valid || md5Orig.String != "new-md5-orig" {
		t.Fatalf("new metadata = %v/%v/%v/%v/%v/%v/%v, want the Desired-derived seven literals",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}

	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after the commit = %d, want 1 (identity SET, no second row)", n)
	}
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)
	cfRequireObjectBytes(t, store, winnerA.SHA1, payloadA)
	cfRequireObjectBytes(t, store, winnerB.SHA1, payloadB)
	if n := cfCountFiles(t, root); n != 2 {
		t.Fatalf("object files after the commit = %d, want exactly 2", n)
	}
	cfRequireBound(t, adm, schema, 0)
	cfRequireCurrent(t, schema, 0)
}

// 场景 GU3：变化路径的临时成功结果后由测试调用方回滚。回滚前独立连接
// 仍只见已提交旧行（门绝不隐式提交）；回滚恰 nil 后旧行（投影+七列）、
// 两条 binary 与 bytes、号段上界全部保留。尾部：独立确认缺席的正 ID 经
// 同一真实委托得到计数 0 原样直通，且 Changed=true ——Changed 是门自身
// 分支的结果，绝不由委托计数推断；不转创建、上界不动。
// Scenario GU3: after the dirty path's provisional success the TEST caller
// rolls back. Before the rollback the independent pool still sees only the
// committed old row (the gate never commits implicitly); after the
// exactly-nil rollback the old row (projection and seven columns), both
// binaries and bytes, and the reservation bound all survive. Tail: an
// independently confirmed absent positive ID returns count 0 verbatim
// through the same real delegate while Changed=true — Changed is the gate's
// own branch result, never inferred from the delegate count; no create and
// no bound movement.
func TestUpdateFileIfChangedDirtyRollbackKeepsOldRow(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	svc, store, root := cfBinaryService(t, db)

	payloadA := "binflow gate update-file regression payload C"
	winnerA, err := svc.Add(ctx, strings.NewReader(payloadA), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(A): %v", err)
	}
	payloadB := "binflow gate update-file regression payload C-longer"
	winnerB, err := svc.Add(ctx, strings.NewReader(payloadB), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(B): %v", err)
	}
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)

	oldSnap := guOldSnapshot(t, "gateC", winnerA)
	oldMD, err := oldSnap.NodeMetadata()
	if err != nil {
		t.Fatalf("old snapshot NodeMetadata: %v", err)
	}
	nodeID := ufCommitNodeWithSnapshot(t, db, 2601, "gateC", "old", "a.bin", winnerA, &oldMD)
	wantSnap := guDirtySnapshot(t, "gateC", winnerB)

	tx, st := nodeTx(t, db)
	d := &ufDelegates{st: st}
	res, err := repository.UpdateFileIfChanged(ctx, repository.FileDirtyUpdateInput{
		Existing: oldSnap, Desired: wantSnap, IgnoreCreated: false, ID: nodeID,
	}, d.updateRef)
	if err != nil || !res.Changed || res.Count != 1 {
		t.Fatalf("UpdateFileIfChanged(dirty) = ({%v,%d}, %v), want provisional ({true,1}, nil)", res.Changed, res.Count, err)
	}
	d.wantUpdateOnce(t)

	// 同一调用方事务必须先看见真正的新值，外部旧行不能单独证明回滚。
	// First prove the new values exist inside the caller transaction.
	var txPath, txName, txSHA1 string
	var txSize, txModified int64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT node_path, node_name, sha1_actual, bin_length, modified FROM %s.nodes WHERE node_id = $1", schema), nodeID).Scan(&txPath, &txName, &txSHA1, &txSize, &txModified); err != nil {
		t.Fatalf("read provisional caller-tx row: %v", err)
	}
	if txPath != "moved" || txName != "f:properties" || txSHA1 != winnerB.SHA1 || txSize != winnerB.Size || txModified != 5555555555555 {
		t.Fatalf("caller transaction does not contain Desired changes: %q %q %q %d %d", txPath, txName, txSHA1, txSize, txModified)
	}

	// 无隐式提交：临时更新只存在于调用方事务，独立连接仍见证旧行。
	// No implicit commit: the provisional update lives only inside the
	// caller transaction and the independent pool still sees the old row.
	ufRequireOldNode(t, adm, schema, nodeID, "gateC", "old", "a.bin", winnerA)
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("independent nodes = %d, want 1 before the caller rollback", n)
	}
	cfRequireBound(t, adm, schema, 0)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback must return nil, got %v", err)
	}

	// 回滚保留：旧行投影+七列、两条 binary 与 bytes、上界与对象文件。
	// Rollback preservation: the old row's projection and seven columns,
	// both binaries and both payloads' bytes, the bound and the object
	// files.
	ufRequireOldNode(t, adm, schema, nodeID, "gateC", "old", "a.bin", winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)
	cfRequireObjectBytes(t, store, winnerA.SHA1, payloadA)
	cfRequireObjectBytes(t, store, winnerB.SHA1, payloadB)
	if n := cfCountFiles(t, root); n != 2 {
		t.Fatalf("object files after the rollback = %d, want exactly 2", n)
	}
	cfRequireBound(t, adm, schema, 0)
	cfRequireCurrent(t, schema, 0)

	// 尾部：缺席正 ID 先独立确认缺席，再经同一真实委托得计数 0 原样直
	// 通且 Changed=true（与计数无关的分支事实），不转创建。
	// Tail: the absent positive ID is independently confirmed absent first,
	// then returns count 0 verbatim through the same real delegate while
	// Changed=true (a branch fact independent of the count) with no create.
	const absentID = 987655
	if nodeIDExists(t, adm, schema, absentID) {
		t.Fatal("the absent positive id must be independently confirmed absent before use")
	}
	tx2, st2 := nodeTx(t, db)
	d2 := &ufDelegates{st: st2}
	res2, err := repository.UpdateFileIfChanged(ctx, repository.FileDirtyUpdateInput{
		Existing: oldSnap, Desired: wantSnap, IgnoreCreated: false, ID: absentID,
	}, d2.updateRef)
	if err != nil || !res2.Changed || res2.Count != 0 {
		t.Fatalf("UpdateFileIfChanged(absent id) = ({%v,%d}, %v), want ({true,0}, nil) verbatim", res2.Changed, res2.Count, err)
	}
	d2.wantUpdateOnce(t)
	if d2.saved.ID != absentID {
		t.Fatalf("absent-id delegate received id %d, want %d", d2.saved.ID, absentID)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("tail rollback: %v", err)
	}
	if nodeIDExists(t, adm, schema, absentID) {
		t.Fatal("the absent id must stay absent (no create from a zero-count update)")
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after the absent-id update = %d, want 1", n)
	}
	cfRequireBound(t, adm, schema, 0)
}

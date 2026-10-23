// 新 UpdateFile 消费缝的真实 PostgreSQL 回归（BIN-158）：真实更新消费缝上的
// 计数薄委托（恰一次真实 Store.Update，运行在调用方事务上）覆盖三个有区分
// 度的场景——UA1 携带快照身份改写与完整显式元数据的更新在调用方回滚后保留
// 旧行（投影+七列）、两条 binary 与各自 bytes，号段上界不动；尾部缺失正 ID
// 先独立建立缺席（所有者更正：更新回滚不删除既有行，不得用已回滚原 ID 顶
// 替）再验证计数 0 原样直通且不转创建。UA2 自有 AFTER UPDATE 延迟约束触发
// 器（在更新事务开启前创建）让消费者先返回临时成功 1，随后调用方真实
// Commit 以 SQLSTATE 恰为 P0001 失败，旧行/两条 binary/bytes/上界保留。
// UA3 Registry 的 NBSP 胜出记录经委托原样（同指针、逐字段）到达旧 Store 的
// SQL 前拒绝：错误因果链保留 ErrInvalidReference 且无任何 PgError，事务仍
// 可用并回滚，登记行/bytes/对象/上界保留。携带路径改写只证明 DAO 快照 SET
// 映射（repo/node_path/node_name/depth 写入），不是上传移动/改名兼容；NBSP
// 十六进制拒绝是显式本地规范边界（目标 mapper 原样透传 sha2），非目标
// parity。nil Metadata 拒绝是无 SQL 的本地守卫，由单元用例覆盖，真库套件不
// 重复；不镜像 adapter/codec/迁移/八进程门禁（既有测试与共享 check-postgres.sh
// 继续承担）。本套件预期与真实 UpdateFile 核心（BIN-157）一同在真实一次性
// 数据库上运行。
// Real-PostgreSQL regression for the new UpdateFile consumer seam (BIN-158):
// counting thin delegates (exactly one real Store.Update on the caller
// transaction) cover three meaningful scenarios — UA1 an update carrying a
// changed snapshot identity plus a full explicit metadata snapshot preserves
// the old row (projection and seven columns), both binaries and their bytes
// after the caller rollback, with the reservation bound untouched; the tail
// first establishes absence INDEPENDENTLY for a missing positive ID (owner
// correction: an update rollback never deletes the existing row, so the
// rolled-back original ID cannot stand in) and then verifies count 0 returned
// verbatim with no create. UA2 an own-schema AFTER UPDATE deferred constraint
// trigger (created before the update transaction opens) lets the consumer
// return provisional success 1 first, then the caller's real Commit fails
// with SQLSTATE exactly P0001 while the old row, both binaries, the bytes and
// the bound survive. UA3 the registry's NBSP winner reaches the old Store's
// pre-SQL rejection verbatim through the delegate (same pointer, field by
// field): the error chain preserves ErrInvalidReference with no PgError at
// all, the transaction stays usable and rolls back, and the registration,
// bytes, object and bound survive. The carried-path rewrite proves only the
// DAO snapshot-SET mapping (repo/node_path/node_name/depth written), never
// upload move/rename parity; the NBSP hex rejection is the explicit LOCAL
// canonical boundary (the target mapper passes sha2 verbatim), never target
// parity. The nil-Metadata rejection is a no-SQL local guard owned by the
// unit cases and is not repeated in the DB suite; no adapter/codec/migration/
// eight-process gate is mirrored here (the existing tests and the shared
// check-postgres.sh keep owning those). This suite is meant to run against
// the real UpdateFile core (BIN-157) on a real disposable database.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// ufDelegates 是三个场景共用的计数薄真实委托：updateRef 直接调用本场景调
// 用方事务上的真实 Store.Update（计数不替代真实数据库行为，也不预构造引用
// 后另行调用）；saved 保存委托实际收到的引用，供“原样到达旧 Store”断言。
// ufDelegates is the counting thin real delegate shared by the three
// scenarios: updateRef calls the real Store.Update on this scenario's caller
// transaction directly (the counter never replaces real database behavior and
// no prefabricated reference is ever called separately); saved keeps the
// reference the delegate actually received for the "reached the old Store
// verbatim" assertion.
type ufDelegates struct {
	st     *refpg.Store
	update int
	saved  repository.FileReference
}

// updateRef 是计数薄委托：直接调用本场景调用方事务的真实 Store.Update。
// updateRef is the counting thin delegate: it calls the real Store.Update on
// this scenario's caller transaction directly.
func (d *ufDelegates) updateRef(ctx context.Context, ref repository.FileReference) (int64, error) {
	d.update++
	d.saved = ref
	return d.st.Update(ctx, ref)
}

// wantUpdateOnce 核对更新消费缝恰一次且无自动重试。
// wantUpdateOnce verifies the update seam fired exactly once with no
// automatic repeat.
func (d *ufDelegates) wantUpdateOnce(t *testing.T) {
	t.Helper()
	if d.update != 1 {
		t.Fatalf("update delegate calls = %d, want exactly 1 (no automatic repeat)", d.update)
	}
}

// ufOldSnapshot 是三场景共用的初始已提交快照：非空与 NULL 的 actor/original
// 取例并存，字面值与 ufRequireOldNode 的独立七列核对一一对应。
// ufOldSnapshot is the committed seed snapshot shared by the three scenarios:
// nonempty and NULL actor/original examples coexist, and the literals map
// one-to-one onto ufRequireOldNode's independent seven-column check.
func ufOldSnapshot() repository.FileMetadata {
	return repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  1111111111111,
		ModifiedMillis: 2222222222222,
		UpdatedMillis:  3333333333333,
		CreatedBy:      repository.NewActor(mdUnits("old-creator")),
		ModifiedBy:     repository.NewActor(nil),
		SHA1Original:   mdUnits("old-orig-1"),
		MD5Original:    nil,
	})
}

// ufCommitNodeWithSnapshot 建立三场景共用的真实起点：显式正 ID + 完整旧快照
// 经真实 Store.Create 在调用方事务内写入并提交，再在全新事务里用真实
// Lookup 按路径取回同一 ID（对齐目标的 lookup-supplied-ID 形态）。
// ufCommitNodeWithSnapshot builds the shared real starting point of the three
// scenarios: an explicit positive id plus the full old snapshot written
// through the real Store.Create inside a committed caller transaction, then a
// real Lookup in a fresh transaction returning that same id by path (the
// target's lookup-supplied-ID shape).
func ufCommitNodeWithSnapshot(t *testing.T, db *sql.DB, id int64, repo, parent, name string,
	winner content.Metadata, snap *repository.FileMetadata) int64 {
	t.Helper()
	ctx := nodeTestContext(t)
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, id, repo, parent, name, winner)
	ref.Metadata = snap
	if n, err := st.Create(ctx, ref); err != nil || n != 1 {
		t.Fatalf("seed Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, repo, parent, name, winner).Path)
	if err != nil {
		t.Fatalf("seed Lookup: %v", err)
	}
	if got.ID != id {
		t.Fatalf("seed Lookup id = %d, want %d", got.ID, id)
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("seed lookup rollback: %v", err)
	}
	return got.ID
}

// ufRequireBinaryFields 独立核对一条登记行的四字段并要求该 SHA-1 存在（不限
// 制总行数：更新场景的初始与更新内容各占一行，区别于创建场景的恰一行断
// 言）。
// ufRequireBinaryFields independently verifies one registration row's four
// fields and requires that SHA-1 to be present (no total-row-count limit: the
// update scenarios keep one row per published content, unlike the create
// scenario's exactly-one assertion).
func ufRequireBinaryFields(t *testing.T, adm *sql.DB, schema string, winner content.Metadata) {
	t.Helper()
	gotSHA1, gotMD5, gotLen, gotSHA256, ok := rowOf(t, adm, schema, winner.SHA1)
	if !ok || gotSHA1 != winner.SHA1 || gotMD5 != winner.MD5 || gotLen != winner.Size ||
		!gotSHA256.Valid || gotSHA256.String != winner.SHA256 {
		t.Fatalf("binaries row = (%q %q %d %v present=%v), want the winner's four fields",
			gotSHA1, gotMD5, gotLen, gotSHA256, ok)
	}
}

// ufRequireOldNode 通过独立 SQL 核对旧行完整保留：node_type/repo/node_path/
// node_name/depth 投影与内容四列等于初始已提交值，七列元数据等于
// ufOldSnapshot 的字面值。
// ufRequireOldNode verifies through independent SQL that the old row survives
// intact: the node_type/repo/node_path/node_name/depth projection plus the
// four content columns equal the committed seed values, and the seven metadata
// columns equal ufOldSnapshot's literals.
func ufRequireOldNode(t *testing.T, adm *sql.DB, schema string, nodeID int64,
	repo, parent, name string, winner content.Metadata) {
	t.Helper()
	wantDepth, err := fileRefOf(t, 0, repo, parent, name, winner).Path.Depth()
	if err != nil {
		t.Fatalf("seed path depth: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var nodeType, depth int16
	var gotRepo, gotPath, gotName, sha1Actual, md5Actual, sha256Actual string
	var binLength int64
	if err := adm.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT node_type, repo, node_path, node_name, depth, bin_length, sha1_actual, md5_actual, sha256 "+
			"FROM %s.nodes WHERE node_id = $1", schema), nodeID).
		Scan(&nodeType, &gotRepo, &gotPath, &gotName, &depth, &binLength, &sha1Actual, &md5Actual, &sha256Actual); err != nil {
		t.Fatalf("independent old-node read: %v", err)
	}
	if nodeType != 1 || gotRepo != repo || gotPath != parent || gotName != name || depth != wantDepth ||
		binLength != winner.Size || sha1Actual != winner.SHA1 || md5Actual != winner.MD5 || sha256Actual != winner.SHA256 {
		t.Fatalf("old projection = (%d %q %q %q %d %d %q %q %q), want the committed seed values",
			nodeType, gotRepo, gotPath, gotName, depth, binLength, sha1Actual, md5Actual, sha256Actual)
	}
	created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig := metadataRowOf(t, adm, schema, nodeID)
	if !created.Valid || created.Int64 != 1111111111111 || !modified.Valid || modified.Int64 != 2222222222222 ||
		!updated.Valid || updated.Int64 != 3333333333333 ||
		!createdBy.Valid || createdBy.String != "old-creator" || modifiedBy.Valid ||
		!sha1Orig.Valid || sha1Orig.String != "old-orig-1" || md5Orig.Valid {
		t.Fatalf("old metadata = %v/%v/%v/%v/%v/%v/%v, want the ufOldSnapshot literals",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
}

// 场景 UA1：两次真实 svc.Add 发布初始与更新内容；初始节点带完整旧快照提交，
// ID 来自真实 Lookup。更新消费缝携带改写路径（moved//renamed.bin →
// parent=moved、name=renamed.bin、depth=2）与显式混合 NULL/空快照：委托恰一
// 次收到无分配 ID、已映射身份、新内容与完整快照；号段上界保持 0。同事务可见
// 新身份/新快照且旧路径缺失，独立连接只见已提交旧行；调用方 Rollback 恰为
// nil 后旧行（投影+七列）、两条 binary 与 bytes 全部保留。携带路径改写只证明
// DAO 快照 SET 映射，不是上传移动/改名兼容。
// Scenario UA1: two real svc.Add calls publish the initial and the update
// content; the initial node is committed with the full old snapshot and its ID
// comes from a real Lookup. The update consumer seam carries a rewritten path
// (moved//renamed.bin → parent=moved, name=renamed.bin, depth=2) plus an
// explicit mixed NULL/empty snapshot: the delegate fires exactly once and
// receives the unallocated id, the mapped identity, the new content and the
// full snapshot; the reservation bound stays 0. The same transaction sees the
// new identity and snapshot with the old path missing while the independent
// pool still sees only the committed old row; after the caller's Rollback
// returns exactly nil the old row (projection and seven columns), both
// binaries and both payloads' bytes all survive. The carried-path rewrite
// proves the DAO snapshot-SET mapping only, never upload move/rename parity.
func TestUpdateFileCallerRollbackKeepsOldRowAndBothBinaries(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	svc, store, root := cfBinaryService(t, db)

	payloadA := "binflow update-file regression payload A"
	winnerA, err := svc.Add(ctx, strings.NewReader(payloadA), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(A): %v", err)
	}
	payloadB := "binflow update-file regression payload A2"
	winnerB, err := svc.Add(ctx, strings.NewReader(payloadB), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(B): %v", err)
	}
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)
	if n := cfCountFiles(t, root); n != 2 {
		t.Fatalf("object files = %d, want exactly 2 after the two Adds", n)
	}

	snapOld := ufOldSnapshot()
	nodeID := ufCommitNodeWithSnapshot(t, db, 2101, "updateA", "old", "a.bin", winnerA, &snapOld)
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after seed = %d, want 1", n)
	}

	snapNew := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  4444444444444,
		ModifiedMillis: 0,
		UpdatedMillis:  -17,
		CreatedBy:      repository.NewActor(nil),
		ModifiedBy:     repository.NewActor(mdUnits("")),
		SHA1Original:   nil,
		MD5Original:    mdUnits(""),
	})

	tx, st := nodeTx(t, db)
	d := &ufDelegates{st: st}
	count, err := repository.UpdateFile(ctx, repository.FileUpdateInput{
		ID: nodeID, Repo: "updateA", Path: "moved//renamed.bin",
		Content: winnerB, Metadata: &snapNew,
	}, d.updateRef)
	if err != nil || count != 1 {
		t.Fatalf("UpdateFile = (%d, %v), want (1, nil)", count, err)
	}
	d.wantUpdateOnce(t)
	if d.saved.ID != nodeID {
		t.Fatalf("delegate received id %d, want the lookup id %d (no allocation)", d.saved.ID, nodeID)
	}
	if p := d.saved.Path; !p.IsFile() || p.Repo() != "updateA" || p.Parent() != "moved" || p.Name() != "renamed.bin" {
		t.Fatalf("delegate path = repo %q parent %q name %q file=%v, want the mapped moved/renamed.bin",
			p.Repo(), p.Parent(), p.Name(), p.IsFile())
	}
	if d.saved.Content != winnerB {
		t.Fatalf("delegate content = %+v, want winner B verbatim", d.saved.Content)
	}
	equalSnapshot(t, d.saved.Metadata, snapNew)
	cfRequireBound(t, adm, schema, 0)

	// 同事务：新身份与完整新快照可见（原始 SQL 投影 + Lookup），旧路径缺
	// 失；未提交更新对独立连接不可见（仍只见已提交旧行）。
	// Same transaction: the new identity and the full new snapshot are visible
	// (raw SQL projection plus Lookup) while the old path misses; the
	// uncommitted update stays invisible to the independent pool (only the
	// committed old row shows).
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	var nodeType, depth int16
	var repo, nodePath, nodeName string
	if err := tx.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT node_type, repo, node_path, node_name, depth FROM %s.nodes WHERE node_id = $1", schema), nodeID).
		Scan(&nodeType, &repo, &nodePath, &nodeName, &depth); err != nil {
		t.Fatalf("in-tx node read: %v", err)
	}
	if nodeType != 1 || repo != "updateA" || nodePath != "moved" || nodeName != "renamed.bin" || depth != 2 {
		t.Fatalf("in-tx projection = (%d %q %q %q %d), want the mapped new identity",
			nodeType, repo, nodePath, nodeName, depth)
	}
	gotNew, err := st.Lookup(qctx, fileRefOf(t, 0, "updateA", "moved", "renamed.bin", winnerB).Path)
	if err != nil {
		t.Fatalf("in-tx Lookup(new): %v", err)
	}
	if gotNew.ID != nodeID || gotNew.Content != winnerB {
		t.Fatalf("in-tx Lookup(new) = (id %d, %+v), want the lookup id with winner B", gotNew.ID, gotNew.Content)
	}
	equalSnapshot(t, gotNew.Metadata, snapNew)
	if _, err := st.Lookup(qctx, fileRefOf(t, 0, "updateA", "old", "a.bin", winnerA).Path); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("in-tx Lookup(old) = %v, want sql.ErrNoRows", err)
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("independent nodes = %d, want 1 before the caller rollback", n)
	}
	ufRequireOldNode(t, adm, schema, nodeID, "updateA", "old", "a.bin", winnerA)
	cfRequireBound(t, adm, schema, 0)

	// 由测试调用方回滚，必须恰好 nil（不接受 sql.ErrTxDone 宽容）。
	// The TEST caller rolls back and must get exactly nil (no tolerant
	// acceptance of sql.ErrTxDone).
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback must return nil, got %v", err)
	}

	// 回滚保留：旧行投影+七列、两条 binary 与 bytes、上界（更新回滚不删除
	// 既有行——缺行覆盖见下方独立建立的缺席 ID）。
	// Rollback preservation: the old row's projection and seven columns, both
	// binaries and both payloads' bytes, and the bound (an update rollback
	// never deletes the existing row — absence coverage uses the separately
	// established absent ID below).
	ufRequireOldNode(t, adm, schema, nodeID, "updateA", "old", "a.bin", winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)
	cfRequireObjectBytes(t, store, winnerA.SHA1, payloadA)
	cfRequireObjectBytes(t, store, winnerB.SHA1, payloadB)
	if n := cfCountFiles(t, root); n != 2 {
		t.Fatalf("object files after rollback = %d, want exactly 2", n)
	}
	cfRequireBound(t, adm, schema, 0)
	cfRequireCurrent(t, schema, 0)

	// 尾段：缺失正 ID 先独立确认缺席（所有者更正），再验证计数 0 原样直通、
	// 委托恰一次、不转创建。nil Metadata 拒绝是无 SQL 的单元守卫，此处不重
	// 复同一拒绝。
	// Tail: the absent positive ID is INDEPENDENTLY confirmed absent first
	// (owner correction), then count 0 must pass through verbatim with the
	// delegate fired exactly once and no create. The nil-Metadata rejection is
	// the no-SQL unit guard and its identical rejection is not repeated here.
	const absentID = 987654
	if nodeIDExists(t, adm, schema, absentID) {
		t.Fatal("the absent positive id must be independently confirmed absent before use")
	}
	tx2, st2 := nodeTx(t, db)
	d2 := &ufDelegates{st: st2}
	zero, err := repository.UpdateFile(ctx, repository.FileUpdateInput{
		ID: absentID, Repo: "updateA", Path: "x/y.bin",
		Content: winnerA, Metadata: &snapOld,
	}, d2.updateRef)
	if err != nil || zero != 0 {
		t.Fatalf("UpdateFile(absent id) = (%d, %v), want (0, nil) verbatim", zero, err)
	}
	d2.wantUpdateOnce(t)
	if d2.saved.ID != absentID {
		t.Fatalf("absent-id delegate received id %d, want %d", d2.saved.ID, absentID)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("tail rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after absent-id update = %d, want 1 (no create)", n)
	}
	if nodeIDExists(t, adm, schema, absentID) {
		t.Fatal("the absent id must stay absent (no create from a zero-count update)")
	}
	cfRequireBound(t, adm, schema, 0)
}

// 场景 UA2：自有 schema 的 AFTER UPDATE 延迟约束触发器在更新事务开启前创建
// （P0001 画像的 UPDATE 变体；INSERT 变体已由创建回归覆盖）：真实消费缝先返
// 回临时成功 1 且同事务新状态可见、独立 admin 仍见旧投影；随后调用方真实
// Commit 失败，SQLSTATE 恰为 P0001（不是 sql.ErrTxDone/超时/断连）。这是确定
// 的延迟约束提交失败。失败后旧行投影+七列、两条 binary 与 bytes、上界全部
// 保留；隔离 schema 清理会删除自有触发器/函数，无需重跑场景。
// Scenario UA2: an own-schema AFTER UPDATE deferred constraint trigger is
// created BEFORE the update transaction opens (the UPDATE variant of the P0001
// profile; the INSERT variant is already covered by the create regression):
// the real consumer seam first returns provisional success 1 with the new
// state visible in-transaction while the independent admin still sees the old
// projection; the caller's real Commit then fails with SQLSTATE exactly P0001
// (not sql.ErrTxDone, a timeout or a disconnect). This is a DETERMINATE
// deferred-constraint commit failure. Afterwards the old row's projection and
// seven columns, both binaries, the bytes and the bound all survive; the
// isolated-schema cleanup drops the owned trigger/function, so no rerun is
// needed.
func TestUpdateFileDeferredConstraintCommitFailureP0001(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	svc, store, root := cfBinaryService(t, db)

	payloadA := "binflow update-file regression payload B"
	winnerA, err := svc.Add(ctx, strings.NewReader(payloadA), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(A): %v", err)
	}
	payloadB := "binflow update-file regression payload B2"
	winnerB, err := svc.Add(ctx, strings.NewReader(payloadB), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add(B): %v", err)
	}
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)
	if n := cfCountFiles(t, root); n != 2 {
		t.Fatalf("object files = %d, want exactly 2 after the two Adds", n)
	}

	snapOld := ufOldSnapshot()
	nodeID := ufCommitNodeWithSnapshot(t, db, 2201, "updateB", "old", "a.bin", winnerA, &snapOld)

	// 先建触发器再开更新事务：必须延迟，否则测的是 UPDATE 失败而非临时成
	// 功后的提交失败。
	// Create the trigger BEFORE opening the update transaction: it must be
	// deferred, otherwise the test would exercise an UPDATE failure instead of
	// a commit failure after provisional success.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"CREATE FUNCTION %s.bfupdate_block() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'deferred test constraint'; END $$ LANGUAGE plpgsql", schema)); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"CREATE CONSTRAINT TRIGGER bfupdate_block AFTER UPDATE ON %s.nodes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s.bfupdate_block()", schema, schema)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	snapNew := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  5555555555555,
		ModifiedMillis: 1,
		UpdatedMillis:  2,
		CreatedBy:      repository.NewActor(mdUnits("updater")),
		ModifiedBy:     repository.NewActor(nil),
		SHA1Original:   mdUnits("up-orig"),
		MD5Original:    nil,
	})

	tx, st := nodeTx(t, db)
	d := &ufDelegates{st: st}
	count, err := repository.UpdateFile(ctx, repository.FileUpdateInput{
		ID: nodeID, Repo: "updateB", Path: "moved//b.bin",
		Content: winnerB, Metadata: &snapNew,
	}, d.updateRef)
	if err != nil || count != 1 {
		t.Fatalf("UpdateFile under the deferred trigger = (%d, %v), want provisional (1, nil)", count, err)
	}
	d.wantUpdateOnce(t)

	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	var inTx int
	if err := tx.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT count(*) FROM %s.nodes WHERE node_id = $1 AND node_path = 'moved'", schema), nodeID).
		Scan(&inTx); err != nil || inTx != 1 {
		t.Fatalf("in-tx updated rows = (%d, %v), want 1 (provisional update really written)", inTx, err)
	}
	// 独立观察：未提交更新不可见，旧行仍按已提交状态可见；上界未动。
	// Independent observation: the uncommitted update stays invisible and the
	// old row shows its committed state; the bound is untouched.
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("independent nodes = %d, want 1 before the commit", n)
	}
	ufRequireOldNode(t, adm, schema, nodeID, "updateB", "old", "a.bin", winnerA)
	cfRequireBound(t, adm, schema, 0)

	// 调用方真实 Commit 失败：RAISE EXCEPTION 的默认 SQLSTATE 恰为 P0001
	//（pgCodeOf 已经要求驱动原因可达，ErrTxDone/超时/断连都会在此失配）。
	// The real caller Commit fails: the RAISE EXCEPTION default SQLSTATE is
	// exactly P0001 (pgCodeOf already demands a reachable driver cause, so
	// ErrTxDone, timeouts and disconnects all mismatch here).
	commitErr := tx.Commit()
	if commitErr == nil {
		t.Fatal("caller Commit must fail under the deferred constraint")
	}
	if code := pgCodeOf(t, commitErr); code != "P0001" {
		t.Fatalf("deferred commit SQLSTATE = %s, want P0001", code)
	}

	// 提交失败保留：旧行投影+七列、两条 binary 与 bytes、上界。
	// Failed-commit preservation: the old row's projection and seven columns,
	// both binaries, the bytes and the bound.
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after the failed commit = %d, want 1", n)
	}
	ufRequireOldNode(t, adm, schema, nodeID, "updateB", "old", "a.bin", winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerA)
	ufRequireBinaryFields(t, adm, schema, winnerB)
	cfRequireObjectBytes(t, store, winnerA.SHA1, payloadA)
	cfRequireObjectBytes(t, store, winnerB.SHA1, payloadB)
	if n := cfCountFiles(t, root); n != 2 {
		t.Fatalf("object files after the failed commit = %d, want exactly 2", n)
	}
	cfRequireBound(t, adm, schema, 0)
	cfRequireCurrent(t, schema, 0)
}

// 场景 UA3：真实 Registry 预置同 SHA-1、真实 MD5/长度、SHA-256=NBSP 的存量
// 行，同 payload 的真实 binary.Service.Add 返回仍是 NBSP 的胜出记录；初始节
// 点以规范 streaming 摘要提交（NBSP 行同 SHA-1 满足外键），ID 来自真实
// Lookup。把 winner（绝非 streaming 替身）与显式快照原样交给新 UpdateFile：
// 委托恰一次收到逐字段一致的 Content（含 NBSP）与同一 Metadata 指针，旧
// Store 在 SQL 前以 errors.Is(ErrInvalidReference) 拒绝且无任何
// pgconn.PgError。调用方事务仍可用并回滚 nil；登记行四字段恰一行、fresh
// provider 读回的 bytes、对象路径/Stat、上界全部保留，无重试/清理。标签：
// 十六进制拒绝是显式本地规范边界（目标 mapper 原样透传 sha2）——本地接缝
// 证明，绝非目标 parity。
// Scenario UA3: a real Registry preloaded with the same SHA-1, the real
// MD5/length and SHA-256=NBSP makes the real binary.Service.Add on the same
// payload return the winner with the SHA-256 STILL NBSP; the initial node is
// committed with the canonical streaming digests (the NBSP row's shared SHA-1
// satisfies the FK) and its ID comes from a real Lookup. Passing the winner
// (never a streaming stand-in) plus the explicit snapshot verbatim into the
// new UpdateFile: the delegate fires exactly once receiving the field-identical
// Content (NBSP included) and the very same Metadata pointer, and the old
// Store rejects BEFORE any SQL with errors.Is(ErrInvalidReference) and no
// pgconn.PgError at all. The caller transaction stays usable and rolls back
// nil; the registration row's four fields stay the single row, the bytes read
// back through a fresh provider, the object path/Stat and the bound all
// survive, with no retry and no cleanup. Label: the hex rejection is the
// explicit LOCAL canonical boundary (the target mapper passes sha2 verbatim)
// — a local seam proof, never target parity.
func TestUpdateFileNBSPWinnerPreSQLRejectionKeepsRegistration(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	// 读回一律走 fresh provider（沿用创建场景 C 的约定），原始 store 句柄不
	// 再单独使用。
	// Read-back goes exclusively through a fresh provider (the scenario-C
	// convention); the original store handle is intentionally unused here.
	svc, _, root := cfBinaryService(t, db)

	const nbsp = "\u00a0"
	payload := "binflow update-file regression payload C"
	streaming, err := content.Calculate(ctx, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("content.Calculate: %v", err)
	}
	if len(streaming.SHA256) != 64 || streaming.SHA256 == nbsp {
		t.Fatalf("streaming SHA-256 = %d chars, want a normal 64-char digest distinct from NBSP", len(streaming.SHA256))
	}

	// NBSP 存量画像先落库（真实 MD5/长度），再让真实 provider+Registry 的
	// Add 撞上它。
	// Seed the stored NBSP profile first (real MD5/length), then let the real
	// provider+Registry Add hit it.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"INSERT INTO %s.binaries (sha1, md5, bin_length, sha256) VALUES ($1, $2, $3, $4)", schema),
		streaming.SHA1, streaming.MD5, streaming.Size, nbsp); err != nil {
		t.Fatalf("seed NBSP sha256 row: %v", err)
	}
	winner, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add: %v", err)
	}
	wantWinner := content.Metadata{SHA1: streaming.SHA1, SHA256: nbsp, MD5: streaming.MD5, Size: streaming.Size}
	if winner != wantWinner || winner.SHA256 != nbsp {
		t.Fatalf("Add winner = %+v, want the stored NBSP record %+v", winner, wantWinner)
	}
	cfRequireBinaryRow(t, adm, schema, streaming.SHA1, streaming.MD5, streaming.Size, nbsp)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files = %d, want exactly 1 after Add", n)
	}
	objPath, objInfo := soleObjectFile(t, root)

	// 初始已提交节点内容用规范 streaming 摘要，更新侧才携带 NBSP 胜出记录。
	// The committed seed node carries the canonical streaming digests; only
	// the update side carries the NBSP winner.
	snapOld := ufOldSnapshot()
	nodeID := ufCommitNodeWithSnapshot(t, db, 2301, "updateC", "old", "a.bin", streaming, &snapOld)

	snapNew := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  6666666666666,
		ModifiedMillis: 3,
		UpdatedMillis:  4,
		CreatedBy:      repository.NewActor(nil),
		ModifiedBy:     repository.NewActor(mdUnits("")),
		SHA1Original:   nil,
		MD5Original:    mdUnits(""),
	})

	tx, st := nodeTx(t, db)
	d := &ufDelegates{st: st}
	count, err := repository.UpdateFile(ctx, repository.FileUpdateInput{
		ID: nodeID, Repo: "updateC", Path: "nbsp//renamed.bin",
		Content: winner, Metadata: &snapNew,
	}, d.updateRef)
	if err == nil || count != 0 {
		t.Fatalf("UpdateFile with the NBSP winner = (%d, %v), want (0, error)", count, err)
	}
	if !errors.Is(err, refpg.ErrInvalidReference) {
		t.Fatalf("UpdateFile error must preserve the old Store cause ErrInvalidReference, got %v", err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("old Store canonical rejection must stay before SQL (no PgError), got SQLSTATE %s", pgErr.Code)
	}
	// 委托恰一次且收到原样输入：消费者把十六进制规范检查提前到委托前会在
	// 此失配；偷换 streaming 摘要则上方预期的拒绝失配。
	// The delegate fired exactly once with the verbatim input: hoisting a hex
	// canonical check above the delegate mismatches here, and silently
	// substituting the streaming digest mismatches the expected rejection
	// above.
	d.wantUpdateOnce(t)
	if d.saved.ID != nodeID {
		t.Fatalf("delegate received id %d, want the lookup id %d", d.saved.ID, nodeID)
	}
	if d.saved.Content != winner || d.saved.Content.SHA256 != nbsp {
		t.Fatalf("delegate received %+v, want the NBSP winner verbatim", d.saved.Content)
	}
	if d.saved.Metadata != &snapNew {
		t.Fatal("delegate must receive the caller's Metadata pointer verbatim (same pointer)")
	}

	cfRequireBound(t, adm, schema, 0)
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	var one int
	if err := tx.QueryRowContext(qctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("caller tx must stay usable after the pre-SQL rejection: (%d, %v)", one, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback must return nil, got %v", err)
	}

	// 拒绝后保留：旧行投影+七列、登记行四字段恰一行、fresh provider bytes、
	// 对象路径/Stat、上界全部不动。
	// Post-rejection preservation: the old row's projection and seven columns,
	// the registration row's four fields as the single row, the fresh-provider
	// bytes, the object path/Stat and the bound all stay unchanged.
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("nodes after the rejection = %d, want 1", n)
	}
	ufRequireOldNode(t, adm, schema, nodeID, "updateC", "old", "a.bin", streaming)
	cfRequireBinaryRow(t, adm, schema, streaming.SHA1, streaming.MD5, streaming.Size, nbsp)
	freshStore, err := filestore.New(root)
	if err != nil {
		t.Fatalf("filestore.New(fresh): %v", err)
	}
	t.Cleanup(func() { _ = freshStore.Close() })
	cfRequireObjectBytes(t, freshStore, winner.SHA1, payload)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files after the rejection = %d, want exactly 1 (no cleanup)", n)
	}
	afterPath, afterInfo := soleObjectFile(t, root)
	if afterPath != objPath || !os.SameFile(objInfo, afterInfo) {
		t.Fatalf("object replaced across the rejection: %q -> %q", objPath, afterPath)
	}
	cfRequireBound(t, adm, schema, 0)
	cfRequireCurrent(t, schema, 0)
}

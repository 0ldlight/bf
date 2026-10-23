// 新 CreateFile 消费缝的真实 PostgreSQL 回归（BIN-151）：真实号段预留闭包
// （Current + 恰一次 Reserve(expected,1)）与调用方事务上真实 Store.Create 的
// 计数薄委托，覆盖三个有区分度的场景——A 调用方回滚后独立预留与已发布
// 内容保留；B 自有延迟约束让调用方真实 Commit 以 SQLSTATE P0001 失败；
// C Registry 的 NBSP 胜出记录原样进入旧 Store 的 SQL 前拒绝：ID 已消耗、
// 登记行与物理 bytes 保留。不镜像 codec/迁移/八进程门禁（既有测试与共享
// check-postgres.sh 继续承担）。
// Real-PostgreSQL regression for the new CreateFile consumer seam (BIN-151):
// counting thin delegates over the real reservation primitive (Current plus
// exactly one Reserve(expected,1)) and the real Store.Create on the caller
// transaction cover three meaningful scenarios — A the caller rollback
// preserving the independent reservation and the published content; B an
// owned deferred constraint failing the real caller Commit with SQLSTATE
// P0001; C the registry's NBSP winner reaching the old Store's pre-SQL
// rejection verbatim with the ID consumed while the registration row and
// physical bytes survive. No codec/migration/eight-process gate is mirrored
// here (the existing tests and the shared check-postgres.sh keep owning
// those).
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	nodereg "github.com/0ldlight/bf/internal/registry/postgres"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// cfReservations 在本测试的隔离 schema 上显式应用一次号段预留模式
// （unique_ids 表 + general 种子行，见共享约定），返回绑定同一连接池的
// Reservations：调用方节点事务占一个连接时，独立 Reserve 仍可用池内其余
// 连接（池容量保持 4，不得缩为 1）。
// cfReservations applies the reservation schema exactly once on this test's
// isolated schema (the unique_ids table plus the general seed row, per the
// shared convention) and returns Reservations bound to the same pool: while
// the caller node transaction holds one connection the independent Reserve
// still reaches a free one (the pool capacity stays 4 and must not shrink
// to 1).
func cfReservations(t *testing.T, db *sql.DB) *refpg.Reservations {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, refpg.ReservationSchemaSQL); err != nil {
		t.Fatalf("apply reservation schema: %v", err)
	}
	return newIDRes(t, db)
}

// cfBinaryService 复用 TestNodeFailureKeepsBinaryAndRetrySucceeds 的真实组合：
// 同 schema 的真实 Registry、t.TempDir() 上的真实 filestore 与 binary.Service
// （非 mock、不改产品），供各场景先真实发布内容与登记。
// cfBinaryService reuses the real composition of
// TestNodeFailureKeepsBinaryAndRetrySucceeds: the real Registry on the same
// schema, a real filestore on t.TempDir() and a binary.Service (no mocks, no
// product changes) so every scenario publishes real content and registration
// first.
func cfBinaryService(t *testing.T, db *sql.DB) (*binary.Service, *filestore.Store, string) {
	t.Helper()
	reg, err := nodereg.New(db)
	if err != nil {
		t.Fatalf("registry New: %v", err)
	}
	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc, err := binary.New(store, reg)
	if err != nil {
		t.Fatalf("binary.New: %v", err)
	}
	return svc, store, root
}

// cfCountFiles 真实统计 store 根下常规文件个数：断言“恰好一个对象”必须真实
// 计数——既有 soleObjectFile 只定位最后一个非目录项、不计数（BIN-151 验收
// 合同）。
// cfCountFiles really counts the regular files under the store root: claiming
// "exactly one object" requires a real count — the existing soleObjectFile
// only locates the last non-directory entry and counts nothing (the BIN-151
// acceptance contract).
func cfCountFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk store root: %v", err)
	}
	return n
}

// cfRequireObjectBytes 打开对象并逐字节核对 payload。
// cfRequireObjectBytes opens the object and verifies the payload byte for
// byte.
func cfRequireObjectBytes(t *testing.T, store *filestore.Store, sha1, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	obj, err := store.Open(ctx, sha1)
	if err != nil {
		t.Fatalf("open published object: %v", err)
	}
	got, readErr := io.ReadAll(obj)
	obj.Close()
	if readErr != nil {
		t.Fatalf("read published object: %v", readErr)
	}
	if string(got) != payload {
		t.Fatalf("object bytes changed: got %d bytes, want the %d-byte payload", len(got), len(payload))
	}
}

// cfRequireBound 同时核对持久上界数值与 general 行存在性（只看数值无法区分
// 缺行）。
// cfRequireBound checks both the persisted bound value and the presence of
// the general row (the value alone cannot distinguish a missing row).
func cfRequireBound(t *testing.T, adm *sql.DB, schema string, want int64) {
	t.Helper()
	bound, present := boundOf(t, adm, schema)
	if bound != want || !present {
		t.Fatalf("independent general bound = (%d, present=%v), want (%d, true)", bound, present, want)
	}
}

// cfRequireCurrent 用全新连接池上的 Reservations 独立读取已提交上界。
// cfRequireCurrent reads the committed bound through Reservations on a
// brand-new pool.
func cfRequireCurrent(t *testing.T, schema string, want int64) {
	t.Helper()
	fresh := newIDRes(t, registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cur, err := fresh.Current(ctx)
	if err != nil || cur != want {
		t.Fatalf("fresh Reservations.Current = (%d, %v), want (%d, nil)", cur, err, want)
	}
}

// cfRequireBinaryRow 独立核对登记行四字段并要求恰好一行。
// cfRequireBinaryRow independently verifies the four registration columns and
// requires exactly one row.
func cfRequireBinaryRow(t *testing.T, adm *sql.DB, schema string, wantSHA1, wantMD5 string, wantLen int64, wantSHA256 string) {
	t.Helper()
	gotSHA1, gotMD5, gotLen, gotSHA256, ok := rowOf(t, adm, schema, wantSHA1)
	if !ok || gotSHA1 != wantSHA1 || gotMD5 != wantMD5 || gotLen != wantLen ||
		!gotSHA256.Valid || gotSHA256.String != wantSHA256 {
		t.Fatalf("binaries row = (%q %q %d %v present=%v), want (%q %q %d %q)",
			gotSHA1, gotMD5, gotLen, gotSHA256, ok, wantSHA1, wantMD5, wantLen, wantSHA256)
	}
	if n := countRows(t, adm, schema); n != 1 {
		t.Fatalf("binaries rows = %d, want exactly 1", n)
	}
}

// cfDelegates 是三个场景共用的计数真实委托。nextID 依序调用真实
// Reservations.Current 与恰好一次 Reserve(expected,1)：只有 err=nil、ok 且
// First=Last=expected+1 才在独立 admin boundOf 确认新上界已提交后返回
// First；竞争失败显式报错，绝不隐含重试。insert 是薄计数委托，直接调用本
// 场景调用方事务上的真实 Store.Create（计数不替代真实数据库行为，也不预
// 分配常数 ID/fake Store 后另行调用）。saved 保存 insert 实际收到的引用，
// 供“胜出记录原样到达旧 Store”断言。
// cfDelegates is the counting real-delegate pair shared by the three
// scenarios. nextID calls the real Reservations.Current and then exactly one
// Reserve(expected,1): only with err=nil, ok and First=Last=expected+1 does
// it return First after the independent admin boundOf confirms the new bound
// is committed; contention turns into an explicit closure error, never a
// hidden retry. insert is a thin counting delegate calling the real
// Store.Create on this scenario's caller transaction directly (the counter
// never replaces real database behavior, and no constant preallocated ID or
// fake Store is ever called separately). saved keeps the reference insert
// actually received for the "winner reached the old Store verbatim"
// assertion.
type cfDelegates struct {
	t       *testing.T
	res     *refpg.Reservations
	adm     *sql.DB
	schema  string
	st      *refpg.Store
	next    int
	reserve int
	insert  int
	saved   repository.FileReference
}

// nextID 是真实窄消费缝委托：计数 → Current → 恰一次 Reserve(expected,1)
// → 独立确认已提交上界 → 返回 First。
// nextID is the real narrow-seam delegate: count, Current, exactly one
// Reserve(expected,1), an independent committed-bound confirmation, then
// First.
func (d *cfDelegates) nextID(ctx context.Context) (int64, error) {
	d.next++
	cur, err := d.res.Current(ctx)
	if err != nil {
		return 0, fmt.Errorf("create-file nextID: read current bound: %w", err)
	}
	got, ok, err := d.res.Reserve(ctx, cur, 1)
	d.reserve++
	if err != nil {
		return 0, fmt.Errorf("create-file nextID: reserve: %w", err)
	}
	if !ok {
		return 0, errors.New("create-file nextID: lost the reservation race; explicit contention error, no retry")
	}
	if got.First != cur+1 || got.Last != cur+1 {
		return 0, fmt.Errorf("create-file nextID: reserved {%d %d}, want inclusive {%d %d}",
			got.First, got.Last, cur+1, cur+1)
	}
	if bound, present := boundOf(d.t, d.adm, d.schema); !present || bound != got.Last {
		return 0, fmt.Errorf("create-file nextID: committed bound = (%d, present=%v), want (%d, true)",
			bound, present, got.Last)
	}
	return got.First, nil
}

// insertRef 是计数薄委托：直接调用本场景调用方事务的真实 Store.Create。
// insertRef is the counting thin delegate: it calls the real Store.Create on
// this scenario's caller transaction directly.
func (d *cfDelegates) insertRef(ctx context.Context, ref repository.FileReference) (int64, error) {
	d.insert++
	d.saved = ref
	return d.st.Create(ctx, ref)
}

// wantCounts 核对两个消费缝各恰一次且无自动重试。
// wantCounts verifies each seam fired exactly once with no automatic repeat.
func (d *cfDelegates) wantCounts(t *testing.T) {
	t.Helper()
	if d.next != 1 || d.reserve != 1 || d.insert != 1 {
		t.Fatalf("callback counts = next %d / reserve %d / insert %d, want 1/1/1 (no automatic repeat)",
			d.next, d.reserve, d.insert)
	}
}

// 场景 A：先真实发布内容与登记，再开启调用方节点事务，经新 CreateFile 拿到
// 临时成功；同事务可见全部节点+元数据字段而独立连接不可见，独立 Reserve 已
// 提交推进；调用方 Rollback 恰好 nil 后节点缺席、上界与 binary/bytes 保留。
// 这组断言把“外部不可见 + 同 tx 可见 + 调用方真实回滚”合在一起：内部擅自
// Commit/Rollback 或根本没写都会被当场检出。
// Scenario A: publish real content and registration first, open the caller
// node transaction, then obtain PROVISIONAL success through the new
// CreateFile; the same transaction sees every node+metadata field while the
// independent pool sees nothing and the independent Reserve has committed
// its advance; after the caller's Rollback returns exactly nil the node is
// absent while the bound and the binary/bytes survive. The combined
// "invisible outside + visible in tx + real caller rollback" assertions
// catch an internal premature Commit/Rollback — or no write at all — on the
// spot.
func TestCreateFileCallerRollbackKeepsIndependentReservation(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	res := cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	svc, store, root := cfBinaryService(t, db)

	payload := "binflow create-file regression payload A"
	winner, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add: %v", err)
	}
	cfRequireBinaryRow(t, adm, schema, winner.SHA1, winner.MD5, winner.Size, winner.SHA256)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files = %d, want exactly 1 after Add", n)
	}
	objPath, objInfo := soleObjectFile(t, root)

	// 先开启调用方节点事务：Reserve 必须在该事务存活期间独立提交。
	// Open the caller node transaction FIRST: the Reserve must commit
	// independently while this transaction stays alive.
	tx, st := nodeTx(t, db)
	snap := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  1111111111111,
		ModifiedMillis: 1222222222222,
		UpdatedMillis:  1333333333333,
		CreatedBy:      repository.NewActor(nil),
		ModifiedBy:     repository.NewActor(mdUnits("create-a")),
		SHA1Original:   nil,
		MD5Original:    mdUnits(""),
	})
	d := &cfDelegates{t: t, res: res, adm: adm, schema: schema, st: st}
	id, err := repository.CreateFile(ctx, repository.FileCreateInput{
		Repo: "createA", Path: "a//b.bin", Content: winner, Metadata: &snap,
	}, d.nextID, d.insertRef)
	if err != nil || id != 1 {
		t.Fatalf("CreateFile = (%d, %v), want (1, nil)", id, err)
	}
	d.wantCounts(t)

	// 同事务直接 SQL：raw a//b.bin 的已知映射 parent=a、name=b.bin、depth=2，
	// 加全部内容投影字段。
	// Same-transaction direct SQL: the known mapping of raw a//b.bin
	// (parent=a, name=b.bin, depth=2) plus every content projection column.
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	var nodeType, depth int16
	var repo, nodePath, nodeName, sha1Actual, md5Actual, sha256Actual string
	var binLength int64
	if err := tx.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT node_type, repo, node_path, node_name, depth, bin_length, sha1_actual, md5_actual, sha256 "+
			"FROM %s.nodes WHERE node_id = 1", schema)).
		Scan(&nodeType, &repo, &nodePath, &nodeName, &depth, &binLength, &sha1Actual, &md5Actual, &sha256Actual); err != nil {
		t.Fatalf("in-tx node read: %v", err)
	}
	if nodeType != 1 || repo != "createA" || nodePath != "a" || nodeName != "b.bin" || depth != 2 ||
		binLength != winner.Size || sha1Actual != winner.SHA1 || md5Actual != winner.MD5 || sha256Actual != winner.SHA256 {
		t.Fatalf("in-tx projection = (%d %q %q %q %d %d %q %q %q), want mapped node with winner content",
			nodeType, repo, nodePath, nodeName, depth, binLength, sha1Actual, md5Actual, sha256Actual)
	}

	// 同事务 Lookup + equalSnapshot：完整 Metadata 未被消费者丢弃（空/NULL
	// actor 与 original 各有取例；未提交快照只能走同事务，不能用 admin 的
	// metadataRowOf）。
	// Same-transaction Lookup plus equalSnapshot: the full Metadata survives
	// the consumer (empty/NULL actor and original examples included; an
	// uncommitted snapshot is only readable in-transaction, never through the
	// admin-side metadataRowOf).
	gotRef, err := st.Lookup(qctx, fileRefOf(t, 0, "createA", "a", "b.bin", winner).Path)
	if err != nil {
		t.Fatalf("in-tx Lookup: %v", err)
	}
	if gotRef.ID != 1 || gotRef.Content != winner || !gotRef.Path.IsFile() ||
		gotRef.Path.Parent() != "a" || gotRef.Path.Name() != "b.bin" {
		t.Fatalf("in-tx Lookup = (id %d, %+v), want id 1 with the mapped path and winner content", gotRef.ID, gotRef.Content)
	}
	equalSnapshot(t, gotRef.Metadata, snap)

	// 独立观察：节点不可见（ID 与任意路径行都无）；上界已独立推进且 general
	// 行存在。
	// Independent observation: the node stays invisible (no ID and no path
	// row at all); the bound advanced independently with the general row
	// present.
	if nodeIDExists(t, adm, schema, 1) {
		t.Fatal("uncommitted node must stay invisible to the independent admin pool")
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("independent nodes = %d, want 0 before the caller rollback", n)
	}
	cfRequireBound(t, adm, schema, 1)

	// 由测试调用方回滚，必须恰好 nil（不接受 sql.ErrTxDone 宽容）。
	// The TEST caller rolls back and must get exactly nil (no tolerant
	// acceptance of sql.ErrTxDone).
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback must return nil, got %v", err)
	}

	if nodeIDExists(t, adm, schema, 1) {
		t.Fatal("node id must be absent after the caller rollback")
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("nodes after rollback = %d, want 0", n)
	}
	cfRequireBound(t, adm, schema, 1)
	cfRequireCurrent(t, schema, 1)
	cfRequireBinaryRow(t, adm, schema, winner.SHA1, winner.MD5, winner.Size, winner.SHA256)
	cfRequireObjectBytes(t, store, winner.SHA1, payload)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files after rollback = %d, want exactly 1", n)
	}
	afterPath, afterInfo := soleObjectFile(t, root)
	if afterPath != objPath || !os.SameFile(objInfo, afterInfo) {
		t.Fatalf("object replaced across the rollback: %q -> %q", objPath, afterPath)
	}
}

// 场景 B：自有 schema 的延迟约束触发器只作用于 nodes INSERT（沿用
// TestNodeDeferredCommitFailureRowAbsent 模式）：CreateFile 先返回临时成功且
// 同 tx 节点可见、独立 admin 不可见；随后调用方真实 Commit 失败，SQLSTATE
// 恰为 P0001（不是 sql.ErrTxDone/超时/断连，也不只是 err 非 nil）。这是确定
// 的延迟约束提交失败，不是结果不确定的网络提交。失败后节点缺席、上界与
// binary/bytes 保留；隔离 schema 清理会删除自有触发器/函数，无需重跑场景。
// Scenario B: an own-schema deferred constraint trigger touching only nodes
// INSERTs (the TestNodeDeferredCommitFailureRowAbsent pattern): CreateFile
// first returns PROVISIONAL success with the node visible in-tx and invisible
// to the independent admin; the caller's real Commit then fails with exactly
// SQLSTATE P0001 (not sql.ErrTxDone, a timeout or a disconnect — and not
// merely a nonnil err). This is a DETERMINATE deferred-constraint commit
// failure, not an ambiguous network commit. Afterwards the node is absent
// while the bound and the binary/bytes survive; the isolated-schema cleanup
// drops the owned trigger/function, so no scenario rerun is needed.
func TestCreateFileDeferredConstraintCommitFailureP0001(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	res := cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	svc, store, root := cfBinaryService(t, db)

	payload := "binflow create-file regression payload B"
	winner, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{})
	if err != nil {
		t.Fatalf("svc.Add: %v", err)
	}
	cfRequireBinaryRow(t, adm, schema, winner.SHA1, winner.MD5, winner.Size, winner.SHA256)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files = %d, want exactly 1 after Add", n)
	}
	objPath, objInfo := soleObjectFile(t, root)

	// 先设置触发器，再开启节点事务：触发器必须延迟，否则测的是 INSERT 失败
	// 而非临时成功后的提交失败。
	// Create the trigger BEFORE opening the node transaction: it must be
	// deferred, otherwise the test would exercise an INSERT failure instead
	// of a commit failure after provisional success.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"CREATE FUNCTION %s.bfnode_block() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'deferred test constraint'; END $$ LANGUAGE plpgsql", schema)); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"CREATE CONSTRAINT TRIGGER bfnode_block AFTER INSERT ON %s.nodes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s.bfnode_block()", schema, schema)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	tx, st := nodeTx(t, db)
	d := &cfDelegates{t: t, res: res, adm: adm, schema: schema, st: st}
	id, err := repository.CreateFile(ctx, repository.FileCreateInput{
		Repo: "createB", Path: "defer.bin", Content: winner,
	}, d.nextID, d.insertRef)
	if err != nil || id != 1 {
		t.Fatalf("CreateFile under the deferred trigger = (%d, %v), want provisional (1, nil)", id, err)
	}
	d.wantCounts(t)

	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	var inTx int
	if err := tx.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT count(*) FROM %s.nodes WHERE node_id = 1", schema)).Scan(&inTx); err != nil || inTx != 1 {
		t.Fatalf("in-tx node count = (%d, %v), want 1 (provisional row really written)", inTx, err)
	}
	if nodeIDExists(t, adm, schema, 1) {
		t.Fatal("uncommitted node must stay invisible to the independent admin pool before the commit")
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("independent nodes = %d, want 0 before the commit", n)
	}
	cfRequireBound(t, adm, schema, 1)

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

	if nodeIDExists(t, adm, schema, 1) {
		t.Fatal("node id must be absent after the failed commit")
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("nodes after the failed commit = %d, want 0", n)
	}
	cfRequireBound(t, adm, schema, 1)
	cfRequireCurrent(t, schema, 1)
	cfRequireBinaryRow(t, adm, schema, winner.SHA1, winner.MD5, winner.Size, winner.SHA256)
	cfRequireObjectBytes(t, store, winner.SHA1, payload)
	if n := cfCountFiles(t, root); n != 1 {
		t.Fatalf("object files after the failed commit = %d, want exactly 1", n)
	}
	afterPath, afterInfo := soleObjectFile(t, root)
	if afterPath != objPath || !os.SameFile(objInfo, afterInfo) {
		t.Fatalf("object replaced across the failed commit: %q -> %q", objPath, afterPath)
	}
}

// 场景 C：真实 Registry 预置同 SHA-1、真实 MD5/长度、SHA-256=NBSP 的存量行
// （TestRegisterPreservesNBSPSHA256AsNonBlank 画像）；同 payload 的
// binary.Service.Add 必须返回仍是 NBSP 的胜出记录。把 winner（绝非 streaming
// 替身）原样传给新 CreateFile：真实 ID 已消耗、insert 已到达一次，旧 Store
// 在 SQL 前以 errors.Is(ErrInvalidReference) 拒绝且无任何 pgconn.PgError。
// 调用方事务仍可用并回滚 nil；登记行四字段与恰一行、fresh provider 读回的
// bytes、对象路径/Stat 全部保留，无重试/清理。
// Scenario C: a real Registry preloaded with the same SHA-1, the real
// MD5/length and SHA-256=NBSP (the TestRegisterPreservesNBSPSHA256AsNonBlank
// profile); binary.Service.Add on the same payload must return the winner
// with the SHA-256 STILL NBSP. Passing that winner (never a streaming
// stand-in) verbatim into the new CreateFile: the real ID is consumed, insert
// is reached once, and the old Store rejects BEFORE any SQL with
// errors.Is(ErrInvalidReference) and no pgconn.PgError at all. The caller
// transaction stays usable and rolls back nil; the registration row's four
// fields and single row, the bytes read back through a fresh provider and the
// object path/Stat all survive, with no retry and no cleanup.
func TestCreateFileNBSPWinnerPreSQLRejectionKeepsRegistration(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	res := cfReservations(t, db)
	cfRequireBound(t, adm, schema, 0)
	// 场景 C 的读回一律走 fresh provider，原始 store 不再单独使用。
	// Scenario C reads back exclusively through a fresh provider; the
	// original store handle is intentionally unused here.
	svc, _, root := cfBinaryService(t, db)

	const nbsp = "\u00a0"
	payload := "binflow create-file regression payload C"
	streaming, err := content.Calculate(ctx, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("content.Calculate: %v", err)
	}
	if len(streaming.SHA256) != 64 || streaming.SHA256 == nbsp {
		t.Fatalf("streaming SHA-256 = %d chars, want a normal 64-char digest distinct from NBSP", len(streaming.SHA256))
	}

	// 存量画像先落库（真实 MD5/长度），再让真实 provider+Registry 的 Add 撞上
	// 它。
	// Seed the stored profile first (real MD5/length), then let the real
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

	// 开节点 tx 后把 winner 原样交给新服务（Metadata=nil，普通合法非根文件）。
	// Open the node tx and hand the winner VERBATIM to the new service
	// (Metadata=nil, an ordinary legal non-root file).
	tx, st := nodeTx(t, db)
	d := &cfDelegates{t: t, res: res, adm: adm, schema: schema, st: st}
	id, err := repository.CreateFile(ctx, repository.FileCreateInput{
		Repo: "createC", Path: "nbsp.bin", Content: winner,
	}, d.nextID, d.insertRef)
	if err == nil || id != 0 {
		t.Fatalf("CreateFile with the NBSP winner = (%d, %v), want (0, error)", id, err)
	}
	if !errors.Is(err, refpg.ErrInvalidReference) {
		t.Fatalf("CreateFile error must preserve the old Store cause ErrInvalidReference, got %v", err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("old Store canonical rejection must stay before SQL (no PgError), got SQLSTATE %s", pgErr.Code)
	}
	// ID 已消耗、insert 已真实到达一次（若有人把 canonical 检查提前到 ID 前，
	// 上界/计数在此失配；若偷偷换用 streaming 摘要，节点会成功、上方预期拒绝
	// 失配）。
	// The ID is consumed and insert really reached the Store once (moving the
	// canonical check before the ID fails the bound/counts here; silently
	// substituting the streaming digest makes the node succeed and the
	// expected rejection above mismatch).
	d.wantCounts(t)
	if d.saved.Content != winner || d.saved.Content.SHA256 != nbsp {
		t.Fatalf("insert received %+v, want the NBSP winner verbatim", d.saved.Content)
	}

	// 独立上界已消耗；调用方事务仍可用，由调用方回滚 nil。
	// The independent bound shows the consumed ID; the caller transaction
	// stays usable and the caller rolls back nil.
	cfRequireBound(t, adm, schema, 1)
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	var one int
	if err := tx.QueryRowContext(qctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("caller tx must stay usable after the pre-SQL rejection: (%d, %v)", one, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback must return nil, got %v", err)
	}

	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("independent nodes = %d, want 0", n)
	}
	cfRequireBound(t, adm, schema, 1)
	cfRequireCurrent(t, schema, 1)
	cfRequireBinaryRow(t, adm, schema, streaming.SHA1, streaming.MD5, streaming.Size, nbsp)

	// fresh provider 读回同 payload；对象路径与 Stat 保持，恰好一个对象。
	// A fresh provider reads the same payload back; the object path and Stat
	// stay, with exactly one object.
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
}

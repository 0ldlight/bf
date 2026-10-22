// v1→v2 元数据加列升级与守卫式物理回退的真实 PostgreSQL 场景（BIN-142）：
// 全新 v2 目录形状、对精确冻结 v1 模式的显式升级、旧投影写入兼容、升级
// 事务回滚、重复升级 42701、全 NULL down 保留行、有数据 down 拒绝、非
// READ COMMITTED 与启用 RLS 的 down 拒绝，以及取锁等待后仍能看到刚提交
// 元数据的确定性回归。
// Real-PostgreSQL scenarios for the v1→v2 additive metadata upgrade and the
// guarded physical downgrade (BIN-142): the fresh v2 catalog shape, the
// explicit upgrade applied to an exact frozen v1 schema, legacy-projection
// write compatibility, upgrade-transaction rollback, repeated-upgrade 42701,
// all-NULL downgrade keeping rows, populated downgrade refusal, non-READ-
// COMMITTED and RLS-enabled downgrade refusals, and the deterministic
// regression that a lock-waiting downgrade still sees freshly committed
// metadata.
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

	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// v1NodesSchema 是 BIN-132 冻结的 v1 全新 nodes DDL（本测试按原文嵌入，
// 用于验证对精确旧模式的显式升级）。
// v1NodesSchema is the frozen BIN-132 v1 fresh nodes DDL (embedded verbatim
// here to verify the explicit upgrade against the exact old schema).
const v1NodesSchema = `CREATE TABLE IF NOT EXISTS nodes (
    node_id     bigint        NOT NULL,
    node_type   smallint      NOT NULL CHECK (node_type IN (0, 1)),
    repo        varchar(64)   NOT NULL COLLATE "C",
    node_path   varchar(1024) NOT NULL COLLATE "C",
    node_name   varchar(255)  NOT NULL COLLATE "C",
    depth       smallint      NOT NULL,
    bin_length  bigint,
    sha1_actual varchar(40),
    md5_actual  varchar(32),
    sha256      varchar(64),
    CONSTRAINT nodes_pk PRIMARY KEY (node_id),
    CONSTRAINT nodes_binaries_fk FOREIGN KEY (sha1_actual) REFERENCES binaries (sha1)
);
CREATE UNIQUE INDEX IF NOT EXISTS nodes_repo_path_name_idx
    ON nodes (repo varchar_pattern_ops, node_path varchar_pattern_ops,
              node_name varchar_pattern_ops);
CREATE INDEX IF NOT EXISTS nodes_sha1_actual_idx ON nodes (sha1_actual);`

// v1Fixture 在隔离 schema 中应用冻结 v1 模式（binaries 由 newRegistry 先建）。
// v1Fixture applies the frozen v1 schema inside an isolated schema (newRegistry
// creates binaries first).
func v1Fixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	_, schema := newRegistry(t)
	db := registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, v1NodesSchema); err != nil {
		t.Fatalf("apply frozen v1 nodes schema: %v", err)
	}
	return db, schema
}

// oldProjectionInsert/oldProjectionUpdate 是 v1 二进制的显式 v1 列清单写入
// （不含任何元数据列）：升级后旧投影写入必须继续工作且不覆盖新元数据。
// oldProjectionInsert/oldProjectionUpdate are the explicit v1-column-list
// writes of a v1 binary (no metadata columns): after the upgrade the legacy
// projection must keep working without touching new metadata.
const (
	oldProjectionInsert = "INSERT INTO nodes (node_id, node_type, repo, node_path, node_name, depth, " +
		"bin_length, sha1_actual, md5_actual, sha256) VALUES ($1, 1, $2, $3, $4, $5, $6, $7, $8, $9)"
	oldProjectionUpdate = "UPDATE nodes SET repo = $2, node_path = $3, node_name = $4, depth = $5, " +
		"bin_length = $6, sha1_actual = $7, md5_actual = $8, sha256 = $9 WHERE node_id = $1 AND node_type = 1"
)

// metadataColumnsOf 目录核实七列的存在性/非空性/默认值。
// metadataColumnsOf verifies the seven columns' presence/nullability/
// defaults through the catalog.
func metadataColumnsOf(t *testing.T, adm *sql.DB, schema string) map[string][2]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := adm.QueryContext(ctx, `
		SELECT a.attname, a.attnotnull, a.atthasdef
		FROM pg_attribute a
		JOIN pg_class r ON r.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace
		WHERE n.nspname = $1 AND r.relname = 'nodes'
		  AND a.attname IN ('created','modified','updated','created_by','modified_by','sha1_original','md5_original')
		  AND a.attnum > 0 AND NOT a.attisdropped`, schema)
	if err != nil {
		t.Fatalf("query nodes catalog: %v", err)
	}
	defer rows.Close()
	cols := map[string][2]bool{}
	for rows.Next() {
		var name string
		var notNull, hasDefault bool
		if err := rows.Scan(&name, &notNull, &hasDefault); err != nil {
			t.Fatalf("scan catalog row: %v", err)
		}
		cols[name] = [2]bool{notNull, hasDefault}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("catalog iteration: %v", err)
	}
	return cols
}

// metadataShapeConstraintOf 返回 nodes_metadata_shape 的 contype（无则空）。
// metadataShapeConstraintOf returns the nodes_metadata_shape contype (empty
// when absent).
func metadataShapeConstraintOf(t *testing.T, adm *sql.DB, schema string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var contype string
	err := adm.QueryRowContext(ctx, `
		SELECT c.contype FROM pg_constraint c
		JOIN pg_class r ON r.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = r.relnamespace
		WHERE n.nspname = $1 AND r.relname = 'nodes' AND c.conname = 'nodes_metadata_shape'`,
		schema).Scan(&contype)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("query shape constraint: %v", err)
	}
	return contype
}

// metadataColumnsAbsent 断言七列已不存在（SELECT 报 42703）。
// metadataColumnsAbsent asserts the seven columns are gone (SELECT fails
// with 42703).
func metadataColumnsAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "SELECT created, modified, updated, created_by, modified_by, sha1_original, md5_original FROM nodes WHERE false"); err == nil {
		t.Fatal("metadata columns must be absent after downgrade/rollback")
	} else if code := pgCodeOf(t, err); code != "42703" {
		t.Fatalf("absent-column SQLSTATE = %s, want 42703", code)
	}
}

func TestMetadataMigrationFreshSchemaIsV2(t *testing.T) {
	_, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)

	// 七列全部存在、可空、无默认值；形状约束以 CHECK 存在。
	// All seven columns exist, nullable, with no defaults; the shape
	// constraint exists as a CHECK.
	cols := metadataColumnsOf(t, adm, schema)
	if len(cols) != 7 {
		t.Fatalf("fresh schema has %d metadata columns (%v), want 7", len(cols), cols)
	}
	for name, nd := range cols {
		if nd[0] {
			t.Errorf("fresh nodes.%s must be nullable", name)
		}
		if nd[1] {
			t.Errorf("fresh nodes.%s must have no default", name)
		}
	}
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "c" {
		t.Fatalf("nodes_metadata_shape contype = %q, want CHECK ('c')", ct)
	}
	// 形状约束行为：created 已知而 modified 未知 → 23514；全 NULL → 通过。
	// Constraint behavior: a known created with unknown modified is rejected
	// with 23514; all NULL passes.
	seedNodeBinary(t, adm, schema, strings.Repeat("1", 40))
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, err := adm.ExecContext(qctx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth, created) "+
			"VALUES (1, 1, 'r', '.', 'f', 2, 3)", schema)); err == nil {
		t.Fatal("partial metadata shape must violate nodes_metadata_shape")
	} else if code := pgCodeOf(t, err); code != "23514" {
		t.Fatalf("partial shape SQLSTATE = %s, want 23514", code)
	}
	if _, err := adm.ExecContext(qctx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth) "+
			"VALUES (2, 1, 'r', '.', 'g', 2)", schema)); err != nil {
		t.Fatalf("all-NULL metadata insert must pass: %v", err)
	}
}

func TestMetadataMigrationUpgradeFromExactV1(t *testing.T) {
	db, schema := v1Fixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("2", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// v1 二进制写入（显式 v1 列清单）。
	// A v1 binary write (the explicit v1 column list).
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, err := db.ExecContext(qctx, oldProjectionInsert,
		801, "repoV1", "old", "a.bin", 3, int64(21), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("legacy v1 insert: %v", err)
	}
	// 显式一次性升级（受信任 search_path 契约，经绑定该 schema 的池执行）。
	// The explicit one-time upgrade (trusted search_path contract, executed
	// through the schema-bound pool).
	if _, err := db.ExecContext(qctx, refpg.MetadataUpgradeSQL); err != nil {
		t.Fatalf("apply MetadataUpgradeSQL: %v", err)
	}
	cols := metadataColumnsOf(t, adm, schema)
	if len(cols) != 7 {
		t.Fatalf("upgraded table has %d metadata columns (%v), want 7", len(cols), cols)
	}
	for name, nd := range cols {
		if nd[0] || nd[1] {
			t.Errorf("upgraded nodes.%s must stay nullable without default", name)
		}
	}
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "c" {
		t.Fatalf("upgraded nodes_metadata_shape contype = %q, want 'c'", ct)
	}
	// v1 行保留且七列全 NULL：Lookup 返回 nil 元数据。
	// The v1 row survives with seven NULL columns: Lookup reports nil
	// metadata.
	if created, modified, _, _, _, _, _ := metadataRowOf(t, adm, schema, 801); created.Valid || modified.Valid {
		t.Fatalf("upgraded v1 row must keep unknown (NULL) history, got %v/%v", created, modified)
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoV1", "old", "a.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup(v1 row): %v", err)
	}
	if got.Metadata != nil {
		t.Fatalf("v1-history Lookup metadata = %+v, want nil", got.Metadata)
	}
	// 升级后的表同时服务新投影：带元数据的 Create/commit 成功。
	// The upgraded table also serves the new projection: a metadata-bearing
	// Create commits.
	snap := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 9, ModifiedMillis: 8, UpdatedMillis: 7,
		CreatedBy: repository.NewActor(mdUnits("v2-writer")),
	})
	refNew := fileRefOf(t, 802, "repoV1", "new", "b.bin", meta)
	refNew.Metadata = &snap
	if n, err := lookSt.Create(ctx, refNew); err != nil || n != 1 {
		t.Fatalf("post-upgrade Create = (%d, %v), want (1, nil)", n, err)
	}
	// 旧投影 UPDATE（显式 v1 列清单，同事务）不覆盖新元数据；提交后旧投影
	// INSERT 继续插入全 NULL 元数据行。
	// A legacy-projection UPDATE (the explicit v1 column list, same
	// transaction) never touches the new metadata; after the commit a
	// legacy INSERT keeps inserting all-NULL metadata rows.
	if _, err := lookTx.ExecContext(qctx, oldProjectionUpdate,
		802, "repoV1", "moved", "b.bin", 3, int64(22), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("legacy v1 update: %v", err)
	}
	if err := lookTx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if created, _, _, createdBy, _, _, _ := metadataRowOf(t, adm, schema, 802); !created.Valid || created.Int64 != 9 || createdBy.String != "v2-writer" {
		t.Fatalf("legacy update clobbered metadata: %v/%v", created, createdBy)
	}
	if _, err := db.ExecContext(qctx, oldProjectionInsert,
		803, "repoV1", "old", "c.bin", 3, int64(21), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("legacy v1 insert after upgrade: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 3 {
		t.Fatalf("node count = %d, want 3", n)
	}
}

func TestMetadataMigrationUpgradeRollback(t *testing.T) {
	db, _ := v1Fixture(t)
	ctx := nodeTestContext(t)
	// 升级在调用方事务内执行；回滚后 DDL 一并消失。
	// The upgrade runs inside a caller transaction; the rollback removes
	// the DDL with it.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, err := tx.ExecContext(qctx, refpg.MetadataUpgradeSQL); err != nil {
		t.Fatalf("in-tx upgrade: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	metadataColumnsAbsent(t, db)
}

func TestMetadataMigrationRepeatedUpgrade42701(t *testing.T) {
	db, _ := v1Fixture(t)
	ctx := nodeTestContext(t)
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, err := db.ExecContext(qctx, refpg.MetadataUpgradeSQL); err != nil {
		t.Fatalf("first upgrade: %v", err)
	}
	_, err := db.ExecContext(qctx, refpg.MetadataUpgradeSQL)
	if err == nil {
		t.Fatal("repeated upgrade must fail instead of swallowing schema drift")
	}
	if code := pgCodeOf(t, err); code != "42701" {
		t.Fatalf("repeated upgrade SQLSTATE = %s, want 42701", code)
	}
}

func TestMetadataMigrationDownAllNullKeepsRows(t *testing.T) {
	db, schema := v1Fixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("4", 40)
	seedNodeBinary(t, adm, schema, sha1)
	qctx, qcancel := context.WithTimeout(ctx, 15*time.Second)
	defer qcancel()
	if _, err := db.ExecContext(qctx, oldProjectionInsert,
		804, "repoD1", "d", "keep.bin", 3, int64(21), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("legacy v1 insert: %v", err)
	}
	if _, err := db.ExecContext(qctx, refpg.MetadataUpgradeSQL); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	// 全 NULL 元数据：物理回退通过并保留行（不自动清空任何数据——本来就没有）。
	// All-NULL metadata: the physical downgrade passes and keeps the rows
	// (nothing is auto-cleared — there is nothing to clear).
	if _, err := db.ExecContext(qctx, refpg.MetadataDowngradeSQL); err != nil {
		t.Fatalf("all-null downgrade: %v", err)
	}
	metadataColumnsAbsent(t, db)
	var repo, nodePath, nodeName string
	var binLength sql.NullInt64
	if err := adm.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT repo, node_path, node_name, bin_length FROM %s.nodes WHERE node_id = 804", schema)).
		Scan(&repo, &nodePath, &nodeName, &binLength); err != nil {
		t.Fatalf("row must survive the downgrade: %v", err)
	}
	if repo != "repoD1" || nodePath != "d" || nodeName != "keep.bin" || !binLength.Valid || binLength.Int64 != 21 {
		t.Fatalf("surviving row = %q/%q/%q/%v, want the original v1 projection", repo, nodePath, nodeName, binLength)
	}
	// 回退后 v1 投影写入继续工作。
	// The v1 projection keeps working after the downgrade.
	if _, err := db.ExecContext(qctx, oldProjectionInsert,
		805, "repoD1", "d", "post.bin", 3, int64(21), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("post-downgrade v1 insert: %v", err)
	}
}

func TestMetadataMigrationDownPopulatedRefused(t *testing.T) {
	db, schema := v1Fixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("5", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)
	qctx, qcancel := context.WithTimeout(ctx, 15*time.Second)
	defer qcancel()
	if _, err := db.ExecContext(qctx, refpg.MetadataUpgradeSQL); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	snap := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 12, ModifiedMillis: 13, UpdatedMillis: 14,
		CreatedBy:    repository.NewActor(mdUnits("keeper")),
		SHA1Original: mdUnits("keep"),
	})
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, 806, "repoD2", "d", "pop.bin", meta)
	ref.Metadata = &snap
	if n, err := st.Create(ctx, ref); err != nil || n != 1 {
		t.Fatalf("Create with metadata = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// 任何已填充元数据行：down 以固定 55000 拒绝，绝不自动清空数据。
	// Any populated metadata row: the downgrade refuses with the fixed
	// 55000 and never auto-clears data.
	_, err := db.ExecContext(qctx, refpg.MetadataDowngradeSQL)
	if err == nil {
		t.Fatal("populated downgrade must be refused")
	}
	if code := pgCodeOf(t, err); code != "55000" {
		t.Fatalf("populated downgrade SQLSTATE = %s, want 55000", code)
	}
	// 列与数据原样保留（拒绝即无丢失）。
	// Columns and data stay intact (refusal means no loss).
	if created, modified, updated, createdBy, _, sha1Orig, _ := metadataRowOf(t, adm, schema, 806); !created.Valid ||
		created.Int64 != 12 || !modified.Valid || modified.Int64 != 13 || !updated.Valid ||
		updated.Int64 != 14 || createdBy.String != "keeper" || sha1Orig.String != "keep" {
		t.Fatalf("refused downgrade must leave metadata intact, got %v/%v/%v/%v/%v",
			created, modified, updated, createdBy, sha1Orig)
	}
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "c" {
		t.Fatalf("refused downgrade must keep the shape constraint, got %q", ct)
	}
}

func TestMetadataMigrationDownRefusesUnsafeIsolation(t *testing.T) {
	db, _ := nodeFixture(t)
	ctx := nodeTestContext(t)
	// 非 READ COMMITTED（旧快照可能漏看刚提交行）以固定 25000 拒绝。
	// Non-READ-COMMITTED (an older snapshot could miss freshly committed
	// rows) is rejected with the fixed 25000.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatalf("begin repeatable read: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	_, err = tx.ExecContext(qctx, refpg.MetadataDowngradeSQL)
	if err == nil {
		t.Fatal("non-READ-COMMITTED downgrade must be refused")
	}
	if code := pgCodeOf(t, err); code != "25000" {
		t.Fatalf("unsafe isolation downgrade SQLSTATE = %s, want 25000", code)
	}
	// 回滚后列仍在。
	// After the rollback the columns remain.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := db.ExecContext(qctx, "SELECT created FROM nodes WHERE false"); err != nil {
		t.Fatalf("columns must remain after the refused downgrade: %v", err)
	}
}

func TestMetadataMigrationDownRefusesRLS(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	qctx, qcancel := context.WithTimeout(ctx, 15*time.Second)
	defer qcancel()
	// 启用 RLS（测试自身以 schema 限定名操作；产品语句保持受信任
	// search_path 契约）→ 以固定 55000 拒绝，而非依赖策略恰好隐藏行。
	// An RLS-enabled table (the test toggles it through the schema-qualified
	// name; the product statements keep the trusted search_path contract)
	// is refused with the fixed 55000 rather than relying on policies
	// happening to hide rows.
	if _, err := adm.ExecContext(qctx, fmt.Sprintf("ALTER TABLE %s.nodes ENABLE ROW LEVEL SECURITY", schema)); err != nil {
		t.Fatalf("enable RLS: %v", err)
	}
	_, err := db.ExecContext(qctx, refpg.MetadataDowngradeSQL)
	if err == nil {
		t.Fatal("RLS-enabled downgrade must be refused")
	}
	if code := pgCodeOf(t, err); code != "55000" {
		t.Fatalf("RLS downgrade SQLSTATE = %s, want 55000", code)
	}
	if _, err := db.ExecContext(qctx, "SELECT created FROM nodes WHERE false"); err != nil {
		t.Fatalf("columns must remain after the refused downgrade: %v", err)
	}
}

func TestMetadataMigrationDownWaitsThenSeesCommittedMetadata(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("6", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// 无元数据行已提交；会话 A 在未提交事务中为该行补写元数据（持 ROW
	// EXCLUSIVE 表锁）；会话 B 执行 down，先在 ACCESS EXCLUSIVE 上等待，
	// A 提交后 B 必须看到该元数据并以 55000 拒绝——READ COMMITTED + 取锁
	// 后检查的组合不允许漏看已提交行。
	// A row without metadata is committed; session A adds metadata for it
	// inside an uncommitted transaction (holding the ROW EXCLUSIVE table
	// lock); session B runs the downgrade, first waiting behind the ACCESS
	// EXCLUSIVE request, and after A commits B MUST see that metadata and
	// refuse with 55000 — the READ COMMITTED plus check-after-lock pairing
	// may not miss committed rows.
	tx, st := nodeTx(t, db)
	if n, err := st.Create(ctx, fileRefOf(t, 807, "repoW", "d", "w.bin", meta)); err != nil || n != 1 {
		t.Fatalf("Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	txA, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin A: %v", err)
	}
	actx, acancel := context.WithTimeout(ctx, 10*time.Second)
	defer acancel()
	if _, err := txA.ExecContext(actx,
		"UPDATE nodes SET created = $2, modified = $2, updated = $2, created_by = $3 WHERE node_id = $1 AND node_type = 1",
		807, int64(21), "late-writer"); err != nil {
		t.Fatalf("A adds metadata in-tx: %v", err)
	}
	connB, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("dedicated conn B: %v", err)
	}
	defer connB.Close()
	var pid int64
	if err := connB.QueryRowContext(actx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("read B pid: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := connB.ExecContext(ctx, refpg.MetadataDowngradeSQL)
		done <- err
	}()
	// 等待 B 确实阻塞在锁上（pg_stat_activity 证据），再提交 A：断言不依赖
	// 睡眠时长，只依赖确定的锁序。
	// Wait until B is genuinely blocked on the lock (pg_stat_activity
	// evidence) before committing A: the assertion depends on the
	// deterministic lock order, not on sleep durations.
	blocked := false
	for i := 0; i < 200; i++ {
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
		var wait string
		err := adm.QueryRowContext(pctx,
			"SELECT wait_event_type FROM pg_stat_activity WHERE pid = $1", pid).Scan(&wait)
		pcancel()
		if err == nil && wait == "Lock" {
			blocked = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("downgrade never blocked behind the uncommitted metadata writer")
	}
	if err := txA.Commit(); err != nil {
		t.Fatalf("commit A: %v", err)
	}
	berr := <-done
	if berr == nil {
		t.Fatal("downgrade must refuse after the lock wait")
	}
	if code := pgCodeOf(t, berr); code != "55000" {
		t.Fatalf("post-wait downgrade SQLSTATE = %s, want 55000", code)
	}
	// 列与刚提交的元数据都完好。
	// Both the columns and the freshly committed metadata survive.
	if created, modified, updated, createdBy, _, _, _ := metadataRowOf(t, adm, schema, 807); !created.Valid ||
		created.Int64 != 21 || !modified.Valid || modified.Int64 != 21 || !updated.Valid ||
		updated.Int64 != 21 || createdBy.String != "late-writer" {
		t.Fatalf("committed metadata lost after refused downgrade: %v/%v/%v/%v",
			created, modified, updated, createdBy)
	}
}

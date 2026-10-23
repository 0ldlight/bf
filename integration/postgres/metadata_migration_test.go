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

// metadataColumn 记录一列的目录事实：NOT NULL、默认值与 format_type 的精确
// 类型（含宽度）。
// metadataColumn records a column's catalog facts: NOT NULL, default and the
// exact type (width included) from format_type.
type metadataColumn struct {
	notNull    bool
	hasDefault bool
	typ        string
}

// metadataColumnTypes 七列的精确类型契约：fresh v2 与升级后的表都必须逐列
// 相同——任何类型/宽度漂移（例如 md5_original 被改成 VARCHAR(32)）都必须
// 失败。
// The exact type contract of the seven columns: the fresh v2 table and the
// upgraded table must match column by column — any type/width drift (for
// example md5_original changed to VARCHAR(32)) must fail.
var metadataColumnTypes = map[string]string{
	"created":       "bigint",
	"modified":      "bigint",
	"updated":       "bigint",
	"created_by":    "character varying(64)",
	"modified_by":   "character varying(64)",
	"sha1_original": "character varying(1024)",
	"md5_original":  "character varying(1024)",
}

// assertMetadataColumns 核对七列：全部存在、可空、无默认值且类型/宽度与
// 契约逐列精确一致。
// assertMetadataColumns checks the seven columns: all present, nullable, no
// default, with each column's type/width exactly matching the contract.
func assertMetadataColumns(t *testing.T, label string, cols map[string]metadataColumn) {
	t.Helper()
	if len(cols) != 7 {
		t.Fatalf("%s table has %d metadata columns (%v), want 7", label, len(cols), cols)
	}
	for name, want := range metadataColumnTypes {
		col, ok := cols[name]
		if !ok {
			t.Errorf("%s nodes.%s missing from catalog", label, name)
			continue
		}
		if col.notNull {
			t.Errorf("%s nodes.%s must be nullable", label, name)
		}
		if col.hasDefault {
			t.Errorf("%s nodes.%s must have no default", label, name)
		}
		if col.typ != want {
			t.Errorf("%s nodes.%s type = %q, want %q", label, name, col.typ, want)
		}
	}
}

// metadataColumnsOf 目录核实七列的存在性/非空性/默认值与精确类型。
// metadataColumnsOf verifies the seven columns' presence/nullability/
// defaults and exact type through the catalog.
func metadataColumnsOf(t *testing.T, adm *sql.DB, schema string) map[string]metadataColumn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := adm.QueryContext(ctx, `
		SELECT a.attname, a.attnotnull, a.atthasdef, format_type(a.atttypid, a.atttypmod)
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
	cols := map[string]metadataColumn{}
	for rows.Next() {
		var col metadataColumn
		var name string
		if err := rows.Scan(&name, &col.notNull, &col.hasDefault, &col.typ); err != nil {
			t.Fatalf("scan catalog row: %v", err)
		}
		cols[name] = col
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

// metadataColumnsAbsent 断言目录中七个元数据列一个都不剩且 nodes_metadata_shape
// 不存在：SELECT 报 42703 只能证明“至少一列”缺失（只删 created 的部分回退
// 同样得到 42703），目录计数才能证明全部消失。行为面的 42703 直查仍保留。
// metadataColumnsAbsent asserts the catalog holds zero of the seven metadata
// columns and no nodes_metadata_shape constraint: a SELECT failing with 42703
// proves only that at least one column is missing (a partial downgrade dropping
// just created still yields 42703), so the catalog count is what proves full
// removal. The behavioral 42703 probe stays as a complement.
func metadataColumnsAbsent(t *testing.T, adm, db *sql.DB, schema string) {
	t.Helper()
	if cols := metadataColumnsOf(t, adm, schema); len(cols) != 0 {
		t.Fatalf("%d metadata columns remain after downgrade/rollback (%v), want 0", len(cols), cols)
	}
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "" {
		t.Fatalf("nodes_metadata_shape contype = %q after downgrade/rollback, want absent", ct)
	}
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

	// 七列全部存在、可空、无默认值且类型/宽度精确（bigint×3、varchar(64)×2、
	// varchar(1024)×2）；形状约束以 CHECK 存在。
	// All seven columns exist, nullable, with no defaults and exact types/
	// widths (bigint×3, varchar(64)×2, varchar(1024)×2); the shape constraint
	// exists as a CHECK.
	cols := metadataColumnsOf(t, adm, schema)
	assertMetadataColumns(t, "fresh", cols)
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
	assertMetadataColumns(t, "upgraded", cols)
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
	// 旧投影 UPDATE（显式 v1 列清单，同事务）不覆盖新元数据且恰好命中一行；
	// 提交后旧投影 INSERT 继续插入全 NULL 元数据行。
	// A legacy-projection UPDATE (the explicit v1 column list, same
	// transaction) never touches the new metadata and affects exactly one
	// row; after the commit a legacy INSERT keeps inserting all-NULL metadata
	// rows.
	res, err := lookTx.ExecContext(qctx, oldProjectionUpdate,
		802, "repoV1", "moved", "b.bin", 3, int64(22), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64))
	if err != nil {
		t.Fatalf("legacy v1 update: %v", err)
	}
	if n, rerr := res.RowsAffected(); rerr != nil || n != 1 {
		t.Fatalf("legacy v1 update rows = (%d, %v), want (1, nil)", n, rerr)
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
	db, schema := v1Fixture(t)
	adm := adminPool(t)
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
	metadataColumnsAbsent(t, adm, db, schema)
}

func TestMetadataMigrationRepeatedUpgrade42701(t *testing.T) {
	db, schema := v1Fixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("8", 40)
	seedNodeBinary(t, adm, schema, sha1)
	qctx, qcancel := context.WithTimeout(ctx, 15*time.Second)
	defer qcancel()
	// 一行既有 v1 数据先落库：重复升级失败后它必须原样幸存。
	// One existing v1 row lands first: it must survive the repeated-upgrade
	// failure untouched.
	if _, err := db.ExecContext(qctx, oldProjectionInsert,
		808, "repoRP", "d", "r.bin", 3, int64(21), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("legacy v1 insert: %v", err)
	}
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
	// 失败不得造成任何部分漂移：七列、形状约束与既有数据全数保留。
	// The failure must leave no partial drift: all seven columns, the shape
	// constraint and the existing data stay intact.
	assertMetadataColumns(t, "post-repeat-upgrade", metadataColumnsOf(t, adm, schema))
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "c" {
		t.Fatalf("post-repeat nodes_metadata_shape contype = %q, want 'c'", ct)
	}
	if created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig := metadataRowOf(t, adm, schema, 808); created.Valid ||
		modified.Valid || updated.Valid || createdBy.Valid || modifiedBy.Valid || sha1Orig.Valid || md5Orig.Valid {
		t.Fatalf("repeated-upgrade failure disturbed the v1 row's unknown history: %v/%v/%v/%v/%v/%v/%v",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
	var repo, nodePath, nodeName string
	var binLength sql.NullInt64
	if err := adm.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT repo, node_path, node_name, bin_length FROM %s.nodes WHERE node_id = 808", schema)).
		Scan(&repo, &nodePath, &nodeName, &binLength); err != nil {
		t.Fatalf("row read after repeated upgrade: %v", err)
	}
	if repo != "repoRP" || nodePath != "d" || nodeName != "r.bin" || !binLength.Valid || binLength.Int64 != 21 {
		t.Fatalf("v1 projection after repeated upgrade = %q/%q/%q/%v, want the original", repo, nodePath, nodeName, binLength)
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
	metadataColumnsAbsent(t, adm, db, schema)
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
		ModifiedBy:   repository.NewActor(mdUnits("mkeeper")),
		SHA1Original: mdUnits("keep"),
		MD5Original:  mdUnits("keepmd5"),
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
	// 全部七列、形状约束与数据原样保留（拒绝即无丢失）。
	// All seven columns, the shape constraint and the data stay intact
	// (refusal means no loss).
	assertMetadataColumns(t, "post-refused-downgrade", metadataColumnsOf(t, adm, schema))
	if created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig := metadataRowOf(t, adm, schema, 806); !created.Valid ||
		created.Int64 != 12 || !modified.Valid || modified.Int64 != 13 || !updated.Valid ||
		updated.Int64 != 14 || createdBy.String != "keeper" || modifiedBy.String != "mkeeper" ||
		sha1Orig.String != "keep" || md5Orig.String != "keepmd5" {
		t.Fatalf("refused downgrade must leave all seven metadata fields intact, got %v/%v/%v/%v/%v/%v/%v",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "c" {
		t.Fatalf("refused downgrade must keep the shape constraint, got %q", ct)
	}
}

func TestMetadataMigrationDownRefusesUnsafeIsolation(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("9", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)
	snap := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 31, ModifiedMillis: 32, UpdatedMillis: 33,
		CreatedBy: repository.NewActor(mdUnits("iso-keeper")),
	})
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, 809, "repoISO", "d", "iso.bin", meta)
	ref.Metadata = &snap
	if n, err := st.Create(ctx, ref); err != nil || n != 1 {
		t.Fatalf("Create with metadata = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// 非 READ COMMITTED（旧快照可能漏看刚提交行）以固定 25000 拒绝。
	// Non-READ-COMMITTED (an older snapshot could miss freshly committed
	// rows) is rejected with the fixed 25000.
	rrtx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		t.Fatalf("begin repeatable read: %v", err)
	}
	t.Cleanup(func() { _ = rrtx.Rollback() })
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	_, err = rrtx.ExecContext(qctx, refpg.MetadataDowngradeSQL)
	if err == nil {
		t.Fatal("non-READ-COMMITTED downgrade must be refused")
	}
	if code := pgCodeOf(t, err); code != "25000" {
		t.Fatalf("unsafe isolation downgrade SQLSTATE = %s, want 25000", code)
	}
	// 回滚后七列、形状约束与既有数据完整。
	// After the rollback all seven columns, the shape constraint and the
	// existing data are intact.
	if err := rrtx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	assertMetadataColumns(t, "post-unsafe-isolation", metadataColumnsOf(t, adm, schema))
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "c" {
		t.Fatalf("post-unsafe-isolation nodes_metadata_shape contype = %q, want 'c'", ct)
	}
	if created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig := metadataRowOf(t, adm, schema, 809); !created.Valid ||
		created.Int64 != 31 || !modified.Valid || modified.Int64 != 32 || !updated.Valid ||
		updated.Int64 != 33 || createdBy.String != "iso-keeper" || modifiedBy.Valid || sha1Orig.Valid || md5Orig.Valid {
		t.Fatalf("unsafe-isolation refusal must leave all seven fields intact, got %v/%v/%v/%v/%v/%v/%v",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
}

func TestMetadataMigrationDownRefusesRLS(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("a", 40)
	seedNodeBinary(t, adm, schema, sha1)
	qctx, qcancel := context.WithTimeout(ctx, 15*time.Second)
	defer qcancel()
	// 一行合法旧投影（七列全 NULL）：与 populatedDown 测试分离——那个独立
	// case 已覆盖非 NULL 拒绝，此处若带非 NULL 元数据，删除 RLS 守卫后
	// populated 守卫仍会给出 55000，构成假阳性。全 NULL 数据确保 55000 只能
	// 出自 RLS 守卫本身：移除该守卫时 down 将真正执行而令本测试失败。
	// One legal legacy-projection row (seven columns all NULL), kept separate
	// from the populatedDown case: that independent test already covers the
	// non-NULL refusal, and carrying populated metadata here would keep
	// yielding 55000 from the populated guard even with the RLS guard deleted
	// — a false positive. All-NULL data ensures the 55000 can only come from
	// the RLS guard itself: removing that guard lets the downgrade actually
	// run, failing this test.
	if _, err := db.ExecContext(qctx, oldProjectionInsert,
		810, "repoRLS", "d", "rls.bin", 3, int64(21), sha1, strings.Repeat("3", 32), strings.Repeat("2", 64)); err != nil {
		t.Fatalf("legacy all-NULL insert: %v", err)
	}
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
	// 拒绝后逐列核对：七列仍全 NULL、目录列/形状约束完整、原行身份与内容
	// 未变（目录/管理读不受 RLS 影响）。
	// After the refusal, column by column: all seven stay NULL, the catalog
	// columns and the shape constraint stay intact, and the original row's
	// identity and content are unchanged (catalog/admin reads are unaffected
	// by RLS).
	assertMetadataColumns(t, "post-RLS-refusal", metadataColumnsOf(t, adm, schema))
	if ct := metadataShapeConstraintOf(t, adm, schema); ct != "c" {
		t.Fatalf("post-RLS-refusal nodes_metadata_shape contype = %q, want 'c'", ct)
	}
	if created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig := metadataRowOf(t, adm, schema, 810); created.Valid ||
		modified.Valid || updated.Valid || createdBy.Valid || modifiedBy.Valid || sha1Orig.Valid || md5Orig.Valid {
		t.Fatalf("RLS refusal must keep the legacy row's seven columns NULL, got %v/%v/%v/%v/%v/%v/%v",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
	var repo, nodePath, nodeName, sha1Actual string
	var binLength sql.NullInt64
	if err := adm.QueryRowContext(qctx, fmt.Sprintf(
		"SELECT repo, node_path, node_name, sha1_actual, bin_length FROM %s.nodes WHERE node_id = 810", schema)).
		Scan(&repo, &nodePath, &nodeName, &sha1Actual, &binLength); err != nil {
		t.Fatalf("legacy row read after RLS refusal: %v", err)
	}
	if repo != "repoRLS" || nodePath != "d" || nodeName != "rls.bin" || sha1Actual != sha1 || !binLength.Valid || binLength.Int64 != 21 {
		t.Fatalf("legacy projection after RLS refusal = %q/%q/%q/%q/%v, want the original",
			repo, nodePath, nodeName, sha1Actual, binLength)
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
	// A 的后端 PID：同一事务连接上读取，pg_blocking_pids 证据要按 PID 指认
	// 阻塞者本人。
	// Session A's backend PID, read on the same transaction connection so the
	// pg_blocking_pids evidence can name the exact blocker.
	var pidA int64
	if err := txA.QueryRowContext(actx, "SELECT pg_backend_pid()").Scan(&pidA); err != nil {
		t.Fatalf("read A pid: %v", err)
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
	// 等待 B 自己处于 Lock 等待且 pg_blocking_pids 明确指认 A 为阻塞者，再
	// 提交 A：断言依赖确定的锁序与目录证据，不以睡眠时长推断。
	// Wait until B itself reports a Lock wait AND pg_blocking_pids names A as
	// its blocker before committing A: the assertion rests on the
	// deterministic lock order and catalog evidence, never on sleep durations.
	blocked := false
	for i := 0; i < 200; i++ {
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Second)
		var wait sql.NullString
		var byA bool
		err := adm.QueryRowContext(pctx,
			"SELECT (SELECT wait_event_type FROM pg_stat_activity WHERE pid = $1), "+
				"EXISTS (SELECT 1 FROM unnest(pg_blocking_pids($1)) AS bp WHERE bp = $2)", pid, pidA).
			Scan(&wait, &byA)
		pcancel()
		if err == nil && wait.Valid && wait.String == "Lock" && byA {
			blocked = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("downgrade never blocked behind the uncommitted metadata writer (pg_blocking_pids evidence missing)")
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

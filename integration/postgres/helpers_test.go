// 真实 PostgreSQL 集成测试的共享夹具：环境门禁、一次性数据库与按测试隔离
// schema。DSN 只从环境读取且永不写入日志。
// Shared fixtures for the real-PostgreSQL integration tests: environment
// gate, disposable-database guard and per-test schema isolation. The DSN
// comes from the environment only and is never logged.
package postgresit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/0ldlight/bf/internal/registry/postgres"
)

const disposableDB = "binflow_registry_test"

// TestMain 是强制门禁：缺失 DSN 或未声明一次性数据库直接失败退出，绝不
// Skip——无真实数据库就没有本套件的意义。
// TestMain is the mandatory gate: a missing DSN or a missing disposable
// declaration fails the run outright; there is no Skip — without a real
// database this suite is meaningless.
func TestMain(m *testing.M) {
	if os.Getenv("BINFLOW_TEST_POSTGRES_DSN") == "" {
		fmt.Fprintln(os.Stderr, "postgresit: BINFLOW_TEST_POSTGRES_DSN is required for the real-PostgreSQL gate")
		os.Exit(1)
	}
	if os.Getenv("BINFLOW_TEST_POSTGRES_DISPOSABLE") != "1" {
		fmt.Fprintln(os.Stderr, "postgresit: BINFLOW_TEST_POSTGRES_DISPOSABLE must be 1: tests write to an isolated schema in a disposable database")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// adminPool 返回不带 search_path 限制的管理连接池，并验证当前数据库确实
// 是一次性测试库；任何写操作之前的硬性前置。
// adminPool returns an unrestricted management pool and verifies the
// current database really is the disposable test database, a hard
// precondition before any write.
func adminPool(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BINFLOW_TEST_POSTGRES_DSN")
	cfg, err := parseTestConfig(dsn)
	if err != nil {
		t.Fatal(err) // 固定文案，永不携带原始 DSN / fixed text only
	}
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var current string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&current); err != nil {
		t.Fatalf("query current_database: %v", err)
	}
	if current != disposableDB {
		t.Fatalf("refusing to run: current_database() = %q, want %q", current, disposableDB)
	}
	return db
}

// registryPool 返回 search_path 绑定到隔离 schema 的连接池：生产 Registry
// 使用不带模式限定的 binaries 表，测试通过可信连接配置定向 schema。
// registryPool returns a pool whose search_path is bound to the isolated
// schema: the production Registry uses the unqualified binaries table and
// tests aim it at a schema through trusted connection configuration.
func registryPool(t *testing.T, dsn, schema string) *sql.DB {
	t.Helper()
	cfg, err := parseTestConfig(dsn)
	if err != nil {
		t.Fatal(err) // 固定文案，永不携带原始 DSN / fixed text only
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	return db
}

// newRegistry 建立隔离 schema（随机十六进制名，无参数拼接面），应用 v1
// 模式并返回绑定该 schema 的 Registry；清理只删除本 schema。
// newRegistry creates an isolated schema (random hex name, no parameter
// interpolation surface), applies the v1 schema and returns a Registry
// bound to it; cleanup drops only this schema.
func newRegistry(t *testing.T) (*postgres.Registry, string) {
	t.Helper()
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	schema := "bfreg_" + hex.EncodeToString(rnd[:])
	adm := adminPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := adm.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA %s", schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer dropCancel()
		if _, err := adm.ExecContext(dropCtx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema)); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	db := registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema)
	if _, err := db.ExecContext(ctx, postgres.SchemaSQL); err != nil {
		t.Fatalf("apply v1 schema: %v", err)
	}
	reg, err := postgres.New(db)
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	return reg, schema
}

// rowOf 用独立 SQL 直读一行，返回 (sha1, md5, bin_length, sha256 NULL?)。
// rowOf reads one row through independent SQL.
func rowOf(t *testing.T, adm *sql.DB, schema, sha1 string) (string, string, int64, sql.NullString, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var gotSHA1, md5 string
	var length int64
	var sha256 sql.NullString
	err := adm.QueryRowContext(ctx,
		fmt.Sprintf("SELECT sha1, md5, bin_length, sha256 FROM %s.binaries WHERE sha1 = $1", schema), sha1).
		Scan(&gotSHA1, &md5, &length, &sha256)
	if err == sql.ErrNoRows {
		return "", "", 0, sql.NullString{}, false
	}
	if err != nil {
		t.Fatalf("independent row read: %v", err)
	}
	return gotSHA1, md5, length, sha256, true
}

// countRows 用独立 SQL 统计表行数。
// countRows counts table rows through independent SQL.
func countRows(t *testing.T, adm *sql.DB, schema string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int
	if err := adm.QueryRowContext(ctx,
		fmt.Sprintf("SELECT count(*) FROM %s.binaries", schema)).Scan(&n); err != nil {
		t.Fatalf("independent row count: %v", err)
	}
	return n
}

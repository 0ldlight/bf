// 号段预留原语的真实 PostgreSQL 核心场景（BIN-135）：初值读取、闭区间预留、
// 过期期望零结果、全新连接池不复用已关闭状态、固定 general 键、缺行
// ErrNoRows 不自动建行、真实模式约束与 MaxInt64 溢出边界。场景共用专用
// bfids_ 隔离夹具（不经 binaries/registry 设置）以留在门禁时限内，每条
// 行为断言保持独立；四个新测试各自拥有随机 schema、只读环境、无全局
// DDL，可安全并行。事务/并发/提交失败场景在 id_reservations_tx_test.go。
// Real-PostgreSQL core scenarios for the ID-range reservation primitive:
// initial bound read, inclusive reservation, stale-expectation zero result,
// fresh pool without closed-state reuse, the fixed general key, missing-row
// ErrNoRows without automatic row creation, real schema constraints and the
// MaxInt64 overflow boundary. Scenarios share the dedicated bfids_ isolated
// fixture (no binaries/registry setup) to stay inside the gate timeout while
// every behavioral assertion stays separate; each of the four new tests owns
// a random schema, reads the environment only and issues no global DDL, so
// they run safely in parallel. Transaction/concurrency/commit-failure
// scenarios live in id_reservations_tx_test.go.
package postgresit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	idrespg "github.com/0ldlight/bf/internal/repository/postgres"
)

// idResFixture 建立专用 bfids_ 隔离 schema（随机十六进制名，无参数拼接
// 面）：一个 adminPool 守卫 + 一个 schema 绑定连接池 + 显式应用
// ReservationSchemaSQL；不做 binaries/registry 设置（号段场景不需要）。
// 返回的 adm 供测试复用，避免重开多余连接池；清理只删除本 schema。
// idResFixture creates the dedicated bfids_ isolated schema (random hex
// name, no parameter interpolation surface): one adminPool guard, one
// schema-bound pool and an explicit ReservationSchemaSQL application; no
// binaries/registry setup (ID scenarios need none). Tests reuse the returned
// adm instead of reopening extra pools; cleanup drops only this schema.
func idResFixture(t *testing.T) (*sql.DB, *sql.DB, string) {
	t.Helper()
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	schema := "bfids_" + hex.EncodeToString(rnd[:])
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
	if _, err := db.ExecContext(ctx, idrespg.ReservationSchemaSQL); err != nil {
		t.Fatalf("apply reservation schema: %v", err)
	}
	return db, adm, schema
}

// newIDRes 构造绑定连接池的 Reservations；构造失败即测试失败。
// newIDRes builds a Reservations bound to the pool; a constructor failure
// fails the test.
func newIDRes(t *testing.T, db *sql.DB) *idrespg.Reservations {
	t.Helper()
	r, err := idrespg.NewReservations(db)
	if err != nil {
		t.Fatalf("NewReservations: %v", err)
	}
	return r
}

// boundOf 用独立管理连接直读 general 行持久上界（绕开被测对象）。
// boundOf reads the persisted general bound through an independent admin
// connection (bypassing the object under test).
func boundOf(t *testing.T, adm *sql.DB, schema string) (int64, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var current int64
	err := adm.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT current_id FROM %s.unique_ids WHERE index_type = 'general'", schema)).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("independent bound read: %v", err)
	}
	return current, true
}

func TestIDReservationCurrentReserveStaleAndFreshPool(t *testing.T) {
	t.Parallel()
	db, adm, schema := idResFixture(t)
	ctx := nodeTestContext(t)
	r := newIDRes(t, db)

	cur, err := r.Current(ctx)
	if err != nil || cur != 0 {
		t.Fatalf("initial Current = (%d, %v), want (0, nil)", cur, err)
	}
	got, ok, err := r.Reserve(ctx, 0, 5)
	if err != nil || !ok {
		t.Fatalf("Reserve(0,5) = (%v, %v, %v), want success", got, ok, err)
	}
	if got.First != 1 || got.Last != 5 {
		t.Fatalf("Reserve(0,5) range = {%d %d}, want inclusive {1 5}", got.First, got.Last)
	}
	if cur, err = r.Current(ctx); err != nil || cur != 5 {
		t.Fatalf("Current after reserve = (%d, %v), want (5, nil)", cur, err)
	}
	got, ok, err = r.Reserve(ctx, 5, 1)
	if err != nil || !ok || got.First != 6 || got.Last != 6 {
		t.Fatalf("Reserve(5,1) = ({%d %d}, %v, %v), want {6 6}, true, nil", got.First, got.Last, ok, err)
	}
	if b, _ := boundOf(t, adm, schema); b != 6 {
		t.Fatalf("independent bound = %d, want 6", b)
	}

	// 同一旧期望再次竞争：0 行受影响，零号段/false/nil，不区分缺行；
	// 持久上界不变。
	// Competing again with the same stale bound: zero affected rows yields
	// the zero range, false, nil; a missing row is not distinguished and the
	// persisted bound stays unchanged.
	if got, ok, err := r.Reserve(ctx, 5, 1); got != (idrespg.IDRange{}) || ok || err != nil {
		t.Fatalf("stale Reserve(5,1) = (%v, %v, %v), want zero range, false, nil", got, ok, err)
	}
	if cur, err := r.Current(ctx); err != nil || cur != 6 {
		t.Fatalf("Current after stale reserve = (%d, %v), want (6, nil)", cur, err)
	}

	// 关闭旧池后用全新池/全新 Reservations 重开：上界已持久化，不得复用
	// 关闭池的内存状态或重放旧号段。
	// After closing the old pool, a brand-new pool and Reservations see the
	// persisted bound; closed-pool in-memory state is never reused and the
	// old range is never replayed.
	if err := db.Close(); err != nil {
		t.Fatalf("close first pool: %v", err)
	}
	fresh := registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema)
	r2 := newIDRes(t, fresh)
	if cur, err = r2.Current(ctx); err != nil || cur != 6 {
		t.Fatalf("fresh-pool Current = (%d, %v), want (6, nil)", cur, err)
	}
	if got, ok, err = r2.Reserve(ctx, 6, 2); err != nil || !ok || got.First != 7 || got.Last != 8 {
		t.Fatalf("fresh-pool Reserve(6,2) = ({%d %d}, %v, %v), want {7 8}, true, nil", got.First, got.Last, ok, err)
	}
	if b, _ := boundOf(t, adm, schema); b != 8 {
		t.Fatalf("independent bound after fresh pool = %d, want 8", b)
	}
}

func TestIDReservationGeneralKeyMissingRowBoundariesAndConstraints(t *testing.T) {
	t.Parallel()
	db, adm, schema := idResFixture(t)
	ctx := nodeTestContext(t)
	r := newIDRes(t, db)

	// 其他命名空间行并存：读取/预留只触碰固定 general 键。
	// Another namespace row coexists: reads and reservations touch only the
	// fixed general key.
	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	defer scancel()
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"INSERT INTO %s.unique_ids (index_type, current_id) VALUES ('files', 777)", schema)); err != nil {
		t.Fatalf("seed other namespace row: %v", err)
	}
	if cur, err := r.Current(ctx); err != nil || cur != 0 {
		t.Fatalf("Current with other namespace row = (%d, %v), want (0, nil)", cur, err)
	}
	if got, ok, err := r.Reserve(ctx, 0, 1); err != nil || !ok || got.Last != 1 {
		t.Fatalf("Reserve(0,1) = (%v, %v, %v), want success up to 1", got, ok, err)
	}
	var other int64
	if err := adm.QueryRowContext(sctx, fmt.Sprintf(
		"SELECT current_id FROM %s.unique_ids WHERE index_type = 'files'", schema)).Scan(&other); err != nil || other != 777 {
		t.Fatalf("other namespace row = (%d, %v), want untouched 777", other, err)
	}

	// 非负 CHECK 是显式本地政策：负持久上界被数据库拒绝；主键拒绝重复
	// general 行，无 upsert 面可依赖。
	// The nonnegative CHECK is explicit local policy: the database rejects a
	// negative persisted bound; the primary key rejects a duplicate general
	// row, so no upsert surface exists to rely on.
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"UPDATE %s.unique_ids SET current_id = -1 WHERE index_type = 'general'", schema)); err == nil {
		t.Fatal("negative current_id must fail the schema CHECK")
	}
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"INSERT INTO %s.unique_ids (index_type, current_id) VALUES ('general', 5)", schema)); err == nil {
		t.Fatal("duplicate general row must fail the primary key")
	}

	// 恰好到 MaxInt64：Last 精确等于上界，成功；越界步长在触库前被哨兵
	// 拒绝，持久上界不变。
	// Exactly reaching MaxInt64: Last equals the bound precisely and it
	// succeeds; an overflowing amount is rejected by the sentinel before
	// touching the DB and the persisted bound stays unchanged.
	seed := int64(math.MaxInt64 - 2)
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"UPDATE %s.unique_ids SET current_id = %d WHERE index_type = 'general'", schema, seed)); err != nil {
		t.Fatalf("seed near-max bound: %v", err)
	}
	if got, ok, err := r.Reserve(ctx, seed, 2); err != nil || !ok ||
		got.First != math.MaxInt64-1 || got.Last != math.MaxInt64 {
		t.Fatalf("Reserve(max-2,2) = ({%d %d}, %v, %v), want {MaxInt64-1 MaxInt64}, true, nil", got.First, got.Last, ok, err)
	}
	if cur, err := r.Current(ctx); err != nil || cur != math.MaxInt64 {
		t.Fatalf("Current at max = (%d, %v), want MaxInt64", cur, err)
	}
	if got, ok, err := r.Reserve(ctx, math.MaxInt64, 1); got != (idrespg.IDRange{}) || ok ||
		!errors.Is(err, idrespg.ErrInvalidIDRange) {
		t.Fatalf("Reserve(max,1) = (%v, %v, %v), want zero range, false, ErrInvalidIDRange", got, ok, err)
	}
	if b, _ := boundOf(t, adm, schema); b != math.MaxInt64 {
		t.Fatalf("bound after overflow reject = %d, want MaxInt64", b)
	}

	// 删除 general 行后：Current 缺行语义 + Reserve 不自动建行。
	// After deleting the general row: Current's missing-row semantics and
	// Reserve's refusal to auto-create the row.
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"DELETE FROM %s.unique_ids WHERE index_type = 'general'", schema)); err != nil {
		t.Fatalf("delete general row: %v", err)
	}
	cur, err := r.Current(ctx)
	if cur != 0 || err == nil {
		t.Fatalf("missing-row Current = (%d, %v), want (0, error)", cur, err)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("sql.ErrNoRows must stay reachable via Unwrap, got %v", err)
	}
	if err.Error() != "postgres id reservations: read current bound" {
		t.Fatalf("missing-row Current text = %q, want fixed stage text", err.Error())
	}
	if got, ok, err := r.Reserve(ctx, 0, 1); got != (idrespg.IDRange{}) || ok || err != nil {
		t.Fatalf("missing-row Reserve = (%v, %v, %v), want zero range, false, nil", got, ok, err)
	}
	// 不自动建行：general 行仍不存在。
	// No automatic row creation: the general row stays absent.
	if _, present := boundOf(t, adm, schema); present {
		t.Fatal("Reserve must not auto-create the general row")
	}
}

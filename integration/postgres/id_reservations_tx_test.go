// 号段预留的事务边界场景（BIN-135）：独立连接池同期望并发恰一胜者、有界
// 锁等待取消后新操作成功、延迟约束导致真实 Commit 失败不返回号段、调用方
// 节点事务回滚不影响独立已提交预留。后三类共用一个夹具以留在门禁时限内，
// 每条行为断言保持独立。
// Transaction-boundary scenarios for ID reservations: exactly one winner when
// independent pools compete on the same expected bound, bounded lock-wait
// cancellation followed by a fresh successful operation, a deferred constraint
// causing a real Commit failure that returns no range, and a caller node
// transaction rollback that leaves an independent committed reservation. The
// last three share one fixture to stay inside the gate timeout while every
// behavioral assertion stays separate.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	idrespg "github.com/0ldlight/bf/internal/repository/postgres"
)

func TestIDReservationConcurrentPoolsSingleWinner(t *testing.T) {
	t.Parallel()
	_, _, schema := idResFixture(t)
	const racers = 4

	type outcome struct {
		got  idrespg.IDRange
		ok   bool
		err  error
		name int
	}
	start := make(chan struct{})
	results := make(chan outcome, racers)
	var wg sync.WaitGroup
	// 每个竞争者独享全新连接池与 Reservations：唯一防线是数据库条件
	// UPDATE，不共享任何进程内状态。连接池全部在测试 goroutine 内创建
	// （registryPool 失败路径用 t.Fatalf，不得在其他 goroutine 调用）。
	// Every racer owns a brand-new pool and Reservations: the only arbiter is
	// the database conditional UPDATE, with zero shared in-process state. All
	// pools are created on the test goroutine (registryPool fails via
	// t.Fatalf and must never run on another goroutine).
	pools := make([]*sql.DB, racers)
	for i := range pools {
		pools[i] = registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema)
	}
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := idrespg.NewReservations(pools[i])
			if err != nil {
				results <- outcome{name: i, err: err}
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			<-start
			got, ok, err := r.Reserve(ctx, 0, 3)
			results <- outcome{got: got, ok: ok, err: err, name: i}
		}(i)
	}
	close(start)
	wctx, wcancel := context.WithTimeout(nodeTestContext(t), 40*time.Second)
	defer wcancel()
	wins, losses, faults := 0, 0, 0
	var winner idrespg.IDRange
	for i := 0; i < racers; i++ {
		select {
		case o := <-results:
			switch {
			case o.err != nil:
				faults++
				t.Errorf("racer %d failed: %v", o.name, o.err)
			case o.ok:
				wins++
				winner = o.got
			default:
				losses++
				// 每个落败者同样必须返回零号段，而非仅 false/无错。
				// Every loser must also return the ZERO range, not merely
				// false with no error.
				if o.got != (idrespg.IDRange{}) {
					t.Errorf("racer %d lost the race but returned a range {%d %d}", o.name, o.got.First, o.got.Last)
				}
			}
		case <-wctx.Done():
			t.Fatalf("timed out collecting racers: %d collected", i)
		}
	}
	wg.Wait()
	if faults != 0 || wins != 1 || losses != racers-1 {
		t.Fatalf("race outcome = %d wins / %d losses / %d faults, want exactly 1/%d/0", wins, losses, faults, racers-1)
	}
	if winner != (idrespg.IDRange{First: 1, Last: 3}) {
		t.Fatalf("winner range = {%d %d}, want {1 3}", winner.First, winner.Last)
	}
	fresh := newIDRes(t, registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema))
	if cur, err := fresh.Current(nodeTestContext(t)); err != nil || cur != 3 {
		t.Fatalf("Current after race = (%d, %v), want (3, nil)", cur, err)
	}
}

func TestIDReservationTransactionBoundaries(t *testing.T) {
	t.Parallel()
	// nodeFixture 提供 binaries+nodes 模式；在同一隔离 schema 上再应用号段
	// 预留模式，让锁持有者、延迟约束与调用方节点事务共库不同事务。
	// nodeFixture supplies the binaries+nodes schemas; applying the
	// reservation schema on the same isolated schema puts the lock holder,
	// the deferred constraint and the caller node transaction on one
	// database but distinct transactions.
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sctx, scancel := context.WithTimeout(ctx, 15*time.Second)
	defer scancel()
	if _, err := db.ExecContext(sctx, idrespg.ReservationSchemaSQL); err != nil {
		t.Fatalf("apply reservation schema: %v", err)
	}
	r := newIDRes(t, db)

	// 场景 1：自有的延迟约束触发器——UPDATE 返回受影响 1 行，COMMIT 才
	// 失败，证明候选号段只在 Commit 成功后返回。
	// Scenario 1: an owned deferred constraint trigger — the UPDATE reports
	// one affected row and only COMMIT fails, proving the candidate range is
	// returned strictly after a successful Commit.
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"CREATE FUNCTION %s.bfid_block() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'deferred test constraint'; END $$ LANGUAGE plpgsql", schema)); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"CREATE CONSTRAINT TRIGGER bfid_block AFTER UPDATE ON %s.unique_ids DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s.bfid_block()", schema, schema)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if got, ok, err := r.Reserve(ctx, 0, 5); got != (idrespg.IDRange{}) || ok || err == nil {
		t.Fatalf("Reserve under deferred trigger = (%v, %v, %v), want zero range, false, error", got, ok, err)
	} else if err.Error() != "postgres id reservations: commit reservation" {
		t.Fatalf("deferred-failure text = %q, want fixed commit stage text", err.Error())
	}
	if b, _ := boundOf(t, adm, schema); b != 0 {
		t.Fatalf("bound survived failed commit: %d, want 0", b)
	}
	if cur, cerr := r.Current(ctx); cerr != nil || cur != 0 {
		t.Fatalf("Current after failed commit = (%d, %v), want (0, nil)", cur, cerr)
	}
	// 移除自有注入后同一预留成功。
	// After removing the owned injection the same reservation succeeds.
	if _, err := adm.ExecContext(sctx, fmt.Sprintf(
		"DROP TRIGGER bfid_block ON %s.unique_ids", schema)); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if got, ok, err := r.Reserve(ctx, 0, 5); err != nil || !ok || got.First != 1 || got.Last != 5 {
		t.Fatalf("Reserve after drop = ({%d %d}, %v, %v), want {1 5}, true, nil", got.First, got.Last, ok, err)
	}

	// 场景 2：锁持有者在自有调用方事务里 FOR UPDATE 锁住 general 行（ctx
	// 管辖事务整个生命周期，兜底回滚先于 cancel 注册（LIFO），绝不从返回
	// Tx 的工厂 defer cancel）；受害者 Reserve 在有界时间内因取消失败。
	// Scenario 2: a lock holder takes the general row FOR UPDATE inside its
	// own caller-owned transaction (the ctx governs the whole tx lifetime,
	// the safety-net rollback is registered before the cancel (LIFO) and a
	// factory returning a Tx never defers its cancel); the victim Reserve
	// fails within a bounded time by cancellation.
	holder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback() })
	lctx, lcancel := context.WithTimeout(ctx, 10*time.Second)
	defer lcancel()
	if _, err := holder.ExecContext(lctx,
		"SELECT current_id FROM unique_ids WHERE index_type = 'general' FOR UPDATE"); err != nil {
		t.Fatalf("holder lock: %v", err)
	}
	vctx, vcancel := context.WithTimeout(ctx, 3*time.Second)
	defer vcancel()
	vgot, vok, verr := r.Reserve(vctx, 5, 4)
	if vgot != (idrespg.IDRange{}) || vok || verr == nil {
		t.Fatalf("locked Reserve = (%v, %v, %v), want zero range, false, error", vgot, vok, verr)
	}
	if !errors.Is(verr, context.DeadlineExceeded) {
		t.Fatalf("bounded cancellation cause expected via Unwrap, got %v", verr)
	}
	if verr.Error() != "postgres id reservations: reserve id range" &&
		verr.Error() != "postgres id reservations: commit reservation" {
		t.Fatalf("locked Reserve text = %q, want a fixed stage text", verr.Error())
	}
	if b, _ := boundOf(t, adm, schema); b != 5 {
		t.Fatalf("bound after cancelled reserve = %d, want 5", b)
	}
	// 释放锁后全新操作成功。
	// After releasing the lock a fresh operation succeeds.
	if err := holder.Rollback(); err != nil {
		t.Fatalf("release holder: %v", err)
	}
	if got, ok, err := r.Reserve(ctx, 5, 4); err != nil || !ok || got.First != 6 || got.Last != 9 {
		t.Fatalf("fresh Reserve after unlock = ({%d %d}, %v, %v), want {6 9}, true, nil", got.First, got.Last, ok, err)
	}

	// 场景 3（事务独立性）：先开启调用方节点事务并保持存活，再在其存活
	// 期间预留**新**号段，用返回的新 ID 写节点引用，然后回滚调用方事务
	// ——只撤销其自有事务：独立提交的预留上界保持推进，节点行未落库，
	// 既有 binaries 行保留。预留发生在调用方事务开启之后，证明两者互不
	// 嵌套。
	// Scenario 3 (transaction independence): open the caller node transaction
	// FIRST and keep it alive, then reserve a NEW range while it stays
	// active, write the node reference with that freshly returned ID and
	// roll the caller transaction back — it undoes only its own transaction:
	// the independently committed reservation bound stays advanced, the node
	// row is absent and the existing binaries row remains. The reservation
	// happens AFTER the caller transaction opened, proving the two never
	// nest.
	sha1 := strings.Repeat("d", 40)
	seedNodeBinary(t, adm, schema, sha1)
	tx, st := nodeTx(t, db)
	rgot, rok, rerr := r.Reserve(ctx, 9, 4)
	if rerr != nil || !rok || rgot.First != 10 || rgot.Last != 13 {
		t.Fatalf("Reserve during open caller tx = ({%d %d}, %v, %v), want {10 13}, true, nil", rgot.First, rgot.Last, rok, rerr)
	}
	nctx, ncancel := context.WithTimeout(ctx, 10*time.Second)
	defer ncancel()
	if n, err := st.Create(nctx, fileRefOf(t, rgot.First, "repoR", "", "kept.bin", nodeMeta(sha1))); err != nil || n != 1 {
		t.Fatalf("caller Create = (%d, %v), want provisional (1, nil)", n, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	if got := countNodes(t, adm, schema); got != 0 {
		t.Fatalf("nodes after rollback = %d, want 0", got)
	}
	if _, _, _, _, present := rowOf(t, adm, schema, sha1); !present {
		t.Fatal("existing binary row must survive the caller rollback")
	}
	if b, _ := boundOf(t, adm, schema); b != 13 {
		t.Fatalf("committed reservation bound = %d, want 13 (survives caller rollback)", b)
	}
	fresh := newIDRes(t, registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema))
	if cur, err := fresh.Current(nodeTestContext(t)); err != nil || cur != 13 {
		t.Fatalf("fresh-pool Current = (%d, %v), want (13, nil)", cur, err)
	}
}

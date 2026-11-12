// 懒 PG 上传保存缝的单元契约（先行）：fake driver 只覆盖构造器、查询失败、
// 回滚故障优先级、号段冲突不重试、正文阶段零节点连接与有界节点上下文；
// 真实成功与 deferred commit 失败归集成角色。
// Unit contract for the lazy PG upload save seam, written first: the fake
// driver covers only constructor, query faults, rollback-fault priority,
// reserve-conflict no-retry, zero node connections during body and the
// bounded node context; real success and deferred-commit faults belong to
// the integration role.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/localrepo"
	regpg "github.com/0ldlight/bf/internal/registry/postgres"
	"github.com/0ldlight/bf/internal/repository"
)

// 固定 fake 哨兵：文本安全，不含输入。
// Fixed fake sentinels with safe, input-free texts.
var (
	errUploadFakeNodeQuery = errors.New("postgres upload test: node lookup fault")
	errUploadFakeRollback  = errors.New("postgres upload test: rollback fault")
	errUploadFakeSecond    = errors.New("postgres upload test: second node lookup fault")
	errUploadFakeQuery     = errors.New("postgres upload test: unexpected query")
	errUploadFakeExec      = errors.New("postgres upload test: unexpected exec")
)

// uploadFakeHub 记录驱动层计数并按测试注入查询/执行/事务行为；并发安全。
// uploadFakeHub records driver-level counters and injects per-test
// query/exec/transaction behavior; safe for concurrent use.
type uploadFakeHub struct {
	mu                sync.Mutex
	connects          int
	begins            int
	rollbacks         int
	commitErr         error
	rollbackErr       error
	blockBeginOrdinal int
	query             func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error)
	exec              func(query string, args []driver.NamedValue) (int64, error)
	lastIDUpdateArgs  []driver.NamedValue
}

func (h *uploadFakeHub) beginCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.begins
}

func (h *uploadFakeHub) snapshot() (connects, begins, rollbacks int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.connects, h.begins, h.rollbacks
}

type uploadFakeConnector struct{ h *uploadFakeHub }

func (c *uploadFakeConnector) Connect(context.Context) (driver.Conn, error) {
	c.h.mu.Lock()
	c.h.connects++
	c.h.mu.Unlock()
	return &uploadFakeConn{h: c.h}, nil
}

func (c *uploadFakeConnector) Driver() driver.Driver { return uploadFakeDriver{h: c.h} }

type uploadFakeDriver struct{ h *uploadFakeHub }

func (uploadFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("postgres upload test: open via OpenDB only")
}

type uploadFakeConn struct{ h *uploadFakeHub }

func (c *uploadFakeConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *uploadFakeConn) Close() error                        { return nil }

// Begin 仅满足 driver.Conn 接口形状；database/sql 优先 ConnBeginTx。
// Begin only satisfies the driver.Conn interface shape; database/sql
// prefers ConnBeginTx.
func (c *uploadFakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("postgres upload test: begin via BeginTx only")
}

func (c *uploadFakeConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	c.h.mu.Lock()
	c.h.begins++
	block := c.h.blockBeginOrdinal == c.h.begins
	c.h.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &uploadFakeTx{h: c.h}, nil
}

func (c *uploadFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.h.mu.Lock()
	hook := c.h.query
	c.h.mu.Unlock()
	if hook == nil {
		return nil, errUploadFakeQuery
	}
	cols, rows, err := hook(query, args)
	if err != nil {
		return nil, err
	}
	return &uploadFakeRows{cols: cols, rows: rows}, nil
}

func (c *uploadFakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.h.mu.Lock()
	hook := c.h.exec
	c.h.mu.Unlock()
	if hook == nil {
		return nil, errUploadFakeExec
	}
	n, err := hook(query, args)
	if err != nil {
		return nil, err
	}
	return uploadFakeResult(n), nil
}

type uploadFakeTx struct{ h *uploadFakeHub }

func (t *uploadFakeTx) Commit() error { return t.h.commitErr }

func (t *uploadFakeTx) Rollback() error {
	t.h.mu.Lock()
	t.h.rollbacks++
	err := t.h.rollbackErr
	t.h.mu.Unlock()
	return err
}

type uploadFakeResult int64

func (r uploadFakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r uploadFakeResult) RowsAffected() (int64, error) { return int64(r), nil }

type uploadFakeRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *uploadFakeRows) Columns() []string { return r.cols }
func (r *uploadFakeRows) Close() error      { return nil }

func (r *uploadFakeRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

// uploadStubClock 计数采样并返回固定范围内毫秒值。
// uploadStubClock counts samples and returns a fixed in-range millis value.
type uploadStubClock struct {
	calls atomic.Int64
	value int64
}

func (c *uploadStubClock) NowMillis() int64 {
	c.calls.Add(1)
	return atomic.LoadInt64(&c.value)
}

func (c *uploadStubClock) callCount() int64 { return c.calls.Load() }

// uploadProofReader 在每次读取时记录驱动 Begin 计数与时钟采样数，证明
// 正文阶段零节点事务、时间戳在发布前恰好采样一次。
// uploadProofReader records the driver Begin count and clock sample count
// at every read, proving zero node transactions during the body phase and
// exactly one clock sample taken before publication.
type uploadProofReader struct {
	r              io.Reader
	beginCount     func() int
	clockCallCount func() int64
	reads          int
	maxBeginsRead  int
	clockAtFirst   int64
	firstDone      bool
}

func (p *uploadProofReader) Read(b []byte) (int, error) {
	if n := p.beginCount(); n > p.maxBeginsRead {
		p.maxBeginsRead = n
	}
	if !p.firstDone {
		p.clockAtFirst = p.clockCallCount()
		p.firstDone = true
	}
	p.reads++
	return p.r.Read(b)
}

var (
	uploadBinCols  = []string{"sha1", "md5", "bin_length", "sha256"}
	uploadNodeCols = []string{"node_id", "node_type", "repo", "node_path", "node_name",
		"bin_length", "sha1_actual", "md5_actual", "sha256", "created", "modified",
		"updated", "created_by", "modified_by", "sha1_original", "md5_original"}
	uploadIDCols = []string{"current_id"}
)

func uploadFakeDB(t *testing.T, h *uploadFakeHub, maxOpen int) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&uploadFakeConnector{h: h})
	db.SetMaxOpenConns(maxOpen)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func uploadRealPublisher(t *testing.T, db *sql.DB) *binary.Service {
	t.Helper()
	store, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	registry, err := regpg.New(db)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	service, err := binary.New(store, registry)
	if err != nil {
		t.Fatalf("binary.New: %v", err)
	}
	return service
}

func uploadTestInput(t *testing.T, declared int64) localrepo.UploadInput {
	t.Helper()
	path, err := localrepo.NewUploadPath("repo-x", "a/b.bin")
	if err != nil {
		t.Fatalf("NewUploadPath: %v", err)
	}
	return localrepo.UploadInput{
		Path:          path,
		OperatorUTF16: utf16.Encode([]rune("op-user")),
		DeclaredSize:  declared,
	}
}

func uploadTestSaver(t *testing.T, db *sql.DB, clock repository.FileSaveClock, timeout time.Duration) *UploadSaver {
	t.Helper()
	saver, err := NewUploadSaver(UploadDeps{
		DB: db, Publisher: uploadRealPublisher(t, db), Clock: clock, NodeTimeout: timeout,
	})
	if err != nil {
		t.Fatalf("NewUploadSaver: %v", err)
	}
	return saver
}

// uploadRegistryFreshHooks 配置“新登记+节点查询可注入”的最小成功发布钩子。
// uploadRegistryFreshHooks configures the minimal fresh-registration hooks
// with an injectable node-lookup outcome.
func uploadRegistryFreshHooks(h *uploadFakeHub, nodeRows [][]driver.Value, nodeErr error) {
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM nodes"):
			return uploadNodeCols, nodeRows, nodeErr
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	h.exec = func(query string, _ []driver.NamedValue) (int64, error) {
		if strings.Contains(query, "INSERT INTO binaries") {
			return 1, nil
		}
		return 0, errUploadFakeExec
	}
}

func TestNewUploadSaverRejectsInvalidDependencies(t *testing.T) {
	newHub := func() (*uploadFakeHub, *sql.DB) {
		h := &uploadFakeHub{}
		return h, uploadFakeDB(t, h, 2)
	}
	validTimeout := 10 * time.Second
	cases := []struct {
		name string
		mut  func(deps *UploadDeps)
		want string
	}{
		{"nil database", func(d *UploadDeps) { d.DB = nil }, "postgres upload: nil database dependency"},
		{"nil publisher", func(d *UploadDeps) { d.Publisher = nil }, "postgres upload: nil publisher dependency"},
		{"nil clock", func(d *UploadDeps) { d.Clock = nil }, "postgres upload: nil clock dependency"},
		{"zero timeout", func(d *UploadDeps) { d.NodeTimeout = 0 }, "postgres upload: unsupported node timeout"},
		{"negative timeout", func(d *UploadDeps) { d.NodeTimeout = -time.Second }, "postgres upload: unsupported node timeout"},
		{"timeout over one minute", func(d *UploadDeps) { d.NodeTimeout = time.Minute + time.Millisecond }, "postgres upload: unsupported node timeout"},
		{"unlimited pool", func(d *UploadDeps) {
			_, db := newHub()
			d.DB, d.Publisher, d.Clock = db, uploadRealPublisher(t, db), repository.SystemFileSaveClock{}
			d.DB.SetMaxOpenConns(0)
		}, "postgres upload: database max open connections must be at least two"},
		{"pool of one", func(d *UploadDeps) {
			_, db := newHub()
			d.DB, d.Publisher, d.Clock = db, uploadRealPublisher(t, db), repository.SystemFileSaveClock{}
			d.DB.SetMaxOpenConns(1)
		}, "postgres upload: database max open connections must be at least two"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, db := newHub()
			deps := UploadDeps{
				DB: db, Publisher: uploadRealPublisher(t, db),
				Clock: repository.SystemFileSaveClock{}, NodeTimeout: validTimeout,
			}
			tc.mut(&deps)
			saver, err := NewUploadSaver(deps)
			if err == nil || saver != nil {
				t.Fatalf("NewUploadSaver(%s) = (%v, %v), want (nil, error)", tc.name, saver, err)
			}
			if err.Error() != tc.want {
				t.Fatalf("NewUploadSaver(%s) error = %q, want %q", tc.name, err.Error(), tc.want)
			}
		})
	}
}

func TestNewUploadSaverAcceptsAndNeverTouchesDatabase(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	saver, err := NewUploadSaver(UploadDeps{
		DB: db, Publisher: uploadRealPublisher(t, db),
		Clock: repository.SystemFileSaveClock{}, NodeTimeout: time.Minute,
	})
	if err != nil || saver == nil {
		t.Fatalf("NewUploadSaver = (%v, %v), want (saver, nil)", saver, err)
	}
	connects, _, _ := h.snapshot()
	if connects != 0 {
		t.Fatalf("constructor opened %d connections, want 0 (no DDL/Ping)", connects)
	}
}

func TestSaveRejectsCallerFaultsWithoutDatabaseAccess(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	saver := uploadTestSaver(t, db, repository.SystemFileSaveClock{}, 5*time.Second)
	body := "caller fault probe\n"
	input := uploadTestInput(t, int64(len(body)))

	precancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := saver.Save(precancelled, strings.NewReader(body), input)
	if err == nil {
		t.Fatal("pre-cancelled context must fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled error = %v, want context.Canceled", err)
	}
	if errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatal("parent cancellation must not be classified as persistence fault")
	}

	badInput := input
	badInput.Path = repository.Path{}
	if _, err := saver.Save(context.Background(), strings.NewReader(body), badInput); !errors.Is(err, localrepo.ErrInvalidUploadInput) {
		t.Fatalf("invalid input error = %v, want ErrInvalidUploadInput", err)
	}

	if _, err := saver.Save(context.Background(), nil, input); !errors.Is(err, localrepo.ErrInvalidUploadInput) {
		t.Fatalf("nil body error = %v, want ErrInvalidUploadInput", err)
	}

	//lint:ignore SA1012 deliberately passing nil to verify the graceful refusal contract
	if _, err := saver.Save(nil, strings.NewReader(body), input); !errors.Is(err, localrepo.ErrInvalidUploadInput) {
		t.Fatalf("nil context error = %v, want ErrInvalidUploadInput", err)
	}

	connects, begins, _ := h.snapshot()
	if connects != 0 || begins != 0 {
		t.Fatalf("caller faults touched DB (connects=%d begins=%d), want 0/0", connects, begins)
	}
}

func TestSaveRejectsOutOfRangeClockStampBeforeReading(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	clock := &uploadStubClock{value: localrepo.MaxUploadMillis + 1}
	saver := uploadTestSaver(t, db, clock, 5*time.Second)
	body := "clock out of range\n"
	reader := &uploadProofReader{r: strings.NewReader(body), beginCount: h.beginCount, clockCallCount: clock.callCount}
	_, err := saver.Save(context.Background(), reader, uploadTestInput(t, int64(len(body))))
	if err == nil {
		t.Fatal("out-of-range clock sample must fail")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("clock range error = %v, want ErrUploadPersistence", err)
	}
	if reader.reads != 0 {
		t.Fatalf("body read %d times after clock rejection, want 0", reader.reads)
	}
	connects, begins, _ := h.snapshot()
	if connects != 0 || begins != 0 {
		t.Fatalf("clock rejection touched DB (connects=%d begins=%d), want 0/0", connects, begins)
	}
}

func TestSaveBodyPhaseHoldsNoNodeTransaction(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	uploadRegistryFreshHooks(h, nil, errUploadFakeNodeQuery)
	clock := &uploadStubClock{value: 1_700_000_000_000}
	saver := uploadTestSaver(t, db, clock, 5*time.Second)
	body := "body phase zero node tx\n"
	reader := &uploadProofReader{r: strings.NewReader(body), beginCount: h.beginCount, clockCallCount: clock.callCount}

	source, err := saver.Save(context.Background(), reader, uploadTestInput(t, int64(len(body))))
	if err == nil {
		t.Fatal("node lookup fault must fail the save")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("node fault error = %v, want ErrUploadPersistence", err)
	}
	if !errors.Is(err, errUploadFakeNodeQuery) {
		t.Fatalf("node fault cause lost: %v", err)
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
	if reader.reads == 0 {
		t.Fatal("body was never read; proof reader is broken")
	}
	if reader.maxBeginsRead != 0 {
		t.Fatalf("driver saw %d Begin calls during body reads, want 0", reader.maxBeginsRead)
	}
	if reader.clockAtFirst != 1 {
		t.Fatalf("clock samples before first read = %d, want exactly 1", reader.clockAtFirst)
	}
	if got := clock.callCount(); got != 1 {
		t.Fatalf("total clock samples = %d, want 1 (stamp only; core never reached)", got)
	}
	_, begins, rollbacks := h.snapshot()
	if begins != 2 {
		t.Fatalf("driver Begin calls = %d, want 2 (registry then node)", begins)
	}
	if rollbacks != 1 {
		t.Fatalf("node rollback attempts = %d, want 1", rollbacks)
	}
}

func TestSaveRollbackFaultOutranksClientMismatch(t *testing.T) {
	h := &uploadFakeHub{rollbackErr: errUploadFakeRollback}
	db := uploadFakeDB(t, h, 2)
	body := "rollback priority body\n"
	fresh, calcErr := content.Calculate(context.Background(), strings.NewReader(body))
	if calcErr != nil {
		t.Fatalf("content.Calculate: %v", calcErr)
	}
	altMD5 := strings.Repeat("e", 32)
	if altMD5 == fresh.MD5 {
		t.Fatal("test body md5 collides with the alternate stored value")
	}
	// 登记层返回既有行（MD5 不同）：预检按新鲜摘要通过，节点 CLIENT 比较
	// 必然失配，随后回滚故障必须把外部错误升级为持久化优先。
	// The registry returns an existing row with a different MD5: the stream
	// precheck passes against fresh digests, the node CLIENT comparison must
	// mismatch, and the rollback fault must then upgrade the outer error to
	// persistence priority.
	h.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, [][]driver.Value{{fresh.SHA1, altMD5, int64(len(body)), fresh.SHA256}}, nil
		case strings.Contains(query, "FROM nodes"):
			return uploadNodeCols, nil, nil
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	h.exec = func(query string, _ []driver.NamedValue) (int64, error) {
		if strings.Contains(query, "INSERT INTO binaries") {
			return 1, nil
		}
		return 0, errUploadFakeExec
	}
	saver := uploadTestSaver(t, db, repository.SystemFileSaveClock{}, 5*time.Second)
	input := uploadTestInput(t, int64(len(body)))
	input.Checksums = repository.ClientFileChecksums{
		SHA1Original: utf16.Encode([]rune(fresh.SHA1)),
		MD5Original:  utf16.Encode([]rune(fresh.MD5)),
	}

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err == nil {
		t.Fatal("client mismatch with rollback fault must fail")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("rollback fault must outrank client class: %v", err)
	}
	if !errors.Is(err, repository.ErrClientChecksumMismatch) {
		t.Fatalf("original client cause lost: %v", err)
	}
	if !errors.Is(err, errUploadFakeRollback) {
		t.Fatalf("rollback fault cause lost: %v", err)
	}
	if err.Error() != "postgres upload: rollback failed" {
		t.Fatalf("rollback fault Error() = %q, want fixed safe text", err.Error())
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
	if _, begins, rollbacks := h.snapshot(); begins != 2 || rollbacks != 1 {
		t.Fatalf("begins=%d rollbacks=%d, want 2/1", begins, rollbacks)
	}
}

func TestSaveReserveConflictFailsOnceWithoutRetry(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	idQueries, idUpdates := 0, 0
	uploadRegistryFreshHooks(h, nil, nil)
	baseQuery := h.query
	baseExec := h.exec
	h.query = func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		if strings.Contains(query, "FROM unique_ids") {
			idQueries++
			return uploadIDCols, [][]driver.Value{{int64(5)}}, nil
		}
		return baseQuery(query, args)
	}
	h.exec = func(query string, args []driver.NamedValue) (int64, error) {
		if strings.Contains(query, "UPDATE unique_ids") {
			idUpdates++
			h.mu.Lock()
			h.lastIDUpdateArgs = append([]driver.NamedValue(nil), args...)
			h.mu.Unlock()
			return 0, nil // 0 行受影响：期望过期 → 冲突，不重试。
		}
		return baseExec(query, args)
	}
	saver := uploadTestSaver(t, db, repository.SystemFileSaveClock{}, 5*time.Second)
	body := "reserve conflict body\n"

	source, err := saver.Save(context.Background(), strings.NewReader(body), uploadTestInput(t, int64(len(body))))
	if err == nil {
		t.Fatal("reserve conflict must fail the save")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("reserve conflict error = %v, want ErrUploadPersistence", err)
	}
	if idQueries != 1 || idUpdates != 1 {
		t.Fatalf("Current/Reserve calls = %d/%d, want exactly 1/1", idQueries, idUpdates)
	}
	h.mu.Lock()
	args := h.lastIDUpdateArgs
	h.mu.Unlock()
	if len(args) != 3 || args[0].Value != int64(6) || args[1].Value != "general" || args[2].Value != int64(5) {
		t.Fatalf("reserve args = %+v, want current_id=6 general expected=5", args)
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
	if _, _, rollbacks := h.snapshot(); rollbacks != 1 {
		t.Fatalf("node rollback attempts = %d, want 1", rollbacks)
	}
}

func TestSaveNodeGateQueueBoundedByNodeTimeout(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	uploadRegistryFreshHooks(h, nil, errUploadFakeNodeQuery)
	saver := uploadTestSaver(t, db, repository.SystemFileSaveClock{}, 150*time.Millisecond)
	// 白盒占位：gate 被占用时，节点阶段的排队等待必须由 NodeTimeout 界定，
	// 且在取得 gate 之前绝不 Begin 节点事务。
	// White-box hold: while the gate is taken, the node-phase queue wait must
	// be bounded by NodeTimeout and must never Begin the node transaction
	// before the gate is acquired.
	saver.gate <- struct{}{}
	defer func() { <-saver.gate }()
	body := "gate queue bound\n"
	start := time.Now()
	source, err := saver.Save(context.Background(), strings.NewReader(body), uploadTestInput(t, int64(len(body))))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("gate hold must fail the save")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("gate timeout error = %v, want ErrUploadPersistence", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gate timeout cause = %v, want context.DeadlineExceeded", err)
	}
	if elapsed < 140*time.Millisecond {
		t.Fatalf("gate wait returned after %v, want >= node timeout", elapsed)
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
	if _, begins, _ := h.snapshot(); begins != 1 {
		t.Fatalf("driver Begin calls = %d, want 1 (registry only; node never began)", begins)
	}
}

func TestSaveNodeBeginBoundedByNodeContext(t *testing.T) {
	h := &uploadFakeHub{blockBeginOrdinal: 2}
	db := uploadFakeDB(t, h, 2)
	uploadRegistryFreshHooks(h, nil, errUploadFakeNodeQuery)
	saver := uploadTestSaver(t, db, repository.SystemFileSaveClock{}, 150*time.Millisecond)
	body := "node begin bound\n"

	source, err := saver.Save(context.Background(), strings.NewReader(body), uploadTestInput(t, int64(len(body))))
	if err == nil {
		t.Fatal("blocked node begin must fail the save")
	}
	if !errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatalf("node begin timeout error = %v, want ErrUploadPersistence", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("node begin cause = %v, want context.DeadlineExceeded", err)
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed save returned nonzero source %+v", source)
	}
	if _, begins, _ := h.snapshot(); begins != 2 {
		t.Fatalf("driver Begin calls = %d, want 2 (registry plus blocked node begin)", begins)
	}
}

func TestSaveReleasesNodeGateAfterFailedSave(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	uploadRegistryFreshHooks(h, nil, errUploadFakeNodeQuery)
	saver := uploadTestSaver(t, db, repository.SystemFileSaveClock{}, 2*time.Second)
	body := "gate release probe\n"
	input := uploadTestInput(t, int64(len(body)))

	if _, err := saver.Save(context.Background(), strings.NewReader(body), input); !errors.Is(err, errUploadFakeNodeQuery) {
		t.Fatalf("first save error = %v, want node lookup fault", err)
	}
	uploadRegistryFreshHooks(h, nil, errUploadFakeSecond)
	if _, err := saver.Save(context.Background(), strings.NewReader(body), input); !errors.Is(err, errUploadFakeSecond) {
		t.Fatalf("second save error = %v, want the second node fault (gate must be free)", err)
	}
	if _, begins, rollbacks := h.snapshot(); begins != 4 || rollbacks != 2 {
		t.Fatalf("begins=%d rollbacks=%d, want 4/2", begins, rollbacks)
	}
}

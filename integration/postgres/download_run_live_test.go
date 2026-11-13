// 真实数据库上的 runDownload 深组合进程内集成测试（task-13）：直接调用
// process.Run（download 模式），以活库驱动 Ping 之后的真实依赖组合——
// 安全存储校验、目录行形状、监听绑定、就绪回调与优雅停机——以及真实
// 网络条件下的 Ping 失败与启动期取消。子进程级关停/信号语义已由
// download_process_test.go 覆盖；本文件补覆盖记入进程内 profile 的分支。
// In-process integration tests for runDownload's deep composition over a
// real database (task-13): process.Run (download mode) is invoked directly,
// driving the post-Ping real dependency composition with the live database
// — security-store validation, catalog row shape, listener binding, the
// readiness callback and graceful shutdown — plus Ping failure and startup
// cancellation under real network conditions. Subprocess shutdown/signal
// semantics are already covered by download_process_test.go; this file adds
// the branches that count toward the in-process coverage profile.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/process"
	"github.com/0ldlight/bf/internal/security"
)

// dlrBudget 固定本文件的时间界：单次 Run 级场景 20s、启动等待 15s。
// dlrBudget fixes this file's bounds: 20s per Run-level scenario and a 15s
// readiness wait.
const (
	dlrScenarioTimeout = 20 * time.Second
	dlrStartupTimeout  = 15 * time.Second
)

// dlrFixture 是启动一次 download 进程所需的最小真实事实：隔离 schema 上
// 的 security 模式与受支持目录行。节点/字节投影在启动期不被探测，请求级
// 语义归既有套件。
// dlrFixture is the minimal set of real facts needed to start one download
// process: the security schema and one supported catalog row on an isolated
// schema. The node/byte projection is not probed at startup; request-level
// semantics belong to the existing suites.
type dlrFixture struct {
	db     *sql.DB
	schema string
}

// dlrSetup 复用 newRegistry 的隔离 schema 与 search_path 绑定连接池，应用
// security 模式并插入唯一受支持目录行；schema 清理归 newRegistry。目录行
// 的非法形状（kind/package_type/revision）被 DB CHECK 约束拒绝，Go 侧
// supportedRepository 是防御性冗余——活库不可达，故不设无效行用例。
// dlrSetup reuses newRegistry's isolated schema and its search_path-bound
// pool, applies the security schema and inserts the single supported
// catalog row; schema cleanup belongs to newRegistry. Invalid catalog
// shapes (kind/package_type/revision) are rejected by DB CHECK
// constraints — the Go-side supportedRepository check is defense in depth
// and unreachable over a live database, so no invalid-row case exists.
func dlrSetup(t *testing.T) *dlrFixture {
	t.Helper()
	_, schema := newRegistry(t)
	db := registryPool(t, os.Getenv("BINFLOW_TEST_POSTGRES_DSN"), schema)
	f := &dlrFixture{db: db, schema: schema}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, security.SchemaSQL); err != nil {
		t.Fatal("dlr: apply security schema failed (driver details not logged)")
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO security_repositories(repo_key,kind,package_type,revision) VALUES('dlrR','local','generic',1)"); err != nil {
		t.Fatal("dlr: insert catalog row failed (driver details not logged)")
	}
	return f
}

// dlrAdminPool 返回管理池（无 search_path 限制）供直接行变更。
// dlrAdminPool returns the management pool (no search_path restriction) for
// direct row mutations.
func dlrAdminPool(t *testing.T) *sql.DB {
	t.Helper()
	return adminPool(t)
}

// dlrDSN 派生该 schema 的命令形态 DSN。
// dlrDSN derives the command-shaped DSN for this schema.
func dlrDSN(t *testing.T, f *dlrFixture) string {
	t.Helper()
	dsn, _ := dpCommandDSN(t, f.schema)
	return dsn
}

// dlrRunConfig 由命令形态 DSN 构造进程内 Run 配置。
// dlrRunConfig builds the in-process Run config from a command-shaped DSN.
func dlrRunConfig(dsn, root, listen string) process.Config {
	return process.Config{
		Mode: process.ModeDownload, ListenAddr: listen,
		PostgresDSN: dsn, FilestoreRoot: root, HTTP: dlcValidHTTP(),
	}
}

// dlrWaitReady 有界等待就绪地址；Run 先退或超时按固定文案失败。
// dlrWaitReady bounds the readiness wait; an early Run exit or timeout
// fails with fixed text.
func dlrWaitReady(t *testing.T, ready <-chan string, done <-chan error) string {
	t.Helper()
	timer := time.NewTimer(dlrStartupTimeout)
	defer timer.Stop()
	select {
	case addr := <-ready:
		return addr
	case err := <-done:
		t.Fatalf("dlr: Run exited before readiness: %v", err)
	case <-timer.C:
		t.Fatal("dlr: readiness wait timed out")
	}
	return ""
}

// dlrRunBlocking 同步执行一次预期快速失败的 Run，并断言从未就绪。
// dlrRunBlocking runs one Run call expected to fail quickly and asserts it
// never announced readiness.
func dlrRunBlocking(t *testing.T, cfg process.Config) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dlrScenarioTimeout)
	defer cancel()
	ready := false
	err := process.Run(ctx, cfg, func(string) { ready = true })
	if ready {
		t.Fatal("dlr: a startup failure announced readiness")
	}
	return err
}

func TestProcessRunDownloadLiveStartsAndStopsCleanly(t *testing.T) {
	f := dlrSetup(t)
	cfg := dlrRunConfig(dlrDSN(t, f), t.TempDir(), "127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- process.Run(ctx, cfg, func(addr string) { ready <- addr }) }()

	addr := dlrWaitReady(t, ready, done)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("dlc: ready address %q is not a loopback host:port", addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 {
		t.Fatalf("dlc: ready port %q invalid", port)
	}

	// 优雅停机：取消 ctx 后 Run 以 nil 返回且端口归还内核。
	// Graceful shutdown: after the ctx cancel Run returns nil and the
	// port returns to the kernel.
	cancel()
	timer := time.NewTimer(dlrScenarioTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dlr: clean shutdown returned %v, want nil", err)
		}
	case <-timer.C:
		t.Fatal("dlr: Run did not stop after ctx cancel")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("dlr: port %s not released after shutdown: %v", addr, err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("dlr: close reuse probe: %v", err)
	}
}

func TestProcessRunDownloadLivePingFailure(t *testing.T) {
	// 真实“连接拒绝”：先占用再释放一个回环端口，DSN 指向它。
	// A real connection refusal: occupy then release a loopback port and
	// point the DSN at it.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dlr: occupy port: %v", err)
	}
	deadPort := blocker.Addr().(*net.TCPAddr).Port
	if err := blocker.Close(); err != nil {
		t.Fatalf("dlr: release port: %v", err)
	}
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("dlr-user", "dlr-disposable-secret"),
		Host:     net.JoinHostPort("127.0.0.1", strconv.Itoa(deadPort)),
		Path:     "/binflow_registry_test",
		RawQuery: "sslmode=disable",
	}
	err = dlrRunBlocking(t, dlrRunConfig(u.String(), t.TempDir(), "127.0.0.1:0"))
	if !errors.Is(err, process.ErrStartup) {
		t.Fatalf("dlr: ping failure error = %v, want ErrStartup", err)
	}
}

func TestProcessRunDownloadLiveStartupCancellation(t *testing.T) {
	// 黑洞监听：接受连接但永不答复，Ping 挂起；期间取消父 ctx。
	// A black-hole listener: connections are accepted but never answered,
	// so Ping hangs; the parent ctx is cancelled meanwhile.
	hole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dlr: black-hole listen: %v", err)
	}
	defer hole.Close()
	held := make(chan struct{})
	go func() {
		conn, aerr := hole.Accept()
		if aerr == nil {
			<-held
			conn.Close()
		}
	}()
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("dlr-user", "dlr-disposable-secret"),
		Host:     hole.Addr().String(),
		Path:     "/binflow_registry_test",
		RawQuery: "sslmode=disable",
	}
	cfg := dlrRunConfig(u.String(), t.TempDir(), "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := false
	done := make(chan error, 1)
	go func() { done <- process.Run(ctx, cfg, func(string) { ready = true }) }()
	time.Sleep(1500 * time.Millisecond)
	cancel()
	timer := time.NewTimer(dlrScenarioTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, process.ErrStartup) {
			t.Fatalf("dlr: cancelled startup error = %v, want ErrStartup", err)
		}
		if ready {
			t.Fatal("dlr: cancelled startup announced readiness")
		}
	case <-timer.C:
		t.Fatal("dlr: cancelled startup did not return")
	}
	close(held)
}

// TestProcessRunDownloadLiveSecurityFactAbsence 覆盖活库上可达的两类安全
// 事实缺失：版本行被删（“恰好一行”被破坏）与目录行被删（构造器目录校验）。
// 非法值形态被 DB CHECK 拒绝（见 dlrSetup 注释），不属于可达状态。
// TestProcessRunDownloadLiveSecurityFactAbsence covers the two security-fact
// absences reachable over a live database: the version row deleted (the
// exactly-one-row invariant broken) and the catalog row deleted (the
// constructor's catalog validation). Invalid value shapes are rejected by
// DB CHECKs (see the dlrSetup comment) and are not reachable states.
func TestProcessRunDownloadLiveSecurityFactAbsence(t *testing.T) {
	cases := []struct {
		name   string
		delete string
	}{
		{"version row deleted", "DELETE FROM %s.security_schema_version"},
		{"catalog row deleted", "DELETE FROM %s.security_repositories WHERE repo_key = 'dlrR'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := dlrSetup(t)
			adm := dlrAdminPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := adm.ExecContext(ctx, fmt.Sprintf(tc.delete, f.schema)); err != nil {
				t.Fatal("dlr: delete security fact failed (driver details not logged)")
			}
			err := dlrRunBlocking(t, dlrRunConfig(dlrDSN(t, f), t.TempDir(), "127.0.0.1:0"))
			if !errors.Is(err, process.ErrStartup) {
				t.Fatalf("dlr: %s error = %v, want ErrStartup", tc.name, err)
			}
		})
	}
}

func TestProcessRunDownloadLivePortBusy(t *testing.T) {
	f := dlrSetup(t)
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dlr: occupy port: %v", err)
	}
	defer blocker.Close()
	err = dlrRunBlocking(t, dlrRunConfig(dlrDSN(t, f), t.TempDir(), blocker.Addr().String()))
	if !errors.Is(err, process.ErrStartup) {
		t.Fatalf("dlr: busy port error = %v, want ErrStartup", err)
	}
}

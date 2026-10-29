// 真实进程级下载命令集成测试（BIN-202）：从仓库根模块实际编译 ./cmd/binflow，
// 以受控 0600 JSON 配置（含 search_path 查询参数的真实 PostgreSQL DSN）启动
// 显式 download 模式，经真实 TCP 验证健康入口、401/403/404/400、撤权→恢复、
// 启动失败闭合与信号退出/端口复用。子进程环境先滤除全部 PG* 与环境 BINFLOW_*
// 再注入显式选择器；DSN/口令永不打印。真实 SQL 用户/组授权与真实 filestore
// SaveFile 提交先于命令启动；不伪造认证器、SQL 或网络协议。
// Real process-level integration tests for the download command (BIN-202):
// the actual ./cmd/binflow binary is compiled from the root module, started
// in explicit download mode from a controlled 0600 JSON config (carrying the
// real PostgreSQL DSN with a search_path query parameter), and exercised over
// real TCP for the health entry, 401/403/404/400, grant revoke→restore,
// fail-closed startup and signal exits with port reuse. The child
// environment filters every PG* and ambient BINFLOW_* variable before the
// explicit selectors are injected; the DSN and passwords are never printed.
// Real SQL users/grants and a real filestore SaveFile commit precede the
// command; no fake verifier, SQL or network protocol.
package postgresit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/binary"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
	"github.com/0ldlight/bf/internal/security"
)

// dp* 常量固定本文件的时间与文本契约：就绪日志前缀逐字来自 main 的
// "binflow: listening on <address>"（stderr）；所有等待有界，整场景 90s、
// 产品操作预算 10s、编译上限 90s，嵌套模块整包 5m 上限保持不变。
// dp* constants fix this file's time and text contracts: the ready-log prefix
// is verbatim main's "binflow: listening on <address>" on stderr; every wait
// is bounded, the scenario budget is 90s, the product operation budget is 10s
// and the build cap is 90s, while the nested module's 5m package cap stays
// unchanged.
const (
	dpReadyPrefix        = "binflow: listening on "
	dpChallenge          = `Basic realm="binflow"`
	dpRepoKey            = "saveR"
	dpPayload            = "\x00dp-download-payload\xff"
	dpBuildTimeout       = 90 * time.Second
	dpReadyTimeout       = 20 * time.Second
	dpExitTimeout        = 20 * time.Second
	dpSignalExitTimeout  = 15 * time.Second
	dpRequestTimeout     = 10 * time.Second
	dpConfigFileMode     = 0o600
	dpConfigBadFileMode  = 0o644
	dpResourceModified41 = 41
)

// dpScenarioContext 为完整多阶段 TCP/SQL 进程场景提供 90s 生命周期上限
// （非单次操作预算）；产品内部操作仍为固定 10s。
// dpScenarioContext bounds a whole multi-stage TCP/SQL process scenario at 90s
// (not a per-operation budget); product-internal operations stay at 10s.
func dpScenarioContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// dpConfig 是受控下载配置的全部九个精确 lower_snake_case 字段；仅用于生成
// 交给真实命令的 JSON 文件，字段名与顺序由实现计划冻结。
// dpConfig carries exactly the nine required lower_snake_case download-config
// fields; it only generates the JSON handed to the real command, with field
// names and count frozen by the implementation plan.
type dpConfig struct {
	ListenAddr           string `json:"listen_addr"`
	PostgresDSN          string `json:"postgres_dsn"`
	FilestoreRoot        string `json:"filestore_root"`
	RepoKey              string `json:"repo_key"`
	Challenge            string `json:"challenge"`
	FailureThreshold     int    `json:"failure_threshold"`
	FailureWindowSeconds int    `json:"failure_window_seconds"`
	BlockSeconds         int    `json:"block_seconds"`
	MaxTrackedUsers      int    `json:"max_tracked_users"`
}

// dpBaseConfig 返回合法基线配置：字面回环监听 127.0.0.1:0、真实 DSN（含
// search_path 查询参数）、绝对 filestore root 与受支持 HTTP 画像。
// dpBaseConfig returns the valid baseline: literal loopback listener
// 127.0.0.1:0, the real DSN (with its search_path query parameter), an
// absolute filestore root and a supported HTTP profile.
func dpBaseConfig(dsn, filestoreRoot string) dpConfig {
	return dpConfig{
		ListenAddr:           "127.0.0.1:0",
		PostgresDSN:          dsn,
		FilestoreRoot:        filestoreRoot,
		RepoKey:              dpRepoKey,
		Challenge:            dpChallenge,
		FailureThreshold:     3,
		FailureWindowSeconds: 60,
		BlockSeconds:         60,
		MaxTrackedUsers:      64,
	}
}

// dpWriteConfig 将九字段配置写入受控 JSON 文件并以显式 mode 落权（默认路径
// 为 0600，0644 仅用于拒绝场景）；返回绝对路径。内容不打印。
// dpWriteConfig writes the nine-field config to a controlled JSON file with an
// explicit mode (0600 for the normal path, 0644 only for the rejection
// scenario) and returns its absolute path. The content is never printed.
func dpWriteConfig(t *testing.T, cfg dpConfig, mode os.FileMode) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("dp: marshal config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "download-config.json")
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("dp: write config: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("dp: chmod config: %v", err)
	}
	return path
}

// dpCommandDSN 由测试 DSN 派生命令用的严格 URL 形态 DSN：唯一主机归一为字面
// 回环 IP（localhost 视作 127.0.0.1，这是本套件对 CI/本地夹具的记录在案的
// 本地选择，非回环主机以固定文案拒绝）、显式 user/password/database、恰好一
// 个 sslmode=disable 与一个 search_path 查询键。返回的 DSN 与口令只进配置
// 文件/泄漏断言，永不进日志。
// dpCommandDSN derives the strict URL-shaped DSN for the command from the test
// DSN: a single host normalized to a literal loopback IP ("localhost" is
// treated as 127.0.0.1, a documented local choice for this suite's CI/local
// fixtures; non-loopback hosts fail with fixed text), an explicit
// user/password/database, exactly one sslmode=disable and one search_path
// query key. The returned DSN and password only reach the config file and the
// leak assertions, never the logs.
func dpCommandDSN(t *testing.T, schema string) (dsn, password string) {
	t.Helper()
	cfg, err := parseTestConfig(os.Getenv("BINFLOW_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatalf("dp: %v", err)
	}
	host := cfg.Host
	if host == "localhost" {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("dp: the download DSN profile requires a literal loopback database host (got a non-loopback host; value not logged)")
	}
	if cfg.User == "" || cfg.Password == "" || cfg.Database == "" {
		t.Fatalf("dp: the test DSN must carry an explicit user, password and database (values not logged)")
	}
	port := cfg.Port
	if port == 0 {
		port = 5432
	}
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(host, strconv.Itoa(int(port))),
		Path:     "/" + cfg.Database,
		RawQuery: url.Values{"sslmode": {"disable"}, "search_path": {schema}}.Encode(),
	}
	return u.String(), cfg.Password
}

// dpChildEnv 先滤除子进程环境里全部 PG* 与环境 BINFLOW_* 键（含测试 DSN），
// 再按字典序注入显式选择器；永不输出任何键值。
// dpChildEnv filters every PG* and ambient BINFLOW_* key (including the test
// DSN) out of the child environment, then injects the explicit selectors in
// sorted order; no key or value is ever printed.
func dpChildEnv(selectors map[string]string) []string {
	env := make([]string, 0, 16)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "PG") || strings.HasPrefix(name, "BINFLOW_") {
			continue
		}
		env = append(env, kv)
	}
	keys := make([]string, 0, len(selectors))
	for k := range selectors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+selectors[k])
	}
	return env
}

// dpDownloadEnv 返回 download 模式的显式选择器：仅 BINFLOW_MODE 与
// BINFLOW_DOWNLOAD_CONFIG；BINFLOW_LISTEN_ADDR 必须缺席（下载模式唯一的
// 监听来源是配置文件）。
// dpDownloadEnv returns the explicit download-mode selectors: only BINFLOW_MODE
// and BINFLOW_DOWNLOAD_CONFIG; BINFLOW_LISTEN_ADDR must stay absent (the
// config file is the sole listener source in download mode).
func dpDownloadEnv(configPath string) []string {
	return dpChildEnv(map[string]string{
		"BINFLOW_MODE":            "download",
		"BINFLOW_DOWNLOAD_CONFIG": configPath,
	})
}

// dpOutput 收集子进程一路输出并在完整行上匹配就绪日志前缀；把解析出的绑定
// 地址送入 ready 通道（缓冲 1，只送一次）。内容只在锁内追加，永不整段打印。
// dpOutput collects the child's whole output and matches the ready-log prefix
// on complete lines, delivering the bound address to a buffered (size 1, once
// only) ready channel. Content is appended under a lock and never printed.
type dpOutput struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	pending []byte
	ready   chan string
	parse   bool
}

var dpReadyLine = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} binflow: listening on (.+)$`)

func newDpOutput(parseReady bool) *dpOutput {
	return &dpOutput{ready: make(chan string, 1), parse: parseReady}
}

// Write 由 exec 的单一拷贝 goroutine 调用；完成行才做前缀匹配。
// Write is called by exec's single copy goroutine; only complete lines match
// the prefix.
func (o *dpOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
	if !o.parse {
		return len(p), nil
	}
	o.pending = append(o.pending, p...)
	for {
		i := bytes.IndexByte(o.pending, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSuffix(string(o.pending[:i]), "\r")
		o.pending = o.pending[i+1:]
		if match := dpReadyLine.FindStringSubmatch(line); match != nil {
			select {
			case o.ready <- match[1]:
			default:
			}
		}
	}
	return len(p), nil
}

// text 返回至今收集到的全部输出；仅在进程退出后由主 goroutine 读取。
// text returns everything collected so far; read only from the main
// goroutine after the process has exited.
func (o *dpOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// dpChild 是一个本测试拥有并负责回收的真实命令进程；done 只发送一次，
// exited 在 Wait 返回后置位供清理路径安全判断。
// dpChild is one real command process owned and reaped by this test; done is
// sent exactly once and exited flips after Wait so the cleanup path can
// decide safely.
type dpChild struct {
	proc    *exec.Cmd
	stdout  *dpOutput
	stderr  *dpOutput
	ctx     context.Context
	done    chan error
	exited  atomic.Bool
	secrets []string
}

// dpGuard 登记绝不允许出现在子进程输出里的私有值（DSN、口令等）；值只用于
// Contains 断言，永不回显。
// dpGuard registers private values (DSN, passwords, ...) that must never
// appear in the child output; they feed Contains assertions only and are
// never echoed.
func dpGuard(c *dpChild, secrets ...string) {
	c.secrets = append(c.secrets, secrets...)
}

// dpRepoRoot 由本文件位置推导仓库根模块目录；不依赖工作目录。
// dpRepoRoot derives the repository root (root module) from this file's
// location, independent of the working directory.
func dpRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("dp: cannot locate this test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// dpBuild caches one actual command build per top-level test. Each build has
// its own 90s budget before fixture acquisition; t.TempDir owns its binary
// until that test's children have been cleaned up.
var dpBuild sync.Map // map[*testing.T]string

func dpCommand(t *testing.T) string {
	t.Helper()
	if path, ok := dpBuild.Load(t); ok {
		return path.(string)
	}
	bin := filepath.Join(t.TempDir(), "binflow")
	ctx, cancel := context.WithTimeout(context.Background(), dpBuildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-race", "-o", bin, "./cmd/binflow")
	cmd.Dir = dpRepoRoot(t)
	// Build diagnostics are deliberately not echoed: compiler/tool environment
	// is outside the private-output contract of the command under test.
	if _, err := cmd.CombinedOutput(); err != nil {
		t.Fatal("dp: build actual command failed (diagnostics not logged)")
	}
	dpBuild.Store(t, bin)
	t.Cleanup(func() { dpBuild.Delete(t) })
	return bin
}

// dpSpawn 启动一个真实命令子进程：stdin 关闭、stdout/stderr 分别收集、
// Wait 只在独立 goroutine 执行一次；清理只回收本进程（先 Kill 再等 Wait）。
// dpSpawn starts one real command child: stdin closed, stdout/stderr collected
// separately, Wait executed exactly once on its own goroutine; the cleanup
// reaps only this process (Kill then drain Wait).
func dpSpawn(t *testing.T, ctx context.Context, env []string) *dpChild {
	t.Helper()
	c := &dpChild{
		ctx:    ctx,
		proc:   exec.CommandContext(ctx, dpCommand(t)),
		stdout: newDpOutput(false),
		stderr: newDpOutput(true),
		done:   make(chan error, 1),
	}
	c.proc.Dir = dpRepoRoot(t)
	c.proc.Env = env
	c.proc.WaitDelay = dpRequestTimeout
	c.proc.Stdout = c.stdout
	c.proc.Stderr = c.stderr
	if err := c.proc.Start(); err != nil {
		t.Fatalf("dp: start command: %v", err)
	}
	go func() {
		err := c.proc.Wait()
		c.exited.Store(true)
		c.done <- err
	}()
	t.Cleanup(func() {
		if !c.exited.Load() {
			_ = c.proc.Process.Kill()
			timer := time.NewTimer(dpExitTimeout)
			defer timer.Stop()
			select {
			case <-c.done:
			case <-timer.C:
				t.Error("dp: owned child did not reap after kill within cleanup budget")
			}
		}
		dpAssertPrivate(t, c)
	})
	return c
}

// dpWaitReady 阻塞等待 stderr 上的就绪日志行（不做睡眠轮询），校验地址是
// 绑定后的字面回环 host:port（端口非 0）并返回它；进程先退或超时以固定文案
// 失败，输出内容不进日志。
// dpWaitReady blocks on the ready log line from stderr (no sleep polling),
// validates it is a bound literal loopback host:port (nonzero port) and
// returns it; an early exit or timeout fails with fixed text and the output
// never enters the log.
func dpWaitReady(t *testing.T, c *dpChild) string {
	t.Helper()
	timer := time.NewTimer(dpReadyTimeout)
	defer timer.Stop()
	select {
	case addr := <-c.stderr.ready:
		host, port, err := net.SplitHostPort(addr)
		if err != nil || host != "127.0.0.1" {
			t.Fatalf("dp: ready log address is not an IPv4 loopback host:port")
		}
		if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
			t.Fatalf("dp: ready log address has no valid bound port")
		}
		return addr
	case <-c.ctx.Done():
		t.Fatal("dp: scenario deadline reached waiting for ready")
	case <-c.done:
		t.Fatalf("dp: command exited before the ready log (output not logged)")
	case <-timer.C:
		t.Fatalf("dp: no ready log line with prefix %q within %s (output not logged)", dpReadyPrefix, dpReadyTimeout)
	}
	return ""
}

// dpWaitExit 有界等待进程退出并返回退出码（信号终止为负值）。
// dpWaitExit waits for the process to exit within the bound and returns its
// exit code (negative when killed by a signal).
func dpWaitExit(t *testing.T, c *dpChild, bound time.Duration) int {
	t.Helper()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case err := <-c.done:
		if c.ctx.Err() != nil {
			t.Fatal("dp: scenario ended before observed command exit")
		}
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("dp: command wait failed: %v", err)
	case <-c.ctx.Done():
		t.Fatal("dp: scenario deadline reached waiting for exit")
	case <-timer.C:
		t.Fatalf("dp: command did not exit within %s (output not logged)", bound)
	}
	return -1
}

// dpSignal 向子进程发送一个信号；只作用于本测试拥有的进程。
// dpSignal delivers one signal to the child; it only touches the process this
// test owns.
func dpSignal(t *testing.T, c *dpChild, sig syscall.Signal) {
	t.Helper()
	if err := c.proc.Process.Signal(sig); err != nil {
		t.Fatalf("dp: deliver signal: %v", err)
	}
}

// dpAssertPrivate 断言子进程 stdout/stderr 既不含任何登记的私有值，也不含
// DSN 形态字符串；失败信息只描述谓词，永不回显输出。
// dpAssertPrivate asserts the child stdout/stderr contain neither any
// registered private value nor a DSN-shaped string; failure messages name the
// predicate only and never echo the output.
func dpAssertPrivate(t *testing.T, c *dpChild) {
	t.Helper()
	for _, out := range []string{c.stdout.text(), c.stderr.text()} {
		for _, secret := range c.secrets {
			if secret != "" && strings.Contains(out, secret) {
				t.Fatalf("dp: process output leaked a registered private configuration value (value not logged)")
			}
		}
		if strings.Contains(out, "postgres://") || strings.Contains(out, "postgresql://") {
			t.Fatalf("dp: process output contains a DSN-shaped string (content not logged)")
		}
	}
}

// dpClient 返回不走代理、单请求 10s 产品预算的真实 HTTP 客户端。
// dpClient returns a real proxy-free HTTP client with the 10s per-request
// product budget.
func dpClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{
		Timeout:   dpRequestTimeout,
		Transport: transport,
	}
}

// dpRequest 经真实 TCP 执行一次请求并读尽正文；空用户名表示不带
// Authorization 头。传输失败直接致命（固定文案，含方法与路径）。
// dpRequest performs one request over real TCP and drains the body; an empty
// username sends no Authorization header. Transport failures are fatal with
// fixed text carrying only the method and path.
func dpRequest(t *testing.T, ctx context.Context, client *http.Client, method, target, username, password string) (int, http.Header, string, int64) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		t.Fatalf("dp: build %s %s request: %v", method, target, err)
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("dp: %s %s: %v", method, target, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatalf("dp: %s %s: read body: %v", method, target, readErr)
	}
	return resp.StatusCode, resp.Header, string(body), resp.ContentLength
}

// dpFixture 汇总命令前必须就绪的真实事实：隔离 schema 上的 registry/nodes/
// 号段/security 模式、目录与授权行、真实 SaveFile 提交后的对象字节、命令
// DSN 及其私有值（永不打印）。
// dpFixture bundles the real facts that must exist before the command: the
// registry/nodes/reservation/security schemas on an isolated schema, catalog
// and grant rows, object bytes after a real SaveFile commit, plus the command
// DSN and its private value (never printed).
type dpFixture struct {
	f          splFixture
	svc        *binary.Service
	schema     string
	dsn        string
	dsnSecret  string
	aliceUser  string
	aliceRaw   string
	carolUser  string
	carolRaw   string
	aliceID    int64
	configPath string
}

// dpExec 在隔离 schema 上执行一条独立 SQL 变更。
// dpExec applies one independent SQL mutation on the isolated schema.
func dpExec(t *testing.T, f splFixture, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, dpRequestTimeout)
	defer cancel()
	if _, err := f.db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal("dp: execute fixture SQL failed (driver details not logged)")
	}
}

// dpUser 经产品 GenerateCredential 以受控合成口令插入真实启用用户并返回
// 数据库身份；口令长度满足种子约束。
// dpUser inserts one real enabled user through the product GenerateCredential
// with a controlled synthetic raw secret (within the seeding length floor) and
// returns the database identity.
func dpUser(t *testing.T, f splFixture, username, raw string) int64 {
	t.Helper()
	encoded, err := security.GenerateCredential(raw)
	if err != nil {
		t.Fatalf("dp: GenerateCredential(%q): %v", username, err)
	}
	var id int64
	ctx, cancel := context.WithTimeout(f.ctx, dpRequestTimeout)
	defer cancel()
	if err := f.db.QueryRowContext(ctx,
		"INSERT INTO security_users(username, encoded_secret, enabled, effective_admin) VALUES($1,$2,true,false) RETURNING user_id",
		username, encoded).Scan(&id); err != nil {
		t.Fatal("dp: insert fixture user failed (driver details not logged)")
	}
	return id
}

// dpRepoRow 插入唯一受支持的 local/generic 仓库事实行。
// dpRepoRow inserts the single supported local/generic repository fact row.
func dpRepoRow(t *testing.T, f splFixture, key string) {
	t.Helper()
	dpExec(t, f, "INSERT INTO security_repositories(repo_key,kind,package_type,revision) VALUES($1,'local','generic',1)", key)
}

// dpUserGrant 插入仓库全域 '**' 直接读授权。
// dpUserGrant inserts the repository-wide '**' direct read grant.
func dpUserGrant(t *testing.T, f splFixture, key string, userID int64) {
	t.Helper()
	dpExec(t, f, "INSERT INTO security_read_grants(repo_key,user_id,scope) VALUES($1,$2,'**')", key, userID)
}

// dpSetup 建立完整真实夹具并完成命令前的提交：security schema、saveR 目录
// 行、alice（直接授权）与 carol（无授权）、真实流 SaveFile 提交对象字节，
// 最后派生命令 DSN。调用方以 fx.f.ctx（90s）继续 SQL/进程场景。
// dpSetup builds the complete real fixture and finishes the pre-command
// commits: the security schema, the saveR catalog row, alice (direct grant)
// and carol (no grant), a real stream SaveFile commit of the object bytes,
// then derives the command DSN. Callers continue SQL/process scenarios on
// fx.f.ctx (90s).
func dpSetup(t *testing.T) *dpFixture {
	t.Helper()
	dpCommand(t) // separate build budget, before live fixture/transaction
	f, svc := sslSetup(t)
	// Replace sslSetup's 30s transaction with one governed by this scenario.
	if err := f.tx.Rollback(); err != nil {
		t.Fatal("dp: replace fixture transaction failed")
	}
	f.ctx = dpScenarioContext(t)
	tx, err := f.db.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal("dp: begin scenario transaction failed")
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	st, err := refpg.New(tx)
	if err != nil {
		t.Fatal("dp: construct scenario store failed")
	}
	f.tx = tx
	f.store = &splStore{st: st}
	f.alloc.st = st
	dpExec(t, f, security.SchemaSQL)
	dpRepoRow(t, f, dpRepoKey)
	fx := &dpFixture{
		f:         f,
		svc:       svc,
		schema:    f.schema,
		aliceUser: "dp-alice",
		aliceRaw:  "Controlled-dp-alice-16!",
		carolUser: "dp-carol",
		carolRaw:  "Controlled-dp-carol-16",
	}
	fx.aliceID = dpUser(t, f, fx.aliceUser, fx.aliceRaw)
	dpUserGrant(t, f, dpRepoKey, fx.aliceID)
	dpUser(t, f, fx.carolUser, fx.carolRaw)
	dghSave(t, f, svc, &splClock{value: dpResourceModified41}, "dir/a.bin", dpPayload)
	if err := f.tx.Commit(); err != nil {
		t.Fatal("dp: commit SaveFile transaction failed (driver details not logged)")
	}
	fx.dsn, fx.dsnSecret = dpCommandDSN(t, f.schema)
	return fx
}

// dpWriteFixtureConfig 写出夹具的 0600 九字段配置并缓存路径。
// dpWriteFixtureConfig writes the fixture's 0600 nine-field config and caches
// the path.
func dpWriteFixtureConfig(t *testing.T, fx *dpFixture, cfg dpConfig) string {
	t.Helper()
	fx.configPath = dpWriteConfig(t, cfg, dpConfigFileMode)
	return fx.configPath
}

// dpAssertHealth 覆盖 download 进程上的既有健康入口契约：无凭据 GET 200
// 固定正文、HEAD 同表示零正文、显式 Content-Length 与 text/plain 类型。
// dpAssertHealth covers the existing health entry on the download process:
// unauthenticated GET 200 with the fixed body, HEAD with the same
// representation and zero body, explicit Content-Length and text/plain type.
func dpAssertHealth(t *testing.T, ctx context.Context, client *http.Client, base string) {
	t.Helper()
	status, header, body, length := dpRequest(t, ctx, client, http.MethodGet, base+"/healthz", "", "")
	if status != http.StatusOK || body != "ok\n" || length != 3 {
		t.Fatalf("dp: GET /healthz = %d %q len=%d, want 200 ok len=3", status, body, length)
	}
	if got := header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("dp: GET /healthz Content-Type = %q", got)
	}
	status, header, body, length = dpRequest(t, ctx, client, http.MethodHead, base+"/healthz", "", "")
	if status != http.StatusOK || body != "" || length != 3 {
		t.Fatalf("dp: HEAD /healthz = %d %q len=%d, want 200 empty len=3", status, body, length)
	}
	if got := header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("dp: HEAD /healthz Content-Type = %q", got)
	}
}

// dpPortReusable 在进程干净退出后实际绑定同地址一次，证明端口已归还内核；
// 绑定失败即致命。
// dpPortReusable actually binds the same address once after a clean exit,
// proving the kernel released the port; a bind failure is fatal.
func dpPortReusable(t *testing.T, addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("dp: address %s not reusable after clean exit: %v", addr, err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("dp: close reuse probe: %v", err)
	}
}

// TestProcessDownloadCommand 是三个真实命令分区测试之一：实际编译并启动
// download 模式后，经真实 TCP 验证健康入口无凭据可用、无/错凭据 401+challenge、
// 真实凭据 GET 完整字节与表示头、HEAD 零正文、无授权 403、未知仓库 404、
// 原始转义路径 400、直接撤权 403→恢复 200，SIGTERM 干净退出且全程输出不泄
// 漏任何私有配置。
// TestProcessDownloadCommand is the first of the three real-command
// partitions: after actually building and starting download mode, real TCP
// requests verify the unauthenticated health entry, missing/wrong credentials
// 401+challenge, full-byte GET with representation headers and bodyless HEAD
// under real credentials, no-grant 403, unknown repository 404, the raw
// escaped path 400, and direct revoke 403 → restore 200; SIGTERM exits
// cleanly and the whole run's output never leaks private configuration.
func TestProcessDownloadCommand(t *testing.T) {
	fx := dpSetup(t)
	ctx := fx.f.ctx
	cfg := dpBaseConfig(fx.dsn, fx.f.root)
	path := dpWriteFixtureConfig(t, fx, cfg)
	c := dpSpawn(t, ctx, dpDownloadEnv(path))
	dpGuard(c, fx.dsn, fx.dsnSecret, fx.f.root, fx.aliceRaw, fx.carolRaw, "Controlled-dp-wrong-1", path)
	addr := dpWaitReady(t, c)
	base := "http://" + addr
	client := dpClient(t)
	artifactURL := base + "/artifactory/" + dpRepoKey + "/dir/a.bin"

	// 健康入口在 download 进程上仍无凭据开放（不被下载认证替换）。
	// The health entry stays open without credentials on the download
	// process (download authentication never replaces it).
	dpAssertHealth(t, ctx, client, base)

	// 缺凭据与错口令：同体 401+challenge（一次错口令在阈值3内，不触发阻断）。
	// Missing credentials and a wrong password: identical 401+challenge
	// bodies (one wrong attempt stays below the threshold of 3).
	status, header, body, length := dpRequest(t, ctx, client, http.MethodGet, artifactURL, "", "")
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
	dseChallengeOf(t, header)
	status, header, body, length = dpRequest(t, ctx, client, http.MethodGet, artifactURL, fx.aliceUser, "Controlled-dp-wrong-1")
	dghError(t, status, header, body, length, http.StatusUnauthorized, "unauthorized\n", false)
	dseChallengeOf(t, header)

	// 真实凭据主线：GET 完整字节与表示头，HEAD 同表示零正文。
	// The real-credential main line: GET returns full bytes and the
	// representation headers; HEAD keeps the representation with no body.
	sha1Hex, md5Hex, sha256Hex := dghDigest(dpPayload)
	status, header, body, length = dpRequest(t, ctx, client, http.MethodGet, artifactURL, fx.aliceUser, fx.aliceRaw)
	if status != http.StatusOK || body != dpPayload || length != int64(len(dpPayload)) {
		t.Fatalf("dp: alice GET = %d len=%d/%d", status, length, len(dpPayload))
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", dpResourceModified41)
	dghNoChallenge(t, header)
	status, header, body, length = dpRequest(t, ctx, client, http.MethodHead, artifactURL, fx.aliceUser, fx.aliceRaw)
	if status != http.StatusOK || body != "" || length != int64(len(dpPayload)) {
		t.Fatalf("dp: alice HEAD = %d %q len=%d", status, body, length)
	}
	dghRepresentation(t, header, "application/octet-stream", sha1Hex, md5Hex, sha256Hex, "a.bin", dpResourceModified41)

	// 有效身份无授权：403 无 challenge。
	// A valid identity without a grant: 403 without a challenge.
	status, header, body, length = dpRequest(t, ctx, client, http.MethodGet, artifactURL, fx.carolUser, fx.carolRaw)
	dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", false)
	dghNoChallenge(t, header)

	// 未知仓库键：目录独立缺席 404，无 challenge。
	// An unknown repository key: an independent catalog absence 404 without
	// a challenge.
	status, header, body, length = dpRequest(t, ctx, client, http.MethodGet, base+"/artifactory/otherR/dir/a.bin", fx.aliceUser, fx.aliceRaw)
	dghError(t, status, header, body, length, http.StatusNotFound, "not found\n", false)
	dghNoChallenge(t, header)

	// 原始转义路径：URL.Path 解码相同但 raw RequestURI 带百分号编码，必须
	// 在本地有限排除内 400（不得经 clean/unescape 后路由成下载）。
	// The raw escaped path: URL.Path decodes identically, but the raw
	// RequestURI carries a percent escape and must land in the local finite
	// exclusion as 400 (never cleaned/unescape-routed into a download).
	status, header, body, length = dpRequest(t, ctx, client, http.MethodGet, base+"/artifactory/"+dpRepoKey+"/dir/%61.bin", fx.aliceUser, fx.aliceRaw)
	dghError(t, status, header, body, length, http.StatusBadRequest, "bad request\n", false)
	dghNoChallenge(t, header)

	// 直接撤权→恢复：撤销后下一次请求即 403；恢复授权行后无需重启即 200。
	// Direct revoke → restore: the first request after revoking the grant
	// row is 403; restoring the row allows 200 again with no restart.
	dpExec(t, fx.f, "DELETE FROM security_read_grants WHERE repo_key=$1 AND user_id=$2", dpRepoKey, fx.aliceID)
	status, header, body, length = dpRequest(t, ctx, client, http.MethodGet, artifactURL, fx.aliceUser, fx.aliceRaw)
	dghError(t, status, header, body, length, http.StatusForbidden, "forbidden\n", false)
	dghNoChallenge(t, header)
	dpUserGrant(t, fx.f, dpRepoKey, fx.aliceID)
	status, _, body, length = dpRequest(t, ctx, client, http.MethodGet, artifactURL, fx.aliceUser, fx.aliceRaw)
	if status != http.StatusOK || body != dpPayload || length != int64(len(dpPayload)) {
		t.Fatalf("dp: GET after grant restore = %d len=%d/%d", status, length, len(dpPayload))
	}

	// 干净关停：SIGTERM 后有限时间内以 0 退出；stdout 必须保持为空（就绪
	// 日志走 stderr），且运行期输出不含任何私有配置（负向泄漏断言）。
	// Clean shutdown: after SIGTERM the process exits 0 within the bound;
	// stdout must stay empty (the ready log goes to stderr) and the run's
	// output never contains private configuration (the negative leak
	// assertion).
	dpSignal(t, c, syscall.SIGTERM)
	if code := dpWaitExit(t, c, dpSignalExitTimeout); code != 0 {
		t.Fatalf("dp: SIGTERM exit code = %d, want 0 (output not logged)", code)
	}
	if got := c.stdout.text(); got != "" {
		t.Fatalf("dp: command wrote %d bytes to stdout (content not logged)", len(got))
	}
	dpAssertPrivate(t, c)
}

// TestProcessDownloadStartupFailures 是三个真实命令分区测试之二：闭合启动
// 失败矩阵——非私有(0644)配置文件、畸形 DSN 的泄漏负向、download 模式下
// BINFLOW_LISTEN_ADDR 竞争、任一 PG* 环境键、filestore root 非目录、目录缺
// 仓库行、缺 security schema 与端口被占，全部必须非零退出且输出不泄漏私有
// 配置；不存在静默回退健康模式的就绪。
// TestProcessDownloadStartupFailures is the second real-command partition:
// the fail-closed startup matrix — a non-private (0644) config file, the
// malformed-DSN leak negative, a competing BINFLOW_LISTEN_ADDR in download
// mode, any present PG* environment key, a non-directory filestore root, a
// missing catalog repository row, a missing security schema and an occupied
// port all must exit nonzero with no private configuration leaked; there is
// no silent fallback into a mistakenly ready health mode.
func TestProcessDownloadStartupFailures(t *testing.T) {
	fx := dpSetup(t)
	ctx := fx.f.ctx
	base := func() dpConfig { return dpBaseConfig(fx.dsn, fx.f.root) }

	// dpExpect 启动一个预期失败的命令实例：非零退出 + 私有输出负向断言。
	// dpExpect starts one command instance expected to fail: nonzero exit
	// plus the negative private-output assertion.
	dpExpect := func(name string, cfg dpConfig, mode os.FileMode, extraEnv map[string]string, extraSecrets ...string) {
		t.Helper()
		path := dpWriteConfig(t, cfg, mode)
		env := dpDownloadEnv(path)
		if len(extraEnv) > 0 {
			merged := make(map[string]string, len(extraEnv))
			for k, v := range extraEnv {
				merged[k] = v
			}
			merged["BINFLOW_MODE"] = "download"
			merged["BINFLOW_DOWNLOAD_CONFIG"] = path
			env = dpChildEnv(merged)
		}
		c := dpSpawn(t, ctx, env)
		dpGuard(c, fx.dsn, fx.dsnSecret, fx.f.root, fx.aliceRaw, fx.carolRaw, "Controlled-dp-wrong-1", path)
		dpGuard(c, extraSecrets...)
		dpGuard(c, cfg.PostgresDSN, cfg.FilestoreRoot)
		code := dpWaitExit(t, c, dpExitTimeout)
		if code == 0 {
			t.Fatalf("dp: %s: command exited 0 on a startup failure (output not logged)", name)
		}
		select {
		case <-c.stderr.ready:
			t.Fatalf("dp: %s: startup failure announced readiness", name)
		default:
		}
		dpAssertPrivate(t, c)
	}

	// 非私有配置文件（0644）：读取即拒绝。
	// A non-private config file (0644): rejected at read time.
	dpExpect("config-file-not-private", base(), dpConfigBadFileMode, nil)

	// 畸形 DSN（端口溢出）：解析失败先于任何连接；专用合成口令绝不允许出现
	// 在输出里（泄漏负向）。
	// A malformed DSN (port overflow): parsing fails before any connection;
	// the dedicated synthetic password must never appear in the output (the
	// leak negative).
	malformed := base()
	malformed.PostgresDSN = "postgres://dp-user:dp-secret-malformed@127.0.0.1:70000/dpno?sslmode=disable"
	dpExpect("malformed-dsn", malformed, dpConfigFileMode, nil, "dp-secret-malformed")

	// download 模式下出现 BINFLOW_LISTEN_ADDR：拒绝歧义监听来源。
	// A present BINFLOW_LISTEN_ADDR in download mode: the ambiguous
	// competing listener source is rejected.
	dpExpect("competing-listen-addr", base(), dpConfigFileMode, map[string]string{"BINFLOW_LISTEN_ADDR": "127.0.0.1:0"})

	// 任一 PG* 环境键（此用 PGSERVICE）：download 模式显式隔离拒绝。
	// Any present PG* environment key (PGSERVICE here): explicit isolation
	// rejects it in download mode.
	dpExpect("pg-env-present", base(), dpConfigFileMode, map[string]string{"PGSERVICE": "dp-must-be-rejected"})

	// filestore root 指向常规文件：启动拒绝。
	// A filestore root pointing at a regular file: startup rejects it.
	badRoot := base()
	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("dp: create bad root: %v", err)
	}
	badRoot.FilestoreRoot = rootFile
	dpExpect("filestore-root-not-directory", badRoot, dpConfigFileMode, nil)

	// 目录缺仓库行：组合根校验失败（随后恢复目录行）。
	// A catalog missing the repository row: composition-root validation
	// fails (the catalog row is restored afterwards).
	dpExec(t, fx.f, "DELETE FROM security_repositories WHERE repo_key=$1", dpRepoKey)
	dpExpect("catalog-repo-missing", base(), dpConfigFileMode, nil)
	dpRepoRow(t, fx.f, dpRepoKey)

	// 占用真实端口，验证绑定失败非零退出；资源关闭次序由内部单元测试证明。
	// An occupied real port proves nonzero bind failure. Internal lifecycle
	// tests separately establish resource-close ordering.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dp: occupy port: %v", err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	busy := base()
	busy.ListenAddr = blocker.Addr().String()
	dpExpect("bind-address-busy", busy, dpConfigFileMode, nil)
	if err := blocker.Close(); err != nil {
		t.Fatalf("dp: release occupied port: %v", err)
	}

	// 缺 security schema（全新隔离 schema 只带 registry/nodes）：安全事实
	// 校验失败，绝不静默降级成无认证服务。
	// A missing security schema (a fresh isolated schema with only
	// registry/nodes): the security-facts validation fails and never
	// silently degrades into an unauthenticated service.
	_, bareSchema := nodeFixture(t)
	bareDsn, bareSecret := dpCommandDSN(t, bareSchema)
	bare := base()
	bare.PostgresDSN = bareDsn
	bare.FilestoreRoot = t.TempDir()
	dpExpect("security-schema-missing", bare, dpConfigFileMode, nil, bareDsn, bareSecret)
}

// TestProcessDownloadCommandSignals 是三个真实命令分区测试之三：空闲
// SIGTERM 与空闲 SIGINT 均以 0 退出且端口随后可再绑定；另做一次「先小 GET
// 再空闲信号」的干净关停检查——它只证明优雅路径可用，不作为在途排空次序
// 证明（该确定性证明属于内部生命周期缝的单元测试）。
// TestProcessDownloadCommandSignals is the third real-command partition: idle
// SIGTERM and idle SIGINT both exit 0 with the port bindable again
// afterwards; one additional clean shutdown runs a tiny GET before the idle
// signal — it only proves the graceful path works and is NOT claimed as an
// in-flight drain proof (that deterministic proof belongs to the internal
// lifecycle seam's unit tests).
func TestProcessDownloadCommandSignals(t *testing.T) {
	fx := dpSetup(t)
	ctx := fx.f.ctx
	cfg := dpBaseConfig(fx.dsn, fx.f.root)
	path := dpWriteFixtureConfig(t, fx, cfg)
	env := dpDownloadEnv(path)

	// spawnClean 启动至就绪、可选预热 GET、发信号、断言 0 退出、私有输出
	// 负向断言与端口复用。
	// spawnClean starts to readiness, optionally warms up with one GET,
	// signals, asserts exit 0, the negative private-output assertion and
	// port reuse.
	spawnClean := func(sig syscall.Signal, warmGet bool) {
		t.Helper()
		c := dpSpawn(t, ctx, env)
		dpGuard(c, fx.dsn, fx.dsnSecret, fx.f.root, fx.aliceRaw, fx.carolRaw, "Controlled-dp-wrong-1", path)
		addr := dpWaitReady(t, c)
		if warmGet {
			// 预热 GET 只验证优雅关停路径本身；不声称证明了在途排空次序。
			// The warm-up GET only exercises the graceful shutdown path
			// itself; it claims nothing about in-flight drain ordering.
			client := dpClient(t)
			status, _, body, length := dpRequest(t, ctx, client, http.MethodGet, "http://"+addr+"/artifactory/"+dpRepoKey+"/dir/a.bin", fx.aliceUser, fx.aliceRaw)
			if status != http.StatusOK || body != dpPayload || length != int64(len(dpPayload)) {
				t.Fatalf("dp: warm-up GET = %d len=%d/%d", status, length, len(dpPayload))
			}
		}
		dpSignal(t, c, sig)
		if code := dpWaitExit(t, c, dpSignalExitTimeout); code != 0 {
			t.Fatalf("dp: exit code after signal = %d, want 0 (output not logged)", code)
		}
		dpAssertPrivate(t, c)
		dpPortReusable(t, addr)
	}

	// 空闲 SIGTERM：无任何请求，干净退出且端口归还。
	// Idle SIGTERM: no requests at all, a clean exit and the port returned.
	spawnClean(syscall.SIGTERM, false)

	// 小 GET 后空闲 SIGTERM：优雅路径可用性检查（非排空证明）。
	// SIGTERM after a tiny GET: a graceful-path sanity check (not a drain
	// proof).
	spawnClean(syscall.SIGTERM, true)

	// 空闲 SIGINT：同样以 0 退出且端口可复用。
	// Idle SIGINT: also exits 0 with a reusable port.
	spawnClean(syscall.SIGINT, false)
}

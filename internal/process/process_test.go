// 本文件是 BIN-202 UNIT 工作者的独立测试：按冻结计划覆盖 LoadConfig 受控
// JSON/私有文件边界/越界值/脱敏、私有 parsePostgresDSN 严格 URL 画像与
// PG 环境隔离、newRouter 原始 RequestURI 分发，以及 serve 生命周期（优雅
// 排空、强制超时 reap、panic 释放、封闭后准入、意外 Serve 错误与取消启动）。
// 所有等待均基于事件通道与终结条件；睡眠只用于带上限的轮询存在性证明。
//
// This file holds the BIN-202 UNIT worker's independent tests per the frozen
// plan: LoadConfig controlled JSON / private-file bounds / value bounds /
// redaction, private parsePostgresDSN strict URL profile with PG-environment
// isolation, newRouter raw RequestURI dispatch, and the serve lifecycle
// (graceful drain, forced-timeout reaping, panic release, post-seal
// admission, unexpected Serve failure and canceled startup). All waits are
// channel/terminal-condition based; sleeps appear only in bounded
// presence-polling helpers, never as absence proofs.
package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/0ldlight/bf/internal/security"
)

// ppCanaryDSNPassword / ppCanaryChallenge 是仅存在于测试输入中的金丝雀值；
// 任何错误文本出现它们都意味着私有数据泄漏。
// ppCanaryDSNPassword / ppCanaryChallenge are canary values that exist only in
// test inputs; their presence in any error text means private-data leakage.
const (
	ppCanaryDSNPassword = "canary-dsn-password-9f1e"
	ppCanaryChallenge   = `canary challenge realm "quoted"`
	ppValidDSN          = "postgres://binflow:" + ppCanaryDSNPassword + "@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow"
)

// ppEnvAll 返回固定键值的环境回调（未列出的键为空字符串）。
// ppEnvAll returns a getenv callback backed by a fixed map (missing keys "").
func ppEnvAll(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// ppDownloadEnv 构造 download 模式环境，可附加其他键。
// ppDownloadEnv builds a download-mode environment with optional extra keys.
func ppDownloadEnv(path string, extra map[string]string) func(string) string {
	kv := map[string]string{
		"BINFLOW_MODE":            ModeDownload,
		"BINFLOW_DOWNLOAD_CONFIG": path,
	}
	for k, v := range extra {
		kv[k] = v
	}
	return ppEnvAll(kv)
}

// ppWritePerm 在私有临时目录写入指定权限的配置文件并注册清理。
// ppWritePerm writes a config file with an exact permission mode under a
// private temp dir and registers cleanup.
func ppWritePerm(t *testing.T, content string, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "download.json")
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod config %o: %v", perm, err)
	}
	t.Cleanup(func() { os.Remove(path) })
	return path
}

// ppDownloadJSON 生成完整 9 键受控 JSON；mutate 可改写或删除键。
// ppDownloadJSON renders the full 9-key controlled JSON; mutate may replace
// or delete keys.
func ppDownloadJSON(t *testing.T, fsRoot string, mutate func(map[string]any)) string {
	t.Helper()
	m := map[string]any{
		"listen_addr":            "127.0.0.1:18443",
		"postgres_dsn":           ppValidDSN,
		"filestore_root":         fsRoot,
		"repo_key":               "local-repo",
		"challenge":              ppCanaryChallenge,
		"failure_threshold":      5,
		"failure_window_seconds": 300,
		"block_seconds":          300,
		"max_tracked_users":      256,
	}
	if mutate != nil {
		mutate(m)
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		t.Fatalf("marshal download json: %v", err)
	}
	return string(b) + "\n"
}

// ppAbsRoot 返回一个存在的绝对目录作为 filestore_root 候选。
// ppAbsRoot returns an existing absolute directory as filestore_root.
func ppAbsRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// ppManualConfig 构造完整有效的手工 download Config（Run 校验路径用）。
// ppManualConfig builds a complete valid manual download Config for the Run
// validation paths.
func ppManualConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Mode:          ModeDownload,
		ListenAddr:    "127.0.0.1:18443",
		PostgresDSN:   ppValidDSN,
		FilestoreRoot: ppAbsRoot(t),
		HTTP: security.HTTPConfig{
			RepoKey:          "local-repo",
			Challenge:        "BinFlow download",
			FailureThreshold: 5,
			FailureWindow:    300 * time.Second,
			BlockDuration:    300 * time.Second,
			MaxTrackedUsers:  256,
		},
	}
}

// ppLoadDownload 走完整受控路径：写 0600 文件并 LoadConfig。
// ppLoadDownload exercises the full controlled path: 0600 file + LoadConfig.
func ppLoadDownload(t *testing.T, content string) (Config, error) {
	t.Helper()
	path := ppWritePerm(t, content, 0o600)
	return LoadConfig(ppDownloadEnv(path, nil))
}

// ppIsFixedSentinel 只接受固定公共哨兵、安全上下文错误及它们的 Join 树。
// ppIsFixedSentinel accepts only fixed public sentinels, safe context errors,
// and join trees containing those exact errors.
func ppIsFixedSentinel(err error) bool {
	if err == nil {
		return false
	}
	for _, want := range []error{ErrConfiguration, ErrStartup, ErrServe, ErrDrainTimeout, ErrResources, context.Canceled, context.DeadlineExceeded} {
		if err == want {
			return true
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !ppIsFixedSentinel(child) {
				return false
			}
		}
		return true
	}
	return false
}

// ppAssertNoCanary 断言文本未泄漏金丝雀秘密。
// ppAssertNoCanary asserts the text leaks no canary secret.
func ppAssertNoCanary(t *testing.T, text, what string) {
	t.Helper()
	for _, secret := range []string{ppCanaryDSNPassword, ppCanaryChallenge} {
		if strings.Contains(text, secret) {
			t.Fatalf("%s leaks secret material: %q", what, text)
		}
	}
}

// ppLog 是带索引的事件日志，用于跨 goroutine 的确定性顺序证明。
// ppLog is an indexed event log for deterministic cross-goroutine ordering
// proofs.
type ppLog struct {
	mu     sync.Mutex
	events []string
}

func (l *ppLog) add(e string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
	return len(l.events) - 1
}

func (l *ppLog) has(e string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, got := range l.events {
		if got == e {
			return true
		}
	}
	return false
}

func (l *ppLog) index(e string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, got := range l.events {
		if got == e {
			return i
		}
	}
	return -1
}

// ppRecvEvent 在有界时间内等待信号事件；超时即失败。
// ppRecvEvent waits for a signal event within a bounded window; timeout
// fails the test.
func ppRecvEvent(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// ppAwaitRefused 轮询直到拨号被拒（存在性证明：监听已关），有上限。
// ppAwaitRefused polls until dialing is refused (a presence proof that the
// listener closed), with a hard bound.
func ppAwaitRefused(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return // listener closed: dial refused.
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("listener %s still accepting after shutdown", addr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ppDialHTTP 拨号并写一条完整 HTTP/1.1 请求，返回 conn 与读取器。
// ppDialHTTP dials and writes one complete HTTP/1.1 request.
func ppDialHTTP(t *testing.T, addr, method, target string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		conn.Close()
		t.Fatalf("set connection deadline: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: keep-alive\r\n\r\n", method, target)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

// ppCloseConn 关闭连接并忽略已关闭错误。
// ppCloseConn closes a conn, ignoring already-closed errors.
func ppCloseConn(c net.Conn) {
	if c != nil {
		c.Close()
	}
}

// ppSpyHandler 记录方法与原始 RequestURI 的下载侧间谍处理器。
// ppSpyHandler is the download-side spy recording method and raw RequestURI.
type ppSpyHandler struct {
	mu    sync.Mutex
	calls []string // "METHOD RequestURI"
}

func (s *ppSpyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls = append(s.calls, r.Method+" "+r.RequestURI)
	s.mu.Unlock()
	w.Header().Set("X-Binflow-Spy", "download")
	w.WriteHeader(http.StatusNoContent)
}

func (s *ppSpyHandler) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// ppRawRequest 构造带精确原始 RequestURI 的请求（绕过 httptest 解析）。
// ppRawRequest builds a request carrying an exact raw RequestURI, bypassing
// httptest's target parsing.
func ppRawRequest(t *testing.T, method, rawTarget string) *http.Request {
	t.Helper()
	u, err := url.Parse(rawTarget)
	if err != nil {
		t.Fatalf("parse raw target %q: %v", rawTarget, err)
	}
	return &http.Request{
		Method:     method,
		URL:        u,
		RequestURI: rawTarget,
		Host:       "127.0.0.1",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		RemoteAddr: "127.0.0.1:9999",
	}
}

// --- LoadConfig：模式与环境 / LoadConfig: modes and environment ---

// 空环境保持既有健康默认（health + 127.0.0.1:8080）。
// An empty environment keeps the existing health defaults.
func TestLoadConfigHealthDefaults(t *testing.T) {
	cfg, err := LoadConfig(ppEnvAll(nil))
	if err != nil {
		t.Fatalf("default load: %v", err)
	}
	if cfg.Mode != ModeHealth {
		t.Fatalf("mode=%q want %q", cfg.Mode, ModeHealth)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("listen=%q want default 127.0.0.1:8080", cfg.ListenAddr)
	}
}

// 健康模式监听地址行为不变：任意地址仍被接受。
// Health-mode listen behavior is unchanged: arbitrary addresses stay valid.
func TestLoadConfigHealthListenOverrideUnchanged(t *testing.T) {
	cfg, err := LoadConfig(ppEnvAll(map[string]string{
		"BINFLOW_MODE":        ModeHealth,
		"BINFLOW_LISTEN_ADDR": "0.0.0.0:9000",
	}))
	if err != nil {
		t.Fatalf("health load: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Fatalf("listen=%q want 0.0.0.0:9000", cfg.ListenAddr)
	}
}

// 未知模式必须以 ErrConfiguration 关闭。
// An unknown mode must fail closed with ErrConfiguration.
func TestLoadConfigUnknownModeRejected(t *testing.T) {
	for _, mode := range []string{"downlaod", "yaml", "HEALTH", "health "} {
		_, err := LoadConfig(ppEnvAll(map[string]string{"BINFLOW_MODE": mode}))
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("mode %q err=%v want ErrConfiguration", mode, err)
		}
	}
}

// 健康模式下非空 BINFLOW_DOWNLOAD_CONFIG 必须拒绝（防半启用误就绪）。
// Nonempty BINFLOW_DOWNLOAD_CONFIG in health mode must be rejected to block
// half-enabled mistaken readiness.
func TestLoadConfigHealthModeRejectsDownloadConfigEnv(t *testing.T) {
	_, err := LoadConfig(ppEnvAll(map[string]string{
		"BINFLOW_MODE":            ModeHealth,
		"BINFLOW_DOWNLOAD_CONFIG": "/absolute/nonempty.json",
	}))
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("err=%v want ErrConfiguration", err)
	}
}

// download 模式必须提供绝对配置路径，且不得携带竞争监听地址。
// Download mode requires an absolute config path and rejects a competing
// listen address.
func TestLoadConfigDownloadPathAndListenEnv(t *testing.T) {
	root := ppAbsRoot(t)
	good := ppWritePerm(t, ppDownloadJSON(t, root, nil), 0o600)

	cases := []struct {
		name string
		env  func(string) string
	}{
		{"empty path", ppEnvAll(map[string]string{"BINFLOW_MODE": ModeDownload})},
		{"relative path", ppDownloadEnv("configs/download.json", nil)},
		{"competing listen addr", ppDownloadEnv(good, map[string]string{"BINFLOW_LISTEN_ADDR": "127.0.0.1:9999"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(tc.env)
			if !errors.Is(err, ErrConfiguration) {
				t.Fatalf("err=%v want ErrConfiguration", err)
			}
		})
	}

	// download + 空 BINFLOW_LISTEN_ADDR 合法（地址只来自 JSON）。
	// download with an empty BINFLOW_LISTEN_ADDR is valid (address comes from
	// JSON only).
	if _, err := LoadConfig(ppDownloadEnv(good, map[string]string{"BINFLOW_LISTEN_ADDR": ""})); err != nil {
		t.Fatalf("empty listen env should not compete: %v", err)
	}
}

// nil 环境回调必须被拒绝。
// A nil getenv callback must be rejected.
func TestLoadConfigNilEnvCallbackRejected(t *testing.T) {
	if _, err := LoadConfig(nil); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("err=%v want ErrConfiguration", err)
	}
}

// --- LoadConfig：成功映射与文件权限 / success mapping and file permissions ---

// 0600 文件的 9 键完整映射到 Config 字段（含秒→时长换算）。
// A 0600 file with the full 9 keys maps onto Config, including seconds to
// durations.
func TestLoadConfigDownloadSuccessMapping(t *testing.T) {
	root := ppAbsRoot(t)
	path := ppWritePerm(t, ppDownloadJSON(t, root, func(m map[string]any) {
		m["listen_addr"] = "127.0.0.1:19001"
		m["failure_window_seconds"] = 60
		m["block_seconds"] = 120
	}), 0o600)

	cfg, err := LoadConfig(ppDownloadEnv(path, nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Mode != ModeDownload {
		t.Fatalf("mode=%q", cfg.Mode)
	}
	if cfg.ListenAddr != "127.0.0.1:19001" {
		t.Fatalf("listen=%q", cfg.ListenAddr)
	}
	if cfg.PostgresDSN != ppValidDSN {
		t.Fatalf("dsn mismatch")
	}
	if cfg.FilestoreRoot != root {
		t.Fatalf("filestore root=%q want %q", cfg.FilestoreRoot, root)
	}
	if cfg.HTTP.RepoKey != "local-repo" || cfg.HTTP.Challenge != ppCanaryChallenge {
		t.Fatal("http strings mismatch")
	}
	if cfg.HTTP.FailureThreshold != 5 {
		t.Fatalf("threshold=%d", cfg.HTTP.FailureThreshold)
	}
	if cfg.HTTP.FailureWindow != 60*time.Second || cfg.HTTP.BlockDuration != 120*time.Second {
		t.Fatalf("durations window=%v block=%v", cfg.HTTP.FailureWindow, cfg.HTTP.BlockDuration)
	}
	if cfg.HTTP.MaxTrackedUsers != 256 {
		t.Fatalf("max users=%d", cfg.HTTP.MaxTrackedUsers)
	}
}

// 精确 0400 与 0600 被接受；更宽或更窄的权限被拒绝。
// Exactly 0400 and 0600 are accepted; wider or unreadable modes are rejected.
func TestLoadConfigFilePermissions(t *testing.T) {
	root := ppAbsRoot(t)
	content := ppDownloadJSON(t, root, nil)

	for _, tc := range []struct {
		perm os.FileMode
		ok   bool
	}{
		{0o400, true},
		{0o600, true},
		{0o644, false},
		{0o640, false},
		{0o604, false},
		{0o666, false},
		{0o000, false},
	} {
		path := ppWritePerm(t, content, tc.perm)
		_, err := LoadConfig(ppDownloadEnv(path, nil))
		if tc.ok && err != nil {
			t.Fatalf("perm %o rejected: %v", tc.perm, err)
		}
		if !tc.ok && !errors.Is(err, ErrConfiguration) {
			t.Fatalf("perm %o err=%v want ErrConfiguration", tc.perm, err)
		}
	}
}

// 符号链接配置路径必须被拒绝（O_NOFOLLOW 语义）。
// A symlinked config path must be rejected (O_NOFOLLOW semantics).
func TestLoadConfigRejectsSymlinkConfig(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := os.WriteFile(real, []byte(ppDownloadJSON(t, ppAbsRoot(t), nil)), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := LoadConfig(ppDownloadEnv(link, nil))
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("err=%v want ErrConfiguration", err)
	}
}

// FIFO 配置必须立刻拒绝，不得阻塞在打开上。
// A FIFO config must be rejected promptly without blocking on open.
func TestLoadConfigRejectsFIFOConfigNonBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "download.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	t.Cleanup(func() { os.Remove(fifo) })

	done := make(chan error, 1)
	go func() {
		_, err := LoadConfig(ppDownloadEnv(fifo, nil))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("err=%v want ErrConfiguration", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LoadConfig blocked on FIFO open")
	}
}

// 目录与缺失路径都必须拒绝。
// A directory and a missing path must both be rejected.
func TestLoadConfigRejectsDirectoryAndMissingConfig(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{"directory": dir, "missing": filepath.Join(dir, "absent.json")} {
		_, err := LoadConfig(ppDownloadEnv(path, nil))
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("%s err=%v want ErrConfiguration", name, err)
		}
	}
}

// 超过 16KiB 的配置必须在解析前拒绝。
// A config larger than 16KiB must be rejected before parsing.
func TestLoadConfigRejectsOversizedConfig(t *testing.T) {
	root := ppAbsRoot(t)
	body := ppDownloadJSON(t, root, nil)
	// 在 "{" 后插入 17KiB 空白：JSON 仍合法，仅尺寸超限。
	// Insert 17KiB of whitespace after "{": the JSON stays valid, only the
	// size exceeds the bound.
	padded := "{" + strings.Repeat(" ", 17*1024) + body[1:]
	path := ppWritePerm(t, padded, 0o600)
	if _, err := LoadConfig(ppDownloadEnv(path, nil)); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("oversized err=%v want ErrConfiguration", err)
	}
}

// --- LoadConfig：JSON 结构 / JSON structure ---

// 非对象、null、尾随第二 JSON、重复键、未知键、错误类型、浮点与溢出。
// Non-object, null, trailing second JSON, duplicate keys, unknown keys, wrong
// types, floats and overflow.
func TestLoadConfigJSONStructureRejections(t *testing.T) {
	root := ppAbsRoot(t)
	valid := ppDownloadJSON(t, root, nil)

	cases := map[string]string{
		"array":            "[]",
		"string":           `"download"`,
		"null":             "null",
		"number":           "42",
		"empty object":     "{}",
		"trailing second":  valid + valid,
		"duplicate key":    strings.Replace(valid, `"repo_key": "local-repo"`, `"repo_key": "local-repo", "repo_key": "other-repo"`, 1),
		"unknown key":      strings.Replace(valid, `"repo_key": "local-repo"`, `"repo_key": "local-repo", "extra_key": 1`, 1),
		"threshold float":  strings.Replace(valid, `"failure_threshold": 5`, `"failure_threshold": 1.5`, 1),
		"threshold string": strings.Replace(valid, `"failure_threshold": 5`, `"failure_threshold": "5"`, 1),
		"window float":     strings.Replace(valid, `"failure_window_seconds": 300`, `"failure_window_seconds": 30.5`, 1),
		"users overflow":   strings.Replace(valid, `"max_tracked_users": 256`, `"max_tracked_users": 99999999999999999999`, 1),
		"listen number":    strings.Replace(valid, `"listen_addr": "127.0.0.1:18443"`, `"listen_addr": 18443`, 1),
		"dsn null":         ppDownloadJSON(t, root, func(m map[string]any) { m["postgres_dsn"] = nil }),
		"challenge bool":   ppDownloadJSON(t, root, func(m map[string]any) { m["challenge"] = true }),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := ppWritePerm(t, content, 0o600)
			_, err := LoadConfig(ppDownloadEnv(path, nil))
			if !errors.Is(err, ErrConfiguration) {
				t.Fatalf("err=%v want ErrConfiguration", err)
			}
		})
	}
}

// 9 个键逐一缺失或为空字符串都必须拒绝。
// Each of the 9 keys, when missing or empty, must be rejected.
func TestLoadConfigRequiredFieldsPresentAndNonEmpty(t *testing.T) {
	keys := []string{
		"listen_addr", "postgres_dsn", "filestore_root", "repo_key",
		"challenge", "failure_threshold", "failure_window_seconds",
		"block_seconds", "max_tracked_users",
	}
	for _, key := range keys {
		for _, mode := range []string{"omit", "empty"} {
			name := key + "/" + mode
			t.Run(name, func(t *testing.T) {
				content := ppDownloadJSON(t, ppAbsRoot(t), func(m map[string]any) {
					switch mode {
					case "omit":
						delete(m, key)
					case "empty":
						switch m[key].(type) {
						case string:
							m[key] = ""
						default:
							m[key] = 0
						}
					}
				})
				path := ppWritePerm(t, content, 0o600)
				_, err := LoadConfig(ppDownloadEnv(path, nil))
				if !errors.Is(err, ErrConfiguration) {
					t.Fatalf("err=%v want ErrConfiguration", err)
				}
			})
		}
	}
}

// --- LoadConfig：值边界 / value bounds ---

// 下载监听地址仅接受字面环回 IP + 数字端口 0..65535。
// The download listen address accepts literal loopback IPs with numeric
// ports 0..65535 only.
func TestLoadConfigListenAddrValidation(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:18443", true},
		{"127.0.0.1:0", true},
		{"127.0.0.1:65535", true},
		{"[::1]:18443", true},
		{"[::1]:0", true},
		{"127.0.0.2:8080", true}, // any 127/8 loopback literal
		{"127.0.0.1:65536", false},
		{"127.0.0.1:-1", false},
		{"127.0.0.1:abc", false},
		{"127.0.0.1", false},
		{":18443", false},
		{"[::1]", false},
		{"[::1]:70000", false},
		{"localhost:18443", false},
		{"LOCALHOST:18443", false},
		{"0.0.0.0:18443", false},
		{"[::]:18443", false},
		{"10.0.0.1:18443", false},
		{"192.168.1.10:8080", false},
		{"db.internal:5432", false},
		{" 127.0.0.1:18443", false},
		{"127.0.0.1:18443 ", false},
		{"127.0.0.1:18443:x", false},
	}
	for _, tc := range cases {
		content := ppDownloadJSON(t, ppAbsRoot(t), func(m map[string]any) {
			m["listen_addr"] = tc.addr
		})
		path := ppWritePerm(t, content, 0o600)
		cfg, err := LoadConfig(ppDownloadEnv(path, nil))
		if tc.ok {
			if err != nil {
				t.Fatalf("addr %q rejected: %v", tc.addr, err)
			}
			if cfg.ListenAddr != tc.addr {
				t.Fatalf("addr %q mapped to %q", tc.addr, cfg.ListenAddr)
			}
			continue
		}
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("addr %q err=%v want ErrConfiguration", tc.addr, err)
		}
	}
}

// filestore_root 必须非空且绝对。
// filestore_root must be nonempty and absolute.
func TestLoadConfigFilestoreRootAbsolute(t *testing.T) {
	for _, root := range []string{"var/data", "data", "", ".", "~/filestore"} {
		content := ppDownloadJSON(t, root, func(m map[string]any) { m["filestore_root"] = root })
		path := ppWritePerm(t, content, 0o600)
		_, err := LoadConfig(ppDownloadEnv(path, nil))
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("root %q err=%v want ErrConfiguration", root, err)
		}
	}
	for _, root := range []string{ppAbsRoot(t), "/binflow/filestore"} {
		content := ppDownloadJSON(t, root, func(m map[string]any) { m["filestore_root"] = root })
		path := ppWritePerm(t, content, 0o600)
		cfg, err := LoadConfig(ppDownloadEnv(path, nil))
		if err != nil {
			t.Fatalf("root %q rejected: %v", root, err)
		}
		if cfg.FilestoreRoot != root {
			t.Fatalf("root %q mapped to %q", root, cfg.FilestoreRoot)
		}
	}
}

// HTTP 失败门与仓库/挑战字段的显式开闭区间。
// Explicit open/closed bounds for the gate numbers and repo/challenge
// strings.
func TestLoadConfigHTTPBounds(t *testing.T) {
	type c struct {
		name  string
		key   string
		val   any
		valid bool
	}
	cases := []c{
		{"threshold 1", "failure_threshold", 1, true},
		{"threshold 100", "failure_threshold", 100, true},
		{"threshold 0", "failure_threshold", 0, false},
		{"threshold 101", "failure_threshold", 101, false},
		{"window 1", "failure_window_seconds", 1, true},
		{"window 3600", "failure_window_seconds", 3600, true},
		{"window 0", "failure_window_seconds", 0, false},
		{"window 3601", "failure_window_seconds", 3601, false},
		{"block 1", "block_seconds", 1, true},
		{"block 3600", "block_seconds", 3600, true},
		{"block 0", "block_seconds", 0, false},
		{"block 3601", "block_seconds", 3601, false},
		{"users 1", "max_tracked_users", 1, true},
		{"users 4096", "max_tracked_users", 4096, true},
		{"users 0", "max_tracked_users", 0, false},
		{"users 4097", "max_tracked_users", 4097, false},
		{"repo 64", "repo_key", strings.Repeat("a", 64), true},
		{"repo 65", "repo_key", strings.Repeat("a", 65), false},
		{"repo dot", "repo_key", ".", false},
		{"repo dotdot", "repo_key", "..", false},
		{"repo space", "repo_key", "repo key", false},
		{"repo nonascii", "repo_key", "rëpo", false},
		{"repo slash", "repo_key", "a/b", false},
		{"challenge printable", "challenge", `Basic realm="x" !?~,;:<>@#$%^&*()_+-=[]{}|\.`, true},
		{"challenge del", "challenge", "a\x7f", false},
		{"challenge nonascii", "challenge", "réalm", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := ppDownloadJSON(t, ppAbsRoot(t), func(m map[string]any) {
				m[tc.key] = tc.val
			})
			path := ppWritePerm(t, content, 0o600)
			_, err := LoadConfig(ppDownloadEnv(path, nil))
			if tc.valid && err != nil {
				t.Fatalf("rejected valid %s=%v: %v", tc.key, tc.val, err)
			}
			if !tc.valid && !errors.Is(err, ErrConfiguration) {
				t.Fatalf("%s=%v err=%v want ErrConfiguration", tc.key, tc.val, err)
			}
		})
	}
}

// --- LoadConfig：脱敏 / redaction ---

// Config 的所有格式化路径都固定脱敏，不泄漏 DSN/挑战/路径。
// Every formatting path for Config stays fixed-redacted, leaking no
// DSN/challenge/path material.
func TestLoadConfigRedactionNeverLeaksSecrets(t *testing.T) {
	cfg, err := ppLoadDownload(t, ppDownloadJSON(t, ppAbsRoot(t), nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	leakVectors := []string{
		cfg.PostgresDSN,
		ppCanaryDSNPassword,
		ppCanaryChallenge,
		cfg.FilestoreRoot,
		cfg.HTTP.Challenge,
	}
	formats := []string{
		fmt.Sprintf("%s", cfg),
		fmt.Sprintf("%v", cfg),
		fmt.Sprintf("%+v", cfg),
		fmt.Sprintf("%#v", cfg),
		fmt.Sprintf("%s %v %+v %#v", cfg, cfg, cfg, cfg),
		fmt.Sprintf("%s", &cfg),
		fmt.Sprintf("%v", &cfg),
		fmt.Sprintf("%#v", &cfg),
	}
	for i, text := range formats {
		for _, secret := range leakVectors {
			if secret != "" && strings.Contains(text, secret) {
				t.Fatalf("format %d leaks private configuration material", i)
			}
		}
		if !strings.Contains(text, "redacted") {
			t.Fatalf("format %d = %q lacks fixed redaction marker", i, text)
		}
	}
}

// 拒绝路径的错误文本不得回显文件内容（金丝雀不出现）。
// Rejection errors must never echo file contents (canaries absent).
func TestLoadConfigErrorsNeverEchoSecrets(t *testing.T) {
	root := ppAbsRoot(t)
	bad := ppDownloadJSON(t, root, func(m map[string]any) {
		m["repo_key"] = "bad repo key" // 结构合法但值非法，秘密仍在文件中。
	})
	for _, content := range []string{bad, "{" + strings.Repeat(" ", 17*1024) + ppDownloadJSON(t, root, nil)[1:]} {
		path := ppWritePerm(t, content, 0o600)
		_, err := LoadConfig(ppDownloadEnv(path, nil))
		if err == nil {
			t.Fatal("expected rejection")
		}
		if !errors.Is(err, ErrConfiguration) {
			t.Fatalf("err=%v want ErrConfiguration", err)
		}
		ppAssertNoCanary(t, err.Error(), "LoadConfig error")
	}
}

// --- parsePostgresDSN：严格 URL 画像 / strict URL profile ---

// ppAssertDSNFields 校验解析结果的核心字段画像。
// ppAssertDSNFields checks the core parsed profile.
func ppAssertDSNFields(t *testing.T, cc *pgx.ConnConfig, host string, port uint16, user, password, database string) {
	t.Helper()
	if cc == nil {
		t.Fatal("nil ConnConfig")
	}
	if cc.Host != host {
		t.Fatalf("host=%q want %q", cc.Host, host)
	}
	if cc.Port != port {
		t.Fatalf("port=%d want %d", cc.Port, port)
	}
	if cc.User != user {
		t.Fatalf("user=%q want %q", cc.User, user)
	}
	if cc.Password != password {
		t.Fatalf("password mismatch")
	}
	if cc.Database != database {
		t.Fatalf("database=%q want %q", cc.Database, database)
	}
	if cc.ConnectTimeout != 10*time.Second {
		t.Fatalf("connect timeout=%v want 10s", cc.ConnectTimeout)
	}
	if got := cc.RuntimeParams["application_name"]; got != "binflow-download" {
		t.Fatalf("application_name=%q want binflow-download", got)
	}
	if len(cc.Fallbacks) != 0 {
		t.Fatalf("fallbacks=%d want 0 (no host lists / prefer chains)", len(cc.Fallbacks))
	}
}

// 合法 disable 画像：环回主机、显式端口、无 TLS、无回退。
// A valid disable profile: loopback host, explicit port, no TLS, no
// fallbacks.
func TestParsePostgresDSNValidDisable(t *testing.T) {
	cc, err := parsePostgresDSN("postgres://binflow:secret@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ppAssertDSNFields(t, cc, "127.0.0.1", 5432, "binflow", "secret", "binflow")
	if cc.TLSConfig != nil {
		t.Fatal("sslmode=disable must yield nil TLSConfig")
	}
	if got := cc.RuntimeParams["search_path"]; got != "binflow" {
		t.Fatalf("search_path=%q want binflow (trusted explicit value retained)", got)
	}
}

// 合法 verify-full 画像：IPv6 环回、TLS 非宽松、显式保留 search_path。
// A valid verify-full profile: IPv6 loopback, strict TLS, explicit
// search_path retained.
func TestParsePostgresDSNValidVerifyFull(t *testing.T) {
	raw := "postgresql://binflow:secret@[::1]:5433/binflow?sslmode=verify-full"
	cc, err := parsePostgresDSN(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ppAssertDSNFields(t, cc, "::1", 5433, "binflow", "secret", "binflow")
	if cc.TLSConfig == nil {
		t.Fatal("verify-full must yield non-nil TLSConfig")
	}
	if cc.TLSConfig.InsecureSkipVerify {
		t.Fatal("verify-full must verify the hostname")
	}
	if _, ok := cc.RuntimeParams["search_path"]; ok {
		t.Fatal("search_path must be absent when not requested")
	}
}

// 单个 DNS 或远端主机在 verify-full 下受支持，解析不发起连接。
// A single DNS or remote host is supported with verify-full; parsing does not dial.
func TestParsePostgresDSNVerifyFullRemoteHosts(t *testing.T) {
	for _, host := range []string{"db.internal", "10.1.2.3", "0.0.0.0"} {
		t.Run(host, func(t *testing.T) {
			cc, err := parsePostgresDSN("postgres://u:p@" + host + ":5432/db?sslmode=verify-full")
			if err != nil {
				t.Fatalf("supported verify-full host rejected: %v", err)
			}
			ppAssertDSNFields(t, cc, host, 5432, "u", "p", "db")
			if cc.TLSConfig == nil || cc.TLSConfig.InsecureSkipVerify {
				t.Fatal("verify-full lost TLS verification")
			}
		})
	}
}

// 显式 URL 字段覆盖对应 PG 环境值；完整隔离由 Run 拒绝所有 PG* 键保证。
// Explicit URL fields override their corresponding PG environment values;
// full isolation is enforced by Run rejecting every present PG* key.
func TestParsePostgresDSNExplicitFieldsTakePrecedence(t *testing.T) {
	t.Setenv("PGUSER", "envuser")
	t.Setenv("PGPASSWORD", "envpassword")
	t.Setenv("PGHOST", "envhost.invalid")
	t.Setenv("PGPORT", "6543")
	t.Setenv("PGDATABASE", "envdb")
	t.Setenv("PGSSLMODE", "verify-full")
	t.Setenv("PGAPPNAME", "envapp")
	t.Setenv("PGOPTIONS", "-c search_path=envschema")

	t.Run("explicit disable wins over env", func(t *testing.T) {
		cc, err := parsePostgresDSN("postgres://binflow:secret@127.0.0.1:5432/binflow?sslmode=disable")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		ppAssertDSNFields(t, cc, "127.0.0.1", 5432, "binflow", "secret", "binflow")
		if cc.TLSConfig != nil {
			t.Fatal("explicit sslmode=disable must stay disabled despite PGSSLMODE")
		}
	})

	t.Run("explicit verify-full wins over env", func(t *testing.T) {
		cc, err := parsePostgresDSN("postgres://binflow:secret@127.0.0.1:5432/binflow?sslmode=verify-full")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		ppAssertDSNFields(t, cc, "127.0.0.1", 5432, "binflow", "secret", "binflow")
		if cc.TLSConfig == nil || cc.TLSConfig.InsecureSkipVerify {
			t.Fatal("explicit sslmode=verify-full must stay verified despite PGSSLMODE")
		}
	})
}

// 拒绝画像：scheme/主机/端口/凭据/库/查询全维度。
// Rejected profiles across scheme/host/port/credentials/database/query.
func TestParsePostgresDSNRejects(t *testing.T) {
	cases := map[string]string{
		"empty":                      "",
		"not a url":                  "host=127.0.0.1 port=5432 user=u password=p dbname=db sslmode=disable",
		"wrong scheme":               "mysql://u:p@127.0.0.1:5432/db?sslmode=disable",
		"near scheme":                "postgre://u:p@127.0.0.1:5432/db?sslmode=disable",
		"opaque form":                "postgres:u:p@127.0.0.1:5432/db",
		"fragment":                   "postgres://u:p@127.0.0.1:5432/db?sslmode=disable#frag",
		"missing port":               "postgres://u:p@127.0.0.1/db?sslmode=disable",
		"port zero":                  "postgres://u:p@127.0.0.1:0/db?sslmode=disable",
		"port over":                  "postgres://u:p@127.0.0.1:65536/db?sslmode=disable",
		"port non numeric":           "postgres://u:p@127.0.0.1:https/db?sslmode=disable",
		"missing host":               "postgres:///db?sslmode=disable",
		"host list":                  "postgres://u:p@127.0.0.1:5432,127.0.0.2:5432/db?sslmode=disable",
		"unix socket dir":            "postgres://u:p@/var/run/postgresql:5432/db?sslmode=disable",
		"missing user":               "postgres://:p@127.0.0.1:5432/db?sslmode=disable",
		"missing password":           "postgres://u@127.0.0.1:5432/db?sslmode=disable",
		"empty password":             "postgres://u:@127.0.0.1:5432/db?sslmode=disable",
		"empty database":             "postgres://u:p@127.0.0.1:5432/?sslmode=disable",
		"database too long":          "postgres://u:p@127.0.0.1:5432/" + strings.Repeat("d", 64) + "?sslmode=disable",
		"database invalid charset":   "postgres://u:p@127.0.0.1:5432/db$name?sslmode=disable",
		"database space":             "postgres://u:p@127.0.0.1:5432/db%20name?sslmode=disable",
		"no sslmode":                 "postgres://u:p@127.0.0.1:5432/db",
		"empty sslmode":              "postgres://u:p@127.0.0.1:5432/db?sslmode=",
		"duplicate sslmode":          "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&sslmode=disable",
		"sslmode prefer":             "postgres://u:p@127.0.0.1:5432/db?sslmode=prefer",
		"sslmode require":            "postgres://u:p@127.0.0.1:5432/db?sslmode=require",
		"sslmode allow":              "postgres://u:p@127.0.0.1:5432/db?sslmode=allow",
		"sslmode verify-ca":          "postgres://u:p@127.0.0.1:5432/db?sslmode=verify-ca",
		"disable non loopback":       "postgres://u:p@10.1.2.3:5432/db?sslmode=disable",
		"unknown query key":          "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&application_name=x",
		"unknown query key host":     "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&hostaddr=127.0.0.1",
		"duplicate search_path":      "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&search_path=a&search_path=b",
		"search_path empty":          "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&search_path=",
		"search_path too long":       "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&search_path=" + strings.Repeat("s", 64),
		"search_path invalid":        "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&search_path=a;b",
		"search_path non ascii":      "postgres://u:p@127.0.0.1:5432/db?sslmode=disable&search_path=sch%C3%A9ma",
		"query only sslmode missing": "postgres://u:p@127.0.0.1:5432/db?search_path=public",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			cc, err := parsePostgresDSN(raw)
			if err == nil {
				t.Fatalf("accepted invalid DSN (host=%q port=%d)", cc.Host, cc.Port)
			}
			if cc != nil {
				t.Fatal("non-nil config alongside error")
			}
		})
	}
}

// 数据库名允许的有限字符集边界。
// The accepted finite database-name charset boundaries.
func TestParsePostgresDSNDatabaseCharset(t *testing.T) {
	for _, db := range []string{"binflow", "bf_data", "bf.data", "BF1", strings.Repeat("d", 63)} {
		raw := "postgres://u:p@127.0.0.1:5432/" + db + "?sslmode=disable"
		cc, err := parsePostgresDSN(raw)
		if err != nil {
			t.Fatalf("database %q rejected: %v", db, err)
		}
		if cc.Database != db {
			t.Fatalf("database %q mapped to %q", db, cc.Database)
		}
	}
}

// 解析失败不得携带原始 pgx 错误细节或密码金丝雀。
// Parse failures must not carry raw pgx error details or the password
// canary.
func TestParsePostgresDSNErrorsDoNotLeakPassword(t *testing.T) {
	raws := []string{
		"postgres://u:" + ppCanaryDSNPassword + "@db.internal:5432/db?sslmode=disable",
		"postgres://u:" + ppCanaryDSNPassword + "@127.0.0.1:99999/db?sslmode=disable",
		"postgres://u:" + ppCanaryDSNPassword + "@127.0.0.1:5432/db?sslmode=require",
	}
	for _, raw := range raws {
		_, err := parsePostgresDSN(raw)
		if err == nil {
			t.Fatalf("accepted invalid DSN")
		}
		ppAssertNoCanary(t, err.Error(), "parsePostgresDSN error")
		if strings.Contains(err.Error(), "pgconn") || strings.Contains(err.Error(), "cannot parse") {
			t.Fatalf("error carries raw driver detail: %q", err.Error())
		}
	}
}

// --- newRouter：原始 RequestURI 分发 / raw RequestURI dispatch ---

// /artifactory/ 原始前缀直发下载处理器：方法不过滤、原始 URI 与查询保留。
// The literal raw /artifactory/ prefix dispatches straight to the download
// handler: no method filtering, raw URI and query preserved.
func TestNewRouterRawPrefixDispatchesToDownload(t *testing.T) {
	spy := &ppSpyHandler{}
	router := newRouter(spy)

	cases := []struct{ method, target string }{
		{http.MethodGet, "/artifactory/local-repo/path/to/asset.bin"},
		{http.MethodHead, "/artifactory/local-repo/asset.bin"},
		{http.MethodPost, "/artifactory/local-repo/upload"},
		{http.MethodGet, "/artifactory/local-repo/a%2Fb?x=1&y=%2F"},
		{http.MethodGet, "/artifactory/local-repo"},
		{http.MethodGet, "/artifactory/"},
	}
	for _, tc := range cases {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, ppRawRequest(t, tc.method, tc.target))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s %q code=%d want spy 204", tc.method, tc.target, rr.Code)
		}
		if rr.Header().Get("X-Binflow-Spy") != "download" {
			t.Fatalf("%s %q did not reach download spy", tc.method, tc.target)
		}
	}

	got := spy.snapshot()
	want := []string{
		"GET /artifactory/local-repo/path/to/asset.bin",
		"HEAD /artifactory/local-repo/asset.bin",
		"POST /artifactory/local-repo/upload",
		"GET /artifactory/local-repo/a%2Fb?x=1&y=%2F",
		"GET /artifactory/local-repo",
		"GET /artifactory/",
	}
	if len(got) != len(want) {
		t.Fatalf("spy calls=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spy[%d]=%q want raw %q (no unescape/clean)", i, got[i], want[i])
		}
	}
}

// 仅 URL.Path（解码/清洗后）指向下载的编码或绝对形式必须留在健康处理器。
// Encoded or absolute forms whose URL.Path alone would select download must
// stay with the health handler.
func TestNewRouterEncodedOrAbsoluteFormsStayWithHealth(t *testing.T) {
	spy := &ppSpyHandler{}
	router := newRouter(spy)

	targets := []string{
		"/artifactory%2Flocal-repo/asset.bin",       // 编码斜杠在首段边界 / encoded slash at the first-segment boundary
		"/%61rtifactory/local-repo/asset.bin",       // 编码字母 / encoded 'a'
		"/x/../artifactory/local-repo/asset.bin",    // 点段前缀 / dot-segment prefix
		"//127.0.0.1/artifactory/local-repo/a",      // 网络路径形式 / network-path form
		"http://127.0.0.1/artifactory/local-repo/a", // 绝对形式 / absolute form
		"/artifactory%00/x",                         // 编码 NUL / encoded NUL
		"/artifactory./local-repo/a",                // 前缀后多字符 / extra char after prefix
		"/artifactoryx/local-repo/a",
		"/ARTIFACTORY/local-repo/a",
		"/prefix/artifactory/local-repo/a",
	}
	for _, target := range targets {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, ppRawRequest(t, http.MethodGet, target))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("target %q code=%d want health 404 (decoded path alone must not select download)", target, rr.Code)
		}
	}
	if calls := spy.snapshot(); len(calls) != 0 {
		t.Fatalf("spy reached for excluded forms: %v", calls)
	}
}

// 健康端点行为保持不变：GET/HEAD 200、405、未知 404。
// Health endpoint behavior is preserved: GET/HEAD 200, 405, unknown 404.
func TestNewRouterHealthEndpointsUnchanged(t *testing.T) {
	spy := &ppSpyHandler{}
	router := newRouter(spy)

	get := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, get)
	if rr.Code != http.StatusOK || rr.Body.String() != "ok\n" {
		t.Fatalf("GET /healthz code=%d body=%q want 200 ok\\n", rr.Code, rr.Body.String())
	}

	head := httptest.NewRequest(http.MethodHead, "/healthz", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, head)
	if rr.Code != http.StatusOK || rr.Body.Len() != 0 {
		t.Fatalf("HEAD /healthz code=%d body=%q want 200 empty", rr.Code, rr.Body.String())
	}

	post := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, post)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz code=%d want 405", rr.Code)
	}

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET /nope code=%d want 404", rr.Code)
	}

	if calls := spy.snapshot(); len(calls) != 0 {
		t.Fatalf("health traffic reached download spy: %v", calls)
	}
}

// --- serve：确定性生命周期 / deterministic lifecycle ---

// ppCloseOwnedOnce 包装 closeOwned：记录事件、发信号，重复调用被记录。
// ppCloseOwnedOnce wraps closeOwned: logs the event, signals, and records
// repeat calls.
func ppCloseOwnedOnce(log *ppLog, ch chan struct{}) func() error {
	var calls atomic.Int32
	return func() error {
		if calls.Add(1) > 1 {
			log.add("closeOwnedExtra")
			return nil
		}
		log.add("closeOwned")
		ch <- struct{}{}
		return nil
	}
}

// ppStartServe 启动 serve 并把返回记入日志。
// ppStartServe starts serve and logs its return.
func ppStartServe(ctx context.Context, ln net.Listener, handler http.Handler, shutdown time.Duration, closeOwned func() error, onReady func(string), log *ppLog) <-chan error {
	serveDone := make(chan error, 1)
	go func() {
		err := serve(ctx, ln, handler, shutdown, closeOwned, onReady)
		log.add("serveReturn")
		serveDone <- err
	}()
	return serveDone
}

func ppWaitServe(t *testing.T, serveDone <-chan error) error {
	t.Helper()
	select {
	case err := <-serveDone:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return")
		return nil
	}
}

// ppWaitReady 等待 onReady 并断言返回的是实际绑定地址。
// ppWaitReady waits for onReady and asserts the actually bound address.
func ppWaitReady(t *testing.T, ready <-chan string, ln net.Listener) string {
	t.Helper()
	select {
	case addr := <-ready:
		if addr != ln.Addr().String() {
			t.Fatalf("onReady addr=%q want bound %q", addr, ln.Addr().String())
		}
		return addr
	case <-time.After(15 * time.Second):
		t.Fatal("onReady never fired")
		return ""
	}
}

func ppNewTCPListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

// 优雅路径：在途请求先完成，资源才关闭；请求上下文不被取消。
// Graceful path: the in-flight request finishes before resources close, and
// its request context is never canceled.
func TestServeGracefulDrainsInFlightBeforeClosingResources(t *testing.T) {
	log := &ppLog{}
	ln := ppNewTCPListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	reqCtxErr := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		reqCtxErr <- r.Context().Err() // graceful drain must leave this nil
		log.add("handlerExit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("done"))
	})

	closedCh := make(chan struct{}, 1)
	ready := make(chan string, 1)
	serveDone := ppStartServe(ctx, ln, handler, 10*time.Second, ppCloseOwnedOnce(log, closedCh), func(addr string) { ready <- addr }, log)

	addr := ppWaitReady(t, ready, ln)
	conn, reader := ppDialHTTP(t, addr, http.MethodGet, "/healthz")
	defer ppCloseConn(conn)
	ppRecvEvent(t, entered, "handler admission")

	cancel()                // graceful drain starts; in-flight request keeps running
	ppAwaitRefused(t, addr) // keep the request blocked until shutdown has started
	releaseHandler()

	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	if err := ppWaitServe(t, serveDone); err != nil {
		t.Fatalf("graceful serve err=%v want nil", err)
	}
	ppRecvEvent(t, closedCh, "closeOwned after graceful drain")

	if got := <-reqCtxErr; got != nil {
		t.Fatalf("graceful drain canceled the admitted request ctx: %v", got)
	}
	iExit, iClose, iRet := log.index("handlerExit"), log.index("closeOwned"), log.index("serveReturn")
	if iExit < 0 || iClose <= iExit || iRet <= iClose {
		t.Fatalf("ordering want handlerExit<closeOwned<serveReturn, got %d/%d/%d (%v)", iExit, iClose, iRet, log.events)
	}
	select {
	case <-closedCh:
		t.Fatal("closeOwned called more than once")
	default:
	}
}

// 强制路径：排空超时先返回 ErrDrainTimeout，资源保持到处理器真正退出后
// 才由单一 reaper 关闭。
// Forced path: the drain deadline returns ErrDrainTimeout first; resources
// stay untouched until the handler actually returns, then a single reaper
// closes them.
func TestServeForcedTimeoutKeepsResourcesUntilRelease(t *testing.T) {
	log := &ppLog{}
	ln := ppNewTCPListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release // 模拟不可中断的阻塞提供者读 / uninterruptible blocked provider read
		log.add("handlerExit")
	})

	closedCh := make(chan struct{}, 1)
	ready := make(chan string, 1)
	serveDone := ppStartServe(ctx, ln, handler, 25*time.Millisecond, ppCloseOwnedOnce(log, closedCh), func(addr string) { ready <- addr }, log)

	addr := ppWaitReady(t, ready, ln)
	conn, _ := ppDialHTTP(t, addr, http.MethodGet, "/healthz")
	defer ppCloseConn(conn)
	ppRecvEvent(t, entered, "handler admission")

	cancel()
	err := ppWaitServe(t, serveDone)
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("err=%v want ErrDrainTimeout", err)
	}
	// 此时 release 尚未发出：closeOwned 不可能已发生（确定性，非睡眠推断）。
	// release has not been sent yet, so closeOwned cannot have happened
	// (deterministic, not a sleep-based inference).
	if log.has("closeOwned") {
		t.Fatal("resources closed while handler still held them")
	}

	releaseHandler()
	ppRecvEvent(t, closedCh, "reaper closeOwned after release")
	iExit, iClose := log.index("handlerExit"), log.index("closeOwned")
	if iExit < 0 || iClose <= iExit {
		t.Fatalf("ordering want handlerExit<closeOwned, got %d/%d (%v)", iExit, iClose, log.events)
	}
	if log.has("closeOwnedExtra") {
		t.Fatal("reaper closed resources more than once")
	}
}

// panic（ErrAbortHandler）经 defer 释放准入计数：仍能优雅排空并先关资源
// 后返回。
// A panic (ErrAbortHandler) releases the admission count via defer: graceful
// drain still closes resources before returning.
func TestServePanicReleasesAdmissionAndStillDrains(t *testing.T) {
	log := &ppLog{}
	ln := ppNewTCPListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	prePanic := make(chan struct{}, 1)
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			log.add("prePanic")
			prePanic <- struct{}{}
			panic(http.ErrAbortHandler)
		}
		entered <- struct{}{}
		<-release
		log.add("handlerExit")
		w.WriteHeader(http.StatusOK)
	})

	closedCh := make(chan struct{}, 1)
	ready := make(chan string, 1)
	serveDone := ppStartServe(ctx, ln, handler, 10*time.Second, ppCloseOwnedOnce(log, closedCh), func(addr string) { ready <- addr }, log)

	addr := ppWaitReady(t, ready, ln)

	// 第一条请求触发 panic；连接被中止，客户端读到 EOF/重置。
	// The first request panics; the connection aborts and the client sees
	// EOF/reset.
	boom, boomReader := ppDialHTTP(t, addr, http.MethodGet, "/boom")
	defer ppCloseConn(boom)
	ppRecvEvent(t, prePanic, "panic handler entry")
	boom.SetReadDeadline(time.Now().Add(5 * time.Second))
	if resp, err := http.ReadResponse(boomReader, &http.Request{Method: http.MethodGet}); err == nil {
		resp.Body.Close()
		t.Fatalf("panicked request got a response (%d), want aborted connection", resp.StatusCode)
	}

	// panic 后仍可服务新请求，随后优雅关闭。
	// New requests are still served after the panic, then shut down
	// gracefully.
	ok, okReader := ppDialHTTP(t, addr, http.MethodGet, "/ok")
	defer ppCloseConn(ok)
	ppRecvEvent(t, entered, "second handler admission")

	cancel()
	ppAwaitRefused(t, addr)
	releaseHandler()
	resp, err := http.ReadResponse(okReader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read second response: %v", err)
	}
	resp.Body.Close()

	if err := ppWaitServe(t, serveDone); err != nil {
		t.Fatalf("post-panic graceful serve err=%v want nil", err)
	}
	ppRecvEvent(t, closedCh, "closeOwned after post-panic drain")
	iExit, iClose, iRet := log.index("handlerExit"), log.index("closeOwned"), log.index("serveReturn")
	if iExit < 0 || iClose <= iExit || iRet <= iClose {
		t.Fatalf("ordering want handlerExit<closeOwned<serveReturn, got %d/%d/%d (%v)", iExit, iClose, iRet, log.events)
	}
	select {
	case <-closedCh:
		t.Fatal("closeOwned called more than once")
	default:
	}
}

// 封闭后：新连接被拒、同连接已排队请求不再进入处理器（503 或连接关闭），
// 处理器调用数保持 1。
// After seal: new dials are refused, a request already queued on the same
// connection never reaches the handler (503 or a closed connection), and the
// handler invocation count stays 1.
func TestServePostSealAdmissionNeverReachesHandler(t *testing.T) {
	log := &ppLog{}
	ln := ppNewTCPListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var calls atomic.Int32
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		log.add("handlerExit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	closedCh := make(chan struct{}, 1)
	ready := make(chan string, 1)
	serveDone := ppStartServe(ctx, ln, handler, 10*time.Second, ppCloseOwnedOnce(log, closedCh), func(addr string) { ready <- addr }, log)

	addr := ppWaitReady(t, ready, ln)
	conn, reader := ppDialHTTP(t, addr, http.MethodGet, "/first")
	defer ppCloseConn(conn)
	// 立即在同一连接排队第二条请求（字节已在途）。
	// Queue a second request on the same connection right away (bytes in
	// flight).
	if _, err := conn.Write([]byte("GET /second HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: keep-alive\r\n\r\n")); err != nil {
		t.Fatalf("write pipelined request: %v", err)
	}
	ppRecvEvent(t, entered, "first handler admission")

	cancel()
	// 监听关闭证明 Shutdown 已开始，而 seal 严格先于 Shutdown。
	// A closed listener proves Shutdown started, and seal strictly precedes
	// Shutdown.
	ppAwaitRefused(t, addr)
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler invoked %d times after seal, want exactly 1", got)
	}

	releaseHandler()
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read first response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status=%d want 200", resp.StatusCode)
	}

	// 第二条请求：要么固定 503（未触处理器），要么连接被直接关闭。
	// The second request: either the fixed 503 without touching the handler,
	// or the connection is simply closed.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp2, err2 := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err2 == nil {
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("post-seal response status=%d want fixed 503", resp2.StatusCode)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("post-seal 503 still reached the handler (%d calls)", got)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler invocation count=%d want 1", got)
	}

	if err := ppWaitServe(t, serveDone); err != nil {
		t.Fatalf("serve err=%v want nil", err)
	}
	ppRecvEvent(t, closedCh, "closeOwned")
	if log.has("closeOwnedExtra") {
		t.Fatal("closeOwned called more than once")
	}
}

// 准入/封闭竞锤：与 cancel 并发发射的请求，任何真实响应只可能是已准入的
// 200 或未触处理器的固定 503；否则必须是传输层关闭。
// Admission/seal race hammer: requests fired concurrently with cancel can
// only produce an admitted 200 or the fixed 503 that never touches the
// handler; every other outcome must be a transport-level closure.
func TestServeLateAdmissionAlwaysFixed503OrClosed(t *testing.T) {
	log := &ppLog{}
	ln := ppNewTCPListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var mu sync.Mutex
	admitted := make(map[string]bool)
	entered := make(chan string, 64)
	releaseAll := make(chan struct{})
	var releaseAllOnce sync.Once
	releaseHandlers := func() { releaseAllOnce.Do(func() { close(releaseAll) }) }
	t.Cleanup(releaseHandlers)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/gate/")
		mu.Lock()
		admitted[id] = true
		mu.Unlock()
		entered <- id
		<-releaseAll
		log.add("handlerExit")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(id))
	})

	closedCh := make(chan struct{}, 1)
	ready := make(chan string, 1)
	serveDone := ppStartServe(ctx, ln, handler, 10*time.Second, ppCloseOwnedOnce(log, closedCh), func(addr string) { ready <- addr }, log)
	addr := ppWaitReady(t, ready, ln)

	const n = 16
	type result struct {
		id     string
		status int
		body   string
		err    error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("%d", i)
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				results[i] = result{id: id, err: err}
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(15 * time.Second))
			req := fmt.Sprintf("GET /gate/%s HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: keep-alive\r\n\r\n", id)
			if _, err := conn.Write([]byte(req)); err != nil {
				results[i] = result{id: id, err: err}
				return
			}
			conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			reader := bufio.NewReader(conn)
			resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
			if err != nil {
				results[i] = result{id: id, err: err}
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			results[i] = result{id: id, status: resp.StatusCode, body: string(body)}
		}(i)
	}

	// 至少一个请求已准入，随后立即封闭；最后统一放行。
	// At least one request is admitted, then seal immediately; release
	// everything afterwards.
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("no request was ever admitted")
	}
	cancel()
	releaseHandlers()
	clientsDone := make(chan struct{})
	go func() { wg.Wait(); close(clientsDone) }()
	ppRecvEvent(t, clientsDone, "late admission clients")

	if err := ppWaitServe(t, serveDone); err != nil {
		t.Fatalf("serve err=%v want nil", err)
	}
	ppRecvEvent(t, closedCh, "closeOwned")

	for _, res := range results {
		mu.Lock()
		wasAdmitted := admitted[res.id]
		mu.Unlock()
		switch {
		case res.err != nil:
			// 拒绝/重置是合法结果：封闭后不受理。 / Refused/reset is a
			// legal outcome: no admission after seal.
		case res.status == http.StatusOK:
			if !wasAdmitted {
				t.Fatalf("200 for %s never admitted to the handler", res.id)
			}
			if res.body != res.id {
				t.Fatalf("200 body %q does not match request %s", res.body, res.id)
			}
		case res.status == http.StatusServiceUnavailable:
			if wasAdmitted {
				t.Fatalf("admitted request %s answered 503 instead of completing", res.id)
			}
		default:
			t.Fatalf("request %s got status %d (body %q): only 200/503/transport-closed are allowed", res.id, res.status, res.body)
		}
	}
}

// 意外 Serve 错误：返回固定 ErrServe（不含原始 OS 细节），排空后先关资源
// 后返回。
// Unexpected Serve failure: returns the fixed ErrServe (no raw OS detail),
// closes owned resources after drain and before returning.
func TestServeUnexpectedServeErrorReturnsErrServe(t *testing.T) {
	log := &ppLog{}
	ln := ppNewTCPListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	closedCh := make(chan struct{}, 1)
	ready := make(chan string, 1)
	serveDone := ppStartServe(ctx, ln, handler, 10*time.Second, ppCloseOwnedOnce(log, closedCh), func(addr string) { ready <- addr }, log)

	ppWaitReady(t, ready, ln)

	// 从外部关闭已移交的监听，模拟 Serve 自身意外失败。
	// Close the transferred listener externally to simulate an unexpected
	// Serve failure.
	if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("external listener close: %v", err)
	}

	err := ppWaitServe(t, serveDone)
	if !errors.Is(err, ErrServe) {
		t.Fatalf("err=%v want ErrServe", err)
	}
	if msg := err.Error(); strings.Contains(msg, "accept") || strings.Contains(msg, "closed network") {
		t.Fatalf("ErrServe carries raw OS detail: %q", msg)
	}
	ppRecvEvent(t, closedCh, "closeOwned after serve failure")
	if iClose, iRet := log.index("closeOwned"), log.index("serveReturn"); iClose < 0 || iRet <= iClose {
		t.Fatalf("ordering want closeOwned<serveReturn, got %d/%d (%v)", iClose, iRet, log.events)
	}
	select {
	case <-closedCh:
		t.Fatal("closeOwned called more than once")
	default:
	}
}

// 已取消的 serve 正常停止：不触发 onReady，关闭移交的监听与资源。
// A pre-canceled serve stops normally without readiness and closes the
// transferred listener and resources. Run owns startup rejection.
func TestServePreCanceledStopsWithoutReadiness(t *testing.T) {
	log := &ppLog{}
	ln := ppNewTCPListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	closedCh := make(chan struct{}, 1)
	ready := make(chan string, 1)
	serveDone := ppStartServe(ctx, ln, handler, 10*time.Second, ppCloseOwnedOnce(log, closedCh), func(addr string) { ready <- addr }, log)

	err := ppWaitServe(t, serveDone)
	if err != nil {
		t.Fatalf("pre-canceled serve err=%v want normal stop", err)
	}
	select {
	case addr := <-ready:
		t.Fatalf("onReady fired on canceled startup with %q", addr)
	default:
	}
	ppRecvEvent(t, closedCh, "closeOwned on canceled startup")
	if iClose, iRet := log.index("closeOwned"), log.index("serveReturn"); iClose < 0 || iRet <= iClose {
		t.Fatalf("ordering want closeOwned<serveReturn, got %d/%d (%v)", iClose, iRet, log.events)
	}

	// 移交的监听必须已被 serve 关闭：同地址可重新绑定。
	// The transferred listener must be closed by serve: the same address can
	// be rebound.
	ln2, err := net.Listen("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("rebind %s failed (listener left open): %v", ln.Addr().String(), err)
	}
	ln2.Close()
}

// --- Run：手工 Config 校验 / Run: manual Config validation ---

// ppBoundedRun 防止 Run 意外进入服务循环时挂起测试。
// ppBoundedRun keeps a test from hanging if Run unexpectedly enters its
// serving loop.
func ppBoundedRun(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

// Run 必须校验手工构造的 Config：健康模式带下载专用字段、未知/空模式与
// 非法下载字段全部拒绝。
// Run must validate manually constructed Configs: download-only fields in
// health mode, unknown/empty modes and invalid download fields are all
// rejected.
func TestRunValidatesManualConfig(t *testing.T) {
	base := ppManualConfig(t)

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty mode", func(c *Config) { c.Mode = "" }},
		{"unknown mode", func(c *Config) { c.Mode = "shiny" }},
		{"health with dsn", func(c *Config) { c.Mode = ModeHealth; c.ListenAddr = "127.0.0.1:18080" }},
		{"health with filestore", func(c *Config) {
			c.Mode = ModeHealth
			c.ListenAddr = "127.0.0.1:18080"
			c.PostgresDSN = ""
			c.FilestoreRoot = ppAbsRoot(t)
		}},
		{"health with repo key", func(c *Config) {
			c.Mode = ModeHealth
			c.ListenAddr = "127.0.0.1:18080"
			c.PostgresDSN = ""
			c.FilestoreRoot = ""
			c.HTTP.RepoKey = "local-repo"
		}},
		{"download empty listen", func(c *Config) { c.ListenAddr = "" }},
		{"download relative filestore", func(c *Config) { c.FilestoreRoot = "relative/root" }},
		{"download bad repo key", func(c *Config) { c.HTTP.RepoKey = "bad key" }},
		{"download empty challenge", func(c *Config) { c.HTTP.Challenge = "" }},
		{"download zero threshold", func(c *Config) { c.HTTP.FailureThreshold = 0 }},
		{"download over max users", func(c *Config) { c.HTTP.MaxTrackedUsers = 4097 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			onReady := make(chan string, 1)
			err := ppBoundedRun(t, func() error { return Run(context.Background(), cfg, func(addr string) { onReady <- addr }) })
			if !errors.Is(err, ErrConfiguration) {
				t.Fatalf("err=%v want ErrConfiguration", err)
			}
			select {
			case addr := <-onReady:
				t.Fatalf("onReady fired on invalid config with %q", addr)
			default:
			}
		})
	}
}

// download Run 在驱动解析前拒绝一切出现的 PG* 环境键，绝不触发 onReady。
// download Run rejects every present PG* environment key before driver
// parsing and never fires onReady.
func TestRunDownloadRejectsPGEnvironment(t *testing.T) {
	cfg := ppManualConfig(t)
	t.Setenv("PGRUNTIME", "1") // 任意 PG 前缀键都必须触发拒绝 / any PG-prefixed key must trigger rejection

	onReady := make(chan string, 1)
	err := ppBoundedRun(t, func() error { return Run(context.Background(), cfg, func(addr string) { onReady <- addr }) })
	if err == nil {
		t.Fatal("Run accepted a PG-polluted environment")
	}
	if !errors.Is(err, ErrConfiguration) {
		t.Fatalf("err=%v want ErrConfiguration before driver parsing", err)
	}
	ppAssertNoCanary(t, err.Error(), "Run error")
	select {
	case addr := <-onReady:
		t.Fatalf("onReady fired on rejected startup with %q", addr)
	default:
	}
}

// 非法 DSN 在任何网络尝试前失败关闭（固定哨兵、无 onReady、无泄漏）。
// An invalid DSN fails closed before any network attempt (fixed sentinel, no
// onReady, no leakage).
func TestRunDownloadInvalidDSNFailsClosed(t *testing.T) {
	cfg := ppManualConfig(t)
	cfg.PostgresDSN = "postgres://binflow:" + ppCanaryDSNPassword + "@127.0.0.1:5432/binflow?sslmode=require" // 被排除的降级模式 / excluded downgrade mode

	onReady := make(chan string, 1)
	err := ppBoundedRun(t, func() error { return Run(context.Background(), cfg, func(addr string) { onReady <- addr }) })
	if err == nil {
		t.Fatal("Run accepted an excluded sslmode")
	}
	if !ppIsFixedSentinel(err) {
		t.Fatalf("err=%v is not a fixed public sentinel", err)
	}
	ppAssertNoCanary(t, err.Error(), "Run error")
	select {
	case addr := <-onReady:
		t.Fatalf("onReady fired with %q", addr)
	default:
	}
}

// nil 上下文必须被拒绝，不得进入服务循环。
// A nil context must be rejected without entering the serving loop.
func TestRunNilContextRejected(t *testing.T) {
	cfg := ppManualConfig(t)
	cfg.Mode = ModeHealth
	cfg.PostgresDSN = ""
	cfg.FilestoreRoot = ""
	cfg.HTTP = security.HTTPConfig{}
	cfg.ListenAddr = "127.0.0.1:18081"

	err := ppBoundedRun(t, func() error { return Run(nil, cfg, nil) })
	if err == nil {
		t.Fatal("Run accepted a nil context")
	}
	if !ppIsFixedSentinel(err) {
		t.Fatalf("err=%v is not a fixed public sentinel", err)
	}
}

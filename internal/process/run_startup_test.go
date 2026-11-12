package process

// 本文件覆盖 Run 的真实启动失败路径与路由组装：健康模式监听失败、下载
// 模式不可达数据库的 Ping 失败回滚、预取消 ctx 的停机汇入、artifacts 路
// 由的 PUT→上传/GET→下载分发、配置格式化的上传脱敏与空受控文件拒绝。
// This file covers the real Run startup failures and router assembly: the
// health-mode listen failure, the download-mode rollback on an unreachable
// database ping, the precanceled-ctx shutdown join, the artifacts-router
// PUT→upload / GET→download dispatch, the upload redaction in configuration
// formatting, and the empty controlled-file rejection.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rstSpyHandler 记录被分发的请求方法与目标。
// rstSpyHandler records the dispatched method and target.
type rstSpyHandler struct {
	events []string
}

func (h *rstSpyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.events = append(h.events, r.Method+" "+r.RequestURI)
	w.WriteHeader(http.StatusTeapot)
}

// TestRunHealthListenFailureFailsClosed 断言健康模式对不可绑定地址
// （非本机 IP）以 ErrStartup 失败关闭，不报告就绪。
// TestRunHealthListenFailureFailsClosed asserts the health mode fails closed
// with ErrStartup for an unbindable address (a nonlocal IP), never reporting
// readiness.
func TestRunHealthListenFailureFailsClosed(t *testing.T) {
	ready := false
	err := Run(context.Background(), Config{Mode: ModeHealth, ListenAddr: "192.0.2.1:58080"}, func(string) { ready = true })
	if !errors.Is(err, ErrStartup) {
		t.Fatalf("error = %v, want ErrStartup", err)
	}
	if ready {
		t.Fatal("failed health run announced readiness")
	}
}

// TestRunDownloadUnreachableDatabaseFailsClosed 断言下载模式对不可达数据
// 库以恰 ErrStartup 失败关闭（Ping 失败即回滚连接池，不混入其他哨兵）。
// TestRunDownloadUnreachableDatabaseFailsClosed asserts the download mode
// fails closed with exactly ErrStartup for an unreachable database (the ping
// failure rolls the pool back without joining other sentinels).
func TestRunDownloadUnreachableDatabaseFailsClosed(t *testing.T) {
	nxClearPG(t)
	cfg := ppManualConfig(t)
	cfg.PostgresDSN = "postgres://binflow:pw@127.0.0.1:1/binflow?sslmode=disable&search_path=binflow"
	ready := false
	err := Run(context.Background(), cfg, func(string) { ready = true })
	if err != ErrStartup {
		t.Fatalf("error = %v (%v), want exactly ErrStartup", err, err)
	}
	if ready {
		t.Fatal("failed download run announced readiness")
	}
}

// TestRunDownloadPrecanceledContextJoinsShutdown 断言下载模式在启动早期
// 取消仅汇入 ErrStartup 与 context.Canceled，且绝不触碰数据库语义。
// TestRunDownloadPrecanceledContextJoinsShutdown asserts a download-mode
// cancellation in early startup joins only ErrStartup and context.Canceled.
func TestRunDownloadPrecanceledContextJoinsShutdown(t *testing.T) {
	nxClearPG(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, ppManualConfig(t), nil)
	if !errors.Is(err, ErrStartup) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want ErrStartup joined with context.Canceled", err)
	}
	if errors.Is(err, ErrConfiguration) {
		t.Fatalf("cancellation mislabeled as configuration failure: %v", err)
	}
}

// TestArtifactRouterDispatchesByMethodAndPrefix 断言 artifacts 路由：raw
// 前缀内 PUT 进入上传、其余方法进入下载；前缀外进入既有健康语义；下载为
// nil 时完全等同于健康处理器。
// TestArtifactRouterDispatchesByMethodAndPrefix asserts the artifacts
// router: inside the raw prefix a PUT reaches the upload handler while other
// methods reach the download handler; outside the prefix the existing health
// semantics apply; a nil download keeps the router exactly the health
// handler.
func TestArtifactRouterDispatchesByMethodAndPrefix(t *testing.T) {
	download := &rstSpyHandler{}
	upload := &rstSpyHandler{}
	router := newArtifactRouter(download, upload)

	put := httptest.NewRequest(http.MethodPut, "/artifactory/repoA/f.bin", nil)
	put.RequestURI = "/artifactory/repoA/f.bin"
	router.ServeHTTP(httptest.NewRecorder(), put)
	if len(upload.events) != 1 || upload.events[0] != "PUT /artifactory/repoA/f.bin" {
		t.Fatalf("upload events = %v, want the PUT dispatch", upload.events)
	}
	if len(download.events) != 0 {
		t.Fatalf("PUT reached download: %v", download.events)
	}

	get := httptest.NewRequest(http.MethodGet, "/artifactory/repoA/f.bin", nil)
	router.ServeHTTP(httptest.NewRecorder(), get)
	if len(download.events) != 1 || download.events[0] != "GET /artifactory/repoA/f.bin" {
		t.Fatalf("download events = %v, want the GET dispatch", download.events)
	}

	post := httptest.NewRequest(http.MethodPost, "/artifactory/repoA/f.bin", nil)
	router.ServeHTTP(httptest.NewRecorder(), post)
	if len(download.events) != 2 {
		t.Fatalf("POST did not reach download: %v", download.events)
	}

	outside := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, outside)
	if w.Code != http.StatusOK || len(download.events) != 2 || len(upload.events) != 1 {
		t.Fatalf("outside-prefix request = %d (downloads=%v uploads=%v), want the health handler untouched",
			w.Code, download.events, upload.events)
	}

	healthOnly := newArtifactRouter(nil, upload)
	putOnly := httptest.NewRequest(http.MethodPut, "/artifactory/repoA/f.bin", nil)
	putOnly.RequestURI = "/artifactory/repoA/f.bin"
	w2 := httptest.NewRecorder()
	healthOnly.ServeHTTP(w2, putOnly)
	if w2.Code != http.StatusNotFound || len(upload.events) != 1 {
		t.Fatalf("nil-download router = %d (uploads=%v), want pure health semantics", w2.Code, upload.events)
	}
}

// TestProcessConfigFormattingRedactsUploadSecrets 断言 UploadConfig 与
// Config 的 String/GoString 恒为脱敏固定文案，不携带任何 DSN/来源秘密。
// TestProcessConfigFormattingRedactsUploadSecrets asserts UploadConfig and
// Config String/GoString stay the fixed redacted text carrying no DSN or
// origin secrets.
func TestProcessConfigFormattingRedactsUploadSecrets(t *testing.T) {
	const canary = "canary-upload-secret-7f31"
	upload := UploadConfig{
		WriterDSN:      "postgres://writer:" + canary + "@127.0.0.1:5432/binflow?sslmode=disable",
		ReservationDSN: "postgres://alloc:" + canary + "@127.0.0.1:5432/binflow?sslmode=disable",
		PublicOrigin:   "https://" + canary + ".example.test",
	}
	if got := fmt.Sprint(upload); got != "upload configuration (redacted)" {
		t.Fatalf("UploadConfig String = %q", got)
	}
	if got := fmt.Sprintf("%#v", upload); got != "upload configuration (redacted)" {
		t.Fatalf("UploadConfig GoString = %q", got)
	}
	cfg := Config{Mode: ModeArtifacts, Upload: upload}
	for _, text := range []string{fmt.Sprint(cfg), fmt.Sprintf("%v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(text, canary) || strings.Contains(text, "postgres://") {
			t.Fatalf("config formatting leaked upload secrets: %q", text)
		}
	}
}

// TestReadPrivateConfigFileEmptyRejected 断言空受控文件（0600、大小 0）
// 被拒绝且错误不回显路径。
// TestReadPrivateConfigFileEmptyRejected asserts an empty controlled file
// (0600, zero size) rejects without echoing the path.
func TestReadPrivateConfigFileEmptyRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	data, err := readPrivateConfigFile(path)
	if data != nil || !errors.Is(err, ErrConfiguration) {
		t.Fatalf("empty file = (%v,%v), want rejection", data != nil, err)
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("error echoes the path: %v", err)
	}
}

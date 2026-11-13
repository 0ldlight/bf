// 真实文件系统上的配置装载集成测试（task-13）：进程内调用 process.LoadConfig，
// 以受控真实文件驱动 readPrivateConfigFile 的开放面（符号链接/FIFO/目录/
// 权限位/大小界）与 parseArtifactDocument/decode* 的严格解析分支。文件条件
// 由真实 inode 提供（O_NOFOLLOW/O_NONBLOCK 语义按内核真实行为裁决）；stat
// 错误、属主错配与读取错误需要错误注入文件系统或跨 uid chown，本机不可
// 生产，见任务报告。
// Real-filesystem integration tests for configuration loading (task-13):
// process.LoadConfig is called in-process, driving readPrivateConfigFile's
// open surface (symlink/FIFO/directory/permission bits/size bounds) and the
// strict parseArtifactDocument/decode* branches from controlled real files.
// File conditions come from real inodes (O_NOFOLLOW/O_NONBLOCK semantics are
// decided by the kernel); stat errors, owner mismatch and read errors need
// an error-injecting filesystem or a cross-uid chown, neither producible on
// this host — see the task report.
package postgresit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/process"
	"github.com/0ldlight/bf/internal/security"
)

// dlcValidDownloadJSON 返回九字段合法下载文档（DSN 形状合法但不指向真实
// 服务；本文件只测装载与解析，不触库）。
// dlcValidDownloadJSON returns the valid nine-field download document (the
// DSN is well-shaped but points nowhere real; this file only exercises
// loading and parsing, never the database).
func dlcValidDownloadJSON() map[string]any {
	return map[string]any{
		"listen_addr":            "127.0.0.1:0",
		"postgres_dsn":           "postgres://dlr-user:dlr-secret@127.0.0.1:5432/binflow_registry_test?sslmode=disable",
		"filestore_root":         "/tmp",
		"repo_key":               "dlrR",
		"challenge":              `Basic realm="dlr"`,
		"failure_threshold":      3,
		"failure_window_seconds": 60,
		"block_seconds":          60,
		"max_tracked_users":      64,
	}
}

// dlcWriteDoc 序列化文档到受控文件并按 mode 落权（json.Marshal 负责正确
// 转义；文档内容永不进日志）。
// dlcWriteDoc serializes the document to a controlled file with the mode
// applied (json.Marshal handles escaping; contents never logged).
func dlcWriteDoc(t *testing.T, doc map[string]any, mode os.FileMode) string {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("dlc: marshal doc: %v", err)
	}
	path := filepath.Join(t.TempDir(), "download-config.json")
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("dlc: write config: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("dlc: chmod config: %v", err)
	}
	return path
}

// dlcGetenv 返回指向 path 的 download/artifacts 环境；不修改进程环境。
// dlcGetenv returns the download/artifacts environment pointing at path;
// the process environment is never mutated.
func dlcGetenv(mode, path string) func(string) string {
	return func(key string) string {
		switch key {
		case "BINFLOW_MODE":
			return mode
		case "BINFLOW_DOWNLOAD_CONFIG":
			return path
		default:
			return ""
		}
	}
}

func TestLoadConfigDownloadAcceptsPrivateFile(t *testing.T) {
	path := dlcWriteDoc(t, dlcValidDownloadJSON(), 0o600)
	cfg, err := process.LoadConfig(dlcGetenv("download", path))
	if err != nil {
		t.Fatalf("dlc: LoadConfig: %v", err)
	}
	if cfg.Mode != process.ModeDownload || cfg.ListenAddr != "127.0.0.1:0" ||
		cfg.FilestoreRoot != "/tmp" || cfg.HTTP.RepoKey != "dlrR" ||
		cfg.HTTP.FailureThreshold != 3 || cfg.HTTP.FailureWindow != time.Minute ||
		cfg.HTTP.BlockDuration != time.Minute || cfg.HTTP.MaxTrackedUsers != 64 {
		t.Fatalf("dlc: parsed config = %+v, want the document fields", cfg)
	}
	if !strings.Contains(cfg.PostgresDSN, "sslmode=disable") || !strings.Contains(cfg.PostgresDSN, "binflow_registry_test") {
		t.Fatalf("dlc: parsed DSN shape mismatch (value not logged)")
	}
	// 0400 同样是合法私有模式。
	// 0400 is an equally valid private mode.
	path0400 := dlcWriteDoc(t, dlcValidDownloadJSON(), 0o400)
	if _, err := process.LoadConfig(dlcGetenv("download", path0400)); err != nil {
		t.Fatalf("dlc: LoadConfig 0400: %v", err)
	}
}

func TestLoadConfigArtifactsParsesUploadSection(t *testing.T) {
	doc := dlcValidDownloadJSON()
	doc["upload_writer_dsn"] = "postgres://writer:dlr-secret-w@127.0.0.1:5432/binflow_registry_test?sslmode=disable"
	doc["upload_reservation_dsn"] = "postgres://alloc:dlr-secret-a@127.0.0.1:5432/binflow_registry_test?sslmode=disable"
	doc["upload_public_origin"] = "https://example.test"
	doc["upload_max_body_bytes"] = 4096
	path := dlcWriteDoc(t, doc, 0o600)
	cfg, err := process.LoadConfig(dlcGetenv("artifacts", path))
	if err != nil {
		t.Fatalf("dlc: LoadConfig artifacts: %v", err)
	}
	if cfg.Mode != process.ModeArtifacts || cfg.Upload.MaxBodyBytes != 4096 ||
		cfg.Upload.PublicOrigin != "https://example.test" {
		t.Fatalf("dlc: artifacts upload section = %+v, want the document fields", cfg.Upload)
	}
}

func TestLoadConfigPrivateFileRejections(t *testing.T) {
	dir := t.TempDir()
	regular := dlcWriteDoc(t, dlcValidDownloadJSON(), 0o600)
	// 符号链接：最终分量符号链接被 O_NOFOLLOW 拒绝。
	// A symlink: the final-component symlink is rejected by O_NOFOLLOW.
	symlink := filepath.Join(dir, "config-symlink.json")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatalf("dlc: symlink: %v", err)
	}
	// 目录：打开成功但 inode 非普通文件（FIFO 用例在 unix 专属文件）。
	// A directory: opening succeeds but the inode is not a regular file
	// (the FIFO case lives in the unix-only file).
	directory := filepath.Join(dir, "config-dir")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("dlc: mkdir: %v", err)
	}
	// 超界文档：stat 大小超过 16KiB 上界。
	// An oversized document: the stat size exceeds the 16KiB bound.
	tooLarge := filepath.Join(dir, "config-large.json")
	big := []byte(`{"listen_addr":"` + strings.Repeat("a", 17*1024) + `"}`)
	if err := os.WriteFile(tooLarge, big, 0o600); err != nil {
		t.Fatalf("dlc: write large config: %v", err)
	}
	cases := []struct {
		name string
		path string
		mode os.FileMode
	}{
		{"symlink final component", symlink, 0o600},
		{"directory", directory, 0o700},
		{"group readable mode", dlcWriteDoc(t, dlcValidDownloadJSON(), 0o640), 0o640},
		{"world readable mode", dlcWriteDoc(t, dlcValidDownloadJSON(), 0o604), 0o604},
		// 注：setuid 位用例在能落位的主机（Linux）才有意义；本 macOS 主机
		// chmod 会剥离 setuid 位，无法生产该 inode 状态。
		// Note: a setuid-bit case only exists on hosts where chmod can set
		// it (Linux); this macOS host strips the bit, so that inode state
		// is not producible here.
		{"empty file", func() string {
			p := filepath.Join(dir, "config-empty.json")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatalf("dlc: write empty: %v", err)
			}
			return p
		}(), 0o600},
		{"oversized document", tooLarge, 0o600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := process.LoadConfig(dlcGetenv("download", tc.path))
			if err == nil {
				t.Fatalf("dlc: %s accepted", tc.name)
			}
			if !errors.Is(err, process.ErrConfiguration) {
				t.Fatalf("dlc: %s error = %v, want ErrConfiguration", tc.name, err)
			}
			if strings.Contains(err.Error(), tc.path) {
				t.Fatalf("dlc: %s error echoes the path", tc.name)
			}
		})
	}
}

func TestLoadConfigDownloadPathGuards(t *testing.T) {
	// 缺路径与相对路径在读取文件之前拒绝。
	// A missing path and a relative path are rejected before any file read.
	if _, err := process.LoadConfig(dlcGetenv("download", "")); !errors.Is(err, process.ErrConfiguration) {
		t.Fatalf("dlc: empty path error = %v", err)
	}
	if _, err := process.LoadConfig(dlcGetenv("download", "relative/config.json")); !errors.Is(err, process.ErrConfiguration) {
		t.Fatalf("dlc: relative path error = %v", err)
	}
	// download 模式拒绝 BINFLOW_LISTEN_ADDR 竞争来源。
	// Download mode rejects the competing BINFLOW_LISTEN_ADDR source.
	getenv := func(key string) string {
		switch key {
		case "BINFLOW_MODE":
			return "download"
		case "BINFLOW_DOWNLOAD_CONFIG":
			return dlcWriteDoc(t, dlcValidDownloadJSON(), 0o600)
		case "BINFLOW_LISTEN_ADDR":
			return "127.0.0.1:0"
		default:
			return ""
		}
	}
	if _, err := process.LoadConfig(getenv); !errors.Is(err, process.ErrConfiguration) {
		t.Fatalf("dlc: competing listen source error = %v", err)
	}
}

func TestLoadConfigDocumentParseRejections(t *testing.T) {
	base := dlcValidDownloadJSON()
	drop := func(key string) map[string]any {
		doc := map[string]any{}
		for k, v := range base {
			if k != key {
				doc[k] = v
			}
		}
		return doc
	}
	wrongTypes := base
	wrongTypes["repo_key"] = 42
	emptyString := base
	emptyString["challenge"] = ""
	fraction := base
	fraction["failure_threshold"] = 3.5
	// 直接以原始 JSON 表达整数溢出与重复键/尾随内容。
	// Raw JSON expresses integer overflow, duplicate keys and trailing
	// content directly.
	rawCases := []struct {
		name string
		body string
	}{
		{"integer overflow", `{"listen_addr":"127.0.0.1:0","postgres_dsn":"postgres://u:p@127.0.0.1:5432/d?sslmode=disable","filestore_root":"/tmp","repo_key":"dlrR","challenge":"c","failure_threshold":99999999999999999999,"failure_window_seconds":60,"block_seconds":60,"max_tracked_users":64}`},
		{"integer above int32 bound", `{"listen_addr":"127.0.0.1:0","postgres_dsn":"postgres://u:p@127.0.0.1:5432/d?sslmode=disable","filestore_root":"/tmp","repo_key":"dlrR","challenge":"c","failure_threshold":3000000000,"failure_window_seconds":60,"block_seconds":60,"max_tracked_users":64}`},
		{"exponent notation integer", `{"listen_addr":"127.0.0.1:0","postgres_dsn":"postgres://u:p@127.0.0.1:5432/d?sslmode=disable","filestore_root":"/tmp","repo_key":"dlrR","challenge":"c","failure_threshold":3e0,"failure_window_seconds":60,"block_seconds":60,"max_tracked_users":64}`},
		{"bad string escape", `{"listen_addr":"127.0.0.1:0","postgres_dsn":"postgres://u:p@127.0.0.1:5432/d?sslmode=disable","filestore_root":"/tmp","repo_key":"dlrR","challenge":"BadEscape\x","failure_threshold":3,"failure_window_seconds":60,"block_seconds":60,"max_tracked_users":64}`},
		{"duplicate field", `{"listen_addr":"127.0.0.1:0","listen_addr":"127.0.0.1:1","postgres_dsn":"postgres://u:p@127.0.0.1:5432/d?sslmode=disable","filestore_root":"/tmp","repo_key":"dlrR","challenge":"c","failure_threshold":3,"failure_window_seconds":60,"block_seconds":60,"max_tracked_users":64}`},
		{"trailing second document", `{"listen_addr":"127.0.0.1:0","postgres_dsn":"postgres://u:p@127.0.0.1:5432/d?sslmode=disable","filestore_root":"/tmp","repo_key":"dlrR","challenge":"c","failure_threshold":3,"failure_window_seconds":60,"block_seconds":60,"max_tracked_users":64}{"repo_key":"x"}`},
		{"not an object", `["listen_addr"]`},
		{"syntax error", `{"listen_addr":`},
	}
	structured := []struct {
		name string
		doc  map[string]any
	}{
		{"unknown field", func() map[string]any {
			doc := base
			doc["unexpected_key"] = "x"
			return doc
		}()},
		{"missing field", drop("block_seconds")},
		{"string field not string", wrongTypes},
		{"empty string field", emptyString},
		{"fractional integer", fraction},
	}
	for _, tc := range structured {
		t.Run(tc.name, func(t *testing.T) {
			path := dlcWriteDoc(t, tc.doc, 0o600)
			if _, err := process.LoadConfig(dlcGetenv("download", path)); !errors.Is(err, process.ErrConfiguration) {
				t.Fatalf("dlc: %s error = %v, want ErrConfiguration", tc.name, err)
			}
		})
	}
	for _, tc := range rawCases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("dlc: write raw doc: %v", err)
			}
			if _, err := process.LoadConfig(dlcGetenv("download", path)); !errors.Is(err, process.ErrConfiguration) {
				t.Fatalf("dlc: %s error = %v, want ErrConfiguration", tc.name, err)
			}
		})
	}
}

// dlcValidHTTP 返回构造 Run 级配置所需的合法 HTTP 画像。
// dlcValidHTTP returns the valid HTTP profile for Run-level configs.
func dlcValidHTTP() security.HTTPConfig {
	return security.HTTPConfig{
		RepoKey: "dlrR", Challenge: `Basic realm="dlr"`, FailureThreshold: 3,
		FailureWindow: time.Minute, BlockDuration: time.Minute, MaxTrackedUsers: 64,
	}
}

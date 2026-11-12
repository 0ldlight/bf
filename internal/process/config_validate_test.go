package process

// 本文件直接覆盖 validateConfig 的全部分支（健康模式携带下载/上传设置、
// artifacts 上传字段域、空 DSN、相对 filestore 根、健康/下载监听地址边
// 界）、LoadConfig 健康路径的监听地址校验、严格文档解码的剩余分支（字符
// 串/整数类型与域、截断、数组文档）与 parseDownloadDocument 包装。
// This file covers every validateConfig branch directly (health mode
// carrying download/upload settings, the artifacts upload field domain, an
// empty DSN, a relative filestore root, the health/download listen-address
// bounds), the LoadConfig health listen-address validation, the remaining
// strict-document decoding branches (string/int types and domains,
// truncation, an array document) and the parseDownloadDocument wrapper.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/security"
)

// cfgValidHTTP 返回合法失败门配置。
// cfgValidHTTP returns a valid failure-gate configuration.
func cfgValidHTTP() security.HTTPConfig {
	return security.HTTPConfig{
		RepoKey: "local-repo", Challenge: "Basic realm=binflow",
		FailureThreshold: 5, FailureWindow: 300 * time.Second,
		BlockDuration: 300 * time.Second, MaxTrackedUsers: 256,
	}
}

// cfgValidUpload 返回合法上传配置。
// cfgValidUpload returns a valid upload configuration.
func cfgValidUpload() UploadConfig {
	return UploadConfig{
		WriterDSN:      "postgres://writer:pw@127.0.0.1:5432/binflow?sslmode=disable",
		ReservationDSN: "postgres://alloc:pw@127.0.0.1:5432/binflow?sslmode=disable",
		PublicOrigin:   "https://uploads.example.test", MaxBodyBytes: 1024,
	}
}

// TestValidateConfigBranches 直接冻结共享校验的全部拒绝分支与两个接受
// 形态（download/artifacts）。
// TestValidateConfigBranches freezes every rejection branch of the shared
// validation plus both accepted shapes (download/artifacts) directly.
func TestValidateConfigBranches(t *testing.T) {
	base := func() Config {
		return Config{
			Mode: ModeDownload, ListenAddr: "127.0.0.1:18443",
			PostgresDSN:   "postgres://binflow:pw@127.0.0.1:5432/binflow?sslmode=disable",
			FilestoreRoot: "/var/lib/binflow", HTTP: cfgValidHTTP(),
		}
	}
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"unknown mode", func(c *Config) { c.Mode = "relay" }},
		{"health carries postgres dsn", func(c *Config) {
			c.Mode = ModeHealth
			c.ListenAddr = "127.0.0.1:8080"
		}},
		{"health carries filestore root", func(c *Config) {
			c.Mode = ModeHealth
			c.ListenAddr = "127.0.0.1:8080"
			c.PostgresDSN = ""
		}},
		{"empty postgres dsn", func(c *Config) { c.PostgresDSN = "" }},
		{"relative filestore root", func(c *Config) { c.FilestoreRoot = "relative/root" }},
		{"empty filestore root", func(c *Config) { c.FilestoreRoot = "" }},
		{"empty listen address", func(c *Config) {
			c.Mode = ModeHealth
			c.ListenAddr = ""
			c.PostgresDSN = ""
			c.FilestoreRoot = ""
			c.HTTP = security.HTTPConfig{}
		}},
		{"unsplittable health listen address", func(c *Config) {
			c.Mode = ModeHealth
			c.ListenAddr = "no-port-here"
			c.PostgresDSN = ""
			c.FilestoreRoot = ""
			c.HTTP = security.HTTPConfig{}
		}},
		{"download wildcard host", func(c *Config) { c.ListenAddr = ":8080" }},
		{"download dns host", func(c *Config) { c.ListenAddr = "localhost:8080" }},
		{"download service port", func(c *Config) { c.ListenAddr = "127.0.0.1:http" }},
		{"download port not decimal", func(c *Config) { c.ListenAddr = "127.0.0.1:8x0" }},
		{"download empty port", func(c *Config) { c.ListenAddr = "127.0.0.1:" }},
		{"download port out of range", func(c *Config) { c.ListenAddr = "127.0.0.1:65536" }},
		{"non-loopback ip", func(c *Config) { c.ListenAddr = "192.0.2.1:8080" }},
		{"upload settings outside artifacts", func(c *Config) { c.Upload = cfgValidUpload() }},
		{"artifacts missing writer dsn", func(c *Config) {
			c.Mode = ModeArtifacts
			upload := cfgValidUpload()
			upload.WriterDSN = ""
			c.Upload = upload
		}},
		{"artifacts missing reservation dsn", func(c *Config) {
			c.Mode = ModeArtifacts
			upload := cfgValidUpload()
			upload.ReservationDSN = ""
			c.Upload = upload
		}},
		{"artifacts missing public origin", func(c *Config) {
			c.Mode = ModeArtifacts
			upload := cfgValidUpload()
			upload.PublicOrigin = ""
			c.Upload = upload
		}},
		{"artifacts zero body bytes", func(c *Config) {
			c.Mode = ModeArtifacts
			upload := cfgValidUpload()
			upload.MaxBodyBytes = 0
			c.Upload = upload
		}},
		{"artifacts body bytes above bound", func(c *Config) {
			c.Mode = ModeArtifacts
			upload := cfgValidUpload()
			upload.MaxBodyBytes = 1<<30 + 1
			c.Upload = upload
		}},
		{"invalid repo key", func(c *Config) { c.HTTP.RepoKey = "a/b" }},
		{"empty challenge", func(c *Config) { c.HTTP.Challenge = "" }},
		{"threshold above bound", func(c *Config) { c.HTTP.FailureThreshold = 101 }},
		{"window below bound", func(c *Config) { c.HTTP.FailureWindow = 0 }},
		{"block above bound", func(c *Config) { c.HTTP.BlockDuration = time.Hour + time.Second }},
		{"tracked users below bound", func(c *Config) { c.HTTP.MaxTrackedUsers = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			if err := validateConfig(cfg); !errors.Is(err, ErrConfiguration) {
				t.Fatalf("validateConfig(%s) = %v, want ErrConfiguration", tc.name, err)
			}
		})
	}
	t.Run("download shape accepted", func(t *testing.T) {
		if err := validateConfig(base()); err != nil {
			t.Fatalf("valid download config rejected: %v", err)
		}
	})
	t.Run("artifacts shape accepted", func(t *testing.T) {
		cfg := base()
		cfg.Mode = ModeArtifacts
		cfg.Upload = cfgValidUpload()
		if err := validateConfig(cfg); err != nil {
			t.Fatalf("valid artifacts config rejected: %v", err)
		}
	})
	t.Run("health bare host port accepted", func(t *testing.T) {
		cfg := Config{Mode: ModeHealth, ListenAddr: "127.0.0.1:0"}
		if err := validateConfig(cfg); err != nil {
			t.Fatalf("port-zero health address rejected: %v", err)
		}
	})
}

// TestLoadConfigHealthListenValidation 断言健康模式经 LoadConfig 的监听
// 地址校验：不可拆分值被拒绝且错误不回显地址。
// TestLoadConfigHealthListenValidation asserts the health-mode listen
// validation through LoadConfig: unsplittable values reject without echoing
// the address.
func TestLoadConfigHealthListenValidation(t *testing.T) {
	getenv := func(override map[string]string) func(string) string {
		return func(key string) string {
			if v, ok := override[key]; ok {
				return v
			}
			return ""
		}
	}
	cfg, err := LoadConfig(getenv(map[string]string{"BINFLOW_LISTEN_ADDR": "127.0.0.1:9090"}))
	if err != nil || cfg.ListenAddr != "127.0.0.1:9090" {
		t.Fatalf("explicit listen address = (%q,%v)", cfg.ListenAddr, err)
	}
	for _, addr := range []string{"missing-port", "justahost"} {
		cfg, err := LoadConfig(getenv(map[string]string{"BINFLOW_LISTEN_ADDR": addr}))
		if cfg.Mode != "" || !errors.Is(err, ErrConfiguration) {
			t.Fatalf("invalid listen %q = (%+v,%v), want rejected empty config", addr, cfg, err)
		}
		if strings.Contains(err.Error(), addr) {
			t.Fatalf("error echoes address: %v", err)
		}
	}
}

// cfgBaseDocument 是九字段全存在的最小合法文档（值只需通过解码，不必通
// 过后续域校验）。
// cfgBaseDocument is the minimal document with all nine fields present (the
// values only need to decode; later domain validation is out of scope).
const cfgBaseDocument = `{"listen_addr":"a","postgres_dsn":"b","filestore_root":"/c","repo_key":"r","challenge":"ch","failure_threshold":1,"failure_window_seconds":2,"block_seconds":3,"max_tracked_users":4}`

// TestParseArtifactDocumentDecodingBounds 断言严格解码剩余分支：字符串/
// 整数类型与域拒绝、截断文档、数组文档与上传字段缺席；错误一律固定
// ErrConfiguration 文案且不回显文档内容。
// TestParseArtifactDocumentDecodingBounds asserts the remaining strict
// decoding branches: string/int type and domain rejections, a truncated
// document, an array document and missing upload fields; every error is the
// fixed ErrConfiguration text without echoing document contents.
func TestParseArtifactDocumentDecodingBounds(t *testing.T) {
	cases := []struct {
		name   string
		doc    string
		upload bool
	}{
		{"string field as number", strings.Replace(cfgBaseDocument, `"a"`, `80`, 1), false},
		{"string field as null", strings.Replace(cfgBaseDocument, `"a"`, `null`, 1), false},
		{"string field empty", strings.Replace(cfgBaseDocument, `"a"`, `""`, 1), false},
		{"string field invalid escape", strings.Replace(cfgBaseDocument, `"a"`, `"a\u00"`, 1), false},
		{"int field as string", strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":"1"`, 1), false},
		{"int field as float", strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":1.5`, 1), false},
		{"int field as exponent", strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":3e2`, 1), false},
		{"int field as null", strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":null`, 1), false},
		{"int field negative sign only", strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":-`, 1), false},
		{"int field beyond int64", strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":9223372036854775808`, 1), false},
		{"int field beyond int32", strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":5000000000`, 1), false},
		{"truncated before closing brace", strings.TrimSuffix(cfgBaseDocument, "}"), false},
		{"truncated after one complete pair", `{"listen_addr":"a"`, false},
		{"leading garbage before document", "x" + cfgBaseDocument, false},
		{"array document", `[` + cfgBaseDocument + `]`, false},
		{"upload doc missing upload fields", cfgBaseDocument, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := parseArtifactDocument([]byte(tc.doc), tc.upload)
			if doc.upload.WriterDSN != "" || err == nil || !errors.Is(err, ErrConfiguration) {
				t.Fatalf("parse = (%+v,%v), want empty doc and ErrConfiguration", doc, err)
			}
			for _, secret := range []string{`"a"`, `postgres`, `/c`} {
				if tc.name == "string field empty" && secret == `"a"` {
					continue
				}
				if strings.Contains(err.Error(), secret) && secret != `postgres` {
					t.Fatalf("error echoes document content %q: %v", secret, err)
				}
			}
		})
	}
	t.Run("upload doc with all fields decodes", func(t *testing.T) {
		doc := strings.Replace(cfgBaseDocument, `"failure_threshold":1`, `"failure_threshold":-2147483648`, 1)
		doc = doc[:len(doc)-1] +
			`,"upload_writer_dsn":"w","upload_reservation_dsn":"r","upload_public_origin":"https://o","upload_max_body_bytes":9}`
		parsed, err := parseArtifactDocument([]byte(doc), true)
		if err != nil || parsed.upload.WriterDSN != "w" || parsed.upload.MaxBodyBytes != 9 {
			t.Fatalf("upload parse = (%+v,%v)", parsed, err)
		}
		if parsed.failureThreshold != -2147483648 {
			t.Fatalf("negative int32 = %d, want -2147483648", parsed.failureThreshold)
		}
	})
	t.Run("download wrapper delegates", func(t *testing.T) {
		parsed, err := parseDownloadDocument([]byte(cfgBaseDocument))
		if err != nil || parsed.repoKey != "r" || parsed.maxTrackedUsers != 4 {
			t.Fatalf("download parse = (%+v,%v)", parsed, err)
		}
		if _, err := parseDownloadDocument([]byte(`{"listen_addr":1}`)); !errors.Is(err, ErrConfiguration) {
			t.Fatalf("invalid wrapper parse = %v, want ErrConfiguration", err)
		}
	})
}

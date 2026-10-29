package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nxDocument() string {
	return `{"listen_addr":"127.0.0.1:0","postgres_dsn":"postgres://user:secret@127.0.0.1:5432/db?sslmode=disable","filestore_root":"/tmp/binflow-contract","repo_key":"repo","challenge":"Basic realm=contract","failure_threshold":3,"failure_window_seconds":30,"block_seconds":60,"max_tracked_users":32}`
}
func nxLoad(t *testing.T, data string, mode os.FileMode) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal("write fixture")
	}
	return LoadConfig(func(key string) string {
		switch key {
		case envMode:
			return ModeDownload
		case envDownloadConfig:
			return path
		}
		return ""
	})
}
func TestNXPrivateDocumentContract(t *testing.T) {
	for _, mode := range []os.FileMode{0400, 0600} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			cfg, err := nxLoad(t, nxDocument(), mode)
			if err != nil {
				t.Fatal("valid private document rejected")
			}
			if cfg.Mode != ModeDownload || cfg.ListenAddr != "127.0.0.1:0" || cfg.HTTP.RepoKey != "repo" || cfg.HTTP.FailureWindow != 30*time.Second || cfg.HTTP.BlockDuration != time.Minute || cfg.HTTP.MaxTrackedUsers != 32 || cfg.HTTP.FailureThreshold != 3 || cfg.PostgresDSN == "" {
				t.Fatal("document roundtrip changed fields")
			}
		})
	}
	cases := map[string]string{
		"duplicate": strings.Replace(nxDocument(), `"failure_threshold":3`, `"failure_threshold":3,"failure_threshold":4`, 1),
		"unknown":   strings.Replace(nxDocument(), `"failure_threshold":3`, `"extra":3,"failure_threshold":3`, 1),
		"trailing":  nxDocument() + ` {}`,
		"missing":   strings.Replace(nxDocument(), `,"max_tracked_users":32`, ``, 1),
		"float":     strings.Replace(nxDocument(), `"failure_threshold":3`, `"failure_threshold":3.0`, 1),
		"overflow":  strings.Replace(nxDocument(), `"failure_threshold":3`, `"failure_threshold":9223372036854775808`, 1),
		"threshold": strings.Replace(nxDocument(), `"failure_threshold":3`, `"failure_threshold":101`, 1),
		"duration":  strings.Replace(nxDocument(), `"block_seconds":60`, `"block_seconds":3601`, 1),
		"capacity":  strings.Replace(nxDocument(), `"max_tracked_users":32`, `"max_tracked_users":0`, 1),
		"wildcard":  strings.Replace(nxDocument(), `127.0.0.1:0`, `:8080`, 1),
		"dns":       strings.Replace(nxDocument(), `127.0.0.1:0`, `localhost:8080`, 1),
		"external":  strings.Replace(nxDocument(), `127.0.0.1:0`, `192.0.2.1:8080`, 1),
		"null":      `null`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := nxLoad(t, data, 0600)
			if !errors.Is(err, ErrConfiguration) {
				t.Fatal("invalid document did not fail closed")
			}
			if cfg.PostgresDSN != "" || cfg.Mode != "" {
				t.Fatal("failed load returned partial configuration")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "postgres://") {
				t.Fatal("configuration error exposed private input")
			}
		})
	}
	if _, err := nxLoad(t, nxDocument(), 0644); !errors.Is(err, ErrConfiguration) {
		t.Fatal("public permissions accepted")
	}
}
func nxClearPG(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") {
			value := os.Getenv(key)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal("clear test environment")
			}
			t.Cleanup(func() { _ = os.Setenv(key, value) })
		}
	}
}
func TestNXDSNFiniteContract(t *testing.T) {
	nxClearPG(t)
	base := "postgres://user:p%40ss%3Aword@127.0.0.1:5432/db?sslmode=disable"
	for _, raw := range []string{base, base + "&search_path=fixture_1", "postgresql://user:secret@[::1]:5432/db?sslmode=disable", "postgres://user:secret@db.example:5432/db?sslmode=verify-full"} {
		cfg, err := parsePostgresDSN(raw)
		if err != nil {
			t.Fatal("valid finite DSN rejected")
		}
		if len(cfg.Fallbacks) != 0 || cfg.ConnectTimeout != 10*time.Second || cfg.RuntimeParams["application_name"] != "binflow-download" {
			t.Fatal("DSN weakened connection bounds")
		}
		if strings.Contains(raw, "verify-full") && (cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify) {
			t.Fatal("verified TLS profile weakened")
		}
		if raw == base && cfg.Password != "p@ss:word" {
			t.Fatal("encoded password changed")
		}
	}
	rejects := []string{
		"host=127.0.0.1 user=user password=secret dbname=db",
		"postgres://user:secret@127.0.0.1:5432/db",
		base + "&sslmode=disable", base + "&passfile=/private/secret", base + "&service=x", base + "&search_path=a&search_path=b",
		strings.Replace(base, "sslmode=disable", "sslmode=prefer", 1), strings.Replace(base, "127.0.0.1", "db.example", 1),
		strings.Replace(base, "127.0.0.1:5432", "127.0.0.1:5432,127.0.0.2:5432", 1),
		strings.Replace(base, "user:p%40ss%3Aword", "user", 1), base + "#private",
	}
	for i, raw := range rejects {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			cfg, err := parsePostgresDSN(raw)
			if err == nil || cfg != nil {
				t.Fatal("unsupported DSN accepted")
			}
			if err != errInvalidPostgresDSN || errors.Unwrap(err) != nil {
				t.Fatal("raw driver error retained")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "postgres://") {
				t.Fatal("DSN error exposed input")
			}
		})
	}
	cfg := Config{Mode: ModeDownload, PostgresDSN: base, FilestoreRoot: "/private/secret"}
	for _, s := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg)} {
		if strings.Contains(s, "p%40ss") || strings.Contains(s, "/private/secret") || strings.Contains(s, "postgres://") {
			t.Fatal("configuration formatting exposed secrets")
		}
	}
}
func TestNXRunEarlyFailures(t *testing.T) {
	cfg, err := nxLoad(t, nxDocument(), 0600)
	if err != nil {
		t.Fatal("fixture config rejected")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	ready := func(string) { t.Error("failed Run announced readiness") }
	if err := Run(canceled, cfg, ready); !errors.Is(err, ErrStartup) || !errors.Is(err, context.Canceled) {
		t.Fatal("precanceled startup not rejected")
	}
	if err := Run(context.Background(), Config{Mode: ModeHealth, ListenAddr: "127.0.0.1:0", PostgresDSN: "secret"}, ready); !errors.Is(err, ErrConfiguration) {
		t.Fatal("health accepted download settings")
	}
	t.Setenv("PGNX_CONTRACT", "secret")
	if err := Run(context.Background(), cfg, ready); !errors.Is(err, ErrConfiguration) {
		t.Fatal("download accepted ambient PG environment")
	}
}
func TestNXActualHealthRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Mode: ModeHealth, ListenAddr: "127.0.0.1:0"}, func(addr string) { ready <- addr })
	}()
	var addr string
	select {
	case addr = <-ready:
	case err := <-done:
		t.Fatalf("health stopped before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("health readiness timed out")
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, tc := range []struct {
		method string
		status int
	}{{"GET", 200}, {"HEAD", 200}, {"POST", 405}} {
		req, err := http.NewRequest(tc.method, "http://"+addr+"/healthz", nil)
		if err != nil {
			t.Fatal("construct health request")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal("health request failed")
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != tc.status {
			t.Fatal("health response contract changed")
		}
		if tc.method == "HEAD" && len(body) != 0 {
			t.Fatal("HEAD wrote a body")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("graceful health cancellation failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("health shutdown timed out")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal("health listener not released")
	}
	ln.Close()
}

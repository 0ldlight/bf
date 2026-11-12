package process

// 本文件覆盖上传三连接身份策略：sameUploadDatabase 的同库判定真值表与
// openUpload 在触库之前的全部拒绝（非法 DSN、模式名策略、跨库写方/预留
// 方、身份重合）。
// This file covers the three-connection upload identity policy: the
// sameUploadDatabase truth table and every openUpload rejection that happens
// before touching the database (illegal DSNs, the schema-name policy,
// cross-database writer/allocator and shared identities).

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// udsConfig 解析一个合法 DSN；失败即终止测试。
// udsConfig parses one legal DSN, failing the test on error.
func udsConfig(t *testing.T, raw string) *pgx.ConnConfig {
	t.Helper()
	cfg, err := parsePostgresDSN(raw)
	if err != nil {
		t.Fatalf("parsePostgresDSN(%q): %v", raw, err)
	}
	return cfg
}

// TestSameUploadDatabaseTruthTable 冻结同库判定：主机/端口/数据库与
// search_path 全等才为真，任一差异为假。
// TestSameUploadDatabaseTruthTable freezes the same-database decision: true
// only when host, port, database and search_path all agree; any difference
// is false.
func TestSameUploadDatabaseTruthTable(t *testing.T) {
	reader := udsConfig(t, "postgres://r:p@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow")
	writer := udsConfig(t, "postgres://w:p@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow")
	if !sameUploadDatabase(reader, writer, "binflow") {
		t.Fatal("identical endpoints reported different databases")
	}
	for name, mutate := range map[string]func(*pgx.ConnConfig){
		"host":           func(c *pgx.ConnConfig) { c.Host = "127.0.0.2" },
		"port":           func(c *pgx.ConnConfig) { c.Port = 5433 },
		"database":       func(c *pgx.ConnConfig) { c.Database = "other" },
		"search path":    func(c *pgx.ConnConfig) { c.RuntimeParams["search_path"] = "other" },
		"no search path": func(c *pgx.ConnConfig) { delete(c.RuntimeParams, "search_path") },
	} {
		changed := *writer
		changed.RuntimeParams = map[string]string{"search_path": "binflow"}
		mutate(&changed)
		if sameUploadDatabase(reader, &changed, "binflow") {
			t.Fatalf("%s difference accepted as the same database", name)
		}
	}
}

// TestOpenUploadAssemblyReachesWriter 断言身份策略满足后装配真正触达写方
// 连接：无数据库时以写方检查失败关闭（此处为连接拒绝），且失败路径不返
// 回处理器/关闭函数。
// TestOpenUploadAssemblyReachesWriter asserts that once the identity policy
// is satisfied the assembly really reaches the writer connection: without a
// database it fails closed at the writer check (connection refused here),
// and the failure path returns neither handler nor close function.
func TestOpenUploadAssemblyReachesWriter(t *testing.T) {
	// 全部身份互异且指向本机不可达端口，保证任何阶段都在拨号处失败。
	// All identities distinct and aimed at an unreachable local port so
	// every stage fails exactly at dialing.
	const unreachable = "postgres://%s:pw@127.0.0.1:1/binflow?sslmode=disable&search_path=binflow"
	cfg := udsArtifactsConfig(fmt.Sprintf(unreachable, "writer"), fmt.Sprintf(unreachable, "alloc"))
	cfg.PostgresDSN = fmt.Sprintf(unreachable, "reader")
	reader := udsConfig(t, cfg.PostgresDSN)
	handler, closeFn, err := openUpload(context.Background(), cfg, reader, nil, nil)
	if handler != nil || closeFn != nil {
		t.Fatalf("unreachable assembly returned (%v,%v)", handler != nil, closeFn != nil)
	}
	if err == nil || errors.Is(err, ErrConfiguration) {
		t.Fatalf("unreachable assembly error = %v, want a dial failure (not configuration)", err)
	}
}

// udsArtifactsConfig 构造带完整上传设置的 artifacts 配置。
// udsArtifactsConfig builds an artifacts configuration with complete upload
// settings.
func udsArtifactsConfig(writer, reservation string) Config {
	return Config{
		Mode: ModeArtifacts, ListenAddr: "127.0.0.1:18443",
		PostgresDSN:   "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow",
		FilestoreRoot: "/var/lib/binflow", HTTP: cfgValidHTTP(),
		Upload: UploadConfig{
			WriterDSN: writer, ReservationDSN: reservation,
			PublicOrigin: "https://uploads.example.test", MaxBodyBytes: 1024,
		},
	}
}

// TestOpenUploadIdentityPolicy 断言 openUpload 在任何连接尝试之前的拒绝：
// 非法写方/预留方 DSN、模式名策略（大写、pg_ 前缀）与跨库/身份重合，全部
// 返回固定哨兵且绝不触库。
// TestOpenUploadIdentityPolicy asserts the openUpload rejections before any
// connection attempt: illegal writer/reservation DSNs, the schema-name policy
// (uppercase, pg_ prefix) and cross-database/shared identities, all returning
// the fixed sentinels without touching the database.
func TestOpenUploadIdentityPolicy(t *testing.T) {
	const writer = "postgres://writer:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow"
	const alloc = "postgres://alloc:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow"
	cases := []struct {
		name      string
		readerDSN string
		writerDSN string
		allocDSN  string
		want      error
	}{
		{"illegal writer dsn", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", "not-a-dsn", alloc, errInvalidPostgresDSN},
		{"illegal reservation dsn", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", writer, "host=x", errInvalidPostgresDSN},
		{"uppercase schema", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=Binflow", writer, alloc, ErrConfiguration},
		{"pg_ schema", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=pg_binflow", writer, alloc, ErrConfiguration},
		{"writer other database", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", "postgres://writer:pw@127.0.0.1:5432/other?sslmode=disable&search_path=binflow", alloc, ErrConfiguration},
		{"writer other host", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", "postgres://writer:pw@127.0.0.2:5432/binflow?sslmode=disable&search_path=binflow", alloc, ErrConfiguration},
		{"writer other port", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", "postgres://writer:pw@127.0.0.1:5433/binflow?sslmode=disable&search_path=binflow", alloc, ErrConfiguration},
		{"writer without schema", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", "postgres://writer:pw@127.0.0.1:5432/binflow?sslmode=disable", alloc, ErrConfiguration},
		{"writer shares reader identity", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", alloc, ErrConfiguration},
		{"allocator shares reader identity", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", writer, "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", ErrConfiguration},
		{"allocator shares writer identity", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", writer, "postgres://writer:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", ErrConfiguration},
		{"allocator other database", "postgres://reader:pw@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow", writer, "postgres://alloc:pw@127.0.0.1:5432/other?sslmode=disable&search_path=binflow", ErrConfiguration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := udsConfig(t, tc.readerDSN)
			cfg := udsArtifactsConfig(tc.writerDSN, tc.allocDSN)
			handler, closeFn, err := openUpload(context.Background(), cfg, reader, nil, nil)
			if handler != nil || closeFn != nil {
				t.Fatalf("openUpload returned (%v,%v) alongside its error", handler != nil, closeFn != nil)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("openUpload error = %v, want %v", err, tc.want)
			}
		})
	}
}

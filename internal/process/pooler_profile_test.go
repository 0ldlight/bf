// 独立连接画像契约：显式 exec、默认行为、严格拒绝及 TLS/错误隐藏边界。
// Independent connection-profile contracts: explicit exec, unchanged defaults,
// strict rejection, TLS and fixed-error boundaries.
package process

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// qpeExecKey 是本契约唯一新增的可选 query 键名（精确小写）。
// qpeExecKey is the single newly allowed optional query key name (exact
// lowercase).
const qpeExecKey = "default_query_exec_mode"

// qpeDefaultCacheCapacity 是 pgx 既有默认缓存容量，键缺席时必须原样保持。
// qpeDefaultCacheCapacity is pgx's existing default cache capacity that must
// stay untouched when the key is absent.
const qpeDefaultCacheCapacity = 512

// qpeAssertExecProfile 断言显式 exec 的连接画像：exec 执行模式、两个缓存容量
// 均为 0，且驱动键已被消费为 DefaultQueryExecMode 而非残留为服务器运行参数
// （残留意味着驱动内部旋钮被当作每次连接的启动参数外发）。
// qpeAssertExecProfile asserts the explicit-exec connection profile: exec
// execution mode, both cache capacities at 0, and the driver key consumed
// into DefaultQueryExecMode instead of surviving as a server runtime
// parameter (a survivor would forward a driver-internal knob as a startup
// parameter on every connection).
func qpeAssertExecProfile(t *testing.T, cc *pgx.ConnConfig) {
	t.Helper()
	if cc.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("default query exec mode=%d want pgx.QueryExecModeExec", cc.DefaultQueryExecMode)
	}
	if cc.StatementCacheCapacity != 0 {
		t.Fatalf("statement cache capacity=%d want 0 with explicit exec", cc.StatementCacheCapacity)
	}
	if cc.DescriptionCacheCapacity != 0 {
		t.Fatalf("description cache capacity=%d want 0 with explicit exec", cc.DescriptionCacheCapacity)
	}
	qpeAssertDriverKnobsConsumed(t, cc)
}

// qpeAssertCacheDefaults 断言键缺席时的既有默认画像：cache_statement 执行
// 模式与 512/512 缓存，缓存容量只能由契约固定，绝不来自输入。
// qpeAssertCacheDefaults asserts the untouched default profile when the key is
// absent: cache_statement execution mode with 512/512 caches; capacities are
// contract-fixed and never sourced from input.
func qpeAssertCacheDefaults(t *testing.T, cc *pgx.ConnConfig) {
	t.Helper()
	if cc.DefaultQueryExecMode != pgx.QueryExecModeCacheStatement {
		t.Fatalf("default query exec mode=%d want pgx.QueryExecModeCacheStatement", cc.DefaultQueryExecMode)
	}
	if cc.StatementCacheCapacity != qpeDefaultCacheCapacity {
		t.Fatalf("statement cache capacity=%d want existing default %d", cc.StatementCacheCapacity, qpeDefaultCacheCapacity)
	}
	if cc.DescriptionCacheCapacity != qpeDefaultCacheCapacity {
		t.Fatalf("description cache capacity=%d want existing default %d", cc.DescriptionCacheCapacity, qpeDefaultCacheCapacity)
	}
	qpeAssertDriverKnobsConsumed(t, cc)
}

// qpeAssertDriverKnobsConsumed 断言驱动旋钮键不出现在运行参数里：exec 模式
// 必须体现在 DefaultQueryExecMode 上，缓存容量绝不作为启动参数存在。
// qpeAssertDriverKnobsConsumed asserts no driver knob keys survive in the
// runtime params: the exec mode must live in DefaultQueryExecMode and cache
// capacities must never be present as startup parameters.
func qpeAssertDriverKnobsConsumed(t *testing.T, cc *pgx.ConnConfig) {
	t.Helper()
	for _, knob := range []string{qpeExecKey, "statement_cache_capacity", "description_cache_capacity"} {
		if _, ok := cc.RuntimeParams[knob]; ok {
			t.Fatalf("driver knob %q leaked into server runtime params", knob)
		}
	}
}

// qpeAssertTLS 断言 sslmode 语义在新键下保持：disable 恒无 TLS，verify-full
// 恒为校验主机名的 TLS。
// qpeAssertTLS asserts the sslmode semantics survive under the new key:
// disable always yields no TLS, verify-full always yields verifying TLS.
func qpeAssertTLS(t *testing.T, cc *pgx.ConnConfig, sslmode string) {
	t.Helper()
	switch sslmode {
	case "disable":
		if cc.TLSConfig != nil {
			t.Fatal("sslmode=disable must keep nil TLSConfig under the pooler profile")
		}
	case "verify-full":
		if cc.TLSConfig == nil || cc.TLSConfig.InsecureSkipVerify {
			t.Fatal("sslmode=verify-full must keep hostname-verifying TLS under the pooler profile")
		}
	default:
		t.Fatalf("unknown sslmode fixture %q", sslmode)
	}
}

// qpeAssertFixedRejection 断言拒绝画像：零值配置、固定哨兵错误（同一实例、无
// 包装原因）、错误文本既无金丝雀也无任何输入片段回显。
// qpeAssertFixedRejection asserts the rejection profile: nil config, the fixed
// sentinel error (same instance, no wrapped cause) and error text free of both
// canaries and any echoed input fragment.
func qpeAssertFixedRejection(t *testing.T, raw string) {
	t.Helper()
	cc, err := parsePostgresDSN(raw)
	if err == nil {
		t.Fatalf("accepted DSN outside the pooler profile contract: %q", raw)
	}
	if cc != nil {
		t.Fatal("non-nil ConnConfig alongside rejection")
	}
	if err != errInvalidPostgresDSN {
		t.Fatalf("error %v is not the fixed sentinel errInvalidPostgresDSN", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatal("rejection error must not wrap a raw cause")
	}
	text := err.Error()
	ppAssertNoCanary(t, text, "pooler profile rejection")
	for _, fragment := range []string{qpeExecKey, "cache_statement", "exec", "binflow", "postgres://"} {
		if strings.Contains(text, fragment) {
			t.Fatalf("rejection error echoes input fragment %q: %q", fragment, text)
		}
	}
}

// TestPoolerProfileExecAccepted 是显式 exec 的接收切片：显式 exec 在 localhost
// disable 与远端 verify-full 下都被接受，键序无关，值比较发生在标准 query 解
// 码之后（exe%63 ≡ exec，与既有 search_path 解码后校验语义一致），且核心画
// 像、TLS、no fallback 与 search_path 语义全部保持。
// TestPoolerProfileExecAccepted is the explicit-exec acceptance slice: explicit
// exec is accepted for both localhost disable and remote verify-full, key
// order is irrelevant, value comparison happens after standard query decoding
// (exe%63 ≡ exec, consistent with the existing decoded-value validation of
// search_path), and the core profile, TLS, no-fallback and search_path
// semantics all survive.
func TestPoolerProfileExecAccepted(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		host       string
		port       uint16
		user       string
		password   string
		database   string
		sslmode    string
		searchPath string // 空串表示必须缺席 / empty means must be absent
	}{
		{
			name:     "loopback disable",
			raw:      "postgres://binflow:secret@127.0.0.1:5432/binflow?sslmode=disable&default_query_exec_mode=exec",
			host:     "127.0.0.1",
			port:     5432,
			user:     "binflow",
			password: "secret",
			database: "binflow",
			sslmode:  "disable",
		},
		{
			name:     "loopback disable ipv6 key order independent",
			raw:      "postgres://binflow:secret@[::1]:5432/binflow?default_query_exec_mode=exec&sslmode=disable",
			host:     "::1",
			port:     5432,
			user:     "binflow",
			password: "secret",
			database: "binflow",
			sslmode:  "disable",
		},
		{
			name:       "loopback disable retains search_path",
			raw:        "postgresql://binflow:secret@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow&default_query_exec_mode=exec",
			host:       "127.0.0.1",
			port:       5432,
			user:       "binflow",
			password:   "secret",
			database:   "binflow",
			sslmode:    "disable",
			searchPath: "binflow",
		},
		{
			name:     "remote dns verify-full",
			raw:      "postgresql://binflow:secret@db.internal:5432/binflow?sslmode=verify-full&default_query_exec_mode=exec",
			host:     "db.internal",
			port:     5432,
			user:     "binflow",
			password: "secret",
			database: "binflow",
			sslmode:  "verify-full",
		},
		{
			name:     "remote ip verify-full",
			raw:      "postgres://binflow:secret@10.1.2.3:5432/binflow?sslmode=verify-full&default_query_exec_mode=exec",
			host:     "10.1.2.3",
			port:     5432,
			user:     "binflow",
			password: "secret",
			database: "binflow",
			sslmode:  "verify-full",
		},
		{
			name:     "decoded value equivalent to exec",
			raw:      "postgres://binflow:secret@127.0.0.1:5432/binflow?sslmode=disable&default_query_exec_mode=exe%63",
			host:     "127.0.0.1",
			port:     5432,
			user:     "binflow",
			password: "secret",
			database: "binflow",
			sslmode:  "disable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc, err := parsePostgresDSN(tc.raw)
			if err != nil {
				t.Fatalf("pooler-profile DSN rejected: %v", err)
			}
			ppAssertDSNFields(t, cc, tc.host, tc.port, tc.user, tc.password, tc.database)
			qpeAssertExecProfile(t, cc)
			qpeAssertTLS(t, cc, tc.sslmode)
			if tc.searchPath == "" {
				if _, ok := cc.RuntimeParams["search_path"]; ok {
					t.Fatal("search_path must stay absent when not requested")
				}
			} else if got := cc.RuntimeParams["search_path"]; got != tc.searchPath {
				t.Fatalf("search_path=%q want %q (trusted explicit value retained)", got, tc.searchPath)
			}
		})
	}
}

// TestPoolerProfileDefaultsWhenKeyAbsent 冻结键缺席时的既有默认：cache_statement
// 与 512/512 缓存在 localhost disable 与远端 verify-full 画像下都不得被本契约
// 改动。
// TestPoolerProfileDefaultsWhenKeyAbsent freezes the untouched defaults when
// the key is absent: cache_statement with 512/512 caches must hold for both
// the localhost disable and remote verify-full profiles; this contract must
// not move them.
func TestPoolerProfileDefaultsWhenKeyAbsent(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		host       string
		port       uint16
		sslmode    string
		searchPath string
	}{
		{
			name:       "loopback disable with search_path",
			raw:        "postgres://binflow:secret@127.0.0.1:5432/binflow?sslmode=disable&search_path=binflow",
			host:       "127.0.0.1",
			port:       5432,
			sslmode:    "disable",
			searchPath: "binflow",
		},
		{
			name:    "remote verify-full",
			raw:     "postgresql://binflow:secret@db.internal:5432/binflow?sslmode=verify-full",
			host:    "db.internal",
			port:    5432,
			sslmode: "verify-full",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cc, err := parsePostgresDSN(tc.raw)
			if err != nil {
				t.Fatalf("existing profile rejected: %v", err)
			}
			ppAssertDSNFields(t, cc, tc.host, tc.port, "binflow", "secret", "binflow")
			qpeAssertCacheDefaults(t, cc)
			qpeAssertTLS(t, cc, tc.sslmode)
			if tc.searchPath == "" {
				if _, ok := cc.RuntimeParams["search_path"]; ok {
					t.Fatal("search_path must stay absent when not requested")
				}
			} else if got := cc.RuntimeParams["search_path"]; got != tc.searchPath {
				t.Fatalf("search_path=%q want %q", got, tc.searchPath)
			}
		})
	}
}

// TestPoolerProfileExecValueRejections 冻结新键的值域拒绝：空值、同值/不同值重
// 复、大小写变体、其他 pgx mode 值（含显式 cache_statement）、空白填充值与
// 键的大小写变体（未知键）一律拒绝；金丝雀口令证明无输入泄露。
// TestPoolerProfileExecValueRejections freezes the value-domain rejections of
// the new key: empty value, identical/differing duplicates, case variants,
// every other pgx mode value (including an explicit cache_statement),
// whitespace-padded values and case variants of the key itself (unknown key)
// are all rejected; the canary password proves no input leakage.
func TestPoolerProfileExecValueRejections(t *testing.T) {
	base := "postgres://binflow:" + ppCanaryDSNPassword + "@127.0.0.1:5432/binflow?sslmode=disable&"
	remote := "postgres://binflow:" + ppCanaryDSNPassword + "@db.internal:5432/binflow?sslmode=verify-full&"
	cases := map[string]string{
		"empty value":                   base + qpeExecKey + "=",
		"duplicate identical exec":      base + qpeExecKey + "=exec&" + qpeExecKey + "=exec",
		"duplicate exec then default":   base + qpeExecKey + "=exec&" + qpeExecKey + "=cache_statement",
		"duplicate default then exec":   base + qpeExecKey + "=cache_statement&" + qpeExecKey + "=exec",
		"duplicate exec then empty":     base + qpeExecKey + "=exec&" + qpeExecKey + "=",
		"uppercase value":               base + qpeExecKey + "=EXEC",
		"mixed case value":              base + qpeExecKey + "=Exec",
		"explicit cache_statement":      base + qpeExecKey + "=cache_statement",
		"cache_describe":                base + qpeExecKey + "=cache_describe",
		"describe_exec":                 base + qpeExecKey + "=describe_exec",
		"simple_protocol":               base + qpeExecKey + "=simple_protocol",
		"space padded value":            base + qpeExecKey + "=+exec",
		"uppercase key":                 base + "DEFAULT_QUERY_EXEC_MODE=exec",
		"mixed case key":                base + "Default_Query_Exec_Mode=exec",
		"remote verify simple_protocol": remote + qpeExecKey + "=simple_protocol",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			qpeAssertFixedRejection(t, raw)
		})
	}
}

// TestPoolerProfileCacheKnobsStayRejected 冻结缓存参数拒绝：缓存容量是契约固
// 定值（exec 时 0、缺席时 512），绝不允许经 DSN 的 statement_cache_capacity/
// description_cache_capacity 键设定，即使值恰好与契约一致或与 exec 并用。
// TestPoolerProfileCacheKnobsStayRejected freezes the cache-knob rejections:
// capacities are contract-fixed (0 with exec, 512 when absent) and never
// operator-settable via the statement_cache_capacity/description_cache_capacity
// DSN keys, even when the value matches the contract exactly or rides along
// with exec.
func TestPoolerProfileCacheKnobsStayRejected(t *testing.T) {
	base := "postgres://binflow:secret@127.0.0.1:5432/binflow?sslmode=disable"
	cases := map[string]string{
		"statement capacity zero":          base + "&statement_cache_capacity=0",
		"statement capacity default value": base + "&statement_cache_capacity=512",
		"description capacity zero":        base + "&description_cache_capacity=0",
		"statement zero with exec":         base + "&statement_cache_capacity=0&" + qpeExecKey + "=exec",
		"description zero with exec":       base + "&description_cache_capacity=0&" + qpeExecKey + "=exec",
		"description large with exec":      base + "&description_cache_capacity=1024&" + qpeExecKey + "=exec",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			qpeAssertFixedRejection(t, raw)
		})
	}
}

// TestPoolerProfileNeighborKeysStayRejected 冻结邻键拒绝：新增 exec 键不得为
// 其余原先拒绝字段开门（凭据文件、证书、会话选择、hostaddr、固定
// application_name、options 与 fragment），全部维持固定哨兵拒绝。
// TestPoolerProfileNeighborKeysStayRejected freezes the neighbor-key
// rejections: the new exec key must not open the door for any other
// previously rejected field (credential files, certificates, session
// selection, hostaddr, the fixed application_name, options and fragments);
// all keep the fixed sentinel rejection.
func TestPoolerProfileNeighborKeysStayRejected(t *testing.T) {
	base := "postgres://binflow:" + ppCanaryDSNPassword + "@127.0.0.1:5432/binflow?sslmode=disable&" + qpeExecKey + "=exec&"
	cases := map[string]string{
		"passfile":          base + "passfile=/private/secret",
		"sslcert":           base + "sslcert=/private/secret",
		"target_session":    base + "target_session_attrs=read-write",
		"hostaddr":          base + "hostaddr=127.0.0.1",
		"application_name":  base + "application_name=operator",
		"options":           base + "options=-c%20search_path=x",
		"unknown extra key": base + "pooler=true",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			qpeAssertFixedRejection(t, raw)
		})
	}
	// fragment 形式与 query 键正交：新键在场时 fragment 依旧拒绝。
	// The fragment form is orthogonal to query keys: a fragment stays
	// rejected even with the new key present.
	qpeAssertFixedRejection(t, "postgres://binflow:"+ppCanaryDSNPassword+"@127.0.0.1:5432/binflow?sslmode=disable&"+qpeExecKey+"=exec#frag")
}

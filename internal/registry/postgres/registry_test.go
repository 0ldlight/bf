// Registry 单元测试先行：先写可失败契约，再实现最小切片。
// Unit tests written first: the failing contract precedes the implementation.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
)

// 编译期断言：Registry 满足 binary.Registry（测试侧声明，不进入生产依赖）。
// Compile-time assertion kept test-side: Registry satisfies binary.Registry.
var _ binary.Registry = (*Registry)(nil)

// failDriver 是一个只会在真正建立连接时失败的最小驱动；它用于证明非法
// 输入在任何驱动访问之前就被拒绝，以及 typed-cause 能穿透阶段包装。
// failDriver is a minimal driver that only fails when a real connection is
// attempted; it proves invalid inputs are rejected before any driver access
// and that the typed cause survives the stage wrapper.
var (
	errDriverConnect = errors.New("postgres registry test: driver connect refused")
	connectAttempts  int
)

type failDriver struct{}

func (failDriver) Open(string) (driver.Conn, error) {
	connectAttempts++
	return nil, errDriverConnect
}

func newFailingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("bf-postgres-fail-test", "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func init() { sql.Register("bf-postgres-fail-test", failDriver{}) }

// validMeta 返回通过规范前置校验的元数据；字段可按用例覆写。
// validMeta passes the canonical precondition; fields are overridden per case.
func validMeta() content.Metadata {
	return content.Metadata{
		SHA1:   strings.Repeat("a", 40),
		SHA256: strings.Repeat("b", 64),
		MD5:    strings.Repeat("c", 32),
		Size:   3,
	}
}

func TestNewRejectsNilDB(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) must fail")
	} else if got := err.Error(); got != "postgres registry: nil db" {
		t.Fatalf("New(nil) error text = %q, want bounded constant", got)
	}
}

func TestRegisterRejectsInvalidMetadataBeforeDriverAccess(t *testing.T) {
	oneMiB := strings.Repeat("x", 1<<20)
	cases := []struct {
		name string
		mut  func(*content.Metadata)
	}{
		{"empty sha1", func(m *content.Metadata) { m.SHA1 = "" }},
		{"short sha1", func(m *content.Metadata) { m.SHA1 = "abc" }},
		{"uppercase sha1", func(m *content.Metadata) { m.SHA1 = strings.Repeat("A", 40) }},
		{"non-hex sha1", func(m *content.Metadata) { m.SHA1 = strings.Repeat("z", 40) }},
		{"empty sha256", func(m *content.Metadata) { m.SHA256 = "" }},
		{"blank sha256", func(m *content.Metadata) { m.SHA256 = strings.Repeat(" ", 64) }},
		{"short sha256", func(m *content.Metadata) { m.SHA256 = strings.Repeat("b", 63) }},
		{"empty md5", func(m *content.Metadata) { m.MD5 = "" }},
		{"uppercase md5", func(m *content.Metadata) { m.MD5 = strings.Repeat("C", 32) }},
		{"non-hex md5", func(m *content.Metadata) { m.MD5 = strings.Repeat("0", 31) + "g" }},
		{"negative size", func(m *content.Metadata) { m.Size = -1 }},
		{"1MiB invalid sha1", func(m *content.Metadata) { m.SHA1 = oneMiB }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := New(newFailingDB(t))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			before := connectAttempts
			meta := validMeta()
			tc.mut(&meta)
			got, err := reg.Register(context.Background(), meta)
			if err == nil {
				t.Fatal("Register must reject invalid metadata")
			}
			if got != (content.Metadata{}) {
				t.Fatalf("Register invalid input returned %+v, want zero Metadata", got)
			}
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("Register error must wrap ErrInvalidMetadata, got %v", err)
			}
			if s := err.Error(); strings.Contains(s, oneMiB) {
				t.Fatalf("error text echoes caller value: %q", s)
			}
			if after := connectAttempts; after != before {
				t.Fatalf("invalid input reached the driver (attempts %d -> %d)", before, after)
			}
		})
	}
}

func TestRegisterPreCanceledContextBeforeDriverAccess(t *testing.T) {
	reg, err := New(newFailingDB(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := connectAttempts
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := reg.Register(ctx, validMeta())
	if err == nil {
		t.Fatal("Register must fail on canceled context")
	}
	if got != (content.Metadata{}) {
		t.Fatalf("Register canceled returned %+v, want zero Metadata", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Register error must preserve context.Canceled, got %v", err)
	}
	if s := err.Error(); strings.Contains(s, "driver") || strings.Contains(s, "refused") {
		t.Fatalf("cancellation must be reported before any driver access: %q", s)
	}
	if after := connectAttempts; after != before {
		t.Fatalf("canceled context reached the driver (attempts %d -> %d)", before, after)
	}
}

func TestRegisterPreservesTypedCauseFromBeginTx(t *testing.T) {
	reg, err := New(newFailingDB(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := reg.Register(context.Background(), validMeta())
	if err == nil {
		t.Fatal("Register must fail when the driver cannot connect")
	}
	if got != (content.Metadata{}) {
		t.Fatalf("Register returned %+v on failure, want zero Metadata", got)
	}
	if !errors.Is(err, errDriverConnect) {
		t.Fatalf("Register error must preserve the driver cause via Unwrap, got %v", err)
	}
	if s := err.Error(); s != "postgres registry: begin transaction" {
		t.Fatalf("Error() must be the fixed stage text, got %q", s)
	}
	if strings.Contains(err.Error(), errDriverConnect.Error()) {
		t.Fatal("Error() must not echo the underlying cause text")
	}
}

func TestJavaBlankMatchesCharacterIsWhitespace(t *testing.T) {
	blank := []string{
		"",
		" ",
		"\t",
		"\n",
		"\v",
		"\f",
		"\r",
		"\x1c", "\x1d", "\x1e", "\x1f",
		" \t\r\n ",
		"\u2028", // Zl
		"\u2029", // Zp
		"\u1680", // Ogham space (plain Zs)
	}
	nonBlank := []string{
		"a",
		"0",
		"\x00",
		"\x1b",
		"\u0085", // NEL is NOT Character.isWhitespace
		"\u00a0", // NBSP excluded
		"\u2007", // figure space excluded
		"\u202f", // narrow NBSP excluded
	}
	for _, s := range blank {
		if !javaBlank(s) {
			t.Errorf("javaBlank(%q) = false, want true", s)
		}
	}
	for _, s := range nonBlank {
		if javaBlank(s) {
			t.Errorf("javaBlank(%q) = true, want false", s)
		}
	}
}

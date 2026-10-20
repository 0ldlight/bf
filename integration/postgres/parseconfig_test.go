// DSN 解析失败的隐私回归：pgx 的 ParseConfigError 只做尽力而为的口令
// redaction（见 pgx v5.11 pgconn/errors.go 注释），畸形连接串可能整段泄
// 漏进错误文本；共享解析器必须返回固定文案且绝不携带原始输入。
// Privacy regression for DSN parse failures: pgx's ParseConfigError only
// redacts passwords best-effort (pgconn/errors.go in pgx v5.11), so a
// malformed connection string may leak verbatim into the error text; the
// shared parser must return a fixed message that never carries the input.
package postgresit

import (
	"strings"
	"testing"
)

const parseFailText = "postgresit: test DSN parse failed (input never logged)"

func TestParseTestConfigNeverLeaksMalformedDSN(t *testing.T) {
	// 畸形输入带醒目的合成标记；固定文案必须出现且标记绝不可见。
	// A malformed input with a distinctive synthetic marker; the fixed
	// text must appear and the marker must never show up.
	malformed := "this is not a valid dsn LEAKMARKER9f31 not=keyword"
	_, err := parseTestConfig(malformed)
	if err == nil {
		t.Fatal("malformed DSN must fail to parse")
	}
	if err.Error() != parseFailText {
		t.Fatalf("parse error text = %q, want fixed %q", err.Error(), parseFailText)
	}
	if strings.Contains(err.Error(), "LEAKMARKER9f31") {
		t.Fatalf("parse error leaks the raw DSN: %q", err.Error())
	}
	// 合法 URL 形态必须原样成功（无错误），不改变有效配置路径。
	// A valid URL form must succeed unchanged.
	cfg, err := parseTestConfig("postgres://u:secret@127.0.0.1:5432/db?sslmode=disable")
	if err != nil {
		t.Fatalf("valid DSN must parse: %v", err)
	}
	if cfg == nil || cfg.Host != "127.0.0.1" || cfg.Database != "db" {
		t.Fatalf("valid config not preserved: %+v", cfg)
	}
}

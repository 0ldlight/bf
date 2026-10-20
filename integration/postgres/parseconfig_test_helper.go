// 共享测试配置解析：唯一允许触碰 DSN 文本的地方。
// Shared test-config parsing: the only place allowed to touch DSN text.
package postgresit

import (
	"errors"

	"github.com/jackc/pgx/v5"
)

// errParseTestConfig 是固定文案：pgx 的 ParseConfigError 只做尽力而为的
// 口令 redaction（pgx v5.11 pgconn/errors.go），畸形连接串可能整段泄漏，
// 因此解析失败永不透传原始错误对象，也不打印/包装它。
// errParseTestConfig is a fixed message: pgx's ParseConfigError redacts
// passwords only best-effort (pgconn/errors.go, pgx v5.11), so a malformed
// connection string can leak verbatim — parse failures never pass through,
// print or wrap the raw error object.
var errParseTestConfig = errors.New("postgresit: test DSN parse failed (input never logged)")

// parseTestConfig 解析测试 DSN；成功原样返回配置，失败只返回固定错误。
// parseTestConfig parses the test DSN, returning the config unchanged on
// success and only the fixed error on failure.
func parseTestConfig(dsn string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errParseTestConfig
	}
	return cfg, nil
}

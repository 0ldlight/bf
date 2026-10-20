// Package postgres 以 database/sql 实现内容元数据登记：PostgreSQL 开发默认。
// Package postgres implements content-metadata registration over database/sql
// with PostgreSQL as the development default; the driver is caller-supplied.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"unicode"

	"github.com/0ldlight/bf/internal/content"
)

// SchemaSQL 是 v1 首个全新数据库模式，仅供可信引导/测试显式应用；
// New 与 Register 绝不自动执行它。
// SchemaSQL is the v1 first fresh-database schema for explicit trusted
// bootstrap/test application only; New and Register never execute it.
//
//go:embed schema.sql
var SchemaSQL string

// ErrInvalidMetadata 表示输入不满足规范内部前置校验（严格小写十六进制与
// 非负长度）；错误文本永不回显调用方值。
// ErrInvalidMetadata means the input fails the canonical internal
// precondition (strict lowercase hex and nonnegative size); error text
// never echoes caller values.
var ErrInvalidMetadata = errors.New("postgres registry: invalid metadata")

// 阶段文案是固定常量：Error() 只输出操作归因，底层错误（可能含摘要、SQL
// 细节或凭据）不会经 %w 进入 Error()；Unwrap 原样保留原因供可信调用方。
// Stage texts are fixed constants: Error() prints only the operation
// attribution, underlying causes never enter it via %w, and Unwrap exposes
// the untouched original cause for trusted callers.
const (
	stageInvalid   = "postgres registry: invalid metadata"
	stageCancelled = "postgres registry: cancelled before transaction"
	stageBegin     = "postgres registry: begin transaction"
	stageQuery     = "postgres registry: query existing record"
	stageInsert    = "postgres registry: insert new record"
	stageBackfill  = "postgres registry: backfill sha256"
	stageReload    = "postgres registry: reload after backfill"
	stageCommit    = "postgres registry: commit"
)

// Registry 在调用方拥有的 *sql.DB 上登记内容元数据；连接创建、生命周期
// 与驱动选择归调用方，本类型无自有连接状态，支持并发 Register。
// Registry registers content metadata on a caller-owned *sql.DB; the
// caller owns connection creation, lifetime and driver choice. The type
// holds no connection state of its own and concurrent Registers are fine.
type Registry struct {
	db *sql.DB
}

// New 拒绝 nil *sql.DB，返回有界常量错误；不做 DDL、Ping 或连接创建。
// New rejects a nil *sql.DB with a bounded constant error; it performs no
// DDL, no Ping and no connection creation.
func New(db *sql.DB) (*Registry, error) {
	if db == nil {
		return nil, errors.New("postgres registry: nil db")
	}
	return &Registry{db: db}, nil
}

// opError 携带固定操作文案与原始原因；不修改、不包装原始错误对象本身。
// opError pairs a fixed operation text with the original cause; the cause
// object is neither redacted nor mutated.
type opError struct {
	op    string
	cause error
}

func (e *opError) Error() string { return e.op }
func (e *opError) Unwrap() error { return e.cause }

// Register 以 READ COMMITTED 事务登记元数据并返回规范结果，语义对齐目标
// insertRecordInDb 的静态分支证据：
// Register commits metadata in one READ COMMITTED transaction and returns
// the canonical result, matching the static branches of the target
// insertRecordInDb:
//
//  1. 先做规范内部前置校验（SHA-1/SHA-256/MD5 必须是精确小写 ASCII 十六
//     进制 40/64/32 位且 Size>=0），任何 DB 访问之前拒绝；这是本适配器
//     对 content.Calculate 输出的显式本地前置，不是目标畸形输入/HTTP
//     对齐。
//     The canonical internal precondition (exact lowercase ASCII hex of
//     length 40/64/32 and Size>=0) rejects before any DB access; it is an
//     explicit local precondition for content.Calculate outputs, not
//     target malformed-input/HTTP parity.
//  2. 按 SHA-1 查既有记录；缺失则 INSERT ... ON CONFLICT DO NOTHING 后
//     Commit 并原样返回输入元数据——即使并发冲突导致零行写入（目标丢弃
//     create 布尔返回值并返回 INCOMING；不做无条件回读/冲突拒绝/重试）。
//     An absent SHA-1 runs INSERT ... ON CONFLICT DO NOTHING, commits and
//     returns the INCOMING metadata verbatim even when a lost conflict
//     writes zero rows (the target discards the create boolean; no
//     unconditional readback, collision rejection or retry is invented).
//  3. 命中且存储 SHA-256 非空白：返回存储元数据，不覆盖 MD5/长度。
//     A found record with nonblank stored SHA-256 returns the stored
//     metadata without touching MD5/length.
//  4. 命中但存储 SHA-256 为 NULL 或 Java-blank：仅回填 SHA-256、回读、
//     Commit 后返回。
//     A found record with NULL or Java-blank SHA-256 backfills SHA-256
//     only, reloads the row and returns it after Commit.
//
// 元数据事务与 provider/节点存储相互独立。失败一律返回零 Metadata 与固定
// 文案 + Unwrap 原因；Commit 失败后事务结局不确定，不做回读或回滚保证。
// The metadata transaction is independent of provider/node saving. Every
// failure returns zero Metadata with fixed text plus the preserved cause;
// after a Commit failure the transaction outcome is uncertain and no
// readback/rollback guarantee is made.
func (r *Registry) Register(ctx context.Context, meta content.Metadata) (content.Metadata, error) {
	if err := validateMetadata(meta); err != nil {
		return content.Metadata{}, &opError{op: stageInvalid, cause: err}
	}
	if err := ctx.Err(); err != nil {
		return content.Metadata{}, &opError{op: stageCancelled, cause: err}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return content.Metadata{}, &opError{op: stageBegin, cause: err}
	}
	row := tx.QueryRowContext(ctx,
		"SELECT sha1, md5, bin_length, sha256 FROM binaries WHERE sha1 = $1", meta.SHA1)
	var sha1, md5 string
	var binLength int64
	var sha256 sql.NullString
	if err := row.Scan(&sha1, &md5, &binLength, &sha256); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 新建分支：冲突输掉时仍返回输入元数据（见上）。所有值均为参数。
			// Fresh branch: a lost conflict still returns the input (see
			// above). Every value is a bound parameter.
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO binaries (sha1, md5, bin_length, sha256) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING",
				meta.SHA1, meta.MD5, meta.Size, meta.SHA256); err != nil {
				return r.fail(tx, stageInsert, err)
			}
			if err := tx.Commit(); err != nil {
				return content.Metadata{}, &opError{op: stageCommit, cause: err}
			}
			return meta, nil
		}
		return r.fail(tx, stageQuery, err)
	}
	if sha256.Valid && !javaBlank(sha256.String) {
		// 既有非空白 SHA-256：返回存储值，不覆盖 MD5/长度。
		// Existing nonblank SHA-256: stored values win; MD5/length are
		// never overwritten.
		if err := tx.Commit(); err != nil {
			return content.Metadata{}, &opError{op: stageCommit, cause: err}
		}
		return content.Metadata{SHA1: sha1, SHA256: sha256.String, MD5: md5, Size: binLength}, nil
	}
	// 缺失（NULL 或 Java-blank）SHA-256：仅回填 SHA-256，再回读。
	// Missing (NULL or Java-blank) SHA-256: update SHA-256 only, then
	// reload the row.
	if _, err := tx.ExecContext(ctx,
		"UPDATE binaries SET sha256 = $1 WHERE sha1 = $2", meta.SHA256, meta.SHA1); err != nil {
		return r.fail(tx, stageBackfill, err)
	}
	reloaded := tx.QueryRowContext(ctx,
		"SELECT sha1, md5, bin_length, sha256 FROM binaries WHERE sha1 = $1", meta.SHA1)
	var outSHA1, outMD5 string
	var outLength int64
	var outSHA256 sql.NullString
	if err := reloaded.Scan(&outSHA1, &outMD5, &outLength, &outSHA256); err != nil {
		return r.fail(tx, stageReload, err)
	}
	if err := tx.Commit(); err != nil {
		return content.Metadata{}, &opError{op: stageCommit, cause: err}
	}
	return content.Metadata{SHA1: outSHA1, SHA256: outSHA256.String, MD5: outMD5, Size: outLength}, nil
}

// fail 回滚自有事务并返回零 Metadata 与固定文案；非 ErrTxDone 的回滚
// 错误与原始原因并列保留（errors.Join 不把原文本并入 Error() 输出，阶段
// 包装的 Error() 仍只返回常量）。
// fail rolls back the owned transaction and returns zero Metadata with the
// fixed stage text; a non-ErrTxDone rollback error is preserved alongside
// the original cause (errors.Join keeps them reachable via errors.Is/As
// while the stage wrapper's Error() still prints only the constant).
func (r *Registry) fail(tx *sql.Tx, stage string, cause error) (content.Metadata, error) {
	if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
		cause = errors.Join(cause, rbErr)
	}
	return content.Metadata{}, &opError{op: stage, cause: cause}
}

// validateMetadata 是规范内部前置校验：精确长度的小写 ASCII 十六进制与
// 非负长度；错误只报字段与长度，永不回显值。
// validateMetadata is the canonical internal precondition: exact-length
// lowercase ASCII hex plus nonnegative size; errors report only the field
// and length, never the value.
func validateMetadata(meta content.Metadata) error {
	if err := canonicalHex("sha1", meta.SHA1, 40); err != nil {
		return err
	}
	if err := canonicalHex("sha256", meta.SHA256, 64); err != nil {
		return err
	}
	if err := canonicalHex("md5", meta.MD5, 32); err != nil {
		return err
	}
	if meta.Size < 0 {
		return fmt.Errorf("postgres registry: size (%d): %w", meta.Size, ErrInvalidMetadata)
	}
	return nil
}

func canonicalHex(field, s string, want int) error {
	if len(s) != want {
		return fmt.Errorf("postgres registry: %s (len %d): %w", field, len(s), ErrInvalidMetadata)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("postgres registry: %s (bad hex at %d): %w", field, i, ErrInvalidMetadata)
		}
	}
	return nil
}

// javaBlank 报告 s 是否为空或全为 Java Character.isWhitespace 空白：
// ASCII 9..13、28..31 与 Zs/Zl/Zp，但排除 NBSP(U+00A0)/U+2007/U+202F；
// NEL(U+0085) 不是空白。目标回填判断使用 StringUtils.isBlank，同一语义。
// javaBlank reports whether s is empty or entirely Java
// Character.isWhitespace: ASCII 9..13, 28..31 and Zs/Zl/Zp minus the
// nonbreaking U+00A0/U+2007/U+202F; NEL U+0085 is not blank. The target's
// backfill check uses StringUtils.isBlank with the same semantics.
func javaBlank(s string) bool {
	for _, rn := range s {
		if !javaBlankRune(rn) {
			return false
		}
	}
	return true
}

func javaBlankRune(rn rune) bool {
	if '\t' <= rn && rn <= '\r' || 0x1c <= rn && rn <= 0x1f {
		return true
	}
	if unicode.In(rn, unicode.Zs, unicode.Zl, unicode.Zp) {
		return rn != 0xa0 && rn != 0x2007 && rn != 0x202f
	}
	return false
}

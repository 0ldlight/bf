// BIN-201 SQL 安全后端：Store 实现冻结的 Backend 接口（Validate/
// Authenticate/LookupRepository/CanRead），只读请求路径，无写、无 DDL、
// 无正向凭据/授权缓存——每次调用重读当前数据库状态，成员/授权/启用变更
// 下一次调用即生效；多阶段请求不是可串行化快照，已授权的在途传输可继续
// 完成，后续请求必须重读状态。数据库连接由调用方提供（产品主模块不导入
// 驱动，pgx 由集成模块在组合根注入）；构造器只验证输入，不校验 schema、
// 不联系数据库。
// BIN-201 SQL security backend: Store implements the frozen Backend
// interface (Validate/Authenticate/LookupRepository/CanRead) with a
// read-only request path — no writes, no DDL and no positive credential or
// grant cache: every call re-reads current database state so membership,
// grant and enablement changes take effect on the next call. Multi-stage
// requests are not serializable snapshots; an already-authorized in-flight
// transfer may complete while later requests must re-read state. The caller
// owns the database connection (the product main module imports no driver;
// composition-root wiring supplies pgx from the integration module).
// Constructors validate inputs only — they never validate the schema or
// contact the database.
package security

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"github.com/0ldlight/bf/internal/repository"
)

// 固定阶段文案：Error() 只输出固定归因，底层错误对象永不进入文本；
// Unwrap 原样保留原因供可信调用方（errors.Is/As 可达）。
// Fixed stage texts: Error() prints only the fixed attribution and the
// underlying error object never enters the text; Unwrap exposes the
// untouched cause for trusted callers (reachable via errors.Is/As).
const (
	stageValidateVersion  = "security: validate schema version"
	stageValidateColumns  = "security: validate schema columns"
	stageAuthenticateWait = "security: authenticate kdf slot"
	stageAuthenticateRow  = "security: authenticate lookup"
	stageAuthenticateCtx  = "security: authenticate context"
	stageRepositoryRow    = "security: repository lookup"
	stageCanReadInput     = "security: can-read input"
	stageCanReadRow       = "security: can-read lookup"
)

// 固定诊断哨兵（文案不含任何输入字节），只作为 storeError 的原因。
// Fixed diagnostic sentinels (text never echoes input bytes) serving only
// as storeError causes.
var (
	errSchemaVersionNotOne = errors.New("security: schema version row is not 1")
	errRepositoryRowShape  = errors.New("security: stored repository row is unsupported")
	errInvalidPrincipalArg = errors.New("security: principal is zero")
	errUnsupportedRepoArg  = errors.New("security: repository info is unsupported")
	errRepoPathMismatchArg = errors.New("security: path repository does not match")
)

// kdfConcurrency 限制并发 Authenticate 的 KDF 数量为 4，防止无效/未知
// 输入引发无界 KDF 消耗。
// kdfConcurrency caps concurrent Authenticate KDF work at 4 so invalid or
// unknown input cannot drive unbounded KDF consumption.
const kdfConcurrency = 4

// dummyCredentialPhrase 是构造期随机盐哑编码所用的固定非账号短语：未知/
// 禁用用户仍对有效 raw 恰好执行一次哑 Check（与已知用户相同的 KDF 工作路
// 径），但即使提交的 raw 恰好等于本短语也绝不产生 Principal。
// dummyCredentialPhrase is the fixed non-account phrase for the
// constructor-time random-salted dummy encoding: unknown and disabled users
// still run exactly one dummy Check on valid raw (the same KDF work path as
// known users), yet a submitted raw equal to this phrase never yields a
// Principal.
const dummyCredentialPhrase = "BinFlow security dummy verification passphrase; no account exists"

// storeError 把固定公开文案与两个 Unwrap 目标（导出哨兵 + 原始原因）配对：
// Error() 永不回显原因或输入，errors.Is(err, ErrSecurityStore) 与
// errors.Is/As 对底层驱动/上下文错误同时成立。
// storeError pairs a fixed public text with two unwrap targets (the
// exported sentinel plus the original cause): Error() never echoes the
// cause or input, while errors.Is(err, ErrSecurityStore) and errors.Is/As
// against the underlying driver/context error both hold.
type storeError struct {
	sentinel error
	op       string
	cause    error
}

func (e *storeError) Error() string   { return e.sentinel.Error() + ": " + e.op }
func (e *storeError) Unwrap() []error { return []error{e.sentinel, e.cause} }
func newStoreError(op string, cause error) *storeError {
	return &storeError{sentinel: ErrSecurityStore, op: op, cause: cause}
}

// Store 是 Backend 的 SQL 实现：持有调用方拥有的 *sql.DB、每次操作的超时
// 上界、容量 4 的 KDF 槽与构造期生成的哑编码。请求方法不做任何写/DDL。
// Store is the SQL Backend implementation holding the caller-owned *sql.DB,
// a per-operation timeout bound, the capacity-4 KDF slot channel and the
// constructor-generated dummy encoding. Request methods perform no writes
// or DDL.
type Store struct {
	db        *sql.DB
	timeout   time.Duration
	kdfSlots  chan struct{}
	dummyCred string
}

// NewStore 校验输入（db 非空、0 < timeout ≤ 1 分钟；typed nil 不支持）并
// 预生成一个随机盐哑编码；不做 schema 校验、不联系数据库、不创建任何
// 账号。哑编码生成失败返回 (nil, 固定文案错误)。
// NewStore validates its inputs (nonnil db, 0 < timeout ≤ 1 minute; typed
// nil is unsupported) and pre-generates one random-salted dummy encoding;
// it validates no schema, contacts no database and creates no account. A
// dummy-encoding failure returns (nil, fixed-text error).
func NewStore(db *sql.DB, timeout time.Duration) (*Store, error) {
	if db == nil || timeout <= 0 || timeout > time.Minute {
		return nil, ErrConfiguration
	}
	dummy, err := generateCredential(rand.Reader, dummyCredentialPhrase)
	if err != nil {
		return nil, err
	}
	return &Store{
		db:        db,
		timeout:   timeout,
		kdfSlots:  make(chan struct{}, kdfConcurrency),
		dummyCred: dummy,
	}, nil
}

// opContext 从调用方 context 派生本次操作的超时 context。
// opContext derives the per-operation timeout context from the caller's.
func (s *Store) opContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// validateColumns 是每张必需表的被动列探针（SELECT … WHERE false）：任何
// 语句都不改写数据；列缺失/不可见以原样错误暴露。
// validateColumns passively probes the expected columns of every required
// table (SELECT … WHERE false): no statement mutates data, and missing or
// invisible columns surface as the raw error.
var validateColumns = []struct {
	table   string
	columns string
}{
	{"security_schema_version", "version"},
	{"security_users", "user_id, username, encoded_secret, enabled, effective_admin"},
	{"security_groups", "group_id, group_name"},
	{"security_group_members", "user_id, group_id"},
	{"security_repositories", "repo_key, kind, package_type, revision"},
	{"security_read_grants", "repo_key, user_id, group_id, scope"},
}

// Validate 只做被动检查：版本表恰好一行且值为 1，随后对全部必需表执行
// WHERE false 的列探针。这不是任意 schema 漂移证明。任何失败包装为
// errors.Is 可达 ErrSecurityStore 的固定文案错误。
// Validate performs passive checks only: the version table holds exactly
// one row with value 1, then column probes with WHERE false run against
// every required table. This is not an attestation of arbitrary schema
// drift. Any failure returns a fixed-text error where
// errors.Is(err, ErrSecurityStore) holds.
func (s *Store) Validate(ctx context.Context) error {
	qctx, cancel := s.opContext(ctx)
	defer cancel()
	var count, minVersion, maxVersion int
	if err := s.db.QueryRowContext(qctx, "SELECT COUNT(*), COALESCE(MIN(version), 0), COALESCE(MAX(version), 0) FROM security_schema_version").Scan(&count, &minVersion, &maxVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return newStoreError(stageValidateVersion, errSchemaVersionNotOne)
		}
		return newStoreError(stageValidateVersion, err)
	}
	if count != 1 || minVersion != 1 || maxVersion != 1 {
		return newStoreError(stageValidateVersion, errSchemaVersionNotOne)
	}
	for _, probe := range validateColumns {
		rows, err := s.db.QueryContext(qctx, "SELECT "+probe.columns+" FROM "+probe.table+" WHERE false")
		if err != nil {
			return newStoreError(stageValidateColumns, err)
		}
		if err := rows.Close(); err != nil {
			return newStoreError(stageValidateColumns, err)
		}
	}
	return nil
}

// normalizeUsername 做有限的 ASCII 归一化：仅修剪两端 ≤0x20 的 ASCII 字节
// 并折叠 A-Z；任何非 ASCII 字节或非法字符返回 false。这是
// UsersUtil.lowerTrimUsername 的有限子集，不是 Unicode/Locale 等价实现。
// 口令不做任何归一化。
// normalizeUsername performs the finite ASCII normalization: trimming only
// ASCII bytes ≤0x20 at both ends and folding A-Z; any non-ASCII byte or
// illegal character returns false. This is a finite subset of
// UsersUtil.lowerTrimUsername, not Unicode/locale parity. Passwords are
// never normalized.
func normalizeUsername(name string) (string, bool) {
	start, end := 0, len(name)
	for start < end && name[start] <= 0x20 {
		start++
	}
	for end > start && name[end-1] <= 0x20 {
		end--
	}
	b := make([]byte, 0, end-start)
	for i := start; i < end; i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b = append(b, c)
	}
	if len(b) == 0 || len(b) > 64 {
		return "", false
	}
	if !isUsernameLowerByte(b[0], true) {
		return "", false
	}
	for _, c := range b[1:] {
		if !isUsernameLowerByte(c, false) {
			return "", false
		}
	}
	return string(b), true
}

// isUsernameLowerByte 判定小写归一后的用户名字节：首字节限 [a-z0-9]，
// 其余限 [a-z0-9._@-]（字节集白名单天然排除全部 ≥0x80 的非 ASCII）。
// isUsernameLowerByte decides a folded username byte: the first byte is
// limited to [a-z0-9] and the rest to [a-z0-9._@-] (the whitelist
// naturally excludes every non-ASCII byte ≥0x80).
func isUsernameLowerByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	case first:
		return false
	default:
		return c == '.' || c == '_' || c == '@' || c == '-'
	}
}

// Authenticate 用真实校验产生 Principal：有限 ASCII 用户名归一化与有限
// raw 边界任一失败直接 ErrInvalidCredentials；随后获取容量 4 的 KDF 槽
// （ctx 取消时等待失败）。SELECT user_id, username, encoded_secret,
// enabled：缺失行→一次哑 Check 后 ErrInvalidCredentials；禁用→一次哑
// Check 后 ErrAccountDisabled（已知启用用户走真实编码 Check：不匹配
// ErrInvalidCredentials、畸形编码 ErrCredentialEncoding）。成功后再查
// ctx，返回以数据库私有 id 为稳定身份、DB 规范用户名的 Principal；零
// 值/哑匹配永不产生 Principal。SQL 失败为固定文案且 Unwrap 保留原因。
// 不承诺网络时序等价。
// Authenticate produces a Principal through real verification: any failure
// of the finite ASCII username normalization or the finite raw bound
// returns ErrInvalidCredentials immediately; the capacity-4 KDF slot is
// then acquired (waiting aborts on ctx cancellation). It SELECTs user_id,
// username, encoded_secret, enabled: a missing row runs one dummy Check
// then ErrInvalidCredentials; a disabled row runs one dummy Check then
// ErrAccountDisabled (a known enabled row runs the real encoding Check:
// mismatch ErrInvalidCredentials, malformed encoding ErrCredentialEncoding).
// After success ctx is rechecked and a Principal with the stable private
// database id and the DB-canonical username returns; zero values and dummy
// matches never yield a Principal. SQL failures keep a fixed text with the
// cause under Unwrap. No network timing equality is promised.
func (s *Store) Authenticate(ctx context.Context, username, raw string) (Principal, error) {
	normalized, ok := normalizeUsername(username)
	if !ok {
		return Principal{}, ErrInvalidCredentials
	}
	if !validRawCredential(raw) {
		return Principal{}, ErrInvalidCredentials
	}
	qctx, cancel := s.opContext(ctx)
	defer cancel()
	if err := qctx.Err(); err != nil {
		return Principal{}, newStoreError(stageAuthenticateCtx, err)
	}
	select {
	case s.kdfSlots <- struct{}{}:
		defer func() { <-s.kdfSlots }()
	case <-qctx.Done():
		return Principal{}, newStoreError(stageAuthenticateWait, qctx.Err())
	}
	var (
		id      int64
		canon   string
		encoded string
		enabled bool
	)
	err := s.db.QueryRowContext(qctx,
		"SELECT user_id, username, encoded_secret, enabled FROM security_users WHERE username = $1",
		normalized).Scan(&id, &canon, &encoded, &enabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, _ = CheckCredential(raw, s.dummyCred)
		if err := qctx.Err(); err != nil {
			return Principal{}, newStoreError(stageAuthenticateCtx, err)
		}
		return Principal{}, ErrInvalidCredentials
	case err != nil:
		return Principal{}, newStoreError(stageAuthenticateRow, err)
	}
	if !enabled {
		_, _ = CheckCredential(raw, s.dummyCred)
		if err := qctx.Err(); err != nil {
			return Principal{}, newStoreError(stageAuthenticateCtx, err)
		}
		return Principal{}, ErrAccountDisabled
	}
	match, err := CheckCredential(raw, encoded)
	if ctxErr := qctx.Err(); ctxErr != nil {
		return Principal{}, newStoreError(stageAuthenticateCtx, ctxErr)
	}
	if err != nil {
		return Principal{}, err
	}
	if !match {
		return Principal{}, ErrInvalidCredentials
	}
	if err := qctx.Err(); err != nil {
		return Principal{}, newStoreError(stageAuthenticateCtx, err)
	}
	if id <= 0 || canon == "" {
		return Principal{}, newStoreError(stageAuthenticateRow, errInvalidPrincipalArg)
	}
	return Principal{id: id, username: canon}, nil
}

// LookupRepository 做精确 repo 键查询（key/kind/package_type/revision），
// 不读节点：缺失返回 ErrRepositoryAbsent，存储行不支持（kind ≠ local、
// package_type ≠ generic、revision ≤ 0、键不回显一致）是 ErrSecurityStore
// 的固定文案错误。仅 local/generic revision>0 受支持。
// LookupRepository performs the exact repo-key query (key/kind/
// package_type, revision) without touching nodes: absence returns
// ErrRepositoryAbsent while an unsupported stored row (kind ≠ local,
// package_type ≠ generic, revision ≤ 0, or a non-echoing key mismatch) is a
// fixed-text ErrSecurityStore error. Only local/generic revision>0 is
// supported.
func (s *Store) LookupRepository(ctx context.Context, repoKey string) (RepositoryInfo, error) {
	qctx, cancel := s.opContext(ctx)
	defer cancel()
	var info RepositoryInfo
	err := s.db.QueryRowContext(qctx,
		"SELECT repo_key, kind, package_type, revision FROM security_repositories WHERE repo_key = $1",
		repoKey).Scan(&info.Key, &info.Kind, &info.PackageType, &info.Revision)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return RepositoryInfo{}, ErrRepositoryAbsent
	case err != nil:
		return RepositoryInfo{}, newStoreError(stageRepositoryRow, err)
	}
	if info.Key != repoKey || info.Kind != "local" || info.PackageType != "generic" || info.Revision <= 0 {
		return RepositoryInfo{}, newStoreError(stageRepositoryRow, errRepositoryRowShape)
	}
	return info, nil
}

// canReadQuery 是单条授权查询：当前用户行（id AND username AND enabled）
// 加仓库存在性，effective_admin 或 scope='**' 的直授/组授权 EXISTS 命中
// 即允许。使用当前 effective_admin 与当前成员关系，不缓存、不用请求侧
// 声明的角色；缺失/删除/禁用的 principal 得到 false,nil；成员/授权变更
// 下一次调用生效。
// canReadQuery is the single authorization query: the CURRENT user row
// (id AND username AND enabled) plus repository existence, allowed by
// effective_admin or an EXISTS of a scope='**' direct or group grant. It
// uses current effective_admin and current memberships with no cache and
// no request-claimed roles; a missing, deleted or disabled principal
// yields false,nil, and membership/grant changes take effect on the next
// call.
const canReadQuery = `
SELECT EXISTS (
        SELECT 1 FROM security_repositories r
        WHERE r.repo_key = $3
          AND r.kind = 'local'
          AND r.package_type = 'generic'
          AND r.revision > 0
    )
    AND EXISTS (
        SELECT 1 FROM security_users u
        WHERE u.user_id = $1
          AND u.username = $2
          AND u.enabled
          AND (
              u.effective_admin
              OR EXISTS (
                  SELECT 1 FROM security_read_grants g
                  WHERE g.repo_key = $3
                    AND g.scope = '**'
                    AND (
                        g.user_id = u.user_id
                        OR (g.user_id IS NULL
                            AND g.group_id IN (
                                SELECT m.group_id
                                FROM security_group_members m
                                WHERE m.user_id = u.user_id
                            ))
                    )
              )
          )
    )`

// CanRead 先验证非零 Principal、受支持的 RepositoryInfo 与 path.Repo 一
// 致（否则固定文案 ErrConfiguration 类错误），随后执行 canReadQuery。
// 任何 SQL 失败为固定文案且 Unwrap 保留原因；布尔结果按当前持久状态
// 决定。
// CanRead first validates a nonzero Principal, a supported RepositoryInfo
// and path.Repo agreement (otherwise a fixed-text ErrConfiguration-family
// error), then executes canReadQuery. Any SQL failure keeps a fixed text
// with the cause under Unwrap; the boolean follows current persisted state.
func (s *Store) CanRead(ctx context.Context, principal Principal, repo RepositoryInfo, path repository.Path) (bool, error) {
	if principal.ID() <= 0 || principal.Username() == "" {
		return false, &storeError{sentinel: ErrConfiguration, op: stageCanReadInput, cause: errInvalidPrincipalArg}
	}
	if repo.Key == "" || repo.Kind != "local" || repo.PackageType != "generic" || repo.Revision <= 0 {
		return false, &storeError{sentinel: ErrConfiguration, op: stageCanReadInput, cause: errUnsupportedRepoArg}
	}
	if path.Repo() != repo.Key {
		return false, &storeError{sentinel: ErrConfiguration, op: stageCanReadInput, cause: errRepoPathMismatchArg}
	}
	qctx, cancel := s.opContext(ctx)
	defer cancel()
	var allowed bool
	if err := s.db.QueryRowContext(qctx, canReadQuery,
		principal.ID(), principal.Username(), repo.Key).Scan(&allowed); err != nil {
		return false, newStoreError(stageCanReadRow, err)
	}
	return allowed, nil
}

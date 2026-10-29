// application.go 实现 Run 的真实生产组合：健康模式只构造既有健康处理器与
// 监听器；下载模式以真实 pgx 驱动、PostgreSQL 池、安全存储、只读节点池与
// 文件存储装配完整下载面。本文件属于运行时编写者所有；不含任何测试/假
// 验证器分支。
// application.go implements the real production composition of Run: health
// mode builds only the existing health handler and a listener; download mode
// assembles the full download surface with the real pgx driver, PostgreSQL
// pool, security store, read-only node pool and filestore. Owned by the
// runtime writer; no test or fake-verifier branch exists here.
package process

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/httpserver"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
	"github.com/0ldlight/bf/internal/security"
)

const (
	// 本文件专属的固定运行参数；启动/操作/排空/连接池常量由 config.go 的
	// 共享固定值提供（startupTimeout/operationTimeout/drainTimeout/
	// poolMaxOpen/poolMaxIdle/poolMaxLifetime），此处不重复声明。
	// Fixed runtime parameters owned by this file; the shared startup/
	// operation/drain/pool constants live in config.go (startupTimeout/
	// operationTimeout/drainTimeout/poolMaxOpen/poolMaxIdle/poolMaxLifetime)
	// and are not redeclared here.
	applicationName    = "binflow-download"
	connectTimeout     = 10 * time.Second
	downloadRouteRoot  = "/artifactory/"
	dsnMaxDatabaseLen  = 63
	dsnMaxSearchPath   = 63
	dsnMaxPort         = 65535
	pgEnvPrefix        = "PG"
	rebuiltDSNScheme   = "postgres"
	dsnSSLModeDisable  = "disable"
	dsnSSLModeVerify   = "verify-full"
	dsnSSLRootSystemCA = "system"
)

// 固定日志类别：仅输出类别文本；绝不输出原始错误原因、DSN、路径或配置值。
// Fixed log categories: category text only; never a raw error cause, DSN,
// path or configuration value.
const (
	logCategoryStartup      = "process: startup failed"
	logCategoryStartupClose = "process: startup rollback close failed"
)

// errInvalidPostgresDSN 是 DSN 解析的固定私有错误：不携带任何原始 pgx
// 解析细节，也不回显输入。
// errInvalidPostgresDSN is the fixed private DSN parse error: it carries no
// raw pgx parse detail and never echoes input.
var errInvalidPostgresDSN = errors.New("process: invalid postgres DSN")

// errPostgresEnvPresent 是下载模式下存在 PG* 环境键的固定私有错误；键名
// 与值都不进入错误文本或日志，也绝不改动全局环境。
// errPostgresEnvPresent is the fixed private error for a PG* environment key
// present in download mode; neither names nor values reach error text or
// logs, and the global environment is never mutated.
var errPostgresEnvPresent = errors.New("process: postgres environment present")

// Run 运行一次进程生命周期。ctx 非 nil；cfg 先经共享 validateConfig 校验
// （含手工构造的 Config），onReady 为可选可信回调，仅在监听器实际绑定且
// 最终 ctx 检查通过后以绑定地址调用一次，无绝对时序保证。所有失败都是
// 固定公共哨兵错误；启动期 ctx 取消仅汇入 context.Canceled/
// DeadlineExceeded。健康模式不触碰数据库/文件/驱动；下载模式按固定次序
// 组合真实依赖，任一步失败都只按获取逆序恰一次关闭已获取资源。
// Run executes one process lifetime. ctx must be nonnil; cfg passes the
// shared validateConfig first (manually constructed Configs included) and
// onReady is an optional trusted callback invoked exactly once with the bound
// address after the listener is actually bound and the final ctx check
// passes, with no absolute timing guarantee. Every failure is a fixed public
// sentinel error; startup-phase ctx cancellation joins only
// context.Canceled/DeadlineExceeded. Health mode touches no database, file or
// driver; download mode composes real dependencies in the fixed order, and
// any failure closes exactly once only the already acquired resources in
// reverse acquisition order.
func Run(ctx context.Context, cfg Config, onReady func(string)) error {
	if ctx == nil {
		return ErrStartup
	}
	if err := validateConfig(cfg); err != nil {
		return ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrStartup, err)
	}
	switch cfg.Mode {
	case ModeHealth:
		return runHealth(ctx, cfg, onReady)
	case ModeDownload:
		return runDownload(ctx, cfg, onReady)
	default:
		return ErrConfiguration
	}
}

// runHealth 仅构造既有健康处理器与 TCP 监听器：不做任何数据库/文件读取或
// 驱动解析，忽略 PG* 环境（进程对 PG 环境的显式隔离仅是下载模式的启动
// 前置）。closeOwned 为必需的无操作。
// runHealth builds only the existing health handler and a TCP listener: no
// database or file access and no driver parsing; PG* environment is ignored
// (the explicit PG environment isolation is a download-mode startup
// precondition only). closeOwned is the required no-op.
func runHealth(ctx context.Context, cfg Config, onReady func(string)) error {
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Print(logCategoryStartup)
		return ErrStartup
	}
	return serve(ctx, ln, newRouter(nil), drainTimeout, func() error { return nil }, onReady)
}

// runDownload 是真实下载组合：校验 → 拒绝 PG* 环境 → 严格 DSN 解析 →
// pgx stdlib 池 → Ping → 安全存储 → 只读节点池 → 文件存储 → 已认证下载
// 入口（构造即做 schema/目录核实）→ ctx 检查 → 绑定监听 → ctx 检查 →
// serve。无 DDL/种子/自动升级；启动失败按获取逆序（监听器、文件存储、
// 数据库池）恰一次关闭；serve 之后的资源所有权（先文件存储后数据库池）
// 转移给 serve 的 closeOwned。
// runDownload is the real download composition: validate → reject PG*
// environment → strict DSN parse → pgx stdlib pool → Ping → security store →
// read node pool → filestore → authenticated download entry (whose
// construction verifies schema and catalog) → ctx check → bind listener →
// ctx check → serve. No DDL, seeding or auto-upgrade; startup failures close
// exactly once in reverse acquisition order (listener, filestore, database
// pool); ownership after serve (filestore first, then the database pool)
// transfers to serve's closeOwned.
func runDownload(ctx context.Context, cfg Config, onReady func(string)) error {
	if err := rejectPostgresEnvironment(); err != nil {
		return ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrStartup, err)
	}
	startupCtx, startupCancel := context.WithTimeout(ctx, startupTimeout)
	defer startupCancel()
	connConfig, err := parsePostgresDSN(cfg.PostgresDSN)
	if err != nil {
		log.Print(logCategoryStartup)
		return ErrConfiguration
	}
	if startupCtx.Err() != nil {
		return startupError(startupCtx)
	}
	db := stdlib.OpenDB(*connConfig)
	db.SetMaxOpenConns(poolMaxOpen)
	db.SetMaxIdleConns(poolMaxIdle)
	db.SetConnMaxLifetime(poolMaxLifetime)

	if err := db.PingContext(startupCtx); err != nil {
		log.Print(logCategoryStartup)
		closeDBOnStartup(db)
		return startupError(ctx)
	}
	backend, err := security.NewStore(db, operationTimeout)
	if err != nil {
		log.Print(logCategoryStartup)
		closeDBOnStartup(db)
		return ErrStartup
	}
	nodes, err := refpg.NewReadPool(db, operationTimeout)
	if err != nil {
		log.Print(logCategoryStartup)
		closeDBOnStartup(db)
		return ErrStartup
	}
	if startupCtx.Err() != nil {
		closeDBOnStartup(db)
		return startupError(startupCtx)
	}
	fs, err := filestore.New(cfg.FilestoreRoot)
	if err != nil {
		log.Print(logCategoryStartup)
		closeDBOnStartup(db)
		return ErrStartup
	}
	download, err := security.NewAuthenticatedDownload(startupCtx, cfg.HTTP, backend, nodes, fs)
	if err != nil {
		log.Print(logCategoryStartup)
		closeOwnedOnStartup(fs, db)
		return ErrStartup
	}
	if err := startupCtx.Err(); err != nil {
		closeOwnedOnStartup(fs, db)
		return startupError(startupCtx)
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Print(logCategoryStartup)
		closeOwnedOnStartup(fs, db)
		return ErrStartup
	}
	if err := startupCtx.Err(); err != nil {
		ln.Close()
		closeOwnedOnStartup(fs, db)
		return startupError(startupCtx)
	}
	// 优雅停机的资源关闭次序固定：先文件存储后数据库池；关闭错误被清洗为
	// 公共 ErrResources 哨兵，绝不外泄原始关闭错误。
	// The graceful-shutdown close order is fixed: filestore first, then the
	// database pool; close errors are sanitized into the public ErrResources
	// sentinel and never leak the raw cause.
	closeOwned := func() error {
		var failed bool
		if err := fs.Close(); err != nil {
			failed = true
		}
		if err := db.Close(); err != nil {
			failed = true
		}
		if failed {
			return ErrResources
		}
		return nil
	}
	return serve(ctx, ln, newRouter(download), drainTimeout, closeOwned, onReady)
}

// startupError 把启动失败固定为 ErrStartup；父 ctx 已取消时仅额外汇入
// context.Canceled/DeadlineExceeded 这两个安全错误。
// startupError fixes a startup failure as ErrStartup; an already canceled
// parent ctx additionally joins only the safe context.Canceled/
// DeadlineExceeded errors.
func startupError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrStartup, err)
	}
	return ErrStartup
}

// closeDBOnStartup 在仅获取到数据库池阶段的启动回滚中恰一次关闭池；关闭
// 失败只记录固定类别。
// closeDBOnStartup closes the pool exactly once during startup rollback at
// the pool-only stage; close failures log the fixed category only.
func closeDBOnStartup(db *sql.DB) {
	if err := db.Close(); err != nil {
		log.Print(logCategoryStartupClose)
	}
}

// closeOwnedOnStartup 在获取到文件存储与数据库池后的启动回滚中按固定
// 次序（先文件存储后池）恰一次关闭；关闭失败只记录固定类别。
// closeOwnedOnStartup closes the filestore then the pool exactly once (fixed
// order) during startup rollback after both were acquired; close failures log
// the fixed category only.
func closeOwnedOnStartup(fs *filestore.Store, db *sql.DB) {
	if err := fs.Close(); err != nil {
		log.Print(logCategoryStartupClose)
	}
	if err := db.Close(); err != nil {
		log.Print(logCategoryStartupClose)
	}
}

// rejectPostgresEnvironment 在驱动解析前拒绝一切存在的 PG 前缀环境键
// （BINFLOW_TEST_POSTGRES_DSN 不以 PG 开头，不受影响）：这是显式本地隔离
// 政策，不是 pgx ParseConfig 具备隔离环境 API 的声明（它没有）。绝不
// 修改全局环境，也绝不输出键名或键值。
// rejectPostgresEnvironment rejects every present PG-prefixed environment
// key before any driver parse (BINFLOW_TEST_POSTGRES_DSN does not start with
// PG and is unaffected): this is an explicit local isolation policy, not a
// claim that pgx ParseConfig offers an environment-isolated API (it does
// not). The global environment is never mutated and no key name or value is
// ever printed.
func rejectPostgresEnvironment() error {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, pgEnvPrefix) {
			return errPostgresEnvPresent
		}
	}
	return nil
}

// parsePostgresDSN 把原始 DSN 解析为受限的 pgx 连接配置。本地方案：仅接受
// 小写 postgres/postgresql URL 形式（拒绝 keyword/value、opaque、fragment）；
// 单主机 + 显式数字端口 1..65535；用户名与非空口令必须显式给出（避免
// .pgpass）；数据库名 1..63 个 ASCII 字母数字/_/.- 字符；query 恰一个
// sslmode（仅 disable/verify-full）加可选单个 search_path（ASCII 标识符
// 1..63，本地选择：[A-Za-z0-9_$] 且不以数字开头），其余/重复键一律拒绝。
// sslmode=disable 仅允许字面回环 IP 数据库主机（IPv4/IPv6，含 4-in-6 映射
// 归一）；verify-full 使用系统 CA。随后以白名单字段重建安全 URL（显式
// 空 sslcert/sslkey/passfile/servicefile，verify-full 时 sslrootcert=system）
// 交给 pgx.ParseConfig，其原始错误被丢弃（不保留、不记录、不 Unwrap），
// 并复核无 fallback、verify-full 必有 TLSConfig；固定 ConnectTimeout=10s
// 与 application_name=binflow-download，保留显式 search_path。这是有界的
// 本地数据库画像：不含自定 CA/mTLS/service/passfile。
// parsePostgresDSN parses the raw DSN into a restricted pgx connection
// configuration. Local profile: only lowercase postgres/postgresql URL form
// (keyword/value, opaque and fragment forms rejected); one host plus an
// explicit numeric port 1..65535; the username and a nonempty password must
// be explicit (avoiding .pgpass); the database name is 1..63 ASCII
// alphanumeric/_/.- characters; the query carries exactly one sslmode
// (disable/verify-full only) plus an optional single search_path (an ASCII
// identifier 1..63 long; local choice: [A-Za-z0-9_$] not starting with a
// digit) — every other or duplicated key is rejected. sslmode=disable is
// allowed only for a literal loopback IP database host (IPv4/IPv6 including
// 4-in-6 unmapping); verify-full uses system CAs. A safe URL is then rebuilt
// from whitelisted fields (explicit empty sslcert/sslkey/passfile/servicefile
// and sslrootcert=system for verify-full) and handed to pgx.ParseConfig,
// whose raw error is discarded (never retained, logged or unwrapped); the
// result is re-verified to carry no fallbacks and a nonnil TLSConfig for
// verify-full. ConnectTimeout is fixed at 10s with
// application_name=binflow-download, retaining the explicit search_path.
// This is a bounded local database profile: no custom CA, mTLS, service or
// passfile support yet.
func parsePostgresDSN(raw string) (*pgx.ConnConfig, error) {
	if raw == "" {
		return nil, errInvalidPostgresDSN
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errInvalidPostgresDSN
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return nil, errInvalidPostgresDSN
	}
	if u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") {
		return nil, errInvalidPostgresDSN
	}
	if u.User == nil {
		return nil, errInvalidPostgresDSN
	}
	username := u.User.Username()
	password, hasPassword := u.User.Password()
	if username == "" || !hasPassword || password == "" {
		return nil, errInvalidPostgresDSN
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, ",/%\t ") {
		// 恰一个主机：拒绝主机列表、Unix 套接字路径与任何百分号编码残留。
		// Exactly one host: host lists, Unix socket paths and any residual
		// percent encoding are rejected.
		return nil, errInvalidPostgresDSN
	}
	portText := u.Port()
	if portText == "" {
		return nil, errInvalidPostgresDSN
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > dsnMaxPort {
		return nil, errInvalidPostgresDSN
	}
	database := strings.TrimPrefix(u.Path, "/")
	if !validDSNDatabaseName(database) {
		return nil, errInvalidPostgresDSN
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errInvalidPostgresDSN
	}
	sslmode := ""
	searchPath := ""
	for key, values := range query {
		switch key {
		case "sslmode":
			if len(values) != 1 || (values[0] != dsnSSLModeDisable && values[0] != dsnSSLModeVerify) {
				return nil, errInvalidPostgresDSN
			}
			sslmode = values[0]
		case "search_path":
			if len(values) != 1 || !validDSNSearchPath(values[0]) {
				return nil, errInvalidPostgresDSN
			}
			searchPath = values[0]
		default:
			return nil, errInvalidPostgresDSN
		}
	}
	if sslmode == "" {
		return nil, errInvalidPostgresDSN
	}
	if sslmode == dsnSSLModeDisable {
		addr, addrErr := netip.ParseAddr(host)
		if addrErr != nil {
			return nil, errInvalidPostgresDSN
		}
		addr = addr.Unmap()
		if !addr.IsValid() || !addr.IsLoopback() {
			return nil, errInvalidPostgresDSN
		}
	}

	// 仅以白名单字段重建安全 URL；显式空值覆盖 pgx 的 home 目录派生默认
	// （sslcert/sslkey/passfile/servicefile），verify-full 用系统 CA。
	// Rebuild the safe URL from whitelisted fields only; the explicit empty
	// values override pgx's home-directory-derived defaults (sslcert/sslkey/
	// passfile/servicefile) and verify-full uses the system CA pool.
	safeQuery := url.Values{}
	safeQuery.Set("sslmode", sslmode)
	safeQuery.Set("sslcert", "")
	safeQuery.Set("sslkey", "")
	safeQuery.Set("passfile", "")
	safeQuery.Set("servicefile", "")
	if sslmode == dsnSSLModeVerify {
		safeQuery.Set("sslrootcert", dsnSSLRootSystemCA)
	} else {
		safeQuery.Set("sslrootcert", "")
	}
	if searchPath != "" {
		safeQuery.Set("search_path", searchPath)
	}
	safe := url.URL{
		Scheme:   rebuiltDSNScheme,
		User:     url.UserPassword(username, password),
		Host:     u.Host,
		Path:     u.Path,
		RawQuery: safeQuery.Encode(),
	}
	connConfig, err := pgx.ParseConfig(safe.String())
	if err != nil {
		// 原始 ParseConfigError 在此丢弃：不保留、不记录、不包装。
		// The raw ParseConfigError is discarded here: never retained,
		// logged or wrapped.
		return nil, errInvalidPostgresDSN
	}
	if connConfig.Host != host || connConfig.Port != uint16(port) ||
		connConfig.User != username || connConfig.Database != database ||
		len(connConfig.Fallbacks) != 0 {
		return nil, errInvalidPostgresDSN
	}
	if connConfig.Password == "" {
		return nil, errInvalidPostgresDSN
	}
	if sslmode == dsnSSLModeVerify && connConfig.TLSConfig == nil {
		return nil, errInvalidPostgresDSN
	}
	connConfig.ConnectTimeout = connectTimeout
	if connConfig.RuntimeParams == nil {
		connConfig.RuntimeParams = make(map[string]string)
	}
	connConfig.RuntimeParams["application_name"] = applicationName
	if searchPath != "" {
		connConfig.RuntimeParams["search_path"] = searchPath
	}
	return connConfig, nil
}

// validDSNDatabaseName 要求数据库名为 1..63 个 ASCII [A-Za-z0-9_.-] 字符。
// validDSNDatabaseName requires 1..63 ASCII [A-Za-z0-9_.-] characters.
func validDSNDatabaseName(name string) bool {
	if name == "" || len(name) > dsnMaxDatabaseLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '.' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// validDSNSearchPath 要求 1..63 个 ASCII [A-Za-z0-9_$] 且不以数字开头；
// 这是本地显式选择的有界标识符画像，不接受引号、逗号或多 schema 列表。
// validDSNSearchPath requires 1..63 ASCII [A-Za-z0-9_$] characters not
// starting with a digit; this bounded identifier profile is an explicit local
// choice accepting no quoting, commas or multi-schema lists.
func validDSNSearchPath(name string) bool {
	if name == "" || len(name) > dsnMaxSearchPath {
		return false
	}
	if name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '_' || c == '$' {
			continue
		}
		return false
	}
	return true
}

// newRouter 返回根处理器：仅当 raw RequestURI 以 /artifactory/ 开头时原样
// （不解码、不清洗、不 unescape、不经 chi 挂载）分发给已认证下载处理器；
// 其余一切请求进入既有健康处理器（/healthz、405、404 语义逐字不变）。
// 无效的原始编码/绝对形式请求因不带该前缀而留在健康侧的有限本地排除内；
// URL.Path 单独永远不能选中下载。download 为 nil 时行为恒等于既有健康
// 处理器（健康模式）。
// newRouter returns the root handler: only a raw RequestURI starting with
// /artifactory/ is dispatched verbatim (no decoding, cleaning, unescaping or
// chi mounting) to the authenticated download handler; every other request
// reaches the existing health handler (/healthz, 405 and 404 semantics
// byte-identical). Invalid raw-encoded or absolute-form requests carry no
// such prefix and remain inside the health side's finite local exclusion;
// URL.Path alone can never select download. A nil download keeps behavior
// identical to the existing health handler (health mode).
func newRouter(download http.Handler) http.Handler {
	health := httpserver.NewHandler()
	if download == nil {
		return health
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.RequestURI, downloadRouteRoot) {
			download.ServeHTTP(w, r)
			return
		}
		health.ServeHTTP(w, r)
	})
}

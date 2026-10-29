package security

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// 下载入口的固定本地边界：Authorization 头值与 raw URI 的字节上限、
// Basic 前缀、口令字节上限与 repo 键长度。均为显式有限政策，不做任何
// 解码/清洗之外的放宽。
// Fixed local bounds for the download entry: the Authorization header-value
// and raw URI byte caps, the Basic prefix, the password byte cap and the
// repository key length. All are explicit finite policies; nothing beyond
// strict decoding is ever relaxed.
const (
	authHeaderName         = "Authorization"
	authMaxHeaderBytes     = 8192
	authBasicSchemePrefix  = "Basic "
	authMaxPasswordBytes   = 1024
	downloadRawMaxURIBytes = 8192
	downloadArtifactRoot   = "/artifactory/"
	entryMaxRepoKeyLen     = 64
	gateMinThreshold       = 1
	gateMaxThreshold       = 100
	gateMinUsers           = 1
	gateMaxUsers           = 4096
	gateMinWindow          = time.Second
	gateMaxBound           = time.Hour
)

// entryErrorBodies 复用 BIN199 的固定英文 LF 错误正文；不含输入或内部
// 细节。入口错误表示不携带下载表示头。
// entryErrorBodies reuses BIN199's fixed English LF error bodies without
// inputs or internals. Entry error responses carry no download
// representation headers.
var entryErrorBodies = map[int]string{
	http.StatusBadRequest:          "bad request\n",
	http.StatusUnauthorized:        "unauthorized\n",
	http.StatusForbidden:           "forbidden\n",
	http.StatusNotFound:            "not found\n",
	http.StatusInternalServerError: "internal error\n",
}

// entryConfigError 是构造失败的固定错误：Error() 只含固定文案，Unwrap 同时
// 暴露 ErrConfiguration 哨兵与原始原因，供可信调用方诊断；不回显输入。
// entryConfigError is the fixed constructor failure: Error() prints only the
// fixed text while Unwrap exposes both the ErrConfiguration sentinel and the
// original cause for trusted callers; inputs are never echoed.
type entryConfigError struct {
	stage string
	cause error
}

func (e *entryConfigError) Error() string {
	return ErrConfiguration.Error() + ": " + e.stage
}

func (e *entryConfigError) Unwrap() []error {
	return []error{ErrConfiguration, e.cause}
}

func newEntryConfigError(stage string, cause error) *entryConfigError {
	return &entryConfigError{stage: stage, cause: cause}
}

const (
	entryStageValidate  = "entry startup validation"
	entryStageCatalog   = "entry startup repository catalog"
	entryStageAssembly  = "entry handler assembly"
	entryStageGateFull  = "entry failure gate capacity"
	entryStagePrincipal = "entry principal verification"
	entryStageBridge    = "entry authorization bridge"
)

// principalContextKey 是包私有主体上下文键；不提供导出注入或读取入口，
// 只有本入口在真实认证成功后铸造主体上下文。
// principalContextKey is the package-private principal context key; no
// exported setter or getter exists and only this entry mints the principal
// context after real authentication succeeds.
type principalContextKey struct{}

// 同次目录事实只能由可信入口写入。
// Same-request catalog facts are written only by the trusted entry.
type repositoryContextKey struct{}

func withPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func principalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

// NewAuthenticatedDownload 组合真实认证入口、有界失败门、仓库目录分发与
// 既有独立下载适配器；本构造器即完整组合缝，不挂接生产路由，不拥有任何
// 客户端凭据或数据库/文件系统生命周期。typed nil 依赖不支持。
// 启动即做 backend.Validate 与配置仓库 LookupRepository + 形状校验，失败
// 返回 nil 与包装 ErrConfiguration 的固定错误。
// NewAuthenticatedDownload composes the real authentication entry, the
// bounded failure gate, catalog dispatch and the existing standalone download
// adapter; the constructor itself is the complete composition seam. It mounts
// no production route and owns no client credential or database/filesystem
// lifecycle. Typed nils are unsupported. Startup performs backend.Validate,
// LookupRepository of the configured key plus shape checks; any failure
// returns nil with a fixed error wrapping ErrConfiguration.
func NewAuthenticatedDownload(ctx context.Context, cfg HTTPConfig, backend Backend, nodes localrepo.FileReadStore, content localrepo.FileOpener) (http.Handler, error) {
	if ctx == nil {
		return nil, newEntryConfigError(entryStageValidate, errors.New("nil context"))
	}
	if err := ctx.Err(); err != nil {
		return nil, newEntryConfigError(entryStageValidate, err)
	}
	if backend == nil || nodes == nil || content == nil {
		return nil, newEntryConfigError(entryStageValidate, errors.New("nil dependency"))
	}
	if !entryValidRepoKey(cfg.RepoKey) {
		return nil, newEntryConfigError(entryStageValidate, errors.New("invalid repository key"))
	}
	if !entryValidChallenge(cfg.Challenge) {
		return nil, newEntryConfigError(entryStageValidate, errors.New("invalid challenge"))
	}
	if cfg.FailureThreshold < gateMinThreshold || cfg.FailureThreshold > gateMaxThreshold {
		return nil, newEntryConfigError(entryStageValidate, errors.New("invalid failure threshold"))
	}
	if cfg.FailureWindow < gateMinWindow || cfg.FailureWindow > gateMaxBound {
		return nil, newEntryConfigError(entryStageValidate, errors.New("invalid failure window"))
	}
	if cfg.BlockDuration < gateMinWindow || cfg.BlockDuration > gateMaxBound {
		return nil, newEntryConfigError(entryStageValidate, errors.New("invalid block duration"))
	}
	if cfg.MaxTrackedUsers < gateMinUsers || cfg.MaxTrackedUsers > gateMaxUsers {
		return nil, newEntryConfigError(entryStageValidate, errors.New("invalid tracked user bound"))
	}
	report := cfg.Report
	if report == nil {
		report = func(f httpserver.DownloadFailure) {
			entryDefaultReport(f)
		}
	}
	if err := backend.Validate(ctx); err != nil {
		return nil, newEntryConfigError(entryStageValidate, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, newEntryConfigError(entryStageValidate, err)
	}
	repo, err := backend.LookupRepository(ctx, cfg.RepoKey)
	if err != nil {
		return nil, newEntryConfigError(entryStageCatalog, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, newEntryConfigError(entryStageCatalog, err)
	}
	if !supportedRepository(repo) || repo.Key != cfg.RepoKey {
		return nil, newEntryConfigError(entryStageCatalog, errors.New("unsupported repository profile"))
	}
	bridge := &downloadAuthBridge{backend: backend}
	inner, err := httpserver.NewDownloadHandler(httpserver.DownloadDeps{
		RepoKey: cfg.RepoKey,
		Auth:    bridge,
		Store:   nodes,
		Content: content,
		Report:  report,
	})
	if err != nil {
		return nil, newEntryConfigError(entryStageAssembly, err)
	}
	return &authenticatedDownload{
		backend:   backend,
		repoKey:   cfg.RepoKey,
		challenge: cfg.Challenge,
		gate: newFailureGate(gateLimits{
			threshold: cfg.FailureThreshold,
			window:    cfg.FailureWindow,
			block:     cfg.BlockDuration,
			maxUsers:  cfg.MaxTrackedUsers,
		}),
		inner:  inner,
		report: report,
		now:    time.Now,
	}, nil
}

// entryDefaultReport 是无 Report 配置时的仅类别固定日志；不输出任何请求
// 数据、凭据或内部错误。
// entryDefaultReport is the category-only fixed logger used when Report is
// unset; it never logs request data, credentials or internal errors.
func entryDefaultReport(f httpserver.DownloadFailure) {
	log.Printf("security: download failure: %s", string(f))
}

// authenticatedDownload 是已认证下载入口：认证/失败门 → 仓库目录分发 →
// 既有适配器（画像/query/Range 守卫、授权桥、单次节点查询与传输）。now
// 为包私有可注入时钟，仅供同包确定性测试使用，不是公开全局时钟。
// authenticatedDownload is the authenticated download entry: authentication
// and failure gate, then catalog dispatch, then the existing adapter (profile/
// query/Range guards, the authorization bridge, a single node lookup and the
// transfer). now is a package-private injectable clock for same-package
// deterministic tests only, never a public global clock.
type authenticatedDownload struct {
	backend   Backend
	repoKey   string
	challenge string
	gate      *failureGate
	inner     http.Handler
	report    func(httpserver.DownloadFailure)
	now       func() time.Time
}

// ServeHTTP 的固定次序：请求取消中止 → 恰一个 Authorization 值与有限
// Basic 解码(401) → 失败门(阻断403带challenge，不做后端/节点工作) →
// backend 真实认证(401/500) → 铸造私有主体上下文 → raw URI 前缀与 repo
// 键解析(400) → 仓库目录(缺404/非配置404/形状或错误500) → 委派既有适配器。
// ServeHTTP runs the fixed order: request cancellation abort, exactly one
// Authorization value with bounded Basic decoding(401), the failure gate
// (blocked 403 WITH challenge, no backend or node work), real backend
// authentication (401/500), minting the private principal context, raw-URI
// prefix and repository key resolution(400), catalog dispatch (absent 404,
// nonconfigured 404, shape or store failure 500) and delegation to the
// existing adapter.
func (h *authenticatedDownload) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.Context().Err(); err != nil {
		h.report(httpserver.DownloadFailureCancel)
		panic(http.ErrAbortHandler)
	}
	credential, key, ok := h.parseAuthorization(r)
	if !ok {
		h.writeError(w, r.Method, http.StatusUnauthorized, h.challenge)
		return
	}
	if h.gate.blocked(key, h.now()) {
		h.report(httpserver.DownloadFailureAuth)
		h.writeError(w, r.Method, http.StatusForbidden, h.challenge)
		return
	}
	principal, err := h.backend.Authenticate(r.Context(), credential.username, credential.password)
	if err != nil {
		if r.Context().Err() != nil {
			h.report(httpserver.DownloadFailureCancel)
			panic(http.ErrAbortHandler)
		}
		h.handleAuthenticateError(w, r, key, err)
		return
	}
	if r.Context().Err() != nil {
		h.report(httpserver.DownloadFailureCancel)
		panic(http.ErrAbortHandler)
	}
	canonical, canonicalOK := normalizeUsername(principal.Username())
	if principal.ID() <= 0 || !canonicalOK || canonical != principal.Username() {
		// 后端返回 nil 错误时主体仍必须非零且规范；零主体是内部失败。
		// A backend returning nil error must still yield a nonzero,
		// canonical principal; a zero principal is an internal failure.
		h.report(httpserver.DownloadFailureAuth)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return
	}
	h.gate.recordSuccess(key)
	r = r.WithContext(withPrincipal(r.Context(), principal))
	rawKey, ok := entryResolveRepoKey(r.RequestURI)
	if !ok {
		h.writeError(w, r.Method, http.StatusBadRequest, "")
		return
	}
	repo, status := h.resolveRepository(r, rawKey)
	if status != 0 {
		h.writeError(w, r.Method, status, "")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), repositoryContextKey{}, repo))
	h.inner.ServeHTTP(w, r)
}

// handleAuthenticateError 分类认证失败：仅 ErrInvalidCredentials 计入失败
// 门并回答401；ErrAccountDisabled 同正文401但不计数（本地隐私政策）；
// 编码/存储等其他失败500且不计数；门容量无法记录新负向条目时为运行
// 500，绝不通过驱逐仍被阻断的身份来绕过阻断。
// handleAuthenticateError classifies authentication failures: only
// ErrInvalidCredentials counts toward the gate and answers 401;
// ErrAccountDisabled answers the same 401 body without counting (a local
// privacy policy); encoding/store and other failures are 500 without
// counting; an inability to record a new negative entry is an operational
// 500 — a still-blocked identity is never evicted to bypass blocking.
func (h *authenticatedDownload) handleAuthenticateError(w http.ResponseWriter, r *http.Request, key string, err error) {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		if capErr := h.gate.recordFailure(key, h.now()); capErr != nil {
			h.report(httpserver.DownloadFailureAuth)
			h.writeError(w, r.Method, http.StatusInternalServerError, "")
			return
		}
		h.writeError(w, r.Method, http.StatusUnauthorized, h.challenge)
	case errors.Is(err, ErrAccountDisabled):
		h.writeError(w, r.Method, http.StatusUnauthorized, h.challenge)
	default:
		h.report(httpserver.DownloadFailureAuth)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
	}
}

// resolveRepository 每请求重新读取目录：缺席404；读取失败500(lookup)；
// 行形状不支持500；已知但非配置仓库404（本地单仓库排除，不暴露存在性）。
// 返回0表示分发继续；同次请求的目录事实由授权桥消费，CanRead 的 SQL
// 本身会重新校验当前仓库存在性与授权状态。
// resolveRepository re-reads the catalog per request: absence is 404, read
// failures are 500(lookup), unsupported row shapes are 500, and a known but
// nonconfigured repository is 404 (the local single-repository exclusion,
// disclosing no existence). Zero means dispatch continues; the bridge holds
// the same-request catalog facts; CanRead also rechecks current repository
// existence and authorization state.
func (h *authenticatedDownload) resolveRepository(r *http.Request, rawKey string) (RepositoryInfo, int) {
	repo, err := h.backend.LookupRepository(r.Context(), rawKey)
	if r.Context().Err() != nil {
		h.report(httpserver.DownloadFailureCancel)
		panic(http.ErrAbortHandler)
	}
	if err != nil {
		if errors.Is(err, ErrRepositoryAbsent) {
			return RepositoryInfo{}, http.StatusNotFound
		}
		h.report(httpserver.DownloadFailureLookup)
		return RepositoryInfo{}, http.StatusInternalServerError
	}
	if !supportedRepository(repo) || repo.Key != rawKey {
		h.report(httpserver.DownloadFailureLookup)
		return RepositoryInfo{}, http.StatusInternalServerError
	}
	if rawKey != h.repoKey {
		return RepositoryInfo{}, http.StatusNotFound
	}
	return repo, 0
}

// basicCredential 是一次有限 Basic 解码的结果；username 保持解码原文
// （后端自行归一化），password 逐字节保留不归一化。
// basicCredential is one bounded Basic decoding result; username keeps the
// decoded raw text (the backend normalizes itself) and the password is kept
// byte-for-byte without normalization.
type basicCredential struct {
	username string
	password string
}

// parseAuthorization 只接受恰一个 Authorization 值：长度有界、大小写不敏
// 感 Basic 前缀、标准 Base64 解码、整个凭据为合法 UTF-8、首个冒号切分、
// 用户名可有限归一化（作为失败门的不受信索引）、口令 1..1024 字节。任何
// 其他方案（含 Bearer）、缺失、重复或畸形值一律 401 challenge。绝不回退
// 到 cookie、用户/代理转发头。返回归一化用户名键供失败门使用。
// parseAuthorization accepts exactly one Authorization value only: bounded
// length, a case-insensitive Basic prefix, standard Base64 decoding, valid
// UTF-8 for the whole credential, a first-colon split, a finitely
// normalizable username (used only as the gate's untrusted index) and a
// 1..1024 byte password. Any other scheme (Bearer included), a missing,
// duplicated or malformed value answers 401 with the challenge. There is
// never a fallback to cookies or user/proxy headers. The normalized username
// key returned is for the failure gate only.
func (h *authenticatedDownload) parseAuthorization(r *http.Request) (basicCredential, string, bool) {
	values := r.Header.Values(authHeaderName)
	if len(values) != 1 {
		return basicCredential{}, "", false
	}
	value := values[0]
	if len(value) > authMaxHeaderBytes || len(value) <= len(authBasicSchemePrefix) {
		return basicCredential{}, "", false
	}
	if !strings.EqualFold(value[:len(authBasicSchemePrefix)-1], "Basic") ||
		value[len(authBasicSchemePrefix)-1] != ' ' {
		return basicCredential{}, "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(value[len(authBasicSchemePrefix):])
	if err != nil {
		return basicCredential{}, "", false
	}
	credential := string(decoded)
	if !utf8.ValidString(credential) {
		return basicCredential{}, "", false
	}
	username, password, hasColon := strings.Cut(credential, ":")
	if !hasColon {
		return basicCredential{}, "", false
	}
	key, ok := normalizeUsername(username)
	if !ok {
		return basicCredential{}, "", false
	}
	if len(password) == 0 || len(password) > authMaxPasswordBytes {
		return basicCredential{}, "", false
	}
	return basicCredential{username: username, password: password}, key, true
}

// entryResolveRepoKey 只在 raw RequestURI 上解析 /artifactory/{key}/... 的
// 仓库键：长度有界、在首个 '?' 前取段、键为单段 ASCII [A-Za-z0-9._-] 且
// 非 ./..、长度≤64。绝不 unescape 或 clean；后续完整路径画像由既有适配器
// 在原始 URI 上执行。
// entryResolveRepoKey resolves the /artifactory/{key}/... repository key
// from the raw RequestURI only: bounded length, the segment before the first
// '?', a single ASCII [A-Za-z0-9._-] key that is never ./.. and never longer
// than 64 bytes. It never unescapes or cleans; the adapter later applies the
// full path profile to the raw URI.
func entryResolveRepoKey(rawURI string) (string, bool) {
	if rawURI == "" || len(rawURI) > downloadRawMaxURIBytes {
		return "", false
	}
	pathPart := rawURI
	if i := strings.IndexByte(rawURI, '?'); i >= 0 {
		pathPart = rawURI[:i]
	}
	if !strings.HasPrefix(pathPart, downloadArtifactRoot) {
		return "", false
	}
	rest := pathPart[len(downloadArtifactRoot):]
	key, _, _ := strings.Cut(rest, "/")
	if !entryValidRepoKey(key) {
		return "", false
	}
	return key, true
}

// entryValidRepoKey 要求单段 ASCII [A-Za-z0-9._-]、非 ./.. 且 1..64 字节。
// entryValidRepoKey requires one ASCII [A-Za-z0-9._-] segment that is never
// ./.. and is 1..64 bytes long.
func entryValidRepoKey(repoKey string) bool {
	if repoKey == "" || len(repoKey) > entryMaxRepoKeyLen || repoKey == "." || repoKey == ".." {
		return false
	}
	for i := 0; i < len(repoKey); i++ {
		c := repoKey[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// entryValidChallenge 要求非空且全部为可打印 ASCII 0x20..0x7e。
// entryValidChallenge requires nonempty printable ASCII 0x20..0x7e.
func entryValidChallenge(challenge string) bool {
	if challenge == "" {
		return false
	}
	for i := 0; i < len(challenge); i++ {
		if challenge[i] < 0x20 || challenge[i] > 0x7e {
			return false
		}
	}
	return true
}

// supportedRepository 只接受精确 local/generic 且 revision>0 的记录；
// 不含对其他类型/版本的隐式支持。
// supportedRepository accepts exactly local/generic records with
// revision>0; no other kind/version is implicitly supported.
func supportedRepository(repo RepositoryInfo) bool {
	return repo.Kind == "local" && repo.PackageType == "generic" && repo.Revision > 0
}

// writeError 写固定错误表示：仅401与被阻断403设合法 challenge；GET/HEAD
// 均显式 Content-Length，HEAD 永不调用 Write。不回显输入或内部错误，不
// 携带任何下载表示头。
// writeError emits the fixed error representation: a valid challenge is set
// only for 401 and the blocked 403; both GET and HEAD set an explicit
// Content-Length and HEAD never calls Write. Inputs and internals are never
// echoed and no download representation headers are attached.
func (h *authenticatedDownload) writeError(w http.ResponseWriter, method string, status int, challenge string) {
	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	body := entryErrorBodies[status]
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if method != http.MethodHead {
		w.Write([]byte(body))
	}
}

// downloadAuthBridge 是适配器 DownloadAuth 端口的真实实现：只消费本入口
// 铸造的私有主体上下文，绝不重新把请求头解析成主体；对当前 principal×
// 目录事实×存储 Path 调用 backend.CanRead，无任何正向缓存。
// downloadAuthBridge is the real implementation of the adapter's
// DownloadAuth port: it consumes only the private principal context minted
// by this entry, never re-parses request headers into a principal, and calls
// backend.CanRead with the current principal, catalog facts and storage
// Path. There is no positive cache.
type downloadAuthBridge struct {
	backend Backend
}

// Authorize 在节点查询前执行路径 read 策略：上下文缺失/零主体或路径与
// 目录事实不符按内部错误处理；CanRead 失败原样上抛（适配器映射500与
// Auth 类别，依赖自身超时且请求仍活跃时亦为500）；false→Denied（403 无
// challenge），true→Allow。每次请求重新读取当前授权状态。
// Authorize applies the path read policy before any node lookup: a missing
// context, zero principal or path/catalog mismatch is an internal error;
// CanRead failures pass through unchanged (the adapter maps them to 500 with
// the Auth category, including a dependency's own timeout while the request
// stays active); false yields Denied (403 without challenge) and true
// yields Allow. Authorization state is re-read fresh for every request.
func (b *downloadAuthBridge) Authorize(ctx context.Context, r *http.Request, path repository.Path) (httpserver.DownloadAuthResult, error) {
	principal, ok := principalFromContext(r.Context())
	if !ok || principal.ID() <= 0 || principal.Username() == "" {
		return httpserver.DownloadAuthResult{}, errors.New(entryStagePrincipal)
	}
	repo, repoOK := r.Context().Value(repositoryContextKey{}).(RepositoryInfo)
	if !repoOK || !supportedRepository(repo) || path.Repo() != repo.Key {
		return httpserver.DownloadAuthResult{}, errors.New(entryStageBridge)
	}
	allowed, err := b.backend.CanRead(ctx, principal, repo, path)
	if err != nil {
		return httpserver.DownloadAuthResult{}, err
	}
	if !allowed {
		return httpserver.DownloadAuthResult{Decision: httpserver.DownloadDenied}, nil
	}
	return httpserver.DownloadAuthResult{Decision: httpserver.DownloadAllow}, nil
}

// gateLimits 汇聚失败门的有界配置；均来自显式 HTTPConfig。
// gateLimits bundles the bounded failure-gate settings, all taken from the
// explicit HTTPConfig.
type gateLimits struct {
	threshold int
	window    time.Duration
	block     time.Duration
	maxUsers  int
}

// failureEntry 记录一个不受信用户名索引的窗口内失败计数与临时阻断截止；
// 任何字段都不构成主体。
// failureEntry tracks one untrusted username index's in-window failure count
// and temporary block deadline; none of its fields form a principal.
type failureEntry struct {
	windowStart  time.Time
	failures     int
	blockedUntil time.Time
}

// failureGate 是进程本地、互斥锁保护的有界失败门：固定窗口与固定阻断
// 时长，无正向凭据/权限缓存。容量永远不会驱逐仍被阻断或仍在窗口内的
// 身份；只有过期条目被删除以约束内存。这不是持久化或分布式 Access
// 锁定的等价实现。
// failureGate is the process-local, mutex-guarded bounded failure gate with
// a fixed window and block duration and no positive credential/permission
// cache. Capacity never evicts a still-blocked or in-window identity; only
// expired entries are deleted to bound memory. This is not persistent or
// distributed Access lockout parity.
type failureGate struct {
	mu      sync.Mutex
	limits  gateLimits
	entries map[string]*failureEntry
}

func newFailureGate(limits gateLimits) *failureGate {
	return &failureGate{limits: limits, entries: make(map[string]*failureEntry)}
}

// blocked 报告该不受信索引当前是否被阻断；顺带删除已整体过期的条目。
// 窗口或阻断到期即重置条目。
// blocked reports whether the untrusted index is currently blocked while
// deleting entries that expired as a whole. Window or block expiry resets
// the entry.
func (g *failureGate) blocked(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.entries[key]
	if !ok {
		return false
	}
	if g.expired(entry, now) {
		delete(g.entries, key)
		return false
	}
	return entry.blockedUntil.After(now)
}

// recordSuccess 在真实认证成功后清除该索引的失败记录。
// recordSuccess clears the index's failure record after real authentication
// succeeds.
func (g *failureGate) recordSuccess(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.entries, key)
}

// recordFailure 记录一次 ErrInvalidCredentials：仍在窗口内则计数递增并
// 在达到阈值时设置阻断；窗口已过则重开窗口。无法记录新负向条目（容量
// 已满且无可回收条目）时返回非 nil 错误，由调用方回答运行500。
// recordFailure records one ErrInvalidCredentials: in-window entries
// increment and reach the threshold by setting the block; an expired window
// restarts. When no new negative entry can be recorded (capacity full with
// nothing reclaimable) it returns a nonnil error and the caller answers an
// operational 500.
func (g *failureGate) recordFailure(key string, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.prune(now)
	entry, ok := g.entries[key]
	if !ok {
		if len(g.entries) >= g.limits.maxUsers {
			return errors.New(entryStageGateFull)
		}
		entry := &failureEntry{windowStart: now, failures: 1}
		if g.limits.threshold == 1 {
			entry.blockedUntil = now.Add(g.limits.block)
		}
		g.entries[key] = entry
		return nil
	}
	if entry.blockedUntil.After(now) {
		// 已被阻断的索引不会再到达此处；防御性保持阻断不变。
		// A blocked index never reaches this path; defensively keep
		// the block unchanged.
		return nil
	}
	if !now.Before(entry.windowStart.Add(g.limits.window)) {
		entry.windowStart = now
		entry.failures = 0
	}
	entry.failures++
	if entry.failures >= g.limits.threshold {
		entry.blockedUntil = now.Add(g.limits.block)
	}
	return nil
}

// expired 判断条目是否整体过期：未阻断时窗口到期、或阻断截止已过；到期
// 即视为重置。
// expired decides whole-entry expiry: an expired window while unblocked, or
// a passed block deadline; expiry counts as a reset.
func (g *failureGate) expired(entry *failureEntry, now time.Time) bool {
	if entry.blockedUntil.IsZero() {
		return !now.Before(entry.windowStart.Add(g.limits.window))
	}
	return !now.Before(entry.blockedUntil)
}

// prune 只删除已整体过期的条目；仍在窗口内或仍被阻断的身份永不驱逐。
// prune deletes only whole-entry expired records; in-window or still-blocked
// identities are never evicted.
func (g *failureGate) prune(now time.Time) {
	for key, entry := range g.entries {
		if g.expired(entry, now) {
			delete(g.entries, key)
		}
	}
}

package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// 下载切片的固定本地边界：raw URI 上限、精确空 SHA1 与 ModifiedMillis
// 上界（UTC 9999-12-31 23:59:59.999）。均为显式有限政策。
// Fixed local bounds for this download slice: the raw URI cap, the exact
// empty SHA1 and the ModifiedMillis upper bound (UTC 9999-12-31 23:59:59.999).
// All are explicit finite policies.
const (
	downloadRootPrefix       = "/artifactory/"
	downloadMaxURIBytes      = 8192
	downloadEmptySHA1        = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	downloadMaxModifiedMilli = int64(253402300799999)
)

// downloadErrorBodies 是固定、不含输入与内部异常的原创错误正文。
// downloadErrorBodies are original fixed bodies without inputs or internals.
var downloadErrorBodies = map[int]string{
	http.StatusBadRequest:          "bad request\n",
	http.StatusUnauthorized:        "unauthorized\n",
	http.StatusForbidden:           "forbidden\n",
	http.StatusNotFound:            "not found\n",
	http.StatusMethodNotAllowed:    "method not allowed\n",
	http.StatusInternalServerError: "internal error\n",
	http.StatusNotImplemented:      "not implemented\n",
}

// errDownloadProjection 是本地投影校验的固定哨兵：区分投影失败与存储错误。
// errDownloadProjection is the fixed local projection sentinel separating
// validation failures from storage errors.
var errDownloadProjection = errors.New("httpserver: invalid download source projection")

// DownloadAuthDecision 由可信授权层显式给出，零值无效。
// DownloadAuthDecision requires an explicit trusted decision; zero is invalid.
type DownloadAuthDecision uint8

const (
	DownloadAllow DownloadAuthDecision = iota + 1
	DownloadUnauthenticated
	DownloadDenied
)

// DownloadAuthResult 不提供默认允许；Challenge 仅用于401。
// DownloadAuthResult has no default allow; Challenge belongs only to 401.
type DownloadAuthResult struct {
	Decision  DownloadAuthDecision
	Challenge string
}

// DownloadAuth 必须在节点查询之前给出决定；不在此实现认证机制。
// DownloadAuth decides before lookup and supplies the trusted auth mechanism.
type DownloadAuth interface {
	Authorize(context.Context, *http.Request, repository.Path) (DownloadAuthResult, error)
}

// DownloadFailure 为固定、不含请求内容的诊断类别。
// DownloadFailure is a fixed diagnostic category without request data.
type DownloadFailure string

const (
	DownloadFailureAuth       DownloadFailure = "auth"
	DownloadFailureLookup     DownloadFailure = "lookup"
	DownloadFailureProjection DownloadFailure = "projection"
	DownloadFailureOpen       DownloadFailure = "open"
	DownloadFailureStream     DownloadFailure = "stream"
	DownloadFailureClose      DownloadFailure = "close"
	DownloadFailureCancel     DownloadFailure = "cancel"
)

// DownloadDeps 由注入者持有；Report 可空，回调须并发安全且不 panic。
// DownloadDeps remain injector-owned; callbacks must be safe and nonpanicking.
type DownloadDeps struct {
	RepoKey string
	Auth    DownloadAuth
	Store   localrepo.FileReadStore
	Content localrepo.FileOpener
	Report  func(DownloadFailure)
}

// ErrDownloadConfiguration 是不含输入的构造错误。
// ErrDownloadConfiguration is the fixed constructor error without inputs.
var ErrDownloadConfiguration = errors.New("httpserver: invalid download configuration")

// NewDownloadHandler 构造独立处理器，不挂接生产路由；typed nil 不支持。
// nil Auth/Store/Content 或非法 RepoKey 返回 ErrDownloadConfiguration；
// 不提供默认允许或默认匿名。Report 可空，空值使用仅含类别的固定日志。
// NewDownloadHandler creates an unmounted handler; typed nils are unsupported.
// nil Auth/Store/Content or an invalid RepoKey returns ErrDownloadConfiguration;
// no default allow or anonymous fallback exists. A nil Report uses a fixed
// category-only logger.
func NewDownloadHandler(deps DownloadDeps) (http.Handler, error) {
	if deps.Auth == nil || deps.Store == nil || deps.Content == nil {
		return nil, ErrDownloadConfiguration
	}
	if !validDownloadRepoKey(deps.RepoKey) {
		return nil, ErrDownloadConfiguration
	}
	report := deps.Report
	if report == nil {
		report = func(f DownloadFailure) {
			log.Printf("httpserver: download failure: %s", string(f))
		}
	}
	return &downloadHandler{
		repoKey: deps.RepoKey,
		auth:    deps.Auth,
		store:   deps.Store,
		content: deps.Content,
		report:  report,
	}, nil
}

// downloadHandler 持有注入依赖；依赖生命周期归注入者，本处理器不关闭
// 存储或管理事务。
// downloadHandler holds injected dependencies; the injector owns their
// lifetimes and no store close or transaction control happens here.
type downloadHandler struct {
	repoKey string
	auth    DownloadAuth
	store   localrepo.FileReadStore
	content localrepo.FileOpener
	report  func(DownloadFailure)
}

// ServeHTTP 按固定全序执行：方法(405) → raw URI 画像/query(400) →
// Range/任一 If-* 头(501) → 取消检查 → 授权(401/403/500) → 取消检查 →
// 校验查询/投影(404/500) → GET Open → 取消检查 → 表示头。未授权、域外
// 与未支持请求都不查询节点，不暴露存在性。
// ServeHTTP enforces the fixed total order: method(405) → raw URI profile/
// query(400) → Range/any If-* header(501) → cancellation check →
// authorization(401/403/500) → cancellation check → validated lookup/
// projection(404/500) → GET Open → cancellation check → representation
// headers. Unauthorized, out-of-profile and unsupported requests never query
// the store and disclose no existence.
func (h *downloadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.writeError(w, r.Method, http.StatusMethodNotAllowed, "")
		return
	}
	path, ok := h.parseRawPath(r)
	if !ok {
		h.writeError(w, r.Method, http.StatusBadRequest, "")
		return
	}
	if downloadUnsupportedHeader(r.Header) {
		h.writeError(w, r.Method, http.StatusNotImplemented, "")
		return
	}
	h.checkCancel(r.Context())
	authResult, err := h.auth.Authorize(r.Context(), r, path)
	h.checkCancel(r.Context())
	if err != nil {
		h.report(DownloadFailureAuth)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return
	}
	if !h.authorize(w, r, authResult) {
		return
	}
	if r.Method == http.MethodHead {
		h.serveHead(w, r, path)
		return
	}
	h.serveGet(w, r, path)
}

// parseRawPath 只信任 raw RequestURI：拒绝超长、任何 '?'（含裸尾问号、
// RawQuery/ForceQuery）、非 origin-form、前缀不符、空/重复/首尾斜杠、
// 点段、非 ASCII 段字符（百分号、控制字符、Unicode、加号、冒号等）、
// 非精确小写 .bin/.txt 后缀与空 stem。永不解码或清洗。
// parseRawPath trusts only the raw RequestURI: it rejects overlong URIs, any
// '?' (bare trailing included, plus RawQuery/ForceQuery), non-origin forms,
// wrong prefixes, empty/duplicate/leading/trailing slashes, dot segments,
// non-ASCII segment bytes (percent, control, Unicode, plus, colon, ...),
// inexact non-lower .bin/.txt suffixes and empty stems. It never decodes or
// cleans.
func (h *downloadHandler) parseRawPath(r *http.Request) (repository.Path, bool) {
	raw := r.RequestURI
	if raw == "" || len(raw) > downloadMaxURIBytes || strings.Contains(raw, "?") {
		return repository.Path{}, false
	}
	if r.URL != nil && (r.URL.ForceQuery || r.URL.RawQuery != "") {
		return repository.Path{}, false
	}
	if !strings.HasPrefix(raw, "/") {
		return repository.Path{}, false
	}
	prefix := downloadRootPrefix + h.repoKey + "/"
	if !strings.HasPrefix(raw, prefix) {
		return repository.Path{}, false
	}
	segments := strings.Split(raw[len(prefix):], "/")
	for _, segment := range segments {
		if !downloadProfileSegment(segment) {
			return repository.Path{}, false
		}
	}
	name := segments[len(segments)-1]
	stem, hasSuffix := strings.CutSuffix(name, ".bin")
	if !hasSuffix {
		stem, hasSuffix = strings.CutSuffix(name, ".txt")
	}
	if !hasSuffix || stem == "" {
		return repository.Path{}, false
	}
	path, err := repository.NewPath(h.repoKey, strings.Join(segments[:len(segments)-1], "/"), name, true)
	if err != nil {
		return repository.Path{}, false
	}
	return path, true
}

// downloadProfileSegment 校验单段：非空、仅 ASCII [A-Za-z0-9._-] 且不为
// ./..；其余一切字节（百分号、控制字符、Unicode、加号、冒号等）均拒绝。
// downloadProfileSegment validates one segment: nonempty, ASCII
// [A-Za-z0-9._-] only, never ./..; every other byte (percent, control,
// Unicode, plus, colon, ...) is rejected.
func downloadProfileSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// validDownloadRepoKey 与文件段同一规则：单段、非点段。
// validDownloadRepoKey reuses the file-segment rule: one segment, no dot names.
func validDownloadRepoKey(repoKey string) bool {
	return downloadProfileSegment(repoKey)
}

// downloadUnsupportedHeader 拒绝任何拼写的 Range 与小写 if- 前缀键，
// 含空值键。net/http 送达的键已规范化，EqualFold/前缀判断仍覆盖任意拼写。
// downloadUnsupportedHeader rejects any spelling of Range and lowercased
// if- prefixed keys, including empty values. net/http canonicalizes keys on
// arrival; EqualFold/prefix matching still covers arbitrary spellings.
func downloadUnsupportedHeader(header http.Header) bool {
	for key := range header {
		if strings.EqualFold(key, "Range") {
			return true
		}
		if strings.HasPrefix(strings.ToLower(key), "if-") {
			return true
		}
	}
	return false
}

// authorize 应用三值决定；字段不合法按 auth 失败500，不默认放行。
// 401 仅在 challenge 合法时输出 WWW-Authenticate；403/允许要求空 challenge。
// authorize applies the three-value decision; invalid fields fail as auth
// 500 with no default allow. WWW-Authenticate is emitted only for an admitted
// 401; 403 and allow require an empty challenge.
func (h *downloadHandler) authorize(w http.ResponseWriter, r *http.Request, result DownloadAuthResult) bool {
	switch result.Decision {
	case DownloadAllow:
		if result.Challenge != "" {
			h.report(DownloadFailureAuth)
			h.writeError(w, r.Method, http.StatusInternalServerError, "")
			return false
		}
		return true
	case DownloadUnauthenticated:
		if !downloadValidChallenge(result.Challenge) {
			h.report(DownloadFailureAuth)
			h.writeError(w, r.Method, http.StatusInternalServerError, "")
			return false
		}
		h.writeError(w, r.Method, http.StatusUnauthorized, result.Challenge)
		return false
	case DownloadDenied:
		if result.Challenge != "" {
			h.report(DownloadFailureAuth)
			h.writeError(w, r.Method, http.StatusInternalServerError, "")
			return false
		}
		h.writeError(w, r.Method, http.StatusForbidden, "")
		return false
	default:
		h.report(DownloadFailureAuth)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return false
	}
}

// downloadValidChallenge 要求非空且全部为可打印 ASCII 0x20..0x7e。
// downloadValidChallenge requires nonempty printable ASCII 0x20..0x7e.
func downloadValidChallenge(challenge string) bool {
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

// validatedDownloadStore 每请求包一次 LookupSnapshotSource：存储成功后
// 立即校验 Source，校验失败返回固定投影哨兵；存储原错误原样透传。GET 经
// ReadFile 复用同一入口，HEAD 直接调用，均只查询一次。
// validatedDownloadStore wraps one LookupSnapshotSource per request: a
// successful store result is validated before returning, validation failures
// yield the fixed projection sentinel and raw store errors pass through
// unchanged. GET goes through ReadFile with the same entry; HEAD calls it
// directly; both perform exactly one lookup.
type validatedDownloadStore struct {
	inner localrepo.FileReadStore
	want  repository.Path
}

func (s *validatedDownloadStore) LookupSnapshotSource(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	source, err := s.inner.LookupSnapshotSource(ctx, path)
	if err != nil {
		return repository.FileSnapshotSource{}, err
	}
	if err := validateDownloadSource(source, s.want); err != nil {
		return repository.FileSnapshotSource{}, err
	}
	return source, nil
}

// validateDownloadSource 在打开内容前校验同一查询的 Source：ID>0、文件
// Path 身份等于已授权路径、Metadata 非 nil、三个 Actual 均非 NULL、
// 小写十六进制 40/32/64、size>=0、精确空 SHA1 要求 size 0、ModifiedMillis
// 在允许区间。无身份替换、DB 改写、预读或重算。
// validateDownloadSource checks the same-query Source before any content
// open: ID>0, file Path identity equal to the authorized path, nonnil
// Metadata, all three Actual flags present, lowercase hex 40/32/64, size>=0,
// the exact empty SHA1 requiring size 0, and ModifiedMillis inside the
// allowed range. No identity substitution, DB rewrite, pre-read or rehash.
func validateDownloadSource(source repository.FileSnapshotSource, want repository.Path) error {
	invalid := errDownloadProjection
	reference := source.Reference
	if reference.ID <= 0 {
		return invalid
	}
	if !reference.Path.IsFile() || reference.Path.Key() != want.Key() {
		return invalid
	}
	if reference.Metadata == nil {
		return invalid
	}
	if !source.SHA1ActualValid || !source.MD5ActualValid || !source.SHA256ActualValid {
		return invalid
	}
	if !downloadLowerHex(reference.Content.SHA1, 40) ||
		!downloadLowerHex(reference.Content.MD5, 32) ||
		!downloadLowerHex(reference.Content.SHA256, 64) {
		return invalid
	}
	if reference.Content.Size < 0 {
		return invalid
	}
	if reference.Content.SHA1 == downloadEmptySHA1 && reference.Content.Size != 0 {
		return invalid
	}
	if modified := reference.Metadata.ModifiedMillis(); modified < 0 || modified > downloadMaxModifiedMilli {
		return invalid
	}
	return nil
}

// downloadLowerHex 校验定长小写十六进制。
// downloadLowerHex validates fixed-length lowercase hexadecimal.
func downloadLowerHex(value string, wantLen int) bool {
	if len(value) != wantLen {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

// recordingDownloadOpener 仅记录 Open 是否被调用，用于把 ReadFile 包装后
// 的打开错误与其他存储错误区分；不改变打开语义。
// recordingDownloadOpener only records whether Open was called so wrapped
// open failures inside ReadFile stay distinguishable from other store
// errors; it never alters open semantics.
type recordingDownloadOpener struct {
	inner     localrepo.FileOpener
	attempted bool
}

func (o *recordingDownloadOpener) Open(ctx context.Context, sha1 string) (io.ReadCloser, error) {
	o.attempted = true
	return o.inner.Open(ctx, sha1)
}

// serveHead 直接调用校验存储一次；只写表示头与 200，永不打开内容或写正文。
// serveHead calls the validating store once directly; it writes only the
// representation headers plus 200 and never opens content or writes a body.
func (h *downloadHandler) serveHead(w http.ResponseWriter, r *http.Request, path repository.Path) {
	source, err := (&validatedDownloadStore{inner: h.store, want: path}).LookupSnapshotSource(r.Context(), path)
	if err != nil {
		h.serveLookupError(w, r, err, nil)
		return
	}
	h.checkCancel(r.Context())
	writeDownloadHeaders(w, source)
	w.WriteHeader(http.StatusOK)
}

// serveGet 经 ReadFile 复用既有内容选择（含精确空 SHA1 合成流与实际键），
// 打开成功后流式写出完整表示。
// serveGet reuses ReadFile's existing content selection (including the exact
// empty SHA1 synthetic stream and the actual key) and streams the complete
// representation after a successful open.
func (h *downloadHandler) serveGet(w http.ResponseWriter, r *http.Request, path repository.Path) {
	store := &validatedDownloadStore{inner: h.store, want: path}
	opener := &recordingDownloadOpener{inner: h.content}
	result, err := localrepo.ReadFile(r.Context(), path, localrepo.FileReadDependencies{
		Store:   store,
		Content: opener,
	})
	if err != nil {
		h.serveLookupError(w, r, err, opener)
		return
	}
	if !result.Found {
		// 缺节点是已观察的正常答案，不计为失败诊断。
		// A missing node is the observed normal answer, not a failure.
		h.writeError(w, r.Method, http.StatusNotFound, "")
		return
	}
	if result.Body == nil {
		// 防御：Open 成功契约要求非 nil 流；否则按打开失败处理。
		// Defensive: the open-success contract requires a nonnil stream;
		// otherwise treat it as an open failure.
		h.report(DownloadFailureOpen)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return
	}
	h.streamRepresentation(w, r, result)
}

// serveLookupError 分类并回答预提交存储失败：取消→abort；Open 已尝试→
// open；投影哨兵→projection；其余（含 ErrNotFile 等非 sql.ErrNoRows）→
// lookup。均 500，仅 sql.ErrNoRows 映射 404。
// serveLookupError classifies and answers precommit store failures:
// cancellation aborts; an attempted Open maps to open; the projection
// sentinel maps to projection; everything else (ErrNotFile and other
// non-sql.ErrNoRows errors) maps to lookup. All answer 500; only
// sql.ErrNoRows maps to 404.
func (h *downloadHandler) serveLookupError(w http.ResponseWriter, r *http.Request, err error, opener *recordingDownloadOpener) {
	// A dependency's own deadline is a storage failure while the request
	// remains active; only the actual request context triggers cancellation.
	// 依赖自己的超时不代表请求取消；仅请求上下文触发取消中止。
	h.checkCancel(r.Context())
	if (opener == nil || !opener.attempted) && errors.Is(err, sql.ErrNoRows) {
		h.writeError(w, r.Method, http.StatusNotFound, "")
		return
	}
	category := DownloadFailureLookup
	if opener != nil && opener.attempted {
		category = DownloadFailureOpen
	} else if errors.Is(err, errDownloadProjection) {
		category = DownloadFailureProjection
	}
	h.report(category)
	h.writeError(w, r.Method, http.StatusInternalServerError, "")
}

// streamRepresentation 提交成功头一次，经上下文检查的读/写适配器有界复制
// 内容；任何读/写/长度不符/关闭失败只按类别上报并 abort，不写第二份错误
// 表示。取得流后所有退出路径恰好关闭一次。
// streamRepresentation commits the success headers once and copies content
// through context-checking reader/writer adapters with bounded memory; any
// read/write/length mismatch/close failure only reports its category and
// aborts without a second error representation. Every exit after acquiring
// the stream closes it exactly once.
func (h *downloadHandler) streamRepresentation(w http.ResponseWriter, r *http.Request, result localrepo.FileReadResult) {
	body := result.Body
	closed := false
	// 取得流后立即安装唯一关闭；后续任何退出（含取消 panic）都不泄漏。
	// Install the single close immediately after acquiring the stream; every
	// later exit (including cancellation panics) leaks nothing.
	defer func() {
		if closed {
			return
		}
		closed = true
		if err := body.Close(); err != nil {
			h.report(DownloadFailureClose)
			panic(http.ErrAbortHandler)
		}
	}()
	// Open 之后的取消检查点：先有关闭保护，再提交表示头。
	// The post-Open cancellation checkpoint: close protection first, then
	// representation headers are committed.
	h.checkCancel(r.Context())
	writeDownloadHeaders(w, result.Source)
	w.WriteHeader(http.StatusOK)
	copied, err := io.Copy(
		downloadContextWriter{ctx: r.Context(), writer: w},
		downloadContextReader{ctx: r.Context(), reader: body},
	)
	if err != nil {
		if r.Context().Err() != nil {
			h.report(DownloadFailureCancel)
		} else {
			h.report(DownloadFailureStream)
		}
		panic(http.ErrAbortHandler)
	}
	if copied != result.Source.Reference.Content.Size {
		h.report(DownloadFailureStream)
		panic(http.ErrAbortHandler)
	}
	closed = true
	if err := body.Close(); err != nil {
		h.report(DownloadFailureClose)
		panic(http.ErrAbortHandler)
	}
}

// downloadContextReader 在每次 Read 前检查取消；仅实现 Read，避免
// WriterTo/ReaderFrom 旁路绕过上下文检查。
// downloadContextReader checks cancellation before each Read; it implements
// only Read so WriterTo/ReaderFrom bypasses cannot skip the context checks.
type downloadContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (c downloadContextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.reader.Read(p)
}

// downloadContextWriter 在每次 Write 前检查取消；仅实现 Write。
// downloadContextWriter checks cancellation before each Write; Write only.
type downloadContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (c downloadContextWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.writer.Write(p)
}

// writeDownloadHeaders 写共享表示：节点 size→Content-Length、固定后缀
// MIME、未加引号 SHA1 ETag、三个校验和头、ModifiedMillis 按秒向下取整的
// UTC HTTP 日期与 attachment 双 filename。先核对存储身份等于授权身份再
// 使用 filename；不广告 Accept-Ranges。
// writeDownloadHeaders emits the shared representation: node size as
// Content-Length, the fixed suffix MIME, the unquoted SHA1 ETag, three
// checksum headers, ModifiedMillis floored to seconds as a UTC HTTP date,
// and the attachment disposition with both filename forms. The filename is
// used only after stored identity equals authorized identity; Accept-Ranges
// is deliberately omitted.
func writeDownloadHeaders(w http.ResponseWriter, source repository.FileSnapshotSource) {
	reference := source.Reference
	name := reference.Path.Name()
	w.Header().Set("Content-Type", downloadMIME(name))
	w.Header().Set("Content-Length", strconv.FormatInt(reference.Content.Size, 10))
	w.Header().Set("ETag", reference.Content.SHA1)
	w.Header().Set("X-Checksum-Sha1", reference.Content.SHA1)
	w.Header().Set("X-Checksum-Md5", reference.Content.MD5)
	w.Header().Set("X-Checksum-Sha256", reference.Content.SHA256)
	w.Header().Set("Last-Modified", time.Unix(reference.Metadata.ModifiedMillis()/1000, 0).UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"; filename*=UTF-8''`+name)
}

// downloadMIME 固定映射 .bin→application/octet-stream、.txt→text/plain。
// downloadMIME maps .bin→application/octet-stream and .txt→text/plain only.
func downloadMIME(name string) string {
	if strings.HasSuffix(name, ".txt") {
		return "text/plain"
	}
	return "application/octet-stream"
}

// writeError 写固定错误表示：仅401带合法 challenge 时设 WWW-Authenticate，
// 仅405设单一 Allow: GET, HEAD；GET/HEAD 均显式 Content-Length，HEAD 永不
// 调用 Write。不回显输入或内部错误。
// writeError emits the fixed error representation: WWW-Authenticate only for
// an admitted 401 with a valid challenge, a single Allow: GET, HEAD only for
// 405; both GET and HEAD set an explicit Content-Length and HEAD never calls
// Write. Inputs and internal errors are never echoed.
func (h *downloadHandler) writeError(w http.ResponseWriter, method string, status int, challenge string) {
	if status == http.StatusUnauthorized && challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	if status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", "GET, HEAD")
	}
	body := downloadErrorBodies[status]
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if method != http.MethodHead {
		io.WriteString(w, body)
	}
}

// checkCancel 在固定检查点上处理取消：仅上报 cancel 类别并以
// http.ErrAbortHandler 中止传输，绝不静默返回或伪造默认 200。
// checkCancel handles cancellation at the fixed checkpoints: it reports only
// the cancel category and aborts the transport via http.ErrAbortHandler,
// never returning silently or fabricating a default 200.
func (h *downloadHandler) checkCancel(ctx context.Context) {
	if ctx.Err() != nil {
		h.report(DownloadFailureCancel)
		panic(http.ErrAbortHandler)
	}
}

// Package httpserver: 有界普通上传(PUT)适配器。本文件实现构造期校验与
// 固定 Serve 顺序；错误分类与 201 投影在 upload_projection.go。
// Package httpserver: bounded ordinary upload (PUT) adapter. This file owns
// constructor validation and the fixed serve order; error classification and
// the 201 projection live in upload_projection.go.
package httpserver

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// uploadMaxBodyLimit 是 MaxBodyBytes 的上界：1 GiB（含）。
// uploadMaxBodyLimit is the inclusive upper bound for MaxBodyBytes: 1 GiB.
const uploadMaxBodyLimit = int64(1) << 30

// uploadLegacyClientMarker 标记历史客户端：其版本相关的 SHA256 缺失行为
// 超出本切片，按本地政策回答 501，绝不静默返回普通形态。
// uploadLegacyClientMarker flags the legacy client whose version-dependent
// SHA256 omission is outside this slice; it gets a local 501 and never a
// silently ordinary shape.
const uploadLegacyClientMarker = "ArtifactoryBuildClient/"

// uploadErrorBodies 是固定、不含输入与内部异常的原创错误正文。
// uploadErrorBodies are original fixed bodies without inputs or internals.
var uploadErrorBodies = map[int]string{
	http.StatusBadRequest:            "bad request\n",
	http.StatusUnauthorized:          "unauthorized\n",
	http.StatusForbidden:             "forbidden\n",
	http.StatusMethodNotAllowed:      "method not allowed\n",
	http.StatusConflict:              "conflict\n",
	http.StatusLengthRequired:        "length required\n",
	http.StatusRequestEntityTooLarge: "payload too large\n",
	http.StatusInternalServerError:   "internal error\n",
	http.StatusNotImplemented:        "not implemented\n",
}

// UploadAuthDecision 由可信授权层显式给出，零值无效；本端口是写授权，
// 不复用 DownloadAuth/CanRead。
// UploadAuthDecision requires an explicit trusted decision; zero is invalid.
// This port is a write authorization, never DownloadAuth/CanRead.
type UploadAuthDecision uint8

const (
	UploadAllow UploadAuthDecision = iota + 1
	UploadUnauthenticated
	UploadDenied
)

// UploadAuthResult 携带三值决定；Challenge 仅用于401，OperatorUTF16 仅在
// Allow 时有效，两者其余出现均为配置错误。
// UploadAuthResult carries the three-value decision; Challenge belongs only
// to 401 and OperatorUTF16 only to Allow, any other presence is a
// misconfiguration.
type UploadAuthResult struct {
	Decision      UploadAuthDecision
	Challenge     string
	OperatorUTF16 []uint16
}

// UploadAuth 在调用 Saver 前决定一次；授权实现可以读取目标节点。
// UploadAuth decides once before the handler calls the Saver. The authorizer
// may read the target node when evaluating permissions.
type UploadAuth interface {
	Authorize(context.Context, *http.Request, repository.Path) (UploadAuthResult, error)
}

// UploadSaver 代理真实保存语义（发布、惰性节点事务、提交）；成功契约由
// 可信依赖保证：仅明确提交成功返回非零 Source。
// UploadSaver proxies the real save semantics (publication, lazy node
// transaction, commit); the trusted dependency guarantees that only an
// explicit commit success yields a nonzero Source.
type UploadSaver interface {
	Save(context.Context, io.Reader, localrepo.UploadInput) (repository.FileSnapshotSource, error)
}

// UploadFailure 为固定、不含请求内容的诊断类别。
// UploadFailure is a fixed diagnostic category without request data.
type UploadFailure string

const (
	UploadFailureAuth       UploadFailure = "auth"
	UploadFailureBody       UploadFailure = "body"
	UploadFailureSave       UploadFailure = "save"
	UploadFailureProjection UploadFailure = "projection"
	UploadFailureWrite      UploadFailure = "write"
	UploadFailureCancel     UploadFailure = "cancel"
)

// UploadDeps 由注入者持有；PublicOrigin 是可信配置与元数据来源，绝不反射
// 请求 Host。Report 可空，回调须并发安全且不 panic。
// UploadDeps remain injector-owned; PublicOrigin is trusted configuration and
// metadata only and never reflects the request Host. Report may be nil;
// callbacks must be safe and nonpanicking.
type UploadDeps struct {
	RepoKey      string
	MaxBodyBytes int64
	PublicOrigin string
	Auth         UploadAuth
	Saver        UploadSaver
	Report       func(UploadFailure)
}

// ErrUploadConfiguration 是不含输入的构造错误。
// ErrUploadConfiguration is the fixed constructor error without inputs.
var ErrUploadConfiguration = errors.New("httpserver: invalid upload configuration")

// NewUploadHandler 构造未挂接生产路由的独立处理器；typed nil 不支持，
// 无默认允许或匿名回退。空 Report 使用仅含类别的固定日志。
// NewUploadHandler builds an unmounted handler; typed nils are unsupported
// and no default allow or anonymous fallback exists. A nil Report uses the
// fixed category-only logger.
func NewUploadHandler(deps UploadDeps) (http.Handler, error) {
	if deps.Auth == nil || deps.Saver == nil {
		return nil, ErrUploadConfiguration
	}
	if !validDownloadRepoKey(deps.RepoKey) {
		return nil, ErrUploadConfiguration
	}
	if deps.MaxBodyBytes < 1 || deps.MaxBodyBytes > uploadMaxBodyLimit {
		return nil, ErrUploadConfiguration
	}
	if !uploadValidOrigin(deps.PublicOrigin) {
		return nil, ErrUploadConfiguration
	}
	origin := strings.TrimSuffix(deps.PublicOrigin, "/")
	report := deps.Report
	if report == nil {
		report = func(f UploadFailure) {
			log.Printf("httpserver: upload failure: %s", string(f))
		}
	}
	return &uploadHandler{
		repoKey: deps.RepoKey,
		maxBody: deps.MaxBodyBytes,
		origin:  origin,
		auth:    deps.Auth,
		saver:   deps.Saver,
		report:  report,
	}, nil
}

// uploadValidOrigin 接受显式 http(s) 起点：非空 host、无 userinfo/query/
// fragment/opaque/RawPath、路径仅空或 "/"、全串无控制与非 ASCII 字节。
// uploadValidOrigin accepts an explicit http(s) origin: nonempty host, no
// userinfo/query/fragment/opaque/RawPath, path only empty or "/", and no
// control or non-ASCII byte anywhere in the string.
func uploadValidOrigin(origin string) bool {
	if origin == "" || strings.Contains(origin, "#") {
		return false
	}
	for i := 0; i < len(origin); i++ {
		if origin[i] < 0x20 || origin[i] == 0x7f || origin[i] >= 0x80 {
			return false
		}
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" ||
		parsed.RawPath != "" {
		return false
	}
	return parsed.Path == "" || parsed.Path == "/"
}

// uploadHandler 持有注入依赖；net/http Server 拥有请求 body，本处理器与
// Saver 包装都不关闭它、不落盘副本、不加后台 goroutine；有界
// ReadHeaderTimeout/ReadTimeout 由宿主服务器保证，是本适配器的前提。
// uploadHandler holds injected dependencies; the net/http Server owns the
// request body, and neither this handler nor any Saver wrapper closes it,
// spools it or adds background goroutines. Bounded hosting
// ReadHeaderTimeout/ReadTimeout are the hosting server's precondition.
type uploadHandler struct {
	repoKey string
	maxBody int64
	origin  string
	auth    UploadAuth
	saver   UploadSaver
	report  func(UploadFailure)
}

// ServeHTTP 固定全序：取消检查 → 非PUT 405 → 严格 raw origin URI +
// NewUploadPath（拒绝任何 query）→ 未支持头/框架 501 → 已知 CL 411/413 →
// 原始 CL 一致性 → 专有校验和头 → 授权一次 → 复制并校验 operator →
// Saver 一次（带预算+读错误记录 reader）→ 取消检查 → 分类错误或二次
// 校验 Source 后序列化 201。任何成功字节都不早于已提交的保存结果。
// ServeHTTP enforces the fixed total order: cancel check → nonPUT 405 →
// strict raw origin URI + NewUploadPath (any query rejected) → unsupported
// header/framing 501 → known CL 411/413 → raw CL agreement → dedicated
// checksum headers → authorize once → copy/validate the operator → Saver
// once through the budgeting read-error-recording reader → cancel check →
// classify the error or revalidate the Source and serialize 201. No success
// byte precedes the committed saver result.
func (h *uploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.checkCancel(r.Context())
	if r.Method != http.MethodPut {
		h.writeError(w, r.Method, http.StatusMethodNotAllowed, "")
		return
	}
	path, ok := h.parseRawPath(r)
	if !ok {
		h.writeError(w, r.Method, http.StatusBadRequest, "")
		return
	}
	if uploadUnsupportedFraming(r) {
		h.writeError(w, r.Method, http.StatusNotImplemented, "")
		return
	}
	if r.ContentLength < 0 {
		h.writeError(w, r.Method, http.StatusLengthRequired, "")
		return
	}
	if r.ContentLength > h.maxBody {
		h.writeError(w, r.Method, http.StatusRequestEntityTooLarge, "")
		return
	}
	if !uploadRawContentLengthValid(r) {
		h.writeError(w, r.Method, http.StatusBadRequest, "")
		return
	}
	checksums, ok := uploadParseChecksums(r.Header)
	if !ok {
		h.writeError(w, r.Method, http.StatusBadRequest, "")
		return
	}
	authResult, err := h.auth.Authorize(r.Context(), r, path)
	if err != nil {
		h.report(UploadFailureAuth)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return
	}
	operator, allowed := h.authorizeResult(w, r, authResult)
	if !allowed {
		return
	}
	body := io.Reader(r.Body)
	if r.Body == nil {
		// 直接手工调用下 nil Body 仅在 CL 0 时可换成 http.NoBody。
		// In a hand-crafted direct call a nil Body may become
		// http.NoBody only for CL 0.
		if r.ContentLength != 0 {
			h.writeError(w, r.Method, http.StatusBadRequest, "")
			return
		}
		body = http.NoBody
	}
	reader := &uploadBodyReader{inner: body, remaining: h.maxBody}
	source, err := h.saver.Save(r.Context(), reader, localrepo.UploadInput{
		Path:          path,
		Checksums:     checksums,
		OperatorUTF16: operator,
		DeclaredSize:  r.ContentLength,
	})
	h.checkCancel(r.Context())
	if err != nil {
		h.serveSaveError(w, r, err, reader)
		return
	}
	if err := localrepo.ValidateUploadSource(source, path); err != nil {
		h.report(UploadFailureProjection)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return
	}
	h.writeCreated(w, r, source)
}

// parseRawPath 只信任 raw RequestURI：拒绝空/超长/任何 '?'（含 URL 的
// RawQuery/ForceQuery）、非 origin-form、前缀不符、空余段，段字节先按
// 下载段画像预检，最终由 NewUploadPath 做完整上传画像判定。永不解码。
// parseRawPath trusts only the raw RequestURI: it rejects empty/overlong
// values, any '?' (including URL RawQuery/ForceQuery), non-origin forms,
// wrong prefixes and empty remainders; segment bytes get the download-style
// precheck and NewUploadPath decides the full upload profile. Never decodes.
func (h *uploadHandler) parseRawPath(r *http.Request) (repository.Path, bool) {
	raw := r.RequestURI
	if raw == "" || len(raw) > downloadMaxURIBytes || strings.Contains(raw, "?") {
		return repository.Path{}, false
	}
	if !strings.HasPrefix(raw, "/") {
		return repository.Path{}, false
	}
	if r.URL != nil && (r.URL.ForceQuery || r.URL.RawQuery != "") {
		return repository.Path{}, false
	}
	prefix := downloadRootPrefix + h.repoKey + "/"
	if !strings.HasPrefix(raw, prefix) {
		return repository.Path{}, false
	}
	rest := raw[len(prefix):]
	if rest == "" {
		return repository.Path{}, false
	}
	for _, segment := range strings.Split(rest, "/") {
		if !downloadProfileSegment(segment) {
			return repository.Path{}, false
		}
	}
	path, err := localrepo.NewUploadPath(h.repoKey, rest)
	if err != nil {
		return repository.Path{}, false
	}
	return path, true
}

// uploadHeaderPresent 判定键存在性：映射中任一 EqualFold 拼写键即存在，
// 即使其值切片为 nil/空也绝不静默视为缺失。
// uploadHeaderPresent reports key presence: any EqualFold spelling of the
// key in the map counts, even with a nil or empty value slice; a present
// key is never silently treated as absent.
func uploadHeaderPresent(header http.Header, name string) bool {
	for key := range header {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// uploadHeaderValues 聚合任意拼写别名的全部值；绝不只用规范化 Get。
// uploadHeaderValues aggregates every value across any-spelling aliases;
// a canonical-only Get is never enough.
func uploadHeaderValues(header http.Header, name string) []string {
	var values []string
	for key, vs := range header {
		if strings.EqualFold(key, name) {
			values = append(values, vs...)
		}
	}
	return values
}

// uploadUnsupportedFraming 拒绝任何拼写的 Transfer-Encoding（含
// r.TransferEncoding 旗标）、Content-Encoding、Expect、Range、If-*、
// X-Checksum（泛型）、X-Checksum-Deploy、X-Binary-Size、
// X-Check-Binary-Existence-In-Filestore、X-Explode-Archive*、X-Artifactory-*、
// Last-Modified、Content-Range，以及含历史客户端标记的 User-Agent。
// 空/false 值与多值别名同样拒绝，绝不静默忽略。
// uploadUnsupportedFraming rejects any spelling of Transfer-Encoding
// (including the r.TransferEncoding flag), Content-Encoding, Expect, Range,
// If-*, X-Checksum (generic), X-Checksum-Deploy, X-Binary-Size,
// X-Check-Binary-Existence-In-Filestore, X-Explode-Archive*, X-Artifactory-*,
// Last-Modified and Content-Range, plus any User-Agent carrying the legacy
// client marker. Empty/false values and multi-value aliases are rejected the
// same way; nothing is silently ignored.
func uploadUnsupportedFraming(r *http.Request) bool {
	if len(r.TransferEncoding) > 0 {
		return true
	}
	for _, value := range uploadHeaderValues(r.Header, "User-Agent") {
		if strings.Contains(value, uploadLegacyClientMarker) {
			return true
		}
	}
	for key := range r.Header {
		lower := strings.ToLower(key)
		if strings.EqualFold(key, "Transfer-Encoding") ||
			strings.EqualFold(key, "Content-Encoding") ||
			strings.EqualFold(key, "Expect") ||
			strings.EqualFold(key, "Range") ||
			strings.EqualFold(key, "X-Checksum") ||
			strings.EqualFold(key, "X-Checksum-Deploy") ||
			strings.EqualFold(key, "X-Binary-Size") ||
			strings.EqualFold(key, "X-Check-Binary-Existence-In-Filestore") ||
			strings.EqualFold(key, "Last-Modified") ||
			strings.EqualFold(key, "Content-Range") ||
			strings.HasPrefix(lower, "if-") ||
			strings.HasPrefix(lower, "x-explode-archive") ||
			strings.HasPrefix(lower, "x-artifactory-") {
			return true
		}
	}
	return false
}

// uploadRawContentLengthValid：原始 Content-Length 键（任意拼写聚合）存在
// 时必须恰一个纯 ASCII 十进制值且与解析字段一致；键缺失也有效，因为
// net/http 会补上解析字段。存在性按键判定：nil/空值切片同样算存在，
// 因值数不为恰一而拒绝，绝不静默忽略。
// uploadRawContentLengthValid: when a raw Content-Length key (any spelling,
// aggregated) is present it must carry exactly one plain ASCII decimal value
// agreeing with the parsed field; an absent raw key stays valid because
// net/http supplies the parsed field. Presence is keyed on map membership:
// nil or empty value slices still count as present and reject for not having
// exactly one value, never silently ignored.
func uploadRawContentLengthValid(r *http.Request) bool {
	if !uploadHeaderPresent(r.Header, "Content-Length") {
		return true
	}
	values := uploadHeaderValues(r.Header, "Content-Length")
	if len(values) == 0 {
		return false
	}
	if len(values) != 1 {
		return false
	}
	value := values[0]
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed == r.ContentLength
}

// uploadParseChecksums 解析三个专有校验和头：存在即要求恰一个可接受
// 小写 hex 值（40/32/64），否则 400；值聚合适用任意拼写别名，但存在性
// 按键判定，nil/空值切片同样算存在并因值数不为恰一而拒绝。
// uploadParseChecksums parses the three dedicated checksum headers: presence
// requires exactly one accepted lowercase hex value (40/32/64) or the caller
// answers 400; values aggregate across any-spelling aliases, but presence is
// keyed on map membership, so nil or empty value slices still count as
// present and reject for not having exactly one value.
func uploadParseChecksums(header http.Header) (repository.ClientFileChecksums, bool) {
	var out repository.ClientFileChecksums
	for _, slot := range [...]struct {
		name   string
		length int
		dst    *[]uint16
	}{
		{"X-Checksum-Sha1", 40, &out.SHA1Original},
		{"X-Checksum-Md5", 32, &out.MD5Original},
		{"X-Checksum-Sha256", 64, &out.SHA256Original},
	} {
		if !uploadHeaderPresent(header, slot.name) {
			continue
		}
		values := uploadHeaderValues(header, slot.name)
		if len(values) == 0 {
			return out, false
		}
		if len(values) != 1 || !downloadLowerHex(values[0], slot.length) {
			return out, false
		}
		*slot.dst = uploadASCIIToUnits(values[0])
	}
	return out, true
}

// uploadASCIIToUnits 把已判定的 ASCII hex 值转成 UTF-16 码元副本。
// uploadASCIIToUnits turns an accepted ASCII hex value into a UTF-16 unit copy.
func uploadASCIIToUnits(value string) []uint16 {
	units := make([]uint16, len(value))
	for i := 0; i < len(value); i++ {
		units[i] = uint16(value[i])
	}
	return units
}

// authorizeResult 应用三值决定；字段不合法按 auth 失败500，绝不默认放
// 行。401 仅在 challenge 合法时输出；Allow 返回 operator 的防御性副本。
// authorizeResult applies the three-value decision; invalid fields fail as
// auth 500 with no default allow. A 401 is emitted only for a valid
// challenge; Allow returns a defensive copy of the operator.
func (h *uploadHandler) authorizeResult(w http.ResponseWriter, r *http.Request, result UploadAuthResult) ([]uint16, bool) {
	switch result.Decision {
	case UploadAllow:
		if result.Challenge != "" || !uploadValidOperator(result.OperatorUTF16) {
			h.report(UploadFailureAuth)
			h.writeError(w, r.Method, http.StatusInternalServerError, "")
			return nil, false
		}
		operator := make([]uint16, len(result.OperatorUTF16))
		copy(operator, result.OperatorUTF16)
		return operator, true
	case UploadUnauthenticated:
		if !downloadValidChallenge(result.Challenge) || len(result.OperatorUTF16) != 0 {
			h.report(UploadFailureAuth)
			h.writeError(w, r.Method, http.StatusInternalServerError, "")
			return nil, false
		}
		h.writeError(w, r.Method, http.StatusUnauthorized, result.Challenge)
		return nil, false
	case UploadDenied:
		if result.Challenge != "" || len(result.OperatorUTF16) != 0 {
			h.report(UploadFailureAuth)
			h.writeError(w, r.Method, http.StatusInternalServerError, "")
			return nil, false
		}
		h.writeError(w, r.Method, http.StatusForbidden, "")
		return nil, false
	default:
		h.report(UploadFailureAuth)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return nil, false
	}
}

// uploadValidOperator 要求 1..64 个可打印 ASCII U+0020..U+007E 码元且非全
// 空白，杜绝 Unicode/截断歧义。
// uploadValidOperator requires 1..64 printable ASCII U+0020..U+007E units and
// not all spaces, leaving no Unicode or truncation ambiguity.
func uploadValidOperator(units []uint16) bool {
	if len(units) < 1 || len(units) > 64 {
		return false
	}
	spacesOnly := true
	for _, unit := range units {
		if unit < 0x20 || unit > 0x7e {
			return false
		}
		if unit != ' ' {
			spacesOnly = false
		}
	}
	return !spacesOnly
}

// uploadBodyReader 在 net/http 所有的 body 前加字节预算并记录底层首个
// 非 EOF 读错误；不关闭、不缓冲、不额外起 goroutine。超预算返回固定
// 哨兵，供分类阶段以有限预算做单因路径身份匹配。
// uploadBodyReader adds a byte budget in front of the net/http-owned body
// and records the first underlying non-EOF read error; it never closes,
// buffers or starts goroutines. Exceeding the budget yields the fixed
// sentinel matched later by the budgeted single-cause classification.
type uploadBodyReader struct {
	inner     io.Reader
	remaining int64
	exceeded  bool
	readErr   error
}

// Read 强制预算：第 maxBody+1 字节触发哨兵（粘性）；底层非 EOF 错误原样
// 返回并记录，供 400 分类使用。
// Read enforces the budget: the (maxBody+1)-th byte triggers the sticky
// sentinel; underlying non-EOF errors pass through unchanged and are
// recorded for the 400 classification.
func (b *uploadBodyReader) Read(p []byte) (int, error) {
	if b.exceeded {
		return 0, errUploadBodyBudget
	}
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.inner.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		b.exceeded = true
		return n, errUploadBodyBudget
	}
	if err != nil && err != io.EOF && b.readErr == nil {
		b.readErr = err
	}
	return n, err
}

// writeError 写固定错误表示：仅401带合法 challenge 时设
// WWW-Authenticate，仅405设 Allow: PUT；显式 Content-Length，HEAD 永不
// 写正文。不回显输入或内部错误。
// writeError emits the fixed error representation: WWW-Authenticate only for
// an admitted 401 with a valid challenge, Allow: PUT only for 405; explicit
// Content-Length everywhere and HEAD never writes a body. Inputs and
// internal errors are never echoed.
func (h *uploadHandler) writeError(w http.ResponseWriter, method string, status int, challenge string) {
	if status == http.StatusUnauthorized && challenge != "" {
		w.Header().Set("WWW-Authenticate", challenge)
	}
	if status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", "PUT")
	}
	body := uploadErrorBodies[status]
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if method != http.MethodHead {
		io.WriteString(w, body)
	}
}

// checkCancel 在固定检查点处理父上下文取消：仅上报 cancel 并以
// http.ErrAbortHandler 中止传输；依赖自己的子 deadline 不走此路径。
// checkCancel handles parent-context cancellation at the fixed checkpoints:
// it reports only the cancel category and aborts the transport via
// http.ErrAbortHandler; a dependency's own child deadline never takes this
// path.
func (h *uploadHandler) checkCancel(ctx context.Context) {
	if ctx.Err() != nil {
		h.report(UploadFailureCancel)
		panic(http.ErrAbortHandler)
	}
}

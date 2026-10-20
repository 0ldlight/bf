// Package compatprobe implements the BIN-128 read-only blackbox probe for
// the frozen generic-download contract in docs/GENERIC-DOWNLOAD-CONTRACT.md.
// compatprobe 包实现 GENERIC-DOWNLOAD-CONTRACT.md 冻结契约的只读黑盒探针。
//
// The probe replays the 18 observed GET/HEAD cases against an arbitrary
// origin. It never sends request bodies, follows redirects, consults proxy
// settings or mutates global HTTP state, and its errors never echo the
// target URL, credential value or response body. Selecting a profile
// describes the frozen behavioral sample only; it does not attest the live
// deployment's version.
package compatprobe

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Frozen identity of the observed behavioral sample.
// 冻结样本的行为标识。
const (
	Profile         = "artifactory-7.161.26-generic-download-v1"
	CaseCount       = 18
	MinFixtureBytes = 5 // bytes=4- must stay satisfiable, so at least 5 bytes / bytes=4- 需可满足，至少 5 字节。
	MaxFixtureBytes = 65536

	DefaultTimeout = 10 * time.Second
	MaxTimeout     = 60 * time.Second

	maxHeaderBytes  = 65536
	redactedLiteral = "[redacted]"
)

// Case execution states / 用例执行状态。
const (
	StatePassed  = "passed"
	StateFailed  = "failed"
	StateSkipped = "skipped"
)

// caseIDs fixes the plan order reported for every run, including skips.
// caseIDs 固定每次运行的计划顺序，包括跳过项。
var caseIDs = [CaseCount]string{
	"get", "head", "range-prefix", "range-suffix", "range-open",
	"range-unsatisfiable", "etag-match", "etag-any", "etag-weak", "etag-miss",
	"modified-match", "modified-old", "if-match-hit", "if-match-miss",
	"if-range-hit", "if-range-miss", "conditional-precedence", "head-range",
}

// headerAllowlist is the only response header family recorded in reports.
// headerAllowlist 是报告允许记录的唯一响应头集合。
var headerAllowlist = []string{
	"Content-Length", "Content-Range", "Content-Type", "ETag",
	"Last-Modified", "X-Checksum-Sha1", "X-Checksum-Sha256", "Accept-Ranges",
}

// Options configures one probe run / 单次探针运行的配置。
type Options struct {
	URL           string        // Absolute artifact URL without userinfo/query/fragment / 绝对制品 URL，无用户信息、查询串或片段。
	Fixture       []byte        // Known synthetic artifact bytes / 已知合成制品字节。
	Authorization string        // Optional single-line "Basic ..." or "Bearer ..." value / 可选单行 Authorization 值。
	Timeout       time.Duration // Per-request timeout; 0 means DefaultTimeout / 每请求超时，0 表示默认。
}

// CaseResult is the outcome of one frozen case / 单个冻结用例的结果。
type CaseResult struct {
	ID         string            `json:"id"`
	State      string            `json:"state"`
	HTTPStatus int               `json:"httpStatus"`
	Bytes      int               `json:"bytes"`
	SHA256     string            `json:"sha256"`
	Headers    map[string]string `json:"headers,omitempty"`
	Failures   []string          `json:"failures,omitempty"`
}

// Report is the full bounded result of one run / 单次运行的完整受控结果。
type Report struct {
	Profile  string       `json:"profile"`
	Passed   bool         `json:"passed"`
	Planned  int          `json:"planned"`
	Executed int          `json:"executed"`
	Cases    []CaseResult `json:"cases"`
}

// errCategory carries a generic failure category; its text never contains
// the URL, credential or body bytes.
// errCategory 只携带通用失败类别，文本不含 URL、凭据或正文。
type errCategory struct{ kind, detail string }

func (e errCategory) Error() string { return "compatprobe: " + e.kind + ": " + e.detail }

func configErr(detail string) error { return errCategory{"invalid-config", detail} }

var errRedirected = errors.New("compatprobe: redirect refused")

// Validate checks options before any network activity.
// Validate 在任何网络活动之前校验配置。
func Validate(opts Options) error {
	if n := len(opts.Fixture); n < MinFixtureBytes || n > MaxFixtureBytes {
		return configErr(fmt.Sprintf("fixture size %d outside %d..%d", n, MinFixtureBytes, MaxFixtureBytes))
	}
	if opts.Timeout < 0 || opts.Timeout > MaxTimeout {
		return configErr("timeout outside 0..60s")
	}
	if err := validateURL(opts.URL); err != nil {
		return err
	}
	if err := validateAuthorization(opts.Authorization); err != nil {
		return err
	}
	return nil
}

func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return configErr("url is not parseable")
	}
	switch {
	case !u.IsAbs() || u.Opaque != "":
		return configErr("url must be absolute, not opaque")
	case u.Scheme != "http" && u.Scheme != "https":
		return configErr("url scheme must be http or https")
	case u.Hostname() == "":
		return configErr("url host is required")
	case u.User != nil:
		return configErr("url must not carry userinfo")
	case u.ForceQuery || u.RawQuery != "":
		return configErr("url must not carry a query")
	case strings.Contains(raw, "#"):
		// A literal '#' delimiter is rejected even when the fragment is
		// empty; an encoded %23 stays an ordinary path byte.
		// 字面 '#' 分隔符一律拒绝（含空片段）；编码的 %23 仍是普通路径字节。
		return configErr("url must not carry a fragment")
	}
	return nil
}

const maxAuthorizationBytes = 4096

func validateAuthorization(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > maxAuthorizationBytes {
		return configErr("authorization exceeds maximum length")
	}
	rest, okBasic := strings.CutPrefix(v, "Basic ")
	if !okBasic {
		var okBearer bool
		rest, okBearer = strings.CutPrefix(v, "Bearer ")
		if !okBearer {
			return configErr("authorization must start with Basic or Bearer")
		}
	}
	if rest == "" || strings.TrimSpace(rest) != rest || strings.ContainsAny(rest, " \t") {
		return configErr("authorization credential carries whitespace")
	}
	for i := 0; i < len(v); i++ {
		if b := v[i]; b < 0x20 || b > 0x7e {
			return configErr("authorization contains control or non-ASCII bytes")
		}
	}
	return nil
}

type fetched struct {
	status  int
	headers http.Header
	body    []byte
}

type caseSpec struct {
	id      string
	method  string
	headers []string // Flat name/value pairs / 扁平名值对。
	verify  func(*probeRun, validators, fetched) []string
}

type probeRun struct {
	opts    Options
	timeout time.Duration
	secrets []string // Literals scrubbed from all recorded text / 所有记录文本需抹除的字面量。
	client  *http.Client
}

// redactionSecrets collects every literal that must never appear in a
// report: the full Authorization value, the bare credential token, and the
// configured URL, since a malicious server may reflect any of them.
// redactionSecrets 收集报告中绝不允许出现的字面量：完整凭据头、裸令牌与配置 URL。
func redactionSecrets(opts Options) []string {
	var secrets []string
	if opts.Authorization != "" {
		secrets = append(secrets, opts.Authorization)
		if _, token, found := strings.Cut(opts.Authorization, " "); found && token != "" {
			secrets = append(secrets, token)
		}
	}
	if opts.URL != "" {
		secrets = append(secrets, opts.URL)
	}
	return secrets
}

// Run executes the 18 frozen cases. It returns a report retaining concrete
// results and a non-nil error for invalid configuration, transport failure
// or any assertion failure. The initial GET failing leaves the remaining
// cases explicitly skipped; later failures are recorded and the run
// continues while validators remain usable.
// Run 执行 18 个冻结用例：初始 GET 失败时其余用例显式跳过；后续失败记录并继续。
func Run(ctx context.Context, opts Options) (Report, error) {
	report := Report{Profile: Profile, Planned: CaseCount}
	fillSkipped := func(from int) {
		for _, id := range caseIDs[from:] {
			report.Cases = append(report.Cases, CaseResult{ID: id, State: StateSkipped})
		}
	}
	if ctx == nil {
		fillSkipped(0)
		return report, configErr("context is nil")
	}
	if err := Validate(opts); err != nil {
		fillSkipped(0)
		return report, err
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	p := &probeRun{
		opts:    opts,
		timeout: timeout,
		secrets: redactionSecrets(opts),
		client: &http.Client{
			// Owned transport: no proxy lookup, no transparent compression,
			// bounded response headers, normal TLS verification.
			// 专属传输：不查代理、不透明压缩、限制响应头，保持系统 TLS 验证。
			Transport: &http.Transport{
				Proxy:                  nil,
				DisableCompression:     true,
				MaxResponseHeaderBytes: maxHeaderBytes,
			},
			// Redirects are refused before any contact with a second host,
			// so credentials are never forwarded elsewhere.
			// 在接触第二主机前拒绝重定向，凭据绝不外发。
			CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirected },
		},
	}
	// Idle keep-alive sockets from this run are closed on every exit path,
	// including early returns, so repeated runs do not accumulate sockets.
	// 所有退出路径（含提前返回）都关闭本次运行遗留的空闲连接。
	defer p.client.CloseIdleConnections()

	var v validators // Empty for the first GET; its verifier uses fixture checksums directly.
	first, firstFetched, transportFail := p.execute(ctx, v, "get", http.MethodGet, nil, verifyFirstGet)
	report.Cases = append(report.Cases, first)
	if first.State != StatePassed {
		fillSkipped(1)
		finalize(&report)
		if transportFail {
			return report, errCategory{"network", "initial GET did not yield a usable representation"}
		}
		return report, errCategory{"assert", "initial GET representation rejected"}
	}

	// Validators are derived from the RAW first response. The public
	// CaseResult carries report-only redacted header copies; using them as
	// an oracle would corrupt later requests whenever a short credential
	// token happens to overlap validator bytes (e.g. a hex digit).
	// validator 从首个响应的原始头派生；公开结果里的头副本仅用于报告且已抹除，
	// 若当作 oracle，短令牌碰巧重叠校验字节时会污染后续请求。
	v = deriveValidators(p.opts.Fixture, firstFetched.headers)
	for _, spec := range remainingSpecs(v, len(p.opts.Fixture)) {
		res, _, _ := p.execute(ctx, v, spec.id, spec.method, spec.headers, spec.verify)
		report.Cases = append(report.Cases, res)
	}
	finalize(&report)
	if !report.Passed {
		return report, errCategory{"assert", "one or more cases failed"}
	}
	return report, nil
}

func finalize(report *Report) {
	executed := 0
	passed := true
	for _, c := range report.Cases {
		if c.State != StateSkipped {
			executed++
		}
		if c.State != StatePassed {
			passed = false
		}
	}
	report.Executed = executed
	report.Passed = passed
}

// deriveValidators keeps the fixture's own checksums plus the etag/date
// observed on the verified first GET.
// deriveValidators 保存制品自身校验和与首个 GET 观察到的 etag/date。
type validators struct {
	etag         string
	lastModified string
	lmTime       time.Time
	sha1Hex      string
	sha256Hex    string
}

func deriveValidators(fixture []byte, rawHeaders http.Header) validators {
	s1, s256 := sha1.Sum(fixture), sha256.Sum256(fixture)
	lm, _ := parseHTTPDate(rawHeaders.Get("Last-Modified"))
	return validators{
		etag:         rawHeaders.Get("ETag"),
		lastModified: rawHeaders.Get("Last-Modified"),
		lmTime:       lm,
		sha1Hex:      hex.EncodeToString(s1[:]),
		sha256Hex:    hex.EncodeToString(s256[:]),
	}
}

func parseHTTPDate(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// epochDate is the frozen "old" If-Modified-Since input.
// epochDate 是冻结的“过旧”If-Modified-Since 输入。
const epochDate = "Thu, 01 Jan 1970 00:00:00 GMT"

// missingETag is the exact unmatched validator input from the original
// capture: a quoted opaque etag string, not a derived hash.
// missingETag 是原始抓包中精确的不匹配校验输入：带引号的不透明字符串。
const missingETag = `"binflow-missing"`

// remainingSpecs returns the 17 cases after the initial GET, in plan order.
// remainingSpecs 按计划顺序返回首个 GET 之后的 17 个用例。
func remainingSpecs(v validators, n int) []caseSpec {
	return []caseSpec{
		{"head", http.MethodHead, nil, verifyHead},
		{"range-prefix", http.MethodGet, []string{"Range", "bytes=0-3"}, verifyRange(0, 3)},
		{"range-suffix", http.MethodGet, []string{"Range", "bytes=-4"}, verifyRange(n-4, n-1)},
		{"range-open", http.MethodGet, []string{"Range", "bytes=4-"}, verifyRange(4, n-1)},
		{"range-unsatisfiable", http.MethodGet, []string{"Range", fmt.Sprintf("bytes=%d-", n)}, verifyUnsatisfiable},
		{"etag-match", http.MethodGet, []string{"If-None-Match", v.etag}, verifyNotModified},
		{"etag-any", http.MethodGet, []string{"If-None-Match", "*"}, verifyFull},
		{"etag-weak", http.MethodGet, []string{"If-None-Match", "W/" + v.etag}, verifyNotModified},
		{"etag-miss", http.MethodGet, []string{"If-None-Match", missingETag}, verifyFull},
		{"modified-match", http.MethodGet, []string{"If-Modified-Since", v.lastModified}, verifyNotModified},
		{"modified-old", http.MethodGet, []string{"If-Modified-Since", epochDate}, verifyFull},
		{"if-match-hit", http.MethodGet, []string{"If-Match", v.etag}, verifyFull},
		{"if-match-miss", http.MethodGet, []string{"If-Match", missingETag}, verifyFull},
		{"if-range-hit", http.MethodGet, []string{"Range", "bytes=0-3", "If-Range", v.etag}, verifyRange(0, 3)},
		{"if-range-miss", http.MethodGet, []string{"Range", "bytes=0-3", "If-Range", missingETag}, verifyFull},
		{"conditional-precedence", http.MethodGet, []string{"If-None-Match", missingETag, "If-Modified-Since", v.lastModified}, verifyFull},
		{"head-range", http.MethodHead, []string{"Range", "bytes=0-3"}, verifyHead},
	}
}

// execute performs one request and applies its verifier. The raw fetched
// response is returned alongside the public result so later cases can derive
// validators from unredacted bytes.
// execute 执行单个请求并运行校验；同时返回原始响应，供后续用例从未抹除字节派生 validator。
func (p *probeRun) execute(ctx context.Context, v validators, id, method string, headers []string, verify func(*probeRun, validators, fetched) []string) (CaseResult, fetched, bool) {
	res := CaseResult{ID: id}
	f, failures, transportFail := p.fetch(ctx, method, headers)
	res.HTTPStatus = f.status
	res.Bytes = len(f.body)
	sum := sha256.Sum256(f.body)
	res.SHA256 = hex.EncodeToString(sum[:])
	if f.headers != nil {
		res.Headers = p.recordHeaders(f.headers)
	}
	if verify != nil {
		failures = append(failures, verify(p, v, f)...)
	}
	if len(failures) > 0 {
		res.State = StateFailed
		res.Failures = failures
	} else {
		res.State = StatePassed
	}
	return res, f, transportFail
}

// fetch issues one bodyless GET/HEAD with a per-request timeout and bounded
// reads. Transport errors are classified without echoing raw error text.
// fetch 以每请求超时发出无正文 GET/HEAD 并限制读取；传输错误仅归类不透出原文。
func (p *probeRun) fetch(ctx context.Context, method string, headers []string) (fetched, []string, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, p.opts.URL, nil)
	if err != nil {
		return fetched{}, []string{"network: request build failed"}, true
	}
	if p.opts.Authorization != "" {
		req.Header.Set("Authorization", p.opts.Authorization)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fetched{}, []string{requestFailure(err)}, true
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxFixtureBytes+1))
	f := fetched{status: resp.StatusCode, headers: resp.Header, body: body}
	var failures []string
	transportFail := false
	if readErr != nil {
		failures = append(failures, "body: read error (truncated or aborted)")
		transportFail = true
	}
	if len(body) > MaxFixtureBytes {
		failures = append(failures, "body-limit: response exceeds maximum size")
		transportFail = true
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n > MaxFixtureBytes {
			failures = append(failures, "body-limit: response exceeds maximum size")
			transportFail = true
		}
	}
	return f, failures, transportFail
}

// requestFailure maps transport errors to generic categories.
// requestFailure 将传输错误映射为通用类别。
func requestFailure(err error) string {
	switch {
	case errors.Is(err, errRedirected):
		return "redirect: refused before contacting redirect target"
	case errors.Is(err, context.Canceled):
		return "network: canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "network: timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "network: timeout"
	}
	return "network: request failed"
}

// redact removes every collected secret literal from any text kept in a
// report, in case a server reflects one back.
// redact 从报告文本中抹除全部敏感字面量，防服务器回显。
func (p *probeRun) redact(s string) string {
	for _, secret := range p.secrets {
		s = strings.ReplaceAll(s, secret, redactedLiteral)
	}
	return s
}

// recordHeaders copies only allowlisted response headers, redacted.
// recordHeaders 仅记录允许清单内的响应头并做抹除。
func (p *probeRun) recordHeaders(h http.Header) map[string]string {
	m := make(map[string]string)
	for _, name := range headerAllowlist {
		if v := h.Get(name); v != "" {
			m[name] = p.redact(v)
		}
	}
	return m
}

func wantStatus(f fetched, want int, failures []string) []string {
	if f.status != want {
		failures = append(failures, fmt.Sprintf("status: got %d want %d", f.status, want))
	}
	return failures
}

// checkCommonValidators verifies the frozen success-representation contract
// on every 200/206/HEAD answer: byte-exact etag, same date, whole-file
// checksum headers, octet-stream type and bytes range support — all as
// actually observed.
// checkCommonValidators 校验所有 200/206/HEAD 成功表示：etag 逐字节一致、date 一致、
// 整文件校验头、octet-stream 类型与 bytes 范围支持，均按实际观察冻结。
func (p *probeRun) checkCommonValidators(v validators, f fetched, failures []string) []string {
	if f.headers.Get("ETag") != v.etag {
		failures = append(failures, "etag: missing or differs from first GET")
	}
	lm, ok := parseHTTPDate(f.headers.Get("Last-Modified"))
	if !ok || !lm.Equal(v.lmTime) {
		failures = append(failures, "last-modified: missing or differs from first GET")
	}
	if !strings.EqualFold(f.headers.Get("X-Checksum-Sha1"), v.sha1Hex) {
		failures = append(failures, "checksum-sha1: mismatch")
	}
	if !strings.EqualFold(f.headers.Get("X-Checksum-Sha256"), v.sha256Hex) {
		failures = append(failures, "checksum-sha256: mismatch")
	}
	if got := f.headers.Get("Content-Type"); got != "application/octet-stream" {
		failures = append(failures, "content-type: missing or not application/octet-stream")
	}
	if got := f.headers.Get("Accept-Ranges"); got != "bytes" {
		failures = append(failures, "accept-ranges: missing or not bytes")
	}
	return failures
}

func (p *probeRun) wantContentLength(f fetched, want int, failures []string) []string {
	if got := f.headers.Get("Content-Length"); got != strconv.Itoa(want) {
		failures = append(failures, fmt.Sprintf("content-length: got %q want %q", p.redact(got), strconv.Itoa(want)))
	}
	return failures
}

// verifyFirstGet is stricter than later cases: exact fixture bytes plus the
// full representation header set and parseable validators.
// verifyFirstGet 比后续用例更严：字节完全一致、表示头齐全、validator 可解析。
func verifyFirstGet(p *probeRun, _ validators, f fetched) []string {
	s1, s256 := sha1.Sum(p.opts.Fixture), sha256.Sum256(p.opts.Fixture)
	failures := wantStatus(f, http.StatusOK, nil)
	if !bytesEqual(f.body, p.opts.Fixture) {
		failures = append(failures, fmt.Sprintf("body: differs from fixture (%d bytes received)", len(f.body)))
	}
	failures = p.wantContentLength(f, len(p.opts.Fixture), failures)
	if got := f.headers.Get("Accept-Ranges"); got != "bytes" {
		failures = append(failures, "accept-ranges: missing or not bytes")
	}
	if got := f.headers.Get("Content-Type"); got != "application/octet-stream" {
		failures = append(failures, "content-type: missing or not application/octet-stream")
	}
	if got := f.headers.Get("ETag"); got != hex.EncodeToString(s1[:]) {
		failures = append(failures, "etag: not the bare hex SHA-1 of the fixture")
	}
	if got := f.headers.Get("X-Checksum-Sha1"); !strings.EqualFold(got, hex.EncodeToString(s1[:])) {
		failures = append(failures, "checksum-sha1: mismatch")
	}
	if got := f.headers.Get("X-Checksum-Sha256"); !strings.EqualFold(got, hex.EncodeToString(s256[:])) {
		failures = append(failures, "checksum-sha256: mismatch")
	}
	if _, ok := parseHTTPDate(f.headers.Get("Last-Modified")); !ok {
		failures = append(failures, "last-modified: missing or unparseable")
	}
	return failures
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// verifyFull covers every case whose frozen answer is the entire fixture.
// verifyFull 覆盖所有冻结答案为完整制品的用例。
func verifyFull(p *probeRun, v validators, f fetched) []string {
	failures := wantStatus(f, http.StatusOK, nil)
	if !bytesEqual(f.body, p.opts.Fixture) {
		failures = append(failures, fmt.Sprintf("body: differs from fixture (%d bytes received)", len(f.body)))
	}
	failures = p.wantContentLength(f, len(p.opts.Fixture), failures)
	return p.checkCommonValidators(v, f, failures)
}

// verifyHead covers HEAD answers: empty body with full Content-Length.
// verifyHead 覆盖 HEAD 答案：空正文且 Content-Length 为全长。
func verifyHead(p *probeRun, v validators, f fetched) []string {
	failures := wantStatus(f, http.StatusOK, nil)
	if len(f.body) != 0 {
		failures = append(failures, fmt.Sprintf("body: HEAD response must be empty (%d bytes received)", len(f.body)))
	}
	failures = p.wantContentLength(f, len(p.opts.Fixture), failures)
	return p.checkCommonValidators(v, f, failures)
}

// verifyRange covers 206 answers with exact slice, Content-Range and length.
// verifyRange 校验 206 的精确切片、Content-Range 与长度。
func verifyRange(start, end int) func(*probeRun, validators, fetched) []string {
	return func(p *probeRun, v validators, f fetched) []string {
		failures := wantStatus(f, http.StatusPartialContent, nil)
		want := p.opts.Fixture[start : end+1]
		if !bytesEqual(f.body, want) {
			failures = append(failures, fmt.Sprintf("body: range bytes differ (%d bytes received)", len(f.body)))
		}
		failures = p.wantContentLength(f, len(want), failures)
		wantCR := fmt.Sprintf("bytes %d-%d/%d", start, end, len(p.opts.Fixture))
		if got := f.headers.Get("Content-Range"); got != wantCR {
			failures = append(failures, fmt.Sprintf("content-range: got %q want %q", p.redact(got), wantCR))
		}
		return p.checkCommonValidators(v, f, failures)
	}
}

// verifyNotModified covers 304 answers: empty, same validators, no length.
// verifyNotModified 校验 304：空正文、validator 一致、无 Content-Length。
func verifyNotModified(p *probeRun, v validators, f fetched) []string {
	failures := wantStatus(f, http.StatusNotModified, nil)
	if len(f.body) != 0 {
		failures = append(failures, fmt.Sprintf("body: 304 response must be empty (%d bytes received)", len(f.body)))
	}
	if f.headers.Get("Content-Length") != "" {
		failures = append(failures, "content-length: must be absent on 304")
	}
	if f.headers.Get("ETag") != v.etag {
		failures = append(failures, "etag: missing or differs from first GET")
	}
	lm, ok := parseHTTPDate(f.headers.Get("Last-Modified"))
	if !ok || !lm.Equal(v.lmTime) {
		failures = append(failures, "last-modified: missing or differs from first GET")
	}
	return failures
}

// verifyUnsatisfiable covers the 416 answer.
// verifyUnsatisfiable 校验 416 答案。
func verifyUnsatisfiable(p *probeRun, _ validators, f fetched) []string {
	failures := wantStatus(f, http.StatusRequestedRangeNotSatisfiable, nil)
	if len(f.body) != 0 {
		failures = append(failures, fmt.Sprintf("body: 416 response must be empty (%d bytes received)", len(f.body)))
	}
	failures = p.wantContentLength(f, 0, failures)
	wantCR := fmt.Sprintf("bytes */%d", len(p.opts.Fixture))
	if got := f.headers.Get("Content-Range"); got != wantCR {
		failures = append(failures, fmt.Sprintf("content-range: got %q want %q", p.redact(got), wantCR))
	}
	if f.headers.Get("ETag") != "" {
		failures = append(failures, "etag: must be absent on 416")
	}
	if f.headers.Get("Last-Modified") != "" {
		failures = append(failures, "last-modified: must be absent on 416")
	}
	return failures
}

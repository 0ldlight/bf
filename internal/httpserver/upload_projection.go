// Package httpserver: 上传错误分类与 201 投影专有 helpers。分类只用
// 有限节点/深度预算的一次树遍历，绝不无界 errors.Is 扫描恶意环。
// Package httpserver: upload-specific error classification and 201
// projection helpers. Classification uses one tree walk under a finite
// node/depth budget and never runs unbounded errors.Is scans over
// potentially cyclic trees.
package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// 固定本地政策常量。
// Fixed local policy constants.
const (
	// uploadItemCreatedMediaType 是冻结的 ItemCreated UTF-8 媒体类型。
	// uploadItemCreatedMediaType is the frozen ItemCreated UTF-8 media type.
	uploadItemCreatedMediaType = "application/vnd.org.jfrog.artifactory.storage.ItemCreated+json;charset=UTF-8"
	// uploadCreatedTimeFormat 输出 UTC 精确毫秒（形如 ...T..:..:...000Z）。
	// uploadCreatedTimeFormat emits UTC with exact milliseconds (...Z form).
	uploadCreatedTimeFormat = "2006-01-02T15:04:05.000Z07:00"
	// uploadTreeMaxNodes/uploadTreeMaxDepth 限制一次分类树遍历；环与超
	// 预算树按 500 处理，绝不无限递归。
	// uploadTreeMaxNodes/uploadTreeMaxDepth bound the single classification
	// walk; cyclic and over-budget trees answer 500, never recursing forever.
	uploadTreeMaxNodes = 64
	uploadTreeMaxDepth = 16
	// uploadChainMaxDepth 限制 400/413 单因路径下降。
	// uploadChainMaxDepth bounds the 400/413 single-cause descent.
	uploadChainMaxDepth = 24
)

// errUploadBodyBudget 是私有预算哨兵：正文超过配置预算时由包装 reader
// 返回；文案固定且不含输入。
// errUploadBodyBudget is the private budget sentinel returned by the
// wrapping reader once the body exceeds the configured budget; its fixed
// text carries no input.
var errUploadBodyBudget = errors.New("httpserver: upload body exceeds the configured budget")

// uploadErrorTree 是一次有界遍历的全部事实。
// uploadErrorTree carries everything one bounded walk observed.
type uploadErrorTree struct {
	nodes       int
	overBudget  bool
	persistence bool
	leaves      []error
}

// uploadVisitTree 以有限节点/深度预算做 DFS；nil 子节点按 errors.Join 语
// 义忽略，叶子是无任何 Unwrap 的节点。
// uploadVisitTree runs a DFS under the finite node/depth budget; nil children
// are ignored per errors.Join semantics and a leaf is a node with no Unwrap.
func uploadVisitTree(tree *uploadErrorTree, err error, depth int) {
	if err == nil || tree.overBudget {
		return
	}
	tree.nodes++
	if tree.nodes > uploadTreeMaxNodes || depth > uploadTreeMaxDepth {
		tree.overBudget = true
		return
	}
	if uploadSameError(err, localrepo.ErrUploadPersistence) {
		tree.persistence = true
	}
	children, tooWide := uploadUnwrapChildren(err)
	if tooWide {
		tree.overBudget = true
		return
	}
	if len(children) == 0 {
		tree.leaves = append(tree.leaves, err)
		return
	}
	for _, child := range children {
		uploadVisitTree(tree, child, depth+1)
		if tree.overBudget {
			return
		}
	}
}

// clientOnlyLeaves 报告是否存在至少一个叶子且全部叶子恰为三个客户端
// 哨兵之一；空树不是纯树。
// clientOnlyLeaves reports whether at least one leaf exists and every leaf is
// exactly one of the three client sentinels; an empty tree is never pure.
func (tree *uploadErrorTree) clientOnlyLeaves() bool {
	if tree.overBudget || len(tree.leaves) == 0 {
		return false
	}
	for _, leaf := range tree.leaves {
		if !uploadClientSentinel(leaf) {
			return false
		}
	}
	return true
}

// uploadClientSentinel 判定叶子是否恰为客户端哨兵（恒等比较）。
// uploadClientSentinel checks a leaf is exactly a client sentinel (identity).
func uploadClientSentinel(err error) bool {
	return uploadSameError(err, filestore.ErrChecksumMismatch) ||
		uploadSameError(err, filestore.ErrLengthMismatch) ||
		uploadSameError(err, repository.ErrClientChecksumMismatch)
}

// uploadUnwrapChildren 先取多路 Unwrap 再取单路，与标准库语义一致；nil
// 子节点被过滤；先检查原始宽度（含 nil），再分配与遍历。
// uploadUnwrapChildren prefers the multi Unwrap then the unary one, matching
// the standard library; raw width is checked before allocation/filtering.
// Dependency-defined Unwrap itself must return promptly.
func uploadUnwrapChildren(err error) ([]error, bool) {
	switch unwrappable := err.(type) {
	case interface{ Unwrap() []error }:
		children := unwrappable.Unwrap()
		if len(children) > uploadTreeMaxNodes {
			return nil, true
		}
		nonNil := make([]error, 0, len(children))
		for _, child := range children {
			if child != nil {
				nonNil = append(nonNil, child)
			}
		}
		return nonNil, false
	case interface{ Unwrap() error }:
		if child := unwrappable.Unwrap(); child != nil {
			return []error{child}, false
		}
	}
	return nil, false
}

// uploadSameError 做恒等比较前先核对运行期值可比性：动态类型一致且两个
// 值都运行期可比才用 ==（指针哨兵恒可比、保持恒等语义）；静态可比但含
// 不可比动态值的错误保守视为不等（避免比较 panic），调用侧因此落到 500。
// uploadSameError checks runtime value comparability before ==: only
// identical dynamic types whose values are both runtime-comparable compare
// (pointer sentinels stay comparable and keep identity semantics); errors
// whose type is statically comparable but whose dynamic values are not
// (e.g. an interface field holding a slice) conservatively differ, so no
// comparison panics and callers fall back to 500.
func uploadSameError(a, b error) bool {
	if a == nil || b == nil {
		return false
	}
	if reflect.TypeOf(a) != reflect.TypeOf(b) {
		return false
	}
	if !reflect.ValueOf(a).Comparable() || !reflect.ValueOf(b).Comparable() {
		return false
	}
	return a == b
}

// uploadChainTo 要求从根到目标恰一条非 nil 因果路径（包装与单子 join 均
// 可）；出现任何额外兄弟子树即失败。深度有界，环必然失配。
// uploadChainTo requires exactly one nonnil cause path from the root to the
// target (wrappers and single-child joins allowed); any extra sibling subtree
// fails the match. The bounded depth makes cycles always miss.
func uploadChainTo(err, target error) bool {
	if target == nil {
		return false
	}
	for i := 0; i <= uploadChainMaxDepth && err != nil; i++ {
		if uploadSameError(err, target) {
			return true
		}
		children, tooWide := uploadUnwrapChildren(err)
		if tooWide || len(children) != 1 {
			return false
		}
		err = children[0]
	}
	return false
}

// serveSaveError 按固定优先级分类：父取消已在调用前检查；随后超预算/环
// →500，ErrUploadPersistence 先于一切客户端哨兵（复合清理故障绝不降级
// 4xx）→ 预算哨兵 413 → 记录的读错误 400 → 纯客户端哨兵树 409 → 其余
// 500。错误数据永不回显。
// serveSaveError classifies in the fixed priority: the parent cancellation was
// already checked by the caller; then over-budget/cyclic →500, and
// ErrUploadPersistence outranks every client sentinel (compound cleanup
// faults never downgrade to 4xx) → budget sentinel 413 → recorded read error
// 400 → pure client-sentinel tree 409 → everything else 500. Error data is
// never echoed.
func (h *uploadHandler) serveSaveError(w http.ResponseWriter, r *http.Request, err error, reader *uploadBodyReader) {
	tree := &uploadErrorTree{}
	uploadVisitTree(tree, err, 0)
	status, category := http.StatusInternalServerError, UploadFailureSave
	switch {
	case tree.overBudget || tree.persistence:
		// 超预算/环与持久化故障保持 500。
		// Over-budget/cyclic trees and persistence faults stay 500.
	case uploadChainTo(err, errUploadBodyBudget):
		status = http.StatusRequestEntityTooLarge
		category = UploadFailureBody
	case reader != nil && reader.readErr != nil && uploadChainTo(err, reader.readErr):
		status = http.StatusBadRequest
		category = UploadFailureBody
	case len(tree.leaves) == 1 && uploadSameError(tree.leaves[0], localrepo.ErrUploadCommitDenied):
		h.writeError(w, r.Method, http.StatusForbidden, "")
		return
	case tree.clientOnlyLeaves():
		status = http.StatusConflict
		category = UploadFailureBody
	}
	h.report(category)
	h.writeError(w, r.Method, status, "")
}

// uploadCreatedItem 是 201 的具体 JSON 投影：键序固定、size 为字符串、
// originalChecksums 仅在 SQL original 存在时含 sha1/md5，恒含 sha256=规范
// Actual；无 updated/modified 字段。
// uploadCreatedItem is the concrete 201 JSON projection: fixed key order,
// string size, originalChecksums carrying sha1/md5 only when the SQL original
// exists and always sha256=canonical Actual; no updated/modified field.
type uploadCreatedItem struct {
	Repo              string                        `json:"repo"`
	Path              string                        `json:"path"`
	Created           string                        `json:"created"`
	CreatedBy         string                        `json:"createdBy"`
	DownloadURI       string                        `json:"downloadUri"`
	MimeType          string                        `json:"mimeType"`
	Size              string                        `json:"size"`
	Checksums         uploadCreatedChecksums        `json:"checksums"`
	OriginalChecksums uploadCreatedOriginalChecksum `json:"originalChecksums"`
	URI               string                        `json:"uri"`
}

// uploadCreatedChecksums 是三 Actual 的小写摘要组。
// uploadCreatedChecksums is the lowercase triple of actual digests.
type uploadCreatedChecksums struct {
	SHA1   string `json:"sha1"`
	MD5    string `json:"md5"`
	SHA256 string `json:"sha256"`
}

// uploadCreatedOriginalChecksums 的 sha1/md5 仅在 SQL original 非 nil 时
// 输出；sha256 恒为规范 Actual。
// uploadCreatedOriginalChecksums emits sha1/md5 only for a nonnil SQL
// original; sha256 is always the canonical Actual.
type uploadCreatedOriginalChecksum struct {
	SHA1   *string `json:"sha1,omitempty"`
	MD5    *string `json:"md5,omitempty"`
	SHA256 string  `json:"sha256"`
}

// writeCreated 在 201 之前完成 marshal 与全部表示头；正文恰写一次，任何
// 写失败/短写只上报 write 类别并中止传输，不写第二份表示。
// writeCreated marshals and sets every representation header before the 201;
// the body is written exactly once, and any write failure or short write only
// reports the write category and aborts without a second representation.
func (h *uploadHandler) writeCreated(w http.ResponseWriter, r *http.Request, source repository.FileSnapshotSource) {
	item, uri := uploadBuildCreated(source, h.origin)
	body, err := json.Marshal(item)
	if err != nil {
		h.report(UploadFailureProjection)
		h.writeError(w, r.Method, http.StatusInternalServerError, "")
		return
	}
	w.Header().Set("Content-Type", uploadItemCreatedMediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Location", uri)
	w.Header().Set("X-Checksum-Sha256", source.Reference.Content.SHA256)
	w.WriteHeader(http.StatusCreated)
	if n, err := w.Write(body); err != nil || n != len(body) {
		h.report(UploadFailureWrite)
		panic(http.ErrAbortHandler)
	}
}

// uploadBuildCreated 从已二次校验的 Source 组装投影与受信起点 URI。
// uploadBuildCreated assembles the projection and the trusted-origin URI from
// the twice-validated Source.
func uploadBuildCreated(source repository.FileSnapshotSource, origin string) (uploadCreatedItem, string) {
	reference := source.Reference
	metadata := reference.Metadata
	uri := origin + "/artifactory/" + reference.Path.Repo() + "/" +
		uploadEscapePath(reference.Path.PathName())
	return uploadCreatedItem{
		Repo:        reference.Path.Repo(),
		Path:        "/" + reference.Path.PathName(),
		Created:     time.UnixMilli(metadata.CreatedMillis()).UTC().Format(uploadCreatedTimeFormat),
		CreatedBy:   uploadUnitsString(metadata.CreatedBy().UTF16()),
		DownloadURI: uri,
		MimeType:    downloadMIME(reference.Path.Name()),
		Size:        strconv.FormatInt(reference.Content.Size, 10),
		Checksums: uploadCreatedChecksums{
			SHA1:   reference.Content.SHA1,
			MD5:    reference.Content.MD5,
			SHA256: reference.Content.SHA256,
		},
		OriginalChecksums: uploadCreatedOriginalChecksum{
			SHA1:   uploadOptionalOriginal(metadata.SHA1OriginalUTF16()),
			MD5:    uploadOptionalOriginal(metadata.MD5OriginalUTF16()),
			SHA256: reference.Content.SHA256,
		},
		URI: uri,
	}, uri
}

// uploadOptionalOriginal 把 SQL original 码元（nil=缺省）转成可选字符串；
// 值域已由 ValidateUploadSource 限定为规范小写 hex。
// uploadOptionalOriginal converts SQL original units (nil=absent) into an
// optional string; ValidateUploadSource already bounds values to canonical
// lowercase hex.
func uploadOptionalOriginal(units []uint16) *string {
	if units == nil {
		return nil
	}
	value := uploadUnitsString(units)
	return &value
}

// uploadUnitsString 把已判定为可打印 ASCII 的码元转成字符串。
// uploadUnitsString converts already-validated printable ASCII units.
func uploadUnitsString(units []uint16) string {
	out := make([]byte, len(units))
	for i, unit := range units {
		out[i] = byte(unit)
	}
	return string(out)
}

// uploadEscapePath 逐段 url.PathEscape：对画像内 ASCII 段为恒等，仅作
// CRLF/空格注入防御。
// uploadEscapePath applies per-segment url.PathEscape: identity for in-profile
// ASCII segments, purely a CRLF/space injection defense.
func uploadEscapePath(pathName string) string {
	segments := strings.Split(pathName, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

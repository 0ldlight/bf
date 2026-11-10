package localrepo

import (
	"errors"
	"strings"

	"github.com/0ldlight/bf/internal/repository"
)

// UploadInput 是普通 PUT 的纯输入画像：已构造的存储身份、可选客户端
// Original、UTF-16 操作者与声明大小。纯数据 DTO，不做 I/O，也不修改
// 调用方输入；调用方不得在验证/保存期间并发修改输入。
// UploadInput is the pure input profile of the ordinary PUT: a constructed
// storage identity, optional client Originals, the UTF-16 operator and the
// declared size. It is a plain DTO: no I/O, no caller-input mutation, and
// callers must not mutate inputs concurrently with validation or saving.
type UploadInput struct {
	Path          repository.Path
	Checksums     repository.ClientFileChecksums
	OperatorUTF16 []uint16
	DeclaredSize  int64
}

// MaxUploadMillis 是上传画像允许的最大毫秒时间戳（含）：9999-12-31
// 23:59:59.999 UTC；三个元数据时间取 [0, MaxUploadMillis] 闭区间。
// MaxUploadMillis is the inclusive greatest millisecond timestamp the
// upload profile admits: 9999-12-31T23:59:59.999Z; the three metadata
// times use the closed range [0, MaxUploadMillis].
const MaxUploadMillis int64 = 253402300799999

// 上传画像的显式字节/码元边界；均在本文件内封闭使用。
// Explicit byte/unit bounds of the upload profile; all local to this file.
const (
	maxUploadRepoBytes   = 64
	maxUploadPathBytes   = 8192
	maxUploadParentBytes = 1024
	maxUploadNameBytes   = 255
	maxUploadActorUnits  = 64
)

var (
	// ErrUploadCommitDenied indicates fresh authorization denied the node write.
	ErrUploadCommitDenied = errors.New("localrepo: upload commit authorization denied")
	// ErrInvalidUploadInput 拒绝不符合上传输入画像的请求事实，固定文案
	// 不回显输入。ErrInvalidUploadInput rejects request facts outside the
	// upload input profile with a fixed, echo-free text.
	ErrInvalidUploadInput = errors.New("localrepo: invalid upload input")
	// ErrInvalidUploadSource 拒绝不符合上传 Source 事实画像的同查询结果；
	// 它只做形状/事实校验，不证明提交或字节。ErrInvalidUploadSource
	// rejects same-query sources outside the upload source fact profile; it
	// validates shape/facts only and proves neither commit nor bytes.
	ErrInvalidUploadSource = errors.New("localrepo: invalid upload source")
	// ErrUploadPersistence 标记节点/分配器/投影/提交/回滚故障；持久层适配
	// 器以其包装真实原因，HTTP 必须先于任何校验和/正文哨兵判定它。
	// ErrUploadPersistence marks node/allocator/projection/commit/rollback
	// faults. The persistence adapter wraps real causes with it and HTTP
	// must test it BEFORE any checksum/body sentinel.
	ErrUploadPersistence = errors.New("localrepo: upload persistence failed")
)

func uploadInvalid(op string) error {
	return &fileOpError{op: op, cause: ErrInvalidUploadInput}
}

func uploadSourceInvalid(op string) error {
	return &fileOpError{op: op, cause: ErrInvalidUploadSource}
}

// uploadProfileSegment 判定单个段：非空、仅 ASCII [A-Za-z0-9._-] 且不是
// ./..；其余一切字节（百分号、控制字符、Unicode、加号、冒号等）拒绝。
// uploadProfileSegment admits one segment: nonempty, ASCII [A-Za-z0-9._-]
// only, never ./..; every other byte (percent, control, Unicode, plus,
// colon, ...) is rejected.
func uploadProfileSegment(segment string) bool {
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

// uploadFileName 判定最终名：非空 stem 加精确小写 .bin/.txt 后缀。
// uploadFileName admits a final name: nonempty stem plus the exact
// lowercase .bin/.txt suffix.
func uploadFileName(name string) bool {
	stem, ok := strings.CutSuffix(name, ".bin")
	if !ok {
		stem, ok = strings.CutSuffix(name, ".txt")
	}
	return ok && stem != ""
}

// uploadHexUnits 判定 UTF-16 码元是否恰为定长小写十六进制。
// uploadHexUnits reports UTF-16 units of exactly lowercase hex length.
func uploadHexUnits(units []uint16, wantLen int) bool {
	if len(units) != wantLen {
		return false
	}
	for _, unit := range units {
		if unit >= '0' && unit <= '9' || unit >= 'a' && unit <= 'f' {
			continue
		}
		return false
	}
	return true
}

// uploadLowerHex 判定字符串摘要是否恰为定长小写十六进制。
// uploadLowerHex reports a string digest of exactly lowercase hex length.
func uploadLowerHex(value string, wantLen int) bool {
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

// uploadActorUnits 判定操作者/Actor 码元：1..64、全为可打印 ASCII
// U+0020..U+007E 且非全空格。
// uploadActorUnits admits operator/Actor units: 1..64, all printable
// ASCII U+0020..U+007E, never all spaces.
func uploadActorUnits(units []uint16) bool {
	if len(units) == 0 || len(units) > maxUploadActorUnits {
		return false
	}
	allSpaces := true
	for _, unit := range units {
		if unit < 0x20 || unit > 0x7e {
			return false
		}
		if unit != 0x20 {
			allSpaces = false
		}
	}
	return !allSpaces
}

// uploadOriginalEquals 判定 Original 码元是否恰等于已验证的规范摘要
// （等值蕴含精确小写）；不修改输入。
// uploadOriginalEquals reports Original units exactly equal to an already
// validated canonical digest (equality implies exact lowercase); inputs
// are never modified.
func uploadOriginalEquals(original []uint16, actual string) bool {
	if len(original) != len(actual) {
		return false
	}
	for i, unit := range original {
		if unit != uint16(actual[i]) {
			return false
		}
	}
	return true
}

// uploadPathProfile 判定已构造 Path 身份是否在上传画像内：file、非根、
// repo<=64、parent<=1024、name<=255、段/后缀规则、Depth 在界内，并复用
// ValidateStoragePath 的字符状态机。
// uploadPathProfile reports whether a constructed Path identity is inside
// the upload profile: file kind, nonroot, repo<=64, parent<=1024,
// name<=255, segment/suffix rules, Depth in range, plus the reused
// ValidateStoragePath character state machine.
func uploadPathProfile(path repository.Path) bool {
	if !path.IsFile() || path.IsRoot() {
		return false
	}
	repo, parent, name := path.Repo(), path.Parent(), path.Name()
	if len(repo) > maxUploadRepoBytes || !uploadProfileSegment(repo) {
		return false
	}
	if len(name) > maxUploadNameBytes || !uploadProfileSegment(name) || !uploadFileName(name) {
		return false
	}
	if parent != "" {
		if len(parent) > maxUploadParentBytes {
			return false
		}
		for _, segment := range strings.Split(parent, "/") {
			if !uploadProfileSegment(segment) {
				return false
			}
		}
	}
	if _, err := path.Depth(); err != nil {
		return false
	}
	return repository.ValidateStoragePath(repo+"/"+path.PathName()) == nil
}

// NewUploadPath 从原始 repo/path 构造上传文件身份：repo 单段 ASCII
// [A-Za-z0-9._-] 非点段 <=64 字节；raw path <=8192 字节、斜杠分隔的非空
// 同规则段，无首/尾/重复斜杠、无点段，末名为非空 stem 加精确 .bin/.txt；
// parent<=1024、name<=255；复用 ValidateStoragePath 字符状态机与
// Path.Depth 界检查。不解码、不清洗、不 trim；拒绝返回零 Path 与包装
// ErrInvalidUploadInput 的固定文案错误。
// NewUploadPath builds the upload file identity from raw repo/path: repo is
// one ASCII [A-Za-z0-9._-] non-dot segment of <=64 bytes; the raw path is
// <=8192 bytes of slash-separated nonempty same-rule segments with no
// leading/trailing/repeated slash and no dot segments, ending in a nonempty
// stem plus exact .bin/.txt; parent<=1024 and name<=255; the
// ValidateStoragePath character state machine and the Path.Depth bound are
// reused. No decode/clean/trim; rejections return the zero Path plus a
// fixed-text error wrapping ErrInvalidUploadInput.
func NewUploadPath(repo, path string) (repository.Path, error) {
	reject := func() (repository.Path, error) {
		return repository.Path{}, uploadInvalid("localrepo: upload invalid request path")
	}
	if len(repo) > maxUploadRepoBytes || !uploadProfileSegment(repo) {
		return reject()
	}
	if path == "" || len(path) > maxUploadPathBytes {
		return reject()
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if !uploadProfileSegment(segment) {
			return reject()
		}
	}
	if err := repository.ValidateStoragePath(repo + "/" + path); err != nil {
		return reject()
	}
	name := segments[len(segments)-1]
	if len(name) > maxUploadNameBytes || !uploadFileName(name) {
		return reject()
	}
	parent := ""
	if len(segments) > 1 {
		parent = strings.Join(segments[:len(segments)-1], "/")
		if len(parent) > maxUploadParentBytes {
			return reject()
		}
	}
	identity, err := repository.NewPath(repo, parent, name, true)
	if err != nil {
		return reject()
	}
	if identity.Repo() != repo || identity.Parent() != parent || identity.Name() != name ||
		!identity.IsFile() || identity.IsRoot() {
		return reject()
	}
	if _, err := identity.Depth(); err != nil {
		return reject()
	}
	return identity, nil
}

// ValidateUploadInput 纯校验 UploadInput：Path 在上传画像内、三个 Original
// 为 nil 或恰长小写十六进制（空 present/标记/大写拒绝）、操作者 1..64
// 可打印 ASCII 非全空格、DeclaredSize>=0。无 I/O、不改输入、不采时钟。
// ValidateUploadInput purely validates the UploadInput: Path inside the
// upload profile, the three Originals nil or exact-length lowercase hex
// (empty-present/marker/uppercase rejected), operator 1..64 printable
// ASCII not all spaces, DeclaredSize>=0. No I/O, no mutation, no clock.
func ValidateUploadInput(in UploadInput) error {
	if !uploadPathProfile(in.Path) {
		return uploadInvalid("localrepo: upload invalid request path")
	}
	slots := [...]struct {
		original []uint16
		length   int
	}{
		{in.Checksums.SHA1Original, 40},
		{in.Checksums.SHA256Original, 64},
		{in.Checksums.MD5Original, 32},
	}
	for _, slot := range slots {
		if slot.original != nil && !uploadHexUnits(slot.original, slot.length) {
			return uploadInvalid("localrepo: upload invalid input checksums")
		}
	}
	if !uploadActorUnits(in.OperatorUTF16) {
		return uploadInvalid("localrepo: upload invalid input operator")
	}
	if in.DeclaredSize < 0 {
		return uploadInvalid("localrepo: upload invalid input declared size")
	}
	return nil
}

// ValidateUploadSource 纯校验同查询 Source 事实：ID>0、file/非根 Path 恰等
// 于同样在画像内的 want、三个 Actual 均有效、SHA1/MD5/SHA256 为 40/32/64
// 恰长小写十六进制、size>=0、精确空 SHA1 要求 size 0、Metadata 非 nil 且
// 三时间在 [0,MaxUploadMillis]、CreatedBy/ModifiedBy 非空可打印 ASCII
// 1..64 非全空格、SHA1/MD5 Original 为 nil 或恰等于其 Actual。只做形状/
// 事实校验，不证明提交或字节；不改输入。
// ValidateUploadSource purely validates same-query Source facts: ID>0, a
// file/nonroot Path exactly equal to a want that is itself inside the
// profile, all three Actuals valid, SHA1/MD5/SHA256 as exact-length
// lowercase hex 40/32/64, size>=0, the exact empty SHA1 requiring size 0,
// nonnil Metadata with three times inside [0,MaxUploadMillis],
// CreatedBy/ModifiedBy nonempty printable ASCII 1..64 not all spaces, and
// SHA1/MD5 Originals nil or exactly equal to their Actuals. It is
// shape/fact validation only, proving neither commit nor bytes, and never
// mutates its inputs.
func ValidateUploadSource(source repository.FileSnapshotSource, want repository.Path) error {
	if !uploadPathProfile(want) {
		return uploadSourceInvalid("localrepo: upload invalid source path")
	}
	reference := source.Reference
	if reference.ID <= 0 {
		return uploadSourceInvalid("localrepo: upload invalid source id")
	}
	if !reference.Path.IsFile() || reference.Path.IsRoot() || reference.Path.Key() != want.Key() {
		return uploadSourceInvalid("localrepo: upload invalid source identity")
	}
	if !source.SHA1ActualValid || !source.MD5ActualValid || !source.SHA256ActualValid {
		return uploadSourceInvalid("localrepo: upload invalid source actual")
	}
	if !uploadLowerHex(reference.Content.SHA1, 40) ||
		!uploadLowerHex(reference.Content.MD5, 32) ||
		!uploadLowerHex(reference.Content.SHA256, 64) {
		return uploadSourceInvalid("localrepo: upload invalid source digest")
	}
	if reference.Content.Size < 0 {
		return uploadSourceInvalid("localrepo: upload invalid source size")
	}
	if reference.Content.SHA1 == emptyFileSHA1 && reference.Content.Size != 0 {
		return uploadSourceInvalid("localrepo: upload invalid source size")
	}
	metadata := reference.Metadata
	if metadata == nil {
		return uploadSourceInvalid("localrepo: upload invalid source metadata")
	}
	for _, millis := range [3]int64{
		metadata.CreatedMillis(), metadata.ModifiedMillis(), metadata.UpdatedMillis(),
	} {
		if millis < 0 || millis > MaxUploadMillis {
			return uploadSourceInvalid("localrepo: upload invalid source time")
		}
	}
	for _, actor := range [2]repository.Actor{metadata.CreatedBy(), metadata.ModifiedBy()} {
		if actor.IsNull() || !uploadActorUnits(actor.UTF16()) {
			return uploadSourceInvalid("localrepo: upload invalid source actor")
		}
	}
	if original := metadata.SHA1OriginalUTF16(); original != nil &&
		!uploadOriginalEquals(original, reference.Content.SHA1) {
		return uploadSourceInvalid("localrepo: upload invalid source original")
	}
	if original := metadata.MD5OriginalUTF16(); original != nil &&
		!uploadOriginalEquals(original, reference.Content.MD5) {
		return uploadSourceInvalid("localrepo: upload invalid source original")
	}
	return nil
}

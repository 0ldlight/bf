package repository

import (
	"context"
	"errors"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
)

const (
	stageDirtyUpdateCancelled       = "repository: update file if changed cancelled"
	stageDirtyUpdateInvalidID       = "repository: update file if changed invalid id"
	stageDirtyUpdateUnrepresentable = "repository: update file if changed unrepresentable snapshot"
)

// ErrUnrepresentableFileSnapshot 表示写侧字符串含有无法无损转为 UTF-8 的
// 孤立 UTF-16 代理；它不表示原始快照结构无效。
// ErrUnrepresentableFileSnapshot identifies unpaired UTF-16 surrogates in a
// write-side string, distinct from a structurally invalid raw snapshot.
var ErrUnrepresentableFileSnapshot = errors.New("repository: unrepresentable file snapshot")

var errNilDirtyUpdateDependency = errors.New("repository: update file if changed requires nonnil update callback")

// FileDirtyUpdateInput 由调用方提供同一比较域的完整原始状态与对应既有 ID。
// 所有写操作数只从 Desired 派生；不查找、重建或验证状态溯源。
// FileDirtyUpdateInput carries caller-owned complete states in the same
// comparison domain and their existing ID. Desired alone supplies the write
// operands; there is no lookup, reconstruction or provenance validation.
type FileDirtyUpdateInput struct {
	Existing      FileInfoSnapshot
	Desired       FileInfoSnapshot
	IgnoreCreated bool
	ID            int64
}

// FileDirtyUpdateResult 仅在无错误时解释：Changed=false 表示跳过节点写；
// Changed=true 表示执行更新分支，Count 为委托计数（含 0），不表示已提交。
// FileDirtyUpdateResult is meaningful on success only: Changed=false means
// the node write was skipped; true means the update branch ran. Count is the
// delegate count, including zero, and establishes no transaction commit.
type FileDirtyUpdateResult struct {
	Changed bool
	Count   int64
}

// UpdateFileIfChanged 按 ctx、委托配置、正 ID、原始比较的顺序检查；相同
// 状态立即跳过。变化时按 repoKey/path/SHA1/SHA256/MD5 顺序转换字符串，
// 再取七字段元数据并调用 UpdateFile。Actual 缺席/null/空均投影为空串，
// 不做摘要规范化；路径重构与 Store 校验留在原层。非 nil ctx、ID 与状态
// 对应关系由调用方保证。错误返回零结果，成功后不追加取消检查；不拥有
// 事务、锁、CAS 或属性/关注行为。完整边界见 docs/FILE-SNAPSHOT-UPDATE.md。
// UpdateFileIfChanged checks context, callback configuration, positive ID
// and raw equality in order, returning immediately for identical states.
// Dirty states convert repoKey/path/SHA1/SHA256/MD5 in order, project metadata
// and call UpdateFile. Missing/null/empty Actuals map to empty strings without
// digest normalization. Existing layers own path reconstruction and Store
// validation. Callers supply a nonnil context and matching ID/state provenance.
// Errors return zero; success has no appended cancellation check. Transactions,
// locks, CAS and properties/watches are outside this node-update seam.
func UpdateFileIfChanged(ctx context.Context, in FileDirtyUpdateInput, update func(context.Context, FileReference) (int64, error)) (FileDirtyUpdateResult, error) {
	if err := ctx.Err(); err != nil {
		return FileDirtyUpdateResult{}, &updateOpError{op: stageDirtyUpdateCancelled, cause: err}
	}
	if update == nil {
		return FileDirtyUpdateResult{}, errNilDirtyUpdateDependency
	}
	if in.ID <= 0 {
		return FileDirtyUpdateResult{}, &updateOpError{op: stageDirtyUpdateInvalidID, cause: ErrInvalidUpdateID}
	}
	identical, err := in.Desired.IsIdentical(in.Existing, in.IgnoreCreated)
	if err != nil || identical {
		return FileDirtyUpdateResult{}, err
	}

	s := in.Desired
	inputs := [5][]uint16{s.repoKey, s.repoPath,
		s.checksums[ChecksumSHA1].actual,
		s.checksums[ChecksumSHA256].actual,
		s.checksums[ChecksumMD5].actual}
	var values [5]string
	for i, units := range inputs {
		values[i], err = fileDirtyUpdateString(units)
		if err != nil {
			return FileDirtyUpdateResult{}, &updateOpError{op: stageDirtyUpdateUnrepresentable, cause: err}
		}
	}
	metadata, err := s.NodeMetadata()
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	count, err := UpdateFile(ctx, FileUpdateInput{
		ID: in.ID, Repo: values[0], Path: values[1],
		Content:  content.Metadata{SHA1: values[2], SHA256: values[3], MD5: values[4], Size: s.size},
		Metadata: &metadata,
	}, update)
	if err != nil {
		return FileDirtyUpdateResult{}, err
	}
	return FileDirtyUpdateResult{Changed: true, Count: count}, nil
}

// 仅转换本切片的写侧字符串；原始 actor/original 继续走既有元数据边界。
// Convert only this seam's write-side strings; actors/originals retain their
// existing metadata boundary and may still contain unpaired raw units.
func fileDirtyUpdateString(units []uint16) (string, error) {
	runes := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u >= 0xd800 && u <= 0xdbff:
			if i+1 == len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", ErrUnrepresentableFileSnapshot
			}
			runes = append(runes, utf16.DecodeRune(rune(u), rune(units[i+1])))
			i++
		case u >= 0xdc00 && u <= 0xdfff:
			return "", ErrUnrepresentableFileSnapshot
		default:
			runes = append(runes, rune(u))
		}
	}
	return string(runes), nil
}

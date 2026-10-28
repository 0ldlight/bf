package repository

import (
	"errors"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrInvalidFileSnapshotSource 表示缺少元数据、Actual 空性矛盾或畸形 UTF-8。
// ErrInvalidFileSnapshotSource marks missing metadata, inconsistent Actual
// nullness or malformed UTF-8 in a snapshot source.
var ErrInvalidFileSnapshotSource = errors.New("repository: invalid file snapshot source")

// ErrUnsupportedLoadedOriginal 表示恰长 Original 超出所选有限画像；
// 它不是目标校验和校验错误。
// ErrUnsupportedLoadedOriginal marks an exact-length Original outside the
// selected bounded profile; it is not a target checksum-validation error.
var ErrUnsupportedLoadedOriginal = errors.New("repository: unsupported loaded original")

// LoadedOriginalCase 选择有限的 Original 加载规则；零值保留旧入口的
// Locale 无关范围。显式规则使用有限 Latin-1/dotted-I 域，不推断部署 Locale。
// LoadedOriginalCase selects bounded Original loading rules. Zero preserves
// the legacy locale-invariant domain; explicit modes use bounded Latin-1 and
// dotted/dotless-I domains, without inferring a deployed locale.
type LoadedOriginalCase uint8

const (
	LoadedOriginalCaseInvariant LoadedOriginalCase = iota
	LoadedOriginalCaseNonTurkic
	LoadedOriginalCaseTurkic
)

// ErrUnsupportedLoadedOriginalCase 拒绝未知规则，不包含输入。
// ErrUnsupportedLoadedOriginalCase rejects unknown modes without input details.
var ErrUnsupportedLoadedOriginalCase = errors.New("repository: unsupported loaded original case")

// LoadedFileSnapshot 从唯一 Source 组装即时加载快照，保留 Actual 的 NULL/空
// 区别；对不含大写 I 的恰长 ASCII Original 小写化。MIME 为 null，不取时钟、配置
// 或复制基线，也不接保存门。失败返回零快照及固定错误，不回显输入。
// LoadedFileSnapshot assembles an immediate loaded snapshot from one source,
// preserving NULL versus empty Actuals and lowercasing only exact-length
// ASCII Originals without capital I. MIME is null; no clock, configuration, copied baseline
// or save gate is involved. Failures return a zero snapshot and a fixed error
// without echoing inputs.
func LoadedFileSnapshot(source FileSnapshotSource) (FileInfoSnapshot, error) {
	return LoadedFileSnapshotWithOriginalCase(source, LoadedOriginalCaseInvariant)
}

// LoadedFileSnapshotWithOriginalCase 使用调用方显式选择的有限规则组装，
// 不读取系统 Locale。未知规则在身份/Metadata/Actual 检查后、Original 前拒绝。
// 非恰长 Original 原样保留；显式恰长域与 NonTurkic CC/CD 排除见规范。
// LoadedFileSnapshotWithOriginalCase assembles using a caller-selected bounded
// mode, never the host locale. Reject unknown modes after identity/metadata/
// Actual guards, before Originals. Non-exact lengths remain raw; explicit
// exact-length domains and NonTurkic CC/CD exclusions follow the documented contract.
func LoadedFileSnapshotWithOriginalCase(source FileSnapshotSource, originalCase LoadedOriginalCase) (FileInfoSnapshot, error) {
	repoKey, repoPath, name, err := LoadedFileIdentity(source.Reference.Path)
	if err != nil {
		return FileInfoSnapshot{}, err
	}
	if source.Reference.Metadata == nil {
		return FileInfoSnapshot{}, ErrInvalidFileSnapshotSource
	}
	slots := [...]struct {
		value string
		valid bool
	}{
		{source.Reference.Content.SHA1, source.SHA1ActualValid},
		{source.Reference.Content.MD5, source.MD5ActualValid},
		{source.Reference.Content.SHA256, source.SHA256ActualValid},
	}
	for _, slot := range slots {
		if !slot.valid && slot.value != "" {
			return FileInfoSnapshot{}, ErrInvalidFileSnapshotSource
		}
		if !utf8.ValidString(slot.value) {
			return FileInfoSnapshot{}, ErrInvalidFileSnapshotSource
		}
	}

	if originalCase != LoadedOriginalCaseInvariant && originalCase != LoadedOriginalCaseNonTurkic && originalCase != LoadedOriginalCaseTurkic {
		return FileInfoSnapshot{}, ErrUnsupportedLoadedOriginalCase
	}

	metadata := *source.Reference.Metadata
	created := metadata.CreatedMillis()
	modified := metadata.ModifiedMillis()
	if modified <= 0 {
		modified = created
	}
	updated := metadata.UpdatedMillis()
	createdBy := metadata.CreatedBy().UTF16()
	modifiedBy := metadata.ModifiedBy().UTF16()
	size := source.Reference.Content.Size
	sha1Original, err := loadedSnapshotOriginal(metadata.SHA1OriginalUTF16(), 40, originalCase)
	if err != nil {
		return FileInfoSnapshot{}, err
	}
	md5Original, err := loadedSnapshotOriginal(metadata.MD5OriginalUTF16(), 32, originalCase)
	if err != nil {
		return FileInfoSnapshot{}, err
	}
	var actuals [3][]uint16
	for i, slot := range slots {
		if slot.valid {
			actuals[i] = utf16.Encode([]rune(slot.value))
		}
	}
	return NewFileInfoSnapshot(FileInfoSnapshotInput{
		RepoKeyUTF16: repoKey, RepoPathUTF16: repoPath, NameUTF16: name,
		CreatedMillis: created, LastModifiedMillis: modified, LastUpdatedMillis: updated,
		CreatedByUTF16: createdBy, ModifiedByUTF16: modifiedBy,
		SizeBytes: size, MimeTypeUTF16: nil,
		Checksums: []ChecksumEntryInput{
			{Kind: ChecksumSHA1, Actual: actuals[0], Original: sha1Original},
			{Kind: ChecksumMD5, Actual: actuals[1], Original: md5Original},
			{Kind: ChecksumSHA256, Actual: actuals[2], Original: []uint16{'N', 'O', '_', 'O', 'R', 'I', 'G'}},
		},
	})
}

// loadedSnapshotOriginal 按 UTF-16 码元长度选择分支，不猜测 Locale；非恰长
// 原样保留；恰长按已验证规则处理，输出不修改传入码元。
// loadedSnapshotOriginal branches on UTF-16 unit length without assuming a
// locale. Other lengths remain verbatim; exact lengths use the validated
// bounded mode without modifying supplied units.
func loadedSnapshotOriginal(units []uint16, length int, originalCase LoadedOriginalCase) ([]uint16, error) {
	if len(units) != length {
		return units, nil
	}

	normalized := make([]uint16, len(units))
	for i, unit := range units {
		switch originalCase {
		case LoadedOriginalCaseInvariant:
			if unit > 127 || unit == 'I' {
				return nil, ErrUnsupportedLoadedOriginal
			}
		case LoadedOriginalCaseNonTurkic:
			// Lithuanian 对 CC/CD 无条件扩长，不能当作 ROOT。
			// Lithuanian expands CC/CD unconditionally; never assume ROOT.
			if unit == 0x00CC || unit == 0x00CD || (unit > 255 && unit != 0x0131) {
				return nil, ErrUnsupportedLoadedOriginal
			}
		case LoadedOriginalCaseTurkic:
			if unit > 255 && unit != 0x0130 && unit != 0x0131 {
				return nil, ErrUnsupportedLoadedOriginal
			}
		}
		switch {
		case originalCase == LoadedOriginalCaseTurkic && unit == 'I':
			unit = 0x0131
		case originalCase == LoadedOriginalCaseTurkic && unit == 0x0130:
			unit = 'i'
		case unit >= 'A' && unit <= 'Z', unit >= 0x00C0 && unit <= 0x00D6, unit >= 0x00D8 && unit <= 0x00DE:
			unit += 'a' - 'A'
		}
		normalized[i] = unit
	}
	return normalized, nil
}

// FileInfoSnapshot 显式捕获原版比较器实际读取的映射前 FileInfo 状态：
// 可空原始 UTF-16（nil=null、非 nil 空=空串）、完整有符号 int64、按键的
// 校验和存在性与 actual/original。构造与 Fields 深复制；无 setter。
// FileInfoSnapshot explicitly captures pre-mapping FileInfo state for the
// target comparator: nullable raw UTF-16 (nil=null, non-nil empty=empty
// string), full signed int64 values, keyed checksum presence with
// actual/original. Construction and Fields deep-copy; no setters.
package repository

import "errors"

// ChecksumKind 标识校验和条目类型；全集为冻结的 sha1/md5/sha256，无其他值。
// ChecksumKind identifies a checksum entry kind; the frozen universe is
// sha1/md5/sha256 with no other values.
type ChecksumKind uint8

const (
	ChecksumSHA1 ChecksumKind = iota + 1
	ChecksumMD5
	ChecksumSHA256
)

// ChecksumEntryInput 是单条校验和的显式输入：Actual/Original 为原始
// UTF-16 码元；NO_ORIG 只是普通字面串，无特殊语义。
// ChecksumEntryInput is the explicit input for one checksum entry:
// Actual/Original are raw UTF-16 units; NO_ORIG is an ordinary literal
// with no special semantics.
type ChecksumEntryInput struct {
	Kind     ChecksumKind
	Actual   []uint16
	Original []uint16
}

// FileInfoSnapshotInput 是快照的完整输入/输出 DTO。nil/空 Checksums 切片
// 均表示合法非 null 容器中的空图。
// FileInfoSnapshotInput is the snapshot's full input/output DTO. Nil and
// empty Checksums slices both denote an empty map inside a valid nonnull
// container.
type FileInfoSnapshotInput struct {
	RepoKeyUTF16, RepoPathUTF16, NameUTF16               []uint16
	CreatedMillis, LastModifiedMillis, LastUpdatedMillis int64
	CreatedByUTF16, ModifiedByUTF16                      []uint16
	SizeBytes                                            int64
	MimeTypeUTF16                                        []uint16
	Checksums                                            []ChecksumEntryInput
}

// ErrInvalidFileInfoSnapshot 表示输入违反受限画像：必填 repoKey/path/name
// 为 nil、未知或重复校验和 Kind，或零值快照参与比较。固定信息，不携带
// 原始内容；是明确的本地 Go API 策略，不声称复刻 Java NPE。
// ErrInvalidFileInfoSnapshot marks input violating the restricted profile:
// nil required repoKey/path/name, unknown or duplicate checksum kinds, or a
// zero snapshot in a comparison. Fixed message, no raw content; explicit
// local Go API policy, not Java NPE parity.
var ErrInvalidFileInfoSnapshot = errors.New("repository: invalid file info snapshot")

type fileSnapshotChecksum struct {
	actual   []uint16
	original []uint16
}

// FileInfoSnapshot 是不可变快照纯值。零值无效：Fields 返回零输入，
// IsIdentical 返回 ErrInvalidFileInfoSnapshot。
// FileInfoSnapshot is the immutable snapshot pure value. The zero value is
// invalid: Fields returns a zero input and IsIdentical returns
// ErrInvalidFileInfoSnapshot.
type FileInfoSnapshot struct {
	repoKey      []uint16
	repoPath     []uint16
	name         []uint16
	created      int64
	lastModified int64
	lastUpdated  int64
	createdBy    []uint16
	modifiedBy   []uint16
	size         int64
	mime         []uint16
	checksums    map[ChecksumKind]fileSnapshotChecksum
	valid        bool
}

// fileSnapshotCopyUnits 深复制码元序列：nil 保持 nil，非 nil 空（含高容量）
// 保持非 nil 空。
func fileSnapshotCopyUnits(src []uint16) []uint16 {
	if src == nil {
		return nil
	}
	dst := make([]uint16, len(src))
	copy(dst, src)
	return dst
}

// fileSnapshotEqualUnits 比较 UTF-16 码元：nil 与非 nil 空互异。
func fileSnapshotEqualUnits(a, b []uint16) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
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

// NewFileInfoSnapshot 构造有效快照：拒绝 nil repoKey/path/name、未知/
// 重复 Kind 与超出三种已知 Kind 的条目数；深复制全部嵌套切片。不读取
// 时钟、配置或环境。
// NewFileInfoSnapshot builds a valid snapshot: nil repoKey/path/name,
// unknown or duplicate kinds, and more entries than the three known kinds
// are rejected before any allocation or copying. No clock, config or
// environment reads.
func NewFileInfoSnapshot(in FileInfoSnapshotInput) (FileInfoSnapshot, error) {
	if in.RepoKeyUTF16 == nil || in.RepoPathUTF16 == nil || in.NameUTF16 == nil {
		return FileInfoSnapshot{}, ErrInvalidFileInfoSnapshot
	}
	// 画像只含三种 Kind 且各至多一条：先拒绝必然非法的长度，再分配。
	// The profile has three kinds at most once each: reject impossible
	// lengths before any map allocation.
	if len(in.Checksums) > 3 {
		return FileInfoSnapshot{}, ErrInvalidFileInfoSnapshot
	}
	var checksums map[ChecksumKind]fileSnapshotChecksum
	if len(in.Checksums) > 0 {
		checksums = make(map[ChecksumKind]fileSnapshotChecksum, len(in.Checksums))
		for _, c := range in.Checksums {
			if c.Kind != ChecksumSHA1 && c.Kind != ChecksumMD5 && c.Kind != ChecksumSHA256 {
				return FileInfoSnapshot{}, ErrInvalidFileInfoSnapshot
			}
			if _, dup := checksums[c.Kind]; dup {
				return FileInfoSnapshot{}, ErrInvalidFileInfoSnapshot
			}
			checksums[c.Kind] = fileSnapshotChecksum{
				actual:   fileSnapshotCopyUnits(c.Actual),
				original: fileSnapshotCopyUnits(c.Original),
			}
		}
	}
	return FileInfoSnapshot{
		repoKey:      fileSnapshotCopyUnits(in.RepoKeyUTF16),
		repoPath:     fileSnapshotCopyUnits(in.RepoPathUTF16),
		name:         fileSnapshotCopyUnits(in.NameUTF16),
		created:      in.CreatedMillis,
		lastModified: in.LastModifiedMillis,
		lastUpdated:  in.LastUpdatedMillis,
		createdBy:    fileSnapshotCopyUnits(in.CreatedByUTF16),
		modifiedBy:   fileSnapshotCopyUnits(in.ModifiedByUTF16),
		size:         in.SizeBytes,
		mime:         fileSnapshotCopyUnits(in.MimeTypeUTF16),
		checksums:    checksums,
		valid:        true,
	}, nil
}

// Fields 返回完整防御性副本。校验和条目的迭代顺序未指定，不构成契约；
// 消费方按 Kind 读取。零值快照返回零输入，不宣称有效。
// Fields returns a full defensive copy. Checksum entry iteration order is
// unspecified and not a contract; consumers read by kind. A zero snapshot
// returns a zero input without claiming validity.
func (s FileInfoSnapshot) Fields() FileInfoSnapshotInput {
	if !s.valid {
		return FileInfoSnapshotInput{}
	}
	var checksums []ChecksumEntryInput
	if len(s.checksums) > 0 {
		checksums = make([]ChecksumEntryInput, 0, len(s.checksums))
		for _, kind := range []ChecksumKind{ChecksumSHA1, ChecksumMD5, ChecksumSHA256} {
			if c, ok := s.checksums[kind]; ok {
				checksums = append(checksums, ChecksumEntryInput{
					Kind:     kind,
					Actual:   fileSnapshotCopyUnits(c.actual),
					Original: fileSnapshotCopyUnits(c.original),
				})
			}
		}
	}
	return FileInfoSnapshotInput{
		RepoKeyUTF16:       fileSnapshotCopyUnits(s.repoKey),
		RepoPathUTF16:      fileSnapshotCopyUnits(s.repoPath),
		NameUTF16:          fileSnapshotCopyUnits(s.name),
		CreatedMillis:      s.created,
		LastModifiedMillis: s.lastModified,
		LastUpdatedMillis:  s.lastUpdated,
		CreatedByUTF16:     fileSnapshotCopyUnits(s.createdBy),
		ModifiedByUTF16:    fileSnapshotCopyUnits(s.modifiedBy),
		SizeBytes:          s.size,
		MimeTypeUTF16:      fileSnapshotCopyUnits(s.mime),
		Checksums:          checksums,
	}
}

// IsIdentical 比较两个有效快照的显式状态：lastModified、条件 created
// （ignoreCreated=true 对应目标开关 true：仅跳过 created）、repoPath/repoKey、
// 独立 name、lastUpdated、modifiedBy/createdBy、按键校验和存在性与
// actual/original、size 与原始 MIME。id 与 folder 标志不参与；结果不直接
// 控制任何 SQL/属性/锁行为。任一操作数为零值无效时返回错误。
// IsIdentical compares the explicit state of two valid snapshots:
// lastModified, conditional created (ignoreCreated=true matches the target
// switch true: skips created only), repoPath/repoKey, the independent name,
// lastUpdated, modifiedBy/createdBy, keyed checksum presence with
// actual/original, size and raw MIME. ID and folder flags do not
// participate; the result controls no SQL/property/lock behavior. An
// invalid zero operand returns ErrInvalidFileInfoSnapshot.
func (s FileInfoSnapshot) IsIdentical(other FileInfoSnapshot, ignoreCreated bool) (bool, error) {
	if !s.valid || !other.valid {
		return false, ErrInvalidFileInfoSnapshot
	}
	if s.lastModified != other.lastModified {
		return false, nil
	}
	if !ignoreCreated && s.created != other.created {
		return false, nil
	}
	if !fileSnapshotEqualUnits(s.repoPath, other.repoPath) ||
		!fileSnapshotEqualUnits(s.repoKey, other.repoKey) ||
		!fileSnapshotEqualUnits(s.name, other.name) {
		return false, nil
	}
	if s.lastUpdated != other.lastUpdated {
		return false, nil
	}
	if !fileSnapshotEqualUnits(s.modifiedBy, other.modifiedBy) ||
		!fileSnapshotEqualUnits(s.createdBy, other.createdBy) {
		return false, nil
	}
	if len(s.checksums) != len(other.checksums) {
		return false, nil
	}
	for kind, c := range s.checksums {
		o, ok := other.checksums[kind]
		if !ok || !fileSnapshotEqualUnits(c.actual, o.actual) ||
			!fileSnapshotEqualUnits(c.original, o.original) {
			return false, nil
		}
	}
	if s.size != other.size {
		return false, nil
	}
	return fileSnapshotEqualUnits(s.mime, other.mime), nil
}

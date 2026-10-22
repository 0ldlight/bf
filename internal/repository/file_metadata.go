package repository

// FileMetadataInput 是构造 FileMetadata 的显式输入：三个独立的 signed
// int64 毫秒时间、两个已由 NewActor 规范化的 Actor 值，以及两个可空原始
// UTF-16 original 校验和。它表示 fileInfoToNode 映射之后的 original 值
// （NO_ORIG 标记原样），ChecksumInfo/FileInfo 构造器与 setter 的归一化
// 属于其他边界，不在本输入内重复。
// FileMetadataInput is the explicit construction input for FileMetadata:
// three independent signed int64 millisecond times, two Actor values already
// normalized by NewActor, and two nullable raw UTF-16 original checksums.
// It represents post-mapping original values (the NO_ORIG marker verbatim);
// ChecksumInfo/FileInfo constructor and setter normalization belongs to
// separate boundaries and is not repeated here.
type FileMetadataInput struct {
	CreatedMillis  int64
	ModifiedMillis int64
	UpdatedMillis  int64
	CreatedBy      Actor
	ModifiedBy     Actor
	SHA1Original   []uint16
	MD5Original    []uint16
}

// FileMetadata 是逐节点文件元数据的显式值快照（目标 Node 的 created/
// modified/updated、createdBy/modifiedBy 与 sha1Original/md5Original）。
// 私有存储：时间与 Actor 按值，original 为私有码元切片（nil=null、非 nil
// 可空=present）。零值快照时间为 0、两 Actor 为 null、两 original 为
// nil——这是显式快照零值，不是目标新建 builder 的当前时间默认值；未来
// 创建流程自行提供已选值。无 setter。
// FileMetadata is the explicit per-node file-metadata value snapshot (the
// target Node's created/modified/updated, createdBy/modifiedBy and
// sha1Original/md5Original). Private storage: times and Actors by value,
// originals as private unit slices (nil=null, nonnil possibly-empty=
// present). The zero snapshot has zero times, null actors and nil
// originals — an explicit-snapshot zero, not the target fresh-builder
// clock defaults; future creation code supplies its own chosen values.
// There are no setters.
type FileMetadata struct {
	createdMillis  int64
	modifiedMillis int64
	updatedMillis  int64
	createdBy      Actor
	modifiedBy     Actor
	sha1Original   []uint16
	md5Original    []uint16
}

// copyOriginalUnits 复制一个 original：nil 保持 nil，非 nil（含空）复制
// 恰好 len(src) 个码元，不保留输入后备数组，也不保留多余容量。两个
// original 构造与读取各用一次，互不共享后备存储。
// copyOriginalUnits copies one original: nil stays nil, nonnil (including
// empty) copies exactly len(src) units, retaining neither the caller
// backing array nor excess capacity. Construction and each read of the two
// originals use it independently and never share a backing store.
func copyOriginalUnits(src []uint16) []uint16 {
	if src == nil {
		return nil
	}
	units := make([]uint16, len(src))
	copy(units, src)
	return units
}

// NewFileMetadata 构造显式快照：三个时间逐字复制、相互独立，完整保留
// signed int64 范围（含负数与 0），不取当前时间、不排序、不强迫相等，
// 也不转换 time.Time；两个 Actor 按值保存（输入已经 NewActor 规范化），
// 无额外截断或转换。两个 original 构造即复制：nil=null、非 nil 空=空串，
// 其余逐码元原样保留任意长度——大小写、NO_ORIG 标记、空白/组合码元、
// NUL、孤立/反序/有效代理与畸形值一律不做缩写（无 Actor 64 码元规则）、
// 不做十六进制/长度/上传合法性验证、不小写化、不替换标记、不按 UTF-8/
// rune 重编码。输入后备数组（包括大容量空子切片）从不保留；两字段互为
// 重叠子切片也不共享后备存储；构造与调用方写入并发不在承诺内。无错误
// 返回；original 存储量 O(输入长度)。
// NewFileMetadata constructs the explicit snapshot: the three times copy
// verbatim and independently across the full signed int64 range (negatives
// and zero included) with no clock, ordering, forced equality or time.Time
// conversion; the two Actors are stored by value (inputs already normalized
// by NewActor) with no extra truncation or conversion. Both originals are
// copied on construction: nil means null and a nonnil empty means the empty
// string, everything else keeps every unit verbatim at any length — case,
// the NO_ORIG marker, whitespace/combining units, NUL, lone/reversed/valid
// surrogates and malformed values get no abbreviation (no Actor 64-unit
// rule), no hex/length/upload validation, no lowercasing, no marker
// substitution and no UTF-8/rune re-encoding. Caller backing arrays
// (including large-capacity empty subslices) are never retained; overlapping
// input subslices never share a backing store; construction racing caller
// writes is outside the contract. There is no error result; original
// storage is O(input length).
func NewFileMetadata(in FileMetadataInput) FileMetadata {
	return FileMetadata{
		createdMillis:  in.CreatedMillis,
		modifiedMillis: in.ModifiedMillis,
		updatedMillis:  in.UpdatedMillis,
		createdBy:      in.CreatedBy,
		modifiedBy:     in.ModifiedBy,
		sha1Original:   copyOriginalUnits(in.SHA1Original),
		md5Original:    copyOriginalUnits(in.MD5Original),
	}
}

// CreatedMillis、ModifiedMillis、UpdatedMillis 逐字返回构造时提供的
// signed int64 毫秒，三者相互独立。
// CreatedMillis, ModifiedMillis and UpdatedMillis return the signed int64
// milliseconds supplied at construction verbatim and independently.
func (m FileMetadata) CreatedMillis() int64  { return m.createdMillis }
func (m FileMetadata) ModifiedMillis() int64 { return m.modifiedMillis }
func (m FileMetadata) UpdatedMillis() int64  { return m.updatedMillis }

// CreatedBy、ModifiedBy 按值返回既有 Actor 纯值，依赖其封装只读接口。
// CreatedBy and ModifiedBy return the existing Actor pure values by value,
// relying on their encapsulated read-only API.
func (m FileMetadata) CreatedBy() Actor  { return m.createdBy }
func (m FileMetadata) ModifiedBy() Actor { return m.modifiedBy }

// SHA1OriginalUTF16、MD5OriginalUTF16 每次返回防御性副本：null 返回
// nil，present-empty 返回非 nil 零长度，其余逐码元返回原始码元；修改
// 构造输入或任何返回切片都不能改变既有快照或其值副本；并发读者可各自
// 修改自己的副本。有意不提供自定义 String/JSON/UTF-8/driver.Valuer/
// Scanner/传输转换；默认 fmt/反射行为不是脱敏或传输保证。
// SHA1OriginalUTF16 and MD5OriginalUTF16 each return a defensive copy:
// nil for null, a nonnil zero length for present-empty, otherwise the raw
// units verbatim; mutating the construction input or any returned slice can
// never change an existing snapshot or its value copies, and concurrent
// readers may mutate their own copies. No custom String/JSON/UTF-8/
// driver.Valuer/Scanner/transport conversion is provided on purpose;
// default fmt/reflection behavior is not a redaction or transport
// guarantee.
func (m FileMetadata) SHA1OriginalUTF16() []uint16 {
	return copyOriginalUnits(m.sha1Original)
}

func (m FileMetadata) MD5OriginalUTF16() []uint16 {
	return copyOriginalUnits(m.md5Original)
}

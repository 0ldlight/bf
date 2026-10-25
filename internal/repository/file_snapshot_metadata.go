package repository

// NodeMetadata 将有效快照投影为目标 Node 保存所需的七字段文件元数据：
// 三个毫秒时间逐字复制；createdBy/modifiedBy 经 NewActor 走既有 64 码元
// 缩写；sha1Original/md5Original 取对应条目的原始 Original 逐字复制
// （NO_ORIG 等标记不替换、不缩写、不校验 actual）。缺席 SHA1/MD5 条目
// 投影 nil original（本地扩域）；条目存在时其 Actual 是否为 null/空/空白
// 不影响投影（本地规则）。SHA256 original、actual、MIME、仓库身份与
// size 不进入投影。零值快照返回零元数据与既有 ErrInvalidFileInfoSnapshot。
// NodeMetadata projects a valid snapshot into the seven file-metadata
// fields the target Node save consumes: the three millisecond times copy
// verbatim; createdBy/modifiedBy go through NewActor's existing 64-unit
// abbreviation; sha1Original/md5Original copy the corresponding entry's
// raw Original verbatim (the NO_ORIG marker and friends are neither
// replaced, abbreviated nor validated against actual). An absent SHA1 or
// MD5 entry projects a nil original (local extension); when an entry is
// present its Actual being null/empty/blank never changes the projection
// (local rule). SHA256 original, actuals, MIME, repository identity and
// size never enter the projection. The zero snapshot returns zero
// metadata plus the existing ErrInvalidFileInfoSnapshot.
func (s FileInfoSnapshot) NodeMetadata() (FileMetadata, error) {
	if !s.valid {
		return FileMetadata{}, ErrInvalidFileInfoSnapshot
	}
	var sha1Original, md5Original []uint16
	if c, ok := s.checksums[ChecksumSHA1]; ok {
		sha1Original = c.original
	}
	if c, ok := s.checksums[ChecksumMD5]; ok {
		md5Original = c.original
	}
	return NewFileMetadata(FileMetadataInput{
		CreatedMillis:  s.created,
		ModifiedMillis: s.lastModified,
		UpdatedMillis:  s.lastUpdated,
		CreatedBy:      NewActor(s.createdBy),
		ModifiedBy:     NewActor(s.modifiedBy),
		SHA1Original:   sha1Original,
		MD5Original:    md5Original,
	}), nil
}

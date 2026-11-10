package repository

import (
	"unicode/utf16"
	"unicode/utf8"
)

// DecideFileOverwriteFromSource 把 Lookup 读取结果适配为 OverwriteFacts 并
// 复用 DecideFileOverwrite。nil source 表示可信调用方已确认目标缺失：适配器
// 不做查找也不分类错误，直接以 FilePresent=false 建模。非 nil source 必须有
// 正 ID 与文件 Path，否则返回 ErrInvalidFileSnapshotSource。仅投影 SHA1/SHA256
// Actual（各自独立校验空性与 UTF-8，即使请求省略某槽或 flag 会短路）；
// NULL 保持 nil、非 NULL 空串为非 nil 空、文本精确编码为 UTF-16；MD5/Metadata/
// MIME/Original 不参与。纯读取入参，不保留/不变异切片。
// DecideFileOverwriteFromSource adapts a Lookup read into OverwriteFacts and
// reuses DecideFileOverwrite. A nil source means the trusted caller confirmed
// absence: the adapter performs no lookup and no error classification, modeling
// FilePresent=false directly. A nonnil source must carry a positive ID and a
// file Path or ErrInvalidFileSnapshotSource results. Only SHA1/SHA256 Actuals
// are projected (nullness and UTF-8 validated per slot independently even when
// the request omits a slot or the flag would short-circuit); SQL NULL stays
// nil, present-empty becomes nonnil empty, text is encoded exactly to UTF-16;
// MD5, metadata, MIME and Original are ignored. Inputs are read-only: nothing
// is retained or mutated. The result is an overwrite predicate, with
// authorization and consistency left to the caller.
func DecideFileOverwriteFromSource(source *FileSnapshotSource, enforceOnIdenticalChecksums bool, requestSHA1, requestSHA256 []uint16) (bool, error) {
	if source == nil {
		return DecideFileOverwrite(OverwriteFacts{}), nil
	}
	if source.Reference.ID <= 0 || !source.Reference.Path.IsFile() {
		return false, ErrInvalidFileSnapshotSource
	}
	slots := [...]struct {
		value string
		valid bool
	}{
		{source.Reference.Content.SHA1, source.SHA1ActualValid},
		{source.Reference.Content.SHA256, source.SHA256ActualValid},
	}
	stored := [2][]uint16{}
	for i, slot := range slots {
		if !slot.valid {
			if slot.value != "" {
				return false, ErrInvalidFileSnapshotSource
			}
			continue
		}
		if !utf8.ValidString(slot.value) {
			return false, ErrInvalidFileSnapshotSource
		}
		stored[i] = utf16.Encode([]rune(slot.value))
	}
	return DecideFileOverwrite(OverwriteFacts{
		FilePresent:                 true,
		EnforceOnIdenticalChecksums: enforceOnIdenticalChecksums,
		RequestSHA1:                 requestSHA1,
		RequestSHA256:               requestSHA256,
		StoredSHA1:                  stored[0],
		StoredSHA256:                stored[1],
	}), nil
}

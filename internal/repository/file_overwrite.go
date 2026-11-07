package repository

// OverwriteFacts 是调用方持有的纯文件事实（BIN-208 冻结 API）。
// FilePresent 为可信调用方事实；摘要为原始 UTF-16 码元（nil=未提供，
// 非 nil 空=空串）；不做规范化/十六进制校验/大小写折叠/长度限制。
// OverwriteFacts carries caller-owned file presence, policy and nullable raw
// UTF-16 digests. Callers supply consistent facts and avoid concurrent mutation.
type OverwriteFacts struct {
	FilePresent                 bool
	EnforceOnIdenticalChecksums bool
	RequestSHA1, RequestSHA256  []uint16
	StoredSHA1, StoredSHA256    []uint16
}

// DecideFileOverwrite 建模既有 isFileOverwrite 决策：仅读取入参，
// 不保留/不变异切片。missing→false（压倒一切）；present 且双请求摘要
// 皆为 nil→true（不消费 flag）；否则 flag OR 任一已提供摘要与存储不符。
// 不涉及路径/仓库/授权/HTTP/SQL/事务语义。
// DecideFileOverwrite computes the bounded overwrite predicate without I/O,
// normalization, slice mutation or retention. Zero facts return false.
func DecideFileOverwrite(f OverwriteFacts) bool {
	if !f.FilePresent {
		return false
	}
	if f.RequestSHA1 == nil && f.RequestSHA256 == nil {
		return true
	}
	if f.EnforceOnIdenticalChecksums {
		return true
	}
	if f.RequestSHA1 != nil && !fileSnapshotEqualUnits(f.RequestSHA1, f.StoredSHA1) {
		return true
	}
	if f.RequestSHA256 != nil && !fileSnapshotEqualUnits(f.RequestSHA256, f.StoredSHA256) {
		return true
	}
	return false
}

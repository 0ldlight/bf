package repository

// FileSnapshotSource 是 Lookup 读取结果加三个 Actual 的 SQL 空性位：调用方
// 拥有的普通 DTO，不是组装完成的 FileInfoSnapshot；Valid 仅表示该列 SQL 非
// NULL（含空串），不证明校验和条目存在或内容完整。
// FileSnapshotSource is the Lookup read result plus the three Actual SQL
// nullability bits: a plain caller-owned DTO, not an assembled
// FileInfoSnapshot; Valid only marks a non-NULL column (empty string
// included) and proves neither checksum-entry presence nor content integrity.
type FileSnapshotSource struct {
	Reference         FileReference
	SHA1ActualValid   bool
	MD5ActualValid    bool
	SHA256ActualValid bool
}

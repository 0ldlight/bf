// FileReference 是事务内文件节点引用的最小投影：调用方分配的节点 ID、
// 已构造的 Path 身份、规范内容元数据与可选的逐节点文件元数据快照；无未用
// 接口或服务脚手架。Metadata 为 nil 表示本引用不携带元数据（SQL 侧见
// postgres 适配器的七列投影）；非 nil 指向显式快照，仍是调用方可变 DTO，
// 不承诺并发修改安全——Store 绑定前按值复制快照，调用方不得在操作进行中
// 同时改写其内容。
// FileReference is the minimal projection of a file node inside a caller
// transaction: a caller-allocated node ID, a constructed Path identity,
// canonical content metadata and an optional per-node file-metadata snapshot;
// no unused interfaces or service scaffolding. A nil Metadata means the
// reference carries no metadata (the postgres adapter owns the seven-column
// SQL projection); a nonnil pointer targets an explicit snapshot. This stays
// a caller-owned mutable DTO with no concurrent-mutation guarantee — the
// Store copies the snapshot by value before binding and callers must not
// mutate it concurrently with an operation.
package repository

import "github.com/0ldlight/bf/internal/content"

type FileReference struct {
	ID       int64
	Path     Path
	Content  content.Metadata
	Metadata *FileMetadata
}

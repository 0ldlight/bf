// FileReference 是事务内文件节点引用的最小投影：调用方分配的节点 ID、
// 已构造的 Path 身份与规范内容元数据；无未用接口或服务脚手架。
// FileReference is the minimal projection of a file node inside a caller
// transaction: a caller-allocated node ID, a constructed Path identity and
// canonical content metadata; no unused interfaces or service scaffolding.
package repository

import "github.com/0ldlight/bf/internal/content"

type FileReference struct {
	ID      int64
	Path    Path
	Content content.Metadata
}

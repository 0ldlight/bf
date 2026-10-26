# 加载态文件身份 / Loaded file identity

状态：已实现，等待 PR 审阅与合并；最终提交验证以 PR 为准。本接口只转换三个身份字段，不构造完整加载快照。
Status: implemented, pending PR review and merge; final commit checks follow the PR. This API converts three identity fields only.

```go
func LoadedFileIdentity(path Path) (
    repoKey, repoPath, name []uint16, err error,
)
```

输入为已构造的 file [Path](REPOSITORY-NODE-PATH.md)。成功返回三个调用方拥有的 UTF-16 副本，空 path/name 仍为非 nil 空切片；修改一个返回值不会影响其他返回值、输入或下一次调用。失败统一返回三个 nil 和既有 `ErrInvalidPath`，错误不含原始路径。
The input is a constructed file Path. Successful UTF-16 results are independently owned, including nonnil empty slices. Every failure returns three nil slices and the existing fixed error.

## 转换规则 / Conversion

1. 拒绝零 Path、folder 和任一字符串字段的畸形 UTF-8。这是本地 Go API 输入政策，不宣称与 Java 异常类型一致。不重新调用 NewPath，以免再次归一化已构造值。
2. repo 经既有 Java trim 后不得为空。拼接路径使用 PathName 的 Java-blank parent 分支，再做 Java trim 和边界 slash 剥离；按显式 file 构造处理，不从原始尾 slash 推导 folder，也不做 force-file 的第二次 trim。
3. cached name 从转换后的 raw path 走既有文件名 getter 与单次 metadata 剥离，独立于 Path.Name。getter 拒绝路径中的 NUL；repo 的 NUL 则遵循其自身 trim 规则，没有通用 NUL 禁令。

The explicit-file constructor differs from ordinary creation's two-argument constructor. The cached name is derived from the resulting raw path, rather than copied from the node name. Existing trim and name helpers retain their original behavior.

| 节点输入 / Node input | raw path | cached name |
|---|---|---|
| name=`/ a/` | ` a` | ` a` |
| name=`f:properties` | `f:properties` | `f` |
| name=`f:properties:statistics` | `f:properties:statistics` | `f:properties` |
| parent=`a\b`, name=`c\d` | `a\b/c\d` | `d` |
| 空 parent/name 的 file root / empty file root | 空串 / empty | 空串 / empty |

不做 path.Clean、反斜杠格式化、内部 slash 折叠、深度或存储路径写校验。root 的派生 isFolder 语义不改变显式 file 构造规则。
No path cleaning, separator rewriting, internal-slash collapse, depth check or storage-write validation is added. Derived root-folder semantics do not change the explicit file construction.

## 证据与边界 / Evidence and limits

固定目标的 22 组真实 Node→FileInfo 路径参考得到 18 次成功与 4 次分阶段拒绝；其中 raw parent=`/` 且空 name 在 NodePath 构造阶段已拒绝，Go 验收对应 NewPath 入口，不能算作此 helper 接到合法 Path 后的拒绝。其余参考与本地零值、UTF-8、所有权政策分别计数。参考没有执行 JDBC、MIME getter、复制或完整保存流程。
Twenty-two bounded real-class cases include eighteen successes and four stage-specific rejections. Reference observations and local API policies are distinct; they do not establish complete loader or save compatibility.

本接口不消费 [FileSnapshotSource](FILE-SNAPSHOT-SOURCE.md)，不接[快照更新门](FILE-SNAPSHOT-UPDATE.md)。完整组装仍须确定 Original/Locale、时间回退、元数据空性与 MIME 生命周期；三字段转换不代表这些规则已完成。
This API neither consumes the read source nor invokes the update gate. Complete assembly still requires separate Original/Locale, time, nullable-metadata and MIME policies.

# 本地内容存储 / Local content storage
状态 / State: BIN-129 的内部 Put/Open 切片已通过本轮验证，草稿 PR 待审。产品 HTTP、数据库引用、GC 与 HA 另行接入。
The internal Put/Open slice passed this round of validation and awaits draft-PR review. Product HTTP, database references, GC and HA remain separate.

## 入口与责任 / Interface and ownership
`internal/filestore.New(root)` 打开服务身份独占管理的目录，返回 Store；调用方停止使用后 Close。Put 接收 context、调用方拥有的 io.Reader 与 Expected，返回[内容元数据](CONTENT-METADATA.md)。Open 仅按规范 SHA-1 返回调用方负责关闭的读取器。
New opens a service-owned directory. Quiesce operations before Close. Put consumes a caller-owned reader and returns content metadata; Open returns a caller-owned reader by canonical SHA-1.

Expected 的 Size 为可选声明长度；SHA1、SHA256、MD5 为调用方按仓库策略选定的期望值。有效十六进制转小写，非空白值精确比较；不修剪非空白值的前后空格。空白语义按目标 Java Character.isWhitespace，NBSP 和 NEL 不作为空白。HTTP 错误映射属于后续合同。
Expected contains optional declared length and caller-selected digests. Valid hex normalizes to lowercase; nonblank values compare exactly without trimming. Whitespace follows the target rule rather than treating NBSP/NEL as blank. HTTP mapping remains separate.

## 写入与复用 / Publication and reuse
- SHA-1 为身份，路径为 `<前2位>/<完整40位小写SHA-1>`，同时保留 SHA-256、MD5 与实际长度。
  SHA-1 addresses content while separate checksums and length remain available.
- 本次临时文件在 root 下的私有 staging 写入；验证与关闭成功后用同文件系统重命名发布。发布前不能作为完整对象读取。
  Stage privately, validate and close, then publish by same-filesystem rename.
- 普通已有对象复用，顺序重复写保持原对象身份；并发首次发布允许相同内容替换 inode。复用路径不重读校验已有内容。
  Reuse existing regular objects; first concurrent publishers may replace an identical-content inode. Reuse does not rehash existing bytes.
- 返回失败时清理本次临时文件；清理失败也应返回，保留原始错误原因。其他任务的临时文件与已发布对象不作为回滚目标。
  Clean this attempt's staging on returned failure and surface cleanup errors; never roll back another attempt's staging or a published object.

## 文件系统边界 / Filesystem boundary
新建目录与文件分别请求 0700/0600；现有调用方目录不被自动 chmod。root 是受信配置，服务身份独占管理；拒绝非规范键及预存的符号链接/特殊文件路径。正常对象流不代表原版异常文件行为已兼容。
New directories/files request 0700/0600; existing caller directories are not chmodded. The configured root is trusted and service-owned. Canonical keys and regular-object checks constrain access; abnormal-file handling is an explicit safety policy.

现有 root/fanout 对组或其他身份可写、以及 staging 对组或其他身份有访问权限时，作为不安全配置拒绝；这是本单机切片的安全约束，原版对异常权限的行为仍待核实。
Group/other-writable root/fanout directories and nonprivate staging are refused as a local safety constraint; target abnormal-permission parity is unverified.

目录范围限制采用 [Go os.Root](https://pkg.go.dev/os@go1.27.1#Root)，配合不跟随叶子链接、非阻塞打开及文件类型检查。os.Root 自身会跟随界内相对链接，也不禁止 bind mount 或设备文件；不能将它描述为完整文件系统沙箱。本轮不支持对抗有权改写 root 的其他进程或恶意挂载。
Root confinement is supplemented by no-follow/nonblocking opens and type checks. Root alone follows in-tree relative symlinks and does not prohibit bind mounts/devices. Hostile writers to the service-owned root and malicious mounts are outside this slice's guarantee.

取消在读取前、读取间和发布前检查；任意阻塞读取器不能被强行中断。Linux 为产品目标，Darwin 用于本机验证；非 Unix 平台拒绝弱化打开策略。
Check cancellation before/between reads and before publication; arbitrary blocking readers cannot be forcibly interrupted. Linux is the product target, Darwin the local validation platform; unsupported platforms refuse weaker opens.

## 验证边界 / Evidence boundary
原版语义、包摘要与实例观察见[存储研究](FILESTORE-CONTRACT.md)，所有者批准范围见[切片方案](FILESTORE-SLICE-PROPOSAL.md)。本切片不声明 fsync、断电持久性、同 SHA-1 不同内容、已有损坏、孤儿回收、数据库事务或多节点兼容完成。
The linked evidence and approved proposal define scope. Power loss, collisions, existing corruption, orphan GC, database transactions and multi-node compatibility remain unverified.

本轮验证 / This round: scripts/check.sh 与 race 通过；独立进程矩阵 38 项通过，包含 8 个进程首次写入、进程重开、发布前不可见、摘要/长度拒绝、失败清理及异常路径。原版实例行为与本地实现测试分别保留证据；不把本地通过等同于完整目标兼容。
Shared checks/race and 38 independent process cases passed, including eight-process first writes, reopening, visibility, validation, cleanup and abnormal paths. Target observations and implementation tests remain distinct evidence.

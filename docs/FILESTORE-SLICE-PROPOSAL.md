# BIN-129 首条本地持久化切片 / First local persistence slice
状态 / State: proposed for owner review, 2026-10-20. This is a concrete slice proposal, not a completed provider or full compatibility claim.

## 决策 / Decision
采用已核实的 Artifactory 本地文件 provider 语义，先实现单机开发与受控兼容验证所需的文件内容存储。沿用已批准的 Go 领域接口方向；最终对象存储产品、HA 与多数据库验证仍保留现有任务。
Use the corroborated local-file semantics for a single-host development and compatibility slice under the approved Go domain-store direction. Final object-storage, HA and database coverage remain tracked.

1. 内容模型：SHA-1 为内容身份；返回 SHA-1、SHA-256、MD5、实际长度。摘要流式计算。
   Identity: SHA-1, plus SHA-256, MD5 and actual length, computed while streaming.
2. 物理布局：root 下的 <SHA1前2位>/<完整40位小写SHA1>。逻辑仓库路径不作为物理路径输入。
   Layout: <first2>/<full lowercase SHA1>; repository paths never select physical files.
3. 写入：在该 root 同一文件系统下的私有 staging 写完，确认读写/关闭和输入校验成功后再发布。已存在的普通对象复用；并发首次写入的原子发布使用重命名。Linux 上并发重命名可能替换同内容 inode，因此不承诺所有并发写保持同一 inode。
   Write to private staging on the same filesystem, finish and validate before publishing. Reuse existing regular objects; use atomic rename for first publication. Concurrent renames may replace an identical-content inode on Linux, so inode identity across first writers is not a guarantee.
4. 正常流的期望摘要：调用方已选定仓库策略后再传入。入口先按已核实的 ChecksumInfo 规则将有效摘要转为小写，再由底层流对非空白期望值逐字比较；失败返回可分类内部错误。HTTP 状态和错误正文随后单独对照。
   The caller selects repository policy before passing expected digests. Normalize valid digest inputs to lowercase using the corroborated ChecksumInfo rule, then compare nonblank values exactly in the stream, returning classified internal errors; HTTP mapping is a separate integration.
5. 成功与失败：返回成功意味着完整对象可读取；失败清理本次 staging。已存在对象的复用沿用证据中的行为；损坏检测、摘要碰撞、断电恢复和 GC 的完整行为继续核实，不能在此宣布兼容完成。
   Success makes the complete object readable; failures clean this attempt's staging. Existing-object reuse follows the evidence. Corruption, collisions, power loss and GC remain explicit investigations.
6. 安全边界：root 由服务身份独占管理；键只接受规范摘要，拒绝目录穿越和非普通对象。异常的符号链接/特殊文件作为本地安全约束记录，不据此声称原版异常行为一致。
   The service owns its root, accepts canonical digest keys and rejects traversal/nonregular objects. Symlink/special-file handling is an explicit local safety constraint, not an attested target equivalence.
7. 第一切片提供 Put/Open，删除与引用计数待数据库/GC合同明确后接入。原版证据显示内容 provider 先完成、随后新事务登记二进制记录；因此本切片测试到 provider，后续再验证元数据失败留下的对象如何处理。
   Implement Put/Open first; integrate deletion/reference tracking under the database/GC contract. Provider-before-metadata ordering is observed, while cross-store failure recovery remains unverified.

## 验收 / Acceptance
- 固定向量、空内容、分块与较大流的三摘要/长度。
- 规范路径、同内容顺序和并发 Put、重新打开 provider 后读取相同字节。
- 错误摘要、源读取错误、写入/关闭/发布失败及取消：本次 staging 清理，完整对象可见性可验证。
- 非法键、符号链接及特殊文件拒绝。
- 同仓库共享检查和 race；独立进程重开、并发读取与受控失败验证。
- 所有待验证与可见差异留在工程文档；Fern 使用文档仅在可用后编写。

源码证据 / Source evidence: docs/FILESTORE-CONTRACT.md, target FilePersistenceHelper.saveStreamFileAndMove/persistFile; FileBinaryProviderReadOnlyBase; PostProcessChecksumBinaryStream.validate; BinaryServiceImpl.addBinary. This proposal contains original semantic notes only.

审批依据 / Approval authority: docs/PROJECT-BRIEF.md 的“架构选型、main 分支 PR 合并、版本发布由所有者决定”。本次请求只批准该持久化切片的实现方向；合并与发布继续单独审批。

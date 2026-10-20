# 本地内容存储对齐 / Local filestore alignment

状态：BIN-129 调查记录，尚未实现。所有者于 2026-10-20 要求严格对齐 Artifactory 并参考源码，原 SHA-256 物理寻址建议已撤回。
Status: investigation for [BIN-129](https://linear.app/binfloow/issue/BIN-129); no storage implementation yet. The owner requires Artifactory alignment; the earlier SHA-256 physical-key proposal is withdrawn.

## 基线与证据 / Baseline and evidence

产品基线为 Artifactory 7.161.26，实例 revision 86126900。恢复树的版本标签有差异，仅用于寻找入口；以下静态结论直接复核了目标发布包的类字节码，没有执行目标类。
The product baseline is 7.161.26, revision 86126900. Historical recovered source locates entry points; target-package bytecode corroborates the findings below without executing target classes.

| 制品 / Artifact | SHA-256 |
| --- | --- |
| Product archive | `b967a1fc263ec8cf0314194fc24daf99e10a130ca8e19b076c8a18faf02bc4d9` |
| `artifactory.war` | `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37` |
| `binary-store-core-4.372.5.jar` | `f22cc9ea6bad295f7317eeab56f666a5a5a5861366e560018d281b05a5ddd198` |

JAR 的组件版本不等于产品版本。历史副本的 binary-store-core JAR 与本目标副本摘要一致；不能据此将整个恢复树视为同一版本。
Component versions differ from the product version. Only this historical core JAR was verified byte-identical; this does not attest the whole recovered tree.

REA 对 WAR 的只读清单证据为 `ev_a0a85dfa700ebde3340fdb25eb8d30fd0b68425c0dcb0bbd0805e1e1f353e130`；它证明包身份与条目清单，不能证明运行行为。方法检查使用 `javap 26.0.2.1 -p -c`，路径拼接额外检查 `-v` 的常量。受控字节码、地址、凭据及原始实例记录留在私有研究区。
REA inventory establishes archive identity/entries, not runtime semantics. Focused javap inspection supplies method evidence; controlled files and raw observations remain private.

## 已核实的边界 / Corroborated boundaries

| 结论 / Finding | 定位 / Evidence location | 限制 / Limit |
| --- | --- | --- |
| 本地对象以 SHA-1 查找；路径为 `<前2位>/<完整SHA-1>` / SHA-1 lookup with full digest filename | `FileBinaryProviderReadOnlyBase.getFile/getStream/exists`; `FilePersistenceHelper.getRelativePath(String,int)` and concat bootstrap | 仅此本地 provider；不外推到云后端 / Local provider only |
| 二进制模型保留 SHA-1、SHA-256、MD5、长度 / Multiple checksums plus length | `FilePersistenceHelper.saveStreamToTempFile`; `BinaryElement` | 不能仅凭模型断言全部上传校验规则 / Not a complete upload validation contract |
| 临时文件写完并检查长度，再尝试原子移动 / Staging and length check precede atomic-move attempt | `FileBinaryProviderImpl.addStream`; `FilePersistenceHelper.saveStreamFileAndMove/persistFile` | 此辅助函数未证明 fsync、跨文件系统降级或断电保证 / No power-loss durability claim |
| 已存在对象走复用/临时文件清理分支 / Existing-object and temporary-file cleanup branches | `FilePersistenceHelper.persistFile/saveStreamFileAndMove` | 存在性检查不等于重读校验；原子移动不能自动证明所有并发平台语义 / No universal collision or integrity guarantee |
| 先调用内容 provider，再以新事务登记二进制记录 / Provider call precedes a new binary-record transaction | `BinaryServiceImpl.addBinary(BinaryStream)/transactionalInsertRecordInDb` | 未证明所有仓库节点和物理文件在同一事务 / No global atomicity claim |
| 记录按 SHA-1 查找和复用，另存 SHA-256；PostgreSQL 插入处理冲突 / SHA-1 record lookup with separate SHA-256 and PostgreSQL conflict handling | `BinariesDbServiceImpl.insertRecordInDb`; `BinariesDao.create` | 仍需验证竞争时的完整结果和错误类别 / Concurrent outcomes remain open |
| 删除二进制记录的 SQL 检查节点无引用 / Binary-row deletion checks node references | `BinariesDao.deleteEntry` | 不代表完整 GC 生命周期或即时物理删除 / Not a complete GC contract |

实例交叉验证使用本项目自创的 65 字节样本：物理路径与内容摘要匹配；将相同内容顺序上传到另一逻辑路径后，原物理 inode、大小和 mtime 未变，两个路径的元数据摘要相同。临时重复路径已删除并验证不存在。
A controlled sequential duplicate upload corroborated reuse of the authored 65-byte object: physical identity remained unchanged and both logical paths returned matching metadata checksums. The duplicate path was removed and absence verified.

## 实现约束与待核实项 / Implementation contract and open checks

下一条 Go 切片沿用已批准的领域存储接口和数据库方向，先建 SHA-1 内容身份及多摘要/长度模型，再验证本地持久化；仓库节点、权限与 Generic HTTP 闭环继续分别接入。这份记录不新增对象存储产品或 HA 架构决定。
The next Go slice follows the approved domain-store/database direction with SHA-1 content identity and multiple checksums. Repository nodes, authorization and Generic HTTP remain separate integrations; no cloud-storage or HA decision is made here.

- 摘要在流中的计算时机、声明值不符和错误映射 / Digest computation, declared mismatch and error mapping.
- 同内容并发写、同 SHA-1 不同字节、已有文件损坏 / Concurrent duplicates, digest collision and existing corruption.
- 中断写入、数据库失败后的引用、临时内容和 GC / Interrupted writes, database failure, references, staging and GC.
- 空文件、大文件、磁盘满、重启与多节点 / Empty/large files, disk exhaustion, restart and multi-node behavior.

刷盘、损坏拒绝、路径防护等实现策略必须说明原版证据和可见差异；没有观察到的能力保持待验证，不能凭设计声明兼容。
Durability, corruption rejection and path hardening require explicit evidence and visible-difference accounting; unobserved behavior stays unverified.

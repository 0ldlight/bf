# 文件覆盖判断 / File overwrite decision

BIN-208 在 `internal/repository` 提供 `DecideFileOverwrite(OverwriteFacts) bool`，读取调用方已提供的事实，计算选定目标 `isFileOverwrite` 的有限纯判断。外层仓库/路径例外、节点读取、权限求值及并发一致性由后续接入负责。
BIN-208 adds a pure decision over caller-supplied facts in the existing repository package. Outer repository/path exceptions, lookup, authorization and consistency remain separate responsibilities.

## 输入与结果 / Inputs and result

`FilePresent` 表示同一文件事实域中是否存在目标文件；`EnforceOnIdenticalChecksums` 是显式传入的策略事实。请求和已存 SHA-1/SHA-256 均为可空原始 UTF-16 码元切片。调用方负责事实来源与一致性，并避免在调用期间并发修改切片。
Inputs carry file presence, a supplied policy value, and independently nullable raw UTF-16 request/stored digests. Callers own provenance, consistency and concurrent-mutation discipline.

| 事实 / Facts | 结果 / Result |
|---|---|
| 文件缺席 / Missing file | false，与其他字段无关 / false regardless of other facts |
| 文件存在、两个请求摘要均 nil / Present, both request digests nil | true，与策略位无关 / true regardless of the policy bit |
| 文件存在、至少一个请求摘要非 nil / Present, at least one request digest supplied | 策略位为 true，或任一已提供摘要与对应已存值不同 / true if policy is enabled or any supplied digest differs |

比较逐 UTF-16 码元进行：nil 请求摘要不构成差异，非 nil 空串与 nil 已存值不同；大小写、空格、NUL、孤立代理码元均原样保留。这里不进行摘要格式校验或规范化；HTTP 有限画像的校验仍在既有适配器。
Comparison preserves exact UTF-16 units, null/empty distinctions, case, whitespace, NUL and unpaired surrogates. HTTP-profile validation remains with the existing adapter.

函数不修改或保留输入切片，无 I/O、配置读取或事务行为；零值输入返回 false。Go 的 false 零值不表示运行配置的默认值。选定制品的初始化默认 false 已单独核实，部署中的有效值仍需由调用方另行确认。
The function neither mutates nor retains slices and performs no I/O or configuration access. Zero facts yield false; the Go zero value does not establish a deployment default or effective setting.

## 读取结果接入 / Lookup result projection

BIN-209 的 `DecideFileOverwriteFromSource` 接收可信调用方提供的 `FileSnapshotSource`。只有确认普通文件缺席时才传 nil。非 nil 来源须有正节点 ID 和文件路径。转换独立使用 SHA-1 与 SHA-256 Actual 的空性位，保留 SQL NULL、空串及 UTF-16 码元；矛盾空性或畸形 UTF-8 返回 `ErrInvalidFileSnapshotSource`。兼容七列元数据全为 NULL 的旧行。

BIN-209 projects trusted lookup results into the predicate. Nil means confirmed absence. A non-nil source requires a positive node ID and file path. The SHA-1 and SHA-256 Actual nullability bits are handled independently. Invalid UTF-8 and inconsistent nullability return `ErrInvalidFileSnapshotSource`. Legacy rows without metadata remain supported.

调用方负责目标身份、缺席与读取错误的区分，以及读取与提交之间的一致性。覆盖判断的布尔值仅用于判定覆盖保护条件。

Callers own identity, the distinction between absence and lookup failure, and consistency between read and commit. The boolean reports only whether the overwrite predicate matched.

BIN-210 的 `localrepo.LookupFileOverwrite` 对已构造的文件路径执行一次节点读取。只有未取消的缺行结果可视为缺席；非文件及其他查询错误保留原因，返回路径必须与输入完全相等。调用方须先处理错误，再使用布尔值。结果取自本次读取；提交时的授权与一致性仍由后续接入负责。

BIN-210 adds one lookup by an already constructed file path. A missing row counts as absence only while the context is live. Non-file and other lookup errors retain their causes, and the returned path must exactly match the request. Callers must handle the error before using the boolean. The result describes the lookup and leaves commit-time authorization and consistency to the caller.

## 依据与验收边界 / Evidence and verification boundary

判断结构来自固定 WAR SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37` 内 `DbStoringRepoMixin` 类 SHA-256 `a72123d3917e2248dd53a270c41afaa3cb8d322f4e211f5ec925c364c147f3ea` 的字节码；既有包内 `fileSnapshotEqualUnits` 提供相同的 nil/empty 与原始码元比较域。
The decision structure is derived from the identified class bytes in the fixed WAR; the existing package-owned equality helper preserves the required comparison domain.

单元验收针对缺席/存在、双 nil、独立摘要、策略顺序、空串/大小写/空白/原始码元及输入不变性；故障变异用于验证回归能捕获错误判断。期望来自经审查的静态契约，本轮未获得成功的目标类重放结果，不将本地测试称为原版运行差分。
Unit checks and compiled mutations validate the reviewed static contract and regression strength. No successful target-class replay was obtained in this slice; local tests are not reference-runtime differential evidence.

2026-11-05 所有者决定暂不使用原版 Artifactory 环境；本片仅使用保留资料和本地 Go 验证。在线 deploy/overwrite 矩阵仍按[写授权研究](GENERIC-UPLOAD-WRITE-AUTH.md)保留未完成状态，生产写授权与 UAT 接入继续单独验收。
The owner deferred the original environment. This slice uses retained evidence and local Go checks; the online permission matrix and production integration remain incomplete.

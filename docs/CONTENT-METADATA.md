# 内容身份计算 / Content metadata calculation
状态 / State: BIN-129 的首条实现切片，尚未接入持久化或产品 HTTP。
First implementation slice for BIN-129; persistence and product HTTP integration remain separate.

内部包 `internal/content` 提供 `Metadata{SHA1, SHA256, MD5, Size}` 和 `Calculate(context.Context, io.Reader)`。SHA-1 用作目标内容身份，另保留 SHA-256、MD5 与实际字节数；三个摘要均输出小写十六进制。原版模型证据见[存储记录](FILESTORE-CONTRACT.md)。
The internal package returns SHA-1 identity, SHA-256, MD5 and actual byte length with lowercase hexadecimal digests; see the linked evidence for the target model.

Calculate 使用固定 32 KiB 读取缓冲，一次消费调用方提供的流，关闭责任留给调用方。仅读取器直接返回 io.EOF 时成功；同时返回的数据仍计入摘要。其他错误（包括包装或合并 EOF）返回空 Metadata，并保留 errors.Is 可识别的原因，避免把不完整输入作为内容身份。
Calculate streams through a fixed read buffer without closing its caller-owned reader. Only literal io.EOF ends successfully; accompanying data counts. Other errors, including wrapped/joined EOF, return zero metadata while preserving their cause.

调用方提供非空 context。取消在读取前和读取间检查；任意阻塞读取器不能被强制打断，持续返回 (0,nil) 的读取器也需要调用方取消。该模块计算实际长度，期望长度/摘要、仓库校验策略、文件路径和数据库事务由后续层明确接入。
Callers provide a nonnil context. Cancellation is checked before/between reads; arbitrary blocking readers cannot be interrupted and perpetual no-progress readers require cancellation. Expected length/checksum policy, paths and metadata transactions belong to subsequent integrations.

验证覆盖固定向量、空内容、分块/较大输入、数据与 EOF 同次返回、读取错误、取消和流所有权；独立进程另用不同长度数据并发核对三个摘要，并复现后修复 EOF 错误分类。
Tests cover fixed vectors, streaming, termination, failure, cancellation and ownership. Independent concurrent vector checks and an EOF regression verify the implementation separately.

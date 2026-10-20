# 二进制写入顺序 / Binary write ordering
状态 / State: BIN-129 领域服务切片已实现并验证，PostgreSQL 元数据登记适配器已接入并以真实 17.10 验证；产品 HTTP 未接入。
Domain-service slice implemented and verified, with the PostgreSQL metadata-registration adapter wired in and verified against a real 17.10 instance; product HTTP remains future work.

## 顺序与责任 / Ordering and ownership
`internal/binary` 定义窄接口 `Provider.Put(ctx, io.Reader, filestore.Expected)` 与 `Registry.Register(ctx, content.Metadata)`，`Service.Add` 依次编排：
The internal package defines the narrow `Provider.Put` and `Registry.Register` interfaces; `Service.Add` orchestrates:

1. 先检查 context：已取消则直接返回零元数据与取消原因，不触碰任何依赖。
   A pre-canceled context returns zero metadata and the cancellation cause without touching either dependency.
2. 恰好一次调用 provider.Put，转发调用方的读取器与期望值；服务不读取、不关闭读取器，读取器与存储生命周期归调用方。
   provider.Put runs exactly once with the caller's reader and expectations forwarded; the service never reads or closes it. Reader and store lifetimes stay with the caller.
3. provider 失败返回零元数据并以 errors.Is 保留原因，不调用登记。
   Provider failure returns zero metadata, preserves the cause and skips registration.
4. provider 成功后、登记前复查 context；取消返回零元数据并保留已写内容。
   The context is rechecked between stages; cancellation leaves the published content in place.
5. 恰好一次以计算出的元数据调用 Registry.Register，返回其规范结果——登记层独立拥有提交策略，已有记录可与流式元数据不同。
   Registration runs exactly once with computed metadata and its canonical result is returned; the registry owns the commit policy and an existing row may differ from fresh streamed metadata.
6. 登记失败（即使返回部分元数据）同样返回零元数据并保留原因；不删除/补偿内容、不自动重试。调用方可换新读取器重试并复用既有对象。
   Registry failure also yields zero metadata with the preserved cause; no deletion, compensation or automatic retry. A caller may retry with a fresh reader and reuse the existing object.

`New` 以有界配置错误拒绝空依赖；调用方保证有效实现并拥有其生命周期，无反射式类型化 nil 检测。服务无内部可变调用状态，依赖支持时允许并发 Add。错误文本只含常量阶段归因，不含读取器内容、摘要或凭据。
`New` rejects nil dependencies with a bounded configuration error; callers supply valid implementations and own their lifetime (no reflection-based typed-nil checks). The service holds no mutable call state, so concurrent Adds follow dependency guarantees. Error text carries only constant stage attribution.

失败包装是内部 `stageError`：`Error()` 只返回固定阶段常量，底层错误文本（可能含请求体、摘要或凭据）不会自动进入 `Error()`；`Unwrap()` 原样暴露未修改的原始原因，供可信调用方用 `errors.Is`/`errors.As` 诊断。HTTP 响应与日志不得盲目序列化解包后的原因文本，需在上层按信任边界过滤。
The failure wrapper is an internal `stageError`: `Error()` returns only the fixed stage constant so underlying text (which may carry bodies, digests or credentials) never propagates automatically, while `Unwrap()` exposes the untouched original cause for trusted callers using `errors.Is`/`errors.As`. HTTP responses and logging must not blindly serialize the unwrapped cause; filter it per trust boundary in upper layers.

## 边界 / Boundaries
- 顺序依据是目标 `addBinary(BinaryStream)`/`transactionalInsertRecordInDb` 的静态字节码证据（provider 在前、`invokeInNewTransaction` 登记在后、正常失败不删内容）；目标的重复插入重试是独立逻辑，本片未实现猜测版本。目标数据库失败路径未实测。
  Ordering follows static bytecode evidence of the target methods (provider first, registration via a new transaction second, normal failure keeps content). The target's duplicate-insert retry is separate logic and is not guessed here; target database-failure paths are untested at runtime.
- 登记失败留下的已发布对象是可读的引用候选；孤儿回收/引用计数/GC 不在写入顺序本身。数据库登记适配器已由 [PostgreSQL 登记切片](POSTGRES-REGISTRY.md)提供（真实 17.10 验证）；网络/HTTP、鉴权与多节点声明仍未接入。
  Content left by a registry failure is a readable reference candidate; orphan collection and GC sit outside the ordering itself. The database registration adapter now exists as the [PostgreSQL registry slice](POSTGRES-REGISTRY.md) (verified on a real 17.10 instance); network/HTTP, authorization and multi-node claims remain unwired.
- 真实 [本地 provider](LOCAL-FILESTORE.md) 集成测试证明登记失败后字节仍可 Open、换新读取器重试成功且顺序复用保持原 inode。
  Real local-provider integration tests prove bytes stay Open-able after a registry failure and that a fresh-reader retry succeeds, reusing the original inode for sequential rewrites.

## 关联 / Links
- 内容模型与摘要：[CONTENT-METADATA.md](CONTENT-METADATA.md) / Content identity
- PostgreSQL 登记适配器：[POSTGRES-REGISTRY.md](POSTGRES-REGISTRY.md) / PostgreSQL registry adapter
- 本地 provider 合同：[LOCAL-FILESTORE.md](LOCAL-FILESTORE.md) / Local provider contract
- 目标证据与边界：[FILESTORE-CONTRACT.md](FILESTORE-CONTRACT.md) / Target evidence

# 加载 Original 的显式大小写规则 / Explicit loaded-Original casing

关联 / Tracking: BIN-193、父 BIN-191、STO-001。

## 接口与事实 / Interface and facts

`LoadedFileSnapshotWithOriginalCase(source, originalCase)` 复用既有即时组装器。`LoadedFileSnapshot(source)` 等价于传入 `LoadedOriginalCaseInvariant`，保留原支持域。三项操作输入 `LoadedFileUpdateInput`、`LoadedFileBinaryInput`、`LocalFileNoClientInput` 新增 `OriginalCase LoadedOriginalCase`；`LocalFileWriteInput` 仍是最后一项的 alias。默认零字段不会扩大旧调用的接受域；结构体字段数量确实改变，不宣称反射形状或非键名构造兼容不变。
The explicit loader shares one assembler with the existing entry point. Three operation inputs gain OriginalCase, preserving the existing defined type and write-input alias. Zero preserves existing keyed callers' behavior; the added field does change struct shape and positional literals.

参数是调用方捕获的有限算法事实，不读取主机 Locale、不推断部署默认，也不放进仅含 SQL 事实的 `FileSnapshotSource`。调用方负责它与 MIME 目录规则等有效配置事实自洽；本片不实现配置生产者、任意 normalizer 回调或 JVM sidecar。
Callers select a bounded algorithm from captured configuration facts and keep it coherent with MIME configuration. SQL sources carry no configuration policy. No host-locale inference, configuration producer, opaque normalizer or runtime sidecar is supplied.

## 支持域 / Supported domain

SHA1/MD5 的长度先按原始 UTF-16 码元数与 40/32 比较。nil、非 nil 空串、非目标长度均原样保留（包括非 ASCII、大小写和孤立代理）；未知规则仍拒绝，不因无字符串需转换而跳过参数校验。
Select the length branch before conversion. Null, nonnull empty and non-target lengths stay raw, including non-ASCII and isolated surrogates. Unknown rules are rejected even when neither Original needs conversion.

| LoadedOriginalCase | 恰长输入域 / Exact-length units | 大写 I / Capital I | U+0131 |
|---|---|---|---|
| Invariant = 0 | ASCII，不含大写 I / ASCII excluding capital I | unsupported | unsupported |
| NonTurkic = 1 | ASCII 加 U+0131 / ASCII plus dotless i | i | 原样 / unchanged |
| Turkic = 2 | ASCII 加 U+0131 / ASCII plus dotless i | U+0131 | 原样 / unchanged |

其余 ASCII 大写转对应小写，其余接受码元逐字保留。显式画像的 U+0131 是输出闭包：Turkic I 加载后变成它，后续保存与再次读取不能拒绝自己产生的值；两显式规则均保留已有 U+0131，不尝试逆造历史 I。恰长的其他非 ASCII（含 U+0130、组合点、Sigma、代理）仍为 `ErrUnsupportedLoadedOriginal`。这不是原版拒绝，而是未完成支持域。NUL 纯快照接受不代表 PostgreSQL text 可存。
Lowercase other ASCII capitals and preserve other accepted units. Dotless i closes the explicit modes' output domain after persistence and reloading; never reconstruct historical capitals. Other exact-length non-ASCII remains explicitly unsupported rather than target-invalid. Pure-value NUL support establishes no SQL text acceptance.

## 顺序与消费者 / Order and consumers

操作前缀保持：取消→nil delegate→正 ID→created policy。加载器保持：身份→Metadata→三个 Actual 的 SQL 空性/UTF-8→规则枚举→SHA1 Original→MD5 Original。未知值返回固定 `ErrUnsupportedLoadedOriginalCase`（`repository: unsupported loaded original case`），不回显输入；即使两个 Original 都是 nil 仍拒绝。随后才物化 MIME、校验源路径、比较与写入；失败零结果、无 delegate 调用。
Operations retain cancellation/callback/ID/created-policy precedence. Loading retains identity/metadata/Actual guards, then validates the enum before both Originals, followed by MIME, path and the existing gate. Unknown values use a fixed error without input data, including when both Originals are null. Failures return zero and never call the update delegate.

四个实际入口均透传规则：通用快照更新、直接 binary 更新、有客户端摘要及无客户端摘要的本地写入。规则只作用于加载比较基线：通用 Desired 保持 raw；请求 Original 的 true-validation 仍只小写合法 hex，SERVER 的 I/非 hex 原样保存、CLIENT mismatch 继续按新 Actual 拒绝；无客户端仍清空 Original。直接 binary 消费者保留加载 Original，因为合法 hex 已小写，i/U+0131 等非法 hex 在目标 true 构造中不变。
All four operation entry points forward the selection through one preparation path. It affects the loaded baseline only: raw Desired, validating-request normalization, local verification and no-client clearing retain their existing contracts. Direct binary assignment preserves loaded Originals under the target validating constructor.

实际回读链保留原始 SQL 与加载状态差异：SERVER 写 raw I→真实 Lookup→选定规则加载成 i/U+0131；同 binary 可跳过且不主动规范化 SQL 行；内容变化时保存加载 Original；再次 Lookup 与显式加载成功。事务仍由调用方提交/回滚。Source 新鲜度、row 关联、binary 来源仍由调用方负责，无 CAS、锁或隐式重读保证。
SERVER stores raw I; lookup preserves it and the explicit loader normalizes it. Same-binary updates may skip without rewriting SQL, while changed-content writes project the loaded Original and support a second real read. Callers retain transaction, freshness, row association and binary provenance responsibilities.

## 证据与未完成项 / Evidence and remaining work

参考实际 ChecksumInfo 的 false→true→false 三阶段：3,224 行、9,672 次构造、32,190 项独立断言；3,168 行符合本片画像，56 行是目标成功而本地仍 unsupported 的上下文/非 ASCII 输入。52 行恰长输出含 U+0131，后两阶段保持。两个实际类与 26 个 JAR 均核对同一固定 WAR。本机受控 JVM **26.0.2.1** 的运行不能改称包内 **25.0.4.1** 或部署行为；包内源码的 Unicode **16.0**、上下文与 Locale 词边界来源另存私有证据。
The actual-component matrix independently verifies three constructor stages and read-back closure. It does not enumerate every string combination. Controlled host-JVM observations, bundled-JDK static identity and deployment settings remain separate evidence scopes.

独立单元保护两显式规则、全 ASCII+U+0131、邻界、所有权、未知规则、错误顺序及四入口透传。真实 PostgreSQL 通过实际 binary/Lookup/Store 和调用方事务检查写入、二次回读、跳过、提交、回滚和错误无修改；行为 RED/GREEN、race、故障回归、共享检查与最终完整 CI 绑定对应 PR 提交。
Independent unit and real-PostgreSQL seams cover the selected domain, wiring and transactions. RED/GREEN, race, mutations, shared checks and final CI bind the delivery revision; reference research is not a substitute for product validation.

父 BIN-191 继续完整 Unicode/Locale/JDK 算法：Sigma 等依赖上下文与 Locale word-break，不能用 Go 简单小写或四个大小写类别代替。部署有效 Locale、配置生产者、HTTP/header/marker 来源、鉴权/管理员覆盖和完整保存生命周期仍未完成。本片不发布或部署。
Full Unicode/Locale, effective configuration, HTTP/provenance/auth/admin behavior and complete save lifecycle remain open. No release or deployment is included.

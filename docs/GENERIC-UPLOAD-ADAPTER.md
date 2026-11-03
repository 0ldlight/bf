# 普通 PUT 文件适配器 / Ordinary PUT file adapter

状态：BIN-206 提供独立、未挂接生产入口的有限普通文件 PUT 实现；验收范围见末节，不将父能力或生产写授权登记为兼容完成。原版观察见[上传接入契约](GENERIC-UPLOAD-INTEGRATION.md)，不在这里重复采样。
State: BIN-206 provides a bounded, unmounted ordinary-file PUT implementation. Verification scope is listed below; parent capability and production write authorization remain incomplete. The linked integration contract owns reference observations.

## 结构与边界 / Structure and boundary

`internal/localrepo` 拥有 `UploadInput`、有限文件身份和输入/Source 纯校验；它不导入 SQL 或 HTTP。`internal/localrepo/postgres` 负责真实保存工作单元；`internal/httpserver` 的独立 PUT 处理器消费必需的写授权和已提交保存端口。旧 `SaveFile` 与节点保存结果不扩展，不新增通用框架或序列化插件。
The parent package owns input/path/source profiles without SQL or HTTP. The PostgreSQL adapter owns actual saving, while the standalone handler consumes required write-authorization and committed-save ports. Existing save APIs remain unchanged; no generic framework or serializer plugin is added.

此处理器不会挂入生产组合根，现有 UAT 继续是下载预览。测试授权端口不证明生产 deploy/overwrite 已完成，read 许可不能用作 write 许可。未来可信 Allow 必须同时绑定认证操作者、正确的 ordinary local/generic 仓库及适用写操作；非管理员 deploy-only/overwrite/delete 组合仍需补证。
The handler is not mounted in the production composition root; UAT remains a download preview. Test authorization does not establish production deploy/overwrite authorization, and read rights cannot substitute for writing. A future trusted Allow must bind principal, eligible repository and operation; deploy-only/overwrite/delete combinations remain unverified.

## 支持域 / Profile

仓库键为长度不超过 64 的非点 ASCII 单段；文件路径按单 slash 分段，只准 `[A-Za-z0-9._-]`，排除空段、点段、前后 slash，终名有非空 stem 和精确 `.bin`/`.txt` 后缀。原始路径最多 8192 字节、parent 最多 1024、name 最多 255，深度复用 `Path.Depth`。`ValidateStoragePath` 仅提供既有字符状态机，不被误称为长度校验。不得 decode、clean 或 trim。
Keys and paths follow the finite ASCII segment/suffix profile with explicit raw-path, parent and name limits. Depth reuses `Path.Depth`; the existing storage validator supplies character rules, not length checks. No decoding, cleaning or trimming is permitted.

操作者为 1–64 个可打印 ASCII UTF-16 码元且非全空格。声明长度必须非负；三个专属摘要各自为 nil 或精确小写十六进制 40/64/32 码元。空但存在、marker、大写、多值与非 ASCII 均为域外。这些是本地支持政策，不代表原版拒绝。
Operators contain 1–64 printable ASCII units and are not all spaces. Length is nonnegative. Each checksum is absent or exact lowercase hex of the required length. Empty-present values, markers, uppercase, multiple values and non-ASCII remain local exclusions, not target rejection claims.

Source 校验要求正 ID、精确身份、文件/非 root、三 Actual 非 NULL、规范摘要与非负 size、完整 Metadata、三时间均在 0–253402300799999 毫秒、有效操作者；SHA1/MD5 Original 缺省或规范且等于对应 Actual。精确空 SHA1 必须 size 为 0。此纯校验只验证事实形状，不证明已提交或物理字节完整性。
Source validation checks identity, nullability, metadata, bounded timestamps/actors and canonical digests/Originals. Exact-empty SHA1 requires zero size. Shape validation alone proves neither commit nor physical integrity.

## 保存生命周期 / Save lifetime

构造必须显式提供真实 DB、初始化的 binary service、并发安全 Clock 和大于零且不超过一分钟的节点预算；DB 连接上限必须显式至少为 2。不执行 DDL、Ping、自动引导或配置修复，不 Close 调用方 DB。固定 ROOT ASCII MIME 目录只涵盖两种后缀。
Construction requires real dependencies, an explicit bounded node budget and at least two configured connections. There is no DDL, ping, bootstrap, repair or ownership transfer of the DB. The fixed MIME catalog covers only the two suffixes.

1. 校验输入，在读取正文前采一次服务器墙钟作为请求 Created/Modified。这是明确的本地采时点，不是原版慢上传顺序观察。
   Validate input and sample request Created/Modified before body reading, an explicit local timing choice.
2. 原样复用 `SaveFile` 的 Verify + CLIENT、保留已有 created、Invariant 加载画像。真实正文发布与登记发生在节点事务之前。
   Reuse the unchanged save command with the explicit profile; publication/registration precede the node transaction.
3. 首次节点 Lookup 才派生节点 context、取得单 Saver 写阶段门并开启 READ COMMITTED 事务。读取正文不占节点连接；排队、SQL、独立号段预留和提交窗口均受节点预算约束。
   Begin the transaction lazily at the first lookup. The single-Saver gate covers only the bounded node phase, not body reading.
4. NextID 只调用真实 Current 和 Reserve 一次，不重试争用，不自动初始化或修复导入高水位。节点事务与已提交号段始终是两个生命周期。
   Allocate through the real one-shot primitives without retries or high-water repair; reservation and node lifetimes remain separate.
5. 同事务二次读 Source，核对保存 ID、规范 Published 四元组、声明长度与 Source 形状，再 Commit 一次。只有明确成功提交才返回 Source，不在提交后另连接回读。
   Re-read and validate inside the same transaction, then commit once. Return facts only after explicit commit success.
6. 未确认提交的事务在退出时尝试 Rollback；仅忽略 ErrTxDone。回滚故障保持存储故障优先，所有错误返回零 Source。标准 Rollback 没有 context 参数，不宣称任意驱动的硬截止保证。
   Clean up unconfirmed transactions and preserve storage-fault priority. Errors return zero facts; generic driver rollback has no hard deadline guarantee.

门只在单个共享 Saver 内串行节点阶段，不证明跨进程或 HA 写入安全。发布字节、登记和已提交 ID 不因后续节点失败而撤销；没有补偿删除、upsert、自动重试或提交结局猜测。未来启用 UAT 写入前，可信运维必须将持久分配器推进到已导入 ID 之上。
Serialization is local to one shared saver, not a distributed guarantee. Post-publication failures retain bytes, registry rows and committed IDs. There is no compensation, upsert, retry or inferred commit outcome. Future UAT writes require an operator-verified allocator high-water mark.

## HTTP 与资源所有权 / HTTP and resource ownership

RepoKey、MaxBodyBytes、PublicOrigin、Auth 和 Saver 都必须显式有效。Origin 是可信配置，不反射 Host；只支持无用户信息、query、fragment 或额外 path 的 http(s) origin。元数据 URI 不代表传输已具备 HTTPS；生产认证仍须独立核实可信 HTTPS。
Dependencies and limits are explicit. URI origin is trusted configuration, never reflected from Host. Metadata URL configuration does not attest secure transport.

检查 raw URI、别名聚合后的头、已知长度与摘要后才授权，再调用一次 Saver。非 PUT 为 405，域外路径/畸形字段 400、未知长度 411、超量 413；特殊 framing/flags、条件或范围请求、泛摘要及老 ArtifactoryBuildClient 画像为 501。HEAD 错误无正文。完整名单与分支由实现验收冻结，不把本地状态冒充完整原版协议。
Validate raw request facts, authorize once and save once. Rejections use explicit local policies, with bodyless HEAD errors. Special framing, flags and legacy-client variants remain outside the profile.

父请求 context 才决定取消并中止传输；父仍活跃时依赖截止是 500。错误树仅含精确客户端摘要/长度哨兵才可为 409；包含额外 I/O、close/remove 或回滚故障必须为 500。正常单子项 join 仍可为纯客户端错误；不可裸用 `errors.Is(mismatch)` 忽略并列清理失败。读取/预算错误也不能遮蔽并列内部故障。
Only the parent request determines cancellation. Client-mismatch classification must inspect the bounded error tree: additional cleanup faults remain 500, while a normal single-child join can remain a client failure. Read/budget errors must not hide sibling internal failures.

Handler/Saver 不关闭原始 body：`net/http.Server` 拥有它，直接调用或替换 body 的调用方负责自己的生命周期。包装器限制字节并检查 context，不承诺打断任意阻塞 reader；宿主必须配置有界 ReadHeaderTimeout/ReadTimeout。没有额外后台 goroutine 或正文回放。
The server owns the original request body; direct callers and replacement-body middleware own their respective lifetimes. Byte/context guards cannot interrupt arbitrary blocked readers, so the host must configure bounded read timeouts. No background worker or replay is introduced.

201 只消费已提交 Source，输出观察到的文件字段、毫秒 UTC created、字符串 size、规范三 Actual、SQL 中可选 SHA1/MD5 Original；SHA256 Original 仅在本次 CLIENT+Verify 成功域取规范 Actual。它不是历史 SQL 列恢复。Location、uri、downloadUri 来自可信 origin 和受限路径，三者相等；MIME 固定后缀，只有 SHA256 响应摘要头。JSON 先序列化后写成功头；短写或输出失败中止传输。
Success derives from committed facts, with the bounded effective-Original rule rather than a fabricated SQL column. URLs share the trusted origin and validated identity. Serialize before success commitment; abort failed or short writes.

## 验收范围 / Verification scope

验收入口是 `scripts/check.sh` 与必须连接一次性真实数据库的 `scripts/check-postgres.sh`，没有 DSN 不允许跳过。纯画像和 HTTP 单元覆盖输入/Source、别名与空值头、尾 slash origin、运行期不可比错误、原始错误树宽度/节点/深度预算、读取/取消/复合清理错误及输出短写。纯画像另有独立参考探针和操作者/字符守卫故障回归。
The shared and mandatory disposable-PostgreSQL scripts own the verification gates. Profile and HTTP tests cover request/source boundaries, headers, origins, runtime error comparability, bounded classification, read/cancel/compound faults and response writes.

`integration/postgres/upload_save_test.go` 覆盖创建、重放、覆盖、空字节、三种错误摘要、正文期零占用节点连接、SQL/号段故障、触发器改写的权威 Source 与延迟 Commit 失败；`upload_concurrent_test.go` 用真实表锁观察单共享 Saver、两连接池的节点阶段串行化，并确认两个 publication 先独立提交。300 毫秒的负向观察是明确的有界调度窗口，不冒称虚拟时间或跨进程并发保证。
The save regressions verify real PostgreSQL/filesystem facts, authoritative re-reads and retained effects on failure. The concurrent regression observes the single-saver gate through real SQL locks while both publications commit; its bounded observation window is explicit.

`upload_http_test.go` 通过有界 `httptest.Server` 连接真实 Saver、SQL 提交与既有下载处理器：逐字段核对 201/头/Original、创建者和时间保持、摘要拒绝后的原字节、授权/路径/flags/长度拒绝零副作用、真实延迟 Commit 失败的 500 与随后 GET 404。测试授权是夹具声明，不证明生产 deploy/overwrite 策略。
The HTTP regression uses the actual saver and existing downloader for success/read-back and real deferred-commit failure. Test authorization is a fixture declaration, never proof of production deploy/overwrite policy.

生产身份/deploy/overwrite、完整 URI/Unicode、未知长度或特殊 framing、目录、checksum-deploy、属性、remote/virtual、并发/HA、完整恢复/GC 和正式发布均不由该有限适配器验收。
Production write security, broader protocol/storage behavior, HA/recovery/GC and formal release remain separate.

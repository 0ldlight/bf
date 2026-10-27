# 即时加载快照 / Immediate loaded snapshot

状态：已实现，等待 PR 审阅及合并；最终提交验证以 PR 为准。
Status: implemented, pending PR review and merge; final commit checks follow the PR.

```go
func LoadedFileSnapshot(source FileSnapshotSource) (FileInfoSnapshot, error)
```

单个 [Source](FILE-SNAPSHOT-SOURCE.md) 提供完整即时字段，身份复用 [LoadedFileIdentity](LOADED-FILE-IDENTITY.md)。没有外部 normalized Original、normalizer 回调、JVM sidecar、新依赖或全局配置读取。
One source supplies all immediate fields. Identity conversion is reused, and Original normalization has a concrete private implementation.

## 字段与空性 / Fields and nullness

- created、updated、size 保留完整 int64；modified>0 原样，否则回退 loaded created（可为零或负）。两个 Actor 取既有码元，不再次缩写或恢复上传历史。
- checksum 固定 SHA1、MD5、SHA256 三条，Actual=NULL 也不缺条目。Valid=false→nil；true→无损 UTF-16，空串为非 nil 空。Actual 不做 hex/长度/大小写或存储写校验。
- SHA256 Original 固定 raw 七码元 `NO_ORIG`；MIME 固定 nil。ID 不进入快照，不新增 ID 正值守卫。

Times, actors and size preserve the source domain, with the target modified-time fallback. Three checksum entries are always present; Actual null and empty remain distinct. MIME represents the immediate unresolved field.

## Original 的明确支持域 / Explicit Original profile

| SHA1/MD5 Original | 行为 / Behavior |
|---|---|
| nil | 保留 nil / preserve null |
| UTF-16 长度不是各自 40/32 | 逐码元保留，包括非 ASCII、NUL、孤立代理 / preserve every unit |
| 恰长且全部 ASCII `0..127`，不含大写 `I` | A–Z 转 a–z，其他码元保留 / lowercase ASCII capitals; preserve other units |
| 恰长且含大写 `I` 或任意非 ASCII | ErrUnsupportedLoadedOriginal / explicitly unsupported |

最后一项是本地范围限制，**不是原版拒绝**。ASCII 大写 `I` 的结果依赖 Locale；本接口不猜测其来源。NUL/控制码元的纯快照支持不表示 PostgreSQL 文本列可存储。目标 validate=false 仍对所有恰长 Original 使用 Java 默认 Locale lowercase；其规则可能依赖上下文、改变长度。此处不以 Go 简单小写近似，也不假定目标部署 Locale。已有 reader、原始快照与保存门的接受域不变。
The target accepts exact-length nonhex originals. Local rejection marks unsupported coverage rather than target validation; existing readers and snapshots keep their domains.

## 错误顺序 / Error order

1. 身份转换失败返回零快照及原 ErrInvalidPath。
2. Metadata=nil、false Actual 位配非空字符串或 Actual 畸形 UTF-8，返回零快照及 `ErrInvalidFileSnapshotSource`（`repository: invalid file snapshot source`）。三槽按 SHA1→MD5→SHA256 检查，各槽先空性矛盾再 UTF-8。nil Metadata 是本 API 缺字段，不表示历史行无效；显式零 Metadata 成功。
3. SHA1→MD5 Original 不支持，返回零快照及 `ErrUnsupportedLoadedOriginal`（`repository: unsupported loaded original`）。
4. NewFileInfoSnapshot 的既有错误原样返回；所有错误均不回显原始值或提供部分快照。

The two new fixed errors distinguish source-input policy from unsupported normalization. Source DTO concurrent mutation is outside the contract; successful snapshots and Fields copies retain defensive ownership.

## 生命周期与证据 / Lifecycle and evidence

结果只表示目标 fileInfoFromNode 刚返回、尚未 MIME getter/copy/wrapper 的状态，**不是 DbMutableFile 的 original/virgin 基线**。复制会触发源 MIME getter，并可能改写源值；不能直接将此结果接入[更新门](FILE-SNAPSHOT-UPDATE.md)或宣称完整保存。
This immediate snapshot is not the copied dirty-check baseline. MIME resolution, copy side effects and save wiring remain separate work.

初始 hex 画像的固定类参考包含 23 次 Original 回放（全部目标成功）及既有 10 次加载回放。23 行中的 12 行符合初始支持域，其余 11 行是目标成功而本片明确不支持；私有研究模型与产品 Go 验证分开记录。受控 JVM/Locale 参考不证明部署环境；未执行 JDBC、MIME getter、复制或保存。实现差分、故障、race 和 CI 出口以对应 PR 为准。
The bounded class references establish observations within controlled runtime profiles. Target success and local unsupported results are counted separately; reference research is not a substitute for product tests.

初始 hex 画像的本地 Go 组合差分覆盖两批33个既有实际类参考：18个支持行的全部即时字段一致、14个目标成功行按本地画像返回 unsupported、1个目标目录拒绝对应既有身份守卫。七个顶层单元测试与 race 通过；行为 RED 先捕获 NULL 被错误折为空切片。故障、共享检查与完整 CI 的最终提交记录以 PR 为准。
The local Go comparison separates eighteen full-field matches, fourteen local exclusions and one folder rejection. Unit/race and behavioral RED evidence precede final PR checks.

后续 MIME 状态转换见[目录与物化](FILE-MIME-MATERIALIZATION.md)：需显式调用，不改变此即时组装器的 nil MIME，也不等于完成 copy 基线。
See [catalog and materialization](FILE-MIME-MATERIALIZATION.md) for the explicit next transition; immediate assembly still produces nil MIME and no copied baseline.

## ASCII 回读扩展 / ASCII read-back extension

BIN-191 复用原版按码元长度选择默认 Locale lowercase 的规则，在无需新参数的范围内扩展支持。3,072 行真实 ChecksumInfo 组件参考覆盖全部 ASCII、首/中/末、SHA1/MD5 与 ROOT/tr/az/lt，后续 true 构造均保持 false 输出；其余位置为 A，未穷举所有组合。3,048 行不含 I 的结果一致；24 行 I 随 Locale 分歧。参考为本机 JVM 26.0.2.1、两个实际类和 26 个同 WAR 依赖，**不是部署 JDK/Locale 观察**。静态规则另参考 [OpenJDK StringLatin1](https://github.com/openjdk/jdk/blob/jdk-26-ga/src/java.base/share/classes/java/lang/StringLatin1.java) 与 [ConditionalSpecialCasing](https://github.com/openjdk/jdk/blob/jdk-26-ga/src/java.base/share/classes/java/lang/ConditionalSpecialCasing.java)。不复制目标实现源码或引入 JVM 运行依赖。
The controlled component matrix and JDK branches support the locale-invariant ASCII extension. They establish no deployed runtime or complete Unicode compatibility; uppercase I and non-ASCII exact-length values remain explicitly tracked under BIN-191.

SERVER 写侧仍原样保存非 hex 大写 Original；真实 Lookup 保留 SQL 原值，即时加载才小写。相同 binary 的直接更新可跳过，不能为了小写化主动改行；内容变化而保存时才投影小写后的 Original，提交与回滚仍由调用方控制。CLIENT 匹配摘要路线继续回读并可再次更新。重复请求同一大写非 hex Original 仍可能 dirty，不承诺所有写请求幂等。
SERVER writes preserve raw nonhex capitals. Lookup preserves SQL values; loading lowercases them. Same-binary direct updates may skip without normalizing the stored row, while a changed-content write projects loaded Originals. Caller-owned commit/rollback and matching CLIENT read/update remain covered. Repeated uppercase nonhex requests may remain dirty.

单元覆盖全部 128 ASCII 码元在两长度、三位置上的真实组装出口，以及非 ASCII、代理、错误顺序和所有权。PostgreSQL 回归覆盖上述 SERVER/CLIENT 闭环及 I/非 ASCII 成功写入后的明确加载拒绝。执行证据与最终 CI 绑定对应 PR 提交；完整 Locale/Unicode、HTTP 与保存生命周期继续开放。
Unit and real-PostgreSQL seams protect the expanded domain and remaining exclusions. Final execution evidence binds the PR revision; full Locale/Unicode, HTTP and save lifecycle remain open.


显式 Original 规则及 I/U+0131 回读闭包见[加载大小写规则](LOADED-ORIGINAL-CASING.md)。旧无参入口继续使用本页 invariant 域；显式入口复用同一组装器。
See [explicit loading rules](LOADED-ORIGINAL-CASING.md) for I/dotless-i closure; the existing entry point retains this page's invariant domain.

# 普通 Generic 下载接入契约 / Ordinary Generic download integration

BIN-198 承接 [BIN-128 下载观察](GENERIC-DOWNLOAD-CONTRACT.md)与 [BIN-197 内部读取](LOCAL-FILE-READ.md)，冻结下一条完整 GET/HEAD 业务切片的证据、接口边界和验收。本文交付契约，不新增 HTTP 路由；当前服务仍只有健康入口。父项 PKG-007/API-001/REP-001/STO-001 保持未完成。
BIN-198 defines the evidence, integration boundaries and acceptance for the next complete ordinary GET/HEAD slice. This contract adds no HTTP route; the current service still exposes health only. Parent capabilities remain incomplete.

## 受控观察 / Controlled observations

2026-10-29 对身份接口返回 Artifactory 7.161.26、revision86126900 的专用实例，新建独立 local/generic 合成仓库，上传35字节 binary、13字节 text 和空文件。25组实际请求禁止重定向与代理，保留响应及 HAR；结束后核实仓库归属、文件摘要，删除本任务仓库并核实缺席。没有修改既有仓库、用户或权限配置。独立离线核验25组记录的453项断言，主线另行只读复核仓库已缺席。
An isolated local/generic repository held three synthetic artifacts. Twenty-five actual requests were captured without redirects or client proxies; ownership and checksums were verified before deleting the owned repository and confirming absence. Existing repositories, users and permissions were unchanged. Independent offline verification checks 453 assertions across all 25 records; a separate read-only recheck confirms repository absence.

| 请求画像 / Request | 观察 / Observation |
|---|---|
| binary/text GET、HEAD | 200；GET 为完整合成字节，HEAD 零正文且表示头与 GET 一致 / Complete GET bytes; bodyless HEAD with matching representation headers |
| empty GET、HEAD | 200、Content-Length=0、空内容三摘要 / Empty representation and its three checksums |
| 缺文件 GET、HEAD / Missing file | 404；HEAD 零正文 / Bodyless HEAD |
| 目录和仓库根 GET、HEAD / Directory/root | 200 HTML 索引；HEAD 零正文 / HTML index, bodyless HEAD |
| `%70ayload.bin`、编码 slash、重复 slash、单点段、编码单点段 | 本部署返回原制品200 / This deployment returns the artifact |
| 双重编码 slash、包含不存在中间目录的 parent 段、文件尾 slash | 本样本404；不外推为统一 URI 规范 / Sample-specific 404 |
| `:properties` | 302 到属性 API；没有跟随 / Redirect to properties API, not followed |
| 不存在仓库 GET、HEAD / Unknown repository | 404 |
| 无认证 GET、HEAD / Anonymous | 当前配置401；不代表其他用户/权限配置 / 401 under current configuration only |

成功样本的 Content-Length 为节点表示长度，Content-Type 分别为 application/octet-stream 与 text/plain；ETag 为未加引号 SHA1，三个 X-Checksum 头与合成字节匹配。Content-Disposition 包含 attachment、filename 和 filename*；Last-Modified 是 HTTP 日期。空文件也有这些头。不能由这些正常样本断言损坏对象会重新计算长度或摘要。
Successful representations include length/type, unquoted SHA1 ETag, three matching checksum headers, attachment disposition with both filename forms, and Last-Modified. Healthy samples do not establish rehashing or size correction for corrupt objects.

这些是当前部署与样本的观察。身份接口不证明运行实例与本机 WAR 字节完全一致；HTTP 路径结果也不能单独证明是哪层完成解码。未对物理对象破坏、数据库篡改、非管理员拒绝权限或传输中断做服务器实验。
These observations are deployment- and sample-bounded. A version endpoint does not attest deployed WAR bytes; URI results do not attribute decoding to one layer. No server experiment altered objects/database rows or exercised non-admin authorization denial or interrupted transfers.

## 调用链约束 / Call-chain constraints

实际同 WAR 的14个独立类（两个审计共15项绑定）逐一核对 class/JAR/javap 摘要，恢复源码仅导航；组件 JAR 7.161.15 不是产品包版本。普通 local HEAD 的响应分支与 GET 的内容发送分支不同；不能将已打开流的内部 ReadFile 直接作为 HEAD 的唯一入口。远端、store-locally 和其他请求策略不能由这个本地分支推断。
Same-WAR class/JAR/javap identities are checked separately from recovered-source navigation; component JAR versions do not identify the product version. Ordinary local HEAD takes a different response branch from GET; the eager internal ReadFile cannot be its sole entry point. Remote/store-locally behavior requires its own evidence.

请求身份必须先经过明确的下载边界，再生成存储 Path；保存命令的 create-path/suffix 映射不是下载解析器。目录、属性重定向、路径解码与重复 slash 不能靠一轮通用 clean 或重复 unescape 冒充目标行为。
A download boundary must produce the stored Path explicitly. Create-path/suffix mapping is not a download parser; generic cleaning or repeated unescaping does not establish target URI behavior.

## 下一实施切片 / Next implementation slice

以单个显式配置的 ordinary local/generic 仓库为范围，交付一个可注入依赖的 GET/HEAD 适配器及真实数据库测试。它不会自动挂入 NewHandler；生产路由必须等可信身份、仓库解析和授权入口一起验收，不用默认匿名放行、伪 principal 或测试 allow-all 填空。
Build one injectable ordinary-local GET/HEAD adapter and real database tests. Production mounting waits for trusted identity, repository resolution and authorization; no anonymous default or test authorization substitute fills that gap.

第一画像只接收显式配置的仓库键与非空 ASCII 文件路径：分段仅 `[A-Za-z0-9._-]+`、不得为 `.` 或 `..`、单 slash 分隔，文件扩展名仅 `.bin`/`.txt`。编码 URI、点段、重复 slash、metadata suffix、文件尾 slash、目录和根索引属于明确排除域；未来按证据扩展，不能宣称这些目标200/302行为已兼容。本地有限策略固定为：其他方法405且 Allow 为 GET, HEAD；域外路径、query 返回400；Range 或任一 If-* 条件头返回501。这些是原创排除策略，不是原版状态断言，不能悄悄退化成普通200。HEAD 的所有错误仍无正文。
The first profile accepts a configured repository key and nonempty ASCII file paths: `[A-Za-z0-9._-]+` segments excluding dot/double-dot, single separators, and only `.bin`/`.txt` extensions. Encodings, dot/repeated segments, metadata suffixes, trailing slashes and index pages remain excluded, even where the target succeeds. Local exclusion policy: other methods return 405 with Allow: GET, HEAD; out-of-profile paths or queries return 400; Range or any If-* header returns 501. These original policies are not target status claims. HEAD errors remain bodyless.

接口依赖必须提供可信授权决定；路径验证后、节点查询前做授权。HEAD 只读取同次节点事实及必要配置，不打开内容；GET 复用已保存节点的内容选择规则，保持同次权威 Source，不重查 registry、不重新组装完整 FileInfo。返回表示所需的时间、MIME 和摘要必须来自明确事实；不能从 nil Metadata 或 SQL NULL 虚构缺省值。
Authorization precedes node lookup after path validation. HEAD uses node facts/configuration without opening content; GET preserves authoritative content selection without registry rereads or complete FileInfo reconstruction. Representation metadata needs explicit sources, not invented defaults for absent data.

表示投影采用明确的本地有限政策：Source 身份必须等于已授权的存储 Path；Metadata 缺席、三个 Actual 的任一 SQL NULL、非规范小写十六进制摘要（SHA1/MD5/SHA256 长度40/32/64）、负 size 均在提交响应前返回500，不省略摘要或虚构时间。此规则收紧 HTTP 支持域，不改变内部 ReadFile 对遗留行的读取能力。精确空 SHA1 的 size 必须为0；正常字节与声明 size 不一致的损坏画像仍无完整兼容结论，不通过预读或重算摘要修补。
Local representation policy requires the source identity to match the authorized stored Path. Missing Metadata, any NULL Actual checksum, noncanonical lowercase hex digests (40/32/64 units) or negative size return 500 before commitment. These HTTP exclusions leave internal legacy reads unchanged. Exact empty SHA1 requires size zero; corrupt size/byte mismatches gain no parity claim or pre-read/rehash repair.

Last-Modified 只取 ModifiedMillis，允许0至253402300799999，按秒向下取整、UTC/GMT HTTP 日期输出；域外返回500，Created/Updated 不作回退。第一画像的 MIME 固定 `.bin`→application/octet-stream、`.txt`→text/plain，不读完整快照或部署 MIME XML，不声称浏览器覆盖、任意配置或其他扩展名兼容。ETag 使用原始规范 SHA1、不加引号，三个 checksum 头原样；Content-Length 使用受支持的节点 size。因 Range 尚未实施，本地适配器不广告 Accept-Ranges: bytes；这是相对目标表示的明确有限差异。
Last-Modified uses only ModifiedMillis in the stated nonnegative through year-9999 range, floored to seconds and rendered in UTC/GMT; no Created/Updated fallback. Fixed profile MIME maps .bin/.txt to the observed types without snapshot/XML loading or broader configuration claims. ETag is unquoted canonical SHA1, checksum headers remain unchanged, and length is the supported node size. The adapter omits Accept-Ranges: bytes until range support exists, an explicit finite deviation.

raw RequestURI 必须先经过画像守卫，再生成存储 Path，不允许路由器先 clean/decode 使域外请求变成支持请求。可信认证依赖必须显式区分允许、未认证401、已认证拒绝403；未提供依赖不能构造可用处理器。两种拒绝在查询前结束且不暴露存在性；401 challenge 由可信认证层提供，不在此发明认证机制。这是本地政策，不证明目标完整权限隐藏规则。
Gate raw RequestURI before path construction or router normalization. A required trusted authentication dependency distinguishes allow, unauthenticated 401 and authenticated denial 403; no dependency means no usable handler. Both denials precede lookup and hide existence; the trusted layer supplies any 401 challenge. This local policy establishes neither an authentication mechanism nor complete target permission masking.

GET 必须在提交成功头之前完成 Open；取得 Body 后所有退出都关闭，不因取消、写入或关闭错误泄漏流。HEAD 不得为了执行这些 GET 步骤而打开对象。
GET must open before committing successful headers and close every acquired body on all exits, including cancellation/I/O failures. HEAD must not open content to execute GET preparation.

GET 成功后由适配器负责恰当关闭内容流；SQL/依赖生命周期仍归注入者。发送响应头前的失败与发送后的 Read/Write/Close 失败必须分开记录和测试：后者不能写第二份错误正文或改报完整下载成功。现有内部读取成功仅表示打开成功，不表示已传输完毕。
The adapter owns the returned body's close while the injector owns dependencies/transactions. Failures before headers and subsequent read/write/close failures need distinct handling; after commitment there is no second error representation or complete-download claim.

缺节点、缺对象、无效来源、认证失败、授权拒绝分别建模。缺节点404已有证据；匿名401仅当前部署有证据。缺对象及已发送头后的目标错误状态尚未由服务器实验确认；本地预提交存储/无效来源失败固定500，不能将其映射404；本地安全错误正文必须原创、固定且不泄漏路径或内部异常，其与原版正文差异明确记录。
Model absence, object failure, invalid source, authentication and authorization separately. Missing-node 404 is observed; anonymous 401 is configuration-specific. Object/committed-stream failures lack server fault evidence. Local precommit storage/invalid-source failures return 500 rather than masquerading as absence. Original fixed local error bodies must avoid leaking internals and record deviations.

## 验收与退出 / Acceptance and exit

实施必须同时交付以下出口；本契约尚未执行新适配器产品测试。
The implementation must deliver these exits; this contract has not run adapter product tests.

- 真实 SaveFile 提交后，新请求 GET 完整 binary/text/empty 字节与表示头；HEAD 零正文、同表示头且零内容 Open。用计数端口证明单次节点读取与先授权后查询。
  Commit through real SaveFile, then verify new GET bytes/headers and bodyless HEAD with zero opens; assert one lookup after authorization.
- 未授权不得查询节点、打开对象或暴露是否存在；缺节点、遗留/NULL 元数据、缺对象及符号链接安全错误分别验证。现有内部读取边界保持不变。
  Denials perform no lookup/open or existence disclosure; independently cover absent/legacy/NULL metadata, missing objects and unsafe symlinks.
- 用真实 net/http 服务验证 Content-Length/HEAD/错误体，不仅依赖 ResponseRecorder；注入可观测 Read/Write/Close 故障与取消，核对响应已提交后的行为。
  Use real net/http transport as well as unit ports for lengths/HEAD/errors; inject observable I/O/close/cancellation failures across the commitment boundary.
- 独立 RED/GREEN、竞态、真实 PostgreSQL/文件系统、故障回归、共享检查与独立审查；交付精确提交及默认分支完整 CI。不得用合同探针通过代替产品测试。
  Preserve independent RED/GREEN, race, real database/filesystem, fault/shared/review and exact/default CI evidence; reference probes are not product tests.

Range/条件请求的既有18样本契约继续保留；远端/虚拟、上传 HTTP、完整权限矩阵、GC/并发写与完整 Unicode/Locale 仍是后续工作。下一切片不能缩小最终 Generic 兼容目标。
The existing eighteen-case range/conditional contract remains intact. Remote/virtual/upload/auth/GC/concurrent-write/full-Unicode work remains open; the finite slice does not reduce the final compatibility goal.

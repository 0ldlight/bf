# 普通 Generic PUT 接入契约 / Ordinary Generic PUT integration

状态：BIN-204 的有限原版调查与下一切片边界，不是已实现的上传处理器。现有 UAT 仍只提供已验收的下载画像。
State: bounded reference investigation and the next-slice boundary for BIN-204, not an implemented upload handler. UAT retains its accepted download profile.

## 目标与证据 / Target and evidence

受控专用实例的身份接口返回 Artifactory `7.161.26`、revision `86126900`。实验使用一个随机独占 local/generic 仓库、合成小文件、无组的非管理员账户和仅有 read 的独占权限；没有修改原有仓库、全局设置或 BinFlow UAT。每次文件 PUT 后分别 GET 字节及读取存储元数据，区分响应与实际持久化结果。
The dedicated instance reports the target version/revision. The experiment uses one uniquely owned local/generic repository, synthetic small files, a groupless nonadmin user and an owned read-only permission. Existing repositories, global settings and BinFlow UAT are untouched. File PUTs are followed by content GETs and storage-metadata reads, separating responses from persisted outcomes.

本轮共 56 条请求记录，其中 19 条 PUT；原始 HAR SHA-256 为 `b44d8c68ac689bdd795a94f2710b8690f4d2bda22be590958471c112282e5f5c`。REA 完整解码并保留 56 条，父证据为 `ev_d1984d240fd56e62b242b599a05ddf2665ce59313f7f6045cea177ddf3e9d80a`；原始 HAR、控制响应、验证与导出保持私有。REA 的离线捕获解析不证明线上部署字节、浏览器事务身份或完整协议覆盖；身份标签来自经审查的采集程序，HAR 不保存认证头。
The capture contains 56 request records, including 19 PUTs, bound to the digest and retained REA evidence above. Raw captures, control responses, verification and exports remain private. Offline REA decoding does not attest deployed bytes, browser transaction identity or complete protocol coverage; identity labels come from the reviewed producer and HAR omits authentication headers.

## 实际观察 / Observed behavior

| 受控输入 / Input | 响应与持久化 / Response and persistence |
|---|---|
| 新文件、相同字节重放、不同字节覆盖 / Fresh, identical replay, different overwrite | 均为 201；回读为相应字节 / All 201; read-back matches the corresponding bytes |
| 空文件、`.txt` / Empty file, `.txt` | 201；长度与三摘要匹配实际字节 / 201; size and three digests match the bytes |
| 正确 SHA1、MD5、SHA256，单独或三者齐备 / Correct individual or combined checksums | 201；实际摘要正确，Original 字段另行观察 / 201; actual digests match, with Originals observed separately |
| 单独错误 SHA1、MD5、SHA256 / Individual incorrect checksums | 409；随后 GET 与元数据均 404 / 409; subsequent content and metadata reads both 404 |
| 错误摘要覆盖已有文件 / Bad-checksum overwrite | 409；旧字节及其摘要保持 / 409; prior bytes and digests remain |
| 文件名中编码 ASCII 字符 / Encoded ASCII filename character | 样本为 201，按解码名字回读成功 / Sample returns 201 and is readable under its decoded name |
| 尾斜杠 PUT / Trailing-slash PUT | 201，但为目录表示；随后无斜杠 GET 为 302，存储 API 为目录 / 201 directory representation; slashless GET redirects and storage API reports a folder |
| 已认证且可 read、无 deploy / Authenticated read-only user | 控制 GET 200；新建和覆盖 PUT 均 403，无 Basic challenge；新文件缺席、旧文件保持 / Control GET 200; create/overwrite PUTs 403 without challenge, with no new file or changed old file |
| 匿名 PUT / Anonymous PUT | 401，带 Basic challenge；文件缺席 / 401 with challenge; no file |
| 不存在仓库 PUT / Unknown-repository PUT | 本部署该样本为 405；不得由下载 404 外推 / This deployment sample returns 405; download 404 cannot be extrapolated |

只读账户的 403 正文明确为 deploy 权限拒绝，与先前下载实验中的 recurrent-failure 临时封禁不同。本轮不使用该账户的错误密码请求污染权限观察。管理员覆盖不证明非管理员仅有 deploy 时可覆盖；delete/overwrite 权限组合仍待专项验证。
The read-only user's 403 explicitly reports deploy denial, unlike earlier recurrent-failure blocking. This experiment avoids bad-password requests for that identity. Admin overwrite does not establish overwrite rights for a deploy-only nonadmin; deploy/delete combinations remain unverified.

### 成功表示 / Successful representation

普通文件返回 `application/vnd.org.jfrog.artifactory.storage.ItemCreated+json;charset=UTF-8` 和 Location。JSON 包含 `repo`、以 slash 开头的 `path`、`created`、`createdBy`、`mimeType`、字符串 `size`、三种 `checksums`、`originalChecksums`、`uri` 和 `downloadUri`。样本未返回独立 `updated` 字段；相同及不同字节覆盖均保留初始 created/createdBy，不能据此推断所有元数据更新规则。
Ordinary file success includes the stated media type and Location. JSON carries repository/path, created/creator, MIME, string size, actual digests, Originals and two URIs. No separate updated field appears in these samples. Both overwrite forms retain initial created/createdBy, without establishing every metadata-update rule.

无摘要头的样本也返回计算所得 SHA256 Original；SHA1-only 和 MD5-only 分别额外保留对应 Original，三头齐备返回三项。响应 Original 不可简单照抄请求头，也不可据此把三 Actual 全部当作三 Original。此观察只涵盖所列有效小写十六进制输入；空白、多值、marker、非 ASCII 和策略开关另行冻结。
Even without checksum headers, the sample returns a computed SHA256 Original. SHA1-only and MD5-only add their corresponding Original; all three headers yield all three. Originals cannot be modeled as a verbatim header map or as all three Actuals. These observations cover only the listed valid lowercase hex values; blank/multiple values, markers, non-ASCII and configuration policies remain separate.

目录成功体只有 `repo`、`path`、`created`、`uri`，没有文件摘要或 size。文件与目录不能共用“201 即文件保存成功”的判断。URI、Location 与时间只作为当前部署字段事实，不将其私有地址或时钟值写入公开资料。
Directory success has only repository, path, created and URI fields. A 201 alone cannot establish file-save success. URIs, Location and timestamps are deployment-bound facts; private addresses and clock values stay out of public records.

## 静态链与未决分支 / Static chain and unresolved branches

固定 Artifactory WAR SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37` 的选定类将普通输入流上传与目录、checksum 文件、checksum-deploy、Expect 复用和扩展分支分开。普通链创建保存 context、调用资源保存，再调用成功响应 helper；独立绑定的 helper 明确构造 201/Location/ItemCreated 表示。三种专属 checksum header 的读取与后续 ChecksumInfo/策略消费是不同阶段，不能用读取层原文返回推断最终 Original。
Selected classes from the fixed WAR distinguish ordinary input-stream uploads from directories, checksum files, checksum-deploy, Expect reuse and extension branches. The ordinary chain builds a save context, saves the resource and calls a success helper explicitly constructing 201/Location/ItemCreated. Reading dedicated checksum headers is separate from later ChecksumInfo/policy consumption; raw header access does not establish final Originals.

私有类/JAR/javap 绑定区分已核实的关键类与仅作导航的支撑代码；组件版本 `7.161.15` 不等于产品版本。静态异常分支并非端到端 HTTP 结论，例如某内部未知仓库路径返回 404，不能推翻本轮整个请求的 405 观察。未读取部署全局开关，未执行完整 Java 保存/SQL；包内字节码也不证明线上实例运行相同包字节。
Private bindings distinguish checked key classes from navigation-only support code. Component version 7.161.15 is distinct from the product version. An internal branch, such as a 404 for one unknown-repository path, cannot override an end-to-end observed 405. Effective global settings and complete Java save/SQL execution remain unobserved; local bytecode does not attest deployed package bytes.

## 既有 Go 接缝 / Existing Go seams

`internal/localrepo.SaveFile` 已接受真实 reader、解码后的长度/Original、认证后的 UTF-16 操作者和显式策略；它先调用真实 binary 发布/登记，再调用节点保存。reader 由调用方关闭，成功节点结果在调用方提交前仍非持久成功；详见[普通流式本地保存](LOCAL-STREAM-SAVE.md)。
The existing command accepts a real reader and decoded resource/principal/policy facts. Real binary publication/registration precedes node saving. The caller owns reader closure and transaction completion; a successful command result is provisional until node commit. The linked stream contract is canonical.

登记事务、已提交 ID 预留和节点事务并非一个原子域；节点失败或回滚不撤销已发布字节、登记或号段。上传接入不能偷偷新增补偿删除、同路径 upsert 或自动重试。当前 UAT 的受信任样本含显式导入 ID；启用未来写入前须先确认持久分配器高水位超过已导入 ID。
Registry, committed ID reservation and node transactions are separate lifetimes. Node failure/rollback does not undo publication, registration or reserved IDs. Upload wiring must not silently add compensating deletion, same-path upsert or retries. Before future UAT writes, the durable allocator must be initialized above explicitly imported fixture IDs.

现有 `internal/security` 已有真实认证、仓库目录及 read 授权，但没有可直接复用为 deploy/overwrite 许可的写侧契约。read 不能替代 deploy，测试 allow-all 不能填补生产写授权。PUT 适配器必须在任何字节发布或节点读取前取得可信写决定，并保留真正的保存顺序。
Real authentication, repository catalog and read authorization already exist; they do not supply a deploy/overwrite contract. Read rights and test allow-all cannot stand in for production write authorization. A PUT boundary must obtain a trusted write decision before publication or node access, while preserving actual save ordering.

## 下一切片与验收 / Next slice and acceptance

先交付独立 ordinary local/generic 文件 PUT 边界，复用真实保存核心，不立即挂入生产进程。建议先沿用现有下载的有限 ASCII 文件身份域；编码 URI、目录、checksum-deploy、属性、多值摘要头、未知长度/特殊 framing、remote/virtual 和并发覆盖保持显式域外。排除是 BinFlow 本地策略，不冒充目标拒绝行为。
The next slice should be a standalone ordinary-local file PUT boundary over real saving, before production mounting. Start from the existing bounded ASCII file identity profile; encoded URIs, directories, checksum-deploy, properties, multiple checksum values, unknown/special framing, remote/virtual and concurrent overwrite remain explicitly outside it. Exclusions are local policies, not target rejection claims.

实施前须定案：可信 deploy/overwrite 授权、解码策略和有限错误映射、原始 HTTP 长度与正文关闭/取消、每请求节点事务与真实 ID 消费、提交后的成功表示来源，以及受信任 public URI 构造。不能从未提交结果先写 201，不能用内部 Published 的三摘要虚构完整 created/Original/路径事实；失败的提交结局不明也不能自动当成功或重试。
Implementation must settle trusted write authorization, decoding/error profiles, HTTP length/body/cancellation ownership, per-request node transaction and real ID use, committed response facts and trusted public-URI construction. A provisional result cannot produce success before commit, nor can Published digests invent full creation/Original/identity facts. Ambiguous commit outcomes cannot silently become success or retry.

验收覆盖真实 reader→provider→registry→节点→提交→响应，包含新建、重放、覆盖、空字节、三摘要拒绝、请求体错误/取消、权限拒绝时零发布/零节点访问、发布后 SQL 失败与保留副作用、提交失败无 201，以及真实 PostgreSQL/文件系统回读。原版对象、DB、连接断开、长流、故障或并发未观察项仍保持未知，不以健康检查或录制解析替代。
Verification must exercise the real save/commit/response chain, successful writes, digest/body/cancellation failures, no side effects on denied access, retained post-publication failure effects and no success on failed commit, using real PostgreSQL/filesystem read-back. Unobserved reference faults, long streams and concurrency remain unknown; health or capture parsing is insufficient.

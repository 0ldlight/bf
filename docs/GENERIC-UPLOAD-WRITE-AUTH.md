# 普通 PUT 写授权契约 / Ordinary PUT write-authorization contract

状态：BIN-207 研究中；实际矩阵因参考实例试用许可证过期而无法建立有效基线，本文不是生产写授权实现或兼容完成记录。沿用[普通上传参考](GENERIC-UPLOAD-INTEGRATION.md)和[有限适配器](GENERIC-UPLOAD-ADAPTER.md)的边界。
State: BIN-207 research is in progress. The attempted runtime matrix could not establish valid baselines because the reference trial license expired; this is not a production authorization or compatibility-completion record.

## 研究范围 / Scope

只研究 ordinary local/generic、有限普通文件的 fresh create、相同字节重放和不同字节覆盖。分别保留无摘要、单 SHA-1、单 SHA-256、双摘要输入。管理员与显式 direct read+deploy、read+deploy+delete 用户分别观察；不从 read 权限推导 deploy，也不从管理员覆盖推导 deploy-only 行为。
Study fresh creation, identical replay and different-byte overwrite separately, including absent, individual and paired digest headers. Read access and admin success do not establish deploy-only rights.

目录、metadata/annotate、checksum-deploy、属性、remote/virtual、通配路径/group 优先级、配置开关切换和并发覆盖仍不在本轮运行矩阵。受控夹具只覆盖显式 direct grant；研究不改变全局配置。
The bounded runtime matrix excludes directories, metadata/annotate, checksum deployment, broader repository/ACL policies, configuration changes and concurrent overwrite.

## 制品与源码依据 / Artifact and source evidence

选定 WAR SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37` 中的七个选定类已逐类绑定。运行参考的 core、web-application、config 三个 JAR 在采样时与对应 WAR 条目相同；组件版本与产品版本分别记录。反编译树标注的安装包版本不同，因此树相对行号仅用于导航，结论需由该 WAR 字节码核实。
Seven selected classes are bound to the fixed WAR. Three deployed component files match its entries at sampling time; decompiled-tree line anchors are navigation, with bytecode corroboration required. File identity alone does not establish classloader choice, plugin policy or effective settings.

普通链先校验路径与 deploy（特定 metadata 分支用 annotate），再判断是否需要 delete/overwrite 许可。已有文件未提供 SHA-1/SHA-256 时，存在性决定覆盖保护；提供摘要时，分别比较实际给出的摘要，并消费相同摘要检查开关。missing digest 不是自动 mismatch。这些是选定字节码的静态事实，具体 HTTP 结论另由矩阵决定。
The selected bytecode separates deploy admission from overwrite protection. Existing-file protection distinguishes absent request digests from supplied digests and consumes a configuration flag. Missing digest values are not automatically mismatches; HTTP outcomes require runtime observation.

该开关在选定 config 类初始化中的默认值为 false；默认值不等于运行时有效值。权限求值还经过当前身份、管理员和 Access ACL；本轮未恢复完整 Access 服务规则。
The selected initializer supplies false as the flag default. This does not prove its effective runtime value or recover the complete Access permission evaluator.

## 运行矩阵 / Runtime matrix

2026-11-03 的独占受控实验原计划涵盖三种身份 × 七种输入，但在文件基线步骤即受阻。上传铺设基线返回 503，正文明确为 `Trial license expired!`；21 个计划场景均未形成有效权限样本，不能据此判断 deploy-only 或 deploy+delete 的允许/拒绝。新建仓库、两个用户及权限项均已清理并逐项确认缺席。
The controlled attempt was blocked by an expired reference trial license. All 21 planned cells lack valid permission baselines; the outcomes do not establish the permission matrix. All four owned setup resources were removed and checked absent.

保留 HAR 的 SHA-256 为 `8bc4589febc4017632726d4c2c1fd58914e201a573bdcd1f8c614ed1d945e4d3`，REA 父证据 `ev_7f6a1efbf7778d68a9ff89d8c4ec0a07908ccae603cfbbebc27398a710811334` 解码覆盖全部 57 条录制记录。录制解析不建立实时执行归属；单独的本机执行、身份、版本、制品和清理记录保存在私有证据中。该记录只证明本次实验的限制，不是 57 项权限验收。
The retained capture has 57 decoded records. Capture decoding does not itself establish live attribution; execution, identity/version/artifact and cleanup records remain separate private evidence. This is evidence of the experiment limitation, not 57 permission checks.

| 输入 / Input | 需要独立观察的差异 / Required distinction |
|---|---|
| fresh，无摘要 / fresh, no digest | deploy 对新文件的许可 / deploy admission for a new file |
| 相同字节，无摘要 / identical, no digest | 已有路径的存在性检查 / existing-path protection |
| 相同字节，SHA-1+SHA-256 / identical, paired digests | 相同摘要开关消费 / identical-digest flag branch |
| 不同字节，无摘要 / different, no digest | 内容覆盖与 delete 权限 / overwrite/delete rights |
| 不同字节，新正确双摘要 / different, new correct pair | 摘要变化是否触发保护 / changed-digest protection |
| 相同字节，仅 SHA-1 / identical, SHA-1 only | 未提供 SHA-256 的分支 / absent SHA-256 branch |
| 相同字节，仅 SHA-256 / identical, SHA-256 only | 未提供 SHA-1 的分支 / absent SHA-1 branch |

以上均须在有效许可证恢复后重新采样；旧失败夹具不重复使用。
Resample these cells with a valid reference license and a fresh owned fixture.

每次尝试独立创建或管理员铺设旧文件，再用指定身份 PUT；随后以管理员 GET 与存储元数据回读。拒绝后的 HTTP 可见缺席或旧字节保持，不自动证明物理 provider/registry 从未写入。原始请求、响应、私有地址和身份资料保持私有，仅发布原创结论。
Each attempt uses an independent baseline and subsequent byte/metadata read-back. HTTP-visible absence or retention does not prove zero physical provider/registry writes. Raw research remains private.

## BinFlow 后续接入约束 / Constraints for the next implementation

`UploadAuth` 的测试注入决定不等于可信生产授权；后续必须绑定真实 Principal、有效仓库、正确操作及原始摘要事实。认证后的 username 与 operator 的关系必须明确，不能接受任意客户端 actor。
Test decisions do not implement trusted authorization. The future boundary must bind verified principal, eligible repository, operation and request facts, including actor identity.

覆盖判断可能需要授权阶段的权威节点读取。因此“授权前无节点访问”不能被用来禁止必要的授权读取；应保留的是未准入时不发布正文、不修改节点。预读事实也不是提交时授权的替代品：并发覆盖、撤权与状态变化的检查时点尚未定案，不应借单 Saver 节点门宣称解决。
Overwrite authorization may require an authoritative node read. Separate such authorization reads from publication and mutation; a stale pre-read is not commit-time authorization. Concurrent overwrite, revocation and state-change semantics remain unresolved, and the existing saver gate does not establish them.

本轮不扩展 SQL schema、可信写端口或生产组合根，不开启 UAT 写入。后续最小实现提案须先冻结判断输入、读取一致性与失败顺序，再实施并用真实 PostgreSQL 验收。
This evidence slice does not change schema, trusted write APIs, process mounting or UAT. The next proposal must freeze decision facts, consistency and failure order before implementation.

## 普通上传权限预检 / Ordinary upload permission preflight

BIN-211 的 `security.CheckUploadPermissions` 接收已认证的主体、当前仓库目录、构造后的文件路径及原始请求摘要。输入与仓库绑定校验通过后，先查 deploy。deploy 拒绝直接结束；获准后读取目标并计算覆盖条件，仅在条件成立时查询 delete。取消、权限读取失败或目标读取失败都返回错误，调用方须先处理错误再使用布尔值。

BIN-211 checks deploy for a verified principal and the supplied local Generic repository and file path. Only deploy admission reaches the overwrite lookup, and only an overwrite match reaches the delete check. Cancellation and lookup failures return errors. Callers handle the error before using the boolean.

权限依赖由调用方提供。预检结论反映这些读取时的状态；提交前撤权及同路径并发写入仍需要单独的一致性边界。

The caller supplies the grant reader and node reader. The result reflects those reads. Revocation and concurrent writes to the same path still require a commit-time consistency boundary.

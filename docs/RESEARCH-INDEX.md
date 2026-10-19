# 研究索引 / Research provenance

此公开索引只保存可公开的身份、摘要、结论和限制。原版安装包、反编译、原始 API 采样与环境记录保存在私有资料库，不从站点提供下载。
This public index contains releasable identity, digests, findings and limitations. Original artifacts, decompilation and raw captures remain private.

## 目标基线 / Target baseline

2026-10-19 原版运行 API 返回 Artifactory **7.161.26**、revision **86126900**；授权返回 Enterprise Plus Trial。该观察仅证明样本身份与当时授权，不能证明所有功能已测试。
The observed runtime reports the target version/revision and trial entitlement. Identity is not feature-validation evidence.

| 字段 / Field | 值 / Value |
|---|---|
| Portable artifact ID | `baseline/artifactory-pro/7.161.26/linux-amd64/package` |
| Source | [JFrog official distribution](https://releases.jfrog.io/artifactory/artifactory-pro/org/artifactory/pro/jfrog-artifactory-pro/7.161.26/jfrog-artifactory-pro-7.161.26-linux.tar.gz) |
| Bytes | `2559888096` |
| SHA-256 | `b967a1fc263ec8cf0314194fc24daf99e10a130ca8e19b076c8a18faf02bc4d9` |
| Inventory | `57506` TAR members; logical member bytes `4143856880` |
| Product properties | version `7.161.26`; revision `86126900`; build `7.161.26-20260907150505-1` |

摘要由完整文件重新计算，TAR 目录逐成员扫描；没有执行包内程序。来源为官方地址，但仅本机摘要不构成独立签名验证。
The digest was computed over the full file and the TAR scanned member by member without executing its programs. A locally computed digest alone is not independent signature verification.

## 组件证据 / Component evidence

以下组件 ID 继承目标包 ID，私有 manifest 保存包内路径、大小、摘要和工具输出；跨主机通过 ID/摘要定位，不能仅复制绝对路径。
Component IDs inherit the package identity. Private manifests retain member paths, sizes, digests and tool output for portable resolution.

| Component ID suffix | 观察 / Observation | 限制 / Limit |
|---|---|---|
| `artifactory-war` | SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37`; 824 ZIP members | 清单/构建元数据，非执行行为 / inventory only |
| `access-war` | SHA-256 `dc871abf684789cf54977ed88ded2524493390c5b35ffa05b9893c391d692ca5`; 498 ZIP members | 清单/构建元数据，非执行行为 / inventory only |
| `router-go` | SHA-256 `0808170da829e6c4bc658cb5a6e0a9daf792ddaa3c630b9baf7b2336bf1f1b90`; ELF amd64, embedded `go1.26.7` | 嵌入字符串不证明工具链或依赖真实性 / embedded metadata only |
| `metadata-go` | ELF amd64, embedded `go1.26.6`, module version `(devel)` | 未恢复源码或函数行为 / no recovered source/behavior |

REA Router 证据 ID：`ev_8d5f8a6a49ade8c62588c355c2f0057a4b5942e347c0b0cab4bc9f75494ac836`。
REA Metadata 证据 ID：`ev_998440c643db3eb012cf6df0effd5eb5b28e8855ef23de2ec59cc5369297ee06`。

WAR manifest 的 `Build-Jdk-Spec` 均为 `25`。抽样 Artifactory 第一方 JAR 标记为 `7.161.15`，Access JAR 标记为 `7.191.16`。这些是组件版本，不能直接当作产品版本或据此断言包损坏。
WAR manifests report JDK 25. Sampled Artifactory and Access JARs use their own versions; component/product version layers must be kept distinct.

包内还存在 frontend、event、topology、jfconfig、jfconnect、evidence 等目录。存在某组件不等于 BinFlow 必须逐服务重建；按职责、调用与目标授权调查必需能力，映射到[能力矩阵 PLAT-001](CAPABILITY-MATRIX.md)。
Bundled presence does not establish implementation topology. Establish required behavior and dependencies before choosing service boundaries.

## 历史恢复资料 / Historical recovery

历史目录的根 README 声明 `7.161.24`，恢复 POM 为 `7.161.24-recovered`，另有内部标签 `7.161.14`。仅对根元数据文件计算了摘要，未宣称已校验整个历史树。
Historical root metadata reports these labels. Only selected root metadata files were hashed, not the full recovered tree.

这些资料是线索：缺少 `sourcesContent` 的前端 sourcemap 近似重建不等于可构建 TypeScript；Ghidra C 伪代码不等于恢复 Go 源码。多层版本标记需要归属核实，不能替代目标包和运行证据。
Approximate sourcemap reconstruction is not buildable TypeScript, and Ghidra pseudocode is not recovered Go source. Establish version-layer provenance and validate against the target.

## 证据规则 / Evidence rules

1. 私有 manifest 记录来源、获取时间、父制品、工具/版本、摘要、证据 ID 和限制；原始输出保持私有。
   Preserve provenance/tool identity/digests and limitations privately.
2. 分开标记静态事实、实际运行观察与推论；任何兼容结论都关联具体样本和测试。
   Separate static facts, runtime observations and inference.
3. 调查输出形成原创行为契约与边界测试；公开仓库不复制受控实现或原始采样。
   Produce original behavioral contracts and tests without publishing controlled implementations/captures.

[P0 计划](P0-PLAN.md)规定研究出口；后续完整依赖清单、前端/其他平台组件及行为采样仍需追加证据，当前索引不宣称逆向或兼容完成。
Further dependency/frontend/platform analysis and behavioral sampling remain open; this inventory is not completed reverse engineering or parity.

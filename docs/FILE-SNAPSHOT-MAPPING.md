# 文件快照元数据投影 / File-snapshot metadata projection

状态：已实现并完成本地验证，等待 PR 审阅及合并。本页记录七字段纯投影；完整保存分派及接线仍开放。
State: implemented with local verification complete, pending PR review and merge. This note describes the seven-field projection; full save dispatch and wiring remain open.

## 接口与用途 / Interface and purpose

```go
func (s FileInfoSnapshot) NodeMetadata() (FileMetadata, error)
```

`internal/repository` 内的转换桥接[原始文件快照](FILE-INFO-SNAPSHOT.md)与既有[节点元数据值](FILE-METADATA.md)。比较器继续读取原始完整状态，投影只提供写侧七个操作数；投影成功不表示上传或完整保存可接受。
This conversion bridges the raw snapshot and existing node metadata value. Comparison continues to read complete raw state; projection supplies seven write-side operands. Its success establishes neither upload acceptance nor full-save validity.

## 七字段规则 / Seven-field rules

| 输入 / Input | 输出 / Output | 规则 / Rule |
|---|---|---|
| CreatedMillis | CreatedMillis | int64 原样 / verbatim |
| LastModifiedMillis | ModifiedMillis | int64 原样 / verbatim |
| LastUpdatedMillis | UpdatedMillis | int64 原样 / verbatim |
| CreatedByUTF16 | CreatedBy | 既有 NewActor / existing NewActor |
| ModifiedByUTF16 | ModifiedBy | 既有 NewActor / existing NewActor |
| SHA1 条目的 Original / entry original | SHA1OriginalUTF16 | 逐码元复制；缺席为 nil / raw units; nil when absent |
| MD5 条目的 Original / entry original | MD5OriginalUTF16 | 逐码元复制；缺席为 nil / raw units; nil when absent |

三个时间独立保留负数、零与完整 int64 范围，无时钟、回退、排序或 created 比较开关。两 actor 按既有规则保留 null/空、≤64 码元原样，>64 保留首 61 码元加三点；不做数据库编码。
Times retain independent negative, zero and full int64 values, with no clock, fallback, ordering or comparison flag. Existing Actor rules preserve null/empty, retain up to 64 units, and abbreviate longer values to 61 units plus dots; database encoding is separate.

original 保留 null、非 null 空串、`NO_ORIG`、大小写、任意长度、NUL 与孤立代理，不缩写、不改写。条目存在时，其 Actual 为 null/空/空白也不抑制 Original。使用的是目标 `getOriginalOrNoOrig` 的 raw 字段语义，不是标记场景下以 Actual 替代的 `getOriginal` 语义。
Originals preserve nullable raw UTF-16, markers, case, arbitrary length, NUL and lone surrogates without abbreviation or rewriting. A null, empty or blank actual never suppresses a present entry's original. This follows raw getOriginalOrNoOrig semantics rather than getOriginal's marker substitution.

## 本地策略与目标成功域 / Local policy and target-success domain

无效零快照返回零 FileMetadata 与既有 `ErrInvalidFileInfoSnapshot`。每个有效快照都可投影，包括空校验和图；缺席条目的 original 为 nil。缺席 MD5 与原版成功映射一致；缺席 SHA1 是明确批准的本地投影扩展。
An invalid zero snapshot returns zero FileMetadata and the existing error. Every valid snapshot projects, including an empty checksum map; missing entries yield nil originals. Absent MD5 matches successful target mapping; absent SHA1 is an explicitly approved local projection extension.

Artifactory 7.161.26 rev 86126900 的完整 mapper 对缺失/空白 SHA1 Actual 抛异常，并涉及 ID、路径、actuals、size 和 Node 构建。因此，只在完整 mapper 实际成功的域内对照这七个输出；域外投影不宣称原版保存成功。原版来源与更大范围见[保存分派契约](FILE-SAVE-DISPATCH-CONTRACT.md)及[文件节点保存契约](FILE-NODE-SAVE-CONTRACT.md)。
The full target mapper rejects absent/blank SHA1 actuals and also handles identity, actuals, size and Node construction. Seven-output parity is therefore limited to its actual successful domain. Projection outside that domain makes no save-success claim; the linked contracts own the broader evidence.

## 所有权与分层 / Ownership and layering

接收者不变；返回值复用 NewActor/NewFileMetadata 的不可变语义与防御性读取。调用方可修改独立构造输入、Fields 或元数据 getter 返回的切片，而不改变已有快照/元数据值；不规定私有只读存储是否共享。
The receiver stays unchanged. Returned values retain the existing immutable ownership and defensive accessors; mutating constructor inputs or returned copies changes no existing value. Private read-only storage sharing is not a contract.

SHA256 Original、完整 actor 尾部和 MIME 可影响快照比较，却不进入这七列；投影相等不能代替 `IsIdentical`。RepoKey/path/name、size、Actual 不参与投影，不因此免除未来完整保存的检查。旧行七列全 NULL 的 Metadata=nil 不由本方法重建或填零。
SHA256 originals, actor tails and MIME may affect comparison without entering these seven columns. Projection equality cannot replace IsIdentical. Excluded fields remain subject to future save checks, and legacy all-NULL metadata is neither reconstructed nor filled.

## 验证与范围 / Verification and scope

真实类参考在 34 组输入上得到 28 个 Node 与 6 次缺失/空白 SHA1 拒绝；只有成功行参与目标对照。13 个实际 JAR 与固定 WAR 条目一致，14 个类摘要已核对。输入前后比较只覆盖导出的捕获字段，不宣称完整对象图全部未变。
Exact-class reference execution returned 28 Nodes and six absent/blank-SHA1 rejections across 34 inputs. Only successes supply target-output comparisons. Thirteen actual JARs match the pinned WAR and 14 class digests were checked. Before/after equality covers exported captured fields, not the complete object graph.

独立 Go 驱动逐字段核对 28 组真实成功参考（196 项），并核对全部 34 组本地策略用例（238 项）；无效零快照的错误与零输出另列本地断言。行为 RED/GREEN、复制隔离、race 与三种可编译故障回归通过：误用 Actual 替代 Original、漏缩写 actor、混入 SHA256 Original。
An independent Go driver matches all 28 successful references (196 fields) and all 34 local-policy fixtures (238 fields). Invalid-zero error/output assertions are local policy, counted separately. Behavioral RED/GREEN, ownership, race and three compiling faults pass: actual substitution, omitted actor abbreviation and SHA256-original contamination.

`./scripts/check.sh` 通过；每项 PR 仍须通过 Check 与真实 PostgreSQL CI。本片没有新增本地数据库探针，也未重跑已有大语料。
The shared script passes; each PR still requires Check and real-PostgreSQL CI. This slice adds no local database probe or rerun of existing large corpora.

本片不改变 SQL/schema、FileReference、现有创建/更新或比较器，不新增 provider、完整分派器、HTTP/权限或发布。工程说明维护在本仓库，Fern 继续只放使用文档。
This slice changes no persistence model, existing save helpers or comparator, and adds no providers, full dispatcher, HTTP/auth or release. Engineering notes remain in this repository; Fern stays usage-only.

# 已有文件快照更新 / Existing-file snapshot update

状态：已实施，等待 PR 审阅及合并。核心、补充单元边界与数据库回归按独立提交审阅；验证记录以 PR 为准。
State: implemented, pending PR review and merge. The core and supplemental boundary/database regressions are reviewed in separate changes; consult their PR checks.

## API

```go
type FileDirtyUpdateInput struct {
    Existing      FileInfoSnapshot
    Desired       FileInfoSnapshot
    IgnoreCreated bool
    ID            int64
}
type FileDirtyUpdateResult struct {
    Changed bool
    Count   int64
}
func UpdateFileIfChanged(ctx context.Context, in FileDirtyUpdateInput,
    update func(context.Context, FileReference) (int64, error),
) (FileDirtyUpdateResult, error)
var ErrUnrepresentableFileSnapshot error
```

输入不接受额外的 Content、路径或 Metadata DTO：全部写入操作数派生自同一个 Desired，比较与写入只有一个事实来源；Existing/Desired 是调用方显式拥有的完整[原始快照](FILE-INFO-SNAPSHOT.md)状态，产品不从 Lookup 返回值猜造它们，调用方负责 ID 与状态来源对应。
No extra Content, path or metadata DTO enters the input: every write operand derives from the same Desired, so comparison and writes share one source of truth; Existing/Desired are caller-owned complete raw snapshots and the product never reconstructs them from Lookup results, while the caller owns the ID-to-state correspondence.

## 固定顺序 / Fixed order

1. `ctx` 非 nil 由调用方保证；预取消返回包裹的 context 错误与零结果，零委托调用 / a nonnil `ctx` is the caller's prerequisite; pre-cancellation returns the wrapped context error and the zero result with zero delegate calls.
2. `nil` 委托返回固定私有配置错误，先于 ID/快照检查 / a nil delegate is a fixed private configuration error checked before the ID/snapshot checks.
3. `ID <= 0` 返回包裹的既有 `ErrInvalidUpdateID`（[FILE-UPDATE.md](FILE-UPDATE.md)）；这是本层明确的门前守卫，顺序区别于 `UpdateFile` 原有的路径先于 ID，不改变原 helper / a nonpositive ID returns the existing `ErrInvalidUpdateID` wrapped; this explicit pre-gate guard deliberately differs from `UpdateFile`'s path-before-ID order and changes no existing helper.
4. `Desired.IsIdentical(Existing, IgnoreCreated)`：任一无效零快照返回既有 `ErrInvalidFileInfoSnapshot`；相同立即返回 `{false,0},nil`，不做后续映射、校验、委托或追加取消检查 / the raw comparator runs first; an invalid zero operand returns the existing `ErrInvalidFileInfoSnapshot`, while identical states return `{false,0},nil` immediately with no later mapping, validation, delegate call or appended cancellation check.
5. 仅变化分支依次无损转换 Desired 的 repoKey、repoPath、SHA1 Actual、SHA256 Actual、MD5 Actual：UTF-16 高/低代理必须成对，合法代理对保持原 Unicode 标量，字面 U+FFFD 保持原值；孤立/反序代理返回 `ErrUnrepresentableFileSnapshot` 且零委托调用，禁止静默替换为 U+FFFD 或问号 / on the changed branch only, lossless conversion of Desired's repoKey, repoPath and the three Actuals in that order: high/low surrogates must pair, valid pairs keep their Unicode scalars and a literal U+FFFD stays verbatim; lone/reversed surrogates return `ErrUnrepresentableFileSnapshot` with zero delegate calls and silent U+FFFD/question-mark substitution is forbidden.
6. Actual 的缺席条目、present-null、present-empty 均转换为 Go 空串——这只是既有 string DTO 中的显式本地投影，发生在原始比较之后，原始比较仍区分三者；非空值、空白、大小写与 NUL 原样转换，不 trim/lowercase、不补摘要、不检查 canonical 语法；size 原样 int64（含负数）/ absent entry, present-null and present-empty Actuals all convert to the Go empty string — an explicit local projection inside the existing string DTO that happens only after the raw gate (the raw comparison still distinguishes the three); nonempty values, blanks, case and NUL convert verbatim with no trim/lowercase, digest completion or canonical-grammar check, and size passes through as int64 including negatives.
7. `Desired.NodeMetadata()`（[FILE-SNAPSHOT-MAPPING.md](FILE-SNAPSHOT-MAPPING.md)）生成显式非 nil Metadata；调用一次既有 `UpdateFile` 并把其委托接到调用方提供的 update：已有路径校验、内部取消检查与 Store 的 canonical/SQL 守卫继续由原层执行；因路径失败委托可能为零次，到达委托时至多一次 / `Desired.NodeMetadata()` builds the explicit nonnil Metadata and one existing `UpdateFile` call wires its delegate to the caller-provided update: the existing path validation, internal cancellation checks and the Store's canonical/SQL guards keep running in their original layers; a path failure can leave zero delegate calls and at most one happens once reached.
8. 失败返回零结果并保留 errors.Is/As 原因链；成功返回 `{true,count},nil`，计数 0/其他值不重解释、不转创建；成功后无追加取消检查 / failures return the zero result keeping the errors.Is/As cause chain; success returns `{true,count},nil` with counts 0 and any other value passed through unreinterpreted and never converted to a create; no cancellation check is appended after success.

阶段文案固定且不含输入；取消发生在转换期间由 `UpdateFile` 观察（若转换成功），初始检查点之后到达的取消可被先到的转换错误胜出 / stage texts are fixed and input-free; cancellation during conversion is observed by `UpdateFile` when conversion succeeds, and a conversion error can win over a cancellation arriving after the initial checkpoint.

## 结果语义 / Result semantics

`Changed` 仅在调用成功时解释：false=原始比较相同而跳过节点写；true=已执行更新分支，`Count` 保留委托计数（含 0）。任何错误返回零结果。调用方绝不由计数推断 `Changed`——计数 0 与 `Changed=true` 并存是合法组合（委托真实执行但零行命中）。
Interpret `Changed` only on success: false means the raw comparison found identical states and skipped the node write; true means the update branch executed with `Count` preserving the delegate count (0 included). Every error returns the zero result. Callers never infer `Changed` from the count — count 0 together with `Changed=true` is a legal combination (the delegate really ran and hit zero rows).

## 路径与存储画像 / Path and storage profile

Desired 的独立缓存 name 只参与原始比较；写入名字继续从 RepoPath 经既有 helper 导出：重构、trim、force-file 与单次元数据后缀剥离仍会发生（如 `f:properties:statistics` 映射为 `f:properties`）。它们不是永远保持原样的已映射身份，不声称所有人工捕获 UTF-16 RepoPath 与目标 mapper 直接等价，也不构成 move/rename 兼容承诺。
The independent cached name affects comparison only; the written name keeps deriving from RepoPath through the existing helper: reconstruction, trim, force-file and the single metadata-suffix strip still happen (for example `f:properties:statistics` maps to `f:properties`). These are not identity-preserving round-trips for every captured state, direct target-mapper parity is not claimed for every hand-captured UTF-16 RepoPath, and no move/rename compatibility follows.

本地 Store 画像严格于目标 mapper：Store 要求三份规范摘要与非负 size；目标 mapper 在 SHA1 摘要门上仅要求 Actual 存在且非 blank，并允许可选 MD5/SHA256。此差异保持不动：变化但摘要缺失/非规范的状态原样到达委托，再由真实 Store 拒绝；即使值来自 binary.Service.Add 也不能假设它通过节点 Store 画像（Registry 可能返回已有非规范记录）；原始状态完全相同时不得提前跑 Store 校验制造新错误。
The local Store profile stays stricter than the target mapper: the Store demands three canonical digests and a non-negative size, while the target mapper's SHA1 digest gate only requires a present nonblank Actual with optional MD5/SHA256. The difference is deliberately kept: changed states with missing/noncanonical digests reach the delegate unchanged and the real Store rejects them; even values from binary.Service.Add are not assumed to satisfy the node Store profile (the Registry may return an existing noncanonical record), and fully identical raw states never run Store validation early to invent new errors.

## 验证 / Verification

独立公共 API 对照 20 场景通过，三种可编译故障均被捕获；三项真实 PostgreSQL 接缝通过，临时资源已清理。单元/race/共享检查及每项 PR 的 Check、真实 PostgreSQL CI 仍以最终提交记录为准。
Twenty independent public-API scenarios and three compiling-fault checks pass. Three real-PostgreSQL seams pass and their disposable resources are cleaned. Final commit records own unit/race/shared and per-PR CI results.


- 单元（BIN-174 所有）：预取消/nil 委托/非正 ID/无效操作数固定优先顺序与零调用；identical 跳过（含有效空图与不可转换身份、created 两开关）；投影相同但原始不同的尾部触发；委托操作数全派生自 Desired 的捕获验证；UTF-16 转换与错误文案；真实委托计数直通与取消后成功不反转。
  Unit (owned by BIN-174): the fixed priority order with zero calls for pre-cancellation/nil delegate/nonpositive ID/invalid operands; identical skips (including valid empty maps, unconvertible identities and both created switches); raw-different/projected-equal tails still update; captured-delegate verification that every operand derives from Desired; UTF-16 conversion and fixed error texts; real-delegate count passthrough with no post-success cancellation flip.
- 数据库回归（BIN-175 草案，root/CI 真库运行）：no-op 相同真实快照委托恰零次（缝上证明）且独立 SQL 全行不变；全字段互异的 dirty 提交后独立 SELECT 精确核对新身份/内容/七列；dirty 临时成功后调用方回滚保留完整旧行且无隐式提交、号段与对象不动；缺席正 ID 计数 0 原样直通且 `Changed=true`。
  DB regression (a separate regression slice): a no-op over identical authentic snapshots proves zero delegate calls at the seam with the independent-SQL full row unchanged; a deliberately field-distinct dirty commit verifies the exact derived identity/content/seven columns through an independent SELECT after commit; a dirty provisional success followed by the caller rollback preserves the full old row with no implicit commit and untouched reservations/objects; an absent positive ID passes count 0 verbatim with `Changed=true`.
- 本片不新增目标探针：静态锚点（DbMutableItem.saveUpdate pc0–20/24–68、DbMutableFile.doUpdateNode pc0–20、FileServiceImpl.fileInfoToNode）沿用 BIN-172 冻结提案的既有静态/既往运行证据，静态顺序不证明有效事务行为；不重跑大语料。
  No new target probes in this slice: the static anchors reuse the frozen BIN-172 proposal's existing static/prior-runtime evidence, static order proves no effective transaction behavior, and no large corpus is rerun.

## 范围与非目标 / Scope and non-goals

事务归调用方：无 begin/commit/rollback、重试、补偿或 CAS；before 比较不保证数据库行未被其他写者改变。节点 skip 不代表整个保存 skip：属性/关注/事件/stats 仍未表示。无 Lookup/create 分派、加载快照重建、时钟/MIME/actor/config 供给、锁/会话/祖先、SQL/schema、HTTP/权限、发布或部署，也不实现完整保存兼容。
Transactions stay caller-owned: no begin/commit/rollback, retry, compensation or CAS, and the before-comparison guarantees nothing about concurrent writers. A node skip is never a whole-save skip: properties/watches/events/stats stay unrepresented. There is no Lookup/create dispatch, loaded-snapshot reconstruction, clock/MIME/actor/config supply, locks/sessions/ancestors, SQL/schema, HTTP/auth or release work, and no full-save parity.

## 相关 / Related

[FILE-INFO-SNAPSHOT.md](FILE-INFO-SNAPSHOT.md)（原始快照与比较器）、
[FILE-SNAPSHOT-MAPPING.md](FILE-SNAPSHOT-MAPPING.md)（七字段投影）、
[FILE-UPDATE.md](FILE-UPDATE.md)（被消费的更新核心）、
[FILE-METADATA-PERSISTENCE.md](FILE-METADATA-PERSISTENCE.md)（七列 SQL 边界）、
[FILE-SAVE-DISPATCH-CONTRACT.md](FILE-SAVE-DISPATCH-CONTRACT.md)（分派契约）、
[NODE-REFERENCES.md](NODE-REFERENCES.md)（FileReference 与 Store）。

[快照读取来源](FILE-SNAPSHOT-SOURCE.md)为未来加载组装保留 Actual 空性，仍不直接生成本门需要的 Existing。
The read source preserves nullness for future loaded-state assembly; it does not yet construct this gate's Existing operand.

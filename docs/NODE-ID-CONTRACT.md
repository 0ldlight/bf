# 节点 ID 分配契约 / Node ID allocation contract

状态 / State: BIN-134 调查切片；**尚未实现 ID 分配器或保存服务**。本页记录静态事实、受控回放与下一实现边界，不批准新的运行拓扑。
Research slice only; **no allocator or save service is implemented**. This page records static facts, controlled replay and the next implementation boundary, without approving a new runtime topology.

## 目标与证据 / Target and evidence

目标为 Artifactory 7.161.26 revision 86126900，父 WAR SHA-256 `9c2c1883d39b9e60587c09d1f1f8cbf1215c9ed2751d0ee817f78cd3cc8c9f37`，来源见[研究索引](RESEARCH-INDEX.md)。以下组件从同一 WAR 提取并逐字节校验：
Target: Artifactory 7.161.26 revision 86126900. Parent provenance is in the [research index](RESEARCH-INDEX.md). These components were extracted from the same WAR and hashed independently:

| 组件 / Component | SHA-256 |
|---|---|
| artifactory-storage-db 7.161.15 | `7687e761d65c1c48819c9dc51ca9478bc31965402c528aae914878052c8164e9` |
| artifactory-addon-ha 7.161.15 | `710e0fe465ce6303c4857f013b40b6f92624016513dd6cd4a7e4e447eb7768bd` |
| artifactory-storage-common 7.161.15 | `7c157b1fb2be2b6f0b7f99eb8edd34ff0ac46bff2be02f6da1e7cddd8b50dc1e` |
| artifactory-config 7.161.15 | `48f5c3d26086b357637224ab7d1bc5a6b4718b7d9615af5acde80c406f9531ea` |

REA 验证选定 class/装配资源身份，`javap -c -p -v` 检查对应方法字节码；历史恢复源码仅为线索。REA 清单不证明 JVM 语义、Spring 装配或原版数据库运行行为。原始字节码、清单、回放源码和完整证据留在私有资料库。
REA verifies selected class/resource identities; javap examines method bytecode. Historical recovery is a lead only. Inventory establishes neither JVM semantics, effective Spring wiring nor live database behavior. Raw bytecode, inventories and replay material remain private.

## 静态分配链 / Static allocation chain

- `DbServiceImpl.nextId()` 委托 `IdGenerator`，共用 `unique_ids` 中的 `general` 行；它不是各节点表的自增列，也不能用 `MAX(node_id)+1` 代替。
  The service delegates to `IdGenerator`, sharing the general row; this is neither per-node identity nor a maximum-ID scan.
- 初始化读取预留上界，缺行时写入零；步长必须为正。打包默认 `db.idGenerator.fetch.amount=2000`、`db.idGenerator.max.update.retries=50`，**实际实例覆盖值未查询**。
  Initialization reads the reserved bound and inserts zero when absent. Step must be positive. Packaged defaults are 2000 IDs and 50 reservation attempts; **effective instance overrides were not queried**.
- `SimpleIdGenerator` 用本地原子计数和对象内互斥，耗尽时按本地上界推进持久化上界，更新条件不比较旧上界；重启跳过已预留区间。不能据此推断多实例唯一性。
  Simple uses a local atomic counter and instance monitor, advancing the stored bound without comparing its old value. Restart skips the reserved interval; uniqueness across independent instances is not established.
- `HaIdGenerator` 从空本地号段开始：以旧上界为条件竞争更新，影响一行才取得新区间；竞争失败则重读上界并有界重试。SQL 异常是另一失败路径。区间含端点，从旧上界加一到新上界；耗尽后再申请。
  HA starts with an empty local range. A conditional old-bound update claims a range only when one row changes. Conflicts reload and retry within a configured limit; SQL exceptions follow a separate failure path. Inclusive ranges run from old bound plus one through new bound.
- HA 组件打包装配资源把该分配器声明为 primary；工厂提供独立 `uniqueIdsDataSource`，包含 bean/JNDI 的 `noTX` 路径和默认池工厂。**实际选中的分配器、连接 autocommit 与提交确认语义未观察**。
  The HA package declares its allocator primary. The factory supplies a separate ID data source, including bean/JNDI noTX routes and a default pool factory. **Effective selection, autocommit and commit acknowledgement remain unobserved**.
- `CreateDbSpecificSequence` 的 `node_events_event_id_seq` 属于节点事件，不能当作 `DbService.nextId()` 的通用实现。
  The packaged event sequence concerns node events; it is not evidence of a general node-ID sequence.

## 保存调用边界 / Save-call boundary

`FileServiceImpl.createFile` 先验证路径，再申请 ID；普通路径随后插入节点。条件插入是另一分支，其辅助方法会再次申请 ID。尝试次数不等于保存行数，ID 无空洞不是契约；更新现有节点沿用既有 ID。
The file service validates the path before requesting an ID and following the ordinary insert path. The separate conditional-insert helper requests another ID. Attempts need not equal saved rows, gap-free numbering is not a contract, and updates retain existing IDs.

[文件引用适配器](NODE-REFERENCES.md)仍由调用方提供 ID 和事务；本调查不改变其操作。[二进制写入](BINARY-WRITE.md)仍先发布内容再独立登记，不能由此新增节点失败时的物理删除、盲目重试或补偿。
The [reference adapter](NODE-REFERENCES.md) still receives IDs and transactions from its caller. [Binary writing](BINARY-WRITE.md) still publishes content before independent registration; this research introduces no physical deletion, blind retry or compensation.

## 受控回放 / Controlled replay

OpenJDK 26.0.2.1 直接加载目标 JAR 的精确分配器 class；配置、日志、存储异常/辅助类型和 JDBC 使用显式替身；回放直接初始化分配器的局部状态，未运行完整初始化和配置装配。步长设为 5，故障测试重试上限 3，并发测试为 50。13 项观察覆盖首个正 ID、缓存与耗尽、重启空洞、Simple 多实例重复、HA 竞争重读/持久化上界重读、缺行耗尽、SQL 失败，以及 8 个实例同步起跑分配 40 个唯一 ID。
Exact allocator class bytes ran under OpenJDK 26.0.2.1 with explicit configuration/logging/storage-exception/helper/JDBC doubles and direct local-state initialization (not the full initialization/wiring path): step 5, attempt limit 3 for faults and 50 for concurrency. Thirteen observations cover initial/cached/exhausted IDs, restart gaps, Simple duplicates across instances, HA reloads, missing-row exhaustion, SQL failure and 40 unique IDs from eight barrier-released instances.

这不是完整应用、真实 PostgreSQL、Spring 事务或 HA 故障测试；网络断开、提交结果不明、连接池耗尽、整型溢出、配置热变更和并发重启的目标行为仍未知。
This is neither application startup, real PostgreSQL, Spring transaction nor HA failure testing. Target behavior under network loss, ambiguous commit, pool exhaustion, integer overflow, live reconfiguration and concurrent restarts remains unknown.

## 下一实现切片 / Next implementation slice

先审阅持久化号段预留契约：明确数据库所有权、初始化竞争、条件更新、重读、取消、失败和上界溢出；再为真实 ID 消费者定义最小接口，SQL 留在适配器，避免预建跨领域框架。
First review durable range reservation: database ownership, initialization races, conditional update, reload, cancellation, failure and overflow. Then define the smallest interface at a real consumer boundary, keeping SQL in its adapter and avoiding a speculative framework.

验收至少覆盖一次性真实数据库中的独立事务/独立进程竞争、区间不重叠、重启不复用、失败不返回未确认号段、内容与引用保留及有界取消。提交结果不明时不得盲目重放旧号段。完整保存编排、PathValidator、节点元数据、权限和 Generic HTTP 继续按独立切片推进。
Acceptance requires independent transactions/processes against an owned disposable database, non-overlapping ranges, no reuse after restart, no IDs from unconfirmed reservations, retained content/references and bounded cancellation. Ambiguous commit must not blindly replay an old range. Full save orchestration, path validation, node metadata, authorization and Generic HTTP remain separate slices.

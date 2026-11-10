# 普通 PUT 提交边界 / Ordinary PUT commit boundary

BIN-213 定义 P0 的保守 PostgreSQL 方案，并在独立数据库连接上验证锁行为。当前预检负责在正文发布前尽早拒绝，节点事务负责重新确认提交所需的权限与目标状态。

## 顺序 / Ordering

1. 请求入口验证身份、目录、构造后的文件路径及原始请求摘要，执行现有权限预检。保留同一组可信事实供提交阶段复用。
2. 流式发布、摘要校验与登记继续先于节点事务。权限在此期间撤销时，提交阶段仍须拒绝节点写入；已发布且未引用的正文沿用既有保留策略。
3. 在既有有界节点阶段开启一个 READ COMMITTED 事务。依次获取 `security_users`、`security_repositories`、`security_group_members` 和 `security_write_grants` 的 SHARE 锁，再获取 `nodes` 的 SHARE ROW EXCLUSIVE 锁。所有参与流程遵循同一锁顺序。锁等待计入节点阶段预算。
4. 锁获取后，在该事务内重新检查主体、仓库版本、deploy、文件存在性和实际摘要，命中覆盖条件时再检查 delete。随后以同一事务执行保存、回读与提交。拒绝、查询失败、锁超时或取消走现有回滚清理；不自动重试。数据库授权查询必须使用该事务的连接，不能调用当前使用独立连接的 `security.Store` 方法。

The early check gates publication. After streaming and registration, the bounded node transaction takes SHARE locks on authorization relations and a SHARE ROW EXCLUSIVE lock on nodes, in the fixed order above. Fresh checks and the node save then use that same READ COMMITTED transaction through commit.

## 一致性与代价 / Guarantees and trade-offs

SHARE 与修改权限事实的事务冲突。若撤权先提交，锁后的查询看到新事实；若上传先获得锁，撤权等待上传结束。节点锁与其他节点写入冲突，也保护尚无行的路径。普通读取可以继续，所有仓库的节点写入会串行化。现有进程内 gate 不提供这个跨连接边界。

SHARE blocks concurrent changes to the authorization facts. The nodes lock also blocks inserts into a previously absent path. The first transaction to obtain the relevant locks determines the order. Ordinary reads can continue, while node writes across repositories serialize.

此方案的测试范围是锁模式、获取顺序及释放后可写。产品接入须验证真实角色能够取得所需锁，且不为此给服务角色增加修改授权表的权限。若部署角色仅有 SELECT，须设计并审查最小权限的数据库入口，再决定是否采用此方案。接入验收还需覆盖撤权先提交、同路径先创建、锁等待取消、拒绝零节点修改，以及分离连接池的资源预算。

The regression covers database lock behavior. Production integration requires a separately reviewed privilege boundary and end-to-end races for revocation and same-path creation.

- [PostgreSQL 17 LOCK](https://www.postgresql.org/docs/17/sql-lock.html)
- [PostgreSQL 17 explicit locking](https://www.postgresql.org/docs/17/explicit-locking.html)

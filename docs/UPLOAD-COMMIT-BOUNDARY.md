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

回归验证持锁后的冲突关系和释放后可写。获取顺序由上面的事务约定和 PostgreSQL 的 LOCK TABLE 规则确定。产品接入须验证真实角色能够取得所需锁，且不为此给服务角色增加修改授权表的权限。若部署角色仅有 SELECT，须设计并审查最小权限的数据库入口，再决定是否采用此方案。接入验收还需覆盖撤权先提交、同路径先创建、锁等待取消、拒绝零节点修改，以及分离连接池的资源预算。

The regression covers database lock behavior. Production integration requires a separately reviewed privilege boundary and end-to-end races for revocation and same-path creation.

## 锁入口的权限 / Lock entry privileges

BIN-214 提供 `security.InstallUploadCommitLock`。在授权表和节点表建好后，以专用、不可登录的函数所有者，在运维事务中安装 `<schema>.lock_upload_commit()`。所有者须能在可信 schema 创建函数，并在固定五张表上取得所需锁。PostgreSQL 17 的 `MAINTAIN` 可授给所有者；上传角色只需该函数的 `EXECUTE` 和 schema 的 `USAGE` 即可经入口取锁。业务读写权限另行配置。

函数无参数，使用固定 schema 限定所有表，固定 `search_path`，在同一事务撤销 `PUBLIC` 的执行权限并授给指定角色。运维须确保函数所有者没有向其他角色开放执行权的默认 ACL，且上传角色不能修改该 schema 或切换到所有者。上传事务须在重查前调用该函数，并限制事务时长。真实数据库回归验证上传角色可经入口取锁、无法直接取授权表的 SHARE 锁或修改授权，以及旁路角色无法执行。

The installer uses an operator transaction and a dedicated function owner. The zero-argument function locks only the qualified relations in its target schema. Runtime callers need schema USAGE and function EXECUTE for this operation. Keep the owner and upload role separate, audit the owner's default function ACLs, and bound the transaction lifetime.

## 保存器接入 / Saver integration

BIN-215 通过 `UploadSaver.SaveAuthorized` 接入提交复查。构造时显式设置 `CommitSchema` 并提供独立、有界的 `ReservationDB`，两条连接池及正文登记均须指向同一可信 schema。配置此模式后，普通 `Save` 入口会拒绝调用。调用方先完成预检，保存器从认证身份派生 actor，使用同一输入摘要在节点事务中复查。

The authorized saver requires a separate bounded ID-reservation pool so a node lock waiter cannot consume its connections. The node transaction checks its schema and isolation, takes the locks, and reuses that transaction for the permission query and node save. Denial rolls back the node transaction. Published bytes and registry entries follow the existing retention policy.

BIN-216 的独立 HTTP 处理器将本次认证身份、目录事实和已解析摘要传给预检与受保护保存。认证失败返回 401，预检或提交复查拒绝返回 403。若同时发生回滚故障，返回 500。应用进程的路由启用仍另行验收。

The standalone authenticated handler uses the same parsed digests for preflight and save. A commit denial returns 403 unless a persistence or rollback error takes precedence.

## 应用进程启用 / Process opt-in

BIN-217 使用 `BINFLOW_MODE=artifacts` 显式启用 PUT。私有配置文件仍通过 `BINFLOW_DOWNLOAD_CONFIG` 指定，在既有下载字段之外必须包含 `upload_writer_dsn`、`upload_reservation_dsn`、`upload_public_origin` 和 `upload_max_body_bytes`（1 至 1 GiB）。三个 DSN 使用同一主机、端口、数据库和显式可信 schema，分别使用下载、写入、号段角色。启动时以写入角色验证提交锁及表权限，以号段角色验证现有 general 号段。应用启动不创建表、函数、用户或授权。PUT 的相同摘要保护开关采用 false。

The opt-in artifacts mode dispatches raw artifact PUT requests to authenticated upload and retains the existing download and health routes. It checks the writer and reservation dependencies before binding the loopback listener. Request headers retain their five-second limit and complete request reads have a one-minute limit in this mode. Install the write-grant extension and lock function separately before activation.

Artifacts 模式的总启动预算为 60 秒，覆盖三条独立连接的 TLS、目录和权限核实。运行期安全查询和节点阶段仍各自受 10 秒预算约束。已有 health/download 模式沿用原启动预算。UAT 的逐步探针完成初始化约需 16 秒；表级 GRANT 还须与运行角色的 RLS 策略同时匹配，才能读取安全版本并写入节点。

Artifacts startup has a 60-second overall deadline for its three connections and initialization checks. Runtime security-query and node-stage deadlines remain ten seconds. Runtime grants and row-level policies must both admit the intended database roles.

- [PostgreSQL 17 LOCK](https://www.postgresql.org/docs/17/sql-lock.html)
- [PostgreSQL 17 explicit locking](https://www.postgresql.org/docs/17/explicit-locking.html)
- [PostgreSQL 17 security definer guidance](https://www.postgresql.org/docs/17/sql-createfunction.html#SQL-CREATEFUNCTION-SECURITY)

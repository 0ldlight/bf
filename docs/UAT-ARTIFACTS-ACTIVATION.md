# UAT 普通 PUT 启用 / UAT ordinary PUT activation

## 基线

确认 Supabase 项目可用，现有 IP HTTPS 的健康检查和受控 GET/HEAD 均通过。保存当前发布标识、下载模式配置代号和 systemd drop-in 的回退副本。确认目标 schema 私有且未列入 Data API 暴露范围。连接地址从该项目的 Connect 面板核实，三条运行时 DSN 使用同一端点、数据库和显式 schema。若继续使用已验收的事务池，三个 DSN 都使用 `default_query_exec_mode=exec`，并分别验证实际生效的数据库角色、schema、TLS 与事务可写性。凭据仅进入主机上的私有配置和已有 Secret 设施。

## 数据库准备

- 在运维事务中为现有私有 schema 增加仓库级写权限表。使用 [write_grants.sql](../internal/security/write_grants.sql)，初始为空，不复用下载权限。校验普通匿名角色无法访问该表或执行锁函数。
- 核实 `unique_ids.general`。若表缺失，应用 [号段 schema](../internal/repository/postgres/id_reservations.sql)，在同一事务将上界抬到至少现有最大节点 ID。先取得阻止节点并发写入的锁再核对最大值。若已有号段，仅在运维确认后提升上界，绝不降低已提交上界。启用前确认仍有可分配的正 ID。
- 由专用 NOLOGIN 所有者安装固定提交锁函数。授予该所有者固定表的取锁权限，按 [提交边界](UPLOAD-COMMIT-BOUNDARY.md) 调用 `security.InstallUploadCommitLock`，并核对函数所有者和执行 ACL。上传数据库角色只经函数取锁。
- 写入角色拥有认证及授权查询所需的 SELECT、nodes 的 SELECT/INSERT/UPDATE、binaries 的 SELECT/INSERT 和 sha256 列 UPDATE，以及函数 EXECUTE。号段角色拥有 unique_ids 的 SELECT 和表级 UPDATE（当前启动校验要求）。现有下载身份继续只读。新数据库角色的凭据与应用 Basic 用户分别管理。
- 为验收建立独立的应用身份和专用制品路径，显式授予 deploy。仅在覆盖测试阶段临时授予 delete。每步验证当前权限及失败响应。

## 主机与发布

1. 由 Jenkins 构建已通过真实 PostgreSQL CI 的提交，记录提交、制品摘要和来源。CircleCI 只执行 UAT 发布，并核对相同摘要。
2. 在主机上预先确认制品根及 staging 的所有权、权限、可用空间和同文件系统条件。在保留 `ProtectSystem=strict` 的前提下，仅对已核实的制品根开放 `ReadWritePaths`，发布树和配置继续只读。由服务用户验证创建、发布及清理能力。
3. 生成下一代私有 JSON，保留下载字段并追加四个 [上传字段](UPLOAD-COMMIT-BOUNDARY.md#应用进程启用--process-opt-in)。切换 drop-in 到 `BINFLOW_MODE=artifacts`，保留回环监听和现有 IP HTTPS 入口。启动时确认锁、号段和运行角色检查通过，配置和日志不输出 DSN。
4. 通过公开 IP HTTPS 验收，依次检查未认证 401、无 deploy 403、正常 PUT 201、GET 字节及摘要一致、相同摘要重传 201、无 delete 的不同内容覆盖 403、授权覆盖 201、撤权后 403。用独立读取确认拒绝保留旧制品。记录受控制品及清理归属。

## 回退

任何启动、健康或权限验收失败时，恢复上一代下载配置和 drop-in，通过既有发布恢复流程切回已知可用制品。验证 HTTPS 健康、受控 GET/HEAD 和 PUT 关闭。写权限扩展与已提交制品数据保留，撤销本轮临时应用授权。数据库对象清理在核对依赖和数据保留后单独执行。

- [Supabase 数据库连接](https://supabase.com/docs/guides/database/connecting-to-postgres)

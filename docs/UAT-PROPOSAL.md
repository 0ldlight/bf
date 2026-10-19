# P0 UAT 部署方案 / UAT design

状态：所有者于 2026-10-19 批准本 P0 方案。按以下范围实施；批准不代表部署或恢复验收已完成。
Status: the owner approved this P0 design on 2026-10-19. Implementation and deployment/recovery acceptance still require evidence.

## 已批准方案 / Approved design

1. Jenkins 动态 Agent 执行共享检查，构建 Linux amd64 静态 Go 二进制，输出以完整提交摘要命名的压缩包及 SHA-256 sidecar；在 Jenkins 归档并指纹追踪。
   Jenkins checks and builds a static Linux binary, archives commit-addressed packages and SHA-256 sidecars.
2. Jenkins 通过独立上传身份，将同一制品上传到 UAT 的 staging 目录。上传身份只能写 staging；部署身份不能修改发布目录中的既有版本。凭据保存在各 CI 的受控 Secret，不复用个人 root 私钥。
   A dedicated uploader stages immutable inputs over SSH; separate scoped identities and controlled CI credentials avoid reusing the owner's root key.
3. CircleCI 接收提交/制品摘要，校验包、清单、路径和二进制摘要，锁定部署过程，建立新的版本目录，切换 current，重启并检查健康。它不编译、打包或访问私有 K8s。
   CircleCI validates immutable inputs, serializes activation, switches the current release and checks health without building or accessing private Kubernetes.
4. P0 用 systemd 运行非 root 的 Go 进程，仅监听 loopback；Caddy 作为 80/443 HTTPS 入口，自动管理 `uat.binflow.org` 证书。没有产品数据库或演示数据占位实现。
   Run the non-root health process under systemd on loopback, with Caddy terminating HTTPS for the UAT domain. This phase needs no product database.
5. 部署前记录上一个有效版本；新版本启动/健康失败时恢复该版本并保留失败证据。首次安装失败不能伪称“回滚成功”。后续引入数据库前另行审阅结构兼容及恢复策略。
   Record the previous release and restore it after a failed activation. A failed first installation has no previous release to roll back to. Database rollout requires a separate compatibility decision.

## CI 身份与 Secret / CI identities and secrets

| 身份 / Identity | 使用方 / Used by | 允许操作 / Allowed operation |
| --- | --- | --- |
| `binflow-upload` | Jenkins | 通过固定上传程序接收制品到 staging / Stage artifacts through the fixed upload helper |
| `binflow-deploy` | CircleCI | 通过固定部署程序校验、切换和恢复版本 / Validate, activate and recover through the fixed deployment helper |

两者使用不同的专用 SSH Key；服务器通过 root 所有的 `authorized_keys` 强制命令限制能力，禁止交互式登录、任意远程命令与转发。部署账号的 sudo 规则只允许固定助手和固定参数，运行中的 BinFlow 仍使用无登录权限的非 root 身份。安装后须实际验证这些限制。
Use separate SSH keys and root-owned forced-command rules, with no interactive shell, arbitrary remote commands or forwarding. The deployment account may elevate only the fixed helper invocation; BinFlow runs as a separate non-root service identity. Verify these restrictions after installation.

CircleCI 将部署私钥保存在项目的 **SSH Keys**，Hostname 限定为 `uat.binflow.org`。`BINFLOW_UAT_SSH_KEY_FINGERPRINT` 环境变量只保存该 Key 的 SHA-256 指纹；`BINFLOW_UAT_KNOWN_HOSTS` 保存经受信连接核实的服务器公钥。源码、环境变量和日志均不保存部署私钥或 root 密码。
Store the private deployment key in project **SSH Keys**, scoped to `uat.binflow.org`. `BINFLOW_UAT_SSH_KEY_FINGERPRINT` holds its SHA-256 fingerprint, while `BINFLOW_UAT_KNOWN_HOSTS` pins the independently verified server public key. Keep the deployment private key and root password out of source, environment variables and logs.

按 [CircleCI 官方约定](https://circleci.com/docs/guides/integration/add-ssh-key/)，`add_ssh_keys.fingerprints` 使用固定指纹，不支持环境变量展开。配置先精确加载所需 Key，再将该 Key 的实际指纹与环境变量核对；任一不符即失败。SSH 使用明确的身份文件与固定服务器公钥，避免选中项目内其他 Key。轮换 Key 时同步更新 SSH Keys、配置中的固定指纹与指纹环境变量。
CircleCI requires literal fingerprints in `add_ssh_keys`. Load the selected key, verify its actual fingerprint against the environment variable and fail on mismatch. Use its explicit identity file and pinned server key. Rotate the stored key, configured fingerprint and fingerprint variable together.

上传网关限制单个压缩包为 32 MiB、暂存总量为 256 MiB、完整制品为 16 个、目录项为 64 个。中断上传遗留的临时文件计入总量；网关只清理本次调用创建的临时文件，不自动删除旧残留。达到上限时，由操作者核对项目归属后处理残留并重试；同一制品的有效重试不再分配临时包。
The upload gateway caps compressed artifacts at 32 MiB, total staging at 256 MiB, completed artifacts at 16 and directory entries at 64. Interrupted temporary files consume the budget. The gateway removes only temporary files created by its current invocation; operators investigate ownership before removing earlier residue. Identical valid retries stream-verify without storing another temporary archive.

## 验收边界 / Acceptance boundary

先在临时目录测试摘要不符、非法包路径、重复版本和健康失败的拒绝/恢复，再执行一次受控 UAT 部署，确认 HTTPS、进程身份、版本摘要和恢复路径。只对 P0 内部健康入口验收，不宣称产品功能或生产可用。
Test invalid inputs and recovery in isolation before one controlled UAT run. Verify HTTPS, identity and digests without claiming product readiness.

本方案不批准完整产品的最终 UAT 拓扑。原有 Compose/PostgreSQL 提案继续保留，待产品存储与界面就绪后比较；本期采用上述 systemd/Caddy 方案。
This does not settle the final product topology. The earlier Compose/PostgreSQL proposal remains open for later product-topology review.

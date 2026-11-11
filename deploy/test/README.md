# 常驻 test 环境 / Resident test environment

`binflow-test` 为常驻环境：BinFlow HTTP Deployment、PostgreSQL 17.10 StatefulSet、10Gi Longhorn PVC。任务结束时保留本命名空间、工作负载、Secret 和 PVC；临时测试只能清理自己创建的数据库。
`binflow-test` retains the HTTP Deployment, PostgreSQL StatefulSet, Secret and PVC across tasks. Clean up only databases created by the individual test run.

HTTP 当前仅提供 `/healthz`；数据库尚未接入 HTTP 入口。不代表完整上传、下载或 Artifactory 兼容服务。单副本可自动恢复，但不提供数据库高可用或备份。
HTTP currently exposes only `/healthz`; it does not consume this database yet. Single replicas recover automatically but do not provide database HA or backups.

## 部署 / Deploy

显式指定已核实的 kubeconfig/context，避免改变默认配置。`KUBECONFIG` 和镜像仓库地址仅保存在本机环境记录。
Select the verified kubeconfig/context explicitly. Keep host access and private registry details in ignored local notes.

```sh
export KUBECONFIG=/path/to/verified/kubeconfig
kubectl --context repo create namespace binflow-test
```

首次部署通过安全渠道创建 `postgres-auth` Secret，其 `password` 键使用随机强密码；不要将值写入 Git 或命令历史。现有 Secret 不得在重部署时重建或旋转，因为持久数据中的密码不会随环境变量变化。
Provision a random password as key `password` in Secret `postgres-auth` before first deployment. Keep it outside Git/history; preserve it on re-deploy because initialized PostgreSQL credentials do not follow environment changes.

```sh
kubectl --context repo apply -f deploy/test/resources.yaml
kubectl --context repo -n binflow-test rollout status statefulset/postgres
```

从要部署的已验证提交构建静态 Linux 二进制，使用 Dockerfile 打包、推送到集群可访问的仓库，并将生成的摘要填入 IMAGE。构建上下文仅包含 Dockerfile 和二进制。
Build the verified revision as a static Linux binary, package with the Dockerfile, push to a reachable registry, and set IMAGE to the resulting digest. Use a context containing only the Dockerfile and binary.

```sh
mkdir -p /tmp/binflow-test-build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/binflow-test-build/binflow ./cmd/binflow
# Copy deploy/test/Dockerfile into the build directory; build and push using your image tooling.
# 将 Dockerfile 放入构建目录，用镜像工具构建并推送。
export IMAGE=registry.example/binflow@sha256:REPLACE_WITH_PUBLISHED_DIGEST
kubectl --context repo set image --local -f deploy/test/app.yaml binflow="$IMAGE" -o yaml |
  kubectl --context repo apply -f -
kubectl --context repo -n binflow-test rollout status deployment/binflow
```

## 访问与验证 / Access and verify

```sh
kubectl --context repo -n binflow-test port-forward --address 127.0.0.1 svc/binflow 18080:8080
# 另一终端 / another terminal
curl --fail http://127.0.0.1:18080/healthz

kubectl --context repo -n binflow-test port-forward --address 127.0.0.1 svc/postgres 15432:5432
```

集群内地址为 `binflow.binflow-test.svc.cluster.local:8080` 与 `postgres.binflow-test.svc.cluster.local:5432`。数据库用户名 `binflow`，常驻数据库 `binflow_test`；密码从 Secret 安全读取。服务通过集群内网络或本机转发访问。
Cluster DNS names are above. PostgreSQL user is `binflow`, retained database is `binflow_test`; retrieve credentials securely from the Secret.

集成测试会更改表结构，应使用独立、唯一命名的临时数据库，完成后仅删除该库。禁止将共享常驻库作为 `BINFLOW_TEST_POSTGRES_DISPOSABLE=1` 的目标。
Integration tests mutate schemas: create a uniquely named disposable database per run and drop only that database afterward. Never mark the retained shared database disposable.

StatefulSet 显式配置 PVC Retain；删除整个命名空间或 PVC 仍会丢失数据，不能作为日常清理方式。持久卷生命周期见 [Kubernetes StatefulSet 文档](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/)。
The StatefulSet explicitly retains PVCs. Deleting the namespace or PVC can still destroy data; neither belongs in routine cleanup.

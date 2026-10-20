# Generic 下载契约 / Generic download contract

范围：BIN-128，承接能力矩阵 PKG-007/API-001。先冻结受控原版观察并提供只读回归探针，未实现 BinFlow 制品存储、认证或下载路由，不代表产品兼容完成。
Scope: a reproducible read-only contract probe, not a product download implementation or parity claim.

## 版本与证据 / Identity and evidence

专用测试实例的身份接口于 2026-10-20 返回 7.161.26、revision86126900。本任务新建独立 local/generic 仓库，上传自主构造的65字节payload；既有仓库未改。原始HAR、认证、地址和资源归属记录只在忽略目录中保存。
The controlled fixture is original synthetic data. Runtime identity is recorded independently of the probe profile; choosing a profile is not proof of target version.

- Fixture SHA-256: `f6bf2482a3380fe0e5217787e777ad09b1d030ab36a2ebd894b5269894ed23a8`.
- Capture ID: `baseline/artifactory-pro/7.161.26/generic-download/BIN-128-v1`.
- HAR SHA-256: `253e30e31b43196808df7d076081bfcf447ad6a17f41e923591a0db7cdfca04a`.
- REA retained evidence: `ev_0f017a89afa2e11301c051b5eef0c611bd7c3f8dbcb3a98fb49d1e55513eb76e`, 18 records decoded. REA inspects the saved capture; it does not independently attest live deployment identity.

## 观察 / Observed cases

以下输入由首次GET响应中的ETag与Last-Modified派生。该样本ETag为未加引号的内容SHA-1，X-Checksum-Sha1/Sha256与合成制品匹配，Accept-Ranges为bytes。正文比较使用合成制品自身，不公开原版错误正文或原始响应。
Derive validators from the first GET and compare exact synthetic bytes. These are version/sample observations, not general HTTP rules.

| ID | 请求 / Request | 状态 / Status | 正文 / Body |
|---|---|---|---|
| get | GET | 200 | entire fixture |
| head | HEAD | 200 | empty, full Content-Length |
| range-prefix | Range bytes=0-3 | 206 | first4bytes |
| range-suffix | Range bytes=-4 | 206 | last4bytes |
| range-open | Range bytes=4- | 206 | bytes4..end |
| range-unsatisfiable | Range bytes=N- | 416 | empty, Content-Range bytes */N |
| etag-match | If-None-Match exact returned ETag | 304 | empty |
| etag-any | If-None-Match * | 200 | entire fixture |
| etag-weak | If-None-Match W/ + returned ETag | 304 | empty |
| etag-miss | If-None-Match `"binflow-missing"` | 200 | entire fixture |
| modified-match | If-Modified-Since returned date | 304 | empty |
| modified-old | If-Modified-Since Unix epoch | 200 | entire fixture |
| if-match-hit | If-Match returned ETag | 200 | entire fixture |
| if-match-miss | If-Match `"binflow-missing"` | 200 | entire fixture |
| if-range-hit | Range0-3, If-Range returned ETag | 206 | first4bytes |
| if-range-miss | Range0-3, If-Range `"binflow-missing"` | 200 | entire fixture |
| conditional-precedence | unmatched If-None-Match + matching If-Modified-Since | 200 | entire fixture |
| head-range | HEAD with Range0-3 | 200 | empty, full Content-Length |

N为已知制品长度；206检查精确Content-Range与分段Content-Length。304无Content-Length；416无ETag/Last-Modified。成功表示的ETag、Last-Modified和checksum头需一致。
Verify exact representation/range headers as well as status and body. Dynamic server/date/node headers are outside this contract.

## 源码交叉验证 / Source corroboration

恢复源码用于定位后，目标包内的同名类以 javap 26.0.2.1 静态复核：`ArtifactoryRequestBase.isNoneMatch/parseETagValue` 对规范化值精确比较；`DownloadServiceImpl.isNotModified` 在存在 If-None-Match 时使用实体匹配分支；`RequestResponseHelper.sendBodyResponse` 的 416 分支在完整表示头填充前返回。范围逻辑另受 `httpRangeSupport` 配置影响。这些分支与上述样本吻合，不能外推到所有设置、完整 ETag 语法或整个请求链。
Recovered source locates the methods; target bytecode corroborates the matching, precedence and early-416 branches. The sample remains configuration-bounded and is not a complete protocol proof.

- `artifactory-api-7.161.15.jar`: SHA-256 `e025580e61df908be80ea08eda49d67bf6cf7439802c679310bcc09ef0f76593`.
- `artifactory-core-7.161.15.jar`: SHA-256 `1abd1ce70341b8125a12b0ef9c3b7d95074e71c90f734c776fdf973297fac831`.

父级产品包身份见[研究索引](RESEARCH-INDEX.md)。组件版本不等于产品版本；受控字节码、完整 manifest 和原始实例记录保持私有。
Parent-package identity is in the research index. Component versions differ from product identity; controlled bytecode/manifests and raw observations stay private.
## 探针边界与验收 / Probe boundaries and verification

Go标准库CLI仅发出上述GET/HEAD，从显式本机文件读取测试正文与认证；仓库配置和制品由操作者单独准备。认证可选，来自权限0600的本机普通文件，内容为单行Authorization值（Basic或Bearer）；不得用命令参数传递值。URL禁止userinfo/query/fragment，报告以独占创建、0600权限写入；不覆盖既有文件或符号链接。禁止代理与重定向，保留系统TLS验证，限定每请求超时和64KiB响应。错误不输出URL、认证值或服务器正文。
The runner has fixed read-only scenarios, bounded I/O, normal TLS verification and private exclusive reports. It does not provision fixtures or elevate access.

只接受5..65536字节的已知合成fixture。首个GET内容/校验头或validator不符则报告失败并标明后续未执行；不能把18项未测当作通过。状态、正文、长度或范围头不符导致非零退出，报告仍保留已执行结果。
A fixed profile is compared against controlled fixture bytes. Early failure explicitly leaves later cases unexecuted.

验证使用独立负例、go vet/test-race、共享 scripts/check.sh 及真实原版重放。文档和代码由所有者审阅，默认分支不自动合并。
Keep raw evidence private, record executed commands and real replay results, and leave owner merge/P0 exit decisions intact.

未覆盖：空/大文件、多段Range、完整ETag语法、上传与校验上传、目录/属性、匿名/权限矩阵、远端/虚拟仓库、并发写/缓存/故障、其他客户端/平台版本。后续分项研究，不缩减PKG-007最终范围。
Open scope stays explicit; no tested-download claim applies to BinFlow yet.

## CLI / CLI command

`cmd/binflow-contract`（Go 标准库）封装 `internal/compatprobe`：
`cmd/binflow-contract` (Go standard library only) wraps `internal/compatprobe`:

```sh
go build -o /tmp/binflow-contract ./cmd/binflow-contract
/tmp/binflow-contract -url https://artifacts.example.test/generic/payload.bin \
  -fixture-file /path/to/known-fixture.bin \
  -authorization-file /path/outside-repository/authorization.txt \
  -report /path/to/new-report.json
```

- `-url` 绝对制品 URL（http/https，无 userinfo/query/fragment）；`-fixture-file` 已知合成制品（普通非符号链接文件，5..65536 字节）；`-report` 报告路径（必须不存在，独占创建 0600，网络前预约）；`-authorization-file` 可选凭据（普通非符号链接、0600 或更严、≤4096 字节、恰一行 Basic/Bearer、位于仓库之外，仅允许末尾一个 LF）；`-timeout` 每请求超时，默认 10s、上限 60s。
  `-url` is the absolute artifact URL; `-fixture-file` known synthetic bytes (regular non-symlink, 5..65536); `-report` exclusive 0600 JSON reserved before any network; `-authorization-file` optional credential (regular non-symlink, 0600 or stricter, ≤4096 bytes, exactly one Basic/Bearer line, outside the repository, one trailing LF at most); `-timeout` per request, default 10s max 60s.
- 显式提供的认证文件不能为空；匿名探测时省略 `-authorization-file`。替换示例中的站点和本机路径，报告路径必须是新文件。
  A supplied authorization file must not be empty; omit the flag for anonymous probes. Replace example endpoints/paths and choose a new report path.
- 退出码：0 全部通过；1 网络/断言失败（仍写入结构化报告）；2 配置或输入拒绝（报告未创建）。stdout 仅计数；错误为静态类别，不回显 URL、凭据、路径或正文；报告仅含允许头与哈希并做凭据/URL 抹除。
  Exit codes: 0 all pass; 1 network/assertion failure (structured report still written); 2 rejected configuration/inputs (no report created). Stdout carries counts only; errors are static categories without URLs, credentials, paths or bodies; reports keep allowlisted headers and hashes only, with credential/URL redaction.
- 无代理、无重定向、无 TLS 降级参数；仅 Linux/macOS 支持防符号链接竞态的输入打开。选择 profile 不证明目标版本。
  No proxy, redirect or TLS-weakening flags; race-free non-symlink input opening supports Linux/macOS. Profile selection does not attest the target version.

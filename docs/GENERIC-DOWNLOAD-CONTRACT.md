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

Go 标准库核心 `internal/compatprobe` 仅发出上述 GET/HEAD；调用者显式提供 URL、已知合成字节和可选 Basic/Bearer 值。URL 禁止 userinfo/query/fragment；拒绝代理与重定向，保留系统 TLS 校验，限定每请求超时、响应头和正文。报告在内存中生成，仅保留允许的头与摘要，并抹除回显的凭据和目标 URL。
The core takes explicit options, bounds transport/I/O, and returns a sanitized in-memory report. The CLI/file-protection wrapper is a separate increment.

只接受5..65536字节的已知合成fixture。首个GET内容/校验头或validator不符则报告失败并标明后续未执行；不能把18项未测当作通过。状态、正文、长度或范围头不符返回非 nil error，报告仍保留已执行结果。
A fixed profile is compared against controlled fixture bytes. Early failure explicitly leaves later cases unexecuted.

验证使用独立负例、go vet/test-race、共享 scripts/check.sh 及真实原版重放。文档和代码由所有者审阅，默认分支不自动合并。
Keep raw evidence private, record executed commands and real replay results, and leave owner merge/P0 exit decisions intact.

未覆盖：空/大文件、多段Range、完整ETag语法、上传与校验上传、目录/属性、匿名/权限矩阵、远端/虚拟仓库、并发写/缓存/故障、其他客户端/平台版本。后续分项研究，不缩减PKG-007最终范围。
Open scope stays explicit; no tested-download claim applies to BinFlow yet.

## 核心调用 / In-repository Go API

```go
report, err := compatprobe.Run(ctx, compatprobe.Options{
    URL: artifactURL, Fixture: fixtureBytes,
    Authorization: authorization, Timeout: 10 * time.Second,
})
```

`Run` 返回结构化的 18 项计划；配置错误、传输错误或断言不符均返回非 nil error。命令行与认证/报告文件保护由独立 CLI 切片接入。
Run returns a full 18-row plan and a non-nil error for invalid configuration, transport or assertion failure; file handling belongs to the CLI increment.

验证入口：`go test -race ./internal/compatprobe`、`go vet ./...`、`./scripts/check.sh`。

# 普通 JSON 制品画像 / Ordinary JSON artifact profile

BIN-221 为既有普通 GET/HEAD/PUT 增加精确小写 `.json` 后缀。JSON 是文件名与 MIME 画像，正文继续作为不解析的原始字节传输和存储。路径、授权、摘要、正文预算与事务边界沿用[普通上传适配器](GENERIC-UPLOAD-ADAPTER.md)及[提交边界](UPLOAD-COMMIT-BOUNDARY.md)。
BIN-221 adds exact lowercase `.json` to ordinary GET/HEAD/PUT. Content remains opaque bytes; existing path, authorization, checksum, budget and transaction rules apply.

## 静态来源 / Static provenance

目标 Artifactory 7.161.26 包中的 config 组件版本为 7.161.15。其 `META-INF/default/mimetypes.xml` 将 `json` 映射为 `application/json`；资源 SHA-256 为 `cd0dcf81f2da9f532222e344bfdfc09be9762b34a5781702973074a782b6a21f`。归档身份和资源字节已重新核对既有目标绑定。这里只使用 MIME 属性事实，没有执行目标 JVM、访问原版实例或推断原版 HTTP/权限/正文校验行为。
The packaged config component maps `json` to `application/json`. Archive/resource identities were rechecked against the existing target binding. This is static MIME data, not original-server runtime evidence; no target code or instance is executed.

## 接受域与表示 / Accepted profile and representation

- 终名必须有非空 stem，并保持现有 ASCII 路径和上传长度限制。`.JSON`、`.Json`、`.jsonl`、单独 `.json`、编码路径和目录尾斜杠继续拒绝。250 字节 stem 加 `.json` 达到上传名的 255 字节上限。
  Names require a nonempty stem and existing ASCII/path/upload-length bounds; the extension is exact and case-sensitive.
- 只按最终后缀选 MIME：`a.txt.json` 为 `application/json`，`a.json.txt` 为 `text/plain`。不做正文 sniff、解析、重新编码或格式修复；合法 JSON、空正文、NUL 与非 UTF-8 字节均保留原样。
  Only the final suffix selects MIME. Valid JSON, empty content, NUL and non-UTF-8 bytes remain unchanged.
- GET/HEAD 的 Content-Type 精确为 `application/json`，不追加 charset。HEAD 沿用零正文和零内容 Open；字节长度、三摘要和 Content-Disposition 规则保持。
  GET/HEAD use exact `application/json`, preserving existing size, digest, attachment and bodyless/zero-open HEAD rules.
- PUT 的 201 仍使用既有 ItemCreated 媒体类型；其 `mimeType` 字段为 `application/json`。输入、已保存 Source 校验与保存侧固定 MIME 目录同步扩展，避免只修改 HTTP 表示。
  PUT keeps the ItemCreated response media type and reports `application/json` in its artifact field. Input/source validation and the save catalog admit the same profile.

## 回归与边界 / Regression and limits

纯值回归覆盖输入/Source 一致接受、后缀拒绝及长度边界；保存目录有独立物化回归。HTTP 回归检查 GET/HEAD、201 表示、原始字节和拒绝前零授权/存储调用。真实 PostgreSQL/文件系统回归经 PUT 再读回，核对节点、登记、对象、号段、重传、覆盖及旧对象保留。出口仍为 `scripts/check.sh` 与必须实际执行的 `scripts/check-postgres.sh`；编译测试二进制不能代替数据库执行。
Pure profile, save-catalog, HTTP and real database/filesystem regressions cover this contract. Shared and mandatory real-PostgreSQL gates remain the verification entry points; compilation alone is not database execution.

Range/条件请求、Unicode 路径、JSON schema、仓库类型、认证机制和 SQL 列保持现状。UAT 来源、验收及临时授权清理在任务记录中逐制品核实；完整 Generic/REST/Access 父项仍未完成。
UAT provenance, acceptance and access cleanup are verified per artifact. Broader protocol, schema and parent parity remain open.

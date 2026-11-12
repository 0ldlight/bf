# 受限条件 GET / Bounded conditional GET

BIN-222 在既有认证 local/generic 下载画像上增加 GET 的 `If-None-Match` 与 `If-Modified-Since`。依据[原版下载契约](GENERIC-DOWNLOAD-CONTRACT.md)中已保存的七个条件样本及绑定静态方法；不访问原版实例或执行目标 JVM。它不代表完整条件请求、Range 或 Generic 兼容。
BIN-222 extends authenticated ordinary GET using seven retained reference observations and bound static methods. It does not establish full conditional-request, Range or Generic parity.

## 输入画像 / Input profile

- 仅 GET；HEAD 携带条件头、Range、其他 `If-*` 头仍返回固定 501。
  GET only; conditional HEAD, Range and other conditions remain unsupported.
- 每个受支持头恰有一个值，大小写不同的同名键合并计数。`If-None-Match` 原值最多 128 字节，允许两端 ASCII 空格、一次 `W/` 前缀及一对外围引号；内部非空 token 仅含 ASCII 字母、数字、点、下划线、星号和连字符。大小写敏感；不支持列表、内部引号、控制字符或重复值。
  ETags admit a single bounded ASCII token, optional weak prefix and outer quotes; lists, duplicates and broader grammar are excluded.
- `If-Modified-Since` 恰为 29 字节的规范 IMF-fixdate GMT，年份 1970..9999，解析后格式化必须完全相同。即使另一个条件可命中，也先校验全部已提供条件。
  Dates require canonical IMF-fixdate GMT in the stated year range; all supplied conditions must be valid before evaluation.
- 新条件请求中的 `Last-Modified` 或 `X-Artifactory-If-None-Match-MD5` 修饰头（包括空值）返回 501；无条件请求保留既有行为。此排除避免冒称实现原版日期 max 和 MD5 开关链。
  Conditional requests exclude those modifiers; ordinary requests retain existing behavior.

上述输入拒绝是明确的本地支持域，不是原版全部错误行为的观察结果。
These rejection policies define the local supported profile, not observed reference-server errors.

## 判定和顺序 / Decision and ordering

方法、raw URI、头画像、取消、认证/read 授权、取消、同次节点查询与完整投影校验、取消，然后才判定条件。缺节点保持 404，投影或依赖失败保持固定 500；未授权请求不查询节点，不泄露验证器。
Authorization and complete same-query source validation precede condition evaluation. Missing nodes and failures retain existing behavior.

存在 `If-None-Match` 时仅比较规范化 token 与当前原始 SHA-1 ETag；不匹配时不回退日期条件。保存的原版样本中 `*` 返回完整 200，因此不使用通用 HTTP 库的 wildcard 匹配。仅有日期条件时比较向下取整到秒的当前 modified 时间是否小于等于请求日期。
ETag presence takes precedence over the date. Wildcard is an ordinary nonmatching token, consistent with the retained 200 sample. Date evaluation uses the current modification time floored to seconds.

命中返回 304、零正文、零内容 Open/Read/Close，保留当前 ETag、Last-Modified、三摘要和 Content-Disposition，不发送 Content-Type/Content-Length，不广告尚未实现的 Accept-Ranges。不匹配复用同次已校验 Source 进入既有内容选择与流所有权，不进行第二次实际查询。
A match returns metadata-backed 304 without opening content, retaining validators, checksums and disposition but omitting representation type/length. A miss streams from the same validated source snapshot.

304 只说明当前元数据与验证器匹配，不证明物理 blob 存在或完整；损坏的节点字段仍须先拒绝。普通 200 保持既有不重新计算内容摘要的语义。
A metadata-backed 304 cannot attest physical blob integrity. Invalid node projections are rejected first; ordinary streaming retains its existing integrity boundary.

## 验收 / Verification

七个冻结样本、真实 HTTP 304 头/正文、单次查询和流关闭、拒绝/授权/取消顺序、源变化与坏字段、真实 PostgreSQL/文件系统回归，以及精确提交 CI 和受控 UAT 是本切片出口。使用说明在实际验收后维护到 Fern；原始捕获和静态资料保持私有。
Acceptance requires reference cases, transport and ownership checks, real database/filesystem regressions, exact-commit CI and controlled UAT. Usage documentation follows verified behavior.

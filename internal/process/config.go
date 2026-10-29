package process

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"time"

	"github.com/0ldlight/bf/internal/security"
)

// 环境变量只承载非机密选择器；下载模式的全部设置（含内联 DSN）来自单个
// 受控 JSON 文件，路径由 BINFLOW_DOWNLOAD_CONFIG 指定。
// Environment variables carry non-secret selectors only; every download-mode
// setting (including the inline DSN) comes from one controlled JSON file
// located by BINFLOW_DOWNLOAD_CONFIG.
const (
	envMode           = "BINFLOW_MODE"
	envListenAddr     = "BINFLOW_LISTEN_ADDR"
	envDownloadConfig = "BINFLOW_DOWNLOAD_CONFIG"

	// defaultListenAddr 保持既有健康模式监听默认不变。
	// defaultListenAddr preserves the existing health-mode listener default.
	defaultListenAddr = "127.0.0.1:8080"

	// configMaxBytes 在解析前限制配置文件大小；超限直接拒绝且不回显内容。
	// configMaxBytes bounds the configuration file before parsing; oversized
	// files are rejected without echoing contents.
	configMaxBytes = 16 << 10

	// 固定运行参数：操作/启动 10s、排空 10s、连接池 8 打开/8 空闲/连接
	// 生命周期 15m；不可配置、不放宽。
	// Fixed runtime values: 10s operation/startup, 10s drain and a pool of
	// 8 open / 8 idle connections with a 15m lifetime; not configurable.
	startupTimeout   = 10 * time.Second
	operationTimeout = 10 * time.Second
	drainTimeout     = 10 * time.Second
	poolMaxOpen      = 8
	poolMaxIdle      = 8
	poolMaxLifetime  = 15 * time.Minute
)

// 下载失败门与仓库键的支持域，与 internal/security 构造器校验保持一致；
// 本包重复声明以免依赖对方私有符号。
// Supported download gate and repository-key domain mirroring the
// constructor checks in internal/security; redeclared here so this package
// never depends on private symbols there.
const (
	configRepoKeyMaxLen = 64
	configThresholdMin  = 1
	configThresholdMax  = 100
	configSecondsMin    = 1
	configSecondsMax    = 3600
	configUsersMin      = 1
	configUsersMax      = 4096
)

// configError 只把固定阶段文案挂到公开哨兵上；绝不包装或回显原始
// OS/JSON/文件内容错误。
// configError attaches fixed stage text to the public sentinel only; raw
// OS/JSON/file-content errors are never wrapped or echoed.
func configError(stage string) error {
	return fmt.Errorf("%w: %s", ErrConfiguration, stage)
}

// LoadConfig 从环境读取显式进程模式并装配已验证配置。回调必须非 nil。
// BINFLOW_MODE 空/health 为默认健康模式；未知值一律拒绝，绝不静默回退。
// 健康模式拒绝非空 BINFLOW_DOWNLOAD_CONFIG，防止半启用造成错误就绪；
// BINFLOW_LISTEN_ADDR 语义保持既有默认 127.0.0.1:8080 不变。下载模式要求
// 绝对 BINFLOW_DOWNLOAD_CONFIG 路径，拒绝非空 BINFLOW_LISTEN_ADDR 的
// 竞争监听来源，全部下载设置来自受控 JSON 文件。错误固定包装
// ErrConfiguration，不含路径、内容或 DSN。
// LoadConfig reads the explicit process mode from the environment and
// assembles validated configuration. The callback must be non-nil. Empty or
// "health" BINFLOW_MODE selects the default health mode; unknown values are
// rejected, never silently falling back. Health mode rejects a nonempty
// BINFLOW_DOWNLOAD_CONFIG to prevent half-enabled mistaken readiness, and
// BINFLOW_LISTEN_ADDR keeps its existing default 127.0.0.1:8080 semantics.
// Download mode requires an absolute BINFLOW_DOWNLOAD_CONFIG path, rejects a
// nonempty BINFLOW_LISTEN_ADDR as an ambiguous competing listener source and
// takes every download setting from the controlled JSON file. Errors are the
// fixed ErrConfiguration wrapper without paths, contents or DSNs.
func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, configError("nil environment callback")
	}
	mode := getenv(envMode)
	if mode == "" {
		mode = ModeHealth
	}
	switch mode {
	case ModeHealth:
		if getenv(envDownloadConfig) != "" {
			return Config{}, configError("health mode rejects download configuration")
		}
		listenAddr := getenv(envListenAddr)
		if listenAddr == "" {
			listenAddr = defaultListenAddr
		}
		cfg := Config{Mode: ModeHealth, ListenAddr: listenAddr}
		if err := validateConfig(cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	case ModeDownload:
		if getenv(envListenAddr) != "" {
			return Config{}, configError("download mode rejects BINFLOW_LISTEN_ADDR")
		}
		path := getenv(envDownloadConfig)
		if path == "" {
			return Config{}, configError("download mode requires BINFLOW_DOWNLOAD_CONFIG")
		}
		if !filepath.IsAbs(path) {
			return Config{}, configError("download configuration path must be absolute")
		}
		data, err := readPrivateConfigFile(path)
		if err != nil {
			return Config{}, err
		}
		doc, err := parseDownloadDocument(data)
		if err != nil {
			return Config{}, err
		}
		cfg := Config{
			Mode:          ModeDownload,
			ListenAddr:    doc.listenAddr,
			PostgresDSN:   doc.postgresDSN,
			FilestoreRoot: doc.filestoreRoot,
			HTTP: security.HTTPConfig{
				RepoKey:          doc.repoKey,
				Challenge:        doc.challenge,
				FailureThreshold: int(doc.failureThreshold),
				FailureWindow:    time.Duration(doc.failureWindowSeconds) * time.Second,
				BlockDuration:    time.Duration(doc.blockSeconds) * time.Second,
				MaxTrackedUsers:  int(doc.maxTrackedUsers),
			},
		}
		if err := validateConfig(cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	default:
		return Config{}, configError("unknown mode")
	}
}

// validateConfig 是 LoadConfig 与 Run 共享的完整运行时校验；健康模式携带
// 任何下载专属字段即拒绝，手动构造的 Config 也必须通过同一域检查。
// validateConfig is the complete shared validation used by LoadConfig and
// Run; health mode carrying any download-only field is rejected and manually
// constructed Configs must pass the same domain checks.
func validateConfig(cfg Config) error {
	switch cfg.Mode {
	case ModeHealth:
		if cfg.PostgresDSN != "" || cfg.FilestoreRoot != "" || !zeroHTTPGate(cfg.HTTP) {
			return configError("health mode carries download settings")
		}
		if err := validateHealthListenAddr(cfg.ListenAddr); err != nil {
			return err
		}
		return nil
	case ModeDownload:
		if err := validateDownloadListenAddr(cfg.ListenAddr); err != nil {
			return err
		}
		if cfg.PostgresDSN == "" {
			return configError("empty postgres dsn")
		}
		if err := validateFilestoreRoot(cfg.FilestoreRoot); err != nil {
			return err
		}
		return validateHTTPGate(cfg.HTTP)
	default:
		return configError("unknown mode")
	}
}

// zeroHTTPGate 判断 HTTP 失败门值字段是否全为零；Report 回调不属于配置
// 文件数据，不参与判定。
// zeroHTTPGate reports whether all HTTP gate value fields are zero; the
// Report callback is not configuration-file data and is not considered.
func zeroHTTPGate(h security.HTTPConfig) bool {
	return h.RepoKey == "" && h.Challenge == "" && h.FailureThreshold == 0 &&
		h.FailureWindow == 0 && h.BlockDuration == 0 && h.MaxTrackedUsers == 0
}

// validateHealthListenAddr 保持既有健康行为：接受历史上 net.Listen 可绑定
// 的任意 host:port 形式（含通配/localhost/DNS），仅排除无法解析出端口或
// 无法拆分地址的值；空端口与 net.Listen 一致视为端口 0，不做回环限制。
// validateHealthListenAddr keeps existing health behavior: any host:port
// form that net.Listen historically accepted (wildcard, localhost, DNS) is
// still accepted; only values that cannot split are rejected. An empty
// port retains net.Listen port-zero behavior. An empty address is invalid because the
// default is applied only inside LoadConfig.
func validateHealthListenAddr(addr string) error {
	if addr == "" {
		return configError("empty listen address")
	}
	_, _, err := net.SplitHostPort(addr)
	if err != nil {
		return configError("invalid listen address")
	}
	return nil
}

// validateDownloadListenAddr 只接受字面回环 IP（IPv4/IPv6，含 IPv4 映射
// IPv6 形式）加 0..65535 十进制端口；拒绝 localhost、通配、DNS 名称、
// 非回环地址与服务名端口。来自 Host/Forwarded/XFP 的传输信任一概不取，
// 对外访问需另行受管的 HTTPS 终结器，本包不做部署变更。
// validateDownloadListenAddr accepts only literal loopback IPs (IPv4/IPv6,
// including IPv4-mapped IPv6 forms) with a decimal port 0..65535; localhost,
// wildcards, DNS names, non-loopback addresses and service-name ports are
// rejected. No transport trust derives from Host/Forwarded/XFP; external
// access requires a separately managed HTTPS terminator with no deploy change
// here.
func validateDownloadListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return configError("invalid download listen address")
	}
	if host == "" {
		return configError("download listen address forbids wildcard host")
	}
	if !decimalDigits(port) {
		return configError("download listen address requires a numeric port")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return configError("download listen address port out of range")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return configError("download listen address must be a literal loopback IP")
	}
	return nil
}

// decimalDigits 要求非空且全为 ASCII 十进制数字。
// decimalDigits requires a nonempty string of ASCII decimal digits only.
func decimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validateFilestoreRoot 要求非空绝对路径；filestore.New 自行拥有其权限/
// 符号链接策略并可创建配置的 root/staging，本包绝不删除既有路径。
// validateFilestoreRoot requires a nonempty absolute path; filestore.New
// owns its own permission/symlink policy and may create the configured
// root/staging, and this package never deletes pre-existing paths.
func validateFilestoreRoot(root string) error {
	if root == "" {
		return configError("empty filestore root")
	}
	if !filepath.IsAbs(root) {
		return configError("filestore root must be absolute")
	}
	return nil
}

// validateHTTPGate 复用受支持的失败门域：单段 ASCII 仓库键 1..64 且非
// ./..、非空可打印 ASCII challenge、阈值 1..100、窗口/阻断 1..3600s、
// 追踪用户 1..4096。
// validateHTTPGate reuses the supported gate domain: a single-segment ASCII
// repository key 1..64 bytes that is never ./.., a nonempty printable ASCII
// challenge, threshold 1..100, window/block 1..3600s and 1..4096 tracked
// users.
func validateHTTPGate(h security.HTTPConfig) error {
	if !validRepoKey(h.RepoKey) {
		return configError("invalid repository key")
	}
	if !validChallenge(h.Challenge) {
		return configError("invalid challenge")
	}
	if h.FailureThreshold < configThresholdMin || h.FailureThreshold > configThresholdMax {
		return configError("invalid failure threshold")
	}
	if h.FailureWindow < configSecondsMin*time.Second || h.FailureWindow > configSecondsMax*time.Second {
		return configError("invalid failure window")
	}
	if h.BlockDuration < configSecondsMin*time.Second || h.BlockDuration > configSecondsMax*time.Second {
		return configError("invalid block duration")
	}
	if h.MaxTrackedUsers < configUsersMin || h.MaxTrackedUsers > configUsersMax {
		return configError("invalid tracked user bound")
	}
	return nil
}

// validRepoKey 要求单段 ASCII [A-Za-z0-9._-]、非 ./.. 且 1..64 字节，
// 与 security 入口解析一致；不回显输入。
// validRepoKey requires one ASCII [A-Za-z0-9._-] segment that is never ./..
// and is 1..64 bytes long, matching the security entry parsing; the input is
// never echoed.
func validRepoKey(repoKey string) bool {
	if repoKey == "" || len(repoKey) > configRepoKeyMaxLen || repoKey == "." || repoKey == ".." {
		return false
	}
	for i := 0; i < len(repoKey); i++ {
		c := repoKey[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// validChallenge 要求非空且全部为可打印 ASCII 0x20..0x7e。
// validChallenge requires nonempty printable ASCII 0x20..0x7e.
func validChallenge(challenge string) bool {
	if challenge == "" {
		return false
	}
	for i := 0; i < len(challenge); i++ {
		if challenge[i] < 0x20 || challenge[i] > 0x7e {
			return false
		}
	}
	return true
}

// downloadDocument 是 9 个必需 lower_snake_case 字段的严格解析结果。
// downloadDocument is the strict parse result of the nine required
// lower_snake_case fields.
type downloadDocument struct {
	listenAddr, postgresDSN, filestoreRoot, repoKey, challenge            string
	failureThreshold, failureWindowSeconds, blockSeconds, maxTrackedUsers int64
}

// configFieldNames 固定 9 个受支持字段的精确名称。
// configFieldNames pins the exact names of the nine supported fields.
var configFieldNames = map[string]bool{
	"listen_addr":            true,
	"postgres_dsn":           true,
	"filestore_root":         true,
	"repo_key":               true,
	"challenge":              true,
	"failure_threshold":      true,
	"failure_window_seconds": true,
	"block_seconds":          true,
	"max_tracked_users":      true,
}

// parseDownloadDocument 以 token 流严格解析单个 JSON 对象：拒绝非对象/
// null、未知键、重复键、尾随第二份 JSON、浮点/溢出数字、缺失或空字段；
// 任何解析错误均为固定 ErrConfiguration 文案，不回显文档内容。
// parseDownloadDocument strictly parses exactly one JSON object from a token
// stream: non-objects/null, unknown keys, duplicate keys, a trailing second
// JSON document, floating-point/overflowing numbers and missing or empty
// fields are all rejected; every parse error is fixed ErrConfiguration text
// that never echoes document contents.
func parseDownloadDocument(data []byte) (downloadDocument, error) {
	var doc downloadDocument
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return doc, configError("configuration document syntax")
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return doc, configError("configuration document must be an object")
	}
	stringFields := map[string]*string{
		"listen_addr":    &doc.listenAddr,
		"postgres_dsn":   &doc.postgresDSN,
		"filestore_root": &doc.filestoreRoot,
		"repo_key":       &doc.repoKey,
		"challenge":      &doc.challenge,
	}
	intFields := map[string]*int64{
		"failure_threshold":      &doc.failureThreshold,
		"failure_window_seconds": &doc.failureWindowSeconds,
		"block_seconds":          &doc.blockSeconds,
		"max_tracked_users":      &doc.maxTrackedUsers,
	}
	seen := make(map[string]bool, len(configFieldNames))
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return doc, configError("configuration document syntax")
		}
		key, ok := keyTok.(string)
		if !ok {
			return doc, configError("configuration document syntax")
		}
		if seen[key] {
			return doc, configError("duplicate configuration field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return doc, configError("configuration document syntax")
		}
		if target, ok := stringFields[key]; ok {
			value, err := decodeConfigString(raw)
			if err != nil {
				return doc, err
			}
			*target = value
			continue
		}
		if target, ok := intFields[key]; ok {
			value, err := decodeConfigInt(raw)
			if err != nil {
				return doc, err
			}
			*target = value
			continue
		}
		return doc, configError("unknown configuration field")
	}
	if _, err := dec.Token(); err != nil {
		return doc, configError("configuration document syntax")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return doc, configError("trailing configuration content")
	}
	if len(seen) != len(configFieldNames) {
		return doc, configError("missing configuration field")
	}
	return doc, nil
}

// decodeConfigString 只接受非空 JSON 字符串；null、数字等其他类型拒绝。
// decodeConfigString accepts only a nonempty JSON string; null, numbers and
// any other type are rejected.
func decodeConfigString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", configError("configuration field must be a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", configError("configuration field must be a string")
	}
	if value == "" {
		return "", configError("empty configuration field")
	}
	return value, nil
}

// decodeConfigInt 只接受纯十进制整数语法（可选负号）；小数、指数、null、
// 字符串与超出有界范围的溢出值一律拒绝，防止平台截断混淆域检查。
// decodeConfigInt accepts only plain decimal integer syntax (optional minus
// sign); fractions, exponents, null, strings and values overflowing the
// bounded range are rejected so platform truncation can never alias a domain
// check.
func decodeConfigInt(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, configError("configuration field must be an integer")
	}
	start := 0
	if raw[0] == '-' {
		start = 1
	}
	if start >= len(raw) {
		return 0, configError("configuration field must be an integer")
	}
	for i := start; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, configError("configuration field must be an integer")
		}
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, configError("configuration integer out of range")
	}
	if value > math.MaxInt32 || value < math.MinInt32 {
		return 0, configError("configuration integer out of range")
	}
	return value, nil
}

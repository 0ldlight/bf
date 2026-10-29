//go:build !unix

package process

// readPrivateConfigFile 在其他平台固定返回不受支持的配置错误；不提供
// 降级打开路径，也不回显路径。
// readPrivateConfigFile returns the fixed unsupported-configuration error on
// other platforms; no fallback open path exists and paths are never echoed.
func readPrivateConfigFile(_ string) ([]byte, error) {
	return nil, configError("private configuration file unsupported on this platform")
}

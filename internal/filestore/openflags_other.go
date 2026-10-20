//go:build !unix

package filestore

import (
	"errors"
	"os"
)

// openNoFollow 在非 Unix 平台显式报错：本切片的安全打开语义依赖
// O_NOFOLLOW|O_NONBLOCK，不做平台兜底。
// openNoFollow explicitly fails on non-Unix platforms: this slice's safe
// open semantics rely on O_NOFOLLOW|O_NONBLOCK with no platform fallback.
func openNoFollow(*os.Root, string) (*os.File, error) {
	return nil, errors.New("filestore: O_NOFOLLOW|O_NONBLOCK open unsupported on this platform")
}

// platformSupported 在非 Unix 平台报告 false，使 New 显式拒绝，避免出现
// Put 成功而 Open 永远失败的假可用状态。
// platformSupported reports false on non-Unix platforms so New rejects
// them explicitly instead of allowing Puts that can never be Opened.
func platformSupported() bool { return false }

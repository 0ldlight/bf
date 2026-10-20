//go:build !unix

package main

import (
	"errors"
	"os"
)

// openNoFollow is unavailable off Unix; the CLI refuses to read inputs it
// cannot protect against symlink races and FIFO hangs.
// 非 Unix 平台无法提供不跟随符号链接的打开语义，CLI 直接拒绝读取输入。
func openNoFollow(string) (*os.File, error) {
	return nil, errors.New("unsupported platform: input files require Linux or macOS")
}

//go:build unix

package main

import (
	"os"
	"syscall"
)

// openNoFollow opens path without following a trailing symlink and without
// blocking on special files such as FIFOs. Supported on Unix platforms
// (Linux, macOS) via O_NOFOLLOW|O_NONBLOCK; other platforms are refused by
// nofollow_other.go so the guarantee is explicit, not assumed.
// openNoFollow 打开路径时不跟随末级符号链接、不因 FIFO 等特殊文件阻塞；基于
// Unix（Linux/macOS）的 O_NOFOLLOW|O_NONBLOCK。其他平台由 nofollow_other.go 拒绝。
func openNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "input"), nil
}

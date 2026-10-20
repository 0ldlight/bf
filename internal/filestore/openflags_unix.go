//go:build unix

package filestore

import (
	"os"
	"syscall"
)

// openNoFollow 在 root 内以 O_NOFOLLOW|O_NONBLOCK 打开最终对象：拒绝符号
// 链接，非阻塞避免 FIFO 型特殊文件挂起读取。仍由调用方复核普通文件。
// openNoFollow opens the final object under root with O_NOFOLLOW|
// O_NONBLOCK: symlinks are refused and nonblocking avoids hanging on FIFO
// special files. Regularity is still re-checked by the caller.
func openNoFollow(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// platformSupported 报告本平台是否具备 New 依赖的 Unix 打开语义。
// platformSupported reports whether this platform provides the Unix open
// semantics New depends on.
func platformSupported() bool { return true }

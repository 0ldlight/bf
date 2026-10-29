//go:build unix

package process

import (
	"io"
	"os"
	"syscall"
)

// readPrivateConfigFile 在 Unix 上以 O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC 打开
// 受控配置文件：O_NOFOLLOW 拒绝最终符号链接竞态，O_NONBLOCK 防止 FIFO
// 阻塞，随后 f.Stat 必须为普通文件、属主为有效 UID、权限恰为 0400 或
// 0600，且大小在 1..16KiB；全部判定作用于已打开的 inode。父目录信任归
// 操作员，本包不声明完整路径命名空间不可变。任何失败都是固定
// ErrConfiguration 文案，不含路径或内容。
// readPrivateConfigFile opens the controlled configuration file on Unix with
// O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC: O_NOFOLLOW rejects final-component symlink
// races, O_NONBLOCK prevents FIFO blocking, and the following f.Stat must be
// a regular file owned by the effective UID with permissions exactly 0400 or
// 0600 and a size within 1..16KiB; every check applies to the already opened
// inode. Parent-path trust belongs to the operator; no claim is made that the
// full path namespace is immutable. Any failure is fixed ErrConfiguration
// text without paths or contents.
func readPrivateConfigFile(path string) ([]byte, error) {
	fd, err := syscall.Open(path,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, configError("open private configuration file")
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, configError("stat private configuration file")
	}
	if !info.Mode().IsRegular() {
		return nil, configError("private configuration file not regular")
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || status.Uid != uint32(syscall.Geteuid()) {
		return nil, configError("private configuration file owner")
	}
	if !validPrivateMode(info.Mode()) {
		return nil, configError("private configuration file permissions")
	}
	if info.Size() <= 0 {
		return nil, configError("empty private configuration file")
	}
	if info.Size() > configMaxBytes {
		return nil, configError("private configuration file too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, configMaxBytes+1))
	if err != nil {
		return nil, configError("read private configuration file")
	}
	if len(data) > configMaxBytes {
		return nil, configError("private configuration file too large")
	}
	return data, nil
}

// validPrivateMode 用于描述符检查，不忽略特殊权限位。
// validPrivateMode validates descriptor mode without ignoring special bits.
func validPrivateMode(mode os.FileMode) bool {
	return mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && (mode.Perm() == 0o400 || mode.Perm() == 0o600)
}

//go:build linux

// setuid/setgid/sticky 位拒绝的 Linux 专属集成用例（task-18 / PR #106 审查
// 发现 4）：Linux 允许文件属主真实落位特殊权限位（macOS 主机会剥离
// setuid 位，故本用例带 linux 构建标签）；readPrivateConfigFile 的
// validPrivateMode 必须拒绝任何带特殊权限位的受控配置文件。每个用例先以
// os.Stat 自证 inode 确实携带该位（若平台剥离则显式失败而非静默通过），
// 再断言装载拒绝且错误不回显路径。
//
// 实现注记：Go 的 os.Chmod 不会把八进制 04000/02000/01000 解释为特殊位——
// syscallMode() 只映射 os.ModeSetuid/ModeSetgid/ModeSticky 标志位，八进制
// 形式会被静默丢弃；因此 chmod 必须传“权限位|标志位”组合（本文件的自证
// 检查最初抓住的正是这一陷阱）。真实断言由 CI 的 ubuntu Check 作业执行；
// macOS 本地验证为 GOOS=linux 编译检查。
//
// Linux-only integration cases for setuid/setgid/sticky rejection
// (task-18 / PR #106 review finding 4): Linux lets the file owner set the
// special permission bits for real (macOS hosts strip the setuid bit, hence
// the linux build tag); readPrivateConfigFile's validPrivateMode must reject
// any controlled configuration file carrying a special permission bit. Every
// case first proves through os.Stat that the inode really carries the bit
// (a bit-stripping platform fails loudly instead of silently passing), then
// asserts the load rejection with no path echo.
//
// Implementation note: Go's os.Chmod does not interpret octal 04000/02000/
// 01000 as special bits — syscallMode() maps only the os.ModeSetuid/
// ModeSetgid/ModeSticky FLAGS, silently dropping the octal form — so the
// chmod must carry perm|flag (exactly the trap this file's self-check
// caught in its first draft). The real assertions run in CI's ubuntu Check
// job; the local macOS verification is the GOOS=linux compile check.
package postgresit

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/process"
)

func TestLoadConfigRejectsSpecialPermissionBits(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
		bit  os.FileMode
	}{
		{"setuid bit", 0o600 | os.ModeSetuid, os.ModeSetuid},
		{"setgid bit", 0o600 | os.ModeSetgid, os.ModeSetgid},
		{"sticky bit", 0o600 | os.ModeSticky, os.ModeSticky},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := dlcWriteDoc(t, dlcValidDownloadJSON(), tc.mode)
			// 自证 inode 状态：特殊位必须真实存在，否则本用例在其所处
			// 平台不成立。
			// Self-prove the inode state: the special bit must really be
			// present, otherwise the case is vacuous on its platform.
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("dlc: stat config: %v", err)
			}
			if info.Mode()&tc.bit == 0 {
				t.Fatalf("dlc: %s: chmod did not leave the special bit on this platform (mode=%v)", tc.name, info.Mode())
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("dlc: %s: base permission bits = %#o, want 0600", tc.name, got)
			}
			_, err = process.LoadConfig(dlcGetenv("download", path))
			if err == nil {
				t.Fatalf("dlc: %s accepted a controlled file", tc.name)
			}
			if !errors.Is(err, process.ErrConfiguration) {
				t.Fatalf("dlc: %s error = %v, want ErrConfiguration", tc.name, err)
			}
			if strings.Contains(err.Error(), path) {
				t.Fatalf("dlc: %s error echoes the path", tc.name)
			}
		})
	}
}

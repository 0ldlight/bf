//go:build unix

// FIFO 配置路径的 Unix 专属集成用例：O_NONBLOCK 打开成功而 stat 判定非
// 普通文件，覆盖 readPrivateConfigFile 的 not-regular 分支（FIFO inode 由
// 真实内核语义提供）。
// Unix-only integration case for a FIFO config path: O_NONBLOCK opens it
// while stat reports a non-regular inode, covering readPrivateConfigFile's
// not-regular branch (the FIFO inode comes from real kernel semantics).
package postgresit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/0ldlight/bf/internal/process"
)

// mkfifoDLC 以真实 syscall.Mkfifo 创建命名管道。
// mkfifoDLC creates the named pipe through the real syscall.Mkfifo.
func mkfifoDLC(path string) error {
	return syscall.Mkfifo(path, 0o600)
}

func TestLoadConfigRejectsFIFOConfigPath(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "config-fifo")
	if err := mkfifoDLC(fifo); err != nil {
		t.Fatalf("dlc: mkfifo: %v", err)
	}
	_, err := process.LoadConfig(dlcGetenv("download", fifo))
	if err == nil {
		t.Fatal("dlc: FIFO config accepted")
	}
	if !errors.Is(err, process.ErrConfiguration) {
		t.Fatalf("dlc: FIFO error = %v, want ErrConfiguration", err)
	}
	if strings.Contains(err.Error(), fifo) {
		t.Fatal("dlc: FIFO error echoes the path")
	}
	if _, err := os.Stat(fifo); err != nil {
		t.Fatalf("dlc: fifo disappeared: %v", err)
	}
}

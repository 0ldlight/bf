// Package process 负责服务配置、可信依赖装配与进程生命周期。
// Package process owns configuration, trusted composition and service lifetime.
package process

import (
	"errors"
	"github.com/0ldlight/bf/internal/security"
)

const (
	ModeHealth   = "health"
	ModeDownload = "download"
)

// Config 是已验证的进程配置；格式化始终隐藏私有配置。
// Config is validated process configuration; formatting always redacts it.
type Config struct {
	Mode, ListenAddr, PostgresDSN, FilestoreRoot string
	HTTP                                         security.HTTPConfig
}

func (Config) String() string   { return "process configuration (redacted)" }
func (Config) GoString() string { return "process configuration (redacted)" }

var (
	ErrConfiguration = errors.New("process: invalid configuration")
	ErrStartup       = errors.New("process: startup failed")
	ErrServe         = errors.New("process: HTTP service failed")
	ErrDrainTimeout  = errors.New("process: shutdown drain timed out")
	ErrResources     = errors.New("process: resource release failed")
)

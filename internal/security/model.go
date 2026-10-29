// Package security 提供持久化身份与仓库读授权。
// Package security supplies persisted identity and repository read authorization.
package security

import (
	"context"
	"errors"
	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/repository"
	"time"
)

// Principal 是实际认证后的不可变数据库身份；零值无效。
// Principal is an authenticated, immutable database identity. Its zero value is invalid.
type Principal struct {
	id       int64
	username string
}

func (p Principal) ID() int64        { return p.id }
func (p Principal) Username() string { return p.username }

// RepositoryInfo 是受支持 local/generic 画像的权威目录记录。
// RepositoryInfo is an authoritative catalog record for the supported local Generic profile.
type RepositoryInfo struct {
	Key, Kind, PackageType string
	Revision               int64
}

// Backend 读取当前身份、目录与许可事实，不拥有 HTTP 策略。
// Backend reads current identity, catalog and permission state; it owns no HTTP policy.
type Backend interface {
	Validate(context.Context) error
	Authenticate(context.Context, string, string) (Principal, error)
	LookupRepository(context.Context, string) (RepositoryInfo, error)
	CanRead(context.Context, Principal, RepositoryInfo, repository.Path) (bool, error)
}

var (
	ErrConfiguration      = errors.New("security: invalid configuration")
	ErrInvalidCredentials = errors.New("security: invalid credentials")
	ErrAccountDisabled    = errors.New("security: account unavailable")
	ErrCredentialEncoding = errors.New("security: unsupported credential encoding")
	ErrSecurityStore      = errors.New("security: store failure")
	ErrRepositoryAbsent   = errors.New("security: repository absent")
)

// HTTPConfig 显式限制进程内认证失败门。
// HTTPConfig explicitly bounds the process-local failed-authentication gate.
type HTTPConfig struct {
	RepoKey, Challenge           string
	FailureThreshold             int
	FailureWindow, BlockDuration time.Duration
	MaxTrackedUsers              int
	Report                       func(httpserver.DownloadFailure)
}

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

// NewPrincipal 由 id 与 username 构造 Principal。正常主体只能来自数据库
// 认证（Store.Authenticate）；本构造器是显式的可测性缝，让包外的深层复核
// 路径（如上传提交 recheck）可以在单元测试中构造真实形状的主体。不做校验
// 或规范化：非正 id 或空 username 保持零值语义，由既有消费方守卫拒绝。
// NewPrincipal builds a Principal from an id and a username. Real
// principals come from database authentication (Store.Authenticate)
// only; this constructor is an explicit testability seam letting
// packages outside security construct realistically shaped principals
// for deep recheck paths (the upload commit recheck, for example) in
// unit tests. It validates and normalizes nothing: a nonpositive id or
// an empty username keeps the zero-value semantics that existing
// consumers already reject.
func NewPrincipal(id int64, username string) Principal {
	return Principal{id: id, username: username}
}

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

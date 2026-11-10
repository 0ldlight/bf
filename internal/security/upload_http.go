package security

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/httpserver"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// AuthorizedUploadSaver accepts the verified identity and catalog used by the
// preflight and repeats authorization when saving the node.
type AuthorizedUploadSaver interface {
	SaveAuthorized(context.Context, io.Reader, localrepo.UploadInput, Principal, RepositoryInfo, bool) (repository.FileSnapshotSource, error)
}

// UploadHTTPConfig configures the fixed repository upload route and failure gate.
type UploadHTTPConfig struct {
	HTTPConfig
	MaxBodyBytes                int64
	PublicOrigin                string
	EnforceOnIdenticalChecksums bool
	ReportUpload                func(httpserver.UploadFailure)
}

// UploadBackend supplies live identity, catalog, and write-grant facts.
type UploadBackend interface {
	Validate(context.Context) error
	Authenticate(context.Context, string, string) (Principal, error)
	LookupRepository(context.Context, string) (RepositoryInfo, error)
	UploadPermissionReader
}

// NewAuthenticatedUpload builds the standalone ordinary PUT handler.
func NewAuthenticatedUpload(ctx context.Context, cfg UploadHTTPConfig, backend UploadBackend, nodes localrepo.FileReadStore, saver AuthorizedUploadSaver) (http.Handler, error) {
	if ctx == nil || backend == nil || nodes == nil || saver == nil ||
		!entryValidRepoKey(cfg.RepoKey) || !entryValidChallenge(cfg.Challenge) ||
		cfg.FailureThreshold < gateMinThreshold || cfg.FailureThreshold > gateMaxThreshold ||
		cfg.FailureWindow < gateMinWindow || cfg.FailureWindow > gateMaxBound ||
		cfg.BlockDuration < gateMinWindow || cfg.BlockDuration > gateMaxBound ||
		cfg.MaxTrackedUsers < gateMinUsers || cfg.MaxTrackedUsers > gateMaxUsers {
		return nil, ErrConfiguration
	}
	h, err := httpserver.NewUploadHandler(httpserver.UploadDeps{
		RepoKey: cfg.RepoKey, MaxBodyBytes: cfg.MaxBodyBytes, PublicOrigin: cfg.PublicOrigin,
		Report: cfg.ReportUpload,
		Binder: &uploadBinder{
			backend: backend, nodes: nodes, saver: saver, cfg: cfg,
			gate: newFailureGate(gateLimits{
				threshold: cfg.FailureThreshold, window: cfg.FailureWindow,
				block: cfg.BlockDuration, maxUsers: cfg.MaxTrackedUsers,
			}),
		},
	})
	if err != nil {
		return nil, newEntryConfigError(entryStageAssembly, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, newEntryConfigError(entryStageValidate, err)
	}
	if err := backend.Validate(ctx); err != nil {
		return nil, newEntryConfigError(entryStageValidate, err)
	}
	repo, err := backend.LookupRepository(ctx, cfg.RepoKey)
	if err != nil {
		return nil, newEntryConfigError(entryStageCatalog, err)
	}
	if !supportedRepository(repo) || repo.Key != cfg.RepoKey {
		return nil, ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return nil, newEntryConfigError(entryStageCatalog, err)
	}
	return h, nil
}

type uploadBinder struct {
	backend UploadBackend
	nodes   localrepo.FileReadStore
	saver   AuthorizedUploadSaver
	cfg     UploadHTTPConfig
	gate    *failureGate
}

type boundUploadSave func(context.Context, io.Reader, localrepo.UploadInput) (repository.FileSnapshotSource, error)

func (save boundUploadSave) Save(ctx context.Context, body io.Reader, in localrepo.UploadInput) (repository.FileSnapshotSource, error) {
	return save(ctx, body, in)
}

func (b *uploadBinder) BindUpload(ctx context.Context, r *http.Request, path repository.Path, checksums repository.ClientFileChecksums) (httpserver.UploadAuthResult, httpserver.UploadSaver, error) {
	unauthenticated := httpserver.UploadAuthResult{Decision: httpserver.UploadUnauthenticated, Challenge: b.cfg.Challenge}
	denied := httpserver.UploadAuthResult{Decision: httpserver.UploadDenied}
	cred, key, ok := parseAuthorization(r)
	if !ok {
		return unauthenticated, nil, nil
	}
	if b.gate.blocked(key, time.Now()) {
		return denied, nil, nil
	}
	principal, err := b.backend.Authenticate(ctx, cred.username, cred.password)
	if ctx.Err() != nil {
		return httpserver.UploadAuthResult{}, nil, ctx.Err()
	}
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		if err := b.gate.recordFailure(key, time.Now()); err != nil {
			return httpserver.UploadAuthResult{}, nil, err
		}
		return unauthenticated, nil, nil
	case errors.Is(err, ErrAccountDisabled):
		return unauthenticated, nil, nil
	case err != nil:
		return httpserver.UploadAuthResult{}, nil, err
	}
	username, valid := normalizeUsername(principal.Username())
	if !valid || username != principal.Username() || principal.ID() <= 0 {
		return httpserver.UploadAuthResult{}, nil, ErrConfiguration
	}
	b.gate.recordSuccess(key)
	repo, err := b.backend.LookupRepository(ctx, path.Repo())
	if errors.Is(err, ErrRepositoryAbsent) {
		return denied, nil, nil
	}
	if err != nil {
		return httpserver.UploadAuthResult{}, nil, err
	}
	allowed, err := CheckUploadPermissions(ctx, principal, repo, path, b.backend, b.nodes, b.cfg.EnforceOnIdenticalChecksums, checksums.SHA1Original, checksums.SHA256Original)
	if err != nil {
		return httpserver.UploadAuthResult{}, nil, err
	}
	if !allowed {
		return denied, nil, nil
	}
	save := boundUploadSave(func(ctx context.Context, body io.Reader, in localrepo.UploadInput) (repository.FileSnapshotSource, error) {
		return b.saver.SaveAuthorized(ctx, body, in, principal, repo, b.cfg.EnforceOnIdenticalChecksums)
	})
	return httpserver.UploadAuthResult{Decision: httpserver.UploadAllow, OperatorUTF16: utf16.Encode([]rune(username))}, save, nil
}

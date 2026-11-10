package security

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// 预检仅依赖 grants 桩与 readStore 查找桩，无需真实数据库或内容存储。
// The preflight only needs grant stubs and a read-store lookup stub; no
// real database or content store is involved.

func uploadRequire(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

type uploadGrants struct {
	deploy       bool
	deployErr    error
	del          bool
	delErr       error
	cancelDeploy func()
	cancelDelete func()
	calls        []string
}

func (g *uploadGrants) CanDeploy(ctx context.Context, p Principal, r RepositoryInfo, path repository.Path) (bool, error) {
	g.calls = append(g.calls, "deploy")
	if g.cancelDeploy != nil {
		g.cancelDeploy()
	}
	if g.deployErr != nil {
		return false, g.deployErr
	}
	return g.deploy, nil
}

func (g *uploadGrants) CanDelete(ctx context.Context, p Principal, r RepositoryInfo, path repository.Path) (bool, error) {
	g.calls = append(g.calls, "delete")
	if g.cancelDelete != nil {
		g.cancelDelete()
	}
	if g.delErr != nil {
		return false, g.delErr
	}
	return g.del, nil
}

type uploadReadStore struct {
	afterLookup func()
	source      repository.FileSnapshotSource
	err         error
	queries     []repository.Path
}

func (s *uploadReadStore) LookupSnapshotSource(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	s.queries = append(s.queries, path)
	if s.afterLookup != nil {
		s.afterLookup()
	}
	if s.err != nil {
		return repository.FileSnapshotSource{}, s.err
	}
	return s.source, nil
}

func uploadFixtures(t *testing.T) (Principal, RepositoryInfo, repository.Path) {
	t.Helper()
	p, err := repository.NewPath("repoA", "dir", "file.bin", true)
	uploadRequire(t, err == nil, "path error=%v", err)
	return Principal{id: 7, username: "reader"}, RepositoryInfo{Key: "repoA", Kind: "local", PackageType: "generic", Revision: 1}, p
}

func uploadSource(t *testing.T, path repository.Path, sha1, sha256 string) repository.FileSnapshotSource {
	t.Helper()
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID:      9,
			Path:    path,
			Content: content.Metadata{SHA1: sha1, SHA256: sha256},
		},
		SHA1ActualValid:   true,
		SHA256ActualValid: true,
	}
}

func sha1Units(s string) []uint16 { return utf16.Encode([]rune(s)) }

func TestCheckUploadPermissionsEntryGuardsBeforeIO(t *testing.T) {
	principal, repo, path := uploadFixtures(t)
	g := &uploadGrants{}
	s := &uploadReadStore{}
	other, err := repository.NewPath("repoB", "dir", "file.bin", true)
	uploadRequire(t, err == nil, "other path error=%v", err)
	rootFile, err := repository.NewPath("repoA", "", "", true)
	uploadRequire(t, err == nil, "root path error=%v", err)
	folder, err := repository.NewPath("repoA", "dir", "folder", false)
	uploadRequire(t, err == nil, "folder error=%v", err)
	for name, tc := range map[string]struct {
		ctx       context.Context
		principal Principal
		repo      RepositoryInfo
		path      repository.Path
		grants    UploadPermissionReader
		nodes     localrepo.FileReadStore
		cause     error
	}{
		"nil ctx":            {ctx: nil, principal: principal, repo: repo, path: path, grants: g, nodes: s, cause: errInvalidUploadInput},
		"nil grants":         {ctx: context.Background(), principal: principal, repo: repo, path: path, grants: nil, nodes: s, cause: errInvalidUploadInput},
		"nil nodes":          {ctx: context.Background(), principal: principal, repo: repo, path: path, grants: g, nodes: nil, cause: errInvalidUploadInput},
		"zero principal":     {ctx: context.Background(), repo: repo, path: path, grants: g, nodes: s, cause: errInvalidPrincipalArg},
		"zero repo":          {ctx: context.Background(), principal: principal, path: path, grants: g, nodes: s, cause: errUnsupportedRepoArg},
		"path repo mismatch": {ctx: context.Background(), principal: principal, repo: repo, path: other, grants: g, nodes: s, cause: errRepoPathMismatchArg},
		"root file path":     {ctx: context.Background(), principal: principal, repo: repo, path: rootFile, grants: g, nodes: s, cause: errRepoPathMismatchArg},
		"folder path":        {ctx: context.Background(), principal: principal, repo: repo, path: folder, grants: g, nodes: s, cause: errRepoPathMismatchArg},
	} {
		got, err := CheckUploadPermissions(tc.ctx, tc.principal, tc.repo, tc.path, tc.grants, tc.nodes, false, nil, nil)
		uploadRequire(t, got == false, "%s: got=%v want false", name, got)
		uploadRequire(t, err != nil && errors.Is(err, tc.cause), "%s: error=%v want cause=%v", name, err, tc.cause)
		uploadRequire(t, err != nil && err.Error() == ErrConfiguration.Error()+": "+stageUploadInput, "%s: text=%q", name, err)
	}
	uploadRequire(t, len(g.calls) == 0, "guards must precede grant calls, calls=%v", g.calls)
	uploadRequire(t, len(s.queries) == 0, "guards must precede node lookups, queries=%d", len(s.queries))
}

func TestCheckUploadPermissionsOrderedCallsAndEarlyStops(t *testing.T) {
	principal, repo, path := uploadFixtures(t)
	existing := uploadSource(t, path, "a1b2", "c3d4")
	cases := []struct {
		name        string
		deploy      bool
		del         bool
		store       *uploadReadStore
		requestSHA1 []uint16
		want        bool
		wantCalls   []string
		wantQuery   bool
	}{
		{"deploy false stops before node and delete", false, true, &uploadReadStore{source: existing}, nil, false, []string{"deploy"}, false},
		{"absent node skips delete", true, true, &uploadReadStore{err: sql.ErrNoRows}, nil, true, []string{"deploy"}, true},
		{"one missing and one matching digest skip delete", true, true, &uploadReadStore{source: existing}, nil, true, []string{"deploy"}, true},
		{"identical checksums skip delete", true, true, &uploadReadStore{source: existing}, sha1Units("a1b2"), true, []string{"deploy"}, true},
		{"overwrite requires delete grant", true, true, &uploadReadStore{source: existing}, sha1Units("fffe"), true, []string{"deploy", "delete"}, true},
		{"overwrite without delete grant", true, false, &uploadReadStore{source: existing}, sha1Units("fffe"), false, []string{"deploy", "delete"}, true},
	}
	for _, tc := range cases {
		g := &uploadGrants{deploy: tc.deploy, del: tc.del}
		nodeAfter := -1
		tc.store.afterLookup = func() { nodeAfter = len(g.calls) }
		got, err := CheckUploadPermissions(context.Background(), principal, repo, path, g, tc.store, false, tc.requestSHA1, sha1Units("c3d4"))
		uploadRequire(t, err == nil, "%s: error=%v", tc.name, err)
		uploadRequire(t, got == tc.want, "%s: got=%v want=%v", tc.name, got, tc.want)
		uploadRequire(t, len(g.calls) == len(tc.wantCalls), "%s: calls=%v want=%v", tc.name, g.calls, tc.wantCalls)
		for i, call := range tc.wantCalls {
			uploadRequire(t, g.calls[i] == call, "%s: call[%d]=%q want=%q", tc.name, i, g.calls[i], call)
		}
		if tc.wantQuery && nodeAfter != 1 {
			t.Fatalf("%s: node read followed %d grant calls, want exactly one (deploy)", tc.name, nodeAfter)
		}
		queried := len(tc.store.queries) > 0
		uploadRequire(t, queried == tc.wantQuery, "%s: queried=%v want=%v", tc.name, queried, tc.wantQuery)
	}
}

func TestCheckUploadPermissionsBackendErrorsReturnFalse(t *testing.T) {
	principal, repo, path := uploadFixtures(t)
	deployFail := errors.New("deploy-boom")
	deleteFail := errors.New("delete-boom")
	cases := []struct {
		name   string
		grants *uploadGrants
		store  *uploadReadStore
		stage  string
	}{
		{"can-deploy error", &uploadGrants{deployErr: deployFail}, &uploadReadStore{err: sql.ErrNoRows}, stageCanDeployRow},
		{"node lookup error", &uploadGrants{deploy: true}, &uploadReadStore{err: errors.New("node-boom")}, stageUploadNode},
		{"can-delete error", &uploadGrants{deploy: true, delErr: deleteFail}, &uploadReadStore{source: uploadSource(t, path, "a1b2", "c3d4")}, stageCanDeleteRow},
	}
	for _, tc := range cases {
		got, err := CheckUploadPermissions(context.Background(), principal, repo, path, tc.grants, tc.store, false, nil, nil)
		uploadRequire(t, got == false, "%s: got=%v want false", tc.name, got)
		uploadRequire(t, err != nil && errors.Is(err, ErrSecurityStore), "%s: error=%v want ErrSecurityStore", tc.name, err)
		uploadRequire(t, err != nil && err.Error() == ErrSecurityStore.Error()+": "+tc.stage, "%s: text=%q want stage=%q", tc.name, err, tc.stage)
		var cause error
		switch tc.stage {
		case stageCanDeployRow:
			cause = deployFail
		case stageCanDeleteRow:
			cause = deleteFail
		}
		if cause != nil {
			uploadRequire(t, errors.Is(err, cause), "%s: error=%v missing cause=%v", tc.name, err, cause)
		}
	}
}

func TestCheckUploadPermissionsCancellation(t *testing.T) {
	principal, repo, path := uploadFixtures(t)
	existing := &uploadReadStore{source: uploadSource(t, path, "a1b2", "c3d4")}

	// Pre-cancel: nothing runs.
	preCtx, preCancel := context.WithCancel(context.Background())
	preCancel()
	preG := &uploadGrants{deploy: true}
	got, err := CheckUploadPermissions(preCtx, principal, repo, path, preG, existing, false, nil, nil)
	uploadRequire(t, got == false, "pre-cancel got=%v", got)
	uploadRequire(t, err != nil && errors.Is(err, context.Canceled), "pre-cancel error=%v", err)
	uploadRequire(t, err.Error() == ErrSecurityStore.Error()+": "+stageUploadCancel, "pre-cancel text=%q", err)
	uploadRequire(t, len(preG.calls) == 0, "pre-cancel calls=%v", preG.calls)

	// Cancel during CanDeploy with a simultaneous backend error: both causes
	// are preserved, cancellation wins.
	deployFail := errors.New("deploy-cancel-boom")
	ctx, cancel := context.WithCancel(context.Background())
	g := &uploadGrants{deploy: true, deployErr: deployFail, cancelDeploy: cancel}
	got, err = CheckUploadPermissions(ctx, principal, repo, path, g, existing, false, nil, nil)
	uploadRequire(t, got == false, "deploy cancel got=%v", got)
	uploadRequire(t, err != nil && errors.Is(err, context.Canceled), "deploy cancel error=%v", err)
	uploadRequire(t, err != nil && errors.Is(err, deployFail), "deploy cancel missing backend cause=%v", err)
	uploadRequire(t, err.Error() == ErrSecurityStore.Error()+": "+stageUploadCancel, "deploy cancel text=%q", err)

	// A canceled node lookup preserves its underlying error and never checks delete.
	nodeCtx, nodeCancel := context.WithCancel(context.Background())
	nodeFailure := errors.New("node-cancel-boom")
	node := &uploadReadStore{err: nodeFailure, afterLookup: nodeCancel}
	nodeGrants := &uploadGrants{deploy: true, del: true}
	got, err = CheckUploadPermissions(nodeCtx, principal, repo, path, nodeGrants, node, false, nil, nil)
	if got || !errors.Is(err, context.Canceled) || !errors.Is(err, nodeFailure) || len(nodeGrants.calls) != 1 {
		t.Fatalf("canceled node lookup = (%v, %v), grant calls = %v", got, err, nodeGrants.calls)
	}

	// Cancel during CanDelete: stops with false before the result is used.
	ctx2, cancel2 := context.WithCancel(context.Background())
	g2 := &uploadGrants{deploy: true, del: true, cancelDelete: cancel2}
	got, err = CheckUploadPermissions(ctx2, principal, repo, path, g2, existing, false, nil, nil)
	uploadRequire(t, got == false, "delete cancel got=%v", got)
	uploadRequire(t, err != nil && errors.Is(err, context.Canceled), "delete cancel error=%v", err)
	uploadRequire(t, len(g2.calls) == 2, "delete cancel calls=%v", g2.calls)
}

func TestCheckUploadPermissionsChecksumNilEmptyDistinction(t *testing.T) {
	principal, repo, path := uploadFixtures(t)
	// Nil request digests mean "no digest facts" and force the overwrite
	// (hence delete) path even when stored checksums exist; an empty nonnil
	// request matching a stored empty checksum without enforcement does not.
	stored := &uploadReadStore{source: uploadSource(t, path, "a1b2", "c3d4")}
	g := &uploadGrants{deploy: true, del: true}
	got, err := CheckUploadPermissions(context.Background(), principal, repo, path, g, stored, false, nil, nil)
	uploadRequire(t, err == nil, "nil request error=%v", err)
	uploadRequire(t, got == true, "nil request got=%v want true (delete granted)", got)
	uploadRequire(t, len(g.calls) == 2, "nil request calls=%v want deploy+delete", g.calls)

	emptyStored := &uploadReadStore{source: uploadSource(t, path, "", "")}
	g2 := &uploadGrants{deploy: true, del: true}
	got, err = CheckUploadPermissions(context.Background(), principal, repo, path, g2, emptyStored, false, []uint16{}, []uint16{})
	uploadRequire(t, err == nil, "empty request error=%v", err)
	uploadRequire(t, got == true, "empty request got=%v want true (identical checksums, no delete)", got)
	uploadRequire(t, len(g2.calls) == 1, "empty request calls=%v want deploy only", g2.calls)

	forced := &uploadGrants{deploy: true}
	got, err = CheckUploadPermissions(context.Background(), principal, repo, path, forced, emptyStored, true, []uint16{}, []uint16{})
	if got || err != nil || len(forced.calls) != 2 || forced.calls[1] != "delete" {
		t.Fatalf("forced check = (%t, %v), grant calls = %v", got, err, forced.calls)
	}
}

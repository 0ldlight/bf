package localrepo

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
)

// 覆盖预检仅依赖 readStore 的查找桩，无需真实内容存储。
// The overwrite preflight only needs readStore's lookup stub, no real content store.

func overwriteRequire(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

func overwritePath(t *testing.T) repository.Path {
	t.Helper()
	p, err := repository.NewPath("repoA", "dir", "file.bin", true)
	overwriteRequire(t, err == nil, "path error=%v", err)
	return p
}

func overwriteSource(t *testing.T, path repository.Path, sha1 string, valid bool) repository.FileSnapshotSource {
	t.Helper()
	return repository.FileSnapshotSource{
		Reference:       repository.FileReference{ID: 9, Path: path, Content: content.Metadata{SHA1: sha1}},
		SHA1ActualValid: valid,
	}
}

func TestLookupFileOverwriteEntryGuards(t *testing.T) {
	p := overwritePath(t)
	s := &readStore{}
	for name, tc := range map[string]struct {
		ctx   context.Context
		store FileReadStore
		path  repository.Path
		cause error
		text  string
	}{
		"nil ctx":    {ctx: nil, store: s, path: p, cause: ErrNilFileOverwriteInput, text: ErrNilFileOverwriteInput.Error()},
		"nil store":  {ctx: context.Background(), store: nil, path: p, cause: ErrNilFileOverwriteInput, text: ErrNilFileOverwriteInput.Error()},
		"pre-cancel": {ctx: func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }(), store: s, path: p, cause: context.Canceled, text: "localrepo: file overwrite cancelled"},
	} {
		got, err := LookupFileOverwrite(tc.ctx, tc.path, tc.store, false, nil, nil)
		overwriteRequire(t, got == false, "%s: got=%v want false", name, got)
		overwriteRequire(t, err != nil && errors.Is(err, tc.cause), "%s: error=%v want cause=%v", name, err, tc.cause)
		overwriteRequire(t, err != nil && err.Error() == tc.text, "%s: text=%q want=%q", name, err, tc.text)
	}
	overwriteRequire(t, len(s.queries) == 0, "guards must precede any lookup, queries=%v", s.queries)

	// 空 repo 用零值 Path 构造器本来就会拒绝，直接以零值进入守卫。
	// A blank repo is rejected by NewPath anyway; feed the zero value to the guards.
	folder, ferr := repository.NewPath("repoA", "dir", "folder", false)
	overwriteRequire(t, ferr == nil, "path error=%v", ferr)
	rootFile, rferr := repository.NewPath("repoA", "", "", true)
	overwriteRequire(t, rferr == nil, "path error=%v", rferr)
	for _, invalid := range []repository.Path{{}, folder, rootFile} {
		got, err := LookupFileOverwrite(context.Background(), invalid, s, false, nil, nil)
		overwriteRequire(t, got == false, "invalid path got=%v", got)
		overwriteRequire(t, err != nil && errors.Is(err, repository.ErrInvalidPath), "invalid path error=%v", err)
		overwriteRequire(t, err.Error() == "localrepo: file overwrite invalid path", "invalid path text=%q", err)
	}
	overwriteRequire(t, len(s.queries) == 0, "invalid paths must not reach lookup, queries=%v", s.queries)
}

func TestLookupFileOverwriteAbsenceIsFalseNil(t *testing.T) {
	p := overwritePath(t)
	s := &readStore{err: sql.ErrNoRows}
	got, err := LookupFileOverwrite(context.Background(), p, s, false, nil, nil)
	overwriteRequire(t, err == nil, "absence error=%v want nil", err)
	overwriteRequire(t, got == false, "absence got=%v want false", got)
	overwriteRequire(t, len(s.queries) == 1, "absence queries=%d want 1", len(s.queries))
}

func TestLookupFileOverwritePostLookupCancelWinsEvenOnNoRows(t *testing.T) {
	p := overwritePath(t)
	lookupFailure := errors.New("boom-lookup")
	cases := []struct {
		name       string
		lookupErr  error
		wantCauses []error
	}{
		{"no rows", sql.ErrNoRows, []error{context.Canceled}},
		{"no error", nil, []error{context.Canceled}},
		{"lookup error", lookupFailure, []error{context.Canceled, lookupFailure}},
	}
	for _, tc := range cases {
		ctx, cancel := context.WithCancel(context.Background())
		s := &readStore{err: tc.lookupErr, afterLookup: cancel}
		got, err := LookupFileOverwrite(ctx, p, s, false, nil, nil)
		overwriteRequire(t, got == false, "%s: got=%v want false", tc.name, got)
		overwriteRequire(t, err != nil, "%s: error=nil want cancellation", tc.name)
		overwriteRequire(t, err.Error() == "localrepo: file overwrite cancelled", "%s: text=%q", tc.name, err)
		for _, cause := range tc.wantCauses {
			overwriteRequire(t, errors.Is(err, cause), "%s: error=%v missing cause=%v", tc.name, err, cause)
		}
	}
}

func TestLookupFileOverwriteLookupErrorPropagatesCause(t *testing.T) {
	p := overwritePath(t)
	cause := &readCause{}
	s := &readStore{err: cause}
	got, err := LookupFileOverwrite(context.Background(), p, s, false, nil, nil)
	overwriteRequire(t, got == false, "got=%v want false", got)
	overwriteRequire(t, errors.Is(err, cause), "error=%v want cause preserved", err)
	overwriteRequire(t, err.Error() == "localrepo: file overwrite lookup failed", "text=%q", err)
}

func TestLookupFileOverwriteRequiresExactPathIdentity(t *testing.T) {
	p := overwritePath(t)
	other, err := repository.NewPath("repoA", "dir", "other.bin", true)
	overwriteRequire(t, err == nil, "path error=%v", err)
	folder, err := repository.NewPath(p.Repo(), p.Parent(), p.Name(), false)
	overwriteRequire(t, err == nil, "path error=%v", err)
	for name, stored := range map[string]repository.Path{
		"different name":  other,
		"same key folder": folder,
		"zero path":       {},
	} {
		s := &readStore{source: overwriteSource(t, stored, "a1b2", true)}
		got, gerr := LookupFileOverwrite(context.Background(), p, s, false, nil, nil)
		overwriteRequire(t, got == false, "%s: got=%v want false", name, got)
		overwriteRequire(t, gerr != nil && errors.Is(gerr, repository.ErrInvalidFileSnapshotSource), "%s: error=%v want ErrInvalidFileSnapshotSource", name, gerr)
	}
}

func TestLookupFileOverwriteNilVersusEmptyChecksumDistinction(t *testing.T) {
	p := overwritePath(t)
	sha1Units := func(s string) []uint16 { return utf16.Encode([]rune(s)) }
	cases := []struct {
		name      string
		sha1      string
		sha1Valid bool
		request   []uint16
		want      bool
	}{
		{"nil request short-circuits true", "a1b2", true, nil, true},
		{"empty request vs stored text mismatches", "a1b2", true, []uint16{}, true},
		{"empty request vs stored empty matches", "", true, []uint16{}, false},
		{"request equal stored matches", "a1b2", true, sha1Units("a1b2"), false},
	}
	for _, tc := range cases {
		s := &readStore{source: overwriteSource(t, p, tc.sha1, tc.sha1Valid)}
		got, err := LookupFileOverwrite(context.Background(), p, s, false, tc.request, nil)
		overwriteRequire(t, err == nil, "%s: error=%v", tc.name, err)
		overwriteRequire(t, got == tc.want, "%s: got=%v want=%v", tc.name, got, tc.want)
	}
}

func TestLookupFileOverwriteInvalidStoredChecksumPropagatesCause(t *testing.T) {
	p := overwritePath(t)
	// valid=false 但非空字符串：委托层返回 ErrInvalidFileSnapshotSource，原样保留。
	s := &readStore{source: overwriteSource(t, p, "a1b2", false)}
	got, err := LookupFileOverwrite(context.Background(), p, s, false, nil, nil)
	overwriteRequire(t, got == false, "got=%v want false", got)
	overwriteRequire(t, err != nil && errors.Is(err, repository.ErrInvalidFileSnapshotSource), "error=%v want cause preserved", err)
	overwriteRequire(t, err.Error() == "localrepo: file overwrite decision failed", "text=%q", err)
}

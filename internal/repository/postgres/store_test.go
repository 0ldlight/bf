// 事务内文件引用投影的单元契约（先行）：只覆盖保证不触库的分支——nil 事务、
// 写前校验、预取消与 SQL 边界映射；真实 SQL 路径由集成门禁验证。
// Unit contract for the transaction-scoped file-reference projection,
// written first: only guaranteed no-DB branches — nil tx, write validation,
// pre-cancellation and the SQL boundary mapping; real SQL paths belong to
// the integration gate.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
)

// dummyTx 只用于保证不触库的分支；任何会执行 SQL 的路径都不得使用它。
// dummyTx exists solely for guaranteed no-DB branches; no SQL-executing
// path may ever use it.
func dummyTx() *sql.Tx { return &sql.Tx{} }

func contentMeta() content.Metadata {
	return content.Metadata{
		SHA1:   strings.Repeat("a", 40),
		SHA256: strings.Repeat("b", 64),
		MD5:    strings.Repeat("c", 32),
		Size:   11,
	}
}

func validRef() repository.FileReference {
	path, err := repository.NewPath("repo-x", "a/b", "f.bin", true)
	if err != nil {
		panic(err)
	}
	return repository.FileReference{
		ID:      7,
		Path:    path,
		Content: contentMeta(),
	}
}

func TestNewRejectsNilTx(t *testing.T) {
	got, err := New(nil)
	if err == nil {
		t.Fatal("New(nil) must fail")
	}
	if got != nil {
		t.Fatalf("New(nil) returned %v, want nil", got)
	}
	if err.Error() != "postgres nodes: nil transaction" {
		t.Fatalf("New(nil) error text = %q, want bounded constant", err.Error())
	}
}

func TestCreateRejectsInvalidReferenceBeforeSQL(t *testing.T) {
	oneMiB := strings.Repeat("x", 1<<20)
	cases := []struct {
		name string
		mut  func(*repository.FileReference)
	}{
		{"zero id", func(r *repository.FileReference) { r.ID = 0 }},
		{"negative id", func(r *repository.FileReference) { r.ID = -3 }},
		{"zero path", func(r *repository.FileReference) { r.Path = repository.Path{} }},
		{"folder kind", func(r *repository.FileReference) {
			p, err := repository.NewPath("repo-x", "a/b", "f.bin", false)
			if err != nil {
				t.Fatalf("NewPath(folder): %v", err)
			}
			r.Path = p
		}},
		{"file root", func(r *repository.FileReference) {
			p, err := repository.NewPath("repo-x", "", "", true)
			if err != nil {
				t.Fatalf("NewPath(root): %v", err)
			}
			r.Path = p
		}},
		{"dot parent", func(r *repository.FileReference) {
			p, err := repository.NewPath("repo-x", ".", "f.bin", true)
			if err != nil {
				t.Fatalf("NewPath(dot parent): %v", err)
			}
			r.Path = p
		}},
		{"dot name", func(r *repository.FileReference) {
			p, err := repository.NewPath("repo-x", "a", ".", true)
			if err != nil {
				t.Fatalf("NewPath(dot name): %v", err)
			}
			r.Path = p
		}},
		{"empty sha1", func(r *repository.FileReference) { r.Content.SHA1 = "" }},
		{"short sha1", func(r *repository.FileReference) { r.Content.SHA1 = "ab" }},
		{"uppercase sha1", func(r *repository.FileReference) { r.Content.SHA1 = strings.Repeat("A", 40) }},
		{"non-hex sha1", func(r *repository.FileReference) { r.Content.SHA1 = strings.Repeat("g", 40) }},
		{"empty md5", func(r *repository.FileReference) { r.Content.MD5 = "" }},
		{"uppercase sha256", func(r *repository.FileReference) { r.Content.SHA256 = strings.Repeat("B", 64) }},
		{"non-hex sha256", func(r *repository.FileReference) { r.Content.SHA256 = strings.Repeat("0", 63) + "z" }},
		{"negative size", func(r *repository.FileReference) { r.Content.Size = -1 }},
		{"1MiB sha1", func(r *repository.FileReference) { r.Content.SHA1 = oneMiB }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := New(dummyTx())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ref := validRef()
			tc.mut(&ref)
			n, err := st.Create(context.Background(), ref)
			if err == nil {
				t.Fatal("Create must reject the invalid reference")
			}
			if n != 0 {
				t.Fatalf("rejected Create count = %d, want 0", n)
			}
			if !errors.Is(err, ErrInvalidReference) {
				t.Fatalf("Create error must wrap ErrInvalidReference, got %v", err)
			}
			if s := err.Error(); strings.Contains(s, oneMiB) {
				t.Fatalf("error text echoes caller value: %q", s)
			}
			if len(err.Error()) > 200 {
				t.Fatalf("error text unbounded: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestUpdateRejectsInvalidReferenceBeforeSQL(t *testing.T) {
	st, err := New(dummyTx())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ref := validRef()
	ref.ID = 0
	n, err := st.Update(context.Background(), ref)
	if err == nil || n != 0 {
		t.Fatalf("Update(invalid id) = (%d, %v), want (0, error)", n, err)
	}
	if !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Update error must wrap ErrInvalidReference, got %v", err)
	}
}

func TestCreateDepthExceededPreservesSentinel(t *testing.T) {
	st, err := New(dummyTx())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	deep, err := repository.NewPath("repo-x", strings.Repeat("x/", 32766)+"y", "f.bin", true)
	if err != nil {
		t.Fatalf("NewPath(deep): %v", err)
	}
	ref := validRef()
	ref.Path = deep
	n, err := st.Create(context.Background(), ref)
	if err == nil || n != 0 {
		t.Fatalf("Create(deep) = (%d, %v), want (0, error)", n, err)
	}
	if !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("deep Create must still wrap ErrInvalidReference, got %v", err)
	}
	if !errors.Is(err, repository.ErrDepthExceeded) {
		t.Fatalf("deep Create must preserve ErrDepthExceeded, got %v", err)
	}
	if len(err.Error()) > 200 {
		t.Fatalf("deep error text unbounded: %d bytes", len(err.Error()))
	}
}

func TestLookupValidation(t *testing.T) {
	st, err := New(dummyTx())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	zero, err := st.Lookup(context.Background(), repository.Path{})
	if err == nil || zero != (repository.FileReference{}) {
		t.Fatalf("Lookup(zero path) = (%+v, %v), want zero ref + error", zero, err)
	}
	if !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Lookup(zero) error must wrap ErrInvalidReference, got %v", err)
	}
	dotParent, err := repository.NewPath("repo-x", ".", "f.bin", false)
	if err != nil {
		t.Fatalf("NewPath(dot parent): %v", err)
	}
	if _, err := st.Lookup(context.Background(), dotParent); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Lookup(dot parent) must be invalid, got %v", err)
	}
	dotName, err := repository.NewPath("repo-x", "a", ".", true)
	if err != nil {
		t.Fatalf("NewPath(dot name): %v", err)
	}
	if _, err := st.Lookup(context.Background(), dotName); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Lookup(dot name) must be invalid, got %v", err)
	}
	// 查询侧允许 folder Kind 与根查询：校验直接返回 nil，不触库。
	// Folder-kind and root queries are permitted: validation returns nil
	// without any SQL access.
	folder, err := repository.NewPath("repo-x", "a", "d", false)
	if err != nil {
		t.Fatalf("NewPath(folder): %v", err)
	}
	if err := validateIdentity(folder); err != nil {
		t.Fatalf("folder-kind query must pass validation: %v", err)
	}
	root, err := repository.NewPath("repo-x", "", "", false)
	if err != nil {
		t.Fatalf("NewPath(root): %v", err)
	}
	if err := validateIdentity(root); err != nil {
		t.Fatalf("root query must pass validation: %v", err)
	}
}

func TestPreCanceledValidCallsBeforeTxAccess(t *testing.T) {
	st, err := New(dummyTx())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := st.Create(ctx, validRef())
	if err == nil || n != 0 {
		t.Fatalf("pre-canceled Create = (%d, %v), want (0, error)", n, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create must preserve context.Canceled, got %v", err)
	}
	if s := err.Error(); s != "postgres nodes: cancelled before statement" {
		t.Fatalf("Create cancel text = %q, want fixed constant", s)
	}
	n, err = st.Update(ctx, validRef())
	if err == nil || n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Update = (%d, %v), want (0, context cause)", n, err)
	}
	path, perr := repository.NewPath("repo-x", "a/b", "f.bin", true)
	if perr != nil {
		t.Fatalf("NewPath: %v", perr)
	}
	got, err := st.Lookup(ctx, path)
	if err == nil || got != (repository.FileReference{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Lookup = (%+v, %v), want zero ref + context cause", got, err)
	}
}

func TestSQLBoundaryMapping(t *testing.T) {
	// 写侧仅空串映射为 '.'；Unicode 空白与非空串原样保留。
	// Writes map only the empty string to '.'; Unicode blanks and other
	// non-empty strings are retained verbatim.
	write := map[string]string{
		"":       ".",
		"a":      "a",
		".":      ".",
		" ":      " ",
		"\u00a0": "\u00a0",
	}
	for in, want := range write {
		if got := dotIfEmpty(in); got != want {
			t.Errorf("dotIfEmpty(%q) = %q, want %q", in, got, want)
		}
	}
	// 读侧 NULL/空/'.' 都归一为空；其余原样。
	// Reads map NULL/empty/'.' to empty; everything else stays verbatim.
	read := map[string]string{
		"":       "",
		".":      "",
		"a":      "a",
		" ":      " ",
		"\u00a0": "\u00a0",
	}
	for in, want := range read {
		if got := rowComponent(sql.NullString{String: in, Valid: true}); got != want {
			t.Errorf("rowComponent(%q) = %q, want %q", in, got, want)
		}
	}
	if got := rowComponent(sql.NullString{}); got != "" {
		t.Errorf("rowComponent(NULL) = %q, want empty", got)
	}
}

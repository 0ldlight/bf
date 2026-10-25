// LookupSnapshotSource 的入口守卫单元契约：只用零值 Store{}（nil 事务），
// 覆盖 SQL 之前保证不触库的顺序——非法身份先于预取消；合法路径在预取消时
// 返回既定阶段错误且绝不解引用 nil 事务；与旧 Lookup 的阶段完全一致。
// Unit contract for the LookupSnapshotSource entry guards: a zero Store{}
// (nil transaction) only, covering the guaranteed pre-SQL order — invalid
// identity wins over pre-cancellation; a valid path under pre-cancellation
// returns the fixed stage error and never dereferences the nil transaction;
// the stages match the old Lookup exactly.
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/0ldlight/bf/internal/repository"
)

// TestLookupSnapshotSourceInvalidIdentityWinsOverPreCancel：预取消语境下，
// 非法身份（零 Path、字面 '.' 组件）必须先于取消检查被拒；两个读取出口同
// 文案、同原因链。
// TestLookupSnapshotSourceInvalidIdentityWinsOverPreCancel: under a
// pre-cancelled context an invalid identity (zero Path, literal '.'
// component) must be rejected before the cancellation check; both read exits
// share the text and cause chain.
func TestLookupSnapshotSourceInvalidIdentityWinsOverPreCancel(t *testing.T) {
	st := &Store{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dotName, err := repository.NewPath("repo-x", "a", ".", true)
	if err != nil {
		t.Fatalf("NewPath(dot name): %v", err)
	}
	for name, path := range map[string]repository.Path{
		"zero path": {},
		"dot name":  dotName,
	} {
		src, gerr := st.LookupSnapshotSource(ctx, path)
		if gerr == nil || src != (repository.FileSnapshotSource{}) {
			t.Fatalf("%s: LookupSnapshotSource = (%+v, %v), want zero source + error", name, src, gerr)
		}
		if !errors.Is(gerr, ErrInvalidReference) {
			t.Fatalf("%s: error must wrap ErrInvalidReference, got %v", name, gerr)
		}
		if errors.Is(gerr, context.Canceled) {
			t.Fatalf("%s: invalid identity must win over pre-cancel, got %v", name, gerr)
		}
		if s := gerr.Error(); s != "postgres nodes: invalid reference" {
			t.Fatalf("%s: Error() = %q, want fixed stageInvalid text", name, s)
		}
		if _, lerr := st.Lookup(ctx, path); !errors.Is(lerr, ErrInvalidReference) ||
			errors.Is(lerr, context.Canceled) || lerr.Error() != gerr.Error() {
			t.Fatalf("%s: old Lookup = %v, want the same invalid-identity stage", name, lerr)
		}
	}
}

// TestLookupSnapshotSourcePreCancelNeverTouchesNilTx：合法路径 + 预取消必须
// 在任何事务访问前返回固定取消阶段与零 Source；零值 Store 的 nil 事务一旦
// 被解引用即 panic，本用例因此同时是顺序与不触库的证明。
// TestLookupSnapshotSourcePreCancelNeverTouchesNilTx: a valid path under
// pre-cancellation must return the fixed cancelled stage and zero Source
// before any transaction access; dereferencing the zero Store's nil
// transaction would panic, so this doubles as the ordering and no-SQL proof.
func TestLookupSnapshotSourcePreCancelNeverTouchesNilTx(t *testing.T) {
	st := &Store{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path, err := repository.NewPath("repo-x", "a/b", "f.bin", true)
	if err != nil {
		t.Fatalf("NewPath: %v", err)
	}
	src, gerr := st.LookupSnapshotSource(ctx, path)
	if gerr == nil || src != (repository.FileSnapshotSource{}) {
		t.Fatalf("LookupSnapshotSource = (%+v, %v), want zero source + error", src, gerr)
	}
	if !errors.Is(gerr, context.Canceled) {
		t.Fatalf("error must wrap context.Canceled, got %v", gerr)
	}
	if s := gerr.Error(); s != "postgres nodes: cancelled before statement" {
		t.Fatalf("Error() = %q, want fixed stageCancelled text", s)
	}
	if _, lerr := st.Lookup(ctx, path); !errors.Is(lerr, context.Canceled) ||
		lerr.Error() != gerr.Error() {
		t.Fatalf("old Lookup = %v, want the same cancelled stage", lerr)
	}
}

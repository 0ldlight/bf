// NewPrincipal 测试缝的包内契约：字段往返与零值语义保持。
// In-package contract for the NewPrincipal testability seam: field
// round-trip and preserved zero-value semantics.
package security

import "testing"

func TestNewPrincipalSeam(t *testing.T) {
	p := NewPrincipal(7, "op-user")
	if p.ID() != 7 || p.Username() != "op-user" {
		t.Fatalf("NewPrincipal = (%d, %q), want (7, op-user)", p.ID(), p.Username())
	}
	// 不做校验：非正 id / 空 username 保持零值语义，由消费方守卫拒绝。
	// No validation: a nonpositive id or empty username keeps zero-value
	// semantics for consumers to reject.
	zero := NewPrincipal(0, "")
	if zero.ID() != 0 || zero.Username() != "" {
		t.Fatalf("NewPrincipal zero inputs = (%d, %q), want zero values", zero.ID(), zero.Username())
	}
	if zero != (Principal{}) {
		t.Fatal("NewPrincipal zero inputs must equal the zero Principal")
	}
}

package security

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 令牌耗尽时配置超时也必须终止等待，不只约束 SQL。
// The configured operation timeout must also bound waiting for a KDF slot.
func TestSecurityStoreTimeoutIncludesKDFWait(t *testing.T) {
	s := &Store{timeout: time.Millisecond, kdfSlots: make(chan struct{}, 1)}
	s.kdfSlots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		p, err := s.Authenticate(ctx, "reader", "Controlled-passphrase-123!")
		if p.ID() != 0 {
			done <- errors.New("unexpected principal")
			return
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("wait error=%v", err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("configured timeout did not bound KDF slot wait")
	}
}

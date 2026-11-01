package postgresit

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/repository"
)

// usConcurrentReader synchronizes the first actual read of both streams.
// It never touches SQL and gives independent bodies to the real publisher.
type usConcurrentReader struct {
	r       io.Reader
	once    sync.Once
	entered chan<- struct{}
	release <-chan struct{}
	ctx     context.Context
}

func (r *usConcurrentReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		r.entered <- struct{}{}
		select {
		case <-r.release:
		case <-r.ctx.Done():
		}
	})
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func TestUploadSaveConcurrentPublicationTwoConnectionNodeGate(t *testing.T) {
	f := usSetup(t) // One shared Saver and exactly two connections, not two savers.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	blocker, err := f.adm.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("lock transaction begin failed")
	}
	defer blocker.Rollback()
	// The lock is independent of the two-connection writer pool. Both registry
	// commits remain available, while the first real node SELECT is observable.
	if _, err = blocker.ExecContext(ctx, fmt.Sprintf("LOCK TABLE %s.nodes IN ACCESS EXCLUSIVE MODE", f.schema)); err != nil {
		t.Fatal("node lock failed")
	}
	payloads := [2]string{"parallel first publication bytes\n", "parallel second publication bytes\n"}
	inputs := [2]struct{ path, payload string }{{"parallel/a.bin", payloads[0]}, {"parallel/b.bin", payloads[1]}}
	type outcome struct {
		i      int
		source repository.FileSnapshotSource
		err    error
	}
	out := make(chan outcome, 2)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	defer func() { cancel(); _ = blocker.Rollback(); wg.Wait() }()
	for i, v := range inputs {
		in := usInput(t, "parallelRepo", v.path, "writer", v.payload)
		wg.Add(1)
		go func(i int, payload string) {
			defer wg.Done()
			src, e := f.saver.Save(ctx, &usConcurrentReader{r: strings.NewReader(payload), entered: entered, release: release, ctx: ctx}, in)
			out <- outcome{i, src, e}
		}(i, v.payload)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both real body readers did not enter")
		}
	}
	close(release)
	// Both binaries must commit before the locked node phase is released.
	// This is the positive assertion that publication is outside the node gate.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var n int
		if err = f.adm.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s.binaries", f.schema)).Scan(&n); err != nil {
			t.Fatal("publication observation failed")
		}
		if n == 2 {
			break
		}
		select {
		case got := <-out:
			t.Fatalf("save returned before locked node phase: %v", got.err)
		case <-ctx.Done():
			t.Fatal("parallel publication did not commit both binaries")
		case <-ticker.C:
		}
	}
	// A lock-backed observation window permits both scheduled requests to reach
	// the gate. Without the gate, a second node SELECT occupies connection two.
	// A non-event cannot be proved instantaneously: this bounded window is an
	// explicit scheduling oracle, not a claim of deterministic virtual time.
	observedOne := false
	until := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(until) {
		var waiting int
		err = f.adm.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE relation=$1::regclass AND NOT granted`, f.schema+".nodes").Scan(&waiting)
		if err != nil {
			t.Fatal("node lock observation failed")
		}
		if waiting == 1 {
			observedOne = true
		}
		if waiting > 1 {
			t.Fatal("multiple node transactions admitted with two-connection pool")
		}
		select {
		case <-ctx.Done():
			t.Fatal("gate observation deadline")
		case <-ticker.C:
		}
	}
	if !observedOne {
		t.Fatal("no real node query was observed waiting for the SQL lock")
	}
	if err = blocker.Rollback(); err != nil {
		t.Fatal("node lock release failed")
	}
	ids := map[int64]bool{}
	for i := 0; i < 2; i++ {
		select {
		case got := <-out:
			if got.err != nil {
				t.Fatalf("concurrent save failed: %v", got.err)
			}
			in := usInput(t, "parallelRepo", inputs[got.i].path, "writer", inputs[got.i].payload)
			usRequireSource(t, got.source, in, got.source.Reference.ID, inputs[got.i].payload)
			if got.source.Reference.ID <= 0 || ids[got.source.Reference.ID] {
				t.Fatal("nonpositive or duplicated reservation ID")
			}
			ids[got.source.Reference.ID] = true
			sha1, _, _ := usDigests(inputs[got.i].payload)
			cfRequireObjectBytes(t, f.store, sha1, inputs[got.i].payload)
		case <-ctx.Done():
			t.Fatal("two-connection concurrent save deadlocked")
		}
	}
	if countNodes(t, f.adm, f.schema) != 2 || countRows(t, f.adm, f.schema) != 2 || cfCountFiles(t, f.root) != 2 {
		t.Fatal("concurrent committed state mismatch")
	}
	cfRequireBound(t, f.adm, f.schema, 2)
}

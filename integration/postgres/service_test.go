// binary.Service + 真实 filestore + 真实 PostgreSQL：登记失败保留已写字节，
// 换新读取器重试成功（移除自有注入约束后）。
// binary.Service with the real filestore and real PostgreSQL: a failed
// registration keeps the written bytes and a fresh-reader retry succeeds
// once the owned injected constraint is removed.
package postgresit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
)

func TestServiceRegisterFailurePreservesBytesAndRetrySucceeds(t *testing.T) {
	reg, schema := newRegistry(t)
	adm := adminPool(t)
	ctx := context.Background()

	root := t.TempDir()
	store, err := filestore.New(root)
	if err != nil {
		t.Fatalf("filestore.New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	svc, err := binary.New(store, reg)
	if err != nil {
		t.Fatalf("binary.New: %v", err)
	}

	// 自有的 BEFORE INSERT 触发器让登记在事务中途失败。
	// An owned BEFORE INSERT trigger fails the registration mid-
	// transaction.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"CREATE FUNCTION %s.bfreg_block_insert() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'injected before-insert failure'; END $$ LANGUAGE plpgsql", schema)); err != nil {
		t.Fatalf("create function: %v", err)
	}
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"CREATE TRIGGER bfreg_block_insert BEFORE INSERT ON %s.binaries FOR EACH ROW EXECUTE FUNCTION %s.bfreg_block_insert()", schema, schema)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	payload := "binflow postgres registry integration payload"
	staged, err := content.Calculate(ctx, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("content.Calculate: %v", err)
	}

	got, err := svc.Add(ctx, strings.NewReader(payload), filestore.Expected{})
	if err == nil {
		t.Fatal("Add must fail while registration fails")
	}
	if got != (content.Metadata{}) {
		t.Fatalf("failed Add returned %+v, want zero Metadata", got)
	}
	if s := err.Error(); s != "binary: registry register failed" {
		t.Fatalf("Error() = %q, want the fixed register stage text", s)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("driver cause must stay reachable through both wrappers, got %v", err)
	}

	// 登记失败不删除/补偿内容：对象仍然可 Open 且字节一致。
	// The registry failure never deletes or compensates content: the
	// object stays Open-able with identical bytes.
	obj, err := store.Open(ctx, staged.SHA1)
	if err != nil {
		t.Fatalf("Open after failed registration: %v", err)
	}
	gotBytes, readErr := io.ReadAll(obj)
	obj.Close()
	if readErr != nil {
		t.Fatalf("read object: %v", readErr)
	}
	if string(gotBytes) != payload {
		t.Fatal("stored bytes changed after a failed registration")
	}

	// 移除自有注入后，换新读取器重试成功。
	// After removing the owned injection, a fresh-reader retry succeeds.
	if _, err := adm.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER bfreg_block_insert ON %s.binaries", schema)); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	retryCtx, retryCancel := context.WithTimeout(ctx, 15*time.Second)
	defer retryCancel()
	got, err = svc.Add(retryCtx, strings.NewReader(payload), filestore.Expected{})
	if err != nil {
		t.Fatalf("Add retry after removing constraint: %v", err)
	}
	if got != staged {
		t.Fatalf("retry returned %+v, want %+v", got, staged)
	}
	_, _, _, _, ok := rowOf(t, adm, schema, staged.SHA1)
	if !ok {
		t.Fatal("retry must leave the registered row")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("store root vanished: %v", err)
	}
}

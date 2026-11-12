package process

// 本文件以脚本化数据库覆盖上传装配的真实阶段链：checkUploadWriter 的
// 权限探针/锁调用/回滚契约与 buildUpload 的预留读取、唯一预留权复核、
// 安全后端校验及最终入口构造（成功与各阶段失败）。
// This file covers the real staged upload assembly through scripted
// databases: checkUploadWriter's privilege probe, lock call and rollback
// contract, and buildUpload's reservation read, the unique-ids reservation
// privilege recheck, the security backend validation and the final entry
// construction (both success and each stage failure).

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/filestore"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
	"github.com/0ldlight/bf/internal/security"
)

// uasWriterResponder 应答写方连接上的全部语句。
// uasWriterResponder answers every statement on the writer connection.
func uasWriterResponder(privileges bool, lockErr error, versionOK bool, repoOK bool) func(context.Context, string, []driver.Value) psResult {
	return func(_ context.Context, query string, _ []driver.Value) psResult {
		switch {
		case strings.Contains(query, "has_table_privilege('security_write_grants'"):
			return psResult{row: []driver.Value{privileges}}
		case strings.Contains(query, "lock_upload_commit()"):
			if lockErr != nil {
				return psResult{err: lockErr}
			}
			return psResult{affected: 0}
		case strings.Contains(query, "FROM security_schema_version"):
			if !versionOK {
				return psResult{row: []driver.Value{int64(1), int64(2), int64(2)}}
			}
			return psResult{row: []driver.Value{int64(1), int64(1), int64(1)}}
		case strings.Contains(query, "WHERE false"):
			return psResult{}
		case strings.Contains(query, "FROM security_repositories WHERE repo_key"):
			if !repoOK {
				return psResult{}
			}
			return psResult{row: []driver.Value{"local-repo", "local", "generic", int64(4)}}
		}
		return psResult{err: psUnexpected(query)}
	}
}

// uasReservationResponder 应答预留方连接上的全部语句。
// uasReservationResponder answers every statement on the reservation
// connection.
func uasReservationResponder(currentErr error, canReserve bool) func(context.Context, string, []driver.Value) psResult {
	return func(_ context.Context, query string, _ []driver.Value) psResult {
		switch {
		case strings.Contains(query, "FROM unique_ids"):
			if currentErr != nil {
				return psResult{err: currentErr}
			}
			return psResult{row: []driver.Value{int64(41)}}
		case strings.Contains(query, "has_table_privilege('unique_ids'"):
			return psResult{row: []driver.Value{canReserve}}
		}
		return psResult{err: psUnexpected(query)}
	}
}

// TestCheckUploadWriterContract 断言写方检查契约：权限探针不支持/失败均
// 为 ErrStartup、锁调用失败保留原因、成功路径恰好一次回滚且事务提交绝不
// 发生。
// TestCheckUploadWriterContract asserts the writer check contract: an
// unsupported or failing privilege probe answers ErrStartup, a lock-call
// failure keeps its cause, and the successful path rolls back exactly once
// while never committing.
func TestCheckUploadWriterContract(t *testing.T) {
	ctx := context.Background()

	t.Run("unsupported privileges", func(t *testing.T) {
		db, script := psOpenDB(t, uasWriterResponder(false, nil, true, true))
		if err := checkUploadWriter(ctx, db, "binflow_sec"); !errors.Is(err, ErrStartup) {
			t.Fatalf("unsupported privileges error = %v, want ErrStartup", err)
		}
		for _, event := range script.snapshotEvents() {
			if event == "tx-commit" {
				t.Fatal("failed check committed its transaction")
			}
		}
	})

	t.Run("privilege probe failure", func(t *testing.T) {
		db, _ := psOpenDB(t, func(_ context.Context, query string, _ []driver.Value) psResult {
			return psResult{err: errors.New("probe-boom")}
		})
		if err := checkUploadWriter(ctx, db, "binflow_sec"); !errors.Is(err, ErrStartup) {
			t.Fatalf("probe failure error = %v, want ErrStartup", err)
		}
	})

	t.Run("lock call failure keeps cause", func(t *testing.T) {
		cause := errors.New("lock-boom")
		db, _ := psOpenDB(t, uasWriterResponder(true, cause, true, true))
		err := checkUploadWriter(ctx, db, "binflow_sec")
		if !errors.Is(err, cause) {
			t.Fatalf("lock failure error = %v, want passthrough %v", err, cause)
		}
	})

	t.Run("success rolls back exactly once", func(t *testing.T) {
		db, script := psOpenDB(t, uasWriterResponder(true, nil, true, true))
		if err := checkUploadWriter(ctx, db, "binflow_sec"); err != nil {
			t.Fatalf("successful check rejected: %v", err)
		}
		rollbacks := 0
		for _, event := range script.snapshotEvents() {
			switch event {
			case "tx-rollback":
				rollbacks++
			case "tx-commit":
				t.Fatal("successful check committed its transaction")
			}
		}
		if rollbacks != 1 {
			t.Fatalf("rollbacks = %d, want exactly 1", rollbacks)
		}
		queries := script.snapshotQueries()
		lock := 0
		for _, q := range queries {
			if strings.Contains(q.Query, `"binflow_sec".lock_upload_commit()`) {
				lock++
			}
		}
		if lock != 1 {
			t.Fatalf("schema-qualified lock calls = %d, want 1", lock)
		}
	})
}

// uasPools 构造带显式容量上限的脚本化写方/预留方连接池。
// uasPools builds the scripted writer/reservation pools with explicit
// capacity bounds.
func uasPools(t *testing.T, writerResponder, reservationResponder func(context.Context, string, []driver.Value) psResult) (*sql.DB, *psScript, *sql.DB, *psScript) {
	t.Helper()
	writerDB, writerScript := psOpenDB(t, writerResponder)
	writerDB.SetMaxOpenConns(8)
	reservationDB, reservationScript := psOpenDB(t, reservationResponder)
	reservationDB.SetMaxOpenConns(1)
	return writerDB, writerScript, reservationDB, reservationScript
}

// TestBuildUploadStagedFailures 断言装配的阶段失败：预留读取失败保留原
// 因、唯一预留权不支持为 ErrStartup、安全后端校验失败传播为
// ErrConfiguration。
// TestBuildUploadStagedFailures asserts the staged assembly failures: a
// reservation read failure keeps its cause, an unsupported unique-ids
// reservation privilege answers ErrStartup, and a failing security backend
// validation propagates as ErrConfiguration.
func TestBuildUploadStagedFailures(t *testing.T) {
	cfg := udsArtifactsConfig(
		"postgres://writer:pw@127.0.0.1:5432/binflow?sslmode=disable",
		"postgres://alloc:pw@127.0.0.1:5432/binflow?sslmode=disable",
	)
	_ = cfg

	t.Run("reservation read failure keeps cause", func(t *testing.T) {
		cause := errors.New("unique-ids-boom")
		writerDB, _, reservationDB, _ := uasPools(t, uasWriterResponder(true, nil, true, true), uasReservationResponder(cause, true))
		nodes, err := refpg.NewReadPool(writerDB, operationTimeout)
		if err != nil {
			t.Fatalf("read pool: %v", err)
		}
		fs, err := filestore.New(t.TempDir())
		if err != nil {
			t.Fatalf("filestore: %v", err)
		}
		t.Cleanup(func() { _ = fs.Close() })
		_, err = buildUpload(context.Background(), cfg, "binflow_sec", writerDB, reservationDB, nodes, fs)
		if !errors.Is(err, cause) {
			t.Fatalf("reservation read error = %v, want passthrough %v", err, cause)
		}
	})

	t.Run("unique ids privilege unsupported", func(t *testing.T) {
		writerDB, _, reservationDB, _ := uasPools(t, uasWriterResponder(true, nil, true, true), uasReservationResponder(nil, false))
		nodes, err := refpg.NewReadPool(writerDB, operationTimeout)
		if err != nil {
			t.Fatalf("read pool: %v", err)
		}
		fs, err := filestore.New(t.TempDir())
		if err != nil {
			t.Fatalf("filestore: %v", err)
		}
		t.Cleanup(func() { _ = fs.Close() })
		_, err = buildUpload(context.Background(), cfg, "binflow_sec", writerDB, reservationDB, nodes, fs)
		if !errors.Is(err, ErrStartup) {
			t.Fatalf("unique-ids privilege error = %v, want ErrStartup", err)
		}
	})

	t.Run("saver pool bound enforced", func(t *testing.T) {
		writerDB, _, reservationDB, _ := uasPools(t, uasWriterResponder(true, nil, true, true), uasReservationResponder(nil, true))
		writerDB.SetMaxOpenConns(1)
		nodes, err := refpg.NewReadPool(writerDB, operationTimeout)
		if err != nil {
			t.Fatalf("read pool: %v", err)
		}
		fs, err := filestore.New(t.TempDir())
		if err != nil {
			t.Fatalf("filestore: %v", err)
		}
		t.Cleanup(func() { _ = fs.Close() })
		_, err = buildUpload(context.Background(), cfg, "binflow_sec", writerDB, reservationDB, nodes, fs)
		if err == nil || errors.Is(err, ErrStartup) || errors.Is(err, ErrConfiguration) {
			t.Fatalf("single-connection writer pool error = %v, want the saver pool-bound failure", err)
		}
	})

	t.Run("security backend validation propagates", func(t *testing.T) {
		writerDB, _, reservationDB, _ := uasPools(t, uasWriterResponder(true, nil, false, true), uasReservationResponder(nil, true))
		nodes, err := refpg.NewReadPool(writerDB, operationTimeout)
		if err != nil {
			t.Fatalf("read pool: %v", err)
		}
		fs, err := filestore.New(t.TempDir())
		if err != nil {
			t.Fatalf("filestore: %v", err)
		}
		t.Cleanup(func() { _ = fs.Close() })
		_, err = buildUpload(context.Background(), cfg, "binflow_sec", writerDB, reservationDB, nodes, fs)
		if !errors.Is(err, security.ErrConfiguration) {
			t.Fatalf("backend validation error = %v, want security ErrConfiguration", err)
		}
	})
}

// TestBuildUploadSuccess 断言成功装配：全部阶段语句按需执行且最终入口可
// 用（处理器非空）。
// TestBuildUploadSuccess asserts the successful assembly: every staged
// statement runs as needed and the final entry is usable (a nonnil
// handler).
func TestBuildUploadSuccess(t *testing.T) {
	cfg := udsArtifactsConfig(
		"postgres://writer:pw@127.0.0.1:5432/binflow?sslmode=disable",
		"postgres://alloc:pw@127.0.0.1:5432/binflow?sslmode=disable",
	)
	writerDB, writerScript, reservationDB, reservationScript := uasPools(t,
		uasWriterResponder(true, nil, true, true), uasReservationResponder(nil, true))
	nodes, err := refpg.NewReadPool(writerDB, operationTimeout)
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	fs, err := filestore.New(t.TempDir())
	if err != nil {
		t.Fatalf("filestore: %v", err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	handler, err := buildUpload(context.Background(), cfg, "binflow_sec", writerDB, reservationDB, nodes, fs)
	if err != nil || handler == nil {
		t.Fatalf("buildUpload = (%v,%v), want handler", handler != nil, err)
	}
	for _, needle := range []string{"has_table_privilege('security_write_grants'", `"binflow_sec".lock_upload_commit()`} {
		if len(psQueriesWith(writerScript, needle)) == 0 {
			t.Fatalf("writer connection never ran %q", needle)
		}
	}
	for _, needle := range []string{"FROM unique_ids", "has_table_privilege('unique_ids'"} {
		if len(psQueriesWith(reservationScript, needle)) == 0 {
			t.Fatalf("reservation connection never ran %q", needle)
		}
	}
}

// psQueriesWith 返回文本包含 needle 的已记录查询。
// psQueriesWith returns recorded queries whose text contains needle.
func psQueriesWith(script *psScript, needle string) []psQuery {
	var found []psQuery
	for _, q := range script.snapshotQueries() {
		if strings.Contains(q.Query, needle) {
			found = append(found, q)
		}
	}
	return found
}

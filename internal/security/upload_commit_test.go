package security

// 本文件覆盖上传提交重检（RecheckUploadCommit）的真实行为：会话/事务/模式
// 输入守卫、READ COMMITTED + 受信模式兼容检查、锁调用与 search_path 的
// 固定 SQL 形状、以及经真实事务的 deploy/node/delete 决策链。
// This file covers the real RecheckUploadCommit behavior: the session/tx/
// schema input guards, the READ COMMITTED plus trusted-schema compatibility
// check, the fixed SQL shapes of the lock call and search_path, and the
// deploy/node/delete decision chain over a real transaction.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

const ssCommitStage = "security: upload commit lock"

// ssBeginTx 在脚本化 db 上开启真实事务并注册回滚清理；失败即终止测试。
// ssBeginTx begins a real transaction on the scripted db with a rollback
// cleanup, failing the test on error.
func ssBeginTx(t *testing.T, responder func(context.Context, string, []driver.Value) ssResult) *sql.Tx {
	t.Helper()
	db, _ := ssOpenDB(t, responder)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

// ssCommitResponder 构造"兼容→锁→deploy 授权→节点缺席"的脚本应答，并
// 记录是否需要拒绝 deploy。
// ssCommitResponder builds the compatibility→lock→deploy-grant→absent-node
// scripted reply, with a switch for denying the deploy grant.
func ssCommitResponder(grantAllowed bool) func(context.Context, string, []driver.Value) ssResult {
	return func(_ context.Context, query string, args []driver.Value) ssResult {
		switch {
		case strings.Contains(query, "current_schema()"):
			return ssResult{row: []driver.Value{true}}
		case strings.Contains(query, "SET LOCAL search_path"):
			return ssResult{affected: 0}
		case strings.Contains(query, "lock_upload_commit()"):
			return ssResult{affected: 0}
		case strings.Contains(query, "security_write_grants"):
			if len(args) != 5 || args[4] != "deploy" {
				return ssResult{err: errors.New("security-test: unexpected write-grant args")}
			}
			return ssResult{row: []driver.Value{grantAllowed}}
		case strings.Contains(query, "FROM nodes WHERE repo"):
			return ssResult{}
		}
		return ssResult{err: ssUnexpected(query)}
	}
}

// TestSecurityRecheckUploadCommitValidation 断言输入守卫：nil ctx、nil 事务
// 与一切非法模式名（大小写、前导数字、超长、空白、连字符）都拒绝为固定
// ErrConfiguration 文案，且绝不触库。注意：本守卫只做标识符正则，pg_ 前缀
// 由调用方（进程装配）拒绝，不属于本函数契约。
// TestSecurityRecheckUploadCommitValidation asserts the input guards: nil
// ctx, a nil transaction and every illegal schema name (case, leading digit,
// overlong, whitespace, hyphen) reject with the fixed ErrConfiguration text
// without touching the database. Note: this guard is the identifier regex
// only; the pg_ prefix is rejected by the caller (process assembly), not by
// this function's contract.
func TestSecurityRecheckUploadCommitValidation(t *testing.T) {
	principal, repo, path := ssRepoPath(t)
	db, script := ssOpenDB(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
		return ssResult{err: ssUnexpected(query)}
	})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, schema := range []string{"", "Binflow", "1abc", "has space", strings.Repeat("a", 64), "a-b"} {
		got, err := RecheckUploadCommit(context.Background(), tx, schema, principal, repo, path, false, nil, nil)
		if got {
			t.Fatalf("schema %q allowed = true", schema)
		}
		ssAssertStoreError(t, err, ErrConfiguration, ssCommitStage, errInvalidUploadInput)
	}
	got, err := RecheckUploadCommit(nil, tx, "binflow_sec", principal, repo, path, false, nil, nil)
	if got {
		t.Fatal("nil ctx allowed = true")
	}
	ssAssertStoreError(t, err, ErrConfiguration, ssCommitStage, errInvalidUploadInput)
	got, err = RecheckUploadCommit(context.Background(), nil, "binflow_sec", principal, repo, path, false, nil, nil)
	if got {
		t.Fatal("nil tx allowed = true")
	}
	ssAssertStoreError(t, err, ErrConfiguration, ssCommitStage, errInvalidUploadInput)
	if n := len(script.snapshotQueries()); n != 0 {
		t.Fatalf("validation executed %d queries, want 0", n)
	}
}

// TestSecurityRecheckUploadCommitSessionGuard 断言兼容检查：SQL 失败保留
// 原因；current_schema 或隔离级别不符时以固定错误拒绝且不再加锁。
// TestSecurityRecheckUploadCommitSessionGuard asserts the compatibility
// check: an SQL failure keeps its cause; a current_schema or isolation
// mismatch rejects with the fixed error and never takes the lock.
func TestSecurityRecheckUploadCommitSessionGuard(t *testing.T) {
	principal, repo, path := ssRepoPath(t)

	t.Run("compatibility sql failure keeps cause", func(t *testing.T) {
		cause := errors.New("compat-boom")
		tx := ssBeginTx(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: cause}
		})
		got, err := RecheckUploadCommit(context.Background(), tx, "binflow_sec", principal, repo, path, false, nil, nil)
		if got {
			t.Fatal("compatibility failure allowed = true")
		}
		ssAssertStoreError(t, err, ErrSecurityStore, ssCommitStage, cause)
	})

	t.Run("incompatible session never locks", func(t *testing.T) {
		db, script := ssOpenDB(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "current_schema()") {
				return ssResult{row: []driver.Value{false}}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		got, err := RecheckUploadCommit(context.Background(), tx, "binflow_sec", principal, repo, path, false, nil, nil)
		if got {
			t.Fatal("incompatible session allowed = true")
		}
		ssAssertStoreError(t, err, ErrConfiguration, ssCommitStage, errUploadCommitSession)
		for _, q := range script.snapshotQueries() {
			if strings.Contains(q.Query, "lock_upload_commit") {
				t.Fatalf("incompatible session took the lock: %q", q.Query)
			}
		}
	})
}

// TestSecurityRecheckUploadCommitLockFailures 断言锁阶段失败保留原因：
// search_path 设置与锁调用任一失败都以固定阶段返回原始错误。
// TestSecurityRecheckUploadCommitLockFailures asserts the lock-phase failures
// keep their causes: either the search_path setup or the lock call failing
// returns the original error under the fixed stage.
func TestSecurityRecheckUploadCommitLockFailures(t *testing.T) {
	principal, repo, path := ssRepoPath(t)
	for _, tc := range []struct {
		name   string
		failOn string
	}{
		{"search path setup", "SET LOCAL search_path"},
		{"lock call", "lock_upload_commit()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("lock-phase-boom")
			tx := ssBeginTx(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
				if strings.Contains(query, tc.failOn) {
					return ssResult{err: cause}
				}
				if strings.Contains(query, "current_schema()") {
					return ssResult{row: []driver.Value{true}}
				}
				if strings.Contains(query, "SET LOCAL search_path") {
					return ssResult{affected: 0}
				}
				return ssResult{err: ssUnexpected(query)}
			})
			got, err := RecheckUploadCommit(context.Background(), tx, "binflow_sec", principal, repo, path, false, nil, nil)
			if got {
				t.Fatal("lock failure allowed = true")
			}
			ssAssertStoreError(t, err, ErrSecurityStore, ssCommitStage, cause)
		})
	}
}

// TestSecurityRecheckUploadCommitOverwriteNeedsDelete 断言覆盖写路径：节
// 点已存在且请求未带摘要事实时判定为覆盖，随后在同一事务上以 delete
// 动作复核写授权；删除被拒则整体拒绝。
// TestSecurityRecheckUploadCommitOverwriteNeedsDelete asserts the overwrite
// path: with an existing node and no request digest facts the decision is an
// overwrite, and the write grant is then rechecked on the same transaction
// with the delete action; a denied delete rejects the whole commit.
func TestSecurityRecheckUploadCommitOverwriteNeedsDelete(t *testing.T) {
	principal, repo, path := ssRepoPath(t)
	var actions []string
	nodeRow := []driver.Value{
		int64(9), int64(1), "repoA", "dir", "file.bin", int64(3),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	}
	deleteAllowed := false
	tx := ssBeginTx(t, func(_ context.Context, query string, args []driver.Value) ssResult {
		switch {
		case strings.Contains(query, "current_schema()"):
			return ssResult{row: []driver.Value{true}}
		case strings.Contains(query, "SET LOCAL search_path"), strings.Contains(query, "lock_upload_commit()"):
			return ssResult{affected: 0}
		case strings.Contains(query, "security_write_grants"):
			actions = append(actions, args[4].(string))
			return ssResult{row: []driver.Value{actions[len(actions)-1] == "deploy" || deleteAllowed}}
		case strings.Contains(query, "FROM nodes WHERE repo"):
			return ssResult{row: nodeRow}
		}
		return ssResult{err: ssUnexpected(query)}
	})
	got, err := RecheckUploadCommit(context.Background(), tx, "binflow_sec", principal, repo, path, false, nil, nil)
	if err != nil {
		t.Fatalf("overwrite recheck error = %v", err)
	}
	if got {
		t.Fatal("overwrite without delete grant allowed = true")
	}
	if len(actions) != 2 || actions[0] != "deploy" || actions[1] != "delete" {
		t.Fatalf("grant actions = %v, want [deploy delete]", actions)
	}
}

// TestSecurityRecheckUploadCommitDecisionChain 断言成功链路：兼容通过后
// 依次 SET LOCAL（受信模式 + pg_temp）与调用模式限定锁；deploy 授权为否
// 时立即 false 且不查节点；deploy 为真且节点缺席时放行。
// TestSecurityRecheckUploadCommitDecisionChain asserts the successful chain:
// after compatibility it sets the search path (trusted schema plus pg_temp)
// and calls the schema-qualified lock; a denied deploy answers false without
// any node lookup, while an allowed deploy with an absent node allows.
func TestSecurityRecheckUploadCommitDecisionChain(t *testing.T) {
	principal, repo, path := ssRepoPath(t)

	t.Run("denied deploy skips nodes", func(t *testing.T) {
		db, script := ssOpenDB(t, ssCommitResponder(false))
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		got, err := RecheckUploadCommit(context.Background(), tx, "binflow_sec", principal, repo, path, false, nil, nil)
		if err != nil || got {
			t.Fatalf("denied deploy = (%v,%v), want (false,nil)", got, err)
		}
		for _, q := range script.snapshotQueries() {
			if strings.Contains(q.Query, "FROM nodes WHERE repo") {
				t.Fatalf("denied deploy queried nodes: %q", q.Query)
			}
		}
	})

	t.Run("allowed deploy with absent node allows", func(t *testing.T) {
		db, script := ssOpenDB(t, ssCommitResponder(true))
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		got, err := RecheckUploadCommit(context.Background(), tx, "binflow_sec", principal, repo, path, false, nil, nil)
		if err != nil || !got {
			t.Fatalf("allowed deploy = (%v,%v), want (true,nil)", got, err)
		}
		queries := script.snapshotQueries()
		setIndex, lockIndex, grantIndex, nodeIndex := -1, -1, -1, -1
		for i, q := range queries {
			switch {
			case strings.Contains(q.Query, "SET LOCAL search_path"):
				setIndex = i
			case strings.Contains(q.Query, `"binflow_sec".lock_upload_commit()`):
				lockIndex = i
			case strings.Contains(q.Query, "security_write_grants"):
				grantIndex = i
			case strings.Contains(q.Query, "FROM nodes WHERE repo"):
				nodeIndex = i
			}
		}
		if !(setIndex < lockIndex && lockIndex < grantIndex && grantIndex < nodeIndex) {
			t.Fatalf("chain order broken: set=%d lock=%d grant=%d node=%d over %d queries",
				setIndex, lockIndex, grantIndex, nodeIndex, len(queries))
		}
		if set := queries[setIndex].Query; !strings.Contains(set, `SET LOCAL search_path = "binflow_sec", pg_temp`) {
			t.Fatalf("search path statement = %q, want schema-qualified with pg_temp last", set)
		}
	})
}

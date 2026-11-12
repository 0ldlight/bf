package security

// 本文件覆盖上传提交锁安装（InstallUploadCommitLock）的真实行为：ctx/事务/
// 标识符守卫（含 pg_ 前缀与 public 角色拒绝）、安装语句的标识符引号形状、
// ACL 复核失败/不符/成功三类结局。
// This file covers the real InstallUploadCommitLock behavior: the ctx/tx/
// identifier guards (including pg_ prefix and public-role rejections), the
// quoted-identifier shape of the install statement, and the three ACL
// recheck outcomes of failure/mismatch/success.

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

const ssLockStage = "security: install upload commit lock"

// TestSecurityInstallUploadCommitLockValidation 断言全部输入守卫：nil ctx、
// nil 事务、非法模式/角色名（正则不符、pg_ 前缀、public 角色）一律拒绝为
// 固定 ErrConfiguration 文案，且绝不发出任何 SQL。
// TestSecurityInstallUploadCommitLockValidation asserts every input guard:
// nil ctx, a nil transaction and illegal schema/role names (regex mismatch,
// pg_ prefix, the public role) all reject with the fixed ErrConfiguration
// text and never issue any SQL.
func TestSecurityInstallUploadCommitLockValidation(t *testing.T) {
	db, script := ssOpenDB(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
		return ssResult{err: ssUnexpected(query)}
	})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	cases := []struct {
		name   string
		ctx    context.Context
		txNil  bool
		schema string
		role   string
	}{
		{"nil ctx", nil, false, "binflow_sec", "binflow_upload"},
		{"nil tx", context.Background(), true, "binflow_sec", "binflow_upload"},
		{"schema regex mismatch", context.Background(), false, "1abc", "binflow_upload"},
		{"schema pg prefix", context.Background(), false, "pg_custom", "binflow_upload"},
		{"schema overlong", context.Background(), false, strings.Repeat("a", 64), "binflow_upload"},
		{"role regex mismatch", context.Background(), false, "binflow_sec", "1abc"},
		{"role pg prefix", context.Background(), false, "binflow_sec", "pg_helper"},
		{"role public", context.Background(), false, "binflow_sec", "public"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tx
			if tc.txNil {
				target = nil
			}
			err := InstallUploadCommitLock(tc.ctx, target, tc.schema, tc.role)
			ssAssertStoreError(t, err, ErrConfiguration, ssLockStage, errInvalidUploadInput)
		})
	}
	if n := len(script.snapshotQueries()); n != 0 {
		t.Fatalf("validation executed %d queries, want 0", n)
	}
}

// TestSecurityInstallUploadCommitLockOutcomes 断言执行结局：安装语句失败与
// ACL 复核查询失败都保留原因；ACL 不符（非独立属主或存在其他执行授权）
// 以固定错误拒绝；成功时语句同时包含引号限定的模式限定函数与角色授权。
// TestSecurityInstallUploadCommitLockOutcomes asserts the execution outcomes:
// install-statement and ACL-recheck failures keep their causes; a failed ACL
// (non-distinct owner or extra execute grantees) rejects with the fixed
// error; success carries both the schema-qualified quoted function and the
// role grant in the statement.
func TestSecurityInstallUploadCommitLockOutcomes(t *testing.T) {
	t.Run("install statement failure keeps cause", func(t *testing.T) {
		cause := errors.New("install-boom")
		tx := ssBeginTx(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "CREATE FUNCTION") {
				return ssResult{err: cause}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		err := InstallUploadCommitLock(context.Background(), tx, "binflow_sec", "binflow_upload")
		ssAssertStoreError(t, err, ErrSecurityStore, ssLockStage, cause)
	})

	t.Run("acl recheck failure keeps cause", func(t *testing.T) {
		cause := errors.New("acl-boom")
		tx := ssBeginTx(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "CREATE FUNCTION") {
				return ssResult{affected: 0}
			}
			if strings.Contains(query, "proowner") {
				return ssResult{err: cause}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		err := InstallUploadCommitLock(context.Background(), tx, "binflow_sec", "binflow_upload")
		ssAssertStoreError(t, err, ErrSecurityStore, ssLockStage, cause)
	})

	t.Run("unrestricted acl rejected", func(t *testing.T) {
		tx := ssBeginTx(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "CREATE FUNCTION") {
				return ssResult{affected: 0}
			}
			if strings.Contains(query, "proowner") {
				return ssResult{row: []driver.Value{false}}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		err := InstallUploadCommitLock(context.Background(), tx, "binflow_sec", "binflow_upload")
		ssAssertStoreError(t, err, ErrConfiguration, ssLockStage, errUploadLockACL)
	})

	t.Run("success installs restricted function", func(t *testing.T) {
		db, script := ssOpenDB(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "CREATE FUNCTION") {
				return ssResult{affected: 0}
			}
			if strings.Contains(query, "proowner") {
				return ssResult{row: []driver.Value{true}}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := InstallUploadCommitLock(context.Background(), tx, "binflow_sec", "binflow_upload"); err != nil {
			t.Fatalf("valid install rejected: %v", err)
		}
		install := ssQueriesMatching(script, "CREATE FUNCTION")
		if len(install) != 1 {
			t.Fatalf("install statements = %d, want 1", len(install))
		}
		for _, needle := range []string{
			`CREATE FUNCTION "binflow_sec".lock_upload_commit()`,
			`REVOKE ALL ON FUNCTION "binflow_sec".lock_upload_commit() FROM PUBLIC`,
			`GRANT EXECUTE ON FUNCTION "binflow_sec".lock_upload_commit() TO "binflow_upload"`,
		} {
			if !strings.Contains(install[0].Query, needle) {
				t.Fatalf("install statement missing %q:\n%s", needle, install[0].Query)
			}
		}
	})
}

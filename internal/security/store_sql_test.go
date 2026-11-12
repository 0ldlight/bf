package security

// 本文件以可脚本化 SQL 探针覆盖 Store 的真实数据库行为契约：Validate 的
// 版本/列探针全部结局、Authenticate 的归一化/槽位/行形状/凭据矩阵、
// LookupRepository 与 CanRead 的缺席/形状/SQL 失败，以及 CanDeploy/
// CanDelete 写授权查询的输入守卫、参数与取消语义。
// This file covers the Store's real database behavior contracts through the
// scriptable SQL probe: every Validate version/column-probe outcome, the
// Authenticate normalization/slot/row-shape/credential matrix, LookupRepository
// and CanRead absence/shape/SQL failures, plus the CanDeploy/CanDelete write
// grant input guards, arguments and cancellation semantics.

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/repository"
)

// ssAssertStoreError 冻结 storeError 表示契约：固定正文（哨兵+阶段）、
// errors.Is 哨兵与底层原因同时成立。
// ssAssertStoreError freezes the storeError representation contract: the
// fixed sentinel+stage text with both the sentinel and the underlying cause
// reachable through errors.Is.
func ssAssertStoreError(t *testing.T, err error, sentinel error, stage string, cause error) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error with stage %q, got nil", stage)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error %v misses sentinel %v", err, sentinel)
	}
	if want := sentinel.Error() + ": " + stage; err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("error %v misses cause %v", err, cause)
	}
}

// TestSecurityStoreValidateSchemaOutcomes 断言 Validate 的全部结局：成功
// （一行版本=1 + 六张表列探针）、版本行数/值不合规、SQL 失败、探针失败与
// 探针关闭失败；所有失败都归因到固定阶段且保留原因。
// TestSecurityStoreValidateSchemaOutcomes asserts every Validate outcome:
// success (exactly one version row valued 1 plus six column probes),
// nonconforming version rows, SQL failures, probe failures and probe close
// failures; every failure attributes a fixed stage while keeping its cause.
func TestSecurityStoreValidateSchemaOutcomes(t *testing.T) {
	t.Run("success probes every required table", func(t *testing.T) {
		store, script := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "FROM security_schema_version") {
				return ssResult{row: []driver.Value{int64(1), int64(1), int64(1)}}
			}
			if strings.Contains(query, "WHERE false") {
				return ssResult{}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		if err := store.Validate(context.Background()); err != nil {
			t.Fatalf("valid schema rejected: %v", err)
		}
		probes := 0
		for _, q := range script.snapshotQueries() {
			if strings.Contains(q.Query, "WHERE false") {
				probes++
			}
		}
		if probes != len(validateColumns) {
			t.Fatalf("column probes = %d, want %d", probes, len(validateColumns))
		}
	})

	badVersions := []struct {
		name string
		row  []driver.Value
	}{
		{"two version rows", []driver.Value{int64(2), int64(1), int64(1)}},
		{"zero version rows", []driver.Value{int64(0), int64(0), int64(0)}},
		{"min version 0", []driver.Value{int64(1), int64(0), int64(1)}},
		{"max version 2", []driver.Value{int64(1), int64(1), int64(2)}},
	}
	for _, tc := range badVersions {
		t.Run(tc.name, func(t *testing.T) {
			store, script := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
				if strings.Contains(query, "FROM security_schema_version") {
					return ssResult{row: tc.row}
				}
				return ssResult{err: ssUnexpected(query)}
			})
			err := store.Validate(context.Background())
			ssAssertStoreError(t, err, ErrSecurityStore, stageValidateVersion, errSchemaVersionNotOne)
			if n := len(script.snapshotQueries()); n != 1 {
				t.Fatalf("queries after bad version row = %d, want 1", n)
			}
		})
	}

	t.Run("empty version result folds to rejection", func(t *testing.T) {
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{}
		})
		err := store.Validate(context.Background())
		ssAssertStoreError(t, err, ErrSecurityStore, stageValidateVersion, errSchemaVersionNotOne)
	})

	t.Run("version query sql failure", func(t *testing.T) {
		cause := errors.New("version-boom")
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: cause}
		})
		err := store.Validate(context.Background())
		ssAssertStoreError(t, err, ErrSecurityStore, stageValidateVersion, cause)
	})

	t.Run("probe failure", func(t *testing.T) {
		cause := errors.New("probe-boom")
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "FROM security_schema_version") {
				return ssResult{row: []driver.Value{int64(1), int64(1), int64(1)}}
			}
			return ssResult{err: cause}
		})
		err := store.Validate(context.Background())
		ssAssertStoreError(t, err, ErrSecurityStore, stageValidateColumns, cause)
	})

	t.Run("probe close failure surfaces", func(t *testing.T) {
		cause := errors.New("close-boom")
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "FROM security_schema_version") {
				return ssResult{row: []driver.Value{int64(1), int64(1), int64(1)}}
			}
			return ssResult{closeErr: cause}
		})
		err := store.Validate(context.Background())
		ssAssertStoreError(t, err, ErrSecurityStore, stageValidateColumns, cause)
	})
}

// TestSecurityStoreAuthenticateMatrix 断言 Authenticate 的完整行为矩阵：
// 归一化/raw 守卫在任何 SQL 之前、预取消 ctx、未知用户（哑校验后拒绝）、
// SQL 失败、禁用账户、真实凭据成功/不匹配、畸形存储编码与零值行形状，
// 以及查询参数使用归一化用户名。
// TestSecurityStoreAuthenticateMatrix asserts the full Authenticate matrix:
// normalization/raw guards before any SQL, a precanceled ctx, an unknown user
// (dummy check then denial), SQL failure, a disabled account, real-credential
// success/mismatch, a malformed stored encoding and zero-value row shapes,
// with the query argument using the normalized username.
func TestSecurityStoreAuthenticateMatrix(t *testing.T) {
	encoded, err := GenerateCredential(suPassword)
	if err != nil {
		t.Fatalf("fixture encoding failed: %v", err)
	}

	t.Run("guards precede any sql", func(t *testing.T) {
		store, script := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: ssUnexpected(query)}
		})
		for _, tc := range []struct{ name, username, raw string }{
			{"illegal username", "reader lane", suPassword},
			{"empty username", "", suPassword},
			{"overlong username", strings.Repeat("a", 65), suPassword},
			{"empty raw", "reader", ""},
			{"malformed utf8 raw", "reader", "\xff"},
		} {
			p, err := store.Authenticate(context.Background(), tc.username, tc.raw)
			if p != (Principal{}) || !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("%s: got (%v,%v), want zero principal and ErrInvalidCredentials", tc.name, p, err)
			}
		}
		if n := len(script.snapshotQueries()); n != 0 {
			t.Fatalf("guards executed %d queries, want 0", n)
		}
	})

	t.Run("precanceled context", func(t *testing.T) {
		store, script := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: ssUnexpected(query)}
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p, err := store.Authenticate(ctx, "reader", suPassword)
		if p != (Principal{}) {
			t.Fatalf("principal = %v, want zero", p)
		}
		ssAssertStoreError(t, err, ErrSecurityStore, stageAuthenticateCtx, context.Canceled)
		if n := len(script.snapshotQueries()); n != 0 {
			t.Fatalf("precanceled ctx executed %d queries, want 0", n)
		}
	})

	t.Run("username normalized for lookup", func(t *testing.T) {
		var gotArg any
		store, _ := ssNewStore(t, func(_ context.Context, query string, args []driver.Value) ssResult {
			if strings.Contains(query, "FROM security_users WHERE username") {
				gotArg = args[0]
				return ssResult{}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		if _, err := store.Authenticate(context.Background(), "  Reader\t", suPassword); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("unknown user error = %v", err)
		}
		if gotArg != "reader" {
			t.Fatalf("lookup arg = %v, want normalized \"reader\"", gotArg)
		}
	})

	sqlCause := errors.New("auth-sql-boom")
	cases := []struct {
		name      string
		row       []driver.Value
		zeroRows  bool
		sqlFail   bool
		raw       string
		wantErr   error
		stage     string
		cause     error
		wantPrinc Principal
	}{
		{name: "unknown user", zeroRows: true, wantErr: ErrInvalidCredentials},
		{name: "sql failure", sqlFail: true, wantErr: ErrSecurityStore, stage: stageAuthenticateRow, cause: sqlCause},
		{name: "disabled account", row: []driver.Value{int64(7), "reader", encoded, false}, wantErr: ErrAccountDisabled},
		{name: "matching credential", row: []driver.Value{int64(7), "reader", encoded, true}, wantPrinc: Principal{id: 7, username: "reader"}},
		{name: "wrong credential", row: []driver.Value{int64(7), "reader", encoded, true}, raw: "Unit-Raw-Password-43", wantErr: ErrInvalidCredentials},
		{name: "malformed stored encoding", row: []driver.Value{int64(7), "reader", "garbage", true}, wantErr: ErrCredentialEncoding},
		{name: "zero database id", row: []driver.Value{int64(0), "reader", encoded, true}, wantErr: ErrSecurityStore, stage: stageAuthenticateRow, cause: errInvalidPrincipalArg},
		{name: "empty canonical username", row: []driver.Value{int64(7), "", encoded, true}, wantErr: ErrSecurityStore, stage: stageAuthenticateRow, cause: errInvalidPrincipalArg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
				if strings.Contains(query, "FROM security_users WHERE username") {
					switch {
					case tc.sqlFail:
						return ssResult{err: sqlCause}
					case tc.zeroRows:
						return ssResult{}
					default:
						return ssResult{row: tc.row}
					}
				}
				return ssResult{err: ssUnexpected(query)}
			})
			raw := tc.raw
			if raw == "" {
				raw = suPassword
			}
			p, err := store.Authenticate(context.Background(), "reader", raw)
			if tc.wantPrinc != (Principal{}) {
				if err != nil || p != tc.wantPrinc {
					t.Fatalf("got (%v,%v), want principal %v", p, err, tc.wantPrinc)
				}
				return
			}
			if p != (Principal{}) {
				t.Fatalf("principal = %v, want zero", p)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.stage != "" {
				ssAssertStoreError(t, err, tc.wantErr, tc.stage, tc.cause)
			}
		})
	}
}

// TestSecurityStoreAuthenticateKDFSlotsBounded 证明容量 4 的 KDF 槽真实
// 限流：第五个并发 Authenticate 在有槽释放前无法进入查询；释放后全部完成
// 且并发峰值不超过 4。
// TestSecurityStoreAuthenticateKDFSlotsBounded proves the capacity-4 KDF slot
// really throttles: a fifth concurrent Authenticate cannot reach its query
// before a slot frees; after release all five finish with the concurrency
// peak never exceeding 4.
func TestSecurityStoreAuthenticateKDFSlotsBounded(t *testing.T) {
	arrived := make(chan string, kdfConcurrency+1)
	release := make(chan struct{})
	store, script := ssNewStore(t, func(_ context.Context, query string, args []driver.Value) ssResult {
		if strings.Contains(query, "FROM security_users WHERE username") {
			arrived <- args[0].(string)
			<-release
			return ssResult{}
		}
		return ssResult{err: ssUnexpected(query)}
	})
	const callers = kdfConcurrency + 1
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := store.Authenticate(context.Background(), "reader"+string(rune('0'+i)), suPassword)
			if p != (Principal{}) {
				t.Errorf("caller %d got principal %v", i, p)
			}
			results <- err
		}(i)
	}
	defer wg.Wait()
	for i := 0; i < kdfConcurrency; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d callers reached the query", i, kdfConcurrency)
		}
	}
	select {
	case extra := <-arrived:
		t.Fatalf("fifth caller %q reached the query despite full KDF slots", extra)
	case <-time.After(250 * time.Millisecond):
	}
	close(release)
	for i := 0; i < callers; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("caller error = %v, want ErrInvalidCredentials", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("callers did not finish after release")
		}
	}
	if peak := script.peakInflight(); peak > kdfConcurrency {
		t.Fatalf("observed %d concurrent queries, want <= %d", peak, kdfConcurrency)
	}
}

// TestSecurityStoreAuthenticateCancellationAfterQuery 断言查询完成后取消
// 的四种结局：未知用户与禁用用户的哑校验之后、真实凭据校验之后、以及成
// 功后的最终复查都以固定取消阶段拒绝，绝不产出 Principal。
// TestSecurityStoreAuthenticateCancellationAfterQuery asserts the four
// post-query cancellation outcomes: after the dummy check for unknown and
// disabled users, after the real credential check and the final recheck on
// success — all reject with the fixed cancellation stage and never yield a
// Principal.
func TestSecurityStoreAuthenticateCancellationAfterQuery(t *testing.T) {
	encoded, err := GenerateCredential(suPassword)
	if err != nil {
		t.Fatalf("fixture encoding failed: %v", err)
	}
	for _, tc := range []struct {
		name string
		row  []driver.Value
	}{
		{"unknown user dummy check", nil},
		{"disabled user dummy check", []driver.Value{int64(7), "reader", encoded, false}},
		{"after real credential check", []driver.Value{int64(0), "reader", encoded, true}},
		{"final recheck", []driver.Value{int64(7), "reader", encoded, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
				if strings.Contains(query, "FROM security_users WHERE username") {
					cancel()
					if tc.row == nil {
						return ssResult{}
					}
					return ssResult{row: tc.row}
				}
				return ssResult{err: ssUnexpected(query)}
			})
			p, err := store.Authenticate(ctx, "reader", suPassword)
			if p != (Principal{}) {
				t.Fatalf("canceled path returned principal %v", p)
			}
			ssAssertStoreError(t, err, ErrSecurityStore, stageAuthenticateCtx, context.Canceled)
		})
	}
}

// TestSecurityStoreLookupRepositoryOutcomes 断言仓库目录查询：缺席、SQL
// 失败、四种不支持行形状与成功回显；键参数精确传递。
// TestSecurityStoreLookupRepositoryOutcomes asserts the repository catalog
// query: absence, SQL failure, four unsupported row shapes and a successful
// echo, with the key argument passed verbatim.
func TestSecurityStoreLookupRepositoryOutcomes(t *testing.T) {
	const key = "repoA"
	sqlCause := errors.New("catalog-boom")
	for _, tc := range []struct {
		name     string
		row      []driver.Value
		zeroRows bool
		sqlFail  bool
		wantErr  error
		stage    string
		cause    error
	}{
		{name: "absent", zeroRows: true, wantErr: ErrRepositoryAbsent},
		{name: "sql failure", sqlFail: true, wantErr: ErrSecurityStore, stage: stageRepositoryRow, cause: sqlCause},
		{name: "remote kind", row: []driver.Value{"repoA", "remote", "generic", int64(4)}, wantErr: ErrSecurityStore, stage: stageRepositoryRow, cause: errRepositoryRowShape},
		{name: "maven package", row: []driver.Value{"repoA", "local", "maven", int64(4)}, wantErr: ErrSecurityStore, stage: stageRepositoryRow, cause: errRepositoryRowShape},
		{name: "zero revision", row: []driver.Value{"repoA", "local", "generic", int64(0)}, wantErr: ErrSecurityStore, stage: stageRepositoryRow, cause: errRepositoryRowShape},
		{name: "key echo mismatch", row: []driver.Value{"other", "local", "generic", int64(4)}, wantErr: ErrSecurityStore, stage: stageRepositoryRow, cause: errRepositoryRowShape},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := ssNewStore(t, func(_ context.Context, query string, args []driver.Value) ssResult {
				if strings.Contains(query, "FROM security_repositories WHERE repo_key") {
					if args[0] != key {
						t.Errorf("lookup key arg = %v, want %q", args[0], key)
					}
					switch {
					case tc.sqlFail:
						return ssResult{err: sqlCause}
					case tc.zeroRows:
						return ssResult{}
					default:
						return ssResult{row: tc.row}
					}
				}
				return ssResult{err: ssUnexpected(query)}
			})
			info, err := store.LookupRepository(context.Background(), key)
			if tc.wantErr == nil {
				want := RepositoryInfo{Key: key, Kind: "local", PackageType: "generic", Revision: 4}
				if err != nil || info != want {
					t.Fatalf("got (%v,%v), want echoed %v", info, err, want)
				}
				return
			}
			if info != (RepositoryInfo{}) {
				t.Fatalf("info = %v, want zero", info)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if tc.stage != "" {
				ssAssertStoreError(t, err, tc.wantErr, tc.stage, tc.cause)
			} else if err != tc.wantErr {
				t.Fatalf("absence error = %v, want the bare sentinel %v", err, tc.wantErr)
			}
		})
	}
	t.Run("success echoes row", func(t *testing.T) {
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "FROM security_repositories WHERE repo_key") {
				return ssResult{row: []driver.Value{"repoA", "local", "generic", int64(4)}}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		info, err := store.LookupRepository(context.Background(), key)
		want := RepositoryInfo{Key: key, Kind: "local", PackageType: "generic", Revision: 4}
		if err != nil || info != want {
			t.Fatalf("got (%v,%v), want %v", info, err, want)
		}
	})
}

// ssRepoPath 构造合法仓库事实与文件路径；失败即终止测试。
// ssRepoPath builds the valid repository fact and file path, failing the
// test on any path error.
func ssRepoPath(t *testing.T) (Principal, RepositoryInfo, repository.Path) {
	t.Helper()
	path, err := repository.NewPath("repoA", "dir", "file.bin", true)
	if err != nil {
		t.Fatalf("path fixture: %v", err)
	}
	return Principal{id: suUserID, username: suUser},
		RepositoryInfo{Key: "repoA", Kind: "local", PackageType: "generic", Revision: 4}, path
}

// TestSecurityStoreCanReadOutcomes 断言 CanRead：零主体/不支持仓库/路径不
// 一致在 SQL 前拒绝，SQL 失败与允许/拒绝行按当前状态回答，参数顺序为
// (id, username, repo key)。
// TestSecurityStoreCanReadOutcomes asserts CanRead: zero-principal,
// unsupported-repo and path-mismatch rejections precede any SQL; SQL failure
// and the allowed/denied rows answer from current state with the argument
// order (id, username, repo key).
func TestSecurityStoreCanReadOutcomes(t *testing.T) {
	principal, repo, path := ssRepoPath(t)
	otherRepo, err := repository.NewPath("repoB", "dir", "file.bin", true)
	if err != nil {
		t.Fatalf("other path: %v", err)
	}

	t.Run("input guards precede sql", func(t *testing.T) {
		store, script := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: ssUnexpected(query)}
		})
		for _, tc := range []struct {
			name      string
			principal Principal
			repo      RepositoryInfo
			path      repository.Path
			cause     error
		}{
			{"zero principal", Principal{}, repo, path, errInvalidPrincipalArg},
			{"empty repo key", principal, RepositoryInfo{Kind: "local", PackageType: "generic", Revision: 4}, path, errUnsupportedRepoArg},
			{"remote repo", principal, RepositoryInfo{Key: "repoA", Kind: "remote", PackageType: "generic", Revision: 4}, path, errUnsupportedRepoArg},
			{"maven repo", principal, RepositoryInfo{Key: "repoA", Kind: "local", PackageType: "maven", Revision: 4}, path, errUnsupportedRepoArg},
			{"zero revision", principal, RepositoryInfo{Key: "repoA", Kind: "local", PackageType: "generic", Revision: 0}, path, errUnsupportedRepoArg},
			{"path repo mismatch", principal, repo, otherRepo, errRepoPathMismatchArg},
		} {
			got, err := store.CanRead(context.Background(), tc.principal, tc.repo, tc.path)
			if got {
				t.Fatalf("%s: allowed = true, want false", tc.name)
			}
			ssAssertStoreError(t, err, ErrConfiguration, stageCanReadInput, tc.cause)
		}
		if n := len(script.snapshotQueries()); n != 0 {
			t.Fatalf("input guards executed %d queries, want 0", n)
		}
	})

	sqlCause := errors.New("canread-boom")
	for _, tc := range []struct {
		name    string
		sqlFail bool
		row     []driver.Value
		want    bool
	}{
		{name: "allowed", row: []driver.Value{true}, want: true},
		{name: "denied", row: []driver.Value{false}},
		{name: "sql failure", sqlFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := ssNewStore(t, func(_ context.Context, query string, args []driver.Value) ssResult {
				if strings.Contains(query, "security_read_grants") {
					if len(args) != 3 || args[0] != int64(suUserID) || args[1] != suUser || args[2] != "repoA" {
						t.Fatalf("can-read args = %v, want (id, username, repo key)", args)
					}
					if tc.sqlFail {
						return ssResult{err: sqlCause}
					}
					return ssResult{row: tc.row}
				}
				return ssResult{err: ssUnexpected(query)}
			})
			got, err := store.CanRead(context.Background(), principal, repo, path)
			if tc.sqlFail {
				ssAssertStoreError(t, err, ErrSecurityStore, stageCanReadRow, sqlCause)
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got (%v,%v), want (%v,nil)", got, err, tc.want)
			}
		})
	}
}

// TestSecurityStoreWriteGrantsBehavior 断言 CanDeploy/CanDelete 写授权：
// nil ctx、零主体、不支持仓库、路径形状（根/目录/仓库不符）全部在 SQL 前
// 拒绝；查询参数为 (id, username, key, revision, action) 且 action 区分
// deploy/delete；SQL 失败与扫描后取消保留原因。
// TestSecurityStoreWriteGrantsBehavior asserts the CanDeploy/CanDelete write
// grants: nil ctx, zero principal, unsupported repository and path shapes
// (root/folder/repo mismatch) all reject before SQL; the query arguments are
// (id, username, key, revision, action) with action distinguishing
// deploy/delete; SQL failures and post-scan cancellation keep their causes.
func TestSecurityStoreWriteGrantsBehavior(t *testing.T) {
	principal, repo, path := ssRepoPath(t)
	folder, err := repository.NewPath("repoA", "dir", "folder", false)
	if err != nil {
		t.Fatalf("folder path: %v", err)
	}
	root, err := repository.NewPath("repoA", "", "", true)
	if err != nil {
		t.Fatalf("root path: %v", err)
	}
	otherRepo, err := repository.NewPath("repoB", "dir", "file.bin", true)
	if err != nil {
		t.Fatalf("other path: %v", err)
	}
	const writeStage = "security: write grant lookup"

	t.Run("nil context", func(t *testing.T) {
		store, script := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: ssUnexpected(query)}
		})
		got, err := store.CanDeploy(nil, principal, repo, path)
		if got {
			t.Fatal("nil ctx allowed = true")
		}
		ssAssertStoreError(t, err, ErrConfiguration, writeStage, errInvalidUploadInput)
		if n := len(script.snapshotQueries()); n != 0 {
			t.Fatalf("nil ctx executed %d queries, want 0", n)
		}
	})

	t.Run("input guards precede sql", func(t *testing.T) {
		store, script := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: ssUnexpected(query)}
		})
		for _, tc := range []struct {
			name      string
			principal Principal
			repo      RepositoryInfo
			path      repository.Path
			cause     error
		}{
			{"zero principal", Principal{}, repo, path, errInvalidPrincipalArg},
			{"empty username", Principal{id: suUserID}, repo, path, errInvalidPrincipalArg},
			{"remote repo", principal, RepositoryInfo{Key: "repoA", Kind: "remote", PackageType: "generic", Revision: 4}, path, errUnsupportedRepoArg},
			{"maven package", principal, RepositoryInfo{Key: "repoA", Kind: "local", PackageType: "maven", Revision: 4}, path, errUnsupportedRepoArg},
			{"zero revision", principal, RepositoryInfo{Key: "repoA", Kind: "local", PackageType: "generic", Revision: 0}, path, errUnsupportedRepoArg},
			{"empty repo key", principal, RepositoryInfo{Kind: "local", PackageType: "generic", Revision: 4}, path, errUnsupportedRepoArg},
			{"path repo mismatch", principal, repo, otherRepo, errRepoPathMismatchArg},
			{"folder path", principal, repo, folder, errRepoPathMismatchArg},
			{"root path", principal, repo, root, errRepoPathMismatchArg},
		} {
			got, err := store.CanDelete(context.Background(), tc.principal, tc.repo, tc.path)
			if got {
				t.Fatalf("%s: allowed = true, want false", tc.name)
			}
			ssAssertStoreError(t, err, ErrConfiguration, writeStage, tc.cause)
		}
		if n := len(script.snapshotQueries()); n != 0 {
			t.Fatalf("input guards executed %d queries, want 0", n)
		}
	})

	t.Run("action distinguishes deploy from delete", func(t *testing.T) {
		var seenActions []string
		store, _ := ssNewStore(t, func(_ context.Context, query string, args []driver.Value) ssResult {
			if strings.Contains(query, "security_write_grants") {
				if len(args) != 5 {
					t.Fatalf("write-grant args = %v, want 5", args)
				}
				if args[0] != int64(suUserID) || args[1] != suUser || args[2] != "repoA" || args[3] != int64(4) {
					t.Fatalf("write-grant args = %v, want (id, username, key, revision, action)", args)
				}
				seenActions = append(seenActions, args[4].(string))
				return ssResult{row: []driver.Value{true}}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		got, err := store.CanDeploy(context.Background(), principal, repo, path)
		if err != nil || !got {
			t.Fatalf("CanDeploy = (%v,%v), want (true,nil)", got, err)
		}
		got, err = store.CanDelete(context.Background(), principal, repo, path)
		if err != nil || !got {
			t.Fatalf("CanDelete = (%v,%v), want (true,nil)", got, err)
		}
		if len(seenActions) != 2 || seenActions[0] != "deploy" || seenActions[1] != "delete" {
			t.Fatalf("recorded actions = %v, want [deploy delete]", seenActions)
		}
	})

	t.Run("sql failure keeps cause", func(t *testing.T) {
		cause := errors.New("write-grant-boom")
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{err: cause}
		})
		got, err := store.CanDeploy(context.Background(), principal, repo, path)
		if got {
			t.Fatal("sql failure allowed = true")
		}
		ssAssertStoreError(t, err, ErrSecurityStore, writeStage, cause)
	})

	t.Run("denied grant answers false", func(t *testing.T) {
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			return ssResult{row: []driver.Value{false}}
		})
		got, err := store.CanDelete(context.Background(), principal, repo, path)
		if err != nil || got {
			t.Fatalf("CanDelete = (%v,%v), want (false,nil)", got, err)
		}
	})

	t.Run("post-scan cancellation preserves cause", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		store, _ := ssNewStore(t, func(_ context.Context, query string, _ []driver.Value) ssResult {
			if strings.Contains(query, "security_write_grants") {
				cancel()
				return ssResult{row: []driver.Value{true}}
			}
			return ssResult{err: ssUnexpected(query)}
		})
		got, err := store.CanDeploy(ctx, principal, repo, path)
		if got {
			t.Fatal("canceled lookup allowed = true")
		}
		ssAssertStoreError(t, err, ErrSecurityStore, writeStage, context.Canceled)
	})
}

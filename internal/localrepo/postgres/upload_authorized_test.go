// SaveAuthorized 深层复核路径的单元契约（依赖 security.NewPrincipal 测试缝）：
// fake driver 逐条驱动真实 recheck SQL 序列（schema 兼容探针、search_path、
// 提交锁、deploy/delete 授权查询），覆盖允许创建、覆盖需 delete、授权故障
// 与授权事实守卫；真实 PostgreSQL 行为归 integration/postgres。
// Unit contract for SaveAuthorized's deep recheck path (via the
// security.NewPrincipal testability seam): the fake driver drives the real
// recheck SQL sequence statement by statement (the schema compatibility
// probe, search_path, the commit lock and the deploy/delete grant queries),
// covering allow-and-create, overwrite-needs-delete, authorization faults
// and authorization-fact guards; real PostgreSQL behavior belongs to
// integration/postgres.
package postgres

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
	"github.com/0ldlight/bf/internal/security"
)

var (
	errUploadFakeSchemaProbe = errors.New("postgres upload test: schema probe fault")
	errUploadFakeSetPath     = errors.New("postgres upload test: set search path fault")
	errUploadFakeLock        = errors.New("postgres upload test: commit lock fault")
	errUploadFakeGrant       = errors.New("postgres upload test: grant lookup fault")
)

// uploadRecheckOptions 配置 recheck 各阶段的注入行为与授权查询记录。
// uploadRecheckOptions injects per-stage recheck behavior and records the
// grant queries.
type uploadRecheckOptions struct {
	compatible   any   // schema 探针扫描值；nil 表示缺省 true。
	compatErr    error // 探针查询故障。
	deploy       any   // deploy 授权扫描值；nil 表示缺省 true。
	deployErr    error
	del          any // delete 授权扫描值；nil 表示缺省 false。
	deleteErr    error
	overwriteRow []driver.Value // 非 nil 时 recheck 节点查询命中该行（覆盖判定）。
	setPathErr   error
	lockErr      error

	grantCalls      []driver.NamedValue
	grantActions    []string
	schemaProbeArgs []driver.NamedValue
	setPathSQL      []string
	lockCalls       int
}

// uploadAuthorizedSaver 返回授权画像 saver 与两个 hub。
// uploadAuthorizedSaver returns the authorized-profile saver and both hubs.
func uploadAuthorizedSaver(t *testing.T) (*UploadSaver, *uploadFakeHub, *uploadFakeHub) {
	t.Helper()
	deps, mainHub, resHub := uploadAuthorizedDeps(t)
	saver, err := NewUploadSaver(deps)
	if err != nil {
		t.Fatalf("NewUploadSaver authorized: %v", err)
	}
	return saver, mainHub, resHub
}

// uploadAuthorizedHooks 配置主池/预留池的完整授权保存钩子。
// uploadAuthorizedHooks wires the full authorized-save hooks on the main
// and reservation pools.
func uploadAuthorizedHooks(t *testing.T, mainHub, resHub *uploadFakeHub, rc *uploadRecheckOptions,
	path repository.Path, body string, fresh content.Metadata, stamp int64) {
	t.Helper()
	nodeQueries := 0
	mainHub.query = func(query string, args []driver.NamedValue) ([]string, [][]driver.Value, error) {
		switch {
		case strings.Contains(query, "current_schema"):
			rc.schemaProbeArgs = append([]driver.NamedValue(nil), args...)
			if rc.compatErr != nil {
				return nil, nil, rc.compatErr
			}
			value := rc.compatible
			if value == nil {
				value = true
			}
			return []string{"compatible"}, [][]driver.Value{{value}}, nil
		case strings.Contains(query, "FROM security_repositories"):
			action := ""
			if len(args) == 5 {
				action, _ = args[4].Value.(string)
			}
			rc.grantActions = append(rc.grantActions, action)
			rc.grantCalls = append(rc.grantCalls, args...)
			switch action {
			case "deploy":
				if rc.deployErr != nil {
					return nil, nil, rc.deployErr
				}
				value := rc.deploy
				if value == nil {
					value = true
				}
				return []string{"allowed"}, [][]driver.Value{{value}}, nil
			case "delete":
				if rc.deleteErr != nil {
					return nil, nil, rc.deleteErr
				}
				value := rc.del
				if value == nil {
					value = false
				}
				return []string{"allowed"}, [][]driver.Value{{value}}, nil
			default:
				return nil, nil, errUploadFakeQuery
			}
		case strings.Contains(query, "FROM binaries"):
			return uploadBinCols, nil, nil
		case strings.Contains(query, "FROM nodes"):
			nodeQueries++
			switch nodeQueries {
			case 1:
				if rc.overwriteRow != nil {
					return uploadNodeCols, [][]driver.Value{rc.overwriteRow}, nil
				}
				return uploadNodeCols, nil, nil
			case 2:
				return uploadNodeCols, nil, nil
			default:
				return uploadNodeCols, [][]driver.Value{uploadNodeRow(6, path, int64(len(body)),
					fresh.SHA1, fresh.MD5, fresh.SHA256, stamp, stamp, stamp)}, nil
			}
		default:
			return nil, nil, errUploadFakeQuery
		}
	}
	mainHub.exec = func(query string, args []driver.NamedValue) (int64, error) {
		switch {
		case strings.Contains(query, "INSERT INTO binaries"):
			return 1, nil
		case strings.Contains(query, "SET LOCAL search_path"):
			rc.setPathSQL = append(rc.setPathSQL, query)
			if rc.setPathErr != nil {
				return 0, rc.setPathErr
			}
			return 0, nil
		case strings.Contains(query, "lock_upload_commit"):
			rc.lockCalls++
			if rc.lockErr != nil {
				return 0, rc.lockErr
			}
			return 0, nil
		case strings.Contains(query, "INSERT INTO nodes"):
			mainHub.mu.Lock()
			mainHub.lastNodeExecArgs = append([]driver.NamedValue(nil), args...)
			mainHub.mu.Unlock()
			return 1, nil
		default:
			return 0, errUploadFakeExec
		}
	}
	resHub.query = func(query string, _ []driver.NamedValue) ([]string, [][]driver.Value, error) {
		if strings.Contains(query, "FROM unique_ids") {
			return uploadIDCols, [][]driver.Value{{int64(5)}}, nil
		}
		return nil, nil, errUploadFakeQuery
	}
	resHub.exec = func(query string, _ []driver.NamedValue) (int64, error) {
		if strings.Contains(query, "UPDATE unique_ids") {
			return 1, nil
		}
		return 0, errUploadFakeExec
	}
}

func TestSaveAuthorizedRechecksThenCreates(t *testing.T) {
	saver, mainHub, resHub := uploadAuthorizedSaver(t)
	stamp := int64(1_700_000_000_000)
	body := "authorized create body\n"
	fresh := uploadFreshCalculation(t, body)
	input := uploadTestInput(t, int64(len(body)))
	// 操作者必须来自 Principal，输入里的操作者被覆盖。
	// The operator must come from the Principal; the input operator is
	// overwritten.
	input.OperatorUTF16 = utf16.Encode([]rune("ignored-op"))
	rc := &uploadRecheckOptions{}
	uploadAuthorizedHooks(t, mainHub, resHub, rc, input.Path, body, fresh, stamp)
	principal := security.NewPrincipal(7, "op-user")
	repo := security.RepositoryInfo{Key: "repo-x", Kind: "local", PackageType: "generic", Revision: 3}

	source, err := saver.SaveAuthorized(context.Background(), strings.NewReader(body), input, principal, repo, false)
	if err != nil {
		t.Fatalf("SaveAuthorized create: %v", err)
	}
	if source.Reference.ID != 6 {
		t.Fatalf("authorized source ID = %d, want the reserved 6", source.Reference.ID)
	}
	if source.Reference.Content != fresh {
		t.Fatalf("authorized content = %+v, want published %+v", source.Reference.Content, fresh)
	}
	if got := string(utf16.Decode(source.Reference.Metadata.CreatedBy().UTF16())); got != "op-user" {
		t.Fatalf("authorized actor = %q, want the principal username op-user", got)
	}
	// recheck SQL 序列：探针一次、search_path 一次、锁一次、deploy 查询一次。
	// The recheck SQL sequence: one probe, one search_path, one lock and
	// one deploy grant query.
	if len(rc.schemaProbeArgs) != 1 || rc.schemaProbeArgs[0].Value != uploadTestCommitSchema {
		t.Fatalf("schema probe args = %+v, want [%s]", rc.schemaProbeArgs, uploadTestCommitSchema)
	}
	if len(rc.setPathSQL) != 1 || !strings.Contains(rc.setPathSQL[0], `SET LOCAL search_path = "`+uploadTestCommitSchema+`", pg_temp`) {
		t.Fatalf("search_path SQL = %q, want the trusted schema then pg_temp", rc.setPathSQL)
	}
	if rc.lockCalls != 1 {
		t.Fatalf("commit lock calls = %d, want 1", rc.lockCalls)
	}
	if len(rc.grantActions) != 1 || rc.grantActions[0] != "deploy" {
		t.Fatalf("grant actions = %v, want exactly one deploy lookup", rc.grantActions)
	}
	if len(rc.grantCalls) != 5 || rc.grantCalls[0].Value != int64(7) || rc.grantCalls[1].Value != "op-user" ||
		rc.grantCalls[2].Value != "repo-x" || rc.grantCalls[3].Value != int64(3) || rc.grantCalls[4].Value != "deploy" {
		t.Fatalf("deploy grant args = %+v, want [id username repo revision deploy]", rc.grantCalls)
	}
	// 操作者来自 Principal：节点写入的 actor 列是 op-user。
	// The operator comes from the Principal: the node write's actor
	// column is op-user.
	if args := mainHub.nodeExecArgs(); len(args) != 16 || args[13].Value != "op-user" || args[12].Value != "op-user" {
		t.Fatalf("node INSERT actor args = %+v, want op-user/op-user", args)
	}
	// 主池两事务（登记+节点）、预留池一事务；号段查询只落在预留池。
	// Two main-pool transactions (registry and node) plus one
	// reservation transaction; the id query stays on the reservation pool.
	if begins, rollbacks := mainHub.beginsRollbacks(); begins != 2 || rollbacks != 0 {
		t.Fatalf("main begins/rollbacks = %d/%d, want 2/0", begins, rollbacks)
	}
	if commits := mainHub.commitCount(); commits != 2 {
		t.Fatalf("main commits = %d, want 2 (registry and node)", commits)
	}
	if begins, rollbacks := resHub.beginsRollbacks(); begins != 1 || rollbacks != 0 {
		t.Fatalf("reservation begins/rollbacks = %d/%d, want 1/0", begins, rollbacks)
	}
	if commits := resHub.commitCount(); commits != 1 {
		t.Fatalf("reservation commits = %d, want 1", commits)
	}
}

func TestSaveAuthorizedOverwriteWithoutDeleteGrantDenied(t *testing.T) {
	saver, mainHub, resHub := uploadAuthorizedSaver(t)
	stamp := int64(1_700_000_000_000)
	body := "authorized overwrite body\n"
	old := uploadFreshCalculation(t, "authorized old body\n")
	fresh := uploadFreshCalculation(t, body)
	input := uploadTestInput(t, int64(len(body)))
	rc := &uploadRecheckOptions{
		// 已存在节点 + 请求无摘要 → 覆盖判定为真 → 需要 delete 授权。
		// An existing node with no request digests decides overwrite
		// true, so the delete grant is required.
		overwriteRow: uploadNodeRow(5, input.Path, old.Size, old.SHA1, old.MD5, old.SHA256, stamp, stamp, stamp),
	}
	uploadAuthorizedHooks(t, mainHub, resHub, rc, input.Path, body, fresh, stamp)
	principal := security.NewPrincipal(7, "op-user")
	repo := security.RepositoryInfo{Key: "repo-x", Kind: "local", PackageType: "generic", Revision: 3}

	source, err := saver.SaveAuthorized(context.Background(), strings.NewReader(body), input, principal, repo, false)
	if err == nil {
		t.Fatal("overwrite without the delete grant must be denied")
	}
	if !errors.Is(err, localrepo.ErrUploadCommitDenied) {
		t.Fatalf("denial error = %v, want ErrUploadCommitDenied", err)
	}
	if errors.Is(err, localrepo.ErrUploadPersistence) {
		t.Fatal("a clean denial is not a persistence fault")
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("denied save returned nonzero source %+v", source)
	}
	if len(rc.grantActions) != 2 || rc.grantActions[0] != "deploy" || rc.grantActions[1] != "delete" {
		t.Fatalf("grant actions = %v, want deploy then delete", rc.grantActions)
	}
	if len(rc.grantCalls) != 10 || rc.grantCalls[9].Value != "delete" {
		t.Fatalf("delete grant args = %+v, want the delete action bound last", rc.grantCalls)
	}
	// 正文已发布登记（登记事务已提交），节点事务被拒后回滚，号段未动。
	// The body was published (registry committed); the denied node tx
	// rolls back and no reservation ever runs.
	if begins, rollbacks := mainHub.beginsRollbacks(); begins != 2 || rollbacks != 1 {
		t.Fatalf("main begins/rollbacks = %d/%d, want 2/1", begins, rollbacks)
	}
	if commits := mainHub.commitCount(); commits != 1 {
		t.Fatalf("main commits = %d, want 1 (registry only)", commits)
	}
	if begins, _ := resHub.beginsRollbacks(); begins != 0 {
		t.Fatalf("reservation begins = %d, want 0 (denied before allocation)", begins)
	}
}

func TestSaveAuthorizedRecheckFaultsMarkedPersistence(t *testing.T) {
	stamp := int64(1_700_000_000_000)
	body := "authorized fault body\n"
	fresh := uploadFreshCalculation(t, body)
	cases := []struct {
		name   string
		mutate func(rc *uploadRecheckOptions)
		cause  error
	}{
		{
			name:   "schema probe fault",
			mutate: func(rc *uploadRecheckOptions) { rc.compatErr = errUploadFakeSchemaProbe },
			cause:  errUploadFakeSchemaProbe,
		},
		{
			name:   "schema mismatch",
			mutate: func(rc *uploadRecheckOptions) { rc.compatible = false },
			cause:  security.ErrConfiguration,
		},
		{
			name:   "set search path fault",
			mutate: func(rc *uploadRecheckOptions) { rc.setPathErr = errUploadFakeSetPath },
			cause:  errUploadFakeSetPath,
		},
		{
			name:   "commit lock fault",
			mutate: func(rc *uploadRecheckOptions) { rc.lockErr = errUploadFakeLock },
			cause:  errUploadFakeLock,
		},
		{
			name:   "deploy grant fault",
			mutate: func(rc *uploadRecheckOptions) { rc.deployErr = errUploadFakeGrant },
			cause:  errUploadFakeGrant,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saver, mainHub, resHub := uploadAuthorizedSaver(t)
			input := uploadTestInput(t, int64(len(body)))
			rc := &uploadRecheckOptions{}
			tc.mutate(rc)
			uploadAuthorizedHooks(t, mainHub, resHub, rc, input.Path, body, fresh, stamp)
			principal := security.NewPrincipal(7, "op-user")
			repo := security.RepositoryInfo{Key: "repo-x", Kind: "local", PackageType: "generic", Revision: 3}

			source, err := saver.SaveAuthorized(context.Background(), strings.NewReader(body), input, principal, repo, false)
			if err == nil {
				t.Fatal("recheck fault must fail the save")
			}
			if !uploadChainContains(err, "postgres upload: commit authorization failed") {
				t.Fatalf("stage text lost from the chain: %v", err)
			}
			if !errors.Is(err, tc.cause) {
				t.Fatalf("cause %v not reachable: %v", tc.cause, err)
			}
			if !errors.Is(err, localrepo.ErrUploadPersistence) {
				t.Fatalf("recheck fault = %v, want the ErrUploadPersistence marker", err)
			}
			if source != (repository.FileSnapshotSource{}) {
				t.Fatalf("failed save returned nonzero source %+v", source)
			}
			if begins, rollbacks := mainHub.beginsRollbacks(); begins != 2 || rollbacks != 1 {
				t.Fatalf("main begins/rollbacks = %d/%d, want 2/1", begins, rollbacks)
			}
			if begins, _ := resHub.beginsRollbacks(); begins != 0 {
				t.Fatalf("reservation begins = %d, want 0", begins)
			}
		})
	}
}

func TestSaveAuthorizedRejectsInvalidFactsBeforeDatabase(t *testing.T) {
	saver, mainHub, resHub := uploadAuthorizedSaver(t)
	body := "invalid facts body\n"
	input := uploadTestInput(t, int64(len(body)))
	principal := security.NewPrincipal(7, "op-user")
	cases := []struct {
		name string
		repo security.RepositoryInfo
	}{
		{"repo key mismatch", security.RepositoryInfo{Key: "other-repo", Kind: "local", PackageType: "generic", Revision: 1}},
		{"repo kind mismatch", security.RepositoryInfo{Key: "repo-x", Kind: "remote", PackageType: "generic", Revision: 1}},
		{"package type mismatch", security.RepositoryInfo{Key: "repo-x", Kind: "local", PackageType: "maven", Revision: 1}},
		{"zero revision", security.RepositoryInfo{Key: "repo-x", Kind: "local", PackageType: "generic", Revision: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := saver.SaveAuthorized(context.Background(), strings.NewReader(body), input, principal, tc.repo, false)
			if err == nil {
				t.Fatal("invalid authorization facts must fail")
			}
			if err.Error() != uploadInvalidFactsText {
				t.Fatalf("Error() = %q, want %q", err.Error(), uploadInvalidFactsText)
			}
			if !errors.Is(err, localrepo.ErrInvalidUploadInput) {
				t.Fatalf("invalid facts error = %v, want ErrInvalidUploadInput", err)
			}
			if source != (repository.FileSnapshotSource{}) {
				t.Fatalf("failed save returned nonzero source %+v", source)
			}
		})
	}
	if connects, _, _ := mainHub.snapshot(); connects != 0 {
		t.Fatalf("facts rejection opened %d main connections, want 0", connects)
	}
	if connects, _, _ := resHub.snapshot(); connects != 0 {
		t.Fatalf("facts rejection opened %d reservation connections, want 0", connects)
	}
}

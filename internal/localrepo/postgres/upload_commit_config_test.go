// 提交授权画像的构造与入口守卫单元契约：CommitSchema/ReservationDB 必须
// 成对、隔离且有界；授权 Saver 拒绝普通 Save，普通 Saver 拒绝
// SaveAuthorized，零值 Principal 在任何数据库访问前被拒（security.Principal
// 无包外构造器，深层 recheck 路径归 security 包自己的测试）。
// Unit contract for the commit-authorization profile's constructor and
// entry guards: CommitSchema/ReservationDB must be paired, separate and
// bounded; an authorized Saver rejects plain Save and a plain Saver rejects
// SaveAuthorized; a zero Principal is rejected before any database access
// (security.Principal has no constructor outside the security package, so
// the deeper recheck path belongs to the security package's own tests).
package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
	"github.com/0ldlight/bf/internal/security"
)

const uploadTestCommitSchema = "bf_upload_test"

const uploadCommitConfigText = "postgres upload: commit authorization requires an explicit schema and separate bounded reservation pool"
const uploadInvalidFactsText = "postgres upload: invalid commit authorization facts"

// uploadAuthorizedDeps 构造带独立预留池的依赖并返回两个 hub 供断言。
// uploadAuthorizedDeps builds deps with a separate reservation pool and
// returns both hubs for assertions.
func uploadAuthorizedDeps(t *testing.T) (UploadDeps, *uploadFakeHub, *uploadFakeHub) {
	t.Helper()
	mainHub := &uploadFakeHub{}
	resHub := &uploadFakeHub{}
	mainDB := uploadFakeDB(t, mainHub, 2)
	resDB := uploadFakeDB(t, resHub, 1)
	deps := UploadDeps{
		DB: mainDB, Publisher: uploadRealPublisher(t, mainDB),
		Clock: &uploadStubClock{value: 1_700_000_000_000}, NodeTimeout: 5 * time.Second,
		CommitSchema: uploadTestCommitSchema, ReservationDB: resDB,
	}
	return deps, mainHub, resHub
}

func TestNewUploadSaverCommitConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(d *UploadDeps)
	}{
		{"schema without reservation db", func(d *UploadDeps) { d.ReservationDB = nil }},
		{"reservation db without schema", func(d *UploadDeps) { d.CommitSchema = "" }},
		{"reservation db equals main db", func(d *UploadDeps) { d.ReservationDB = d.DB }},
		{"unbounded reservation pool", func(d *UploadDeps) {
			d.ReservationDB.SetMaxOpenConns(0) // 无限池：Stats 上界为 0。
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, mainHub, _ := uploadAuthorizedDeps(t)
			tc.mut(&deps)
			saver, err := NewUploadSaver(deps)
			if err == nil || saver != nil {
				t.Fatalf("NewUploadSaver(%s) = (%v, %v), want (nil, error)", tc.name, saver, err)
			}
			if err.Error() != uploadCommitConfigText {
				t.Fatalf("NewUploadSaver(%s) error = %q, want the fixed commit-config text", tc.name, err.Error())
			}
			if connects, _, _ := mainHub.snapshot(); connects != 0 {
				t.Fatalf("commit-config rejection opened %d connections, want 0", connects)
			}
		})
	}
}

func TestNewUploadSaverAcceptsPairedCommitConfigWithoutDatabase(t *testing.T) {
	deps, mainHub, resHub := uploadAuthorizedDeps(t)
	saver, err := NewUploadSaver(deps)
	if err != nil || saver == nil {
		t.Fatalf("NewUploadSaver authorized = (%v, %v), want (saver, nil)", saver, err)
	}
	if saver.commitSchema != uploadTestCommitSchema {
		t.Fatalf("commitSchema = %q, want %q", saver.commitSchema, uploadTestCommitSchema)
	}
	if connects, _, _ := mainHub.snapshot(); connects != 0 {
		t.Fatalf("main pool opened %d connections at construction, want 0", connects)
	}
	if connects, _, _ := resHub.snapshot(); connects != 0 {
		t.Fatalf("reservation pool opened %d connections at construction, want 0", connects)
	}
}

func TestAuthorizedSaverRejectsPlainSave(t *testing.T) {
	deps, mainHub, resHub := uploadAuthorizedDeps(t)
	saver, err := NewUploadSaver(deps)
	if err != nil || saver == nil {
		t.Fatalf("NewUploadSaver authorized = (%v, %v), want (saver, nil)", saver, err)
	}
	body := "authorized guard body\n"
	input := uploadTestInput(t, int64(len(body)))

	source, err := saver.Save(context.Background(), strings.NewReader(body), input)
	if err == nil {
		t.Fatal("plain Save on an authorized saver must fail")
	}
	if err.Error() != uploadCommitConfigText {
		t.Fatalf("plain Save error = %q, want the fixed commit-config text", err.Error())
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("rejected Save returned nonzero source %+v", source)
	}
	// 入口即拒：正文不读、连接不开。
	// Rejected at the entry: the body stays unread and no connection
	// opens.
	if connects, _, _ := mainHub.snapshot(); connects != 0 {
		t.Fatalf("plain Save rejection opened %d connections, want 0", connects)
	}
	if connects, _, _ := resHub.snapshot(); connects != 0 {
		t.Fatalf("plain Save rejection opened %d reservation connections, want 0", connects)
	}
}

func TestPlainSaverRejectsSaveAuthorized(t *testing.T) {
	h := &uploadFakeHub{}
	db := uploadFakeDB(t, h, 2)
	saver := uploadTestSaver(t, db, &uploadStubClock{value: 1_700_000_000_000}, 5*time.Second)
	body := "plain guard body\n"
	input := uploadTestInput(t, int64(len(body)))
	repo := security.RepositoryInfo{Key: "repo-x", Kind: "local", PackageType: "generic", Revision: 1}

	source, err := saver.SaveAuthorized(context.Background(), strings.NewReader(body), input, security.Principal{}, repo, true)
	if err == nil {
		t.Fatal("SaveAuthorized on a plain saver must fail")
	}
	if err.Error() != uploadCommitConfigText {
		t.Fatalf("SaveAuthorized error = %q, want the fixed commit-config text", err.Error())
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("rejected SaveAuthorized returned nonzero source %+v", source)
	}
	if connects, _, _ := h.snapshot(); connects != 0 {
		t.Fatalf("guard rejection opened %d connections, want 0", connects)
	}
}

func TestSaveAuthorizedRejectsZeroPrincipalBeforeDatabase(t *testing.T) {
	deps, mainHub, resHub := uploadAuthorizedDeps(t)
	saver, err := NewUploadSaver(deps)
	if err != nil || saver == nil {
		t.Fatalf("NewUploadSaver authorized = (%v, %v), want (saver, nil)", saver, err)
	}
	body := "zero principal body\n"
	input := uploadTestInput(t, int64(len(body)))
	repo := security.RepositoryInfo{Key: "repo-x", Kind: "local", PackageType: "generic", Revision: 1}

	source, err := saver.SaveAuthorized(context.Background(), strings.NewReader(body), input, security.Principal{}, repo, true)
	if err == nil {
		t.Fatal("zero Principal must fail SaveAuthorized")
	}
	if err.Error() != uploadInvalidFactsText {
		t.Fatalf("SaveAuthorized error = %q, want the invalid-facts text", err.Error())
	}
	if !errors.Is(err, localrepo.ErrInvalidUploadInput) {
		t.Fatalf("SaveAuthorized error = %v, want the ErrInvalidUploadInput marker", err)
	}
	if source != (repository.FileSnapshotSource{}) {
		t.Fatalf("failed SaveAuthorized returned nonzero source %+v", source)
	}
	// 零值 Principal 在事务开启前被拒：两个池都不得打开连接。
	// A zero Principal is rejected before any transaction: neither pool
	// may open a connection.
	if connects, _, _ := mainHub.snapshot(); connects != 0 {
		t.Fatalf("main pool opened %d connections, want 0", connects)
	}
	if connects, _, _ := resHub.snapshot(); connects != 0 {
		t.Fatalf("reservation pool opened %d connections, want 0", connects)
	}
}

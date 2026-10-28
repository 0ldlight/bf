// 流命令直接消费真实 reader；节点事务、登记与独立号段使用真实 PostgreSQL。
// The stream command consumes real readers with real node, registry and ID SQL.
package postgresit

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
)

// 只计数真实读取且暴露 Close；不替换发布结果或 SQL 查询结果。
// Count real reads and expose Close without replacing publication or SQL results.
type sslReader struct {
	source        *strings.Reader
	bytes, closes int
}

func (r *sslReader) Read(p []byte) (int, error) { n, e := r.source.Read(p); r.bytes += n; return n, e }
func (r *sslReader) Close() error               { r.closes++; return nil }
func sslSource(s string) *sslReader             { return &sslReader{source: strings.NewReader(s)} }

// 不预发布；唯一 Add 由受测入口调用。
// No prepublication: the command under test owns the only Add call.
func sslSetup(t *testing.T) (splFixture, *binary.Service) {
	t.Helper()
	db, schema := nodeFixture(t)
	ctx, adm := nodeTestContext(t), adminPool(t)
	res := cfReservations(t, db)
	svc, objects, root := cfBinaryService(t, db)
	tx, st := nodeTx(t, db)
	return splFixture{db: db, adm: adm, schema: schema, ctx: ctx, tx: tx, store: &splStore{st: st}, alloc: &cfDelegates{t: t, res: res, adm: adm, schema: schema, st: st}, objects: objects, root: root}, svc
}
func sslInput() localrepo.FileInput {
	return localrepo.FileInput{Repo: "saveR", Path: "dir/a.bin", Checksums: repository.ClientFileChecksums{SHA256Original: mdUnits("NO_ORIG")}, ResourceCreatedMillis: -7, CurrentUserUTF16: mdUnits("writer"), CreatedPolicy: repository.PreserveLoadedFileCreated, ChecksumPolicy: repository.LocalFileChecksumClient, Precheck: localrepo.SkipChecksumPrecheck}
}
func sslDeps(f splFixture, s *binary.Service, c repository.FileSaveClock) localrepo.FileDependencies {
	return localrepo.FileDependencies{Publisher: s, Node: splDeps(f, c)}
}
func sslRead(t *testing.T, r *sslReader, want int) {
	t.Helper()
	if r.bytes != want || r.closes != 0 {
		t.Fatalf("caller reader: bytes=%d closes=%d, want %d/0", r.bytes, r.closes, want)
	}
}

var sslABC = content.Metadata{SHA1: "a9993e364706816aba3e25717850c26c9cd0d89d", SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", MD5: "900150983cd24fb0d6963f7d28e17f72", Size: 3}
var sslABCD = content.Metadata{SHA1: "81fe8bfe87576c3ecb22426f8e57847382917acf", SHA256: "88d4266fd4e6338d13b845fcf289579d209c897823b9217da3e161936f031589", MD5: "e2fc714c4727ee9395f324cd2e7f331f", Size: 4}

func TestStreamLocalSaveCommitNoopUpdateRollback(t *testing.T) {
	f, svc := sslSetup(t)
	// 只种登记胜出记录，不提供字节；SaveFile 仍必须实际读取并发布 abc。
	// Seed only the canonical registry winner, never bytes; SaveFile must publish abc itself.
	winner := sslABC
	winner.MD5 = strings.Repeat("d", 32)
	if _, err := f.db.ExecContext(f.ctx, "INSERT INTO binaries(sha1,md5,bin_length,sha256) VALUES($1,$2,$3,$4)", winner.SHA1, winner.MD5, winner.Size, winner.SHA256); err != nil {
		t.Fatal(err)
	}
	in := sslInput()
	clock := &splClock{value: 41}
	r := sslSource("abc")
	got, err := localrepo.SaveFile(f.ctx, r, in, sslDeps(f, svc, clock))
	if err != nil || got.Published != winner || got.Save != (repository.LocalFileSaveResult{ID: 1, Created: true, Changed: true, Count: 1}) {
		t.Fatalf("stream create = %+v %v", got, err)
	}
	sslRead(t, r, 3)
	splRow(t, f, f.tx, 1, winner, 41, 41)
	ufRequireBinaryFields(t, f.adm, f.schema, winner)
	cfRequireObjectBytes(t, f.objects, winner.SHA1, "abc")
	cfRequireBound(t, f.adm, f.schema, 1)
	cfRequireCurrent(t, f.schema, 1)
	if countNodes(t, f.adm, f.schema) != 0 || clock.calls != 2 || f.store.lookup != 1 || f.store.create != 1 || f.store.update != 0 || f.alloc.next != 1 || f.alloc.reserve != 1 {
		t.Fatal("caller transaction committed or incorrect clock captures")
	}
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	splRow(t, f, f.adm, 1, winner, 41, 41)

	// 全新真实 Store 重新加载七字段；同内容无更新，新内容可回滚。
	// A fresh real Store reloads all seven fields; same bytes skip and changed bytes roll back.
	f.tx, f.alloc.st = nodeTx(t, f.db)
	f.store = &splStore{st: f.alloc.st}
	clock.calls = 0
	in.ResourceModifiedMillis = 41
	r = sslSource("abc")
	got, err = localrepo.SaveFile(f.ctx, r, in, sslDeps(f, svc, clock))
	if err != nil || got.Published != winner || got.Save != (repository.LocalFileSaveResult{ID: 1}) || clock.calls != 1 || f.store.update != 0 {
		t.Fatalf("fresh noop = %+v %v, clock=%d updates=%d", got, err, clock.calls, f.store.update)
	}
	sslRead(t, r, 3)
	in.ResourceModifiedMillis = 77
	clock.value = 88
	r = sslSource("abcd")
	got, err = localrepo.SaveFile(f.ctx, r, in, sslDeps(f, svc, clock))
	if err != nil || got.Published != sslABCD || got.Save != (repository.LocalFileSaveResult{ID: 1, Changed: true, Count: 1}) {
		t.Fatalf("stream update = %+v %v", got, err)
	}
	sslRead(t, r, 4)
	splRow(t, f, f.tx, 1, sslABCD, 77, 88)
	splRow(t, f, f.adm, 1, winner, 41, 41)
	if f.store.lookup != 2 || f.store.create != 0 || f.store.update != 1 || f.alloc.next != 1 || f.alloc.reserve != 1 || clock.calls != 2 {
		t.Fatalf("unexpected retry/allocation: %+v alloc=%d/%d clock=%d", f.store, f.alloc.next, f.alloc.reserve, clock.calls)
	}
	if err := f.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	splRow(t, f, f.adm, 1, winner, 41, 41)
	for _, m := range []content.Metadata{winner, sslABCD} {
		ufRequireBinaryFields(t, f.adm, f.schema, m)
	}
	cfRequireObjectBytes(t, f.objects, winner.SHA1, "abc")
	cfRequireObjectBytes(t, f.objects, sslABCD.SHA1, "abcd")
	if countRows(t, f.adm, f.schema) != 2 || cfCountFiles(t, f.root) != 2 {
		t.Fatal("rollback compensated registry or bytes, or repeated publication leaked files")
	}
	cfRequireCurrent(t, f.schema, 1)
}

func TestStreamLocalSaveChecksumStages(t *testing.T) {
	for _, tc := range []struct {
		name                string
		mode                localrepo.ChecksumPrecheck
		policy              repository.LocalFileChecksumPolicy
		length              bool
		want                error
		lookup, clocks, ids int
	}{
		{"verify-client", localrepo.VerifyChecksumPrecheck, repository.LocalFileChecksumClient, false, filestore.ErrChecksumMismatch, 0, 0, 0},
		{"verify-server", localrepo.VerifyChecksumPrecheck, repository.LocalFileChecksumServer, false, filestore.ErrChecksumMismatch, 0, 0, 0},
		{"skip-client", localrepo.SkipChecksumPrecheck, repository.LocalFileChecksumClient, false, repository.ErrClientChecksumMismatch, 1, 1, 0},
		{"skip-server", localrepo.SkipChecksumPrecheck, repository.LocalFileChecksumServer, false, nil, 1, 2, 1},
		{"length-skip", localrepo.SkipChecksumPrecheck, repository.LocalFileChecksumClient, true, filestore.ErrLengthMismatch, 0, 0, 0},
		{"length-verify", localrepo.VerifyChecksumPrecheck, repository.LocalFileChecksumClient, true, filestore.ErrLengthMismatch, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, svc := sslSetup(t)
			in := sslInput()
			in.Precheck, in.ChecksumPolicy = tc.mode, tc.policy
			in.Checksums.SHA1Original = mdUnits(strings.Repeat("0", 40))
			if tc.length {
				size := int64(4)
				in.DeclaredSize = &size
				in.Checksums.SHA1Original = nil
			}
			clock := &splClock{value: 123}
			r := sslSource("abc")
			got, err := localrepo.SaveFile(f.ctx, r, in, sslDeps(f, svc, clock))
			sslRead(t, r, 3)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || got != (localrepo.FileResult{}) {
					t.Fatalf("rejection = %+v %v, want %v", got, err, tc.want)
				}
			} else {
				if err != nil || got.Published != sslABC || got.Save != (repository.LocalFileSaveResult{ID: 1, Created: true, Changed: true, Count: 1}) {
					t.Fatalf("SERVER = %+v %v", got, err)
				}
				var original string
				if err := f.tx.QueryRowContext(f.ctx, "SELECT sha1_original FROM nodes WHERE node_id=1").Scan(&original); err != nil || original != strings.Repeat("0", 40) {
					t.Fatalf("raw original = %q %v", original, err)
				}
			}
			if f.store.lookup != tc.lookup || f.store.create != tc.ids || f.store.update != 0 || clock.calls != tc.clocks || f.alloc.next != tc.ids || f.alloc.reserve != tc.ids {
				t.Fatalf("wrong stages: %+v clock=%d IDs=%d/%d", f.store, clock.calls, f.alloc.next, f.alloc.reserve)
			}
			// 发布层失败清理 staging；节点拒绝及调用方回滚不能补偿已发布内容。
			// Publication failures clear staging; node rejection and caller rollback keep publication.
			published := 0
			if tc.lookup != 0 {
				published = 1
				ufRequireBinaryFields(t, f.adm, f.schema, sslABC)
				cfRequireObjectBytes(t, f.objects, sslABC.SHA1, "abc")
			}
			if err := f.tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if countRows(t, f.adm, f.schema) != published || cfCountFiles(t, f.root) != published || countNodes(t, f.adm, f.schema) != 0 {
				t.Fatal("wrong publication/staging/transaction survival")
			}
			cfRequireBound(t, f.adm, f.schema, int64(tc.ids))
			cfRequireCurrent(t, f.schema, int64(tc.ids))
		})
	}
}

func TestStreamLocalSaveInsertFailureKeepsPublicationAndID(t *testing.T) {
	f, svc := sslSetup(t)
	// 真 NoRows 之后竞争事务提交相同路径；受测 INSERT 必须原样返回 23505。
	// After real NoRows, a competing transaction commits the path; INSERT must return 23505.
	f.store.afterMissing = func() {
		ufRequireBinaryFields(t, f.adm, f.schema, sslABC)
		cfRequireObjectBytes(t, f.objects, sslABC.SHA1, "abc")
		id, err := f.alloc.nextID(f.ctx)
		if err != nil || id != 1 {
			t.Fatalf("competing reservation = %d %v", id, err)
		}
		md := repository.NewFileMetadata(repository.FileMetadataInput{CreatedMillis: -7, ModifiedMillis: 11, UpdatedMillis: 22, CreatedBy: repository.NewActor(mdUnits("writer")), ModifiedBy: repository.NewActor(mdUnits("writer"))})
		ufCommitNodeWithSnapshot(t, f.db, id, "saveR", "dir", "a.bin", sslABC, &md)
	}
	clock := &splClock{value: 99}
	r := sslSource("abc")
	got, err := localrepo.SaveFile(f.ctx, r, sslInput(), sslDeps(f, svc, clock))
	if got != (localrepo.FileResult{}) || err == nil || pgCodeOf(t, err) != "23505" {
		t.Fatalf("INSERT failure = %+v %v", got, err)
	}
	sslRead(t, r, 3)
	if f.store.lookup != 1 || f.store.create != 1 || f.store.update != 0 || f.alloc.next != 2 || f.alloc.reserve != 2 || clock.calls != 2 {
		t.Fatalf("retried/compensated: %+v IDs=%d/%d clock=%d", f.store, f.alloc.next, f.alloc.reserve, clock.calls)
	}
	if err := f.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	splRow(t, f, f.adm, 1, sslABC, 11, 22)
	if countNodes(t, f.adm, f.schema) != 1 || nodeIDExists(t, f.adm, f.schema, 2) {
		t.Fatal("failed caller INSERT survived")
	}
	cfRequireCurrent(t, f.schema, 2)
	cfRequireBound(t, f.adm, f.schema, 2)
	ufRequireBinaryFields(t, f.adm, f.schema, sslABC)
	cfRequireObjectBytes(t, f.objects, sslABC.SHA1, "abc")
	if countRows(t, f.adm, f.schema) != 1 || cfCountFiles(t, f.root) != 1 {
		t.Fatal("publication retried or compensated")
	}
}

// Compile-time assertion keeps the source's caller-owned close capability visible.
// 编译期断言保留调用方拥有的 Close 能力。
var _ io.ReadCloser = (*sslReader)(nil)

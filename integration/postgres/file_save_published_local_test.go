// Real command seams: publication, caller-owned SQL and committed ID reservations.
// These tests do not establish HTTP, authorization or complete session behavior.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

type splClock struct {
	value int64
	calls int
}

func (c *splClock) NowMillis() int64 { c.calls++; return c.value }

// Instrumentation counts actual methods; the hook runs only after real NoRows.
type splStore struct {
	st                     *refpg.Store
	lookup, create, update int
	afterMissing           func()
}

func (s *splStore) LookupSnapshotSource(ctx context.Context, p repository.Path) (repository.FileSnapshotSource, error) {
	s.lookup++
	src, err := s.st.LookupSnapshotSource(ctx, p)
	if errors.Is(err, sql.ErrNoRows) && s.afterMissing != nil {
		s.afterMissing()
	}
	return src, err
}
func (s *splStore) Create(ctx context.Context, r repository.FileReference) (int64, error) {
	s.create++
	return s.st.Create(ctx, r)
}
func (s *splStore) Update(ctx context.Context, r repository.FileReference) (int64, error) {
	s.update++
	return s.st.Update(ctx, r)
}

type splFixture struct {
	db, adm *sql.DB
	schema  string
	ctx     context.Context
	tx      *sql.Tx
	store   *splStore
	alloc   *cfDelegates
	objects *filestore.Store
	root    string
	meta    content.Metadata
}

func splSetup(t *testing.T) splFixture {
	t.Helper()
	db, schema := nodeFixture(t)
	ctx, adm := nodeTestContext(t), adminPool(t)
	res := cfReservations(t, db)
	svc, objects, root := cfBinaryService(t, db)
	meta, err := svc.Add(ctx, strings.NewReader("save command published bytes"), filestore.Expected{})
	if err != nil {
		t.Fatal(err)
	}
	tx, st := nodeTx(t, db)
	return splFixture{db, adm, schema, ctx, tx, &splStore{st: st}, &cfDelegates{t: t, res: res, adm: adm, schema: schema, st: st}, objects, root, meta}
}
func splInput(meta content.Metadata) repository.LocalFileSaveInput {
	return repository.LocalFileSaveInput{Repo: "saveR", Path: "dir/a.bin", Published: meta,
		Checksums:             repository.ClientFileChecksums{SHA256Original: mdUnits("NO_ORIG")},
		ResourceCreatedMillis: -7, CurrentUserUTF16: mdUnits("writer"),
		CreatedPolicy: repository.PreserveLoadedFileCreated, ChecksumPolicy: repository.LocalFileChecksumClient}
}
func splDeps(f splFixture, clock repository.FileSaveClock) repository.LocalFileSaveDependencies {
	cat, err := repository.NewFileMimeCatalog([]repository.FileMimeDefinition{{Type: "application/octet-stream", Extensions: "bin"}}, repository.FileMimeCaseRootASCII)
	if err != nil {
		panic(err)
	}
	return repository.LocalFileSaveDependencies{Store: f.store, NextID: f.alloc.nextID, Clock: clock, Catalog: cat}
}

// Independent SQL oracle: no producer or snapshot loader computes expected fields.
func splRow(t *testing.T, f splFixture, r ncRowReader, id int64, meta content.Metadata, modified, updated int64) {
	t.Helper()
	var repo, path, name, sha1, sha256, md5 string
	var kind, depth int
	var size, created, mod, upd int64
	var creator, modifier, so, mo sql.NullString
	err := r.QueryRowContext(f.ctx, fmt.Sprintf("SELECT repo,node_path,node_name,node_type,depth,bin_length,sha1_actual,sha256,md5_actual,created,modified,updated,created_by,modified_by,sha1_original,md5_original FROM %s.nodes WHERE node_id=$1", f.schema), id).Scan(&repo, &path, &name, &kind, &depth, &size, &sha1, &sha256, &md5, &created, &mod, &upd, &creator, &modifier, &so, &mo)
	if err != nil {
		t.Fatal(err)
	}
	if repo != "saveR" || path != "dir" || name != "a.bin" || kind != 1 || depth != 2 || size != meta.Size || sha1 != meta.SHA1 || sha256 != meta.SHA256 || md5 != meta.MD5 {
		t.Fatal("SQL identity/content mismatch")
	}
	if created != -7 || mod != modified || upd != updated || !creator.Valid || creator.String != "writer" || !modifier.Valid || modifier.String != "writer" || so.Valid || mo.Valid {
		t.Fatalf("SQL metadata mismatch: %d/%d/%d/%v/%v/%v/%v", created, mod, upd, creator, modifier, so, mo)
	}
}
func splKept(t *testing.T, f splFixture, bound int64) {
	t.Helper()
	cfRequireBound(t, f.adm, f.schema, bound)
	cfRequireCurrent(t, f.schema, bound)
	ufRequireBinaryFields(t, f.adm, f.schema, f.meta)
	cfRequireObjectBytes(t, f.objects, f.meta.SHA1, "save command published bytes")
	if cfCountFiles(t, f.root) != 1 {
		t.Fatal("published object was compensated or duplicated")
	}
}

func TestSavePublishedLocalFileCreateUpdateAndRollback(t *testing.T) {
	f := splSetup(t)
	in := splInput(f.meta)
	deps := splDeps(f, repository.SystemFileSaveClock{})
	// Loaded-only guards must not reject a genuinely missing node.
	in.CreatedPolicy = 0
	in.OriginalCase = 255
	deps.Catalog = repository.FileMimeCatalog{}
	before := time.Now().UnixMilli()
	got, err := repository.SavePublishedLocalFile(f.ctx, in, deps)
	after := time.Now().UnixMilli()
	if err != nil || got != (repository.LocalFileSaveResult{ID: 1, Created: true, Changed: true, Count: 1}) {
		t.Fatalf("create = %+v, %v", got, err)
	}
	var modified, updated int64
	if err := f.tx.QueryRowContext(f.ctx, "SELECT modified,updated FROM nodes WHERE node_id=1").Scan(&modified, &updated); err != nil {
		t.Fatal(err)
	}
	if modified < before || modified > after || updated < before || updated > after {
		t.Fatalf("system clock outside real window: %d/%d [%d,%d]", modified, updated, before, after)
	}
	splRow(t, f, f.tx, 1, f.meta, modified, updated)
	if f.store.lookup != 1 || f.store.create != 1 || f.store.update != 0 || f.alloc.next != 1 || f.alloc.reserve != 1 {
		t.Fatal("creation must lookup, reserve and insert exactly once")
	}
	if countNodes(t, f.adm, f.schema) != 0 {
		t.Fatal("command committed caller transaction")
	}
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	splRow(t, f, f.adm, 1, f.meta, modified, updated)
	f.tx, f.alloc.st = nodeTx(t, f.db)
	f.store = &splStore{st: f.alloc.st}
	path := fileRefOf(t, 0, "saveR", "dir", "a.bin", f.meta).Path
	source, err := f.store.st.LookupSnapshotSource(f.ctx, path)
	if err != nil || source.Reference.ID != 1 || source.Reference.Metadata == nil {
		t.Fatalf("fresh read: %+v %v", source, err)
	}
	in = splInput(f.meta)
	in.ResourceModifiedMillis = modified
	clock := &splClock{value: updated}
	deps = splDeps(f, clock)
	got, err = repository.SavePublishedLocalFile(f.ctx, in, deps)
	if err != nil || got != (repository.LocalFileSaveResult{ID: 1}) || clock.calls != 1 || f.store.update != 0 {
		t.Fatalf("real no-op = %+v %v; clock=%d updates=%d", got, err, clock.calls, f.store.update)
	}
	in.ResourceModifiedMillis = 77
	clock.value = 88
	got, err = repository.SavePublishedLocalFile(f.ctx, in, deps)
	if err != nil || got != (repository.LocalFileSaveResult{ID: 1, Changed: true, Count: 1}) {
		t.Fatalf("hit update = %+v %v", got, err)
	}
	splRow(t, f, f.tx, 1, f.meta, 77, 88)
	splRow(t, f, f.adm, 1, f.meta, modified, updated)
	if f.alloc.next != 1 || f.alloc.reserve != 1 || f.store.lookup != 2 || f.store.create != 0 || f.store.update != 1 || clock.calls != 2 {
		t.Fatal("command retried, allocated on hit or skipped a real stage")
	}
	if err := f.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	splRow(t, f, f.adm, 1, f.meta, modified, updated)
	splKept(t, f, 1)
}

func TestSavePublishedLocalFileRejectionsPreserveRows(t *testing.T) {
	for _, kind := range []string{"folder", "legacy", "lookup-error", "client-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			f := splSetup(t)
			in := splInput(f.meta)
			want := error(repository.ErrClientChecksumMismatch)
			rows, clocks := 0, 1
			switch kind {
			case "folder":
				_, err := f.db.ExecContext(f.ctx, "INSERT INTO nodes(node_id,node_type,repo,node_path,node_name,depth) VALUES(501,0,'saveR','dir','a.bin',2)")
				if err != nil {
					t.Fatal(err)
				}
				want = refpg.ErrNotFile
				rows = 1
				clocks = 0
			case "legacy":
				ufCommitNodeWithSnapshot(t, f.db, 502, "saveR", "dir", "a.bin", f.meta, nil)
				want = repository.ErrInvalidFileSnapshotSource
				rows = 1
				clocks = 0
			case "lookup-error":
				ufCommitNodeWithSnapshot(t, f.db, 503, "saveR", "dir", "a.bin", f.meta, nil)
				if err := f.tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				want = sql.ErrTxDone
				rows = 1
				clocks = 0
			case "client-mismatch":
				in.Checksums.SHA1Original = mdUnits(strings.Repeat("0", 40))
			}
			clock := &splClock{value: 123}
			got, err := repository.SavePublishedLocalFile(f.ctx, in, splDeps(f, clock))
			if !errors.Is(err, want) || got != (repository.LocalFileSaveResult{}) {
				t.Fatalf("rejection = %+v %v, want %v", got, err, want)
			}
			if f.store.lookup != 1 || f.store.create != 0 || f.store.update != 0 || f.alloc.next != 0 || f.alloc.reserve != 0 || clock.calls != clocks {
				t.Fatalf("rejection side effects: %+v clock=%d allocations=%d", f.store, clock.calls, f.alloc.next)
			}
			if countNodes(t, f.adm, f.schema) != rows {
				t.Fatal("rejection changed committed rows")
			}
			if rows == 1 {
				var id int64
				var nodeType int
				if err := f.db.QueryRowContext(f.ctx, "SELECT node_id,node_type FROM nodes WHERE repo='saveR' AND node_path='dir' AND node_name='a.bin'").Scan(&id, &nodeType); err != nil {
					t.Fatal(err)
				}
				expectedID := int64(502)
				expectedType := 1
				if kind == "folder" {
					expectedID = 501
					expectedType = 0
				}
				if kind == "lookup-error" {
					expectedID = 503
				}
				if id != expectedID || nodeType != expectedType {
					t.Fatal("stored rejection target changed")
				}
			}
			splKept(t, f, 0)
		})
	}
}

func TestSavePublishedLocalFileDuplicateDoesNotRetryOrCompensate(t *testing.T) {
	f := splSetup(t)
	// The first real lookup has returned NoRows. Another caller then commits the
	// same key before our plain INSERT, exercising actual SQLSTATE 23505.
	f.store.afterMissing = func() {
		id, err := f.alloc.nextID(f.ctx)
		if err != nil || id != 1 {
			t.Fatalf("racing reservation: %d %v", id, err)
		}
		md := repository.NewFileMetadata(repository.FileMetadataInput{CreatedMillis: -7, ModifiedMillis: 11, UpdatedMillis: 22, CreatedBy: repository.NewActor(mdUnits("writer")), ModifiedBy: repository.NewActor(mdUnits("writer"))})
		ufCommitNodeWithSnapshot(t, f.db, id, "saveR", "dir", "a.bin", f.meta, &md)
	}
	clock := &splClock{value: 99}
	got, err := repository.SavePublishedLocalFile(f.ctx, splInput(f.meta), splDeps(f, clock))
	if got != (repository.LocalFileSaveResult{}) || err == nil || pgCodeOf(t, err) != "23505" {
		t.Fatalf("duplicate = %+v %v", got, err)
	}
	if f.store.lookup != 1 || f.store.create != 1 || f.store.update != 0 || f.alloc.next != 2 || f.alloc.reserve != 2 || clock.calls != 2 {
		t.Fatalf("retry/upsert or stage mismatch: %+v alloc=%d reserve=%d clock=%d", f.store, f.alloc.next, f.alloc.reserve, clock.calls)
	}
	if err := f.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if countNodes(t, f.adm, f.schema) != 1 || nodeIDExists(t, f.adm, f.schema, 2) {
		t.Fatal("duplicate write survived caller rollback")
	}
	splRow(t, f, f.adm, 1, f.meta, 11, 22)
	splKept(t, f, 2)
}

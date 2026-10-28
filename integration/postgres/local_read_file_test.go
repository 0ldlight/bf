// 真实流发布、调用方事务与新 Store 的读取契约；不模拟 SQL 或对象字节。
// Real stream publication, caller transactions and fresh-store reads; no fake SQL or bytes.
package postgresit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// 透明计数器始终委托真实文件 Store；只验证空流禁止打开物理对象。
// Transparent instrumentation delegates to the real store; only empty reads forbid Open.
type lrfOpener struct {
	store *filestore.Store
	calls int
}

func (o *lrfOpener) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o.calls++
	return o.store.Open(ctx, key)
}
func lrfSQL(t *testing.T, f splFixture, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(f.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}
func lrfState(t *testing.T, f splFixture, readers ...ncRowReader) string {
	t.Helper()
	var reader ncRowReader = f.db
	if len(readers) != 0 {
		reader = readers[0]
	}
	var state string
	if err := reader.QueryRowContext(f.ctx, `SELECT json_build_array(
		(SELECT json_agg(n ORDER BY node_id) FROM nodes n),
		(SELECT json_agg(b ORDER BY sha1) FROM binaries b))::text`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
func lrfBytes(t *testing.T, body io.ReadCloser, want string) {
	t.Helper()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != want {
		t.Fatalf("body = %q %v, want %q", got, err, want)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalReadFileCommittedStreamAndCallerTransaction(t *testing.T) {
	f, svc := sslSetup(t)
	winner := sslABC
	winner.MD5 = strings.Repeat("d", 32)
	lrfSQL(t, f, "INSERT INTO binaries(sha1,md5,bin_length,sha256) VALUES($1,$2,$3,$4)", winner.SHA1, winner.MD5, winner.Size, winner.SHA256)
	in := sslInput()
	in.Path = "dir/a.bin:properties:statistics"
	saved, err := localrepo.SaveFile(f.ctx, sslSource("abc"), in, sslDeps(f, svc, &splClock{value: 41}))
	if err != nil || saved.Published != winner || saved.Save.ID != 1 {
		t.Fatalf("stream save = %+v %v", saved, err)
	}
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 节点长度是提示，不得截断、扩充或用 fstat 重写返回的 Source。
	// Node length is a hint: never truncate, pad or replace Source using fstat.
	lrfSQL(t, f, "UPDATE nodes SET bin_length=999 WHERE node_id=1")
	tx, st := nodeTx(t, f.db)
	path := fileRefOf(t, 0, "saveR", "dir", "a.bin:properties", winner).Path
	before := lrfState(t, f)
	exact, err := st.LookupSnapshotSource(f.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := localrepo.ReadFile(f.ctx, path, localrepo.FileReadDependencies{Store: st, Content: f.objects})
	if err != nil || !got.Found || got.Body == nil {
		t.Fatalf("fresh read = %+v %v", got, err)
	}
	defer got.Body.Close()
	src := got.Source
	if !reflect.DeepEqual(src, exact) {
		t.Fatalf("source changed from SQL projection: %+v vs %+v", src, exact)
	}
	md := src.Reference.Metadata
	if src.Reference.ID != 1 || src.Reference.Path != path || src.Reference.Content.SHA1 != "a9993e364706816aba3e25717850c26c9cd0d89d" || src.Reference.Content.MD5 != strings.Repeat("d", 32) || src.Reference.Content.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || src.Reference.Content.Size != 999 || !src.SHA1ActualValid || !src.MD5ActualValid || !src.SHA256ActualValid {
		t.Fatalf("canonical node source = %+v", src)
	}
	if md == nil || md.CreatedMillis() != -7 || md.ModifiedMillis() != 41 || md.UpdatedMillis() != 41 || !reflect.DeepEqual(md.CreatedBy().UTF16(), mdUnits("writer")) || !reflect.DeepEqual(md.ModifiedBy().UTF16(), mdUnits("writer")) || md.SHA1OriginalUTF16() != nil || md.MD5OriginalUTF16() != nil {
		t.Fatalf("saved node metadata = %+v", md)
	}
	if lrfState(t, f, tx) != before {
		t.Fatal("read wrote inside caller transaction")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// 文件描述符由调用者持有，SQL 事务结束后仍可读取。
	// The caller owns the descriptor, which remains readable after SQL rollback.
	lrfBytes(t, got.Body, "abc")
	_, fresh := nodeTx(t, f.db)
	again, err := localrepo.ReadFile(f.ctx, path, localrepo.FileReadDependencies{Store: fresh, Content: f.objects})
	if err != nil || !again.Found || again.Body == nil || again.Body == got.Body || !reflect.DeepEqual(again.Source, src) {
		t.Fatalf("independent second read = %+v %v", again, err)
	}
	lrfBytes(t, again.Body, "abc")
	if lrfState(t, f) != before || countNodes(t, f.adm, f.schema) != 1 || countRows(t, f.adm, f.schema) != 1 {
		t.Fatal("read changed node or canonical registry")
	}
	cfRequireCurrent(t, f.schema, 1)
	cfRequireBound(t, f.adm, f.schema, 1)
	ufRequireBinaryFields(t, f.adm, f.schema, winner)
}

func TestLocalReadFileAbsenceLegacyAndActualProfiles(t *testing.T) {
	f, svc := sslSetup(t)
	if _, err := localrepo.SaveFile(f.ctx, sslSource("abc"), sslInput(), sslDeps(f, svc, &splClock{value: 41})); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedNodeBinary(t, f.adm, f.schema, "")
	tx, st := nodeTx(t, f.db)
	path := fileRefOf(t, 0, "saveR", "dir", "a.bin", sslABC).Path
	read := func(p repository.Path, want error, found bool) {
		t.Helper()
		before := lrfState(t, f, tx)
		got, err := localrepo.ReadFile(f.ctx, p, localrepo.FileReadDependencies{Store: st, Content: f.objects})
		if !errors.Is(err, want) || got.Found != found {
			t.Fatalf("read = %+v %v, want found=%v cause=%v", got, err, found, want)
		}
		if !found && got != (localrepo.FileReadResult{}) {
			t.Fatalf("failed/absent result is nonzero: %+v", got)
		}
		if found {
			if got.Body == nil || got.Source.Reference.ID != 1 || got.Source.Reference.Metadata != nil || !got.Source.SHA1ActualValid || got.Source.Reference.Content != sslABC {
				t.Fatalf("legacy source = %+v", got.Source)
			}
			lrfBytes(t, got.Body, "abc")
		}
		if lrfState(t, f, tx) != before {
			t.Fatal("read rewrote stored rows")
		}
	}
	read(fileRefOf(t, 0, "saveR", "dir", "missing.bin", sslABC).Path, nil, false)
	lrfSQL(t, f, "INSERT INTO nodes(node_id,node_type,repo,node_path,node_name,depth) VALUES(2,0,'saveR','dir','folder',2)")
	read(fileRefOf(t, 0, "saveR", "dir", "folder", sslABC).Path, refpg.ErrNotFile, false)
	lrfSQL(t, f, "UPDATE nodes SET created=NULL,modified=NULL,updated=NULL,created_by=NULL,modified_by=NULL,sha1_original=NULL,md5_original=NULL WHERE node_id=1")
	read(path, nil, true)
	lrfSQL(t, f, "UPDATE nodes SET sha1_actual=NULL WHERE node_id=1")
	read(path, localrepo.ErrInvalidFileReadSource, false)
	lrfSQL(t, f, "UPDATE nodes SET sha1_actual='' WHERE node_id=1")
	read(path, filestore.ErrInvalidKey, false)
	lrfSQL(t, f, "UPDATE nodes SET sha1_actual=$1 WHERE node_id=1", sslABC.SHA1)
	object := filepath.Join(f.root, "a9", sslABC.SHA1)
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	read(path, os.ErrNotExist, false)
	// 真符号链接错误保留安全哨兵，不能当作节点缺失。
	// Real object symlinks preserve the safety sentinel, never node absence.
	if err := os.Symlink("missing-target", object); err != nil {
		t.Fatal(err)
	}
	read(path, filestore.ErrUnsafePath, false)
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, "a9")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-fanout", filepath.Join(f.root, "a9")); err != nil {
		t.Fatal(err)
	}
	read(path, filestore.ErrUnsafePath, false)
	cfRequireCurrent(t, f.schema, 1)
	cfRequireBound(t, f.adm, f.schema, 1)
	if countNodes(t, f.adm, f.schema) != 2 || countRows(t, f.adm, f.schema) != 2 {
		t.Fatal("read allocated or removed rows")
	}
}

func TestLocalReadFileEmptyHashWithoutPhysicalObject(t *testing.T) {
	f, svc := sslSetup(t)
	const emptySHA1 = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	saved, err := localrepo.SaveFile(f.ctx, sslSource(""), sslInput(), sslDeps(f, svc, &splClock{value: 41}))
	if err != nil || saved.Published.SHA1 != emptySHA1 || saved.Published.Size != 0 || saved.Save.ID != 1 {
		t.Fatalf("empty stream save = %+v %v", saved, err)
	}
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.root, "da", emptySHA1)); err != nil {
		t.Fatal(err)
	}
	lrfSQL(t, f, "UPDATE nodes SET bin_length=7 WHERE node_id=1")
	tx, st := nodeTx(t, f.db)
	path := fileRefOf(t, 0, "saveR", "dir", "a.bin", saved.Published).Path
	opener := &lrfOpener{store: f.objects}
	before := lrfState(t, f, tx)
	first, err := localrepo.ReadFile(f.ctx, path, localrepo.FileReadDependencies{Store: st, Content: opener})
	if err != nil || !first.Found || first.Body == nil || first.Source.Reference.ID != 1 || first.Source.Reference.Content.SHA1 != emptySHA1 || first.Source.Reference.Content.Size != 7 || !first.Source.SHA1ActualValid {
		t.Fatalf("absent empty object = %+v %v", first, err)
	}
	defer first.Body.Close()
	second, err := localrepo.ReadFile(f.ctx, path, localrepo.FileReadDependencies{Store: st, Content: opener})
	if err != nil || !second.Found || second.Body == nil || second.Body == first.Body || !reflect.DeepEqual(second.Source, first.Source) {
		t.Fatalf("independent empty read = %+v %v", second, err)
	}
	defer second.Body.Close()
	buf := make([]byte, 1)
	if n, err := first.Body.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("open empty read = %d %v", n, err)
	}
	for i := 0; i < 2; i++ {
		if err := first.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := first.Body.Read(nil); n != 0 || err != nil {
		t.Fatalf("closed zero-length read = %d %v", n, err)
	}
	if n, err := first.Body.Read(buf); n != 0 || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed empty read = %d %v", n, err)
	}
	lrfBytes(t, second.Body, "")
	if opener.calls != 0 || lrfState(t, f, tx) != before || cfCountFiles(t, f.root) != 0 {
		t.Fatal("empty read opened/repaired object or rewrote source")
	}
	cfRequireCurrent(t, f.schema, 1)
	cfRequireBound(t, f.adm, f.schema, 1)
	// 回滚节点不补偿已发布字节；新 Store 只能看到真实节点缺失。
	// Node rollback keeps published bytes; a fresh store sees actual node absence.
	f.tx, f.alloc.st = nodeTx(t, f.db)
	f.store = &splStore{st: f.alloc.st}
	in := sslInput()
	in.Path = "dir/rolled-back.bin"
	if _, err := localrepo.SaveFile(f.ctx, sslSource("abc"), in, sslDeps(f, svc, &splClock{value: 42})); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, fresh := nodeTx(t, f.db)
	orphanPath := fileRefOf(t, 0, "saveR", "dir", "rolled-back.bin", sslABC).Path
	missing, err := localrepo.ReadFile(f.ctx, orphanPath, localrepo.FileReadDependencies{Store: fresh, Content: opener})
	if err != nil || missing != (localrepo.FileReadResult{}) || opener.calls != 0 || countNodes(t, f.adm, f.schema) != 1 || countRows(t, f.adm, f.schema) != 2 {
		t.Fatalf("rollback visibility = %+v %v", missing, err)
	}
	cfRequireObjectBytes(t, f.objects, sslABC.SHA1, "abc")
	cfRequireCurrent(t, f.schema, 2)
	cfRequireBound(t, f.adm, f.schema, 2)
}

// Original 的真实写入、回读及后续 binary 更新接缝；不拼 Desired 或替换 Store。
// Real Original write/read/subsequent-binary seams, without manual Desired or fake Store.
package postgresit

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
)

func loaReload(t *testing.T, f lbFixture) lbFixture {
	t.Helper()
	tx, st := nodeTx(t, f.db)
	source, err := st.LookupSnapshotSource(f.ctx, f.source.Reference.Path)
	if err != nil || source.Reference.ID != f.source.Reference.ID || source.Reference.Metadata == nil {
		t.Fatalf("fresh Original lookup = (%+v,%v)", source, err)
	}
	f.tx, f.st, f.source = tx, st, source
	return f
}

// 比较字面期望；快照组装不是 SQL 值的 oracle，SQL 另由 ccRow 完整核对。
// Compare literal expectations; ccRow independently verifies the complete SQL row.
func loaSnapshotOriginals(t *testing.T, f lbFixture, sha1Original, md5Original string) {
	t.Helper()
	snapshot, err := repository.LoadedFileSnapshot(f.source)
	if err != nil {
		t.Fatalf("fresh Original assembly: %v", err)
	}
	seen := make(map[repository.ChecksumKind]bool)
	for _, entry := range snapshot.Fields().Checksums {
		if seen[entry.Kind] {
			t.Fatalf("duplicate loaded checksum kind %d", entry.Kind)
		}
		seen[entry.Kind] = true
		switch entry.Kind {
		case repository.ChecksumSHA1:
			equalMDUnits(t, "loaded SHA1 Original", entry.Original, mdUnits(sha1Original))
		case repository.ChecksumMD5:
			equalMDUnits(t, "loaded MD5 Original", entry.Original, mdUnits(md5Original))
		case repository.ChecksumSHA256:
			equalMDUnits(t, "loaded SHA256 Original", entry.Original, mdUnits("NO_ORIG"))
		default:
			t.Fatalf("unexpected loaded checksum kind %d", entry.Kind)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("loaded checksum kinds = %d, want 3", len(seen))
	}
}

func loaBinary(t *testing.T, f lbFixture, published content.Metadata, changed bool, sha1Original, md5Original string) {
	t.Helper()
	d := &ufDelegates{st: f.st}
	got, err := repository.UpdateLoadedFileBinary(f.ctx, repository.LoadedFileBinaryInput{
		Source: f.source, Published: published, CreatedPolicy: repository.PreserveLoadedFileCreated,
	}, f.catalog, d.updateRef)
	wantCount := int64(0)
	if changed {
		wantCount = 1
	}
	if err != nil || got.Changed != changed || got.Count != wantCount {
		t.Fatalf("loaded Original binary update = (%+v,%v), want Changed=%v Count=%d", got, err, changed, wantCount)
	}
	if !changed {
		if d.update != 0 {
			t.Fatalf("same-binary Store calls = %d, want 0", d.update)
		}
		return
	}
	d.wantUpdateOnce(t)
	if d.saved.ID != f.source.Reference.ID || d.saved.Path != f.source.Reference.Path || d.saved.Content != published || d.saved.Metadata == nil {
		t.Fatal("binary write lost source identity, published content or metadata")
	}
	equalMDUnits(t, "written SHA1 Original", d.saved.Metadata.SHA1OriginalUTF16(), mdUnits(sha1Original))
	equalMDUnits(t, "written MD5 Original", d.saved.Metadata.MD5OriginalUTF16(), mdUnits(md5Original))
}

// SERVER 写侧保留非 hex 大写，加载侧才小写；相同 binary 不为规范化而写行。
// SERVER stores uppercase nonhex verbatim; load normalizes, while equal binary skips.
func TestLoadedOriginalASCIIServerReadUpdateCommitRollback(t *testing.T) {
	f := lbSetup(t)
	const modified, updated = int64(4444), int64(5555)
	user := "original-server"
	rawSHA1, rawMD5 := strings.Repeat("G", 39)+"Z", strings.Repeat("Z", 31)+"G"
	wantSHA1, wantMD5 := strings.Repeat("g", 39)+"z", strings.Repeat("z", 31)+"g"
	checksums := ccMatching(f.next)
	checksums.SHA1Original, checksums.MD5Original = mdUnits(rawSHA1), mdUnits(rawMD5)
	ccSuccess(t, f, f.next, checksums, repository.LocalFileChecksumServer, modified, updated, mdUnits(user), true)
	ccRow(t, f, f.tx, f.next, modified, updated, &user, &rawSHA1, &rawMD5)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("SERVER Original commit: %v", err)
	}
	ccRow(t, f, f.adm, f.next, modified, updated, &user, &rawSHA1, &rawMD5)

	f = loaReload(t, f)
	equalMDUnits(t, "raw read SHA1 Original", f.source.Reference.Metadata.SHA1OriginalUTF16(), mdUnits(rawSHA1))
	equalMDUnits(t, "raw read MD5 Original", f.source.Reference.Metadata.MD5OriginalUTF16(), mdUnits(rawMD5))
	loaSnapshotOriginals(t, f, wantSHA1, wantMD5)
	loaBinary(t, f, f.next, false, wantSHA1, wantMD5)
	ccRow(t, f, f.tx, f.next, modified, updated, &user, &rawSHA1, &rawMD5)
	ccRow(t, f, f.adm, f.next, modified, updated, &user, &rawSHA1, &rawMD5)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("same-binary caller commit: %v", err)
	}

	f = loaReload(t, f)
	loaBinary(t, f, f.winner, true, wantSHA1, wantMD5)
	ccRow(t, f, f.tx, f.winner, modified, updated, &user, &wantSHA1, &wantMD5)
	ccRow(t, f, f.adm, f.next, modified, updated, &user, &rawSHA1, &rawMD5)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("normalized Original caller commit: %v", err)
	}
	ccRow(t, f, f.adm, f.winner, modified, updated, &user, &wantSHA1, &wantMD5)

	f = loaReload(t, f)
	loaSnapshotOriginals(t, f, wantSHA1, wantMD5)
	loaBinary(t, f, f.next, true, wantSHA1, wantMD5)
	ccRow(t, f, f.tx, f.next, modified, updated, &user, &wantSHA1, &wantMD5)
	ccRow(t, f, f.adm, f.winner, modified, updated, &user, &wantSHA1, &wantMD5)
	if err := f.tx.Rollback(); err != nil {
		t.Fatalf("subsequent binary caller rollback: %v", err)
	}
	ccRow(t, f, f.adm, f.winner, modified, updated, &user, &wantSHA1, &wantMD5)
	if n := countNodes(t, f.adm, f.schema); n != 1 {
		t.Fatalf("node count = %d, want 1", n)
	}
}

// CLIENT 的匹配 hex 路线仍可写、回读、再更新；不把 Original 重验为新 Actual。
// Matching CLIENT hex still writes, reloads and updates; no revalidation against new Actual.
func TestLoadedOriginalASCIIClientReadThenBinaryUpdate(t *testing.T) {
	f := lbSetup(t)
	const modified, updated = int64(6666), int64(7777)
	user := "original-client"
	ccSuccess(t, f, f.next, ccMatching(f.next), repository.LocalFileChecksumClient, modified, updated, mdUnits(user), true)
	ccRow(t, f, f.tx, f.next, modified, updated, &user, &f.next.SHA1, &f.next.MD5)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("CLIENT Original commit: %v", err)
	}
	ccRow(t, f, f.adm, f.next, modified, updated, &user, &f.next.SHA1, &f.next.MD5)

	f = loaReload(t, f)
	loaSnapshotOriginals(t, f, f.next.SHA1, f.next.MD5)
	loaBinary(t, f, f.next, false, f.next.SHA1, f.next.MD5)
	loaBinary(t, f, f.winner, true, f.next.SHA1, f.next.MD5)
	ccRow(t, f, f.tx, f.winner, modified, updated, &user, &f.next.SHA1, &f.next.MD5)
	ccRow(t, f, f.adm, f.next, modified, updated, &user, &f.next.SHA1, &f.next.MD5)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("CLIENT-derived binary caller commit: %v", err)
	}
	ccRow(t, f, f.adm, f.winner, modified, updated, &user, &f.next.SHA1, &f.next.MD5)
	f = loaReload(t, f)
	loaSnapshotOriginals(t, f, f.next.SHA1, f.next.MD5)
	loaBinary(t, f, f.winner, false, f.next.SHA1, f.next.MD5)
	if err := f.tx.Commit(); err != nil {
		t.Fatalf("CLIENT-derived no-op caller commit: %v", err)
	}
	ccRow(t, f, f.adm, f.winner, modified, updated, &user, &f.next.SHA1, &f.next.MD5)
}

// SERVER 接受写入不等于加载器接受：I/非 ASCII 的 Locale 支持仍显式未完成。
// SERVER acceptance does not imply loader support: I/non-ASCII remain explicit exclusions.
func TestLoadedOriginalASCIIUnsupportedReadPreservesRow(t *testing.T) {
	for _, row := range []struct {
		name, sha1Original, md5Original string
	}{
		{"SHA1-uppercase-I", strings.Repeat("I", 40), strings.Repeat("0", 32)},
		{"MD5-uppercase-I", strings.Repeat("0", 40), strings.Repeat("I", 32)},
		{"SHA1-non-ASCII", "\u0130" + strings.Repeat("A", 39), strings.Repeat("0", 32)},
		{"MD5-non-ASCII", strings.Repeat("0", 40), "\u0130" + strings.Repeat("A", 31)},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := lbSetup(t)
			const modified, updated = int64(8888), int64(9999)
			user := "unsupported-original"
			checksums := ccMatching(f.next)
			checksums.SHA1Original, checksums.MD5Original = mdUnits(row.sha1Original), mdUnits(row.md5Original)
			ccSuccess(t, f, f.next, checksums, repository.LocalFileChecksumServer, modified, updated, mdUnits(user), true)
			ccRow(t, f, f.tx, f.next, modified, updated, &user, &row.sha1Original, &row.md5Original)
			if err := f.tx.Commit(); err != nil {
				t.Fatalf("unsupported Original write commit: %v", err)
			}
			f = loaReload(t, f)
			equalMDUnits(t, "unsupported raw SHA1 read", f.source.Reference.Metadata.SHA1OriginalUTF16(), mdUnits(row.sha1Original))
			equalMDUnits(t, "unsupported raw MD5 read", f.source.Reference.Metadata.MD5OriginalUTF16(), mdUnits(row.md5Original))
			snapshot, err := repository.LoadedFileSnapshot(f.source)
			if !errors.Is(err, repository.ErrUnsupportedLoadedOriginal) || !reflect.DeepEqual(snapshot.Fields(), repository.FileInfoSnapshotInput{}) {
				t.Fatalf("unsupported assembly = (%+v,%v), want zero/unsupported", snapshot.Fields(), err)
			}
			d := &ufDelegates{st: f.st}
			got, err := repository.UpdateLoadedFileBinary(f.ctx, repository.LoadedFileBinaryInput{
				Source: f.source, Published: f.winner, CreatedPolicy: repository.PreserveLoadedFileCreated,
			}, f.catalog, d.updateRef)
			if !errors.Is(err, repository.ErrUnsupportedLoadedOriginal) || got != (repository.FileDirtyUpdateResult{}) || d.update != 0 {
				t.Fatalf("unsupported update = (%+v,%v), Store calls=%d, want zero/unsupported/0", got, err, d.update)
			}
			ccRow(t, f, f.tx, f.next, modified, updated, &user, &row.sha1Original, &row.md5Original)
			ccRow(t, f, f.adm, f.next, modified, updated, &user, &row.sha1Original, &row.md5Original)
			if err := f.tx.Rollback(); err != nil {
				t.Fatalf("unsupported read transaction rollback: %v", err)
			}
			ccRow(t, f, f.adm, f.next, modified, updated, &user, &row.sha1Original, &row.md5Original)
		})
	}
}

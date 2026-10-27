// 显式 Original casing 的真实写入/回读闭环；不替换 Source、Store 或事务。
// Real write/read closure for explicit Original casing, with actual sources and stores.
package postgresit

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
)

const lcModified, lcUpdated, lcUser = int64(4444), int64(5555), "case-writer"

var lcProfiles = []repository.LoadedOriginalCase{repository.LoadedOriginalCaseNonTurkic, repository.LoadedOriginalCaseTurkic}

func lcSeed(t *testing.T, profile repository.LoadedOriginalCase, raw string) lbFixture {
	t.Helper()
	f := lbSetup(t)
	mimeCase := repository.FileMimeCaseRootASCII
	if profile == repository.LoadedOriginalCaseTurkic {
		mimeCase = repository.FileMimeCaseTurkicASCII
	}
	var err error
	f.catalog, err = repository.NewFileMimeCatalog([]repository.FileMimeDefinition{{Type: "application/octet-stream", Extensions: "bin"}}, mimeCase)
	if err != nil {
		t.Fatal(err)
	}
	checksums := ccMatching(f.next)
	checksums.SHA1Original, checksums.MD5Original = mdUnits(strings.Repeat(raw, 40)), mdUnits(strings.Repeat(raw, 32))
	ccSuccess(t, f, f.next, checksums, repository.LocalFileChecksumServer, lcModified, lcUpdated, mdUnits(lcUser), true)
	if err := f.tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return loaReload(t, f)
}

// 期望来自字面配置事实；不调用 normalizer 生成 SQL 或快照的答案。
// Expected units are literal configuration facts, never normalizer-derived SQL expectations.
func lcSnapshot(t *testing.T, f lbFixture, profile repository.LoadedOriginalCase, unit string) repository.FileInfoSnapshot {
	t.Helper()
	snapshot, err := repository.LoadedFileSnapshotWithOriginalCase(f.source, profile)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[repository.ChecksumKind]bool{}
	for _, entry := range snapshot.Fields().Checksums {
		if seen[entry.Kind] {
			t.Fatal("duplicate checksum")
		}
		seen[entry.Kind] = true
		want := "NO_ORIG"
		switch entry.Kind {
		case repository.ChecksumSHA1:
			want = strings.Repeat(unit, 40)
		case repository.ChecksumMD5:
			want = strings.Repeat(unit, 32)
		case repository.ChecksumSHA256:
		default:
			t.Fatal("unexpected checksum")
		}
		equalMDUnits(t, "explicit loaded Original", entry.Original, mdUnits(want))
	}
	if len(seen) != 3 {
		t.Fatal("missing checksum")
	}
	return snapshot
}

func lcBinary(t *testing.T, f lbFixture, profile repository.LoadedOriginalCase, published content.Metadata, changed bool) {
	t.Helper()
	d := &ufDelegates{st: f.st}
	got, err := repository.UpdateLoadedFileBinary(f.ctx, repository.LoadedFileBinaryInput{
		Source: f.source, Published: published, CreatedPolicy: repository.PreserveLoadedFileCreated, OriginalCase: profile,
	}, f.catalog, d.updateRef)
	count := int64(0)
	if changed {
		count = 1
	}
	if err != nil || got != (repository.FileDirtyUpdateResult{Changed: changed, Count: count}) || d.update != int(count) {
		t.Fatalf("explicit binary update = (%+v,%v), calls=%d", got, err, d.update)
	}
}

// U+0131 真正写入后再 Lookup，不能只在内存重用第一次组装结果。
// Read U+0131 again from SQL after commit, rather than reusing an assembled snapshot.
func TestLoadedOriginalCaseServerClosedReadUpdateRollback(t *testing.T) {
	for _, profile := range lcProfiles {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			f := lcSeed(t, profile, "I")
			rawSHA1, rawMD5, user := strings.Repeat("I", 40), strings.Repeat("I", 32), lcUser
			unit := "i"
			if profile == repository.LoadedOriginalCaseTurkic {
				unit = "\u0131"
			}
			wantSHA1, wantMD5 := strings.Repeat(unit, 40), strings.Repeat(unit, 32)
			lcSnapshot(t, f, profile, unit)
			lcBinary(t, f, profile, f.next, false)
			ccRow(t, f, f.tx, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
			ccRow(t, f, f.adm, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
			lcBinary(t, f, profile, f.winner, true)
			ccRow(t, f, f.tx, f.winner, lcModified, lcUpdated, &user, &wantSHA1, &wantMD5)
			ccRow(t, f, f.adm, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
			if err := f.tx.Commit(); err != nil {
				t.Fatal(err)
			}
			f = loaReload(t, f)
			lcSnapshot(t, f, profile, unit)
			lcBinary(t, f, profile, f.winner, false)
			ccRow(t, f, f.tx, f.winner, lcModified, lcUpdated, &user, &wantSHA1, &wantMD5)
			lcBinary(t, f, profile, f.next, true)
			ccRow(t, f, f.tx, f.next, lcModified, lcUpdated, &user, &wantSHA1, &wantMD5)
			ccRow(t, f, f.adm, f.winner, lcModified, lcUpdated, &user, &wantSHA1, &wantMD5)
			if err := f.tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			ccRow(t, f, f.adm, f.winner, lcModified, lcUpdated, &user, &wantSHA1, &wantMD5)
		})
	}
}

// 加载基线选择不能污染 CLIENT/SERVER 写侧规则或调用方的原始 Desired。
// Loaded casing must not leak into client/server write rules or raw Desired values.
func TestLoadedOriginalCaseAllWriteRoutes(t *testing.T) {
	for _, profile := range lcProfiles {
		for _, route := range []string{"client", "server-raw", "no-client", "raw-desired", "client-mismatch"} {
			t.Run(fmt.Sprintf("%d/%s", profile, route), func(t *testing.T) {
				f := lcSeed(t, profile, "I")
				rawSHA1, rawMD5, user := strings.Repeat("I", 40), strings.Repeat("I", 32), lcUser
				d := &ufDelegates{st: f.st}
				in := ncInput(f, f.winner, repository.LocalFileChecksumClient, lcModified, lcUpdated, mdUnits(user))
				in.OriginalCase = profile
				published, sha1, md5 := f.winner, &f.winner.SHA1, &f.winner.MD5
				var got repository.FileDirtyUpdateResult
				var err error
				switch route {
				case "raw-desired":
					unit := "i"
					if profile == repository.LoadedOriginalCaseTurkic {
						unit = "\u0131"
					}
					fields := lcSnapshot(t, f, profile, unit).Fields()
					for i := range fields.Checksums {
						if fields.Checksums[i].Kind == repository.ChecksumSHA1 {
							fields.Checksums[i].Original = mdUnits(rawSHA1)
						} else if fields.Checksums[i].Kind == repository.ChecksumMD5 {
							fields.Checksums[i].Original = mdUnits(rawMD5)
						}
					}
					desired, e := repository.NewFileInfoSnapshot(fields)
					if e != nil {
						t.Fatal(e)
					}
					got, err = repository.UpdateLoadedFileIfChanged(f.ctx, repository.LoadedFileUpdateInput{Source: f.source, Desired: desired, CreatedPolicy: repository.PreserveLoadedFileCreated, OriginalCase: profile}, f.catalog, d.updateRef)
					published, sha1, md5 = f.next, &rawSHA1, &rawMD5
				case "no-client":
					got, err = repository.UpdateLocalFileWithoutClientChecksums(f.ctx, in, f.catalog, d.updateRef)
					sha1, md5 = nil, nil
				default:
					checksums := ccMatching(f.winner)
					if route == "server-raw" || route == "client-mismatch" {
						checksums.SHA1Original, checksums.MD5Original = mdUnits(rawSHA1), mdUnits(rawMD5)
					}
					if route == "server-raw" {
						in.ChecksumPolicy, sha1, md5 = repository.LocalFileChecksumServer, &rawSHA1, &rawMD5
					}
					got, err = repository.UpdateLocalFileWithClientChecksums(f.ctx, in, checksums, f.catalog, d.updateRef)
				}
				if route == "client-mismatch" {
					if !errors.Is(err, repository.ErrClientChecksumMismatch) || got != (repository.FileDirtyUpdateResult{}) || d.update != 0 {
						t.Fatalf("mismatch = (%+v,%v), calls=%d", got, err, d.update)
					}
					published, sha1, md5 = f.next, &rawSHA1, &rawMD5
				} else if err != nil || got != (repository.FileDirtyUpdateResult{Changed: true, Count: 1}) || d.update != 1 {
					t.Fatalf("write route = (%+v,%v), calls=%d", got, err, d.update)
				}
				ccRow(t, f, f.tx, published, lcModified, lcUpdated, &user, sha1, md5)
				ccRow(t, f, f.adm, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
				if err := f.tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				ccRow(t, f, f.adm, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
			})
		}
	}
}

// 既有入口不扩大范围，未知配置错误在真实回读后仍不触及 Store。
// Legacy loading keeps its domain; unknown profiles after real reads never reach Store.
func TestLoadedOriginalCaseUnknownAndLegacyPreserveRow(t *testing.T) {
	for _, raw := range []string{"I", "\u0131"} {
		t.Run(raw, func(t *testing.T) {
			f := lcSeed(t, repository.LoadedOriginalCaseTurkic, raw)
			rawSHA1, rawMD5, user := strings.Repeat(raw, 40), strings.Repeat(raw, 32), lcUser
			snapshot, err := repository.LoadedFileSnapshot(f.source)
			if !errors.Is(err, repository.ErrUnsupportedLoadedOriginal) || !reflect.DeepEqual(snapshot.Fields(), repository.FileInfoSnapshotInput{}) {
				t.Fatalf("legacy loading = (%+v,%v)", snapshot.Fields(), err)
			}
			snapshot, err = repository.LoadedFileSnapshotWithOriginalCase(f.source, repository.LoadedOriginalCase(255))
			if !errors.Is(err, repository.ErrUnsupportedLoadedOriginalCase) || !reflect.DeepEqual(snapshot.Fields(), repository.FileInfoSnapshotInput{}) {
				t.Fatalf("unknown loading = (%+v,%v)", snapshot.Fields(), err)
			}
			d := &ufDelegates{st: f.st}
			got, err := repository.UpdateLoadedFileBinary(f.ctx, repository.LoadedFileBinaryInput{Source: f.source, Published: f.winner, CreatedPolicy: repository.PreserveLoadedFileCreated, OriginalCase: repository.LoadedOriginalCase(255)}, f.catalog, d.updateRef)
			if !errors.Is(err, repository.ErrUnsupportedLoadedOriginalCase) || got != (repository.FileDirtyUpdateResult{}) || d.update != 0 {
				t.Fatalf("unknown update = (%+v,%v), calls=%d", got, err, d.update)
			}
			ccRow(t, f, f.tx, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
			if err := f.tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			ccRow(t, f, f.adm, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
		})
	}
}

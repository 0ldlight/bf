// Latin1 Original 的真实写入与再次回读；复用既有实际 binary、Lookup、Store 和事务。
// Real Latin1 write/read seams using actual binary, lookup, store and caller transactions.
package postgresit

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/0ldlight/bf/internal/repository"
)

// 期望为字面 Unicode 对，不用加载器生成答案；同内容跳过仍保留 SQL 大写原值。
// Literal Unicode pairs provide expectations; same-content skips preserve raw SQL capitals.
func TestLoadedOriginalLatin1ServerClosedReadUpdateRollback(t *testing.T) {
	for _, row := range []struct {
		name, raw, lower string
		profile          repository.LoadedOriginalCase
	}{
		{"NonTurkic-O-grave", "\u00D2", "\u00F2", repository.LoadedOriginalCaseNonTurkic},
		{"NonTurkic-O-diaeresis", "\u00D6", "\u00F6", repository.LoadedOriginalCaseNonTurkic},
		{"Turkic-dotted-I", "\u0130", "i", repository.LoadedOriginalCaseTurkic},
		{"Turkic-I-grave", "\u00CC", "\u00EC", repository.LoadedOriginalCaseTurkic},
	} {
		t.Run(row.name, func(t *testing.T) {
			profile, unit := row.profile, row.lower
			f := lcSeed(t, profile, row.raw)
			rawSHA1, rawMD5, user := strings.Repeat(row.raw, 40), strings.Repeat(row.raw, 32), lcUser
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

// 新 Latin1 加载值不改变 CLIENT/SERVER 请求规则或原始 Desired。
// Expanded loading must not rewrite validating request inputs or raw Desired.
func TestLoadedOriginalLatin1AllWriteRoutes(t *testing.T) {
	for _, profile := range lcProfiles {
		for _, route := range []string{"client", "server-raw", "no-client", "raw-desired", "client-mismatch"} {
			t.Run(fmt.Sprintf("%d/%s", profile, route), func(t *testing.T) {
				raw, lower := "\u00D2", "\u00F2"
				if profile == repository.LoadedOriginalCaseTurkic {
					raw, lower = "\u0130", "i"
				}
				f := lcSeed(t, profile, raw)
				rawSHA1, rawMD5, user := strings.Repeat(raw, 40), strings.Repeat(raw, 32), lcUser
				d := &ufDelegates{st: f.st}
				in := ncInput(f, f.winner, repository.LocalFileChecksumClient, lcModified, lcUpdated, mdUnits(user))
				in.OriginalCase = profile
				published, sha1, md5 := f.winner, &f.winner.SHA1, &f.winner.MD5
				var got repository.FileDirtyUpdateResult
				var err error
				switch route {
				case "raw-desired":
					fields := lcSnapshot(t, f, profile, lower).Fields()
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

// NonTurkic 特例、旧画像和未知选择必须拒绝，保留真实 Source 及 SQL 行。
// NonTurkic exclusions, legacy mode and unknown modes preserve the actual source and row.
func TestLoadedOriginalLatin1UnsupportedPreservesRow(t *testing.T) {
	for _, row := range []struct {
		name, raw string
		profile   repository.LoadedOriginalCase
		want      error
	}{
		{"NonTurkic-grave", "\u00CC", repository.LoadedOriginalCaseNonTurkic, repository.ErrUnsupportedLoadedOriginal},
		{"NonTurkic-acute", "\u00CD", repository.LoadedOriginalCaseNonTurkic, repository.ErrUnsupportedLoadedOriginal},
		{"NonTurkic-dotted", "\u0130", repository.LoadedOriginalCaseNonTurkic, repository.ErrUnsupportedLoadedOriginal},
		{"Invariant-Latin1", "\u00D2", repository.LoadedOriginalCaseInvariant, repository.ErrUnsupportedLoadedOriginal},
		{"Unknown-mode", "\u00D2", repository.LoadedOriginalCase(255), repository.ErrUnsupportedLoadedOriginalCase},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := lcSeed(t, repository.LoadedOriginalCaseNonTurkic, row.raw)
			rawSHA1, rawMD5, user := strings.Repeat(row.raw, 40), strings.Repeat(row.raw, 32), lcUser
			snapshot, err := repository.LoadedFileSnapshotWithOriginalCase(f.source, row.profile)
			if !errors.Is(err, row.want) || !reflect.DeepEqual(snapshot.Fields(), repository.FileInfoSnapshotInput{}) {
				t.Fatalf("unsupported loading = (%+v,%v), want zero/%v", snapshot.Fields(), err, row.want)
			}
			d := &ufDelegates{st: f.st}
			got, err := repository.UpdateLoadedFileBinary(f.ctx, repository.LoadedFileBinaryInput{
				Source: f.source, Published: f.winner, CreatedPolicy: repository.PreserveLoadedFileCreated, OriginalCase: row.profile,
			}, f.catalog, d.updateRef)
			if !errors.Is(err, row.want) || got != (repository.FileDirtyUpdateResult{}) || d.update != 0 {
				t.Fatalf("unsupported update = (%+v,%v), calls=%d, want zero/%v/0", got, err, d.update, row.want)
			}
			equalMDUnits(t, "unchanged Source SHA1 Original", f.source.Reference.Metadata.SHA1OriginalUTF16(), mdUnits(rawSHA1))
			equalMDUnits(t, "unchanged Source MD5 Original", f.source.Reference.Metadata.MD5OriginalUTF16(), mdUnits(rawMD5))
			ccRow(t, f, f.tx, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
			if err := f.tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			ccRow(t, f, f.adm, f.next, lcModified, lcUpdated, &user, &rawSHA1, &rawMD5)
		})
	}
}

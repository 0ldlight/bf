// BIN-177 读取出口的真实 PostgreSQL 契约：三个顶层用例覆盖三个 Actual 的
// SQL NULL/空串/非空画像（含非规范存量字符串）、all-NULL 与显式零元数据、
// updated/size NULL 投影、缺行/非文件/存量 repo '.' 拒绝及与旧 Lookup 的
// Reference 兼容；错误零结果、原因链与固定文案逐项断言，绝无 Skip。
// Real-PostgreSQL contract for the BIN-177 read source: three top-level
// cases cover the three Actuals across SQL NULL/empty/nonempty profiles
// (noncanonical legacy strings included), all-NULL versus explicit-zero
// metadata, updated/size NULL projections, missing-row/non-file/stored
// repo-'.' rejections and Reference compatibility with the old Lookup; zero
// results, cause chains and fixed texts are asserted item by item with no
// Skip ever used as evidence.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// srcSeedLegacyRow 在真实约束全开的前提下用原始 SQL 播种一行 v1 语义的存量
// nodes 行：node_type=1、无元数据列（七列全 NULL），三个 Actual 传 nil 为
// SQL NULL；sha1 非空/空串取值必须由调用方先播好合法 binaries 外键目标。
// srcSeedLegacyRow seeds one legacy v1-semantics nodes row through raw SQL
// with every real constraint left enabled: node_type=1, no metadata columns
// (all seven NULL) and the three Actuals mapped from nil to SQL NULL; any
// nonempty or empty sha1 value requires the caller to seed the binaries FK
// target first.
func srcSeedLegacyRow(t *testing.T, adm *sql.DB, schema string, id int64, repo, parent, name string, sha1, md5, sha256 any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := adm.ExecContext(ctx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth, bin_length, "+
			"sha1_actual, md5_actual, sha256) VALUES ($1, 1, $2, $3, $4, 2, 21, $5, $6, $7)", schema),
		id, repo, parent, name, sha1, md5, sha256); err != nil {
		t.Fatalf("seed legacy row %d: %v", id, err)
	}
}

// TestSnapshotSource 是 RED 主靶：同一次读取里三个 Actual 各自覆盖 SQL
// NULL、空串与非空（含非规范存量字符串），逐位断言 Valid 与字符串原值，
// 并与旧 Lookup 的全部已表示字段对照；空串行是区分“空性位来自同一 Scan
// 的 sql.NullString.Valid”与“从字符串非空推断”的判据。
// TestSnapshotSource is the primary RED target: one read per row covering
// each Actual as SQL NULL, empty string and nonempty (noncanonical legacy
// strings included), asserting every Valid bit and verbatim string, plus
// full represented-field comparison against the old Lookup; the
// empty-string rows discriminate a Valid bit taken from the same Scan's
// sql.NullString.Valid from one inferred from string non-emptiness.
func TestSnapshotSource(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	binA := strings.Repeat("a", 40)
	seedNodeBinary(t, adm, schema, binA)
	// 空串 sha1 的合法外键目标：原始行允许，不假设任何规范 API。
	// A legitimate FK target for an empty-string sha1: raw rows allow it and
	// no canonical API is assumed.
	seedNodeBinary(t, adm, schema, "")
	md5Legacy := "ZZ" + strings.Repeat("9", 30)
	sha256Legacy := "ZZ" + strings.Repeat("9", 62)
	rows := []struct {
		id                               int64
		name                             string
		sha1, md5, sha256                any
		sha1Valid, md5Valid, sha256Valid bool
		wantSHA1, wantMD5, wantSHA256    string
	}{
		{701, "all-null.bin", nil, nil, nil, false, false, false, "", "", ""},
		{702, "all-empty.bin", "", "", "", true, true, true, "", "", ""},
		{703, "all-nonempty.bin", binA, md5Legacy, sha256Legacy, true, true, true, binA, md5Legacy, sha256Legacy},
		{704, "mixed.bin", binA, nil, "", true, false, true, binA, "", ""},
		{705, "null-sha1.bin", nil, md5Legacy, sha256Legacy, false, true, true, "", md5Legacy, sha256Legacy},
		{706, "null-sha256.bin", binA, md5Legacy, nil, true, true, false, binA, md5Legacy, ""},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			srcSeedLegacyRow(t, adm, schema, r.id, "srcR", "d", r.name, r.sha1, r.md5, r.sha256)
			_, st := nodeTx(t, db)
			path := fileRefOf(t, 0, "srcR", "d", r.name, nodeMeta(binA)).Path
			src, err := st.LookupSnapshotSource(ctx, path)
			if err != nil {
				t.Fatalf("LookupSnapshotSource: %v", err)
			}
			if src.SHA1ActualValid != r.sha1Valid || src.MD5ActualValid != r.md5Valid || src.SHA256ActualValid != r.sha256Valid {
				t.Fatalf("validity bits = (%v %v %v), want (%v %v %v)",
					src.SHA1ActualValid, src.MD5ActualValid, src.SHA256ActualValid,
					r.sha1Valid, r.md5Valid, r.sha256Valid)
			}
			c := src.Reference.Content
			if c.SHA1 != r.wantSHA1 || c.MD5 != r.wantMD5 || c.SHA256 != r.wantSHA256 {
				t.Fatalf("content actuals = (%q %q %q), want (%q %q %q)",
					c.SHA1, c.MD5, c.SHA256, r.wantSHA1, r.wantMD5, r.wantSHA256)
			}
			if c.Size != 21 {
				t.Fatalf("content size = %d, want 21", c.Size)
			}
			if src.Reference.ID != r.id {
				t.Fatalf("reference id = %d, want %d", src.Reference.ID, r.id)
			}
			if src.Reference.Metadata != nil {
				t.Fatalf("reference metadata = %+v, want nil (all-NULL columns)", src.Reference.Metadata)
			}
			if p := src.Reference.Path; p.Repo() != "srcR" || p.Parent() != "d" || p.Name() != r.name || !p.IsFile() {
				t.Fatalf("reference path = %q/%q/%q (file %v), want srcR/d/%s", p.Repo(), p.Parent(), p.Name(), p.IsFile(), r.name)
			}
			legacy, lerr := st.Lookup(ctx, path)
			if lerr != nil {
				t.Fatalf("old Lookup: %v", lerr)
			}
			if legacy != src.Reference {
				t.Fatalf("old Lookup reference %+v disagrees with source reference %+v", legacy, src.Reference)
			}
		})
	}
}

// TestSnapshotSourceMetadataShapes 区分七列全 NULL（nil 快照）与显式零快照，
// 覆盖 updated NULL→0、bin_length NULL→0 投影、actor 缩写与 original 的
// nil/空差别，并逐字段对照旧 Lookup。
// TestSnapshotSourceMetadataShapes distinguishes all-seven-NULL metadata
// (nil snapshot) from an explicit zero snapshot, covers the updated
// NULL→0 and bin_length NULL→0 projections, actor abbreviation and the
// nil-versus-empty original distinction, comparing the old Lookup field by
// field.
func TestSnapshotSourceMetadataShapes(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	binB := strings.Repeat("b", 40)
	seedNodeBinary(t, adm, schema, binB)

	srcSeedLegacyRow(t, adm, schema, 710, "srcM", "d", "unknown-meta.bin", binB, nil, nil)
	tx, st := nodeTx(t, db)
	zero := repository.NewFileMetadata(repository.FileMetadataInput{})
	zeroRef := fileRefOf(t, 711, "srcM", "d", "zero-meta.bin", nodeMeta(binB))
	zeroRef.Metadata = &zero
	if n, err := st.Create(ctx, zeroRef); err != nil || n != 1 {
		t.Fatalf("Create(zero snapshot) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// 存量原始行：created/modified 已知、updated NULL、actor 混合、original
	// nil 与空并存、bin_length NULL——全部在真实形状约束下插入。
	// A legacy raw row: created/modified known, updated NULL, mixed actors,
	// nil and empty originals side by side, bin_length NULL — inserted with
	// the real shape constraint live.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer seedCancel()
	if _, err := adm.ExecContext(seedCtx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth, bin_length, "+
			"sha1_actual, created, modified, updated, created_by, modified_by, sha1_original, md5_original) "+
			"VALUES (712, 1, 'srcM', 'd', 'legacy-meta.bin', 2, NULL, $1, 111, 222, NULL, "+
			"'legacy-actor', NULL, NULL, '')", schema), binB); err != nil {
		t.Fatalf("seed legacy metadata row: %v", err)
	}

	_, look := nodeTx(t, db)
	// 710：未知历史 → nil；旧 Lookup 同为 nil。
	// 710: unknown history → nil; the old Lookup reports nil too.
	src0, err := look.LookupSnapshotSource(ctx, fileRefOf(t, 0, "srcM", "d", "unknown-meta.bin", nodeMeta(binB)).Path)
	if err != nil {
		t.Fatalf("LookupSnapshotSource(unknown meta): %v", err)
	}
	if src0.Reference.Metadata != nil {
		t.Fatalf("all-NULL metadata = %+v, want nil", src0.Reference.Metadata)
	}
	if legacy0, lerr := look.Lookup(ctx, fileRefOf(t, 0, "srcM", "d", "unknown-meta.bin", nodeMeta(binB)).Path); lerr != nil || legacy0 != src0.Reference {
		t.Fatalf("old Lookup(unknown meta) = (%+v, %v), want same reference", legacy0, lerr)
	}

	// 711：显式零快照非 nil 且逐字段为零。
	// 711: an explicit zero snapshot stays nonnil with zero fields.
	src1, err := look.LookupSnapshotSource(ctx, fileRefOf(t, 0, "srcM", "d", "zero-meta.bin", nodeMeta(binB)).Path)
	if err != nil {
		t.Fatalf("LookupSnapshotSource(zero meta): %v", err)
	}
	if src1.Reference.ID != 711 || src1.Reference.Content != nodeMeta(binB) {
		t.Fatal("zero metadata source ID/content differs from independent fixture")
	}
	if p := src1.Reference.Path; p.Repo() != "srcM" || p.Parent() != "d" || p.Name() != "zero-meta.bin" || !p.IsFile() {
		t.Fatal("zero metadata source path differs from independent fixture")
	}
	if !src1.SHA1ActualValid || !src1.MD5ActualValid || !src1.SHA256ActualValid {
		t.Fatal("zero metadata source must retain all three non-NULL Actuals")
	}
	m1 := src1.Reference.Metadata
	if m1 == nil {
		t.Fatal("explicit zero snapshot must stay nonnil")
	}
	if m1.CreatedMillis() != 0 || m1.ModifiedMillis() != 0 || m1.UpdatedMillis() != 0 {
		t.Fatalf("zero snapshot times = (%d %d %d), want zeros", m1.CreatedMillis(), m1.ModifiedMillis(), m1.UpdatedMillis())
	}
	if !m1.CreatedBy().IsNull() || !m1.ModifiedBy().IsNull() {
		t.Fatalf("zero snapshot actors = (%v %v), want null actors", m1.CreatedBy().IsNull(), m1.ModifiedBy().IsNull())
	}
	if m1.SHA1OriginalUTF16() != nil || m1.MD5OriginalUTF16() != nil {
		t.Fatalf("zero snapshot originals = (%v %v), want nil", m1.SHA1OriginalUTF16(), m1.MD5OriginalUTF16())
	}

	legacy1, legacy1Err := look.Lookup(ctx, src1.Reference.Path)
	if legacy1Err != nil {
		t.Fatalf("old Lookup(zero metadata): %v", legacy1Err)
	}
	if legacy1.ID != src1.Reference.ID || legacy1.Path != src1.Reference.Path || legacy1.Content != src1.Reference.Content {
		t.Fatal("zero metadata Reference projection differs between readers")
	}
	if legacy1.Metadata == nil {
		t.Fatal("old Lookup must retain explicit zero metadata")
	}
	equalSnapshot(t, legacy1.Metadata, zero)

	// 712：updated NULL→0、bin_length NULL→0、actor 经 NewActor、original
	// nil≠空；每个期望值独立给出，不经被测代码推导。
	// 712: updated NULL→0, bin_length NULL→0, actors through NewActor,
	// nil≠empty originals; every expectation is stated independently and
	// never derived through the code under test.
	src2, err := look.LookupSnapshotSource(ctx, fileRefOf(t, 0, "srcM", "d", "legacy-meta.bin", nodeMeta(binB)).Path)
	if err != nil {
		t.Fatalf("LookupSnapshotSource(legacy meta): %v", err)
	}
	m2 := src2.Reference.Metadata
	if m2 == nil {
		t.Fatal("known created/modified must yield a nonnil snapshot")
	}
	if m2.CreatedMillis() != 111 || m2.ModifiedMillis() != 222 || m2.UpdatedMillis() != 0 {
		t.Fatalf("legacy snapshot times = (%d %d %d), want (111 222 0)", m2.CreatedMillis(), m2.ModifiedMillis(), m2.UpdatedMillis())
	}
	if m2.CreatedBy().IsNull() || !slices.Equal(m2.CreatedBy().UTF16(), mdUnits("legacy-actor")) {
		t.Fatalf("legacy created-by actor = %v, want legacy-actor", m2.CreatedBy().UTF16())
	}
	if !m2.ModifiedBy().IsNull() {
		t.Fatalf("legacy modified-by must be the null actor, got %v", m2.ModifiedBy().UTF16())
	}
	if m2.SHA1OriginalUTF16() != nil {
		t.Fatalf("legacy sha1 original = %v, want nil", m2.SHA1OriginalUTF16())
	}
	if got := m2.MD5OriginalUTF16(); got == nil || len(got) != 0 {
		t.Fatalf("legacy md5 original = %v, want nonnil empty", got)
	}
	if src2.Reference.Content.Size != 0 {
		t.Fatalf("NULL bin_length projected to %d, want 0", src2.Reference.Content.Size)
	}
	if !src2.SHA1ActualValid || src2.MD5ActualValid || src2.SHA256ActualValid {
		t.Fatalf("legacy metadata row actual bits = (%v %v %v), want (true false false)",
			src2.SHA1ActualValid, src2.MD5ActualValid, src2.SHA256ActualValid)
	}
	legacy2, lerr := look.Lookup(ctx, fileRefOf(t, 0, "srcM", "d", "legacy-meta.bin", nodeMeta(binB)).Path)
	if lerr != nil {
		t.Fatalf("old Lookup(legacy meta): %v", lerr)
	}
	if legacy2.ID != src2.Reference.ID || legacy2.Path != src2.Reference.Path ||
		legacy2.Content != src2.Reference.Content || legacy2.Metadata == nil {
		t.Fatalf("old Lookup disagreement: %+v vs %+v", legacy2, src2.Reference)
	}
	lm := legacy2.Metadata
	if lm.CreatedMillis() != 111 || lm.ModifiedMillis() != 222 || lm.UpdatedMillis() != 0 ||
		!slices.Equal(lm.CreatedBy().UTF16(), mdUnits("legacy-actor")) || !lm.ModifiedBy().IsNull() ||
		lm.SHA1OriginalUTF16() != nil || lm.MD5OriginalUTF16() == nil || len(lm.MD5OriginalUTF16()) != 0 {
		t.Fatalf("old Lookup legacy snapshot = %+v, want the same represented fields", lm)
	}
}

// TestSnapshotSourceErrorProfiles 核对缺行、非文件与存量 repo '.' 拒绝：
// 两个出口同因、同固定文案、零结果，ErrNoRows/ErrNotFile/ErrInvalidReference
// 原因链保留且不扩张；全部走真实可达夹具，无约束删改。
// TestSnapshotSourceErrorProfiles checks missing-row, non-file and the
// stored repo-'.' rejection: both exits share the cause, the fixed text and
// the zero result, the ErrNoRows/ErrNotFile/ErrInvalidReference chains stay
// intact and unexpanded, and everything rides real reachable fixtures with
// no constraint dropped or edited.
func TestSnapshotSourceErrorProfiles(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	binE := strings.Repeat("e", 40)
	seedNodeBinary(t, adm, schema, binE)
	_, st := nodeTx(t, db)

	missing := fileRefOf(t, 0, "srcE", "d", "missing.bin", nodeMeta(binE)).Path
	src, err := st.LookupSnapshotSource(ctx, missing)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing-row error = %v, want sql.ErrNoRows chain", err)
	}
	if err.Error() != "postgres nodes: lookup file reference" {
		t.Fatalf("missing-row Error() = %q, want fixed lookup stage text", err.Error())
	}
	if src != (repository.FileSnapshotSource{}) {
		t.Fatalf("missing-row source = %+v, want zero source", src)
	}
	legacy, lerr := st.Lookup(ctx, missing)
	if !errors.Is(lerr, sql.ErrNoRows) || legacy != (repository.FileReference{}) {
		t.Fatalf("old Lookup(missing) = (%+v, %v), want zero reference + ErrNoRows", legacy, lerr)
	}
	if lerr.Error() != err.Error() {
		t.Fatalf("missing-row texts differ: %q vs %q", lerr.Error(), err.Error())
	}

	seedCtx, seedCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer seedCancel()
	if _, ferr := adm.ExecContext(seedCtx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth) "+
			"VALUES (721, 0, 'srcE', 'd', 'afolder', 2)", schema)); ferr != nil {
		t.Fatalf("seed non-file row: %v", ferr)
	}
	folder := fileRefOf(t, 0, "srcE", "d", "afolder", nodeMeta(binE)).Path
	srcNF, errNF := st.LookupSnapshotSource(ctx, folder)
	if !errors.Is(errNF, refpg.ErrNotFile) {
		t.Fatalf("non-file error = %v, want ErrNotFile chain", errNF)
	}
	if errNF.Error() != "postgres nodes: lookup file reference" {
		t.Fatalf("non-file Error() = %q, want fixed lookup stage text", errNF.Error())
	}
	if srcNF != (repository.FileSnapshotSource{}) {
		t.Fatalf("non-file source = %+v, want zero source", srcNF)
	}
	if legacyNF, lerrNF := st.Lookup(ctx, folder); !errors.Is(lerrNF, refpg.ErrNotFile) ||
		legacyNF != (repository.FileReference{}) || lerrNF.Error() != errNF.Error() {
		t.Fatalf("old Lookup(non-file) = (%+v, %v), want matching rejection", legacyNF, lerrNF)
	}

	if _, derr := adm.ExecContext(seedCtx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth, bin_length, sha1_actual) "+
			"VALUES (722, 1, '.', 'd', 'dot-repo.bin', 2, 21, $1)", schema), binE); derr != nil {
		t.Fatalf("seed stored dot-repo row: %v", derr)
	}
	dotRepo := fileRefOf(t, 0, ".", "d", "dot-repo.bin", nodeMeta(binE)).Path
	srcDot, errDot := st.LookupSnapshotSource(ctx, dotRepo)
	if !errors.Is(errDot, refpg.ErrInvalidReference) || errors.Is(errDot, sql.ErrNoRows) {
		t.Fatalf("stored dot-repo error = %v, want ErrInvalidReference (not ErrNoRows)", errDot)
	}
	if errDot.Error() != "postgres nodes: lookup file reference" {
		t.Fatalf("stored dot-repo Error() = %q, want fixed lookup stage text", errDot.Error())
	}
	if srcDot != (repository.FileSnapshotSource{}) {
		t.Fatalf("stored dot-repo source = %+v, want zero source", srcDot)
	}
	if legacyDot, lerrDot := st.Lookup(ctx, dotRepo); !errors.Is(lerrDot, refpg.ErrInvalidReference) ||
		legacyDot != (repository.FileReference{}) || lerrDot.Error() != errDot.Error() {
		t.Fatalf("old Lookup(stored dot repo) = (%+v, %v), want matching rejection", legacyDot, lerrDot)
	}
}

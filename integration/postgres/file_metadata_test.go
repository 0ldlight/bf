// 逐节点文件元数据七列投影的真实 PostgreSQL 场景（BIN-142）：字段独立性、
// nil/空区分、显式零与缺失、int64 极值、孤立代理编码与字面 U+FFFD、actor
// 读取侧缩写、VARCHAR(1024) 原生长度/尾部空格/NUL、nil 更新保持与非 nil
// 整体替换、失败回滚保留、畸形行形状防御。
// Real-PostgreSQL scenarios for the seven per-node file-metadata columns
// (BIN-142): field independence, nil-versus-empty, explicit zero versus
// absence, int64 extremes, lone-surrogate encoding versus a literal U+FFFD,
// read-side actor abbreviation, native VARCHAR(1024) length/trailing-space/
// NUL behavior, nil-update preservation versus full nonnil replacement,
// failed-update rollback preservation and defensive malformed-row shapes.
package postgresit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// mdUnits 构造 UTF-16 码元（星面字符成代理对），与纯值语义一致。
// mdUnits builds UTF-16 units (astral characters become surrogate pairs),
// matching the pure-value semantics.
func mdUnits(s string) []uint16 {
	units := make([]uint16, 0, len(s))
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			units = append(units, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
			continue
		}
		units = append(units, uint16(r))
	}
	return units
}

func equalMDUnits(t *testing.T, label string, got, want []uint16) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %d units %04x, want %d units %04x", label, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s unit %d = %04x, want %04x (got %04x)", label, i, got[i], want[i], got)
		}
	}
}

func equalMDActor(t *testing.T, label string, got repository.Actor, want repository.Actor) {
	t.Helper()
	gu, wu := got.UTF16(), want.UTF16()
	if (gu == nil) != (wu == nil) || len(gu) != len(wu) {
		t.Fatalf("%s null/length mismatch: got %v, want %v", label, gu, wu)
	}
	for i := range wu {
		if gu[i] != wu[i] {
			t.Fatalf("%s unit %d = %04x, want %04x", label, i, gu[i], wu[i])
		}
	}
}

// equalSnapshot 逐字段核对快照（七字段独立比较）。
// equalSnapshot compares a snapshot field by field (seven independent
// fields).
func equalSnapshot(t *testing.T, got *repository.FileMetadata, want repository.FileMetadata) {
	t.Helper()
	if got == nil {
		t.Fatal("Lookup returned a nil metadata snapshot, want the stored snapshot")
	}
	if got.CreatedMillis() != want.CreatedMillis() {
		t.Errorf("CreatedMillis = %d, want %d", got.CreatedMillis(), want.CreatedMillis())
	}
	if got.ModifiedMillis() != want.ModifiedMillis() {
		t.Errorf("ModifiedMillis = %d, want %d", got.ModifiedMillis(), want.ModifiedMillis())
	}
	if got.UpdatedMillis() != want.UpdatedMillis() {
		t.Errorf("UpdatedMillis = %d, want %d", got.UpdatedMillis(), want.UpdatedMillis())
	}
	equalMDActor(t, "CreatedBy", got.CreatedBy(), want.CreatedBy())
	equalMDActor(t, "ModifiedBy", got.ModifiedBy(), want.ModifiedBy())
	s1, w1 := got.SHA1OriginalUTF16(), want.SHA1OriginalUTF16()
	if (s1 == nil) != (w1 == nil) {
		t.Errorf("SHA1Original nullness = %v, want %v", s1 == nil, w1 == nil)
	} else {
		equalMDUnits(t, "SHA1Original", s1, w1)
	}
	m1, wm := got.MD5OriginalUTF16(), want.MD5OriginalUTF16()
	if (m1 == nil) != (wm == nil) {
		t.Errorf("MD5Original nullness = %v, want %v", m1 == nil, wm == nil)
	} else {
		equalMDUnits(t, "MD5Original", m1, wm)
	}
}

// metadataRowOf 独立直读七列。
// metadataRowOf reads the seven columns through independent SQL.
func metadataRowOf(t *testing.T, adm *sql.DB, schema string, nodeID int64) (
	created, modified, updated sql.NullInt64, createdBy, modifiedBy, sha1Orig, md5Orig sql.NullString) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := adm.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT created, modified, updated, created_by, modified_by, sha1_original, md5_original FROM %s.nodes WHERE node_id = $1", schema),
		nodeID).Scan(&created, &modified, &updated, &createdBy, &modifiedBy, &sha1Orig, &md5Orig)
	if err != nil {
		t.Fatalf("independent metadata row read: %v", err)
	}
	return
}

func TestFileMetadataIndependentFieldsRoundTrip(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1A := strings.Repeat("1", 40)
	seedNodeBinary(t, adm, schema, sha1A)
	meta := nodeMeta(sha1A)

	// 两个方向的极端与空/非空组合：七字段彼此独立，互不推导。
	// Extremes and empty/nonempty mixes in both directions: the seven
	// fields stay independent with no cross-derivation.
	snapA := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  -9223372036854775808,
		ModifiedMillis: 9223372036854775807,
		UpdatedMillis:  0,
		CreatedBy:      repository.NewActor(mdUnits("creator-one")),
		ModifiedBy:     repository.NewActor(nil),
		SHA1Original:   mdUnits("NO_ORIG"),
		MD5Original:    []uint16{},
	})
	snapB := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis:  1 << 62,
		ModifiedMillis: -1,
		UpdatedMillis:  42,
		CreatedBy:      repository.NewActor(nil),
		ModifiedBy:     repository.NewActor(mdUnits("m")),
		SHA1Original:   nil,
		MD5Original:    mdUnits("x y"),
	})

	tx, st := nodeTx(t, db)
	refA := fileRefOf(t, 701, "repoM", "d", "a.bin", meta)
	refA.Metadata = &snapA
	if n, err := st.Create(ctx, refA); err != nil || n != 1 {
		t.Fatalf("Create A = (%d, %v), want (1, nil)", n, err)
	}
	refB := fileRefOf(t, 702, "repoM", "d", "b.bin", meta)
	refB.Metadata = &snapB
	if n, err := st.Create(ctx, refB); err != nil || n != 1 {
		t.Fatalf("Create B = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	lookTx, lookSt := nodeTx(t, db)
	gotA, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoM", "d", "a.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup A: %v", err)
	}
	equalSnapshot(t, gotA.Metadata, snapA)
	gotB, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoM", "d", "b.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup B: %v", err)
	}
	equalSnapshot(t, gotB.Metadata, snapB)
	// 两次 Lookup 的快照独立分配。
	// The two lookup snapshots are independently allocated.
	if gotA.Metadata == gotB.Metadata {
		t.Fatal("distinct rows must not share a snapshot pointer")
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// 独立直读：A 行七列按写入逐列核实（null actor→NULL，空 original→''）。
	// Independent read: all seven columns of row A verbatim (a null actor is
	// NULL and an empty original is '').
	created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig :=
		metadataRowOf(t, adm, schema, 701)
	if !created.Valid || created.Int64 != -9223372036854775808 ||
		!modified.Valid || modified.Int64 != 9223372036854775807 ||
		!updated.Valid || updated.Int64 != 0 {
		t.Fatalf("stored times = %v/%v/%v, want int64 extremes and zero", created, modified, updated)
	}
	if !createdBy.Valid || createdBy.String != "creator-one" || modifiedBy.Valid {
		t.Fatalf("stored actors = %v/%v, want creator-one and NULL", createdBy, modifiedBy)
	}
	if !sha1Orig.Valid || sha1Orig.String != "NO_ORIG" || !md5Orig.Valid || md5Orig.String != "" {
		t.Fatalf("stored originals = %v/%v, want NO_ORIG and empty string", sha1Orig, md5Orig)
	}
}

func TestFileMetadataAbsentVersusExplicitZero(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("2", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// nil Metadata → 七列全 NULL（v1 历史兼容）；显式零值快照 → 三时间为 0
	// 的已知快照，两者不同。
	// A nil Metadata writes seven NULL columns (v1-history compatible); an
	// explicit zero snapshot is a KNOWN snapshot with zero times — the two
	// differ.
	zero := repository.NewFileMetadata(repository.FileMetadataInput{})
	tx, st := nodeTx(t, db)
	if n, err := st.Create(ctx, fileRefOf(t, 711, "repoZ", "", "absent.bin", meta)); err != nil || n != 1 {
		t.Fatalf("Create(absent) = (%d, %v), want (1, nil)", n, err)
	}
	refZero := fileRefOf(t, 712, "repoZ", "", "zero.bin", meta)
	refZero.Metadata = &zero
	if n, err := st.Create(ctx, refZero); err != nil || n != 1 {
		t.Fatalf("Create(zero) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	lookTx, lookSt := nodeTx(t, db)
	absent, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoZ", "", "absent.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup(absent): %v", err)
	}
	if absent.Metadata != nil {
		t.Fatalf("absent metadata Lookup = %+v, want nil snapshot", absent.Metadata)
	}
	zeroGot, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoZ", "", "zero.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup(zero): %v", err)
	}
	equalSnapshot(t, zeroGot.Metadata, zero)
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig :=
		metadataRowOf(t, adm, schema, 711)
	if created.Valid || modified.Valid || updated.Valid || createdBy.Valid ||
		modifiedBy.Valid || sha1Orig.Valid || md5Orig.Valid {
		t.Fatalf("absent metadata stored nonnull columns: %v/%v/%v/%v/%v/%v/%v",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
	created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig =
		metadataRowOf(t, adm, schema, 712)
	if !created.Valid || created.Int64 != 0 || !modified.Valid || modified.Int64 != 0 ||
		!updated.Valid || updated.Int64 != 0 || createdBy.Valid || modifiedBy.Valid ||
		sha1Orig.Valid || md5Orig.Valid {
		t.Fatalf("explicit zero stored = %v/%v/%v/%v/%v/%v/%v, want 0/0/0 with four NULLs",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
}

func TestFileMetadataLoneSurrogateEncodingVersusLiteralReplacement(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("3", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// 孤立代理在所选参数编码边界成为 ASCII '?'；有效代理对与字面 U+FFFD、
	// 字面 '?' 保持自身。
	// Lone surrogates become ASCII '?' at the selected parameter-encoding
	// boundary; valid pairs, a literal U+FFFD and a literal '?' stay
	// themselves.
	snap := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 1, ModifiedMillis: 2, UpdatedMillis: 3,
		SHA1Original: []uint16{0xD83D, 0xDE00, 0xD83D}, // 有效对 + 孤立高位
		MD5Original:  []uint16{0xDE00, 0xD83D},         // 反序对：两个孤立
	})
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, 721, "repoS", "", "sur.bin", meta)
	ref.Metadata = &snap
	if n, err := st.Create(ctx, ref); err != nil || n != 1 {
		t.Fatalf("Create = (%d, %v), want (1, nil)", n, err)
	}
	literal := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 4, ModifiedMillis: 5, UpdatedMillis: 6,
		SHA1Original: []uint16{0xFFFD}, // 字面 U+FFFD 不参与替换规则
		MD5Original:  []uint16{'?'},
	})
	refLit := fileRefOf(t, 722, "repoS", "", "lit.bin", meta)
	refLit.Metadata = &literal
	if n, err := st.Create(ctx, refLit); err != nil || n != 1 {
		t.Fatalf("Create(literal) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, _, _, _, _, sha1Orig, _ := metadataRowOf(t, adm, schema, 721)
	if sha1Orig.String != "\U0001F600?" {
		t.Fatalf("stored sha1_original = %q, want emoji + ASCII '?'", sha1Orig.String)
	}
	_, _, _, _, _, _, md5Orig := metadataRowOf(t, adm, schema, 721)
	if md5Orig.String != "??" {
		t.Fatalf("stored md5_original = %q, want two ASCII '?'", md5Orig.String)
	}
	_, _, _, _, _, litSHA1, litMD5 := metadataRowOf(t, adm, schema, 722)
	if litSHA1.String != "�" || litMD5.String != "?" {
		t.Fatalf("stored literals = %q/%q, want literal U+FFFD and literal '?'", litSHA1.String, litMD5.String)
	}

	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoS", "", "sur.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// 读回期望是编码后的值：孤立代理已成为 0x3f 码元（不逆推入库前的
	// 孤立代理项）。
	// The read-back expectation is the ENCODED value: lone surrogates come
	// back as 0x3f units (the pre-insert units are not reconstructed).
	wantRT := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 1, ModifiedMillis: 2, UpdatedMillis: 3,
		SHA1Original: []uint16{0xD83D, 0xDE00, 0x3F},
		MD5Original:  []uint16{0x3F, 0x3F},
	})
	equalSnapshot(t, got.Metadata, wantRT)
	gotLit, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoS", "", "lit.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup(literal): %v", err)
	}
	equalSnapshot(t, gotLit.Metadata, literal)
	if s1 := gotLit.Metadata.SHA1OriginalUTF16(); len(s1) != 1 || s1[0] != 0xFFFD {
		t.Fatalf("literal U+FFFD read-back = %04x, want single 0xfffd", s1)
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestFileMetadataActorReadSideAbbreviation(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)

	// 存量行可携带超过 64 个 UTF-16 码元的 actor（VARCHAR(64) 按字符计）：
	// 读取侧经 NewActor 缩写为首 61 码元 + 三个 '.'；64 个 ASCII 不缩写。
	// A stored row may carry an actor above 64 UTF-16 units (VARCHAR(64)
	// counts characters): read-side NewActor abbreviates to the first 61
	// units plus three dots; 64 ASCII characters stay verbatim.
	longEmoji := strings.Repeat("\U0001F600", 64) // 64 chars = 128 units
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, err := adm.ExecContext(qctx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth, created, modified, created_by, modified_by) "+
			"VALUES ($1, 1, $2, $3, $4, $5, $6, $6, $7, $8)", schema),
		731, "repoA2", ".", "long.bin", 2, int64(7), longEmoji, strings.Repeat("a", 64)); err != nil {
		t.Fatalf("raw insert long actors: %v", err)
	}
	meta := nodeMeta(strings.Repeat("9", 40))
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoA2", "", "long.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Metadata == nil {
		t.Fatal("raw row with known times must decode a snapshot")
	}
	units := got.Metadata.CreatedBy().UTF16()
	if len(units) != 64 {
		t.Fatalf("abbreviated created_by = %d units, want 64 (61 kept + 3 dots)", len(units))
	}
	want := mdUnits(longEmoji)
	for i := 0; i < 61; i++ {
		if units[i] != want[i] {
			t.Fatalf("kept unit %d = %04x, want %04x", i, units[i], want[i])
		}
	}
	if units[61] != 0x2E || units[62] != 0x2E || units[63] != 0x2E {
		t.Fatalf("abbreviation suffix = %04x, want three dots", units[61:64])
	}
	if m := got.Metadata.ModifiedBy().UTF16(); len(m) != 64 || m[63] != 'a' {
		t.Fatalf("64 ASCII modified_by must stay verbatim, got %d units", len(m))
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestFileMetadataOriginalLengthBoundaries(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("4", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// 1024 个 emoji（2048 码元/4096 字节）恰可存；1025 个以 22001 拒绝；
	// 超限部分仅为尾部空格时 PostgreSQL 截去超限空格（无 Go 侧预检）。
	// Exactly 1024 emoji (2048 units/4096 bytes) fit; 1025 is rejected with
	// 22001; an excess consisting only of trailing spaces is truncated by
	// PostgreSQL (no Go-side pre-check).
	ok1024 := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 1, ModifiedMillis: 1,
		SHA1Original: mdUnits(strings.Repeat("\U0001F600", 1024)),
	})
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, 741, "repoL", "", "ok.bin", meta)
	ref.Metadata = &ok1024
	if n, err := st.Create(ctx, ref); err != nil || n != 1 {
		t.Fatalf("Create(1024 emoji) = (%d, %v), want (1, nil)", n, err)
	}
	tooLong := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 1, ModifiedMillis: 1,
		SHA1Original: mdUnits(strings.Repeat("\U0001F600", 1025)),
	})
	refLong := fileRefOf(t, 742, "repoL", "", "long.bin", meta)
	refLong.Metadata = &tooLong
	_, err := st.Create(ctx, refLong)
	if err == nil {
		t.Fatal("Create(1025 emoji) must fail")
	}
	if code := pgCodeOf(t, err); code != "22001" {
		t.Fatalf("overlong original SQLSTATE = %s, want 22001", code)
	}
	if s := err.Error(); s != "postgres nodes: create file reference" {
		t.Fatalf("overlong Error() = %q, want fixed create stage text", s)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("count after rollback = %d, want 0", n)
	}

	// 1020 'a' + 10 空格 = 1030 字符：存为 1024 字符（1020 'a' + 4 空格）。
	// 1020 'a' + 10 spaces = 1030 characters: stored as 1024 (1020 'a' plus
	// 4 spaces).
	spaced := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 1, ModifiedMillis: 1,
		SHA1Original: mdUnits(strings.Repeat("a", 1020) + strings.Repeat(" ", 10)),
	})
	tx2, st2 := nodeTx(t, db)
	refSp := fileRefOf(t, 743, "repoL", "", "spaces.bin", meta)
	refSp.Metadata = &spaced
	if n, err := st2.Create(ctx, refSp); err != nil || n != 1 {
		t.Fatalf("Create(trailing spaces) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	_, _, _, _, _, sha1Orig, _ := metadataRowOf(t, adm, schema, 743)
	wantStored := strings.Repeat("a", 1020) + strings.Repeat(" ", 4)
	if sha1Orig.String != wantStored {
		t.Fatalf("stored spaced original len = %d, want %d (trailing spaces truncated by PostgreSQL)",
			len(sha1Orig.String), len(wantStored))
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoL", "", "spaces.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup(spaces): %v", err)
	}
	if s1 := got.Metadata.SHA1OriginalUTF16(); len(s1) != 1024 {
		t.Fatalf("read-back spaced original = %d units, want 1024", len(s1))
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestFileMetadataNULRejected22021(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("5", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	// NUL 逐字送达数据库并以 22021 拒绝（不预截断/替换）；错误有界，原因经
	// pgCodeOf 的 errors.As 可达；调用方回滚后无残留行。
	// NUL reaches the database verbatim and is rejected with 22021 (no
	// pre-truncation/replacement); the error stays bounded, the cause stays
	// reachable (pgCodeOf asserts errors.As), and no row survives the
	// caller rollback.
	nulMeta := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 1, ModifiedMillis: 1,
		SHA1Original: []uint16{'a', 0, 'b'},
	})
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, 751, "repoN", "", "nul.bin", meta)
	ref.Metadata = &nulMeta
	_, err := st.Create(ctx, ref)
	if err == nil {
		t.Fatal("Create with NUL original must fail")
	}
	if code := pgCodeOf(t, err); code != "22021" {
		t.Fatalf("NUL SQLSTATE = %s, want 22021", code)
	}
	if s := err.Error(); s != "postgres nodes: create file reference" || len(s) > 200 {
		t.Fatalf("NUL Error() = %q, want bounded create stage text", s)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 0 {
		t.Fatalf("count after NUL rollback = %d, want 0", n)
	}
}

func TestFileMetadataUpdateNilPreservesNonNilReplaces(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("6", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	snapA := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 100, ModifiedMillis: 200, UpdatedMillis: 300,
		CreatedBy:    repository.NewActor(mdUnits("admin")),
		ModifiedBy:   repository.NewActor(mdUnits("ops")),
		SHA1Original: mdUnits("orig-1"),
		MD5Original:  mdUnits("d-1"),
	})
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, 761, "repoU", "dir", "a.bin", meta)
	ref.Metadata = &snapA
	if n, err := st.Create(ctx, ref); err != nil || n != 1 {
		t.Fatalf("Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// nil Metadata 更新：只更新可变投影列，七列元数据保持不变。
	// A nil-Metadata update touches only the mutable projection columns;
	// the seven metadata columns are preserved.
	tx2, st2 := nodeTx(t, db)
	updNil := fileRefOf(t, 761, "repoU", "new", "a.bin", meta)
	if n, err := st2.Update(ctx, updNil); err != nil || n != 1 {
		t.Fatalf("Update(nil metadata) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig :=
		metadataRowOf(t, adm, schema, 761)
	if !created.Valid || created.Int64 != 100 || !modified.Valid || modified.Int64 != 200 ||
		!updated.Valid || updated.Int64 != 300 || createdBy.String != "admin" ||
		modifiedBy.String != "ops" || sha1Orig.String != "orig-1" || md5Orig.String != "d-1" {
		t.Fatalf("nil update disturbed metadata: %v/%v/%v/%v/%v/%v/%v",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}

	// 非 nil Metadata 更新：同一 UPDATE 原子替换七列，包括把既有值替换为
	// NULL（null actor / nil original）。
	// A nonnil-Metadata update atomically replaces all seven columns in one
	// UPDATE, including replacing existing values with NULL (null actors
	// and nil originals).
	snapB := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: -7, ModifiedMillis: 0, UpdatedMillis: 7,
		CreatedBy:    repository.NewActor(nil),
		ModifiedBy:   repository.NewActor(mdUnits("")),
		SHA1Original: nil,
		MD5Original:  []uint16{},
	})
	tx3, st3 := nodeTx(t, db)
	updB := fileRefOf(t, 761, "repoU", "new", "a.bin", meta)
	updB.Metadata = &snapB
	if n, err := st3.Update(ctx, updB); err != nil || n != 1 {
		t.Fatalf("Update(nonnil metadata) = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx3.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig =
		metadataRowOf(t, adm, schema, 761)
	if !created.Valid || created.Int64 != -7 || !modified.Valid || modified.Int64 != 0 ||
		!updated.Valid || updated.Int64 != 7 || createdBy.Valid ||
		!modifiedBy.Valid || modifiedBy.String != "" || sha1Orig.Valid ||
		!md5Orig.Valid || md5Orig.String != "" {
		t.Fatalf("nonnil update did not replace all seven: %v/%v/%v/%v/%v/%v/%v",
			created, modified, updated, createdBy, modifiedBy, sha1Orig, md5Orig)
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoU", "new", "a.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	equalSnapshot(t, got.Metadata, snapB)

	// 缺失 ID 的元数据更新：0 行、不创建、不影响既有行。
	// A metadata update on a missing ID: zero rows, no create, no effect on
	// existing rows.
	missing := fileRefOf(t, 762, "repoU", "x", "y.bin", meta)
	missing.Metadata = &snapA
	if n, err := lookSt.Update(ctx, missing); err != nil || n != 0 {
		t.Fatalf("Update(missing id) = (%d, %v), want (0, nil)", n, err)
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("count = %d, want 1 (no insert from missing-id update)", n)
	}
}

func TestFileMetadataFailedUpdateRollbackPreservesOriginal(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)
	sha1 := strings.Repeat("7", 40)
	seedNodeBinary(t, adm, schema, sha1)
	meta := nodeMeta(sha1)

	snapA := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 11, ModifiedMillis: 22, UpdatedMillis: 33,
		CreatedBy:    repository.NewActor(mdUnits("keeper")),
		SHA1Original: mdUnits("keep-orig"),
	})
	tx, st := nodeTx(t, db)
	ref := fileRefOf(t, 771, "repoR", "dir", "a.bin", meta)
	ref.Metadata = &snapA
	if n, err := st.Create(ctx, ref); err != nil || n != 1 {
		t.Fatalf("Create = (%d, %v), want (1, nil)", n, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 同一事务：一次成功写入 + 一次超长 original 的失败元数据更新；调用方
	// 回滚后两者全部消失，原行元数据保持。
	// One transaction: a successful write plus a failing metadata update
	// with an overlong original; the caller rollback discards both and the
	// original row keeps its metadata.
	tx2, st2 := nodeTx(t, db)
	if n, err := st2.Create(ctx, fileRefOf(t, 772, "repoR", "dir", "b.bin", meta)); err != nil || n != 1 {
		t.Fatalf("in-tx Create = (%d, %v), want (1, nil)", n, err)
	}
	bad := repository.NewFileMetadata(repository.FileMetadataInput{
		CreatedMillis: 1, ModifiedMillis: 1,
		SHA1Original: mdUnits(strings.Repeat("\U0001F600", 1025)),
	})
	updBad := fileRefOf(t, 771, "repoR", "dir", "a.bin", meta)
	updBad.Metadata = &bad
	_, err := st2.Update(ctx, updBad)
	if err == nil {
		t.Fatal("Update with overlong original must fail")
	}
	if code := pgCodeOf(t, err); code != "22001" {
		t.Fatalf("failed update SQLSTATE = %s, want 22001", code)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("caller rollback: %v", err)
	}
	if n := countNodes(t, adm, schema); n != 1 {
		t.Fatalf("count after rollback = %d, want 1 (earlier write rolled back)", n)
	}
	lookTx, lookSt := nodeTx(t, db)
	got, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoR", "dir", "a.bin", meta).Path)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	equalSnapshot(t, got.Metadata, snapA)
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

func TestFileMetadataLookupMalformedShapeDefenses(t *testing.T) {
	db, schema := nodeFixture(t)
	adm := adminPool(t)
	ctx := nodeTestContext(t)

	// 防御性读取：绕过约束注入畸形行（先移除本地形状约束），Lookup 必须给
	// 出有界 stageLookup 错误且保留 ErrInvalidReference；文件 Kind 检查先于
	// 元数据处理（文件夹畸形行仍报 ErrNotFile）。
	// Defensive reads: malformed rows injected past the constraint (the
	// local shape constraint is dropped first) must yield the bounded
	// stageLookup error preserving ErrInvalidReference; the file-kind check
	// precedes metadata handling (a malformed folder row still reports
	// ErrNotFile).
	qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
	defer qcancel()
	if _, err := adm.ExecContext(qctx, fmt.Sprintf(
		"ALTER TABLE %s.nodes DROP CONSTRAINT nodes_metadata_shape", schema)); err != nil {
		t.Fatalf("drop shape constraint for malformed-row injection: %v", err)
	}
	if _, err := adm.ExecContext(qctx, fmt.Sprintf(
		"INSERT INTO %s.nodes (node_id, node_type, repo, node_path, node_name, depth, created) "+
			"VALUES ($1, 1, $2, '.', $3, 2, $4), ($5, 0, $6, '.', $7, 2, $8)", schema),
		781, "repoF2", "partial.bin", int64(1), 782, "repoF2", "folder.bin", int64(1)); err != nil {
		t.Fatalf("inject malformed rows: %v", err)
	}

	meta := nodeMeta(strings.Repeat("8", 40))
	lookTx, lookSt := nodeTx(t, db)
	_, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoF2", "", "partial.bin", meta).Path)
	if err == nil {
		t.Fatal("partial metadata shape must fail the lookup")
	}
	if !errors.Is(err, refpg.ErrInvalidReference) {
		t.Fatalf("partial shape error must wrap ErrInvalidReference, got %v", err)
	}
	if s := err.Error(); s != "postgres nodes: lookup file reference" || len(s) > 200 {
		t.Fatalf("partial shape Error() = %q, want bounded stageLookup text", s)
	}
	if _, err := lookSt.Lookup(ctx, fileRefOf(t, 0, "repoF2", "", "folder.bin", meta).Path); !errors.Is(err, refpg.ErrNotFile) {
		t.Fatalf("malformed folder row must report ErrNotFile before metadata handling, got %v", err)
	}
	if err := lookTx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
}

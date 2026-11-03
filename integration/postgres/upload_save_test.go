// 真实 PostgreSQL 的普通上传保存缝集成测试（BIN-206）：真实 NewUploadSaver
// （懒节点事务、真实 Current/Reserve 号段、真实 Registry/filestore）配 SQL
// 触发器 oracle，覆盖 fresh/replay/overwrite/empty、正确/错误 headers、读正文
// 期间零节点连接、发布后节点故障与号段冲突的不补偿、同事务二读投影守卫
// （触发器改 Content 拒绝、createdBy 被 DB 改写返回 DB 权威）与延迟约束
// Commit 失败。不建立 HTTP/授权行为（后续 HTTP 角色负责）。
// Real-PostgreSQL integration tests for the ordinary upload save seam
// (BIN-206): the real NewUploadSaver (lazy node transaction, real
// Current/Reserve allocator, real Registry/filestore) plus SQL trigger
// oracles cover fresh/replay/overwrite/empty, right/wrong headers, zero node
// connections during the body read, non-compensating node faults and ID
// high-water conflicts after publication, same-transaction re-read
// projection guards (trigger-mutated Content rejected, DB-rewritten createdBy
// returned as the authority) and deferred-constraint Commit failure. No HTTP
// or authorization behavior is established here (the later HTTP role owns it).
package postgresit

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/0ldlight/bf/internal/filestore"
	"github.com/0ldlight/bf/internal/localrepo"
	uploadpg "github.com/0ldlight/bf/internal/localrepo/postgres"
	"github.com/0ldlight/bf/internal/repository"
	refpg "github.com/0ldlight/bf/internal/repository/postgres"
)

// usClock 是固定毫秒钟：测试在每次 Save 前串行设置值（单测试 goroutine，
// 无并发访问）。固定值让 created/modified/updated 与 SQL 断言完全确定。
// usClock is a fixed-millis clock: the test sets the value serially before
// each Save (single test goroutine, no concurrent access). Fixed values make
// created/modified/updated and the SQL assertions fully deterministic.
type usClock struct{ millis int64 }

func (c *usClock) NowMillis() int64 { return c.millis }
func (c *usClock) set(v int64)      { c.millis = v }

const (
	usMillisOne = int64(1_700_000_000_123)
	usMillisTwo = int64(1_700_000_000_456)
)

// usFixture 组合被测保存缝的全部真实依赖：隔离 schema 上的 nodes +
// unique_ids 模式、两连接池、真实 Publisher/Registry/filestore 与真实
// NewUploadSaver 产物。
// usFixture composes every real dependency of the save seam under test:
// the nodes plus unique_ids schemas on an isolated schema, a two-connection
// pool, the real Publisher/Registry/filestore and the real NewUploadSaver.
type usFixture struct {
	db, adm *sql.DB
	schema  string
	clock   *usClock
	saver   *uploadpg.UploadSaver
	store   *filestore.Store
	root    string
}

func usSetup(t *testing.T) usFixture {
	t.Helper()
	db, schema := nodeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, refpg.ReservationSchemaSQL); err != nil {
		t.Fatalf("apply reservation schema: %v", err)
	}
	db.SetMaxOpenConns(2)
	svc, store, root := cfBinaryService(t, db)
	clock := &usClock{millis: usMillisOne}
	saver, err := uploadpg.NewUploadSaver(uploadpg.UploadDeps{
		DB: db, Publisher: svc, Clock: clock, NodeTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewUploadSaver: %v", err)
	}
	return usFixture{db: db, adm: adminPool(t), schema: schema, clock: clock, saver: saver, store: store, root: root}
}

// usDigests 用测试本地标准库独立计算三种摘要（不复用被测产品代码）。
// usDigests computes all three digests independently with the local standard
// library (never reusing product code under test).
func usDigests(payload string) (sha1Hex, sha256Hex, md5Hex string) {
	s1, s2, s5 := sha1.Sum([]byte(payload)), sha256.Sum256([]byte(payload)), md5.Sum([]byte(payload))
	return hex.EncodeToString(s1[:]), hex.EncodeToString(s2[:]), hex.EncodeToString(s5[:])
}

// usInput 构造一份全部 headers 与 DeclaredSize 均与 payload 对账的合法输入。
// usInput builds one valid input whose headers and DeclaredSize all reconcile
// with the payload.
func usInput(t *testing.T, repo, rawPath, actor, payload string) localrepo.UploadInput {
	t.Helper()
	sha1Hex, sha256Hex, md5Hex := usDigests(payload)
	path, err := localrepo.NewUploadPath(repo, rawPath)
	if err != nil {
		t.Fatalf("NewUploadPath(%q,%q): %v", repo, rawPath, err)
	}
	return localrepo.UploadInput{
		Path: path,
		Checksums: repository.ClientFileChecksums{
			SHA1Original: mdUnits(sha1Hex), SHA256Original: mdUnits(sha256Hex), MD5Original: mdUnits(md5Hex),
		},
		OperatorUTF16: mdUnits(actor),
		DeclaredSize:  int64(len(payload)),
	}
}

// usSave 是带节点测试上下文的窄入口。
// usSave is the narrow entry with the bounded node-test context.
func (f usFixture) usSave(t *testing.T, payload string, in localrepo.UploadInput) (repository.FileSnapshotSource, error) {
	t.Helper()
	return f.saver.Save(nodeTestContext(t), strings.NewReader(payload), in)
}

// usStatsReader 在每次真实 Read 采样保存缝连接池的 InUse：正文读取阶段
// 不得占用任何节点/数据库连接（懒事务的前置证明）。
// usStatsReader samples the save-seam pool InUse on every real Read: the
// body-reading phase must hold no node/database connection (the lazy
// transaction precondition).
type usStatsReader struct {
	r        *strings.Reader
	db       *sql.DB
	reads    int
	maxInUse int
}

func (b *usStatsReader) Read(p []byte) (int, error) {
	b.reads++
	if n := b.db.Stats().InUse; n > b.maxInUse {
		b.maxInUse = n
	}
	return b.r.Read(p)
}

// usAdmittedMismatch 仅承认两种业务摘要哨兵之一为叶子（HTTP 409 白名单）。
// usAdmittedMismatch admits only one of the two business checksum sentinels
// as the leaf (the HTTP 409 whitelist).
func usAdmittedMismatch(err error) bool {
	return errors.Is(err, filestore.ErrChecksumMismatch) || errors.Is(err, repository.ErrClientChecksumMismatch)
}

// usDDL 在隔离 schema 上执行固定 fixture DDL（触发器/函数）。
// usDDL runs fixed fixture DDL (triggers/functions) on the isolated schema.
func usDDL(t *testing.T, adm *sql.DB, statement string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := adm.ExecContext(ctx, statement); err != nil {
		t.Fatalf("fixture DDL: %v", err)
	}
}

// usRequireNode 用独立 SQL 直读并核对完整节点行：投影身份、三摘要+长度、
// 三时间、两 actor 与两 original。
// usRequireNode reads and verifies the full node row through independent SQL:
// projection identity, three digests plus size, three times, both actors and
// both originals.
func usRequireNode(t *testing.T, f usFixture, id int64, repo, parent, name string,
	payload string, wantCreated, wantModified, wantUpdated int64, creator, modifier string,
	sha1Orig, md5Orig sql.NullString) {
	t.Helper()
	sha1Hex, sha256Hex, md5Hex := usDigests(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var nodeType, depth int16
	var gotRepo, gotPath, gotName, gotSHA1, gotSHA256, gotMD5 string
	var created, modified, updated, size int64
	var gotCreator, gotModifier sql.NullString
	var gotSHA1Orig, gotMD5Orig sql.NullString
	err := f.adm.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT node_type,repo,node_path,node_name,depth,bin_length,sha1_actual,sha256,md5_actual,created,modified,updated,created_by,modified_by,sha1_original,md5_original FROM %s.nodes WHERE node_id=$1", f.schema), id).
		Scan(&nodeType, &gotRepo, &gotPath, &gotName, &depth, &size, &gotSHA1, &gotSHA256, &gotMD5,
			&created, &modified, &updated, &gotCreator, &gotModifier, &gotSHA1Orig, &gotMD5Orig)
	if err != nil {
		t.Fatalf("independent node read: %v", err)
	}
	if created != wantCreated || modified != wantModified || updated != wantUpdated {
		t.Fatalf("node times mismatch: %d/%d/%d, want %d/%d/%d", created, modified, updated, wantCreated, wantModified, wantUpdated)
	}
	if nodeType != 1 || gotRepo != repo || gotPath != parent || gotName != name || depth != 2 ||
		size != int64(len(payload)) || gotSHA1 != sha1Hex || gotSHA256 != sha256Hex || gotMD5 != md5Hex {
		t.Fatalf("node projection/content mismatch: type=%d repo=%q path=%q name=%q depth=%d size=%d", nodeType, gotRepo, gotPath, gotName, depth, size)
	}
	if gotCreator.String != creator || gotModifier.String != modifier || !gotCreator.Valid || !gotModifier.Valid {
		t.Fatalf("node actors mismatch: %v/%v, want %q/%q", gotCreator, gotModifier, creator, modifier)
	}
	if gotSHA1Orig != sha1Orig || gotMD5Orig != md5Orig {
		t.Fatalf("node originals mismatch: %v/%v, want %v/%v", gotSHA1Orig, gotMD5Orig, sha1Orig, md5Orig)
	}
}

// usRequireSource 把权威返回值与本地摘要、请求事实对账并复跑共享
// ValidateUploadSource。
// usRequireSource reconciles the authoritative return value with the local
// digests and request facts and re-runs the shared ValidateUploadSource.
func usRequireSource(t *testing.T, src repository.FileSnapshotSource, in localrepo.UploadInput, id int64, payload string) {
	t.Helper()
	sha1Hex, sha256Hex, md5Hex := usDigests(payload)
	if src.Reference.ID != id || src.Reference.Path != in.Path || src.Reference.Path.IsFolder() {
		t.Fatalf("source identity mismatch: id=%d", src.Reference.ID)
	}
	if content := src.Reference.Content; content.SHA1 != sha1Hex || content.SHA256 != sha256Hex ||
		content.MD5 != md5Hex || content.Size != int64(len(payload)) {
		t.Fatalf("source content mismatch: %+v", content)
	}
	if src.Reference.Metadata == nil {
		t.Fatal("source metadata missing")
	}
	if err := localrepo.ValidateUploadSource(src, in.Path); err != nil {
		t.Fatalf("ValidateUploadSource: %v", err)
	}
}

// usRequirePublished 核对发布不补偿：对象字节、恰好一个文件、登记行四字段
// 与指定总行数。
// usRequirePublished verifies non-compensated publication: object bytes,
// exactly one file, the registration row's four fields and the given row
// count.
func usRequirePublished(t *testing.T, f usFixture, payload string, rows int) {
	t.Helper()
	sha1Hex, sha256Hex, md5Hex := usDigests(payload)
	cfRequireObjectBytes(t, f.store, sha1Hex, payload)
	if n := cfCountFiles(t, f.root); n != 1 {
		t.Fatalf("store files = %d, want 1 (no compensation or duplication)", n)
	}
	gotSHA1, gotMD5, gotLen, gotSHA256, ok := rowOf(t, f.adm, f.schema, sha1Hex)
	if !ok || gotSHA1 != sha1Hex || gotMD5 != md5Hex || gotLen != int64(len(payload)) ||
		!gotSHA256.Valid || gotSHA256.String != sha256Hex {
		t.Fatalf("binaries row mismatch for %s", sha1Hex)
	}
	if n := countRows(t, f.adm, f.schema); n != rows {
		t.Fatalf("binaries rows = %d, want %d", n, rows)
	}
}

// usRequireZeroSource 断言失败路径零 Source。
// usRequireZeroSource asserts a zero Source on failure paths.
func usRequireZeroSource(t *testing.T, src repository.FileSnapshotSource, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) || src != (repository.FileSnapshotSource{}) {
		t.Fatalf("failure = (%+v, %v), want zero source wrapping %v", src, err, want)
	}
}

// 场景一：全新上传（三个正确 headers）→ replay 同字节同值 → overwrite 换
// 字节换 actor；全程独立 SQL/字节对账，actor/created/original 保留。
// Scenario one: a fresh upload (three correct headers) -> an identical-byte
// replay with identical values -> an overwrite with new bytes and a new
// actor; independent SQL/byte reconciliation throughout with
// actor/created/original retention.
func TestUploadSaveFreshReplayOverwriteAndEmpty(t *testing.T) {
	f := usSetup(t)
	payloadA := "ordinary upload alpha bytes\n"
	in := usInput(t, "upRepo", "dir/a.bin", "writer", payloadA)
	if err := localrepo.ValidateUploadInput(in); err != nil {
		t.Fatalf("ValidateUploadInput: %v", err)
	}
	src, err := f.usSave(t, payloadA, in)
	if err != nil {
		t.Fatalf("fresh save: %v", err)
	}
	sha1Hex, _, md5Hex := usDigests(payloadA)
	usRequireSource(t, src, in, 1, payloadA)
	if c, m, u := src.Reference.Metadata.CreatedMillis(), src.Reference.Metadata.ModifiedMillis(), src.Reference.Metadata.UpdatedMillis(); c != usMillisOne || m != usMillisOne || u != usMillisOne {
		t.Fatalf("fresh metadata times = %d/%d/%d, want all %d", c, m, u, usMillisOne)
	}
	equalMDUnits(t, "fresh createdBy", src.Reference.Metadata.CreatedBy().UTF16(), mdUnits("writer"))
	equalMDUnits(t, "fresh modifiedBy", src.Reference.Metadata.ModifiedBy().UTF16(), mdUnits("writer"))
	equalMDUnits(t, "fresh sha1 original", src.Reference.Metadata.SHA1OriginalUTF16(), mdUnits(sha1Hex))
	equalMDUnits(t, "fresh md5 original", src.Reference.Metadata.MD5OriginalUTF16(), mdUnits(md5Hex))
	usRequireNode(t, f, 1, "upRepo", "dir", "a.bin", payloadA, usMillisOne, usMillisOne, usMillisOne, "writer", "writer",
		sql.NullString{String: sha1Hex, Valid: true}, sql.NullString{String: md5Hex, Valid: true})
	usRequirePublished(t, f, payloadA, 1)
	cfRequireBound(t, f.adm, f.schema, 1)

	// replay：同字节、同 headers、同固定钟值；不做“必 skip”笼统假设，但
	// created/actor 必保留且无重复对象/登记/新号段。
	// Replay: same bytes, headers and fixed clock value; no blanket
	// "must skip" assumption, while created/actor stay retained with no
	// duplicated object/registration/new ID range.
	src2, err := f.usSave(t, payloadA, in)
	if err != nil || src2.Reference.ID != 1 {
		t.Fatalf("replay = (%d, %v), want id 1", src2.Reference.ID, err)
	}
	usRequireSource(t, src2, in, 1, payloadA)
	usRequireNode(t, f, 1, "upRepo", "dir", "a.bin", payloadA, usMillisOne, usMillisOne, usMillisOne, "writer", "writer",
		sql.NullString{String: sha1Hex, Valid: true}, sql.NullString{String: md5Hex, Valid: true})
	usRequirePublished(t, f, payloadA, 1)
	cfRequireBound(t, f.adm, f.schema, 1)

	// overwrite：新字节、新 actor、新钟值；created/createdBy 保留，
	// modified/updated/modifiedBy 更新，originals 取新权威值。
	// Overwrite: new bytes, actor and clock value; created/createdBy stay
	// retained while modified/updated/modifiedBy move and the originals take
	// the new authoritative values.
	payloadB := "ordinary upload beta bytes with a longer tail\n"
	f.clock.set(usMillisTwo)
	inB := usInput(t, "upRepo", "dir/a.bin", "second", payloadB)
	src3, err := f.usSave(t, payloadB, inB)
	if err != nil || src3.Reference.ID != 1 {
		t.Fatalf("overwrite = (%d, %v), want id 1", src3.Reference.ID, err)
	}
	sha1B, _, md5B := usDigests(payloadB)
	usRequireSource(t, src3, inB, 1, payloadB)
	usRequireNode(t, f, 1, "upRepo", "dir", "a.bin", payloadB, usMillisOne, usMillisTwo, usMillisTwo, "writer", "second",
		sql.NullString{String: sha1B, Valid: true}, sql.NullString{String: md5B, Valid: true})
	if n := cfCountFiles(t, f.root); n != 2 {
		t.Fatalf("store files after overwrite = %d, want 2", n)
	}
	cfRequireObjectBytes(t, f.store, sha1B, payloadB)
	if n := countRows(t, f.adm, f.schema); n != 2 {
		t.Fatalf("binaries rows after overwrite = %d, want 2", n)
	}
	cfRequireBound(t, f.adm, f.schema, 1)

	// empty：零字节正文加三个空内容真实摘要；ValidateUploadSource 的
	// “空 SHA-1 ⇒ size 0”事实必须成立。
	// Empty: a zero-byte body with the three real empty digests; the
	// "empty SHA-1 implies size 0" ValidateUploadSource fact must hold.
	t.Run("empty", func(t *testing.T) {
		fe := usSetup(t)
		in := usInput(t, "upRepo", "dir/empty.txt", "writer", "")
		src, err := fe.usSave(t, "", in)
		if err != nil {
			t.Fatalf("empty save: %v", err)
		}
		if src.Reference.Content.Size != 0 {
			t.Fatalf("empty size = %d, want 0", src.Reference.Content.Size)
		}
		usRequireSource(t, src, in, 1, "")
		sha1Empty, _, md5Empty := usDigests("")
		usRequireNode(t, fe, 1, "upRepo", "dir", "empty.txt", "", usMillisOne, usMillisOne, usMillisOne, "writer", "writer",
			sql.NullString{String: sha1Empty, Valid: true}, sql.NullString{String: md5Empty, Valid: true})
		usRequirePublished(t, fe, "", 1)
		cfRequireBound(t, fe.adm, fe.schema, 1)
	})
}

// 场景二：真实 Reader 回调证明读正文期间保存缝池 InUse==0（懒事务）；
// 三种错误 headers 在发布前拒绝且无节点访问/号段推进，纯业务哨兵不得
// 降级为 ErrUploadPersistence。
// Scenario two: a real Reader callback proves pool InUse==0 during the body
// read (lazy transaction); the three wrong headers reject before publication
// with no node access or ID progress, and the pure business sentinel never
// degrades into ErrUploadPersistence.
func TestUploadSaveBodyReadHoldsNoNodeConnectionAndWrongHeaders(t *testing.T) {
	f := usSetup(t)
	payload := "body phase keeps the node pool idle\n"
	in := usInput(t, "upRepo", "dir/b.bin", "writer", payload)
	body := &usStatsReader{r: strings.NewReader(payload), db: f.db}
	src, err := f.saver.Save(nodeTestContext(t), body, in)
	if err != nil {
		t.Fatalf("save with stats reader: %v", err)
	}
	if body.reads == 0 || body.maxInUse != 0 {
		t.Fatalf("body reads=%d maxInUse=%d, want reads>0 and InUse=0 during reads", body.reads, body.maxInUse)
	}
	usRequireSource(t, src, in, 1, payload)
	usRequirePublished(t, f, payload, 1)

	for _, kind := range []string{"sha1", "sha256", "md5"} {
		t.Run("wrong-"+kind, func(t *testing.T) {
			fw := usSetup(t)
			in := usInput(t, "upRepo", "dir/b.bin", "writer", payload)
			switch kind {
			case "sha1":
				in.Checksums.SHA1Original = mdUnits(strings.Repeat("7", 40))
			case "sha256":
				in.Checksums.SHA256Original = mdUnits(strings.Repeat("7", 64))
			case "md5":
				in.Checksums.MD5Original = mdUnits(strings.Repeat("7", 32))
			}
			src, err := fw.usSave(t, payload, in)
			if err == nil || !usAdmittedMismatch(err) {
				t.Fatalf("wrong %s = (%+v, %v), want an admitted mismatch leaf", kind, src, err)
			}
			if errors.Is(err, localrepo.ErrUploadPersistence) {
				t.Fatalf("pure %s mismatch degraded into persistence: %v", kind, err)
			}
			if src != (repository.FileSnapshotSource{}) {
				t.Fatalf("wrong %s returned a source", kind)
			}
			if countNodes(t, fw.adm, fw.schema) != 0 {
				t.Fatal("node written on checksum mismatch")
			}
			if bound, present := boundOf(t, fw.adm, fw.schema); !present || bound != 0 {
				t.Fatalf("id bound = (%d, %v) on mismatch, want (0, true)", bound, present)
			}
			if n := cfCountFiles(t, fw.root); n != 0 {
				t.Fatalf("store files = %d on mismatch, want 0", n)
			}
			if n := countRows(t, fw.adm, fw.schema); n != 0 {
				t.Fatalf("binaries rows = %d on mismatch, want 0", n)
			}
		})
	}
}

// 场景三：provider/registry 已发布后的节点故障不补偿——(a) 节点 INSERT 被
// 触发器拒绝：ID 号段已推进并保留；(b) Reserve 条件 UPDATE 被触发器抑制
// （返回 NULL ⇒ 0 行受影响）：高水位冲突、号段不动。两者字节/登记保留。
// Scenario three: node faults after provider/registry publication never
// compensate — (a) the node INSERT is rejected by a trigger: the ID range
// already advanced and stays; (b) the Reserve conditional UPDATE is
// suppressed by a trigger (returns NULL, hence zero affected rows): the
// high-water conflict leaves the bound untouched. Both keep bytes/registry.
func TestUploadSaveNodeFaultsAfterPublicationRetainState(t *testing.T) {
	t.Run("node-insert-sql-failure", func(t *testing.T) {
		f := usSetup(t)
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE FUNCTION %s.us_reject_node() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'upload save node insert rejected'; END $$ LANGUAGE plpgsql", f.schema))
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE TRIGGER us_reject_node BEFORE INSERT ON %s.nodes FOR EACH ROW EXECUTE FUNCTION %s.us_reject_node()", f.schema, f.schema))
		payload := "published before the node sql fault\n"
		in := usInput(t, "upRepo", "dir/c.bin", "writer", payload)
		src, err := f.usSave(t, payload, in)
		usRequireZeroSource(t, src, err, localrepo.ErrUploadPersistence)
		if countNodes(t, f.adm, f.schema) != 0 {
			t.Fatal("failed node insert survived")
		}
		cfRequireBound(t, f.adm, f.schema, 1)
		usRequirePublished(t, f, payload, 1)
	})
	t.Run("id-high-water-conflict", func(t *testing.T) {
		f := usSetup(t)
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE FUNCTION %s.us_suppress_reserve() RETURNS trigger AS $$ BEGIN RETURN NULL; END $$ LANGUAGE plpgsql", f.schema))
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE TRIGGER us_suppress_reserve BEFORE UPDATE ON %s.unique_ids FOR EACH ROW EXECUTE FUNCTION %s.us_suppress_reserve()", f.schema, f.schema))
		payload := "published before the id conflict\n"
		in := usInput(t, "upRepo", "dir/d.bin", "writer", payload)
		src, err := f.usSave(t, payload, in)
		usRequireZeroSource(t, src, err, localrepo.ErrUploadPersistence)
		if countNodes(t, f.adm, f.schema) != 0 {
			t.Fatal("node written on id conflict")
		}
		cfRequireBound(t, f.adm, f.schema, 0)
		usRequirePublished(t, f, payload, 1)
	})
}

// 场景四：同事务二读与 Commit 语义——(a) 触发器改写 bin_length：二读 Content
// 不等于 Published，投影拒绝并回滚；(b) 触发器把 created_by 改成另一个合法
// 值：成功 Source 必须返回 DB 权威而非请求回显；(c) 延迟约束触发器使真实
// Commit 失败：零 Source、无重试（号段恰一次）、节点缺席而字节/登记/号段
// 保留。
// Scenario four: same-transaction re-read and Commit semantics — (a) a
// trigger rewrites bin_length: the re-read Content no longer equals
// Published, so the projection rejects and rolls back; (b) a trigger rewrites
// created_by to another legal value: the success Source must return the DB
// authority rather than the request echo; (c) a deferred constraint trigger
// fails the real Commit: zero Source, no retry (exactly one reservation), the
// node absent while bytes/registry/ID range survive.
func TestUploadSaveSameTxRereadGuardsAndCommitFailure(t *testing.T) {
	t.Run("content-rewritten-rejected", func(t *testing.T) {
		f := usSetup(t)
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE FUNCTION %s.us_bump_length() RETURNS trigger AS $$ BEGIN NEW.bin_length := NEW.bin_length + 1; RETURN NEW; END $$ LANGUAGE plpgsql", f.schema))
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE TRIGGER us_bump_length BEFORE INSERT ON %s.nodes FOR EACH ROW EXECUTE FUNCTION %s.us_bump_length()", f.schema, f.schema))
		payload := "published before the projection guard\n"
		in := usInput(t, "upRepo", "dir/e.bin", "writer", payload)
		src, err := f.usSave(t, payload, in)
		usRequireZeroSource(t, src, err, localrepo.ErrUploadPersistence)
		if countNodes(t, f.adm, f.schema) != 0 {
			t.Fatal("rejected projection was committed")
		}
		cfRequireBound(t, f.adm, f.schema, 1)
		usRequirePublished(t, f, payload, 1)
	})
	t.Run("created-by-returns-db-authority", func(t *testing.T) {
		f := usSetup(t)
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE FUNCTION %s.us_rewrite_creator() RETURNS trigger AS $$ BEGIN NEW.created_by := 'db-actor'; RETURN NEW; END $$ LANGUAGE plpgsql", f.schema))
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE TRIGGER us_rewrite_creator BEFORE INSERT ON %s.nodes FOR EACH ROW EXECUTE FUNCTION %s.us_rewrite_creator()", f.schema, f.schema))
		payload := "the database rewrites the creator\n"
		in := usInput(t, "upRepo", "dir/f.bin", "writer", payload)
		src, err := f.usSave(t, payload, in)
		if err != nil {
			t.Fatalf("save under creator rewrite: %v", err)
		}
		usRequireSource(t, src, in, 1, payload)
		equalMDUnits(t, "db authority createdBy", src.Reference.Metadata.CreatedBy().UTF16(), mdUnits("db-actor"))
		if got := src.Reference.Metadata.CreatedBy().UTF16(); strings.EqualFold(string(utf16.Decode(got)), "writer") {
			t.Fatal("createdBy echoed the request instead of the DB authority")
		}
		if countNodes(t, f.adm, f.schema) != 1 {
			t.Fatal("creator rewrite commit missing")
		}
		sha1Hex, _, md5Hex := usDigests(payload)
		usRequireNode(t, f, 1, "upRepo", "dir", "f.bin", payload, usMillisOne, usMillisOne, usMillisOne, "db-actor", "writer",
			sql.NullString{String: sha1Hex, Valid: true}, sql.NullString{String: md5Hex, Valid: true})
	})
	t.Run("deferred-constraint-commit-failure", func(t *testing.T) {
		f := usSetup(t)
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE FUNCTION %s.us_deferred_reject() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'upload save deferred rejection'; END $$ LANGUAGE plpgsql", f.schema))
		usDDL(t, f.adm, fmt.Sprintf(
			"CREATE CONSTRAINT TRIGGER us_deferred_reject AFTER INSERT ON %s.nodes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION %s.us_deferred_reject()", f.schema, f.schema))
		payload := "published before the deferred commit fault\n"
		in := usInput(t, "upRepo", "dir/g.bin", "writer", payload)
		src, err := f.usSave(t, payload, in)
		usRequireZeroSource(t, src, err, localrepo.ErrUploadPersistence)
		if code := pgCodeOf(t, err); code != "P0001" {
			t.Fatalf("commit cause SQLSTATE = %s, want P0001 (cause preserved)", code)
		}
		if countNodes(t, f.adm, f.schema) != 0 {
			t.Fatal("failed commit survived")
		}
		cfRequireBound(t, f.adm, f.schema, 1)
		usRequirePublished(t, f, payload, 1)
	})
}

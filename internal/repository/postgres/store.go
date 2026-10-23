// Package postgres 在调用方拥有的 *sql.Tx 上实现事务内文件引用的最小
// nodes 投影（Store），并提供本地 PostgreSQL 号段预留原语（Reservations）。
// Store 侧：调用方拥有事务、节点 ID 与生命周期，Store 从不 Begin/Commit/
// Rollback/Ping/建表/Close。Reservations 侧：调用方仅提供可信 *sql.DB，
// 每次预留由 Reservations 开启并提交自有的 READ COMMITTED 事务（见
// docs/ID-RESERVATIONS.md）。
// Package postgres implements the minimal transaction-scoped nodes
// projection for file references on a caller-owned *sql.Tx (Store), plus the
// local PostgreSQL ID-range reservation primitive (Reservations). On the
// Store side the caller owns the transaction, node IDs and lifetimes and
// Store never begins, commits, rolls back, pings, bootstraps DDL or closes
// anything. On the Reservations side the caller supplies only a trusted
// *sql.DB and every reservation begins and commits its own READ COMMITTED
// transaction (see docs/ID-RESERVATIONS.md).
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
)

// SchemaSQL 是 v2 全新库的最小 nodes 投影（v1 + 七个可空无默认元数据列与
// nodes_metadata_shape 形状约束），仅供可信引导/测试显式应用；依赖既有
// binaries 表（sha1 外键，无级联）。既有 v1 库必须显式执行
// MetadataUpgradeSQL，CREATE TABLE IF NOT EXISTS 不视作升级。
// SchemaSQL is the v2 minimal fresh-database nodes projection (v1 plus the
// seven nullable no-default metadata columns and the nodes_metadata_shape
// constraint) for explicit trusted bootstrap/tests only; it depends on the
// existing binaries table (sha1 foreign key, no cascade). An existing v1
// database must apply MetadataUpgradeSQL explicitly — CREATE TABLE IF NOT
// EXISTS is not an upgrade.
//
//go:embed schema.sql
var SchemaSQL string

// MetadataUpgradeSQL 是既有 v1 nodes 表的一次性加列升级（单条 ALTER：七列 +
// 形状约束），仅供可信运维/测试在自有事务中显式执行；重复执行以 42701
// 报错，绝不静默吞掉模式漂移。Store/启动从不执行任何 DDL。
// MetadataUpgradeSQL is the one-time additive upgrade for an existing v1
// nodes table (a single ALTER: seven columns plus the shape constraint),
// executed explicitly by trusted operators/tests inside their own
// transaction; a repeat fails with 42701 instead of silently swallowing
// schema drift. The Store/startup never executes any DDL.
//
//go:embed metadata_upgrade.sql
var MetadataUpgradeSQL string

// MetadataDowngradeSQL 是对应的物理回退（同一原子 DO 语句）：仅限 READ
// COMMITTED（其他隔离级别以 25000 拒绝——防止旧快照漏看已提交行），先取
// ACCESS EXCLUSIVE 表锁，再拒绝启用 RLS 的表与任何已填充元数据行（均
// 55000，绝不自动清空数据来通过守卫），最后仅删形状约束与七列。
// MetadataDowngradeSQL is the matching physical downgrade inside one atomic
// DO statement: READ COMMITTED only (other isolation levels are rejected
// with 25000 — an older snapshot could miss committed rows), then an ACCESS
// EXCLUSIVE table lock, then refusals for an RLS-enabled table and for any
// populated metadata row (both 55000 — data is never auto-cleared to pass
// the guard), and finally only the shape constraint and the seven columns
// are dropped.
//
//go:embed metadata_downgrade.sql
var MetadataDowngradeSQL string

var (
	// ErrInvalidReference 表示写入/查询输入不满足本投影的显式本地前置
	// （见 validateWriteRef/validateIdentity）；文本永不回显输入。
	// ErrInvalidReference means the input fails this projection's explicit
	// local preconditions; the text never echoes input.
	ErrInvalidReference = errors.New("postgres nodes: invalid reference")
	// ErrNotFile 表示查询命中的存储行不是文件（node_type != 1）。
	// ErrNotFile means the stored row hit by a lookup is not a file.
	ErrNotFile = errors.New("postgres nodes: stored node is not a file")
)

// 阶段文案是固定常量：Error() 只输出操作归因，底层错误对象不经 %w 并入
// 文本；Unwrap 原样保留原因供可信调用方（errors.Is/As 可达）。
// Stage texts are fixed constants: Error() prints only the operation
// attribution and Unwrap exposes the untouched cause for trusted callers.
const (
	stageInvalid   = "postgres nodes: invalid reference"
	stageCancelled = "postgres nodes: cancelled before statement"
	stageCreate    = "postgres nodes: create file reference"
	stageUpdate    = "postgres nodes: update file reference"
	stageLookup    = "postgres nodes: lookup file reference"
)

// opError 携带固定操作文案与原始原因；不修改、不包装原始错误对象本身。
// opError pairs a fixed operation text with the original cause.
type opError struct {
	op    string
	cause error
}

func (e *opError) Error() string { return e.op }
func (e *opError) Unwrap() error { return e.cause }

// Store 绑定调用方拥有的单个 *sql.Tx；无自有连接状态，事务结局（含成功
// 计数的临时性）完全归调用方。
// Store binds one caller-owned *sql.Tx; it holds no connection state of its
// own and the transaction outcome (including the provisional nature of
// successful counts) belongs entirely to the caller.
type Store struct {
	tx *sql.Tx
}

// New 拒绝 nil *sql.Tx，返回有界常量错误；不做 DDL、Ping 或连接创建。
// New rejects a nil *sql.Tx with a bounded constant error; no DDL, Ping or
// connection creation happens here.
func New(tx *sql.Tx) (*Store, error) {
	if tx == nil {
		return nil, errors.New("postgres nodes: nil transaction")
	}
	return &Store{tx: tx}, nil
}

// Create 在调用方事务内做普通参数化 INSERT（node_type=1 + 七个元数据列）
// 并返回受影响行数。nil Metadata 写七列全 NULL（兼容旧投影/未知历史）；
// 非 nil 写全部显式快照字段（含合法的时间 0/负值、null actor/original 与
// 空串）——绑定前按值复制快照。无 ON CONFLICT/回读/重试/提交；成功计数在
// 调用方 Commit 之前只是临时的。对齐目标 NodesDao.create 的普通插入
// （createIfNotExists 是另一方法），SQL 空串父/名仅按空串映射为 '.'。
// Create performs an ordinary parameterized INSERT (node_type=1 plus the
// seven metadata columns) inside the caller transaction and returns the
// affected count. A nil Metadata writes seven NULL columns (compatible
// with the legacy projection and unknown history); a nonnil Metadata writes
// every explicit snapshot field (legal zero/negative times, null actors/
// originals and empty strings included) — the snapshot is copied by value
// before binding. No ON CONFLICT, readback, retry or commit; a successful
// count stays PROVISIONAL until the caller commits. This mirrors the target
// NodesDao.create plain insert (createIfNotExists is a separate method);
// only the empty string maps to the SQL '.' sentinel.
func (s *Store) Create(ctx context.Context, ref repository.FileReference) (int64, error) {
	depth, err := validateWriteRef(ref)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, &opError{op: stageCancelled, cause: err}
	}
	args := append([]any{
		ref.ID, ref.Path.Repo(), dotIfEmpty(ref.Path.Parent()), dotIfEmpty(ref.Path.Name()),
		depth, ref.Content.Size, ref.Content.SHA1, ref.Content.MD5, ref.Content.SHA256,
	}, metadataArgs(ref.Metadata)...)
	res, err := s.tx.ExecContext(ctx,
		"INSERT INTO nodes (node_id, node_type, repo, node_path, node_name, depth, bin_length, "+
			"sha1_actual, md5_actual, sha256, created, modified, updated, created_by, modified_by, "+
			"sha1_original, md5_original) "+
			"VALUES ($1, 1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)",
		args...)
	if err != nil {
		return 0, &opError{op: stageCreate, cause: err}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, &opError{op: stageCreate, cause: err}
	}
	return n, nil
}

// Update 只更新可变投影列（repo/node_path/node_name/depth/实际校验和/
// 长度），WHERE node_id 且 node_type=1，不改 node_type（文件 Kind 守卫是
// 显式本地投影策略；目标通用 DAO 按 node_id 更新）。nil Metadata 是本地
// 旧调用方兼容策略：语句不触碰七列元数据（保持不变）；非 nil 在同一条
// 参数化 UPDATE 中原子替换全部七列（包括替换为 NULL）。缺失 ID 返回 0,nil，
// 绝不转创建；无重试/提交；本片不提供清空整个快照的操作。
// Update touches only the mutable projection columns (repo/node_path/
// node_name/depth/actual checksums/length) WHERE node_id AND node_type=1,
// never node_type (the file-kind guard is explicit local projection policy;
// the target generic DAO updates by node_id). A nil Metadata is the explicit
// legacy-caller policy: the statement leaves the seven metadata columns
// untouched (preserved); a nonnil Metadata atomically replaces all seven in
// the same parameterized UPDATE (including replacement with NULL). A missing
// ID returns 0,nil with no create, no retry and no commit; this slice adds
// no snapshot-clearing operation.
func (s *Store) Update(ctx context.Context, ref repository.FileReference) (int64, error) {
	depth, err := validateWriteRef(ref)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, &opError{op: stageCancelled, cause: err}
	}
	var query string
	args := []any{
		ref.ID, ref.Path.Repo(), dotIfEmpty(ref.Path.Parent()), dotIfEmpty(ref.Path.Name()),
		depth, ref.Content.Size, ref.Content.SHA1, ref.Content.MD5, ref.Content.SHA256,
	}
	if ref.Metadata == nil {
		query = "UPDATE nodes SET repo = $2, node_path = $3, node_name = $4, depth = $5, " +
			"bin_length = $6, sha1_actual = $7, md5_actual = $8, sha256 = $9 " +
			"WHERE node_id = $1 AND node_type = 1"
	} else {
		query = "UPDATE nodes SET repo = $2, node_path = $3, node_name = $4, depth = $5, " +
			"bin_length = $6, sha1_actual = $7, md5_actual = $8, sha256 = $9, " +
			"created = $10, modified = $11, updated = $12, created_by = $13, modified_by = $14, " +
			"sha1_original = $15, md5_original = $16 WHERE node_id = $1 AND node_type = 1"
		args = append(args, metadataArgs(ref.Metadata)...)
	}
	res, err := s.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, &opError{op: stageUpdate, cause: err}
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, &opError{op: stageUpdate, cause: err}
	}
	return n, nil
}

// Lookup 按 repo/parent/name 查询并忽略输入 Kind（对齐目标 NodesDao.get
// 的键），并追加七个元数据列。命中非文件行返回 ErrNotFile 与零引用（Kind
// 检查先于元数据处理）；缺失返回固定文案且 errors.Is(err, sql.ErrNoRows)
// 成立。元数据形状：七列全 NULL → Metadata nil（未知历史，区别于显式零值
// 快照）；created 与 modified 已知 → 返回独立分配的快照（updated NULL 映射
// 0，对齐所选目标读取器；actor 经 NewActor，original 恢复可空 UTF-16）；
// 其他部分形状返回有界 stageLookup 错误且保留 ErrInvalidReference，永不
// 回显行值。返回存储 ID、重构的文件 Path 与内容元数据，不做重算/回填；
// NULL MD5/SHA256 经 sql.NullString 映射为空，行内 NULL/空/'.' 组件归一为
// 空（对齐 BaseDao 的字面空/点边界，而非 Java-blank 折叠）。写入侧 Content
// 恒为规范值，但 Lookup 对存量行按原样返回（可为空校验和投影）；外键只
// 证明 SHA-1 已登记，不证明跨校验和/长度一致或物理完整性。
// Lookup queries by repo/parent/name (matching the target NodesDao.get
// keys) with the seven metadata columns appended. A stored non-file row
// yields ErrNotFile with a zero reference (the kind check precedes any
// metadata handling); a miss yields the fixed text with
// errors.Is(err, sql.ErrNoRows) true. Metadata shapes: all seven NULL → a
// nil Metadata (unknown history, distinct from an explicit zero snapshot);
// created and modified known → an independently allocated snapshot (a NULL
// updated maps to 0 like the selected target reader; actors pass through
// NewActor and originals rebuild as nullable UTF-16); any other partial
// shape returns the bounded stageLookup error preserving
// ErrInvalidReference and never echoing row values. It returns the stored
// ID, a reconstructed file Path and content metadata with no recompute or
// backfill; NULL MD5/SHA256 map to empty via sql.NullString and row
// NULL/empty/'.' components normalize to empty (the literal BaseDao
// empty/dot boundary, not a Java-blank collapse). Writes always carry
// canonical Content, but Lookup returns legacy rows as-is (possibly empty
// checksum projections); the FK proves only SHA-1 registration, never
// cross-checksum/length agreement or physical integrity.
func (s *Store) Lookup(ctx context.Context, path repository.Path) (repository.FileReference, error) {
	if err := validateIdentity(path); err != nil {
		return repository.FileReference{}, err
	}
	if err := ctx.Err(); err != nil {
		return repository.FileReference{}, &opError{op: stageCancelled, cause: err}
	}
	row := s.tx.QueryRowContext(ctx,
		"SELECT node_id, node_type, repo, node_path, node_name, bin_length, sha1_actual, md5_actual, sha256, "+
			"created, modified, updated, created_by, modified_by, sha1_original, md5_original "+
			"FROM nodes WHERE repo = $1 AND node_path = $2 AND node_name = $3",
		path.Repo(), dotIfEmpty(path.Parent()), dotIfEmpty(path.Name()))
	var id int64
	var nodeType int16
	var repo, nodePath, nodeName sql.NullString
	var binSHA1, binMD5, binSHA256 sql.NullString
	var length sql.NullInt64
	var sm storedMetadata
	if err := row.Scan(&id, &nodeType, &repo, &nodePath, &nodeName,
		&length, &binSHA1, &binMD5, &binSHA256,
		&sm.created, &sm.modified, &sm.updated,
		&sm.createdBy, &sm.modifiedBy, &sm.sha1Original, &sm.md5Original); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repository.FileReference{}, &opError{op: stageLookup, cause: sql.ErrNoRows}
		}
		return repository.FileReference{}, &opError{op: stageLookup, cause: err}
	}
	if nodeType != 1 {
		return repository.FileReference{}, &opError{op: stageLookup, cause: ErrNotFile}
	}
	stored, nerr := repository.NewPath(rowComponent(repo), rowComponent(nodePath),
		rowComponent(nodeName), true)
	if nerr != nil {
		return repository.FileReference{}, &opError{op: stageLookup,
			cause: fmt.Errorf("postgres nodes: stored path components: %w", ErrInvalidReference)}
	}
	meta, metaErr := sm.snapshot()
	if metaErr != nil {
		return repository.FileReference{}, &opError{op: stageLookup, cause: metaErr}
	}
	return repository.FileReference{
		ID:       id,
		Path:     stored,
		Content:  content.Metadata{SHA1: binSHA1.String, SHA256: binSHA256.String, MD5: binMD5.String, Size: length.Int64},
		Metadata: meta,
	}, nil
}

// validateWriteRef 及全部校验出口都返回 opError{stageInvalid}：公开
// Error() 恒为固定文案；带细节的诊断原因（字段/长度/深度哨兵）只存在于
// Unwrap 链下，供可信调用方排查，永不进入 Error()。
// validateWriteRef and every validation exit return opError{stageInvalid}:
// the public Error() stays the fixed constant; detailed diagnostic causes
// (field/length/depth sentinels) live only under Unwrap for trusted
// callers and never enter Error().
//
// validateWriteRef 在任何 SQL 之前拒绝非法写入：ID>0、非零且成功构造的
// Path、IsFile 且非根、父/名字面 '.' 排除（本地规范画像，避免与 SQL 点
// 哨兵互为别名）、Depth 上界（保留 repository.ErrDepthExceeded）与规范
// 内容前置（精确小写 ASCII 十六进制 40/64/32、Size>=0 —— 显式本地内容
// 管道前置，非目标畸形/存量对齐）。不做 Clean/TrimSpace/小写化/其他斜杠
// 归一，也不实现臆测的 PathValidator（上游 FileService 另行调用之）。
// validateWriteRef rejects invalid writes before any SQL: ID>0, a nonzero
// constructed Path, IsFile and non-root, literal '.' parent/name excluded
// (a local canonical profile avoiding aliasing with the SQL dot sentinel),
// the Depth bound (preserving repository.ErrDepthExceeded) and the canonical
// content precondition (exact lowercase ASCII hex 40/64/32, Size>=0 — an
// explicit local content-pipeline precondition, not target malformed/legacy
// parity). No Clean/TrimSpace/lowercasing/other-slash normalization and no
// guessed PathValidator (upstream FileService applies its own).
func validateWriteRef(ref repository.FileReference) (int16, error) {
	if err := validateIdentity(ref.Path); err != nil {
		return 0, err
	}
	if ref.ID <= 0 {
		return 0, invalidField("node id")
	}
	if !ref.Path.IsFile() {
		return 0, invalidField("kind")
	}
	if ref.Path.IsRoot() {
		return 0, invalidField("root")
	}
	depth, err := ref.Path.Depth()
	if err != nil {
		return 0, &opError{op: stageInvalid, cause: errors.Join(ErrInvalidReference, err)}
	}
	if err := validateContent(ref.Content); err != nil {
		return 0, err
	}
	return depth, nil
}

// validateIdentity 校验查询/写入共用的路径身份：非零、repo 非空与字面
// '.' 排除；查询侧允许 folder Kind 与根查询。
// validateIdentity covers the identity shared by writes and lookups:
// nonzero, nonempty repo and the literal '.' exclusion; folder-kind and
// root lookups stay permitted.
func validateIdentity(p repository.Path) error {
	if p == (repository.Path{}) {
		return invalidField("path")
	}
	if p.Repo() == "" {
		return invalidField("repo")
	}
	if p.Parent() == "." || p.Name() == "." {
		return invalidField("dot component")
	}
	return nil
}

func validateContent(meta content.Metadata) error {
	if err := canonicalHex("sha1", meta.SHA1, 40); err != nil {
		return err
	}
	if err := canonicalHex("sha256", meta.SHA256, 64); err != nil {
		return err
	}
	if err := canonicalHex("md5", meta.MD5, 32); err != nil {
		return err
	}
	if meta.Size < 0 {
		return invalidCause(fmt.Errorf("size (%d): %w", meta.Size, ErrInvalidReference))
	}
	return nil
}

// canonicalHex 只报字段与长度/位置，错误有界且永不回显值。
// canonicalHex reports only the field and length/position; errors stay
// bounded and never echo values.
func canonicalHex(field, s string, want int) error {
	if len(s) != want {
		return invalidCause(fmt.Errorf("%s (len %d): %w", field, len(s), ErrInvalidReference))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return invalidCause(fmt.Errorf("%s (bad hex at %d): %w", field, i, ErrInvalidReference))
		}
	}
	return nil
}

func invalidField(field string) error {
	return invalidCause(fmt.Errorf("%s: %w", field, ErrInvalidReference))
}

// invalidCause 把诊断细节固定在 Unwrap 链下，公开文案恒为 stageInvalid。
// invalidCause pins diagnostic detail under Unwrap; the public text stays
// stageInvalid.
func invalidCause(cause error) error {
	return &opError{op: stageInvalid, cause: cause}
}

// dotIfEmpty 仅把空串映射为 SQL '.' 哨兵；Unicode 空白原样保留。
// dotIfEmpty maps only the empty string to the SQL '.' sentinel; Unicode
// blanks are retained verbatim.
func dotIfEmpty(s string) string {
	if s == "" {
		return "."
	}
	return s
}

// rowComponent 把行内 NULL/空/'.' 组件归一为空；其余原样。
// rowComponent normalizes row NULL/empty/'.' components to empty;
// everything else stays verbatim.
func rowComponent(ns sql.NullString) string {
	if !ns.Valid || ns.String == "" || ns.String == "." {
		return ""
	}
	return ns.String
}

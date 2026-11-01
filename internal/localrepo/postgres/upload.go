// Package postgres 提供普通上传的懒 PostgreSQL 提交保存缝：正文发布与登记
// 完成后，才在首次节点查询处开启有界节点事务并在提交成功后返回 Source。
// Package postgres provides the lazy committed PostgreSQL save seam for the
// ordinary upload: the bounded node transaction starts at the first node
// lookup only after body publication and registration, and a Source returns
// only after an explicitly successful commit.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/localrepo"
	"github.com/0ldlight/bf/internal/repository"
	nodepg "github.com/0ldlight/bf/internal/repository/postgres"
)

// 构造器固定安全错误文案；不含任何输入。
// Fixed safe constructor texts; no input is ever echoed.
var (
	errUploadNilDB        = errors.New("postgres upload: nil database dependency")
	errUploadNilPublisher = errors.New("postgres upload: nil publisher dependency")
	errUploadNilClock     = errors.New("postgres upload: nil clock dependency")
	errUploadNodeTimeout  = errors.New("postgres upload: unsupported node timeout")
	errUploadDBPool       = errors.New("postgres upload: database max open connections must be at least two")
)

// 固定阶段文案常量：Error() 只输出操作归因，原因仅经 Unwrap 供可信调用方。
// Fixed stage texts: Error() prints only the operation attribution and
// causes stay reachable through Unwrap for trusted callers.
const (
	opUploadNilContext          = "postgres upload: nil context"
	opUploadNilBody             = "postgres upload: nil body"
	opUploadSaveCancelled       = "postgres upload: save cancelled"
	opUploadClockRange          = "postgres upload: clock sample out of supported range"
	opUploadRollbackFailed      = "postgres upload: rollback failed"
	opUploadParentAfterCommit   = "postgres upload: parent cancelled after commit"
	opUploadResultInvalid       = "postgres upload: invalid node save result"
	opUploadIdentityMismatch    = "postgres upload: authoritative identity mismatch"
	opUploadContentMismatch     = "postgres upload: authoritative content mismatch"
	opUploadSizeMismatch        = "postgres upload: declared size mismatch"
	opUploadSourceInvalid       = "postgres upload: authoritative source invalid"
	opUploadAuthoritativeLookup = "postgres upload: authoritative lookup"
	opUploadCommit              = "postgres upload: node commit"
)

// uploadMaxNodeTimeout 是节点阶段（排队/事务/分配/提交窗口）的上界。
// uploadMaxNodeTimeout bounds the node phase (queue/transaction/allocation/
// commit window).
const uploadMaxNodeTimeout = time.Minute

// UploadDeps 汇集调用方拥有的真实依赖：*sql.DB、binary.New 产生的 Publisher、
// 并发安全的 Clock 与节点阶段期限。typed nil 不受支持；调用方拥有生命周期。
// UploadDeps gathers caller-owned real dependencies: the *sql.DB, the
// Publisher from binary.New, a concurrency-safe Clock and the node-phase
// deadline. Typed nils are unsupported; the caller owns all lifetimes.
type UploadDeps struct {
	DB          *sql.DB
	Publisher   *binary.Service
	Clock       repository.FileSaveClock
	NodeTimeout time.Duration
}

// UploadSaver 串行化有界节点阶段；正文发布从不占用节点 gate。一个写入方
// 一个实例；跨进程写入与 HA 不在本切片。
// UploadSaver serializes only the bounded node phase; body publication never
// takes the node gate. One instance per writer process; cross-process
// writers and HA stay out of this slice.
type UploadSaver struct {
	db           *sql.DB
	publisher    *binary.Service
	clock        repository.FileSaveClock
	nodeTimeout  time.Duration
	catalog      repository.FileMimeCatalog
	reservations *nodepg.Reservations
	gate         chan struct{}
}

// NewUploadSaver 拒绝 nil 依赖、期限 <=0 或 >1 分钟、MaxOpenConnections<2
// （含无限 0，要求显式有界池）；不做 DDL、Ping、Close 或连接创建。
// NewUploadSaver rejects nil dependencies, deadlines <=0 or >1 minute and
// MaxOpenConnections<2 (unlimited 0 included; an explicit bounded pool is
// required); it performs no DDL, Ping, Close or connection creation.
func NewUploadSaver(deps UploadDeps) (*UploadSaver, error) {
	if deps.DB == nil {
		return nil, errUploadNilDB
	}
	if deps.Publisher == nil {
		return nil, errUploadNilPublisher
	}
	if deps.Clock == nil {
		return nil, errUploadNilClock
	}
	if deps.NodeTimeout <= 0 || deps.NodeTimeout > uploadMaxNodeTimeout {
		return nil, errUploadNodeTimeout
	}
	if deps.DB.Stats().MaxOpenConnections < 2 {
		return nil, errUploadDBPool
	}
	catalog, err := repository.NewFileMimeCatalog([]repository.FileMimeDefinition{
		{Type: "application/octet-stream", Extensions: "bin"},
		{Type: "text/plain", Extensions: "txt"},
	}, repository.FileMimeCaseRootASCII)
	if err != nil {
		return nil, &uploadOpError{op: "postgres upload: fixed mime catalog invalid", cause: err}
	}
	reservations, err := nodepg.NewReservations(deps.DB)
	if err != nil {
		return nil, &uploadOpError{op: "postgres upload: id reservations unavailable", cause: err}
	}
	return &UploadSaver{
		db: deps.DB, publisher: deps.Publisher, clock: deps.Clock,
		nodeTimeout: deps.NodeTimeout, catalog: catalog,
		reservations: reservations, gate: make(chan struct{}, 1),
	}, nil
}

// copyUploadUnits 返回防御性副本；nil 保持 nil。
// copyUploadUnits returns a defensive copy; nil stays nil.
func copyUploadUnits(units []uint16) []uint16 {
	if units == nil {
		return nil
	}
	out := make([]uint16, len(units))
	copy(out, units)
	return out
}

// Save 校验父 ctx/输入/正文后，在发布前采样一次时钟盖戳，调用真实 SaveFile
// （CLIENT 策略、Verify 预检、PreserveCreated、Invariant、固定目录与懒节点
// 事务），成功后同事务重读权威 Source，校验一致才提交一次；仅显式提交成功
// 且父 ctx 仍存活时返回 Source。失败零 Source；回滚故障使外部错误成为
// ErrUploadPersistence 优先并保留全部原因；不关闭 reader，不补偿，不重试。
//
// Save validates the parent ctx/input/body, stamps one clock sample before
// publication, calls the real SaveFile (CLIENT policy, Verify precheck,
// PreserveCreated, Invariant, the fixed catalog and the lazy node
// transaction), re-reads the authoritative Source inside the same
// transaction on success, validates agreement and commits exactly once; a
// Source returns only for an explicit commit success with the parent still
// live. Failures return the zero Source; an independent rollback fault makes
// the outer error ErrUploadPersistence-first while preserving every cause;
// the reader is never closed and nothing is compensated or retried.
func (s *UploadSaver) Save(ctx context.Context, body io.Reader, in localrepo.UploadInput) (source repository.FileSnapshotSource, err error) {
	if ctx == nil {
		return repository.FileSnapshotSource{}, uploadInputErr(opUploadNilContext)
	}
	if err := ctx.Err(); err != nil {
		return repository.FileSnapshotSource{}, &uploadOpError{op: opUploadSaveCancelled, cause: err}
	}
	if err := localrepo.ValidateUploadInput(in); err != nil {
		return repository.FileSnapshotSource{}, err
	}
	if body == nil {
		return repository.FileSnapshotSource{}, uploadInputErr(opUploadNilBody)
	}
	stampMillis := s.clock.NowMillis()
	if stampMillis < 0 || stampMillis > localrepo.MaxUploadMillis {
		return repository.FileSnapshotSource{}, uploadPersistErr(opUploadClockRange, nil)
	}
	checksums := repository.ClientFileChecksums{
		SHA1Original:   copyUploadUnits(in.Checksums.SHA1Original),
		SHA256Original: copyUploadUnits(in.Checksums.SHA256Original),
		MD5Original:    copyUploadUnits(in.Checksums.MD5Original),
	}
	operator := copyUploadUnits(in.OperatorUTF16)
	declared := in.DeclaredSize
	lazy := &lazyNodeStore{saver: s}
	defer func() {
		lazy.finish()
		if lazy.rollbackFault != nil {
			source = repository.FileSnapshotSource{}
			err = &uploadOpError{op: opUploadRollbackFailed,
				cause: errors.Join(localrepo.ErrUploadPersistence, err, lazy.rollbackFault)}
		}
	}()
	result, saveErr := localrepo.SaveFile(ctx, body, localrepo.FileInput{
		Repo: in.Path.Repo(), Path: in.Path.PathName(), Checksums: checksums,
		ResourceCreatedMillis: stampMillis, ResourceModifiedMillis: stampMillis,
		CurrentUserUTF16: operator, CreatedPolicy: repository.PreserveLoadedFileCreated,
		OriginalCase:   repository.LoadedOriginalCaseInvariant,
		ChecksumPolicy: repository.LocalFileChecksumClient,
		Precheck:       localrepo.VerifyChecksumPrecheck, DeclaredSize: &declared,
	}, localrepo.FileDependencies{Publisher: s.publisher, Node: repository.LocalFileSaveDependencies{
		Store: lazy, NextID: lazy.nextID, Clock: s.clock, Catalog: s.catalog,
	}})
	if saveErr != nil {
		return repository.FileSnapshotSource{}, saveErr
	}
	if err := validateSavedResult(&result.Save, lazy); err != nil {
		return repository.FileSnapshotSource{}, err
	}
	reread, rerr := lazy.lookupAuthoritative(ctx, in.Path)
	if rerr != nil {
		return repository.FileSnapshotSource{}, rerr
	}
	if reread.Reference.ID != result.Save.ID {
		return repository.FileSnapshotSource{}, uploadPersistErr(opUploadIdentityMismatch, nil)
	}
	if reread.Reference.Content != result.Published {
		return repository.FileSnapshotSource{}, uploadPersistErr(opUploadContentMismatch, nil)
	}
	if result.Published.Size != declared {
		return repository.FileSnapshotSource{}, uploadPersistErr(opUploadSizeMismatch, nil)
	}
	if verr := localrepo.ValidateUploadSource(reread, in.Path); verr != nil {
		return repository.FileSnapshotSource{}, uploadPersistErr(opUploadSourceInvalid, verr)
	}
	if cerr := lazy.commit(); cerr != nil {
		return repository.FileSnapshotSource{}, cerr
	}
	if perr := ctx.Err(); perr != nil {
		return repository.FileSnapshotSource{}, &uploadOpError{op: opUploadParentAfterCommit, cause: perr}
	}
	return reread, nil
}

// validateSavedResult 拒绝非正 ID、Created/Changed 非 1 行、skip 非 0 行，
// 以及 Created 结果未精确等于本次预留首 ID 的情形。
// validateSavedResult rejects a nonpositive ID, Created/Changed results with
// a count other than one, skips with a nonzero count, and any Created result
// not exactly equal to this save's reserved first ID.
func validateSavedResult(result *repository.LocalFileSaveResult, lazy *lazyNodeStore) error {
	if result.ID <= 0 {
		return uploadPersistErr(opUploadResultInvalid, nil)
	}
	switch {
	case result.Created:
		if !lazy.reservedOK || result.ID != lazy.reserved || result.Count != 1 {
			return uploadPersistErr(opUploadResultInvalid, nil)
		}
	case result.Changed:
		if result.Count != 1 {
			return uploadPersistErr(opUploadResultInvalid, nil)
		}
	default:
		if result.Count != 0 {
			return uploadPersistErr(opUploadResultInvalid, nil)
		}
	}
	return nil
}

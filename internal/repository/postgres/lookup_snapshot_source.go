package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/0ldlight/bf/internal/content"
	"github.com/0ldlight/bf/internal/repository"
)

// lookupFile 执行两个读取出口共用的唯一一次 SELECT/Scan 与全部既有守卫
// （identity→ctx→查询→文件 Kind→存储 Path→元数据形状）；列清单、参数、
// 错误阶段、固定文案与原因链保持旧 Lookup 原样，无第二次查询。
// lookupFile performs the single shared SELECT/Scan plus every existing
// guard of both read exits (identity→ctx→query→file kind→stored Path→
// metadata shape); the column list, parameters, error stages, fixed texts
// and cause chains stay exactly as in the old Lookup, with no second query.
func (s *Store) lookupFile(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	if err := validateIdentity(path); err != nil {
		return repository.FileSnapshotSource{}, err
	}
	if err := ctx.Err(); err != nil {
		return repository.FileSnapshotSource{}, &opError{op: stageCancelled, cause: err}
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
			return repository.FileSnapshotSource{}, &opError{op: stageLookup, cause: sql.ErrNoRows}
		}
		return repository.FileSnapshotSource{}, &opError{op: stageLookup, cause: err}
	}
	if nodeType != 1 {
		return repository.FileSnapshotSource{}, &opError{op: stageLookup, cause: ErrNotFile}
	}
	stored, nerr := repository.NewPath(rowComponent(repo), rowComponent(nodePath),
		rowComponent(nodeName), true)
	if nerr != nil {
		return repository.FileSnapshotSource{}, &opError{op: stageLookup,
			cause: fmt.Errorf("postgres nodes: stored path components: %w", ErrInvalidReference)}
	}
	meta, metaErr := sm.snapshot()
	if metaErr != nil {
		return repository.FileSnapshotSource{}, &opError{op: stageLookup, cause: metaErr}
	}
	return repository.FileSnapshotSource{
		Reference: repository.FileReference{
			ID:       id,
			Path:     stored,
			Content:  content.Metadata{SHA1: binSHA1.String, SHA256: binSHA256.String, MD5: binMD5.String, Size: length.Int64},
			Metadata: meta,
		},
		SHA1ActualValid:   binSHA1.Valid,
		MD5ActualValid:    binMD5.Valid,
		SHA256ActualValid: binSHA256.Valid,
	}, nil
}

// LookupSnapshotSource 在旧 Lookup 的同一读取语义上追加返回三个 Actual 的
// SQL 空性位（来自同一 Scan 的 NullString.Valid，空串计为有效）；任一错误
// 返回零 Source 与原有原因链，不额外包裹或重分类。
// LookupSnapshotSource returns the old Lookup read plus the three Actual
// SQL nullability bits taken from the same Scan's NullString.Valid (an
// empty string counts as valid); any error returns the zero Source with the
// original cause chain, never rewrapped or reclassified.
func (s *Store) LookupSnapshotSource(ctx context.Context, path repository.Path) (repository.FileSnapshotSource, error) {
	return s.lookupFile(ctx, path)
}

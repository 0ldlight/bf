package process

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/0ldlight/bf/internal/binary"
	"github.com/0ldlight/bf/internal/filestore"
	uploadpg "github.com/0ldlight/bf/internal/localrepo/postgres"
	registry "github.com/0ldlight/bf/internal/registry/postgres"
	nodepg "github.com/0ldlight/bf/internal/repository/postgres"
	"github.com/0ldlight/bf/internal/security"
)

var commitSchemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

const uploadReadTimeout = time.Minute

type uploadClock struct{}

func (uploadClock) NowMillis() int64 { return time.Now().UnixMilli() }

func sameUploadDatabase(a, b *pgx.ConnConfig, schema string) bool {
	return a.Host == b.Host && a.Port == b.Port && a.Database == b.Database &&
		b.RuntimeParams["search_path"] == schema
}

func openUpload(ctx context.Context, cfg Config, reader *pgx.ConnConfig, nodes *nodepg.ReadPool, fs *filestore.Store) (http.Handler, func() error, error) {
	writer, err := parsePostgresDSN(cfg.Upload.WriterDSN)
	if err != nil {
		return nil, nil, err
	}
	allocator, err := parsePostgresDSN(cfg.Upload.ReservationDSN)
	if err != nil {
		return nil, nil, err
	}
	schema := reader.RuntimeParams["search_path"]
	if !commitSchemaName.MatchString(schema) || strings.HasPrefix(schema, "pg_") ||
		!sameUploadDatabase(reader, writer, schema) || !sameUploadDatabase(reader, allocator, schema) ||
		reader.User == writer.User || reader.User == allocator.User || writer.User == allocator.User {
		return nil, nil, ErrConfiguration
	}
	writerDB := stdlib.OpenDB(*writer)
	writerDB.SetMaxOpenConns(poolMaxOpen)
	writerDB.SetMaxIdleConns(poolMaxIdle)
	writerDB.SetConnMaxLifetime(poolMaxLifetime)
	reservationDB := stdlib.OpenDB(*allocator)
	reservationDB.SetMaxOpenConns(1)
	reservationDB.SetMaxIdleConns(1)
	reservationDB.SetConnMaxLifetime(poolMaxLifetime)
	closePools := func() error { return errors.Join(reservationDB.Close(), writerDB.Close()) }
	handler, err := buildUpload(ctx, cfg, schema, writerDB, reservationDB, nodes, fs)
	if err != nil {
		if closePools() != nil {
			// Startup reports only fixed categories and never the database error.
			return nil, nil, errors.Join(err, ErrResources)
		}
		return nil, nil, err
	}
	return handler, closePools, nil
}

func buildUpload(ctx context.Context, cfg Config, schema string, writerDB, reservationDB *sql.DB, nodes *nodepg.ReadPool, fs *filestore.Store) (http.Handler, error) {
	if err := checkUploadWriter(ctx, writerDB, schema); err != nil {
		return nil, err
	}
	reservations, err := nodepg.NewReservations(reservationDB)
	if err != nil {
		return nil, err
	}
	if _, err := reservations.Current(ctx); err != nil {
		return nil, err
	}
	var canReserve bool
	if err := reservationDB.QueryRowContext(ctx, `
SELECT pg_catalog.current_schema() = $1::text
   AND pg_catalog.has_table_privilege('unique_ids', 'UPDATE')
   AND pg_catalog.current_setting('transaction_read_only') = 'off'`, schema).Scan(&canReserve); err != nil || !canReserve {
		return nil, ErrStartup
	}
	reg, err := registry.New(writerDB)
	if err != nil {
		return nil, err
	}
	publisher, err := binary.New(fs, reg)
	if err != nil {
		return nil, err
	}
	saver, err := uploadpg.NewUploadSaver(uploadpg.UploadDeps{
		DB: writerDB, ReservationDB: reservationDB, CommitSchema: schema,
		Publisher: publisher, Clock: uploadClock{}, NodeTimeout: operationTimeout,
	})
	if err != nil {
		return nil, err
	}
	backend, err := security.NewStore(writerDB, operationTimeout)
	if err != nil {
		return nil, err
	}
	return security.NewAuthenticatedUpload(ctx, security.UploadHTTPConfig{
		HTTPConfig: cfg.HTTP, MaxBodyBytes: cfg.Upload.MaxBodyBytes,
		PublicOrigin: cfg.Upload.PublicOrigin,
	}, backend, nodes, saver)
}

func checkUploadWriter(ctx context.Context, db *sql.DB, schema string) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var supported bool
	err = tx.QueryRowContext(ctx, `
SELECT pg_catalog.current_schema() = $1::text
   AND pg_catalog.has_table_privilege('security_write_grants', 'SELECT')
   AND pg_catalog.has_table_privilege('security_group_members', 'SELECT')
   AND pg_catalog.has_table_privilege('nodes', 'SELECT')
   AND pg_catalog.has_table_privilege('nodes', 'INSERT')
   AND pg_catalog.has_table_privilege('nodes', 'UPDATE')
   AND pg_catalog.has_table_privilege('binaries', 'SELECT')
   AND pg_catalog.has_table_privilege('binaries', 'INSERT')
   AND pg_catalog.has_column_privilege('binaries', 'sha256', 'UPDATE')
   AND pg_catalog.current_setting('transaction_read_only') = 'off'`, schema).Scan(&supported)
	if err != nil || !supported {
		return ErrStartup
	}
	if _, err := tx.ExecContext(ctx, `SELECT "`+schema+`".lock_upload_commit()`); err != nil {
		return err
	}
	return tx.Rollback()
}

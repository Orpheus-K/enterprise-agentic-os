package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrator owns a separate privileged connection; never pass it to request handlers.
type Migrator struct {
	db       *sql.DB
	provider *goose.Provider
}

func NewMigrator(ctx context.Context, url string) (*Migrator, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	return newMigrator(ctx, url, files)
}

func newMigrator(ctx context.Context, url string, files fs.FS) (*Migrator, error) {
	if url == "" {
		return nil, errors.New("migration database URL is required")
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid migration database configuration")
	}
	config.RuntimeParams["role"] = "agentos_migrator"
	config.RuntimeParams["search_path"] = "public,pg_catalog"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	closeOnError := func(err error) (*Migrator, error) { return nil, errors.Join(err, db.Close()) }
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return closeOnError(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return closeOnError(err)
	}
	return &Migrator{db: db, provider: provider}, nil
}

func (m *Migrator) Up(ctx context.Context) (int, error) {
	results, err := m.provider.Up(ctx)
	return len(results), err
}

func (m *Migrator) Close() error { return m.db.Close() }

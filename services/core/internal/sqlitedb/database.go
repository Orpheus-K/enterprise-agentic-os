// Package sqlitedb provides the file database foundation for the lightweight
// driver. It does not provide row-level security; domain repositories own scope.
package sqlitedb

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 8

//go:embed migrations/*.sql
var migrations embed.FS

func connect(ctx context.Context, path, mode string) (*sql.DB, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return nil, errors.New("SQLite requires an absolute file path")
	}
	uriPath := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := url.Values{"mode": {mode}, "_txlock": {"immediate"}, "_pragma": {"foreign_keys(1)", "busy_timeout(5000)"}}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// Open requires an existing, explicitly migrated database. Callers are trusted
// domain repositories, never request handlers or untrusted SQL consumers.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := connect(ctx, path, "rw")
	if err != nil {
		return nil, err
	}
	var version int
	err = db.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1").Scan(&version)
	if err == nil && version != SchemaVersion {
		err = fmt.Errorf("unsupported SQLite schema version %d (require %d)", version, SchemaVersion)
	}
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// Migrate is an offline operation: stop application processes before invoking it.
// SQLite versions are independent of PostgreSQL migrations.
func Migrate(ctx context.Context, path string) (err error) {
	db, err := connect(ctx, path, "rwc")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	if version > SchemaVersion {
		return fmt.Errorf("unsupported SQLite schema version %d", version)
	}
	_, err = provider.Up(ctx)
	return err
}

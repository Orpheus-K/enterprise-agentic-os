package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Queries deliberately excludes Commit/Rollback: transaction ownership stays here.
type Queries interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// SchemaVersion is the only migration version this build serves; startup and readiness share it.
const SchemaVersion = 21

type DB struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string, maxConnections int32) (*DB, error) {
	if url == "" || maxConnections < 1 {
		return nil, errors.New("database URL and positive connection limit are required")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid runtime database configuration")
	}
	config.MaxConns = maxConnections
	config.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		var safe bool
		err := connection.QueryRow(ctx, `
            SELECT current_user = session_user
              AND pg_has_role(current_user, 'agentos_runtime', 'MEMBER')
              AND NOT pg_has_role(current_user, 'agentos_migrator', 'MEMBER')
              AND NOT EXISTS (
                SELECT 1 FROM pg_roles r
                WHERE pg_has_role(current_user, r.oid, 'MEMBER')
                  AND (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication)
              )
              AND NOT EXISTS (
                SELECT 1 FROM pg_database d WHERE d.datname = current_database()
                  AND pg_has_role(current_user, d.datdba, 'MEMBER')
              )
              AND NOT EXISTS (
                SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname IN ('iam','channel','knowledge') AND pg_has_role(current_user, c.relowner, 'MEMBER')
              )`).Scan(&safe)
		if err != nil {
			return err
		}
		if !safe {
			return errors.New("runtime database role must be unprivileged and separate from ownership/migrations")
		}
		var version int64
		if err := connection.QueryRow(ctx, `SELECT coalesce(max(version_id), 0) FROM public.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
			return err
		}
		if version != SchemaVersion {
			return fmt.Errorf("unsupported database schema version: %d", version)
		}
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() { d.pool.Close() }

// Transaction is an internal storage primitive, not authorization. Call identity.Within
// at the application boundary. Callbacks must not retain Queries or perform network IO.
func (d *DB) Transaction(ctx context.Context, fn func(Queries) error) (err error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rollbackErr := tx.Rollback(cleanup)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	// Always reset all context fields locally, even on a reused pooled session.
	_, err = tx.Exec(ctx, `SELECT
        set_config('agentos.tenant_id', '', true), set_config('agentos.member_id', '', true),
        set_config('agentos.provider', '', true), set_config('agentos.issuer', '', true),
        set_config('agentos.subject', '', true), set_config('agentos.app_id', '', true)`)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

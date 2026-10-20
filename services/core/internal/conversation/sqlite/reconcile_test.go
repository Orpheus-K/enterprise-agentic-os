package sqlite

import (
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation/contracttest"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func TestReconciliationContract(t *testing.T) {
	contracttest.RunReconciliation(t, func(t *testing.T) contracttest.ReconcileFixture {
		ctx := t.Context()
		path := filepath.Join(t.TempDir(), "tasks.db")
		if err := sqlitedb.Migrate(ctx, path); err != nil {
			t.Fatal(err)
		}
		db, err := sqlitedb.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return contracttest.ReconcileFixture{
			New: func(tenant uuid.UUID, app, revision string) contracttest.RecoveryStore {
				connection, err := sqlitedb.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { connection.Close() })
				return NewInbox(connection, tenant, app, revision)
			},
			Exec: func(query string, args ...any) error {
				_, err := db.ExecContext(ctx, strings.ReplaceAll(strings.ReplaceAll(query, "iam.", "iam_"), "channel.", "channel_"), args...)
				return err
			},
			Expire: func(id uuid.UUID) error {
				_, err := db.ExecContext(ctx, "UPDATE channel_tasks SET lease_until=unixepoch('subsec')-1 WHERE id=?", id.String())
				return err
			},
			Ledgers: func(id uuid.UUID) (count int, err error) {
				err = db.QueryRowContext(ctx, "SELECT count(*) FROM channel_reconciliations WHERE task_id=?", id.String()).Scan(&count)
				return
			},
			Fail: func(table string) error {
				switch table {
				case "audit", "audit_reads", "reconciliations":
				default:
					t.Fatal("invalid fault table")
				}
				_, err := db.ExecContext(ctx, "CREATE TRIGGER fail_write BEFORE INSERT ON channel_"+table+" BEGIN SELECT RAISE(ABORT,'test write failure'); END")
				return err
			},
		}
	})
}

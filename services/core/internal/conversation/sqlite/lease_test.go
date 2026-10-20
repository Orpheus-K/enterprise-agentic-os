package sqlite

import (
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation/contracttest"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func TestLeaseContract(t *testing.T) {
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
	contracttest.RunLeases(t, contracttest.LeaseFixture{
		New: func(tenant uuid.UUID, app, revision string) contracttest.Leases {
			connection, err := sqlitedb.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { connection.Close() })
			return NewInbox(connection, tenant, app, revision)
		},
		Exec: func(query string, args ...any) error {
			_, err := db.ExecContext(ctx, strings.ReplaceAll(query, "iam.", "iam_"), args...)
			return err
		},
		Expire: func(id uuid.UUID) error {
			_, err := db.ExecContext(ctx, "UPDATE channel_tasks SET lease_until=unixepoch('subsec')-1 WHERE id=?", id.String())
			return err
		},
		Probe: func(id uuid.UUID) (state string, version int64, audits int, err error) {
			err = db.QueryRowContext(ctx, "SELECT state,step_version,(SELECT count(*) FROM channel_audit WHERE task_id=?) FROM channel_tasks WHERE id=?", id.String(), id.String()).Scan(&state, &version, &audits)
			return
		},
		FailAudit: func() error {
			_, err := db.ExecContext(ctx, "CREATE TRIGGER fail_audit BEFORE INSERT ON channel_audit BEGIN SELECT RAISE(ABORT,'test audit failure'); END")
			return err
		},
	})
}

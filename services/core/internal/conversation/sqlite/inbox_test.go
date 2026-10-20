package sqlite

import (
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation/contracttest"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func TestInboxContract(t *testing.T) {
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
	contracttest.RunInbox(t, contracttest.InboxFixture{
		New: func(tenant uuid.UUID, app, revision string) contracttest.Inbox {
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
		Counts: func() (tasks, audits int, err error) {
			err = db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM channel_tasks),(SELECT count(*) FROM channel_audit)").Scan(&tasks, &audits)
			return
		},
		Read: func(tenant uuid.UUID, app, event string) (revision, prompt, state string, err error) {
			err = db.QueryRowContext(ctx, "SELECT model_revision,prompt,state FROM channel_tasks WHERE tenant_id=? AND app_id=? AND event_id=?", tenant.String(), app, event).Scan(&revision, &prompt, &state)
			return
		},
		FailAudit: func() error {
			_, err := db.ExecContext(ctx, "CREATE TRIGGER fail_audit BEFORE INSERT ON channel_audit BEGIN SELECT RAISE(ABORT,'test audit failure'); END")
			return err
		},
	})
}

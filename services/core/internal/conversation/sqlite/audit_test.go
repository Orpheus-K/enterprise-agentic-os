package sqlite

import (
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation/contracttest"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func TestAuditContract(t *testing.T) {
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
	contracttest.RunAudit(t, contracttest.AuditFixture{
		New: func(tenant uuid.UUID, app, revision string) contracttest.AuditStore {
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
		Receipts: func(tenant, member uuid.UUID) (count, returned, wrong int, err error) {
			err = db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(row_count),0),coalesce(sum(CASE WHEN member_id<>? THEN 1 ELSE 0 END),0) FROM channel_audit_reads WHERE tenant_id=?", member.String(), tenant.String()).Scan(&count, &returned, &wrong)
			return
		},
		FailReads: func() error {
			_, err := db.ExecContext(ctx, "CREATE TRIGGER fail_audit BEFORE INSERT ON channel_audit_reads BEGIN SELECT RAISE(ABORT,'test audit failure'); END")
			return err
		},
	})
}

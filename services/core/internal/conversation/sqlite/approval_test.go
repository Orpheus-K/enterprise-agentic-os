package sqlite

import (
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/conversation/contracttest"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func TestApprovalContract(t *testing.T) {
	contracttest.RunApprovals(t, func(t *testing.T) contracttest.ApprovalFixture {
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
		return contracttest.ApprovalFixture{
			New: func(tenant uuid.UUID, app, revision string, policy *conversation.ApprovalPolicy) (contracttest.Approvals, error) {
				connection, err := sqlitedb.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { connection.Close() })
				store := NewInbox(connection, tenant, app, revision)
				if policy != nil {
					return store.WithApproval(*policy)
				}
				return store, nil
			},
			Exec: func(query string, args ...any) error {
				query = strings.ReplaceAll(strings.ReplaceAll(query, "iam.", "iam_"), "channel.", "channel_")
				_, err := db.ExecContext(ctx, query, args...)
				return err
			},
			Expire: func(id uuid.UUID) error {
				_, err := db.ExecContext(ctx, "UPDATE channel_approvals SET expires_at=unixepoch('subsec')-1 WHERE task_id=?", id.String())
				return err
			},
			FailAudit: func() error {
				_, err := db.ExecContext(ctx, "CREATE TRIGGER fail_audit BEFORE INSERT ON channel_audit BEGIN SELECT RAISE(ABORT,'test audit failure'); END")
				return err
			},
			FailReadAudit: func() error {
				_, err := db.ExecContext(ctx, "CREATE TRIGGER fail_reads BEFORE INSERT ON channel_audit_reads BEGIN SELECT RAISE(ABORT,'test read failure'); END")
				return err
			},
		}
	})
}

package sqlite

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/conversation/contracttest"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func TestReportContract(t *testing.T) {
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
	contracttest.RunReports(t, contracttest.ReportFixture{
		New: func(tenant uuid.UUID, app, revision string) contracttest.ReportStore {
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
		Receipts: func(tenant, member uuid.UUID) (count, returned, wrong int, err error) {
			err = db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(row_count),0),coalesce(sum(CASE WHEN member_id<>? THEN 1 ELSE 0 END),0) FROM channel_audit_reads WHERE tenant_id=?", member.String(), tenant.String()).Scan(&count, &returned, &wrong)
			return
		},
		SeedApproval: func(tenant, task, member uuid.UUID) error {
			_, err := db.ExecContext(ctx, `INSERT INTO channel_approvals(tenant_id,task_id,id,member_id,revision,content_hash,waiting_version,expires_at,status) VALUES($1,$2,$3,$4,'revision','hash',1,unixepoch('subsec')+3600,'pending')`, tenant.String(), task.String(), uuid.New().String(), member.String())
			return err
		},
		FailReads: func() error {
			_, err := db.ExecContext(ctx, "CREATE TRIGGER fail_audit BEFORE INSERT ON channel_audit_reads BEGIN SELECT RAISE(ABORT,'test audit failure'); END")
			return err
		},
	})
}

func TestReportBound(t *testing.T) {
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		delta time.Duration
		want  string
	}{{0, "2026-10-20T00:00:00.000Z"}, {time.Nanosecond, "2026-10-20T00:00:00.001Z"}, {time.Millisecond, "2026-10-20T00:00:00.001Z"}, {-time.Nanosecond, "2026-10-20T00:00:00.000Z"}} {
		if got := reportBound(base.Add(test.delta)); got != test.want {
			t.Fatalf("bound %s: %s", test.delta, got)
		}
	}
}

//go:build integration

package feishu

import (
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/conversation/contracttest"
	"github.com/google/uuid"
)

func TestPostgresAuditContract(t *testing.T) {
	f := chain(t)
	ctx := t.Context()
	contracttest.RunAudit(t, contracttest.AuditFixture{
		New: func(tenant uuid.UUID, app, revision string) contracttest.AuditStore {
			return conversation.NewStore(f.db, tenant, app, revision)
		},
		Exec: func(query string, args ...any) error {
			_, err := f.pg.Admin.Exec(ctx, query, args...)
			return err
		},
		Receipts: func(tenant, member uuid.UUID) (count, returned, wrong int, err error) {
			err = f.pg.Admin.QueryRow(ctx, "SELECT count(*),coalesce(sum(row_count),0),coalesce(sum(CASE WHEN member_id<>$2 THEN 1 ELSE 0 END),0) FROM channel.audit_reads WHERE tenant_id=$1", tenant, member).Scan(&count, &returned, &wrong)
			return
		},
		FailReads: func() error {
			_, err := f.pg.Admin.Exec(ctx, `CREATE FUNCTION channel.test_fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'test audit failure'; END $$;
CREATE TRIGGER fail_audit BEFORE INSERT ON channel.audit_reads FOR EACH ROW EXECUTE FUNCTION channel.test_fail_audit();`)
			return err
		},
	})
}

//go:build integration

package feishu

import (
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/conversation/contracttest"
	"github.com/google/uuid"
)

func TestPostgresApprovalContract(t *testing.T) {
	contracttest.RunApprovals(t, func(t *testing.T) contracttest.ApprovalFixture {
		f := chain(t)
		ctx := t.Context()
		return contracttest.ApprovalFixture{
			New: func(tenant uuid.UUID, app, revision string, policy *conversation.ApprovalPolicy) (contracttest.Approvals, error) {
				store := conversation.NewStore(f.db, tenant, app, revision)
				if policy != nil {
					return store.WithApproval(*policy)
				}
				return store, nil
			},
			Exec: func(query string, args ...any) error {
				_, err := f.pg.Admin.Exec(ctx, query, args...)
				return err
			},
			Expire: func(id uuid.UUID) error {
				_, err := f.pg.Admin.Exec(ctx, "UPDATE channel.approvals SET expires_at=clock_timestamp()-interval '1 second' WHERE task_id=$1", id)
				return err
			},
			FailAudit: func() error {
				_, err := f.pg.Admin.Exec(ctx, `CREATE FUNCTION channel.test_fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'test audit failure'; END $$;
CREATE TRIGGER fail_audit BEFORE INSERT ON channel.audit FOR EACH ROW EXECUTE FUNCTION channel.test_fail_audit();`)
				return err
			},
			FailReadAudit: func() error {
				_, err := f.pg.Admin.Exec(ctx, `CREATE FUNCTION channel.test_fail_reads() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test read failure'; END $$;
CREATE TRIGGER fail_reads BEFORE INSERT ON channel.audit_reads FOR EACH ROW EXECUTE FUNCTION channel.test_fail_reads();`)
				return err
			},
		}
	})
}

//go:build integration

package feishu

import (
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/conversation/contracttest"
	"github.com/google/uuid"
)

func TestPostgresLeaseContract(t *testing.T) {
	f := chain(t)
	ctx := t.Context()
	contracttest.RunLeases(t, contracttest.LeaseFixture{
		New: func(tenant uuid.UUID, app, revision string) contracttest.Leases {
			return conversation.NewStore(f.db, tenant, app, revision)
		},
		Exec: func(query string, args ...any) error {
			_, err := f.pg.Admin.Exec(ctx, query, args...)
			return err
		},
		Expire: func(id uuid.UUID) error {
			_, err := f.pg.Admin.Exec(ctx, "UPDATE channel.tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", id)
			return err
		},
		Probe: func(id uuid.UUID) (state string, version int64, audits int, err error) {
			err = f.pg.Admin.QueryRow(ctx, "SELECT state,step_version,(SELECT count(*) FROM channel.audit WHERE task_id=$1) FROM channel.tasks WHERE id=$1", id).Scan(&state, &version, &audits)
			return
		},
		FailAudit: func() error {
			_, err := f.pg.Admin.Exec(ctx, `CREATE FUNCTION channel.test_fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'test audit failure'; END $$;
CREATE TRIGGER fail_audit BEFORE INSERT ON channel.audit FOR EACH ROW EXECUTE FUNCTION channel.test_fail_audit();`)
			return err
		},
	})
}

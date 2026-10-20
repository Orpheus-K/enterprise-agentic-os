//go:build integration

package feishu

import (
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/conversation/contracttest"
	"github.com/google/uuid"
)

func TestPostgresReconciliationContract(t *testing.T) {
	contracttest.RunReconciliation(t, func(t *testing.T) contracttest.ReconcileFixture {
		f := chain(t)
		ctx := t.Context()
		return contracttest.ReconcileFixture{
			New: func(tenant uuid.UUID, app, revision string) contracttest.RecoveryStore {
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
			Ledgers: func(id uuid.UUID) (count int, err error) {
				err = f.pg.Admin.QueryRow(ctx, "SELECT count(*) FROM channel.reconciliations WHERE task_id=$1", id).Scan(&count)
				return
			},
			Fail: func(table string) error {
				switch table {
				case "audit", "audit_reads", "reconciliations":
				default:
					t.Fatal("invalid fault table")
				}
				_, err := f.pg.Admin.Exec(ctx, `CREATE FUNCTION channel.test_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test write failure'; END $$;
CREATE TRIGGER fail_write BEFORE INSERT ON channel.`+table+` FOR EACH ROW EXECUTE FUNCTION channel.test_failure();`)
				return err
			},
		}
	})
}

//go:build integration

package feishu

import (
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/conversation/contracttest"
	"github.com/google/uuid"
)

func TestPostgresInboxContract(t *testing.T) {
	f := chain(t)
	ctx := t.Context()
	contracttest.RunInbox(t, contracttest.InboxFixture{
		New: func(tenant uuid.UUID, app, revision string) contracttest.Inbox {
			return conversation.NewStore(f.db, tenant, app, revision)
		},
		Exec: func(query string, args ...any) error {
			_, err := f.pg.Admin.Exec(ctx, query, args...)
			return err
		},
		Counts: func() (tasks, audits int, err error) {
			err = f.pg.Admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM channel.tasks),(SELECT count(*) FROM channel.audit)").Scan(&tasks, &audits)
			return
		},
		Read: func(tenant uuid.UUID, app, event string) (revision, prompt, state string, err error) {
			err = f.pg.Admin.QueryRow(ctx, "SELECT model_revision,prompt,state FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND event_id=$3", tenant, app, event).Scan(&revision, &prompt, &state)
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

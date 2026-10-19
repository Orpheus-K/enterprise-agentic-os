//go:build integration

package operations

import (
	identitypg "agentos.local/core/internal/identity/postgres"
	"context"
	"errors"
	"testing"
	"time"
)

func TestRecoveryRevokedDuringLockWait(t *testing.T) {
	for _, read := range []bool{true, false} {
		t.Run(map[bool]string{true: "inspect", false: "decide"}[read], func(t *testing.T) {
			f := setup(t)
			in := prepareRecovery(t, f, "delivery_running")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			locked, err := f.pg.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer locked.Rollback(context.Background())
			var pid int
			if err = locked.QueryRow(ctx, "SELECT pg_backend_pid() FROM channel.tasks WHERE id=$1 FOR UPDATE", f.task).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				var err error
				if read {
					_, err = f.store.InspectRecovery(ctx, f.external, f.task)
				} else {
					_, err = f.store.Reconcile(ctx, f.external, in)
				}
				done <- err
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = f.pg.Admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatal("operation did not wait", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if _, err = f.pg.Admin.Exec(ctx, "UPDATE iam.operation_grants SET enabled=false WHERE member_id=$1 AND capability='task.reconcile'", f.member); err != nil {
				t.Fatal(err)
			}
			if err = locked.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, identitypg.ErrForbidden) {
				t.Fatalf("revoked operation: %v", err)
			}
			var state string
			if err = f.pg.Admin.QueryRow(ctx, "SELECT state FROM channel.tasks WHERE id=$1", f.task).Scan(&state); err != nil || state != "unknown" {
				t.Fatal(state, err)
			}
		})
	}
}

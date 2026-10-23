package contracttest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type RecoveryStore interface {
	Leases
	InspectRecovery(context.Context, identity.ExternalIdentity, uuid.UUID) (conversation.RecoverySnapshot, error)
	Reconcile(context.Context, identity.ExternalIdentity, conversation.Reconciliation) (conversation.RecoverySnapshot, error)
}

type ReconcileFixture struct {
	New     func(uuid.UUID, string, string) RecoveryStore
	Exec    func(string, ...any) error
	Expire  func(uuid.UUID) error
	Fail    func(string) error
	Ledgers func(uuid.UUID) (int, error)
}

func RunReconciliation(t *testing.T, fixture func(*testing.T) ReconcileFixture) {
	t.Helper()
	for _, scenario := range []string{"model_resume", "delivery_resume", "delivered", "unresolved", "wrong_stage", "missing_response", "ledger_failure", "audit_failure", "read_failure"} {
		t.Run(scenario, func(t *testing.T) {
			f, ctx := fixture(t), t.Context()
			tenant, worker, operator, another := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			write := func(query string, args ...any) {
				t.Helper()
				if err := f.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			write("INSERT INTO iam.tenants(id,name) VALUES($1,'reconcile')", tenant.String())
			for _, member := range []uuid.UUID{worker, operator, another} {
				write("INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,$3)", tenant.String(), member.String(), member.String())
				write("INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,'contract-channel','issuer',$2,$3)", tenant.String(), member.String(), member.String())
			}
			for _, member := range []uuid.UUID{operator, another} {
				write("INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'app','task.reconcile')", tenant.String(), member.String())
			}
			store := f.New(tenant, "app", "revision")
			actor := identity.ExternalIdentity{TenantID: tenant, Provider: "contract-channel", Issuer: "issuer", Subject: operator.String()}
			message := conversation.Message{EventID: "event", MessageID: "message", Text: "secret input", Fingerprint: "hash", Identity: actor}
			message.Identity.Subject = worker.String()
			if err := store.Enqueue(ctx, message); err != nil {
				t.Fatal(err)
			}
			task, err := store.Claim(ctx)
			if err != nil || task == nil {
				t.Fatalf("claim: %v", err)
			}
			step := func(to conversation.State, response string) {
				t.Helper()
				if err := store.Transition(ctx, task, to, "", response); err != nil {
					t.Fatal(err)
				}
			}
			step(conversation.ModelRunning, "")
			stage := conversation.ModelRunning
			if scenario == "delivery_resume" || scenario == "delivered" || scenario == "missing_response" {
				step(conversation.DeliveryPending, "saved response")
				step(conversation.DeliveryRunning, "")
				stage = conversation.DeliveryRunning
			}
			if err := f.Expire(task.ID); err != nil {
				t.Fatal(err)
			}
			if err := store.RecoverExpired(ctx); err != nil {
				t.Fatal(err)
			}
			if scenario == "read_failure" {
				if err := f.Fail("audit_reads"); err != nil {
					t.Fatal(err)
				}
				if out, err := store.InspectRecovery(ctx, actor, task.ID); err == nil || out != (conversation.RecoverySnapshot{}) {
					t.Fatalf("inspection ignored audit failure: %v %v", out, err)
				}
				return
			}
			before, err := store.InspectRecovery(ctx, actor, task.ID)
			if err != nil || before.State != conversation.Unknown || before.Stage != stage || before.Version != task.Version+1 {
				t.Fatalf("snapshot: %v %v", before, err)
			}
			for _, scope := range []RecoveryStore{f.New(tenant, "app", "wrong-revision"), f.New(tenant, "other-app", "revision")} {
				if out, err := scope.InspectRecovery(ctx, actor, task.ID); err == nil || out != (conversation.RecoverySnapshot{}) {
					t.Fatalf("out-of-scope recovery: %v %v", out, err)
				}
			}
			in := conversation.Reconciliation{TaskID: task.ID, RequestID: uuid.New(), EvidenceID: uuid.New(), ExpectedVersion: before.Version, Decision: "not_executed"}
			if scenario == "delivered" || scenario == "wrong_stage" {
				in.Decision = "delivered"
			}
			if scenario == "unresolved" {
				in.Decision = "unresolved"
			}
			invalid := in
			invalid.EvidenceID = uuid.Nil
			if out, err := store.Reconcile(ctx, actor, invalid); !errors.Is(err, conversation.ErrInvalidReconciliation) || out != (conversation.RecoverySnapshot{}) {
				t.Fatalf("missing evidence accepted: %v %v", out, err)
			}
			stale := in
			stale.ExpectedVersion--
			if out, err := store.Reconcile(ctx, actor, stale); !errors.Is(err, conversation.ErrReconcileConflict) || out != (conversation.RecoverySnapshot{}) {
				t.Fatalf("stale version accepted: %v %v", out, err)
			}
			if scenario == "missing_response" {
				write("UPDATE channel.tasks SET response=NULL WHERE tenant_id=$1 AND id=$2", tenant.String(), task.ID.String())
			}
			if scenario == "wrong_stage" || scenario == "missing_response" {
				if out, err := store.Reconcile(ctx, actor, in); !errors.Is(err, conversation.ErrReconcileConflict) || out != (conversation.RecoverySnapshot{}) {
					t.Fatalf("unsafe recovery accepted: %v %v", out, err)
				}
				return
			}
			if scenario == "ledger_failure" || scenario == "audit_failure" {
				table := "reconciliations"
				if scenario == "audit_failure" {
					table = "audit"
				}
				if err := f.Fail(table); err != nil {
					t.Fatal(err)
				}
				if out, err := store.Reconcile(ctx, actor, in); err == nil || out != (conversation.RecoverySnapshot{}) {
					t.Fatalf("ignored write failure: %v %v", out, err)
				}
				after, err := store.InspectRecovery(ctx, actor, task.ID)
				if err != nil || after != before {
					t.Fatalf("partial reconciliation: %v %v", after, err)
				}
				if count, err := f.Ledgers(task.ID); err != nil || count != 0 {
					t.Fatalf("failed transaction left ledger: %d %v", count, err)
				}
				return
			}
			want := before
			want.Version++
			switch scenario {
			case "model_resume":
				want.State = conversation.Pending
			case "delivery_resume":
				want.State = conversation.DeliveryPending
			case "delivered":
				want.State = conversation.Succeeded
			}
			restarted := f.New(tenant, "app", "revision")
			var wg sync.WaitGroup
			for _, current := range []RecoveryStore{store, restarted} {
				wg.Go(func() {
					out, err := current.Reconcile(ctx, actor, in)
					if err != nil || out != want {
						t.Errorf("duplicate reconciliation: %v %v want %v", out, err, want)
					}
				})
			}
			wg.Wait()
			if count, err := f.Ledgers(task.ID); err != nil || count != 1 {
				t.Fatalf("duplicate ledger count: %d %v", count, err)
			}
			tampered := in
			tampered.EvidenceID = uuid.New()
			if out, err := restarted.Reconcile(ctx, actor, tampered); !errors.Is(err, conversation.ErrReconcileConflict) || out != (conversation.RecoverySnapshot{}) {
				t.Fatalf("changed evidence replay: %v %v", out, err)
			}
			other := actor
			other.Subject = another.String()
			if out, err := restarted.Reconcile(ctx, other, in); !errors.Is(err, conversation.ErrReconcileConflict) || out != (conversation.RecoverySnapshot{}) {
				t.Fatalf("another operator reused request: %v %v", out, err)
			}
			if err := store.Transition(ctx, task, conversation.Unknown, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
				t.Fatalf("old owner wrote after reconciliation: %v", err)
			}
			next, err := restarted.Claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "model_resume" || scenario == "delivery_resume" {
				if next == nil || next.ID != task.ID || next.Generation <= task.Generation {
					t.Fatalf("recovery did not advance fence: %v", next)
				}
				if scenario == "delivery_resume" && next.Response != "saved response" {
					t.Fatal("lost saved response")
				}
			} else if next != nil {
				t.Fatalf("claimed unresolved or delivered task: %v", next)
			}
			if out, err := restarted.Reconcile(ctx, actor, in); err != nil || out != want {
				t.Fatalf("replay did not return original result: %v %v", out, err)
			}
			write("UPDATE iam.operation_grants SET enabled=$1 WHERE tenant_id=$2 AND member_id=$3", false, tenant.String(), operator.String())
			if out, err := restarted.Reconcile(ctx, actor, in); !errors.Is(err, identity.ErrForbidden) || out != (conversation.RecoverySnapshot{}) {
				t.Fatalf("duplicate ignored revocation: %v %v", out, err)
			}
		})
	}
}

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

type Leases interface {
	Inbox
	Claim(context.Context) (*conversation.Task, error)
	Authorize(context.Context, *conversation.Task) error
	Renew(context.Context, *conversation.Task) error
	Transition(context.Context, *conversation.Task, conversation.State, string, string) error
	RecoverExpired(context.Context) error
}

type LeaseFixture struct {
	New       func(uuid.UUID, string, string) Leases
	Exec      func(string, ...any) error
	Expire    func(uuid.UUID) error
	Probe     func(uuid.UUID) (state string, version int64, audits int, err error)
	FailAudit func() error
}

func RunLeases(t *testing.T, f LeaseFixture) {
	t.Helper()
	ctx := t.Context()
	tenant, member := uuid.New(), uuid.New()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO iam.tenants(id,name) VALUES($1,'lease')", []any{tenant.String()}},
		{"INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'lease')", []any{tenant.String(), member.String()}},
		{"INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,'contract-channel','issuer','subject',$2)", []any{tenant.String(), member.String()}},
	} {
		if err := f.Exec(statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	first, second := f.New(tenant, "app", "revision"), f.New(tenant, "app", "revision")
	message := conversation.Message{EventID: "event", MessageID: "message", Text: "fixture", Fingerprint: "hash",
		Identity: identity.ExternalIdentity{TenantID: tenant, Provider: "contract-channel", Issuer: "issuer", Subject: "subject"}}
	if err := first.Enqueue(ctx, message); err != nil {
		t.Fatal(err)
	}
	for _, scoped := range []Leases{f.New(uuid.New(), "app", "revision"), f.New(tenant, "other", "revision"), f.New(tenant, "app", "other")} {
		if task, err := scoped.Claim(ctx); err != nil || task != nil {
			t.Fatalf("claimed outside scope: %v %v", task, err)
		}
	}
	type claim struct {
		store Leases
		task  *conversation.Task
	}
	results := make(chan claim, 2)
	var wg sync.WaitGroup
	for _, store := range []Leases{first, second} {
		wg.Go(func() {
			task, err := store.Claim(ctx)
			if err != nil {
				t.Error(err)
			}
			if task != nil {
				results <- claim{store, task}
			}
		})
	}
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("concurrent claims=%d", len(results))
	}
	old := <-results
	if old.task.Generation != 1 || old.task.Version != 2 || old.task.State != conversation.Claimed {
		t.Fatalf("initial lease: %+v", old.task)
	}
	if err := old.store.Renew(ctx, old.task); err != nil {
		t.Fatal(err)
	}
	if err := f.Expire(old.task.ID); err != nil {
		t.Fatal(err)
	}
	if err := old.store.Renew(ctx, old.task); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatalf("revived expired lease: %v", err)
	}
	restarted := f.New(tenant, "app", "revision")
	current, err := restarted.Claim(ctx)
	if err != nil || current == nil || current.Generation != 2 || current.Version != 3 {
		t.Fatalf("reclaim: %v %v", current, err)
	}
	if err := old.store.Transition(ctx, old.task, conversation.ModelRunning, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatalf("old owner wrote: %v", err)
	}
	stale := *current
	step := func(store Leases, task *conversation.Task, state conversation.State, response string) {
		t.Helper()
		if err := store.Transition(ctx, task, state, "", response); err != nil {
			t.Fatal(err)
		}
	}
	step(restarted, current, conversation.ModelRunning, "")
	if err := restarted.Renew(ctx, &stale); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatalf("stale version renewed: %v", err)
	}
	step(restarted, current, conversation.DeliveryPending, "saved response")
	if err := f.Expire(current.ID); err != nil {
		t.Fatal(err)
	}
	resumer := f.New(tenant, "app", "revision")
	resumed, err := resumer.Claim(ctx)
	if err != nil || resumed == nil || resumed.State != conversation.DeliveryPending || resumed.Response != "saved response" || resumed.ID != current.ID {
		t.Fatalf("checkpoint resume: %v %v", resumed, err)
	}
	if err := f.Exec("UPDATE iam.identity_links SET enabled=$1 WHERE tenant_id=$2", false, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if err := resumer.Authorize(ctx, resumed); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("revoked checkpoint authorized: %v", err)
	}
	if err := f.Exec("UPDATE iam.identity_links SET enabled=$1 WHERE tenant_id=$2", true, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if err := resumer.Authorize(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	if err := resumer.Transition(ctx, resumed, conversation.Succeeded, "", ""); !errors.Is(err, conversation.ErrInvalidTransition) {
		t.Fatalf("skipped intent: %v", err)
	}
	step(resumer, resumed, conversation.DeliveryRunning, "")
	if err := f.Expire(resumed.ID); err != nil {
		t.Fatal(err)
	}
	if err := resumer.RecoverExpired(ctx); err != nil {
		t.Fatal(err)
	}
	state, version, audits, err := f.Probe(resumed.ID)
	if err != nil || state != "unknown" || version != resumed.Version+1 || audits != int(version) {
		t.Fatalf("recover: %s %d %d %v", state, version, audits, err)
	}
	if task, err := resumer.Claim(ctx); err != nil || task != nil {
		t.Fatalf("replayed unknown intent: %v %v", task, err)
	}
	if err := resumer.Transition(ctx, resumed, conversation.Succeeded, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatalf("late result wrote: %v", err)
	}
	message.EventID, message.MessageID = "success", "success"
	if err := first.Enqueue(ctx, message); err != nil {
		t.Fatal(err)
	}
	success, err := first.Claim(ctx)
	if err != nil || success == nil {
		t.Fatalf("success fixture claim: %v", err)
	}
	step(first, success, conversation.ModelRunning, "")
	step(first, success, conversation.DeliveryPending, "saved success")
	step(first, success, conversation.DeliveryRunning, "")
	step(first, success, conversation.Succeeded, "")
	if err := first.Renew(ctx, success); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatalf("renewed terminal task: %v", err)
	}
	state, version, audits, err = f.Probe(success.ID)
	if err != nil || state != "succeeded" || version != success.Version || audits != int(version) {
		t.Fatalf("success persistence: %s %d %d %v", state, version, audits, err)
	}
	message.EventID, message.MessageID = "rollback", "rollback"
	if err := first.Enqueue(ctx, message); err != nil {
		t.Fatal(err)
	}
	pending, err := first.Claim(ctx)
	if err != nil || pending == nil {
		t.Fatalf("rollback fixture claim: %v", err)
	}
	message.EventID, message.MessageID = "recovery-rollback", "recovery-rollback"
	if err := first.Enqueue(ctx, message); err != nil {
		t.Fatal(err)
	}
	intent, err := first.Claim(ctx)
	if err != nil || intent == nil {
		t.Fatalf("intent fixture claim: %v", err)
	}
	step(first, intent, conversation.ModelRunning, "")
	if err := f.Expire(intent.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.FailAudit(); err != nil {
		t.Fatal(err)
	}
	if err := first.Transition(ctx, pending, conversation.ModelRunning, "", ""); err == nil {
		t.Fatal("transition ignored audit failure")
	}
	state, version, audits, err = f.Probe(pending.ID)
	if err != nil || state != "claimed" || version != pending.Version || audits != int(version) || pending.State != conversation.Claimed {
		t.Fatalf("partial transition: %s %d %d %v", state, version, audits, err)
	}
	if err := first.RecoverExpired(ctx); err == nil {
		t.Fatal("recovery ignored audit failure")
	}
	state, version, audits, err = f.Probe(intent.ID)
	if err != nil || state != "model_running" || version != intent.Version || audits != int(version) {
		t.Fatalf("partial recovery: %s %d %d %v", state, version, audits, err)
	}
}

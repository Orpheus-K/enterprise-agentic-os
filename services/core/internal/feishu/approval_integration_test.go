//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/operations"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type approvalFixture struct {
	*chainFixture
	actor         identity.ExternalIdentity
	approver      uuid.UUID
	worker        *conversation.Worker
	models, sends atomic.Int32
	task          uuid.UUID
}

func approvalEnvironment(t *testing.T) *approvalFixture {
	t.Helper()
	f := &approvalFixture{chainFixture: chain(t), approver: uuid.New()}
	binding := testBinding()
	f.actor = identity.ExternalIdentity{TenantID: binding.TenantID, Provider: "operator", Issuer: "agentos:ops:" + binding.AppID, Subject: "synthetic-approver"}
	approvalSQL(t, f, "INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'synthetic-approver')", binding.TenantID, f.approver)
	approvalSQL(t, f, "INSERT INTO iam.identity_links VALUES($1,$2,$3,$4,$5,true)", binding.TenantID, f.actor.Provider, f.actor.Issuer, f.actor.Subject, f.approver)
	approvalSQL(t, f, "INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'task.approve')", binding.TenantID, f.approver, binding.AppID)
	var err error
	f.store, err = f.store.WithApproval(conversation.ApprovalPolicy{MemberID: f.approver, TTLSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	f.adapter = testAdapter(t, f.store)
	f.worker = &conversation.Worker{Store: f.store, Logger: f.logger, Telemetry: f.adapter.telemetry, Model: modelFunc(func(context.Context, string) (string, error) {
		f.models.Add(1)
		return "synthetic approval response", nil
	}), Sender: senderFunc(func(_ context.Context, _, _, text string) error {
		if text != "synthetic approval response" {
			t.Error("response changed")
		}
		f.sends.Add(1)
		return nil
	})}
	return f
}
func approvalChain(t *testing.T) *approvalFixture {
	t.Helper()
	f := approvalEnvironment(t)
	enqueue(t, f.chainFixture)
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT id FROM channel.tasks").Scan(&f.task); err != nil {
		t.Fatal(err)
	}
	return f
}
func approvalSQL(t *testing.T, f *approvalFixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.pg.Admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func approvalPending(t *testing.T, f *approvalFixture) conversation.Approval {
	t.Helper()
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatalf("run: %v %v", worked, err)
	}
	f.state(t, "waiting_approval")
	a, err := f.store.InspectApproval(t.Context(), f.actor, f.task)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func decision(a conversation.Approval) conversation.ApprovalDecision {
	return conversation.ApprovalDecision{TaskID: a.TaskID, ID: a.ID, RequestID: uuid.New(), Revision: a.Revision, Version: a.Version, ContentHash: a.ContentHash, Decision: "approve"}
}
func TestApprovalSuspendCallbackAndRestart(t *testing.T) {
	f := approvalChain(t)
	a := approvalPending(t, f)
	var released bool
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT lease_owner IS NULL AND lease_until IS NULL FROM channel.tasks").Scan(&released); err != nil || !released {
		t.Fatal("waiting retains lease")
	}
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || worked || f.sends.Load() != 0 {
		t.Fatal("waiting executed")
	}
	// Lose all runtime connections; policy and waiting state survive without configuration replay.
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	restarted := conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	f.worker.Store = restarted
	in := decision(a)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			out, err := restarted.DecideApproval(t.Context(), f.actor, in)
			if err != nil || out.Status != "approved" {
				t.Errorf("callback: %v", err)
			}
		})
	}
	wg.Wait()
	claimed, claimErr := restarted.Claim(t.Context())
	if claimErr != nil || claimed == nil {
		t.Fatal("approved checkpoint not claimable", claimErr)
	}
	f.worker.Store = conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	approvalSQL(t, f, "UPDATE channel.tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE lease_until IS NOT NULL")
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatalf("resume: %v", err)
	}
	f.state(t, "succeeded")
	if f.models.Load() != 1 || f.sends.Load() != 1 {
		t.Fatal("restart repeated external effects")
	}
	if _, err = restarted.DecideApproval(t.Context(), f.actor, in); err != nil {
		t.Fatal("completed replay rejected", err)
	}
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || worked {
		t.Fatal("duplicate woke task")
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit WHERE reason='approval_approved'").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate decision event")
	}
}
func TestApprovalVersionScopeAndReject(t *testing.T) {
	f := approvalChain(t)
	a := approvalPending(t, f)
	base := decision(a)
	for _, change := range []func(*conversation.ApprovalDecision){func(d *conversation.ApprovalDecision) { d.ID = uuid.New() }, func(d *conversation.ApprovalDecision) { d.Version++ }, func(d *conversation.ApprovalDecision) { d.Revision = "other" }, func(d *conversation.ApprovalDecision) { d.ContentHash = string(bytes.Repeat([]byte("a"), 64)) }} {
		in := base
		change(&in)
		if _, err := f.store.DecideApproval(t.Context(), f.actor, in); !errors.Is(err, conversation.ErrApprovalConflict) {
			t.Fatalf("mismatch accepted: %v", err)
		}
	}
	for _, store := range []*conversation.Store{conversation.NewStore(f.db, testBinding().TenantID, "other", "approved-revision"), conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "other")} {
		_, err := store.DecideApproval(t.Context(), f.actor, base)
		if !errors.Is(err, conversation.ErrTaskNotFound) && !errors.Is(err, identitypg.ErrForbidden) {
			t.Fatalf("cross scope %v", err)
		}
	}
	other := f.actor
	other.TenantID = uuid.New()
	if _, err := f.store.DecideApproval(t.Context(), other, base); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("cross tenant accepted")
	}
	in := base
	in.Decision = "reject"
	if _, err := f.store.DecideApproval(t.Context(), f.actor, in); err != nil {
		t.Fatal(err)
	}
	f.state(t, "rejected")
	if _, err := f.store.DecideApproval(t.Context(), f.actor, base); !errors.Is(err, conversation.ErrApprovalConflict) {
		t.Fatal("conflicting callback accepted")
	}
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || worked || f.sends.Load() != 0 {
		t.Fatal("rejected task sent")
	}
}
func TestApprovalRevocationBeforeCallbackAndDelivery(t *testing.T) {
	for _, after := range []bool{false, true} {
		for _, kind := range []string{"grant", "member", "mapping", "origin"} {
			t.Run(kind+map[bool]string{true: "_after", false: "_before"}[after], func(t *testing.T) {
				f := approvalChain(t)
				a := approvalPending(t, f)
				in := decision(a)
				if after {
					if _, err := f.store.DecideApproval(t.Context(), f.actor, in); err != nil {
						t.Fatal(err)
					}
				}
				switch kind {
				case "grant":
					approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false")
				case "member":
					approvalSQL(t, f, "UPDATE iam.members SET enabled=false WHERE id=$1", f.approver)
				case "mapping":
					approvalSQL(t, f, "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.approver)
				case "origin":
					approvalSQL(t, f, "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.member)
				}
				if !after {
					_, err := f.store.DecideApproval(t.Context(), f.actor, in)
					if kind != "origin" {
						if err == nil {
							t.Fatal("revoked approver accepted")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.worker.ProcessOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				f.state(t, "failed")
				if f.sends.Load() != 0 {
					t.Fatal("revoked permission sent")
				}
				if kind != "origin" {
					if _, err := f.store.DecideApproval(t.Context(), f.actor, in); err == nil {
						t.Fatal("revoked duplicate accepted")
					}
				}
			})
		}
	}
}
func TestApprovalExpiryMutationAndAtomicity(t *testing.T) {
	for _, kind := range []string{"expired", "changed", "audit"} {
		t.Run(kind, func(t *testing.T) {
			f := approvalChain(t)
			a := approvalPending(t, f)
			in := decision(a)
			switch kind {
			case "expired":
				approvalSQL(t, f, "UPDATE channel.approvals SET expires_at=clock_timestamp()-interval '1 second'")
			case "changed":
				approvalSQL(t, f, "UPDATE channel.tasks SET response='modified synthetic response'")
			case "audit":
				approvalSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT reject_decision CHECK(reason<>'approval_approved') NOT VALID")
			}
			if _, err := f.store.DecideApproval(t.Context(), f.actor, in); err == nil {
				t.Fatal("invalid callback accepted")
			}
			f.state(t, "waiting_approval")
			if kind == "expired" {
				if _, err := f.worker.ProcessOne(t.Context()); err != nil {
					t.Fatal(err)
				}
				f.state(t, "expired")
				if _, err := f.store.DecideApproval(t.Context(), f.actor, in); !errors.Is(err, conversation.ErrApprovalConflict) {
					t.Fatalf("expired callback: %v", err)
				}
			}
			if f.sends.Load() != 0 {
				t.Fatal("invalid approval sent")
			}
		})
	}
}
func TestApprovalAuthenticatedHTTP(t *testing.T) {
	f := approvalChain(t)
	a := approvalPending(t, f)
	token := "synthetic-approval-token-at-least-32-bytes"
	api, err := operations.New(token, f.actor, f.store, nil, f.adapter.telemetry)
	if err != nil {
		t.Fatal(err)
	}
	in := decision(a)
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"Bearer forged", "Bearer " + token} {
		r := httptest.NewRequest("POST", "/api/ops/approval", bytes.NewReader(body))
		r.Header.Set("Authorization", auth)
		r.Header.Set("X-Member-ID", f.approver.String())
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		want := 200
		if auth == "Bearer forged" {
			want = 401
		}
		if w.Code != want {
			t.Fatalf("callback HTTP=%d", w.Code)
		}
	}
}

func TestApprovalSuspendRollbackAndPreSuspendRecovery(t *testing.T) {
	f := approvalChain(t)
	approvalSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT reject_wait CHECK(reason<>'approval_requested') NOT VALID")
	if _, err := f.worker.ProcessOne(t.Context()); err == nil {
		t.Fatal("suspend audit failure hidden")
	}
	f.state(t, "delivery_pending")
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.approvals").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial approval persisted")
	}
	approvalSQL(t, f, "ALTER TABLE channel.audit DROP CONSTRAINT reject_wait")
	expireLease(t, f.chainFixture)
	f.worker.Store = anotherStore(f.chainFixture)
	if _, err := f.worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.state(t, "waiting_approval")
	if f.models.Load() != 1 || f.sends.Load() != 0 {
		t.Fatal("checkpoint not reused")
	}
}
func TestApprovalFinalBoundaryAndAssignedMember(t *testing.T) {
	for _, kind := range []string{"expired", "mutated", "other_member", "lease"} {
		t.Run(kind, func(t *testing.T) {
			f := approvalChain(t)
			a := approvalPending(t, f)
			in := decision(a)
			if kind == "other_member" {
				approvalSQL(t, f, "INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'task.approve')", testBinding().TenantID, f.member, testBinding().AppID)
				other := identity.ExternalIdentity{TenantID: testBinding().TenantID, Provider: "feishu", Issuer: testBinding().Issuer(), Subject: "ou_user"}
				if _, err := f.store.DecideApproval(t.Context(), other, in); !errors.Is(err, identitypg.ErrForbidden) {
					t.Fatalf("wrong assigned member: %v", err)
				}
				if _, err := f.store.InspectApproval(t.Context(), other, f.task); !errors.Is(err, identitypg.ErrForbidden) {
					t.Fatal("wrong member read content")
				}
				return
			}
			if _, err := f.store.DecideApproval(t.Context(), f.actor, in); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "expired":
				approvalSQL(t, f, "UPDATE channel.approvals SET expires_at=clock_timestamp()-interval '1 second'")
			case "mutated":
				approvalSQL(t, f, "UPDATE channel.tasks SET response='changed after approval'")
			case "lease":
				task, err := f.store.Claim(t.Context())
				if err != nil || task == nil {
					t.Fatal(err)
				}
				expireLease(t, f.chainFixture)
				if _, err := f.store.SuspendApproval(t.Context(), task); !errors.Is(err, conversation.ErrLeaseLost) {
					t.Fatal("expired owner suspended")
				}
				if err := f.store.Transition(t.Context(), task, conversation.DeliveryRunning, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
					t.Fatal("expired owner sent")
				}
				return
			}
			if _, err := f.worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.state(t, "failed")
			if f.sends.Load() != 0 {
				t.Fatal("invalid approval sent")
			}
		})
	}
}

func TestApprovalRevocationDuringTaskLockWait(t *testing.T) {
	f := approvalChain(t)
	a := approvalPending(t, f)
	tx, err := f.pg.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), "SELECT id FROM channel.tasks WHERE id=$1 FOR UPDATE", f.task); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.store.DecideApproval(ctx, f.actor, decision(a)); done <- err }()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err = f.pg.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("callback did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false WHERE capability='task.approve'")
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, identitypg.ErrForbidden) {
		t.Fatalf("stale authority: %v", err)
	}
	f.state(t, "waiting_approval")
}

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

type Approvals interface {
	Leases
	SuspendApproval(context.Context, *conversation.Task) (bool, error)
	InspectApproval(context.Context, identity.ExternalIdentity, uuid.UUID) (conversation.Approval, error)
	DecideApproval(context.Context, identity.ExternalIdentity, conversation.ApprovalDecision) (conversation.Approval, error)
	ExpireApprovals(context.Context) error
}

type ApprovalFixture struct {
	New           func(uuid.UUID, string, string, *conversation.ApprovalPolicy) (Approvals, error)
	Exec          func(string, ...any) error
	Expire        func(uuid.UUID) error
	FailAudit     func() error
	FailReadAudit func() error
}

func RunApprovals(t *testing.T, fixture func(*testing.T) ApprovalFixture) {
	t.Helper()
	for _, scenario := range []string{"approve", "reject", "revoke", "revoke_identity", "changed_response", "expire", "expiry_audit", "approved_expire", "suspend_audit", "decision_audit", "read_audit"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture(t)
			ctx := t.Context()
			tenant, user, approver := uuid.New(), uuid.New(), uuid.New()
			write := func(query string, args ...any) {
				t.Helper()
				if err := f.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			write("INSERT INTO iam.tenants(id,name) VALUES($1,'approval')", tenant.String())
			for _, member := range []uuid.UUID{user, approver} {
				write("INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,$3)", tenant.String(), member.String(), member.String())
				write("INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,'feishu','issuer',$2,$3)", tenant.String(), member.String(), member.String())
			}
			write("INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'app','task.approve')", tenant.String(), approver.String())
			store, err := f.New(tenant, "app", "revision", &conversation.ApprovalPolicy{MemberID: approver, TTLSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			actor := identity.ExternalIdentity{TenantID: tenant, Provider: "feishu", Issuer: "issuer", Subject: approver.String()}
			message := conversation.Message{EventID: "event", MessageID: "message", Text: "input", Fingerprint: "hash", Identity: actor}
			message.Identity.Subject = user.String()
			if err := store.Enqueue(ctx, message); err != nil {
				t.Fatal(err)
			}
			task, err := store.Claim(ctx)
			if err != nil || task == nil {
				t.Fatalf("claim: %v", err)
			}
			for _, step := range []struct {
				state conversation.State
				text  string
			}{{conversation.ModelRunning, ""}, {conversation.DeliveryPending, "approved bytes"}} {
				if err := store.Transition(ctx, task, step.state, "", step.text); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Transition(ctx, task, conversation.DeliveryRunning, "", ""); !errors.Is(err, conversation.ErrApprovalDenied) {
				t.Fatalf("bypassed approval: %v", err)
			}
			if scenario == "suspend_audit" {
				if err := f.FailAudit(); err != nil {
					t.Fatal(err)
				}
				if waiting, err := store.SuspendApproval(ctx, task); err == nil || waiting || task.State != conversation.DeliveryPending {
					t.Fatalf("partial suspension: %v %v", waiting, err)
				}
				if err := store.Renew(ctx, task); err != nil {
					t.Fatalf("failed suspension changed lease: %v", err)
				}
				if out, err := store.InspectApproval(ctx, actor, task.ID); !errors.Is(err, conversation.ErrTaskNotFound) || out.Response != "" {
					t.Fatalf("failed suspension left approval: %v %v", out, err)
				}
				return
			}
			if waiting, err := store.SuspendApproval(ctx, task); err != nil || !waiting || task.State != conversation.WaitingApproval {
				t.Fatalf("suspend: %v %v", waiting, err)
			}
			if err := store.Renew(ctx, task); !errors.Is(err, conversation.ErrLeaseLost) {
				t.Fatalf("waiting task kept lease: %v", err)
			}
			// Removing policy from a new instance must not remove persisted approval.
			restarted, err := f.New(tenant, "app", "revision", nil)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "read_audit" {
				if err := f.FailReadAudit(); err != nil {
					t.Fatal(err)
				}
				if out, err := restarted.InspectApproval(ctx, actor, task.ID); err == nil || out.Response != "" {
					t.Fatalf("read audit failure disclosed response: %v %v", out, err)
				}
				return
			}
			out, err := restarted.InspectApproval(ctx, actor, task.ID)
			if err != nil || out.Response != "approved bytes" || out.Status != "pending" || out.Version != task.Version {
				t.Fatalf("inspect: %v %v", out, err)
			}
			wrongRevision, err := f.New(tenant, "app", "another-revision", nil)
			if err != nil {
				t.Fatal(err)
			}
			if leaked, err := wrongRevision.InspectApproval(ctx, actor, task.ID); !errors.Is(err, conversation.ErrTaskNotFound) || leaked.Response != "" {
				t.Fatalf("cross-revision approval: %v %v", leaked, err)
			}
			unauthorized := actor
			unauthorized.Subject = user.String()
			if leaked, err := restarted.InspectApproval(ctx, unauthorized, task.ID); err == nil || leaked.Response != "" {
				t.Fatalf("non-approver read response: %v %v", leaked, err)
			}
			in := conversation.ApprovalDecision{TaskID: task.ID, ID: out.ID, RequestID: uuid.New(), Revision: out.Revision, Version: out.Version, ContentHash: out.ContentHash, Decision: "approve"}
			tampered := in
			tampered.Version++
			if _, err := restarted.DecideApproval(ctx, actor, tampered); !errors.Is(err, conversation.ErrApprovalConflict) {
				t.Fatalf("accepted stale approval version: %v", err)
			}
			tampered = in
			tampered.ContentHash = "z" + in.ContentHash[1:]
			if _, err := restarted.DecideApproval(ctx, actor, tampered); !errors.Is(err, conversation.ErrApprovalConflict) {
				t.Fatalf("accepted wrong content hash: %v", err)
			}
			if scenario == "expire" || scenario == "expiry_audit" {
				if err := f.Expire(task.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := restarted.DecideApproval(ctx, actor, in); !errors.Is(err, conversation.ErrApprovalConflict) {
					t.Fatalf("accepted expired approval: %v", err)
				}
				if scenario == "expiry_audit" {
					if err := f.FailAudit(); err != nil {
						t.Fatal(err)
					}
					if err := restarted.ExpireApprovals(ctx); err == nil {
						t.Fatal("expiry ignored audit failure")
					}
					still, err := restarted.InspectApproval(ctx, actor, task.ID)
					if err != nil || still.Status != "pending" {
						t.Fatalf("partial expiry: %v %v", still, err)
					}
					if claimed, err := restarted.Claim(ctx); err != nil || claimed != nil {
						t.Fatalf("claimed waiting task after expiry failure: %v %v", claimed, err)
					}
					return
				}
				if err := restarted.ExpireApprovals(ctx); err != nil {
					t.Fatal(err)
				}
				expired, err := restarted.InspectApproval(ctx, actor, task.ID)
				if err != nil || expired.Status != "expired" {
					t.Fatalf("expiry: %v %v", expired, err)
				}
				if claimed, err := restarted.Claim(ctx); err != nil || claimed != nil {
					t.Fatalf("claimed expired approval: %v %v", claimed, err)
				}
				return
			}
			if scenario == "decision_audit" {
				if err := f.FailAudit(); err != nil {
					t.Fatal(err)
				}
				if out, err := restarted.DecideApproval(ctx, actor, in); err == nil || out.Response != "" {
					t.Fatalf("decision ignored audit failure: %v %v", out, err)
				}
				still, err := restarted.InspectApproval(ctx, actor, task.ID)
				if err != nil || still.Status != "pending" || still.Version != in.Version {
					t.Fatalf("partial decision: %v %v", still, err)
				}
				return
			}
			want := "approved"
			if scenario == "reject" {
				in.Decision, want = "reject", "rejected"
			}
			var wg sync.WaitGroup
			otherReviewer, err := f.New(tenant, "app", "revision", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, reviewer := range []Approvals{restarted, otherReviewer} {
				wg.Go(func() {
					decided, err := reviewer.DecideApproval(ctx, actor, in)
					if err != nil || decided.Status != want || decided.Response != "" || decided.ResultVersion != in.Version+1 {
						t.Errorf("duplicate decision: %v %v", decided, err)
					}
				})
			}
			wg.Wait()
			if scenario == "reject" {
				if claimed, err := restarted.Claim(ctx); err != nil || claimed != nil {
					t.Fatalf("claimed rejected task: %v %v", claimed, err)
				}
				return
			}
			resumed, err := restarted.Claim(ctx)
			if err != nil || resumed == nil || resumed.ID != task.ID || resumed.Response != "approved bytes" {
				t.Fatalf("approval resume: %v %v", resumed, err)
			}
			if scenario == "revoke" {
				write("UPDATE iam.operation_grants SET enabled=$1 WHERE tenant_id=$2", false, tenant.String())
				if _, err := restarted.DecideApproval(ctx, actor, in); !errors.Is(err, identity.ErrForbidden) {
					t.Fatalf("duplicate did not reauthorize: %v", err)
				}
				if err := restarted.Transition(ctx, resumed, conversation.DeliveryRunning, "", ""); !errors.Is(err, conversation.ErrApprovalDenied) {
					t.Fatalf("revoked approval sent: %v", err)
				}
				return
			}
			if scenario == "revoke_identity" || scenario == "changed_response" || scenario == "approved_expire" {
				switch scenario {
				case "revoke_identity":
					write("UPDATE iam.identity_links SET enabled=$1 WHERE tenant_id=$2 AND subject=$3", false, tenant.String(), actor.Subject)
				case "changed_response":
					write("UPDATE channel.tasks SET response=$1 WHERE tenant_id=$2 AND id=$3", "changed response", tenant.String(), resumed.ID.String())
				case "approved_expire":
					if err := f.Expire(resumed.ID); err != nil {
						t.Fatal(err)
					}
				}
				if err := restarted.Transition(ctx, resumed, conversation.DeliveryRunning, "", ""); !errors.Is(err, conversation.ErrApprovalDenied) {
					t.Fatalf("invalidated approval sent: %v", err)
				}
				return
			}
			if err := restarted.Transition(ctx, resumed, conversation.DeliveryRunning, "", ""); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Transition(ctx, resumed, conversation.Succeeded, "", ""); err != nil {
				t.Fatal(err)
			}
		})
	}
}

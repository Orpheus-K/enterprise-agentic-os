package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"testing"
)

type decisionFunc func(context.Context, identity.ExternalIdentity, conversation.ApprovalDecision) (conversation.Approval, error)

func (f decisionFunc) DecideApproval(ctx context.Context, a identity.ExternalIdentity, d conversation.ApprovalDecision) (conversation.Approval, error) {
	return f(ctx, a, d)
}

func cardBody(t *testing.T, decision conversation.ApprovalDecision, mutate func(map[string]any)) []byte {
	t.Helper()
	value := map[string]any{"task_id": decision.TaskID, "approval_id": decision.ID, "revision": decision.Revision, "version": decision.Version, "content_hash": decision.ContentHash, "decision": decision.Decision}
	e := map[string]any{"schema": "2.0", "header": map[string]any{"app_id": testBinding().AppID, "tenant_key": testBinding().TenantKey, "event_id": "synthetic-card-event", "event_type": "card.action.trigger", "token": testToken}, "event": map[string]any{"operator": map[string]any{"open_id": "ou_approver", "tenant_key": testBinding().TenantKey}, "host": "im_message", "context": map[string]any{"open_message_id": "om_notice"}, "action": map[string]any{"tag": "button", "value": value}}}
	if mutate != nil {
		mutate(e)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCardCallbackTrustBoundary(t *testing.T) {
	for _, tc := range []string{"valid", "signature", "app", "tenant", "operator_tenant", "missing_operator", "request_id", "unknown_value", "disabled"} {
		t.Run(tc, func(t *testing.T) {
			calls := 0
			a := testAdapter(t, &recordingInbox{}).WithApprovals(decisionFunc(func(_ context.Context, actor identity.ExternalIdentity, d conversation.ApprovalDecision) (conversation.Approval, error) {
				calls++
				if actor.Subject != "ou_approver" || actor.Issuer != testBinding().Issuer() || actor.TenantID != testBinding().TenantID || d.RequestID == uuid.Nil {
					t.Fatal("untrusted identity or request")
				}
				return conversation.Approval{}, nil
			}))
			if tc == "disabled" {
				a = a.WithApprovals(nil)
			}
			body := cardBody(t, conversation.ApprovalDecision{}, func(e map[string]any) {
				h := e["header"].(map[string]any)
				v := e["event"].(map[string]any)
				switch tc {
				case "app":
					h["app_id"] = "other"
				case "tenant":
					h["tenant_key"] = "other"
				case "operator_tenant":
					v["operator"].(map[string]any)["tenant_key"] = "other"
				case "missing_operator":
					delete(v, "operator")
				case "request_id", "unknown_value":
					v["action"].(map[string]any)["value"].(map[string]any)[tc] = uuid.NewString()
				}
			})
			r := encryptedRequest(t, body)
			if tc == "signature" {
				r.Header.Set("X-Lark-Signature", "invalid")
			}
			status := webhookStatus(t, a, r)
			if (status == 200) != (tc == "valid") || (calls == 1) != (tc == "valid") {
				t.Fatal(status, calls)
			}
		})
	}
}

func TestCardCredentialBoundSDK(t *testing.T) {
	calls := 0
	a := testAdapter(t, &recordingInbox{}).WithApprovals(decisionFunc(func(context.Context, identity.ExternalIdentity, conversation.ApprovalDecision) (conversation.Approval, error) {
		calls++
		return conversation.Approval{}, nil
	}))
	d := a.dispatcher(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := d.Do(t.Context(), cardBody(t, conversation.ApprovalDecision{}, nil)); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

type ApprovalDecider interface {
	DecideApproval(context.Context, identity.ExternalIdentity, conversation.ApprovalDecision) (conversation.Approval, error)
}

func (a *Adapter) WithApprovals(decider ApprovalDecider) *Adapter {
	copy := *a
	copy.approvals = decider
	return &copy
}

// Called only by the authenticated webhook or the credential-bound SDK connection.
func (a *Adapter) receiveCard(ctx context.Context, body []byte) (*callback.CardActionTriggerResponse, error) {
	rejected := errors.New("invalid approval card action")
	if a.approvals == nil || len(body) > 65536 || !json.Valid(body) {
		return nil, rejected
	}
	var e callback.CardActionTriggerEvent
	input := json.NewDecoder(bytes.NewReader(body))
	input.UseNumber()
	if input.Decode(&e) != nil || e.EventV2Base == nil || e.EventV2Base.Header == nil || e.Event == nil {
		return nil, rejected
	}
	if e.Schema != "2.0" || e.EventV2Base.Header.EventType != "card.action.trigger" || e.EventV2Base.Header.AppID != a.binding.AppID || e.EventV2Base.Header.TenantKey != a.binding.TenantKey || e.EventV2Base.Header.EventID == "" || len(e.EventV2Base.Header.EventID) > 256 {
		return nil, rejected
	}
	v := e.Event
	if v.Operator == nil || v.Operator.TenantKey == nil || *v.Operator.TenantKey != a.binding.TenantKey || v.Operator.OpenID == "" || v.Action == nil || v.Action.Tag != "button" || v.Host != "im_message" || v.Context == nil || v.Context.OpenMessageID == "" {
		return nil, rejected
	}
	value, err := json.Marshal(v.Action.Value)
	if err != nil {
		return nil, rejected
	}
	var decision conversation.ApprovalDecision
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&decision) != nil || decision.RequestID != uuid.Nil {
		return nil, rejected
	}
	// The transport event owns replay identity; action values cannot choose it.
	decision.RequestID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(a.binding.Issuer()+":"+e.EventV2Base.Header.EventID))
	actor := identity.ExternalIdentity{TenantID: a.binding.TenantID, Provider: "feishu", Issuer: a.binding.Issuer(), Subject: v.Operator.OpenID}
	if _, err = a.approvals.DecideApproval(ctx, actor, decision); err != nil {
		return nil, err
	}
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "success", Content: "审批决定已记录"}}, nil
}

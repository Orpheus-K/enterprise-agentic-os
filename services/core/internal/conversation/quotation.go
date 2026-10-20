package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/quotation"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SaveQuotation accepts a calculated draft from the trusted business adapter, not model text.
func (s *Store) SaveQuotation(ctx context.Context, t *Task, d quotation.Draft) error {
	if (t.State != ModelRunning && t.State != ToolRunning) || d.ID != t.ID || d.Version != 1 || quotation.Verify(d) != nil {
		return ErrInvalidApproval
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(raw) > 16000 {
		return ErrInvalidApproval
	}
	err = s.scoped(ctx, func(q database.Queries) error {
		if err := s.lockTask(ctx, q, t); err != nil {
			return err
		}
		var valid, hasApprover bool
		if err := q.QueryRow(ctx, `SELECT state=$8 AND lease_owner=$5 AND lease_generation=$6 AND step_version=$7 AND lease_until>clock_timestamp() AND quote_version IS NULL,approval_member IS NOT NULL FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4`, s.tenant, s.app, s.revision, t.ID, s.owner, t.Generation, t.Version, t.State).Scan(&valid, &hasApprover); err != nil {
			return err
		}
		if !valid {
			return ErrLeaseLost
		}
		if d.RequiresApproval && !hasApprover {
			return ErrApprovalDenied
		}
		if err := s.checkTaskSkill(ctx, q, t.ID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `INSERT INTO channel.quote_versions VALUES($1,$2,$3,$4,$5)`, s.tenant, t.ID, d.Version, d.Hash, raw); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `UPDATE channel.tasks SET quote_version=1,response=$3,state='delivery_pending',step_version=step_version+1,approval_member=CASE WHEN $4 THEN approval_member ELSE NULL END,approval_ttl=CASE WHEN $4 THEN approval_ttl ELSE NULL END,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND lease_until>clock_timestamp()`, s.tenant, t.ID, string(raw), d.RequiresApproval)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return audit(ctx, q, s.tenant, t.ID, "delivery_pending", "quotation_saved")
	})
	if err == nil {
		t.QuoteVersion = d.Version
		t.State = DeliveryPending
		t.Version++
		t.Response = string(raw)
	}
	return err
}

type QuoteEdit struct {
	ApprovalDecision
	QuoteHash string                  `json:"quote_hash"`
	Changes   []quotation.PriceChange `json:"changes"`
}

// Amend only pending approvals. Every modification creates a fresh approval, even above the threshold.
func (s *Store) AmendQuotation(ctx context.Context, actor identity.ExternalIdentity, in QuoteEdit) (Approval, error) {
	if in.TaskID == uuid.Nil || in.ID == uuid.Nil || in.RequestID == uuid.Nil || in.Version < 1 || in.Decision != "amend" || len(in.QuoteHash) != 64 || len(in.Changes) < 1 || len(in.Changes) > 100 {
		return Approval{}, ErrInvalidApproval
	}
	raw, err := json.Marshal(struct {
		Actor identity.ExternalIdentity
		Input QuoteEdit
	}{actor, in})
	if err != nil {
		return Approval{}, err
	}
	fingerprint := contentHash(string(raw))
	var out Approval
	err = s.Operation(ctx, actor, "task.approve", func(q database.Queries) error {
		snapshot, err := s.recoverySnapshot(ctx, q, in.TaskID)
		if err != nil {
			return err
		}
		current, member, err := s.readApproval(ctx, q, in.TaskID)
		if err != nil {
			return err
		}
		if err = s.approvalActor(ctx, q, actor, member); err != nil {
			return err
		}
		if err = s.approvalKnowledge(ctx, q, in.TaskID, actor, member); err != nil {
			return err
		}
		var priorHash string
		var priorMember uuid.UUID
		err = q.QueryRow(ctx, `SELECT request_hash,member_id,result FROM channel.quote_edits WHERE tenant_id=$1 AND task_id=$2 AND request_id=$3`, s.tenant, in.TaskID, in.RequestID).Scan(&priorHash, &priorMember, &out)
		if err == nil {
			if priorHash != fingerprint || priorMember != member {
				return ErrApprovalConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if snapshot.State != WaitingApproval || snapshot.Version != in.Version || current.Status != "pending" || current.ID != in.ID || current.Revision != in.Revision || current.ContentHash != in.ContentHash || contentHash(current.Response) != in.ContentHash {
			return ErrApprovalConflict
		}
		var old quotation.Draft
		var active bool
		var ttl int
		err = q.QueryRow(ctx, `SELECT v.draft,a.expires_at>clock_timestamp(),t.approval_ttl FROM channel.tasks t JOIN channel.quote_versions v ON (v.tenant_id,v.task_id,v.version)=(t.tenant_id,t.id,t.quote_version) JOIN channel.approvals a ON (a.tenant_id,a.task_id)=(t.tenant_id,t.id) WHERE t.tenant_id=$1 AND t.id=$2`, s.tenant, in.TaskID).Scan(&old, &active, &ttl)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrApprovalConflict
		}
		if err != nil {
			return err
		}
		if !active {
			return ErrApprovalConflict
		}
		next, err := quotation.Revise(old, in.QuoteHash, in.Changes)
		if err != nil {
			return ErrApprovalConflict
		}
		body, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if len(body) > 16000 {
			return ErrInvalidApproval
		}
		if _, err = q.Exec(ctx, `INSERT INTO channel.quote_versions VALUES($1,$2,$3,$4,$5)`, s.tenant, in.TaskID, next.Version, next.Hash, body); err != nil {
			return err
		}
		if _, err = q.Exec(ctx, `UPDATE channel.tasks SET quote_version=$3,response=$4,step_version=step_version+1,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, s.tenant, in.TaskID, next.Version, string(body)); err != nil {
			return err
		}
		out = Approval{QuoteVersion: next.Version, TaskID: in.TaskID, ID: uuid.New(), Revision: current.Revision, Version: in.Version + 1, ContentHash: contentHash(string(body)), Status: "pending"}
		err = q.QueryRow(ctx, `UPDATE channel.approvals SET id=$3,content_hash=$4,waiting_version=$5,expires_at=clock_timestamp()+$6*interval '1 second',status='pending',request_id=NULL,result_version=NULL,actor_provider=NULL,actor_issuer=NULL,actor_subject=NULL WHERE tenant_id=$1 AND task_id=$2 RETURNING expires_at`, s.tenant, in.TaskID, out.ID, out.ContentHash, out.Version, ttl).Scan(&out.ExpiresAt)
		if err != nil {
			return err
		}
		if _, err = q.Exec(ctx, `INSERT INTO channel.quote_edits VALUES($1,$2,$3,$4,$5,$6,$7)`, s.tenant, in.TaskID, in.RequestID, member, fingerprint, current, out); err != nil {
			return err
		}
		if err = s.enqueueNotice(ctx, q, in.TaskID); err != nil {
			return err
		}
		return audit(ctx, q, s.tenant, in.TaskID, "waiting_approval", "quotation_amended")
	})
	if err != nil {
		return Approval{}, err
	}
	return out, nil
}

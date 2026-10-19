package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/quotation"
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

type Notifier interface {
	Notify(context.Context, string, string, ApprovalNotice) error
}

type ApprovalNotice struct {
	Approval      Approval
	PreviousQuote *quotation.Draft
}

func (s *Store) enqueueNotice(ctx context.Context, q database.Queries, id uuid.UUID) error {
	_, err := q.Exec(ctx, `INSERT INTO channel.approval_notices(tenant_id,task_id,id,member_id,recipient,issuer,revision,waiting_version,content_hash) SELECT t.tenant_id,t.id,a.id,a.member_id,t.approval_recipient,t.issuer,a.revision,a.waiting_version,a.content_hash FROM channel.tasks t JOIN channel.approvals a ON (a.tenant_id,a.task_id)=(t.tenant_id,t.id) WHERE t.tenant_id=$1 AND t.id=$2 AND t.approval_recipient IS NOT NULL ON CONFLICT DO NOTHING`, s.tenant, id)
	return err
}

// NotifyOne persists intent before sending. An expired or failed send is never replayed.
func (s *Store) NotifyOne(ctx context.Context, sender Notifier) (bool, error) {
	var task, id, member uuid.UUID
	var recipient, issuer, hash string
	var version, attempt int64
	var notice ApprovalNotice
	send, worked := false, false
	err := s.scoped(ctx, func(q database.Queries) error {
		// Task-first lock order matches approvals and amendments.
		err := q.QueryRow(ctx, `SELECT t.id FROM channel.tasks t WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.model_revision=$3 AND EXISTS(SELECT 1 FROM channel.approval_notices n WHERE n.tenant_id=t.tenant_id AND n.task_id=t.id AND (n.state='pending' OR (n.state='sending' AND n.lease_until<=clock_timestamp()))) ORDER BY t.created_at,t.id FOR UPDATE OF t SKIP LOCKED LIMIT 1`, s.tenant, s.app, s.revision).Scan(&task)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var state string
		err = q.QueryRow(ctx, `SELECT id,member_id,recipient,issuer,content_hash,waiting_version,state FROM channel.approval_notices WHERE tenant_id=$1 AND task_id=$2 AND (state='pending' OR (state='sending' AND lease_until<=clock_timestamp())) ORDER BY waiting_version,id LIMIT 1`, s.tenant, task).Scan(&id, &member, &recipient, &issuer, &hash, &version, &state)
		if err != nil {
			return err
		}
		worked = true
		finish := func(state string) error {
			if _, err := q.Exec(ctx, `UPDATE channel.approval_notices SET state=$3,version=version+1,owner=NULL,lease_until=NULL WHERE tenant_id=$1 AND id=$2`, s.tenant, id, state); err != nil {
				return err
			}
			return s.noticeAudit(ctx, q, task, id, state)
		}
		if state == "sending" {
			return finish("unknown")
		}
		var valid bool
		err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel.approvals a JOIN channel.tasks t ON (a.tenant_id,a.task_id)=(t.tenant_id,t.id) WHERE a.tenant_id=$1 AND a.task_id=$2 AND a.id=$3 AND a.status='pending' AND a.expires_at>clock_timestamp() AND a.waiting_version=$4 AND a.content_hash=$5 AND t.state='waiting_approval' AND t.step_version=$4)`, s.tenant, task, id, version, hash).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return finish("cancelled")
		}
		if _, err = q.Exec(ctx, "SELECT set_config('agentos.member_id',$1,true)", member.String()); err != nil {
			return err
		}
		actor := identity.ExternalIdentity{TenantID: s.tenant, Provider: "feishu", Issuer: issuer, Subject: recipient}
		if err = s.approvalActor(ctx, q, actor, member); err != nil {
			if errors.Is(err, identity.ErrUnauthenticated) || errors.Is(err, identitypg.ErrForbidden) {
				return finish("cancelled")
			}
			return err
		}
		if sender == nil {
			return errors.New("approval notifier unavailable")
		}
		notice.Approval, _, err = s.readApproval(ctx, q, task)
		if err != nil {
			return err
		}
		if notice.Approval.ContentHash != contentHash(notice.Approval.Response) {
			return ErrApprovalConflict
		}
		if notice.Approval.QuoteVersion > 1 {
			var previous quotation.Draft
			if err = q.QueryRow(ctx, `SELECT draft FROM channel.quote_versions WHERE tenant_id=$1 AND task_id=$2 AND version=$3`, s.tenant, task, notice.Approval.QuoteVersion-1).Scan(&previous); err != nil {
				return err
			}
			notice.PreviousQuote = &previous
		}
		if err = q.QueryRow(ctx, `UPDATE channel.approval_notices SET state='sending',version=version+1,owner=$3,lease_until=clock_timestamp()+interval '30 seconds' WHERE tenant_id=$1 AND id=$2 RETURNING version`, s.tenant, id, s.owner).Scan(&attempt); err != nil {
			return err
		}
		if err = s.noticeAudit(ctx, q, task, id, "sending"); err != nil {
			return err
		}
		send = true
		return nil
	})
	if err != nil || !send {
		return worked, err
	}
	sendErr := sender.Notify(ctx, recipient, id.String(), notice)
	state := "sent"
	if sendErr != nil {
		state = "unknown"
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = s.scoped(cleanup, func(q database.Queries) error {
		if err := s.lockTask(cleanup, q, &Task{ID: task}); err != nil {
			return err
		}
		result, err := q.Exec(cleanup, `UPDATE channel.approval_notices SET state=$4,version=version+1,owner=NULL,lease_until=NULL WHERE tenant_id=$1 AND id=$2 AND state='sending' AND owner=$3 AND version=$5 AND lease_until>clock_timestamp()`, s.tenant, id, s.owner, state, attempt)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return s.noticeAudit(cleanup, q, task, id, state)
	})
	return true, err
}

// Notification events do not advance the task or invalidate its waiting approval version.
func (s *Store) noticeAudit(ctx context.Context, q database.Queries, task, id uuid.UUID, state string) error {
	_, err := q.Exec(ctx, `INSERT INTO channel.audit(tenant_id,task_id,state,reason,trace_id,operation_id) VALUES($1,$2,'approval_notice',$3,$4,$5)`, s.tenant, task, state, spanTraceID(ctx), id)
	return err
}

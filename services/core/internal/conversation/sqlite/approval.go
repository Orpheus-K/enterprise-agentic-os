package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

func (s *Inbox) WithApproval(policy conversation.ApprovalPolicy) (*Inbox, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	// Notification delivery has its own durable protocol and is not ported yet.
	if policy.NotifySubject != "" {
		return nil, conversation.ErrInvalidApproval
	}
	copy := *s
	copy.approval = &policy
	return &copy, nil
}

func (s *Inbox) SuspendApproval(ctx context.Context, task *conversation.Task) (bool, error) {
	waiting := false
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var member sql.NullString
		var ttl sql.NullInt64
		var response string
		var exists bool
		err := tx.QueryRowContext(ctx, `SELECT approval_member,approval_ttl,coalesce(response,''),
EXISTS(SELECT 1 FROM channel_approvals a WHERE a.tenant_id=channel_tasks.tenant_id AND a.task_id=channel_tasks.id)
FROM channel_tasks WHERE `+leasePredicate, s.leaseArgs(task)...).Scan(&member, &ttl, &response, &exists)
		if errors.Is(err, sql.ErrNoRows) || task.State != conversation.DeliveryPending {
			return conversation.ErrLeaseLost
		}
		if err != nil {
			return err
		}
		if !member.Valid || exists {
			return nil
		}
		if response == "" || !ttl.Valid || ttl.Int64 < 1 || ttl.Int64 > 604800 {
			return conversation.ErrApprovalDenied
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_approvals
(tenant_id,task_id,id,member_id,revision,content_hash,waiting_version,expires_at,status)
VALUES(?,?,?,?,?,?,?,unixepoch('subsec')+?,'pending')`, s.tenant.String(), task.ID.String(), uuid.New().String(),
			member.String, s.revision, conversation.ApprovalContentHash(response), task.Version+1, ttl.Int64)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE channel_tasks SET state='waiting_approval',lease_owner=NULL,lease_until=NULL,step_version=step_version+1 WHERE tenant_id=? AND id=?`, s.tenant.String(), task.ID.String())
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, task.ID, "waiting_approval", "approval_requested"); err != nil {
			return err
		}
		waiting = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if waiting {
		task.State = conversation.WaitingApproval
		task.Version++
	}
	return waiting, nil
}

func (s *Inbox) checkApproval(ctx context.Context, tx *sql.Tx, id uuid.UUID) error {
	var required bool
	var response string
	err := tx.QueryRowContext(ctx, `SELECT approval_member IS NOT NULL,coalesce(response,'') FROM channel_tasks
WHERE tenant_id=? AND app_id=? AND model_revision=? AND id=?`, s.tenant.String(), s.app, s.revision, id.String()).Scan(&required, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.ErrLeaseLost
	}
	if err != nil || !required {
		return err
	}
	var member uuid.UUID
	var hash string
	var valid bool
	actor := identity.ExternalIdentity{TenantID: s.tenant}
	err = tx.QueryRowContext(ctx, `SELECT member_id,content_hash,status='approved' AND revision=? AND expires_at>unixepoch('subsec'),
coalesce(actor_provider,''),coalesce(actor_issuer,''),coalesce(actor_subject,'') FROM channel_approvals WHERE tenant_id=? AND task_id=?`,
		s.revision, s.tenant.String(), id.String()).Scan(&member, &hash, &valid, &actor.Provider, &actor.Issuer, &actor.Subject)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.ErrApprovalDenied
	}
	if err != nil {
		return err
	}
	if !valid || hash != conversation.ApprovalContentHash(response) {
		return conversation.ErrApprovalDenied
	}
	p, err := s.identities.RequireCapabilityIn(ctx, tx, actor, s.app, "task.approve")
	if errors.Is(err, identity.ErrUnauthenticated) || errors.Is(err, identity.ErrForbidden) || errors.Is(err, identity.ErrInvalidIdentity) {
		return conversation.ErrApprovalDenied
	}
	if err != nil {
		return err
	}
	if p.MemberID != member {
		return conversation.ErrApprovalDenied
	}
	return nil
}

func (s *Inbox) readApproval(ctx context.Context, tx *sql.Tx, id uuid.UUID) (conversation.Approval, uuid.UUID, error) {
	out := conversation.Approval{TaskID: id}
	var member uuid.UUID
	var expiry float64
	err := tx.QueryRowContext(ctx, `SELECT a.id,a.member_id,a.revision,a.content_hash,a.waiting_version,a.expires_at,a.status,
coalesce(a.result_version,0),coalesce(t.response,'') FROM channel_approvals a JOIN channel_tasks t ON t.tenant_id=a.tenant_id AND t.id=a.task_id
WHERE t.tenant_id=? AND t.app_id=? AND t.model_revision=? AND t.id=?`, s.tenant.String(), s.app, s.revision, id.String()).
		Scan(&out.ID, &member, &out.Revision, &out.ContentHash, &out.Version, &expiry, &out.Status, &out.ResultVersion, &out.Response)
	if errors.Is(err, sql.ErrNoRows) {
		err = conversation.ErrTaskNotFound
	}
	out.ExpiresAt = time.UnixMilli(int64(expiry * 1000)).UTC()
	return out, member, err
}

func (s *Inbox) InspectApproval(ctx context.Context, actor identity.ExternalIdentity, id uuid.UUID) (conversation.Approval, error) {
	var out conversation.Approval
	if actor.TenantID != s.tenant {
		return out, identity.ErrUnauthenticated
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := s.identities.RequireCapabilityIn(ctx, tx, actor, s.app, "task.approve")
		if err != nil {
			return err
		}
		var member uuid.UUID
		out, member, err = s.readApproval(ctx, tx, id)
		if err != nil {
			return err
		}
		if member != p.MemberID {
			return identity.ErrForbidden
		}
		traceID := ""
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			traceID = sc.TraceID().String()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id)
VALUES(?,?,?,?,?,1,?)`, uuid.New().String(), s.tenant.String(), member.String(), s.app, id.String(), traceID)
		return err
	})
	if err != nil {
		return conversation.Approval{}, err
	}
	return out, nil
}

func (s *Inbox) DecideApproval(ctx context.Context, actor identity.ExternalIdentity, in conversation.ApprovalDecision) (conversation.Approval, error) {
	if err := in.Validate(); err != nil {
		return conversation.Approval{}, err
	}
	if actor.TenantID != s.tenant {
		return conversation.Approval{}, identity.ErrUnauthenticated
	}
	var out conversation.Approval
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := s.identities.RequireCapabilityIn(ctx, tx, actor, s.app, "task.approve")
		if err != nil {
			return err
		}
		var member uuid.UUID
		out, member, err = s.readApproval(ctx, tx, in.TaskID)
		if err != nil {
			return err
		}
		if member != p.MemberID {
			return identity.ErrForbidden
		}
		if out.ID != in.ID || out.Revision != in.Revision || out.Version != in.Version || out.ContentHash != in.ContentHash || conversation.ApprovalContentHash(out.Response) != in.ContentHash {
			return conversation.ErrApprovalConflict
		}
		status, target := "approved", "delivery_pending"
		if in.Decision == "reject" {
			status, target = "rejected", "rejected"
		}
		if out.Status != "pending" {
			var same bool
			err := tx.QueryRowContext(ctx, `SELECT coalesce(request_id=? AND status=? AND actor_provider=? AND actor_issuer=? AND actor_subject=?,0)
FROM channel_approvals WHERE tenant_id=? AND task_id=?`, in.RequestID.String(), status, actor.Provider, actor.Issuer, actor.Subject, s.tenant.String(), in.TaskID.String()).Scan(&same)
			if err != nil {
				return err
			}
			if !same {
				return conversation.ErrApprovalConflict
			}
			return nil
		}
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT t.state='waiting_approval' AND t.step_version=? AND a.expires_at>unixepoch('subsec')
FROM channel_tasks t JOIN channel_approvals a ON t.tenant_id=a.tenant_id AND t.id=a.task_id WHERE t.tenant_id=? AND t.id=?`,
			in.Version, s.tenant.String(), in.TaskID.String()).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return conversation.ErrApprovalConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE channel_tasks SET state=?,reason=?,lease_owner=CASE WHEN ?='delivery_pending' THEN ? ELSE NULL END,
lease_until=CASE WHEN ?='delivery_pending' THEN unixepoch('subsec') ELSE NULL END,step_version=step_version+1 WHERE tenant_id=? AND id=?`,
			target, "approval_"+status, target, s.owner.String(), target, s.tenant.String(), in.TaskID.String())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE channel_approvals SET status=?,request_id=?,result_version=?,actor_provider=?,actor_issuer=?,actor_subject=?
WHERE tenant_id=? AND task_id=?`, status, in.RequestID.String(), in.Version+1, actor.Provider, actor.Issuer, actor.Subject, s.tenant.String(), in.TaskID.String())
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, in.TaskID, target, "approval_"+status); err != nil {
			return err
		}
		out.Status, out.ResultVersion = status, in.Version+1
		return nil
	})
	if err != nil {
		return conversation.Approval{}, err
	}
	out.Response = ""
	return out, nil
}

func (s *Inbox) ExpireApprovals(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.id FROM channel_tasks t JOIN channel_approvals a ON t.tenant_id=a.tenant_id AND t.id=a.task_id
WHERE t.tenant_id=? AND t.app_id=? AND t.model_revision=? AND t.state='waiting_approval' AND a.status='pending'
AND a.expires_at<=unixepoch('subsec') ORDER BY a.expires_at,t.id LIMIT 100`, s.tenant.String(), s.app, s.revision)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return errors.Join(err, rows.Close())
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE channel_tasks SET state='expired',reason='approval_expired',step_version=step_version+1 WHERE tenant_id=? AND id=?", s.tenant.String(), id.String()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE channel_approvals SET status='expired' WHERE tenant_id=? AND task_id=?", s.tenant.String(), id.String()); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, id, "expired", "approval_expired"); err != nil {
				return err
			}
		}
		return nil
	})
}

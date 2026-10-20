package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

func (s *Inbox) recoverySnapshot(ctx context.Context, tx *sql.Tx, id uuid.UUID) (conversation.RecoverySnapshot, error) {
	out := conversation.RecoverySnapshot{TaskID: id}
	err := tx.QueryRowContext(ctx, `SELECT state,step_version,
coalesce((SELECT a.state FROM channel_audit a WHERE a.tenant_id=t.tenant_id AND a.task_id=t.id
AND a.state IN ('model_running','delivery_running','tool_running') ORDER BY a.id DESC LIMIT 1),'')
FROM channel_tasks t WHERE tenant_id=? AND app_id=? AND model_revision=? AND id=?`,
		s.tenant.String(), s.app, s.revision, id.String()).Scan(&out.State, &out.Version, &out.Stage)
	if errors.Is(err, sql.ErrNoRows) {
		err = conversation.ErrTaskNotFound
	}
	return out, err
}

func (s *Inbox) InspectRecovery(ctx context.Context, actor identity.ExternalIdentity, id uuid.UUID) (conversation.RecoverySnapshot, error) {
	if actor.TenantID != s.tenant {
		return conversation.RecoverySnapshot{}, identity.ErrUnauthenticated
	}
	var out conversation.RecoverySnapshot
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := s.identities.RequireCapabilityIn(ctx, tx, actor, s.app, "task.reconcile")
		if err != nil {
			return err
		}
		out, err = s.recoverySnapshot(ctx, tx, id)
		if err != nil {
			return err
		}
		traceID := ""
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			traceID = sc.TraceID().String()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id)
VALUES(?,?,?,?,?,1,?)`, uuid.New().String(), s.tenant.String(), p.MemberID.String(), s.app, id.String(), traceID)
		return err
	})
	if err != nil {
		return conversation.RecoverySnapshot{}, err
	}
	return out, nil
}

// Evidence is supplied by an authorized human operator; no outcome is inferred.
func (s *Inbox) Reconcile(ctx context.Context, actor identity.ExternalIdentity, in conversation.Reconciliation) (conversation.RecoverySnapshot, error) {
	if err := in.Validate(); err != nil {
		return conversation.RecoverySnapshot{}, err
	}
	if actor.TenantID != s.tenant {
		return conversation.RecoverySnapshot{}, identity.ErrUnauthenticated
	}
	var out conversation.RecoverySnapshot
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := s.identities.RequireCapabilityIn(ctx, tx, actor, s.app, "task.reconcile")
		if err != nil {
			return err
		}
		out, err = s.recoverySnapshot(ctx, tx, in.TaskID)
		if err != nil {
			return err
		}
		var same bool
		var saved conversation.RecoverySnapshot
		saved.TaskID = in.TaskID
		err = tx.QueryRowContext(ctx, `SELECT member_id=? AND expected_version=? AND decision=? AND evidence_id=?,
result_state,result_version,stage FROM channel_reconciliations
WHERE tenant_id=? AND app_id=? AND task_id=? AND request_id=?`, p.MemberID.String(), in.ExpectedVersion, in.Decision,
			in.EvidenceID.String(), s.tenant.String(), s.app, in.TaskID.String(), in.RequestID.String()).
			Scan(&same, &saved.State, &saved.Version, &saved.Stage)
		if err == nil {
			if !same {
				return conversation.ErrReconcileConflict
			}
			out = saved
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if out.State != conversation.Unknown || out.Version != in.ExpectedVersion {
			return conversation.ErrReconcileConflict
		}
		target := conversation.Unknown
		switch in.Decision {
		case "not_executed":
			switch out.Stage {
			case conversation.ModelRunning:
				target = conversation.Pending
			case conversation.DeliveryRunning:
				var saved bool
				if err := tx.QueryRowContext(ctx, "SELECT coalesce(response,'')<>'' FROM channel_tasks WHERE tenant_id=? AND id=?", s.tenant.String(), in.TaskID.String()).Scan(&saved); err != nil {
					return err
				}
				if !saved {
					return conversation.ErrReconcileConflict
				}
				target = conversation.DeliveryPending
			default:
				return conversation.ErrReconcileConflict
			}
		case "delivered":
			if out.Stage != conversation.DeliveryRunning {
				return conversation.ErrReconcileConflict
			}
			target = conversation.Succeeded
		}
		_, err = tx.ExecContext(ctx, `UPDATE channel_tasks SET state=?,reason=?,step_version=step_version+1,
lease_owner=CASE WHEN ?='delivery_pending' THEN ? ELSE NULL END,
lease_until=CASE WHEN ?='delivery_pending' THEN unixepoch('subsec') ELSE NULL END WHERE tenant_id=? AND id=?`,
			string(target), "reconciled_"+in.Decision, string(target), s.owner.String(), string(target), s.tenant.String(), in.TaskID.String())
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, in.TaskID, string(target), "reconciled_"+in.Decision); err != nil {
			return err
		}
		out.State, out.Version = target, out.Version+1
		traceID := ""
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			traceID = sc.TraceID().String()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_reconciliations
(tenant_id,app_id,task_id,request_id,member_id,expected_version,decision,evidence_id,result_state,result_version,trace_id,stage)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, s.tenant.String(), s.app, in.TaskID.String(), in.RequestID.String(), p.MemberID.String(),
			in.ExpectedVersion, in.Decision, in.EvidenceID.String(), string(out.State), out.Version, traceID, string(out.Stage))
		return err
	})
	if err != nil {
		return conversation.RecoverySnapshot{}, err
	}
	return out, nil
}

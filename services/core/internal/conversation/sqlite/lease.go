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

func (s *Inbox) transaction(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if e := tx.Rollback(); e != nil && !errors.Is(e, sql.ErrTxDone) {
			err = errors.Join(err, e)
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Inbox) audit(ctx context.Context, tx *sql.Tx, id uuid.UUID, state, reason string) error {
	traceID := ""
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		traceID = sc.TraceID().String()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO channel_audit(tenant_id,task_id,state,reason,trace_id,lease_generation,step_version)
SELECT tenant_id,id,?,?,?,lease_generation,step_version FROM channel_tasks WHERE tenant_id=? AND id=?`,
		state, reason, traceID, s.tenant.String(), id.String())
	return err
}

func (s *Inbox) Claim(ctx context.Context) (*conversation.Task, error) {
	task := conversation.Task{Principal: identity.Principal{TenantID: s.tenant}}
	task.Message.Identity = identity.ExternalIdentity{TenantID: s.tenant, Provider: "feishu"}
	var tid, sid string
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `UPDATE channel_tasks SET
state=CASE WHEN state='delivery_pending' THEN state ELSE 'claimed' END,
lease_owner=?,lease_until=unixepoch('subsec')+90,lease_generation=lease_generation+1,step_version=step_version+1
WHERE tenant_id=? AND id=(SELECT id FROM channel_tasks WHERE tenant_id=? AND app_id=? AND model_revision=?
AND (state='pending' OR (state IN ('claimed','delivery_pending') AND lease_until<=unixepoch('subsec')))
ORDER BY created_at,id LIMIT 1)
RETURNING id,member_id,issuer,subject,event_id,message_id,prompt,ingress_trace_id,ingress_span_id,state,lease_generation,step_version,coalesce(response,'')`,
			s.owner.String(), s.tenant.String(), s.tenant.String(), s.app, s.revision).
			Scan(&task.ID, &task.Principal.MemberID, &task.Message.Identity.Issuer, &task.Message.Identity.Subject,
				&task.Message.EventID, &task.Message.MessageID, &task.Message.Text, &tid, &sid, &task.State, &task.Generation, &task.Version, &task.Response)
		if err != nil {
			return err
		}
		if tid != "" {
			traceID, err := trace.TraceIDFromHex(tid)
			if err != nil {
				return err
			}
			spanID, err := trace.SpanIDFromHex(sid)
			if err != nil {
				return err
			}
			task.Ingress = trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID})
		}
		return s.audit(ctx, tx, task.ID, string(task.State), "lease_claimed")
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Inbox) Authorize(ctx context.Context, task *conversation.Task) error {
	if task.Principal.TenantID != s.tenant || task.Message.Identity.TenantID != s.tenant {
		return identity.ErrUnauthenticated
	}
	p, err := s.identities.Resolve(ctx, task.Message.Identity)
	if err != nil {
		return err
	}
	if p != task.Principal {
		return identity.ErrUnauthenticated
	}
	return nil
}

func (s *Inbox) leaseArgs(t *conversation.Task) []any {
	return []any{s.tenant.String(), s.app, s.revision, t.ID.String(), s.owner.String(), t.Generation, t.Version, string(t.State)}
}

const leasePredicate = `tenant_id=? AND app_id=? AND model_revision=? AND id=?
AND lease_owner=? AND lease_generation=? AND step_version=? AND state=? AND lease_until>unixepoch('subsec')`

func changed(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return conversation.ErrLeaseLost
	}
	return nil
}

func (s *Inbox) Renew(ctx context.Context, task *conversation.Task) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		return changed(tx.ExecContext(ctx, "UPDATE channel_tasks SET lease_until=unixepoch('subsec')+90 WHERE "+leasePredicate, s.leaseArgs(task)...))
	})
}

func (s *Inbox) Transition(ctx context.Context, task *conversation.Task, to conversation.State, reason, response string) error {
	if !conversation.ValidTransition(task.State, to) ||
		(to == conversation.DeliveryPending && response == "") ||
		(to != conversation.DeliveryPending && response != "") ||
		task.State == conversation.ToolPending || task.State == conversation.ToolRunning {
		return conversation.ErrInvalidTransition
	}
	terminal := to == conversation.Succeeded || to == conversation.Failed || to == conversation.Unknown
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		args := []any{string(to), reason, string(to), response, terminal, terminal}
		args = append(args, s.leaseArgs(task)...)
		err := changed(tx.ExecContext(ctx, `UPDATE channel_tasks SET state=?,reason=?,
response=CASE WHEN ?='delivery_pending' THEN ? ELSE response END,
lease_owner=CASE WHEN ? THEN NULL ELSE lease_owner END,
lease_until=CASE WHEN ? THEN NULL ELSE unixepoch('subsec')+90 END,
step_version=step_version+1 WHERE `+leasePredicate, args...))
		if err != nil {
			return err
		}
		return s.audit(ctx, tx, task.ID, string(to), reason)
	})
	if err == nil {
		task.State = to
		task.Version++
		if to == conversation.DeliveryPending {
			task.Response = response
		}
	}
	return err
}

func (s *Inbox) RecoverExpired(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `UPDATE channel_tasks SET state='unknown',reason='lease_expired_external_intent',
lease_owner=NULL,lease_until=NULL,step_version=step_version+1
WHERE tenant_id=? AND id IN (SELECT id FROM channel_tasks WHERE tenant_id=? AND app_id=? AND model_revision=?
AND state IN ('model_running','delivery_running') AND lease_until<=unixepoch('subsec') ORDER BY lease_until,id LIMIT 100)
RETURNING id`, s.tenant.String(), s.tenant.String(), s.app, s.revision)
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
			if err := s.audit(ctx, tx, id, "unknown", "lease_expired_external_intent"); err != nil {
				return err
			}
		}
		return nil
	})
}

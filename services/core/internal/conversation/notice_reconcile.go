package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type NoticeSnapshot struct {
	TaskID   uuid.UUID `json:"task_id"`
	NoticeID uuid.UUID `json:"notice_id"`
	Version  int64     `json:"version"`
	State    string    `json:"state"`
}
type NoticeReconciliation struct {
	TaskID          uuid.UUID `json:"task_id"`
	NoticeID        uuid.UUID `json:"notice_id"`
	RequestID       uuid.UUID `json:"request_id"`
	ExpectedVersion int64     `json:"expected_version"`
	Decision        string    `json:"decision"`
	EvidenceID      uuid.UUID `json:"evidence_id"`
}

func (s *Store) noticeSnapshot(ctx context.Context, q database.Queries, task, id uuid.UUID) (NoticeSnapshot, error) {
	out := NoticeSnapshot{TaskID: task, NoticeID: id}
	if _, err := s.recoverySnapshot(ctx, q, task); err != nil {
		return out, err
	}
	err := q.QueryRow(ctx, `SELECT state,version FROM channel.approval_notices WHERE tenant_id=$1 AND task_id=$2 AND id=$3`, s.tenant, task, id).Scan(&out.State, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrTaskNotFound
	}
	return out, err
}

// Recheck after the task lock; authorization before a lock wait is not sufficient.
func (s *Store) reconcileOperator(ctx context.Context, q database.Queries, actor identity.ExternalIdentity) error {
	if _, err := q.Exec(ctx, `SELECT set_config('agentos.provider',$1,true),set_config('agentos.issuer',$2,true),set_config('agentos.subject',$3,true)`, actor.Provider, actor.Issuer, actor.Subject); err != nil {
		return err
	}
	var authenticated, allowed bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.identity_links WHERE tenant_id=$1 AND member_id=current_setting('agentos.member_id')::uuid AND provider=$3 AND issuer=$4 AND subject=$5),EXISTS(SELECT 1 FROM iam.operation_grants WHERE tenant_id=$1 AND member_id=current_setting('agentos.member_id')::uuid AND app_id=$2 AND capability='task.reconcile' AND enabled)`, s.tenant, s.app, actor.Provider, actor.Issuer, actor.Subject).Scan(&authenticated, &allowed)
	if err != nil {
		return err
	}
	if !authenticated {
		return identity.ErrUnauthenticated
	}
	if !allowed {
		return identitypg.ErrForbidden
	}
	return nil
}

func (s *Store) InspectNotice(ctx context.Context, actor identity.ExternalIdentity, task, id uuid.UUID) (NoticeSnapshot, error) {
	if task == uuid.Nil || id == uuid.Nil {
		return NoticeSnapshot{}, ErrInvalidReconciliation
	}
	var out NoticeSnapshot
	err := s.Operation(ctx, actor, "task.reconcile", func(q database.Queries) error {
		var err error
		out, err = s.noticeSnapshot(ctx, q, task, id)
		if err != nil {
			return err
		}
		if err = s.reconcileOperator(ctx, q, actor); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO channel.audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id) VALUES($1,$2,current_setting('agentos.member_id')::uuid,$3,$4,1,$5)`, uuid.New(), s.tenant, s.app, task, spanTraceID(ctx))
		return err
	})
	if err != nil {
		return NoticeSnapshot{}, err
	}
	return out, nil
}

func (s *Store) ReconcileNotice(ctx context.Context, actor identity.ExternalIdentity, in NoticeReconciliation) (NoticeSnapshot, error) {
	if in.TaskID == uuid.Nil || in.NoticeID == uuid.Nil || in.RequestID == uuid.Nil || in.EvidenceID == uuid.Nil || in.ExpectedVersion < 1 || (in.Decision != "unresolved" && in.Decision != "not_executed" && in.Decision != "delivered") {
		return NoticeSnapshot{}, ErrInvalidReconciliation
	}
	raw, err := json.Marshal(struct {
		Actor identity.ExternalIdentity
		Input NoticeReconciliation
	}{actor, in})
	if err != nil {
		return NoticeSnapshot{}, err
	}
	fingerprint := contentHash(string(raw))
	var out NoticeSnapshot
	err = s.Operation(ctx, actor, "task.reconcile", func(q database.Queries) error {
		var err error
		out, err = s.noticeSnapshot(ctx, q, in.TaskID, in.NoticeID)
		if err != nil {
			return err
		}
		if err = s.reconcileOperator(ctx, q, actor); err != nil {
			return err
		}
		var same bool
		var saved NoticeSnapshot
		err = q.QueryRow(ctx, `SELECT member_id=current_setting('agentos.member_id')::uuid AND request_hash=$5,result FROM channel.notice_reconciliations WHERE tenant_id=$1 AND task_id=$2 AND notice_id=$3 AND request_id=$4`, s.tenant, in.TaskID, in.NoticeID, in.RequestID, fingerprint).Scan(&same, &saved)
		if err == nil {
			if !same {
				return ErrReconcileConflict
			}
			out = saved
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if out.State != "unknown" || out.Version != in.ExpectedVersion {
			return ErrReconcileConflict
		}
		target := "unknown"
		if in.Decision == "delivered" {
			target = "sent"
		}
		if in.Decision == "not_executed" {
			target = "pending"
		}
		if _, err = q.Exec(ctx, `UPDATE channel.approval_notices SET state=$4,version=version+1 WHERE tenant_id=$1 AND task_id=$2 AND id=$3`, s.tenant, in.TaskID, in.NoticeID, target); err != nil {
			return err
		}
		out.State = target
		out.Version++
		if _, err = q.Exec(ctx, `INSERT INTO channel.notice_reconciliations VALUES($1,$2,$3,$4,current_setting('agentos.member_id')::uuid,$5,$6,$7,$8,$9)`, s.tenant, in.TaskID, in.NoticeID, in.RequestID, fingerprint, out, in.EvidenceID, in.Decision, in.ExpectedVersion); err != nil {
			return err
		}
		return s.noticeAudit(ctx, q, in.TaskID, in.NoticeID, "reconciled_"+in.Decision)
	})
	if err != nil {
		return NoticeSnapshot{}, err
	}
	return out, nil
}

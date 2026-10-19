package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrReconcileConflict = errors.New("reconciliation conflict")
var ErrTaskNotFound = errors.New("task not found")
var ErrInvalidReconciliation = errors.New("invalid reconciliation")

type Reconciliation struct {
	TaskID          uuid.UUID `json:"task_id"`
	RequestID       uuid.UUID `json:"request_id"`
	ExpectedVersion int64     `json:"expected_version"`
	Decision        string    `json:"decision"`
	EvidenceID      uuid.UUID `json:"evidence_id"`
}
type RecoverySnapshot struct {
	TaskID  uuid.UUID `json:"task_id"`
	State   State     `json:"state"`
	Version int64     `json:"version"`
	Stage   State     `json:"stage"`
}

func (s *Store) recoverySnapshot(ctx context.Context, q database.Queries, id uuid.UUID) (RecoverySnapshot, error) {
	out := RecoverySnapshot{TaskID: id}
	err := q.QueryRow(ctx, `SELECT state,step_version,coalesce((SELECT a.state FROM channel.audit a WHERE a.tenant_id=t.tenant_id AND a.task_id=t.id AND a.state IN ('model_running','delivery_running','tool_running') ORDER BY a.id DESC LIMIT 1),'') FROM channel.tasks t WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4 FOR UPDATE`, s.tenant, s.app, s.revision, id).Scan(&out.State, &out.Version, &out.Stage)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrTaskNotFound
	}
	return out, err
}

func (s *Store) InspectRecovery(ctx context.Context, actor identity.ExternalIdentity, id uuid.UUID) (RecoverySnapshot, error) {
	var out RecoverySnapshot
	err := s.Operation(ctx, actor, "task.reconcile", func(q database.Queries) error {
		var err error
		out, err = s.recoverySnapshot(ctx, q, id)
		if err != nil {
			return err
		}
		if err = s.reconcileOperator(ctx, q, actor); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO channel.audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id) VALUES($1,$2,current_setting('agentos.member_id')::uuid,$3,$4,1,$5)`, uuid.New(), s.tenant, s.app, id, spanTraceID(ctx))
		return err
	})
	if err != nil {
		return RecoverySnapshot{}, err
	}
	return out, nil
}

// Evidence is an operator attestation reference, never an assertion inferred by the model.
func (s *Store) Reconcile(ctx context.Context, actor identity.ExternalIdentity, in Reconciliation) (RecoverySnapshot, error) {
	if in.TaskID == uuid.Nil || in.RequestID == uuid.Nil || in.EvidenceID == uuid.Nil || in.ExpectedVersion < 1 {
		return RecoverySnapshot{}, ErrInvalidReconciliation
	}
	switch in.Decision {
	case "unresolved", "not_executed", "delivered":
	default:
		return RecoverySnapshot{}, ErrInvalidReconciliation
	}
	var out RecoverySnapshot
	err := s.Operation(ctx, actor, "task.reconcile", func(q database.Queries) error {
		var err error
		out, err = s.recoverySnapshot(ctx, q, in.TaskID)
		if err != nil {
			return err
		}
		if err = s.reconcileOperator(ctx, q, actor); err != nil {
			return err
		}
		var same bool
		var savedState, savedStage State
		var savedVersion int64
		err = q.QueryRow(ctx, `SELECT expected_version=$5 AND decision=$6 AND evidence_id=$7,result_state,result_version,stage FROM channel.reconciliations WHERE tenant_id=$1 AND app_id=$2 AND task_id=$3 AND request_id=$4`, s.tenant, s.app, in.TaskID, in.RequestID, in.ExpectedVersion, in.Decision, in.EvidenceID).Scan(&same, &savedState, &savedVersion, &savedStage)
		if err == nil {
			if !same {
				return ErrReconcileConflict
			}
			out.Stage = savedStage
			out.State = savedState
			out.Version = savedVersion
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if out.State != Unknown || out.Version != in.ExpectedVersion {
			return ErrReconcileConflict
		}
		target := Unknown
		switch in.Decision {
		case "not_executed":
			switch out.Stage {
			case ModelRunning:
				target = Pending
			case DeliveryRunning:
				var saved bool
				if err := q.QueryRow(ctx, "SELECT coalesce(response,'')<>'' FROM channel.tasks WHERE tenant_id=$1 AND id=$2", s.tenant, in.TaskID).Scan(&saved); err != nil {
					return err
				}
				if !saved {
					return ErrReconcileConflict
				}
				target = DeliveryPending
			default:
				return ErrReconcileConflict
			}
		case "delivered":
			if out.Stage != DeliveryRunning {
				return ErrReconcileConflict
			}
			target = Succeeded
		}
		// An expired checkpoint lease is immediately claimable; the next owner increments the fence.
		_, err = q.Exec(ctx, `UPDATE channel.tasks SET state=$3,reason=$4,step_version=step_version+1,updated_at=clock_timestamp(),lease_owner=CASE WHEN $3='delivery_pending' THEN $5::uuid ELSE NULL END,lease_until=CASE WHEN $3='delivery_pending' THEN clock_timestamp() ELSE NULL END WHERE tenant_id=$1 AND id=$2`, s.tenant, in.TaskID, target, "reconciled_"+in.Decision, s.owner)
		if err != nil {
			return err
		}
		if err = audit(ctx, q, s.tenant, in.TaskID, string(target), "reconciled_"+in.Decision); err != nil {
			return err
		}
		out.State = target
		out.Version++
		_, err = q.Exec(ctx, `INSERT INTO channel.reconciliations(tenant_id,app_id,task_id,request_id,member_id,expected_version,decision,evidence_id,result_state,result_version,trace_id,stage) VALUES($1,$2,$3,$4,current_setting('agentos.member_id')::uuid,$5,$6,$7,$8,$9,$10,$11)`, s.tenant, s.app, in.TaskID, in.RequestID, in.ExpectedVersion, in.Decision, in.EvidenceID, out.State, out.Version, spanTraceID(ctx), out.Stage)
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "reconciliations_pkey" {
			return RecoverySnapshot{}, ErrReconcileConflict
		}
		return RecoverySnapshot{}, err
	}
	return out, nil
}

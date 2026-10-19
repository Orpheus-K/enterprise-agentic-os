package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

var ErrApprovalDenied = errors.New("approval is not currently valid")
var ErrApprovalConflict = errors.New("approval version or decision conflict")
var ErrInvalidApproval = errors.New("invalid approval request")

type ApprovalPolicy struct {
	NotifySubject string    `json:"notify_subject,omitempty"`
	MemberID      uuid.UUID `json:"member_id"`
	TTLSeconds    int       `json:"ttl_seconds"`
}

func (p ApprovalPolicy) Validate() error {
	if p.NotifySubject != "" && (!strings.HasPrefix(p.NotifySubject, "ou_") || len(p.NotifySubject) > 128 || strings.ContainsAny(p.NotifySubject, " \r\n\t")) {
		return ErrInvalidApproval
	}
	if p.MemberID == uuid.Nil || p.TTLSeconds < 1 || p.TTLSeconds > 604800 {
		return ErrInvalidApproval
	}
	return nil
}

// Copy configuration before accepting messages; persisted tasks keep their original policy.
func (s *Store) WithApproval(p ApprovalPolicy) (*Store, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	copy := *s
	copy.approval = &p
	return &copy, nil
}
func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

type Approval struct {
	QuoteVersion  int64     `json:"quote_version,omitempty"`
	TaskID        uuid.UUID `json:"task_id"`
	ID            uuid.UUID `json:"approval_id"`
	Revision      string    `json:"revision"`
	Version       int64     `json:"version"`
	ContentHash   string    `json:"content_hash"`
	ExpiresAt     time.Time `json:"expires_at"`
	Status        string    `json:"status"`
	Response      string    `json:"response,omitempty"`
	ResultVersion int64     `json:"result_version,omitempty"`
}
type ApprovalDecision struct {
	TaskID      uuid.UUID `json:"task_id"`
	ID          uuid.UUID `json:"approval_id"`
	RequestID   uuid.UUID `json:"request_id"`
	Revision    string    `json:"revision"`
	Version     int64     `json:"version"`
	ContentHash string    `json:"content_hash"`
	Decision    string    `json:"decision"`
}

// Suspend only an owned pre-delivery checkpoint. Waiting owns no execution lease.
func (s *Store) SuspendApproval(ctx context.Context, t *Task) (bool, error) {
	waiting := false
	err := s.scoped(ctx, func(q database.Queries) error {
		if err := s.lockTask(ctx, q, t); err != nil {
			return err
		}
		var required, valid, exists bool
		var member uuid.UUID
		var ttl int
		var response string
		err := q.QueryRow(ctx, `SELECT approval_member IS NOT NULL,coalesce(approval_member,'00000000-0000-0000-0000-000000000000'),coalesce(approval_ttl,0),coalesce(response,''),state='delivery_pending' AND lease_owner=$5 AND lease_generation=$6 AND step_version=$7 AND lease_until>clock_timestamp(),EXISTS(SELECT 1 FROM channel.approvals WHERE tenant_id=$1 AND task_id=$4) FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4`, s.tenant, s.app, s.revision, t.ID, s.owner, t.Generation, t.Version).Scan(&required, &member, &ttl, &response, &valid, &exists)
		if err != nil {
			return err
		}
		if !valid {
			return ErrLeaseLost
		}
		if !required || exists {
			return nil
		}
		if response == "" {
			return ErrApprovalDenied
		}
		_, err = q.Exec(ctx, `INSERT INTO channel.approvals(tenant_id,task_id,id,member_id,revision,content_hash,waiting_version,expires_at,status) VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp()+$8*interval '1 second','pending')`, s.tenant, t.ID, uuid.New(), member, s.revision, contentHash(response), t.Version+1, ttl)
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `UPDATE channel.tasks SET state='waiting_approval',lease_owner=NULL,lease_until=NULL,step_version=step_version+1,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, s.tenant, t.ID)
		if err != nil {
			return err
		}
		if err = audit(ctx, q, s.tenant, t.ID, "waiting_approval", "approval_requested"); err != nil {
			return err
		}
		if err = s.enqueueNotice(ctx, q, t.ID); err != nil {
			return err
		}
		waiting = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if waiting {
		t.State = WaitingApproval
		t.Version++
	}
	return waiting, nil
}

// Lock the task before calling this function, including at the final send-intent boundary.
func (s *Store) checkApproval(ctx context.Context, q database.Queries, id uuid.UUID) error {
	var required bool
	var response string
	if err := q.QueryRow(ctx, "SELECT approval_member IS NOT NULL,coalesce(response,'') FROM channel.tasks WHERE tenant_id=$1 AND id=$2", s.tenant, id).Scan(&required, &response); err != nil {
		return err
	}
	if !required {
		return nil
	}
	var member uuid.UUID
	var hash, provider, issuer, subject string
	var valid bool
	err := q.QueryRow(ctx, `SELECT member_id,content_hash,status='approved' AND revision=$3 AND expires_at>clock_timestamp(),coalesce(actor_provider,''),coalesce(actor_issuer,''),coalesce(actor_subject,'') FROM channel.approvals WHERE tenant_id=$1 AND task_id=$2`, s.tenant, id, s.revision).Scan(&member, &hash, &valid, &provider, &issuer, &subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrApprovalDenied
	}
	if err != nil {
		return err
	}
	if !valid || hash != contentHash(response) {
		return ErrApprovalDenied
	}
	_, err = q.Exec(ctx, `SELECT set_config('agentos.member_id',$1,true),set_config('agentos.provider',$2,true),set_config('agentos.issuer',$3,true),set_config('agentos.subject',$4,true)`, member.String(), provider, issuer, subject)
	if err != nil {
		return err
	}
	err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.operation_grants WHERE tenant_id=$1 AND member_id=$2 AND app_id=$3 AND capability='task.approve' AND enabled) AND EXISTS(SELECT 1 FROM iam.identity_links WHERE tenant_id=$1 AND member_id=$2 AND provider=$4 AND issuer=$5 AND subject=$6)`, s.tenant, member, s.app, provider, issuer, subject).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrApprovalDenied
	}
	return nil
}

func (s *Store) readApproval(ctx context.Context, q database.Queries, id uuid.UUID) (Approval, uuid.UUID, error) {
	out := Approval{TaskID: id}
	var member uuid.UUID
	// All callers lock the task first: expiry, callbacks and workers share one lock order.
	err := q.QueryRow(ctx, `SELECT a.id,a.member_id,a.revision,a.content_hash,a.waiting_version,a.expires_at,a.status,coalesce(a.result_version,0),coalesce(t.response,''),coalesce(t.quote_version,0) FROM channel.approvals a JOIN channel.tasks t ON (t.tenant_id,t.id)=(a.tenant_id,a.task_id) WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.model_revision=$3 AND t.id=$4`, s.tenant, s.app, s.revision, id).Scan(&out.ID, &member, &out.Revision, &out.ContentHash, &out.Version, &out.ExpiresAt, &out.Status, &out.ResultVersion, &out.Response, &out.QuoteVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrTaskNotFound
	}
	return out, member, err
}
func (s *Store) approvalActor(ctx context.Context, q database.Queries, actor identity.ExternalIdentity, member uuid.UUID) error {
	var same bool
	if err := q.QueryRow(ctx, "SELECT current_setting('agentos.member_id')::uuid=$1", member).Scan(&same); err != nil {
		return err
	}
	if !same {
		return identitypg.ErrForbidden
	}
	// Recheck after taking the task lock: a callback may have waited behind a worker.
	if _, err := q.Exec(ctx, `SELECT set_config('agentos.provider',$1,true),set_config('agentos.issuer',$2,true),set_config('agentos.subject',$3,true)`, actor.Provider, actor.Issuer, actor.Subject); err != nil {
		return err
	}
	var authenticated, allowed bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.identity_links WHERE tenant_id=$1 AND member_id=$2 AND provider=$4 AND issuer=$5 AND subject=$6) AND EXISTS(SELECT 1 FROM iam.members WHERE tenant_id=$1 AND id=$2),EXISTS(SELECT 1 FROM iam.operation_grants WHERE tenant_id=$1 AND member_id=$2 AND app_id=$3 AND capability='task.approve' AND enabled)`, s.tenant, member, s.app, actor.Provider, actor.Issuer, actor.Subject).Scan(&authenticated, &allowed)
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
func (s *Store) InspectApproval(ctx context.Context, actor identity.ExternalIdentity, id uuid.UUID) (Approval, error) {
	var out Approval
	err := s.Operation(ctx, actor, "task.approve", func(q database.Queries) error {
		if _, err := s.recoverySnapshot(ctx, q, id); err != nil {
			return err
		}
		var member uuid.UUID
		var err error
		out, member, err = s.readApproval(ctx, q, id)
		if err != nil {
			return err
		}
		if err = s.approvalActor(ctx, q, actor, member); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO channel.audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id) VALUES($1,$2,$3,$4,$5,1,$6)`, uuid.New(), s.tenant, member, s.app, id, spanTraceID(ctx))
		return err
	})
	if err != nil {
		return Approval{}, err
	}
	return out, nil
}
func (s *Store) DecideApproval(ctx context.Context, actor identity.ExternalIdentity, in ApprovalDecision) (Approval, error) {
	if in.TaskID == uuid.Nil || in.ID == uuid.Nil || in.RequestID == uuid.Nil || in.Version < 1 || in.Revision == "" || len(in.ContentHash) != 64 || (in.Decision != "approve" && in.Decision != "reject") {
		return Approval{}, ErrInvalidApproval
	}
	var out Approval
	err := s.Operation(ctx, actor, "task.approve", func(q database.Queries) error {
		snapshot, err := s.recoverySnapshot(ctx, q, in.TaskID)
		if err != nil {
			return err
		}
		var member uuid.UUID
		out, member, err = s.readApproval(ctx, q, in.TaskID)
		if err != nil {
			return err
		}
		if err = s.approvalActor(ctx, q, actor, member); err != nil {
			return err
		}
		if out.ID != in.ID || out.Revision != in.Revision || out.Version != in.Version || out.ContentHash != in.ContentHash || contentHash(out.Response) != in.ContentHash {
			return ErrApprovalConflict
		}
		status := "approved"
		target := DeliveryPending
		if in.Decision == "reject" {
			status = "rejected"
			target = Rejected
		}
		if out.Status != "pending" {
			var same bool
			if err = q.QueryRow(ctx, `SELECT coalesce(request_id=$3 AND status=$4 AND actor_provider=$5 AND actor_issuer=$6 AND actor_subject=$7,false) FROM channel.approvals WHERE tenant_id=$1 AND task_id=$2`, s.tenant, in.TaskID, in.RequestID, status, actor.Provider, actor.Issuer, actor.Subject).Scan(&same); err != nil {
				return err
			}
			if !same {
				return ErrApprovalConflict
			}
			return nil
		}
		if snapshot.State != WaitingApproval || snapshot.Version != in.Version {
			return ErrApprovalConflict
		}
		var active bool
		if err = q.QueryRow(ctx, "SELECT expires_at>clock_timestamp() FROM channel.approvals WHERE tenant_id=$1 AND task_id=$2", s.tenant, in.TaskID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrApprovalConflict
		}
		_, err = q.Exec(ctx, `UPDATE channel.tasks SET state=$3,reason=$4,lease_owner=CASE WHEN $3='delivery_pending' THEN $5::uuid ELSE NULL END,lease_until=CASE WHEN $3='delivery_pending' THEN clock_timestamp() ELSE NULL END,step_version=step_version+1,updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, s.tenant, in.TaskID, target, "approval_"+status, s.owner)
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `UPDATE channel.approvals SET status=$3,request_id=$4,result_version=$5,actor_provider=$6,actor_issuer=$7,actor_subject=$8 WHERE tenant_id=$1 AND task_id=$2`, s.tenant, in.TaskID, status, in.RequestID, in.Version+1, actor.Provider, actor.Issuer, actor.Subject)
		if err != nil {
			return err
		}
		if err = audit(ctx, q, s.tenant, in.TaskID, string(target), "approval_"+status); err != nil {
			return err
		}
		out.Status = status
		out.ResultVersion = in.Version + 1
		return nil
	})
	if err != nil {
		return Approval{}, err
	}
	out.Response = ""
	return out, nil
}
func (s *Store) ExpireApprovals(ctx context.Context) error {
	return s.scoped(ctx, func(q database.Queries) error {
		_, err := q.Exec(ctx, `WITH due AS (
 SELECT t.tenant_id,t.id FROM channel.tasks t JOIN channel.approvals a ON (a.tenant_id,a.task_id)=(t.tenant_id,t.id)
 WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.model_revision=$3 AND t.state='waiting_approval' AND a.status='pending' AND a.expires_at<=clock_timestamp()
 ORDER BY a.expires_at,t.id FOR UPDATE OF t SKIP LOCKED LIMIT 100
 ), ended AS (
 UPDATE channel.tasks t SET state='expired',reason='approval_expired',step_version=step_version+1,updated_at=clock_timestamp() FROM due d WHERE (t.tenant_id,t.id)=(d.tenant_id,d.id) RETURNING t.*
 ), marked AS (
 UPDATE channel.approvals a SET status='expired' FROM ended e WHERE (a.tenant_id,a.task_id)=(e.tenant_id,e.id)
 ) INSERT INTO channel.audit(tenant_id,task_id,state,reason,trace_id,lease_generation,step_version) SELECT tenant_id,id,state,reason,$4,lease_generation,step_version FROM ended`, s.tenant, s.app, s.revision, spanTraceID(ctx))
		return err
	})
}

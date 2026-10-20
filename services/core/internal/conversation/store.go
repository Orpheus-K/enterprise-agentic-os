package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/skills"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/trace"
)

type Message struct {
	EventID, MessageID, Text, Fingerprint string
	Identity                              identity.ExternalIdentity
}
type Task struct {
	KnowledgeRefs       []knowledge.Reference
	QuoteVersion        int64
	Routes              []skills.Ref
	History             []model.Message
	Tool                *ToolInvocation
	ToolSteps           int
	Skill               skills.Ref
	ID                  uuid.UUID
	Principal           identity.Principal
	Message             Message
	Ingress             trace.SpanContext
	State               State
	Generation, Version int64
	Response            string
}
type Store struct {
	quotationWASM bool
	db            *database.DB
	identities    *identitypg.Repository
	tenant        uuid.UUID
	app, revision string
	owner         uuid.UUID
	approval      *ApprovalPolicy
	skills        *skills.Registry
	selectedSkill skills.Ref
	routes        []skills.Ref
	retrieval     *retrieval
}

func NewStore(db *database.DB, tenant uuid.UUID, app, revision string) *Store {
	return &Store{db: db, identities: identitypg.New(db), tenant: tenant, app: app, revision: revision, owner: uuid.New()}
}

func (s *Store) Enqueue(ctx context.Context, m Message) error {
	if m.Identity.TenantID != s.tenant {
		return identity.ErrUnauthenticated
	}
	p, err := s.identities.Resolve(ctx, m.Identity)
	if err != nil {
		return err
	}
	var routeRefs []skills.Ref
	if len(s.routes) > 0 {
		summaries, err := s.routeSummaries(ctx, p, s.routes)
		if err != nil {
			return err
		}
		for _, summary := range summaries {
			routeRefs = append(routeRefs, summary.Ref)
		}
	}
	routeJSON := []byte("[]")
	if len(routeRefs) > 0 {
		routeJSON, err = json.Marshal(routeRefs)
		if err != nil {
			return err
		}
	}
	return s.identities.Within(ctx, p, func(q database.Queries) error {
		id := uuid.New()
		spanID := ""
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			spanID = sc.SpanID().String()
		}
		var skillName, skillRevision any
		if s.selectedSkill.Name != "" {
			skillName = s.selectedSkill.Name
			skillRevision = s.selectedSkill.Revision
		}
		var recipient any
		var approver any
		var ttl any
		if s.approval != nil {
			if s.approval.NotifySubject != "" {
				recipient = s.approval.NotifySubject
			}
			approver = s.approval.MemberID
			ttl = s.approval.TTLSeconds
		}
		result, err := q.Exec(ctx, `INSERT INTO channel.tasks(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,ingress_trace_id,ingress_span_id,approval_member,approval_ttl,skill_name,skill_revision,skill_routes,approval_recipient)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) ON CONFLICT DO NOTHING`, s.tenant, id, p.MemberID, s.app, m.Identity.Issuer, m.Identity.Subject, m.EventID, m.MessageID, m.Fingerprint, s.revision, m.Text, spanTraceID(ctx), spanID, approver, ttl, skillName, skillRevision, routeJSON, recipient)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 1 {
			for _, ref := range routeRefs {
				if err := s.skillPermission(ctx, q, p.MemberID, ref); err != nil {
					return err
				}
			}
			if s.selectedSkill.Name != "" {
				if err := s.skillPermission(ctx, q, p.MemberID, s.selectedSkill); err != nil {
					return err
				}
			}
			return audit(ctx, q, s.tenant, id, "pending", "")
		}
		var same bool
		err = q.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(fingerprint=$5 AND member_id=$6 AND message_id=$4) FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND (event_id=$3 OR message_id=$4)`, s.tenant, s.app, m.EventID, m.MessageID, m.Fingerprint, p.MemberID).Scan(&same)
		if err != nil {
			return err
		}
		if !same {
			return errors.New("conflicting channel replay")
		}
		return nil
	})
}

func (s *Store) scoped(ctx context.Context, fn func(database.Queries) error) error {
	return s.db.Transaction(ctx, func(q database.Queries) error {
		if _, err := q.Exec(ctx, "SELECT set_config('agentos.tenant_id',$1,true)", s.tenant.String()); err != nil {
			return err
		}
		return fn(q)
	})
}

func (s *Store) Claim(ctx context.Context) (*Task, error) {
	var task Task
	task.Principal.TenantID = s.tenant
	task.Message.Identity = identity.ExternalIdentity{TenantID: s.tenant, Provider: "feishu"}
	var ingressTrace, ingressSpan string
	err := s.scoped(ctx, func(q database.Queries) error {
		err := q.QueryRow(ctx, `UPDATE channel.tasks SET state=CASE WHEN state IN ('delivery_pending','tool_pending','tool_running') THEN state ELSE 'claimed' END,
 lease_owner=$4,lease_until=clock_timestamp()+interval '90 seconds',lease_generation=lease_generation+1,step_version=step_version+1,updated_at=clock_timestamp()
 WHERE (tenant_id,id)=(SELECT tenant_id,id FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3
 AND (state='pending' OR (state IN ('claimed','delivery_pending','tool_pending','tool_running') AND lease_until<=clock_timestamp()))
 ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 RETURNING id,member_id,issuer,subject,event_id,message_id,prompt,ingress_trace_id,ingress_span_id,state,lease_generation,step_version,coalesce(response,''),coalesce(skill_name,''),coalesce(skill_revision,''),tool_history,tool_invocation,tool_steps,skill_routes,coalesce(quote_version,0),knowledge_refs`,
			s.tenant, s.app, s.revision, s.owner).Scan(&task.ID, &task.Principal.MemberID, &task.Message.Identity.Issuer, &task.Message.Identity.Subject, &task.Message.EventID, &task.Message.MessageID, &task.Message.Text, &ingressTrace, &ingressSpan, &task.State, &task.Generation, &task.Version, &task.Response, &task.Skill.Name, &task.Skill.Revision, &task.History, &task.Tool, &task.ToolSteps, &task.Routes, &task.QuoteVersion, &task.KnowledgeRefs)
		if err != nil {
			return err
		}
		return audit(ctx, q, s.tenant, task.ID, string(task.State), "lease_claimed")
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ingressTrace != "" {
		tid, err := trace.TraceIDFromHex(ingressTrace)
		if err != nil {
			return nil, err
		}
		sid, err := trace.SpanIDFromHex(ingressSpan)
		if err != nil {
			return nil, err
		}
		task.Ingress = trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid})
	}
	return &task, nil
}

func (s *Store) Authorize(ctx context.Context, t *Task) error {
	p, err := s.identities.Resolve(ctx, t.Message.Identity)
	if err != nil {
		return err
	}
	if p != t.Principal {
		return identity.ErrUnauthenticated
	}
	return s.scoped(ctx, func(q database.Queries) error {
		return s.checkTaskKnowledge(ctx, q, t.ID, nil, uuid.Nil)
	})
}

func (s *Store) Transition(ctx context.Context, t *Task, to State, reason, response string) error {
	if !validTransition(t.State, to) || (to == DeliveryPending && response == "") || (to != DeliveryPending && response != "") {
		return ErrInvalidTransition
	}
	terminal := to == Succeeded || to == Failed || to == Unknown
	err := s.scoped(ctx, func(q database.Queries) error {
		if err := s.lockTask(ctx, q, t); err != nil {
			return err
		}
		if to == ModelRunning || to == DeliveryRunning {
			if err := s.checkTaskSkill(ctx, q, t.ID); err != nil {
				return err
			}
		}
		if to == DeliveryRunning {
			if err := s.checkApproval(ctx, q, t.ID); err != nil {
				return err
			}
		}
		if to == ModelRunning || to == ToolRunning || to == DeliveryRunning {
			if err := s.checkTaskKnowledge(ctx, q, t.ID, nil, uuid.Nil); err != nil {
				return err
			}
		}
		result, err := q.Exec(ctx, `UPDATE channel.tasks SET state=$8,reason=$9,
 response=CASE WHEN $8='delivery_pending' THEN $10 ELSE response END,
 lease_owner=CASE WHEN $11 THEN NULL ELSE lease_owner END,
 lease_until=CASE WHEN $11 THEN NULL ELSE clock_timestamp()+interval '90 seconds' END,
 step_version=step_version+1,updated_at=clock_timestamp()
 WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4 AND lease_owner=$5
 AND lease_generation=$6 AND step_version=$7 AND state=$12 AND lease_until>clock_timestamp()`,
			s.tenant, s.app, s.revision, t.ID, s.owner, t.Generation, t.Version, to, reason, response, terminal, t.State)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return audit(ctx, q, s.tenant, t.ID, string(to), reason)
	})
	if err == nil {
		t.State = to
		t.Version++
		if to == DeliveryPending {
			t.Response = response
		}
	}
	return err
}

// Take the row lock before checking database time: a lock wait must not revive a lease.
func (s *Store) lockTask(ctx context.Context, q database.Queries, t *Task) error {
	var id uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4 FOR UPDATE`, s.tenant, s.app, s.revision, t.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

// Renew cannot revive an expired owner or cross a committed step boundary.
func (s *Store) Renew(ctx context.Context, t *Task) error {
	return s.scoped(ctx, func(q database.Queries) error {
		if err := s.lockTask(ctx, q, t); err != nil {
			return err
		}
		if err := s.checkTaskKnowledge(ctx, q, t.ID, nil, uuid.Nil); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `UPDATE channel.tasks SET lease_until=clock_timestamp()+interval '90 seconds'
 WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4 AND lease_owner=$5 AND lease_generation=$6 AND step_version=$7 AND state=$8 AND lease_until>clock_timestamp()`, s.tenant, s.app, s.revision, t.ID, s.owner, t.Generation, t.Version, t.State)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

// An expired intent may have reached the provider. Reconciliation never replays it.
func (s *Store) RecoverExpired(ctx context.Context) error {
	return s.scoped(ctx, func(q database.Queries) error {
		_, err := q.Exec(ctx, `WITH expired AS (
 SELECT tenant_id,id FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3
 AND state IN ('model_running','delivery_running') AND lease_until<=clock_timestamp()
 ORDER BY lease_until,id FOR UPDATE SKIP LOCKED LIMIT 100
 ), recovered AS (
 UPDATE channel.tasks t SET state='unknown',reason='lease_expired_external_intent',lease_owner=NULL,lease_until=NULL,
 step_version=step_version+1,updated_at=clock_timestamp() FROM expired e WHERE t.tenant_id=e.tenant_id AND t.id=e.id
 RETURNING t.tenant_id,t.id,t.state,t.reason,t.lease_generation,t.step_version
 ) INSERT INTO channel.audit(tenant_id,task_id,state,reason,trace_id,lease_generation,step_version)
 SELECT tenant_id,id,state,reason,$4,lease_generation,step_version FROM recovered`, s.tenant, s.app, s.revision, spanTraceID(ctx))
		return err
	})
}

func audit(ctx context.Context, q database.Queries, tenant, id uuid.UUID, state, reason string) error {
	_, err := q.Exec(ctx, "INSERT INTO channel.audit(tenant_id,task_id,state,reason,trace_id,lease_generation,step_version) SELECT $1,$2,$3,$4,$5,lease_generation,step_version FROM channel.tasks WHERE tenant_id=$1 AND id=$2", tenant, id, state, reason, spanTraceID(ctx))
	return err
}

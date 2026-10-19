package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"context"
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
	ID        uuid.UUID
	Principal identity.Principal
	Message   Message
	Ingress   trace.SpanContext
}
type Store struct {
	db            *database.DB
	identities    *identitypg.Repository
	tenant        uuid.UUID
	app, revision string
}

func NewStore(db *database.DB, tenant uuid.UUID, app, revision string) *Store {
	return &Store{db, identitypg.New(db), tenant, app, revision}
}

func (s *Store) Enqueue(ctx context.Context, m Message) error {
	if m.Identity.TenantID != s.tenant {
		return identity.ErrUnauthenticated
	}
	p, err := s.identities.Resolve(ctx, m.Identity)
	if err != nil {
		return err
	}
	return s.identities.Within(ctx, p, func(q database.Queries) error {
		id := uuid.New()
		spanID := ""
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			spanID = sc.SpanID().String()
		}
		result, err := q.Exec(ctx, `INSERT INTO channel.tasks(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,ingress_trace_id,ingress_span_id)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT DO NOTHING`, s.tenant, id, p.MemberID, s.app, m.Identity.Issuer, m.Identity.Subject, m.EventID, m.MessageID, m.Fingerprint, s.revision, m.Text, spanTraceID(ctx), spanID)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 1 {
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
		err := q.QueryRow(ctx, `UPDATE channel.tasks SET state='model_running',updated_at=now()
   WHERE (tenant_id,id)=(SELECT tenant_id,id FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND state='pending' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
   RETURNING id,member_id,issuer,subject,event_id,message_id,prompt,ingress_trace_id,ingress_span_id`, s.tenant, s.app, s.revision).Scan(&task.ID, &task.Principal.MemberID, &task.Message.Identity.Issuer, &task.Message.Identity.Subject, &task.Message.EventID, &task.Message.MessageID, &task.Message.Text, &ingressTrace, &ingressSpan)
		if err != nil {
			return err
		}
		return audit(ctx, q, s.tenant, task.ID, "model_running", "")
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
	return nil
}

func (s *Store) Transition(ctx context.Context, id uuid.UUID, from, to, reason, response string) error {
	return s.scoped(ctx, func(q database.Queries) error {
		result, err := q.Exec(ctx, `UPDATE channel.tasks SET state=$4,reason=$5,response=CASE WHEN $6='' THEN response ELSE $6 END,updated_at=now() WHERE tenant_id=$1 AND id=$2 AND state=$3`, s.tenant, id, from, to, reason, response)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("task transition conflict")
		}
		return audit(ctx, q, s.tenant, id, to, reason)
	})
}
func audit(ctx context.Context, q database.Queries, tenant, id uuid.UUID, state, reason string) error {
	_, err := q.Exec(ctx, "INSERT INTO channel.audit(tenant_id,task_id,state,reason,trace_id) VALUES($1,$2,$3,$4,$5)", tenant, id, state, reason, spanTraceID(ctx))
	return err
}

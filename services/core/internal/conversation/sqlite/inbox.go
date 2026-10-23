// Package sqlite implements the lightweight conversation persistence slices.
// Inbox includes basic text-task leases; runtime configuration is not yet supported.
package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	identitysqlite "agentos.local/core/internal/identity/sqlite"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

type Inbox struct {
	db            *sql.DB
	identities    *identitysqlite.Repository
	tenant        uuid.UUID
	owner         uuid.UUID
	approval      *conversation.ApprovalPolicy
	app, revision string
}

func NewInbox(db *sql.DB, tenant uuid.UUID, app, revision string) *Inbox {
	return &Inbox{db: db, identities: identitysqlite.New(db), tenant: tenant, owner: uuid.New(), app: app, revision: revision}
}

func (s *Inbox) Enqueue(ctx context.Context, m conversation.Message) (err error) {
	if m.Identity.TenantID != s.tenant {
		return identity.ErrUnauthenticated
	}
	// sqlitedb uses BEGIN IMMEDIATE: acquire the writer slot before reading
	// identity, so a queued write cannot authorize from a pre-lock snapshot.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	p, err := s.identities.ResolveIn(ctx, tx, m.Identity)
	if err != nil {
		return err
	}
	traceID, spanID := "", ""
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		traceID, spanID = sc.TraceID().String(), sc.SpanID().String()
	}
	id := uuid.New().String()
	var approver, ttl any
	if s.approval != nil {
		approver, ttl = s.approval.MemberID.String(), s.approval.TTLSeconds
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO channel_tasks
(tenant_id,id,member_id,app_id,provider,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,ingress_trace_id,ingress_span_id,approval_member,approval_ttl)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		s.tenant.String(), id, p.MemberID.String(), s.app, m.Identity.Provider, m.Identity.Issuer, m.Identity.Subject,
		m.EventID, m.MessageID, m.Fingerprint, s.revision, m.Text, traceID, spanID, approver, ttl)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		_, err = tx.ExecContext(ctx, "INSERT INTO channel_audit(tenant_id,task_id,state,reason,trace_id) VALUES(?,?,'pending','',?)",
			s.tenant.String(), id, traceID)
	} else {
		var same bool
		err = tx.QueryRowContext(ctx, `SELECT count(*)=1 AND coalesce(min(fingerprint=? AND member_id=? AND message_id=?),0)
FROM channel_tasks WHERE tenant_id=? AND app_id=? AND (event_id=? OR message_id=?)`,
			m.Fingerprint, p.MemberID.String(), m.MessageID, s.tenant.String(), s.app, m.EventID, m.MessageID).Scan(&same)
		if err == nil && !same {
			err = errors.New("conflicting channel replay")
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

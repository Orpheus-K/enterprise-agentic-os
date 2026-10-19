package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"context"
	"errors"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"time"
)

type AuditEntry struct {
	ID         int64     `json:"id"`
	State      string    `json:"state"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recorded_at"`
	TraceID    string    `json:"trace_id"`
}
type AuditPage struct {
	TaskID    uuid.UUID    `json:"task_id"`
	Entries   []AuditEntry `json:"entries"`
	NextAfter int64        `json:"next_after,omitempty"`
}

func (s *Store) Operation(ctx context.Context, external identity.ExternalIdentity, capability string, fn func(database.Queries) error) error {
	if external.TenantID != s.tenant {
		return identity.ErrUnauthenticated
	}
	p, err := s.identities.Resolve(ctx, external)
	if err != nil {
		return err
	}
	return s.identities.WithCapability(ctx, p, s.app, capability, fn)
}

func (s *Store) ReadAudit(ctx context.Context, external identity.ExternalIdentity, task uuid.UUID, after int64, limit int) (AuditPage, error) {
	page := AuditPage{TaskID: task, Entries: []AuditEntry{}}
	if task == uuid.Nil || after < 0 || limit < 1 || limit > 100 {
		return page, errors.New("invalid audit query")
	}
	err := s.Operation(ctx, external, "audit.read", func(q database.Queries) error {
		rows, err := q.Query(ctx, `WITH scoped AS (
   SELECT row_number() OVER(ORDER BY a.id) AS ordinal,a.state,a.reason,a.recorded_at,a.trace_id
   FROM channel.audit a JOIN channel.tasks t ON t.tenant_id=a.tenant_id AND t.id=a.task_id
   WHERE a.tenant_id=$1 AND t.app_id=$2 AND a.task_id=$3)
   SELECT ordinal,state,reason,recorded_at,trace_id FROM scoped WHERE ordinal>$4 ORDER BY ordinal LIMIT $5`, s.tenant, s.app, task, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var entry AuditEntry
			if err := rows.Scan(&entry.ID, &entry.State, &entry.Reason, &entry.RecordedAt, &entry.TraceID); err != nil {
				return err
			}
			page.Entries = append(page.Entries, entry)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Entries) > limit {
			page.Entries = page.Entries[:limit]
			page.NextAfter = page.Entries[limit-1].ID
		}
		// Attribute the read to the resolved member, never to request-supplied identity fields.
		_, err = q.Exec(ctx, `INSERT INTO channel.audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id)
   VALUES($1,$2,nullif(current_setting('agentos.member_id',true),'')::uuid,$3,$4,$5,$6)`, uuid.New(), s.tenant, s.app, task, len(page.Entries), spanTraceID(ctx))
		return err
	})
	if err != nil {
		return AuditPage{}, err
	}
	return page, nil
}

func (s *Store) Ready(ctx context.Context) error {
	return s.scoped(ctx, func(q database.Queries) error {
		var ready bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.tenants WHERE id=$1 AND enabled) AND (SELECT max(version_id) FROM public.goose_db_version WHERE is_applied)=3`, s.tenant).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return errors.New("installation storage unavailable")
		}
		return nil
	})
}
func spanTraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

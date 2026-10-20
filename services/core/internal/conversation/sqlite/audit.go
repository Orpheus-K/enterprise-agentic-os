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

func (s *Inbox) ReadAudit(ctx context.Context, external identity.ExternalIdentity, task uuid.UUID, after int64, limit int) (conversation.AuditPage, error) {
	page := conversation.AuditPage{TaskID: task, Entries: []conversation.AuditEntry{}}
	if task == uuid.Nil || after < 0 || limit < 1 || limit > 100 {
		return page, errors.New("invalid audit query")
	}
	if external.TenantID != s.tenant {
		return conversation.AuditPage{}, identity.ErrUnauthenticated
	}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		p, err := s.identities.RequireCapabilityIn(ctx, tx, external, s.app, "audit.read")
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `WITH scoped AS (
SELECT row_number() OVER(ORDER BY a.id) AS ordinal,a.state,a.reason,a.recorded_at,a.trace_id
FROM channel_audit a JOIN channel_tasks t ON t.tenant_id=a.tenant_id AND t.id=a.task_id
WHERE a.tenant_id=? AND t.app_id=? AND a.task_id=?)
SELECT ordinal,state,reason,recorded_at,trace_id FROM scoped WHERE ordinal>? ORDER BY ordinal LIMIT ?`,
			s.tenant.String(), s.app, task.String(), after, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var entry conversation.AuditEntry
			var recorded string
			if err := rows.Scan(&entry.ID, &entry.State, &entry.Reason, &recorded, &entry.TraceID); err != nil {
				return errors.Join(err, rows.Close())
			}
			entry.RecordedAt, err = time.Parse(time.RFC3339Nano, recorded)
			if err != nil {
				return errors.Join(err, rows.Close())
			}
			page.Entries = append(page.Entries, entry)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(page.Entries) > limit {
			page.Entries = page.Entries[:limit]
			page.NextAfter = page.Entries[limit-1].ID
		}
		traceID := ""
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			traceID = sc.TraceID().String()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id)
VALUES(?,?,?,?,?,?,?)`, uuid.New().String(), s.tenant.String(), p.MemberID.String(), s.app, task.String(), len(page.Entries), traceID)
		return err
	})
	if err != nil {
		return conversation.AuditPage{}, err
	}
	return page, nil
}

package sqlite

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"context"
	"database/sql"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	"time"
)

// SQLite task timestamps are UTC milliseconds. Ceil both half-open bounds so
// sub-millisecond input never rounds a stored task into or out of the window.
func reportBound(t time.Time) string {
	rounded := t.UTC().Truncate(time.Millisecond)
	if rounded.Before(t) {
		rounded = rounded.Add(time.Millisecond)
	}
	return rounded.Format("2006-01-02T15:04:05.000Z")
}
func (s *Inbox) recordRead(ctx context.Context, tx *sql.Tx, member uuid.UUID, scope string, rows int) error {
	traceID := ""
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		traceID = sc.TraceID().String()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO channel_audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id,scope) VALUES(?,?,?,?,NULL,?,?,?)`, uuid.New().String(), s.tenant.String(), member.String(), s.app, rows, traceID, scope)
	return err
}

// ListTasks pages tasks by (created_at, id). An unknown or out-of-scope cursor yields an empty
// page rather than an error, so cursors cannot probe for other tenants' or apps' tasks.
func (s *Inbox) ListTasks(ctx context.Context, external identity.ExternalIdentity, w conversation.ReportWindow, after uuid.UUID, limit int) (conversation.TaskPage, error) {
	page := conversation.TaskPage{Tasks: []conversation.TaskSummary{}}
	if err := w.Validate(); err != nil || limit < 1 || limit > 100 {
		return conversation.TaskPage{}, conversation.ErrInvalidReport
	}
	if external.TenantID != s.tenant {
		return conversation.TaskPage{}, identity.ErrUnauthenticated
	}
	err := s.transaction(ctx, func(q *sql.Tx) error {
		p, err := s.identities.RequireCapabilityIn(ctx, q, external, s.app, "audit.read")
		if err != nil {
			return err
		}
		var cursor any
		if after != uuid.Nil {
			cursor = after.String()
		}
		rows, err := q.QueryContext(ctx, `SELECT t.id,t.state,t.reason,NULL,NULL,t.created_at,coalesce((SELECT a.recorded_at FROM channel_audit a WHERE a.tenant_id=t.tenant_id AND a.task_id=t.id ORDER BY a.id DESC LIMIT 1),t.created_at) FROM channel_tasks t
   WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.created_at>=$3 AND t.created_at<$4
     AND ($5 IS NULL OR (t.created_at,t.id)>(SELECT c.created_at,c.id FROM channel_tasks c WHERE c.tenant_id=$1 AND c.app_id=$2 AND c.id=$5))
   ORDER BY t.created_at,t.id LIMIT $6`, s.tenant.String(), s.app, reportBound(w.From), reportBound(w.To), cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t conversation.TaskSummary
			var created, updated string
			if err := rows.Scan(&t.TaskID, &t.State, &t.Reason, &t.SkillName, &t.SkillRevision, &created, &updated); err != nil {
				return err
			}
			t.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
			if err != nil {
				return err
			}
			t.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
			if err != nil {
				return err
			}
			page.Tasks = append(page.Tasks, t)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Tasks) > limit {
			page.Tasks = page.Tasks[:limit]
			last := page.Tasks[limit-1].TaskID
			page.NextAfter = &last
		}
		return s.recordRead(ctx, q, p.MemberID, "tasks", len(page.Tasks))
	})
	if err != nil {
		return conversation.TaskPage{}, err
	}
	return page, nil
}

// Stats aggregates only recorded facts; it does not estimate labour or value.
func (s *Inbox) Stats(ctx context.Context, external identity.ExternalIdentity, w conversation.ReportWindow) (conversation.TaskStats, error) {
	if err := w.Validate(); err != nil {
		return conversation.TaskStats{}, err
	}
	out := conversation.TaskStats{From: w.From, To: w.To, ByState: map[string]int64{}, Approvals: map[string]int64{}}
	if external.TenantID != s.tenant {
		return conversation.TaskStats{}, identity.ErrUnauthenticated
	}
	err := s.transaction(ctx, func(q *sql.Tx) error {
		p, err := s.identities.RequireCapabilityIn(ctx, q, external, s.app, "audit.read")
		if err != nil {
			return err
		}
		collect := func(sql string, into map[string]int64) error {
			rows, err := q.QueryContext(ctx, sql, s.tenant.String(), s.app, reportBound(w.From), reportBound(w.To))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var key string
				var n int64
				if err := rows.Scan(&key, &n); err != nil {
					return err
				}
				into[key] = n
			}
			return rows.Err()
		}
		if err := collect(`SELECT state,count(*) FROM channel_tasks WHERE tenant_id=$1 AND app_id=$2 AND created_at>=$3 AND created_at<$4 GROUP BY state`, out.ByState); err != nil {
			return err
		}
		if err := collect(`SELECT a.status,count(*) FROM channel_approvals a JOIN channel_tasks t ON (t.tenant_id,t.id)=(a.tenant_id,a.task_id)
   WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.created_at>=$3 AND t.created_at<$4 GROUP BY a.status`, out.Approvals); err != nil {
			return err
		}
		for _, n := range out.ByState {
			out.Tasks += n
		}
		return s.recordRead(ctx, q, p.MemberID, "stats", int(out.Tasks))
	})
	if err != nil {
		return conversation.TaskStats{}, err
	}
	return out, nil
}

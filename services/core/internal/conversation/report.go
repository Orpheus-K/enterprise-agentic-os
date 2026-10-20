package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"context"
	"errors"
	"github.com/google/uuid"
	"time"
)

// ErrInvalidReport rejects malformed report windows and cursors.
var ErrInvalidReport = errors.New("invalid report query")

// MaxReportWindow bounds installation-wide queries so one request cannot scan all history.
const MaxReportWindow = 92 * 24 * time.Hour

// ReportWindow selects tasks created in [From, To) for the configured application.
type ReportWindow struct {
	From, To time.Time
}

// TaskSummary is the auditor's view of one task: no prompt, response, channel subject or member.
type TaskSummary struct {
	TaskID        uuid.UUID `json:"task_id"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	SkillName     *string   `json:"skill_name"`
	SkillRevision *string   `json:"skill_revision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type TaskPage struct {
	Tasks []TaskSummary `json:"tasks"`
	// NextAfter is the last task of a full page; pass it back as after. Absent on the last page.
	NextAfter *uuid.UUID `json:"next_after,omitempty"`
}

// TaskStats counts tasks created in the window by their current state, and approvals of those
// tasks by status. States are current at query time, not as of the window end.
type TaskStats struct {
	From      time.Time        `json:"from"`
	To        time.Time        `json:"to"`
	Tasks     int64            `json:"tasks"`
	ByState   map[string]int64 `json:"by_state"`
	Approvals map[string]int64 `json:"approvals"`
}

func (w ReportWindow) Validate() error {
	if w.From.IsZero() || !w.To.After(w.From) || w.To.Sub(w.From) > MaxReportWindow {
		return ErrInvalidReport
	}
	return nil
}

// recordRead writes the access trail for an installation-wide query in the same transaction.
func (s *Store) recordRead(ctx context.Context, q database.Queries, scope string, rows int) error {
	_, err := q.Exec(ctx, `INSERT INTO channel.audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id,scope)
   VALUES($1,$2,nullif(current_setting('agentos.member_id',true),'')::uuid,$3,NULL,$4,$5,$6)`, uuid.New(), s.tenant, s.app, rows, spanTraceID(ctx), scope)
	return err
}

// ListTasks pages tasks by (created_at, id). An unknown or out-of-scope cursor yields an empty
// page rather than an error, so cursors cannot probe for other tenants' or apps' tasks.
func (s *Store) ListTasks(ctx context.Context, external identity.ExternalIdentity, w ReportWindow, after uuid.UUID, limit int) (TaskPage, error) {
	page := TaskPage{Tasks: []TaskSummary{}}
	if err := w.Validate(); err != nil || limit < 1 || limit > 100 {
		return TaskPage{}, ErrInvalidReport
	}
	err := s.Operation(ctx, external, "audit.read", func(q database.Queries) error {
		var cursor *uuid.UUID
		if after != uuid.Nil {
			cursor = &after
		}
		rows, err := q.Query(ctx, `SELECT t.id,t.state,t.reason,t.skill_name,t.skill_revision,t.created_at,t.updated_at FROM channel.tasks t
   WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.created_at>=$3 AND t.created_at<$4
     AND ($5::uuid IS NULL OR (t.created_at,t.id)>(SELECT c.created_at,c.id FROM channel.tasks c WHERE c.tenant_id=$1 AND c.app_id=$2 AND c.id=$5))
   ORDER BY t.created_at,t.id LIMIT $6`, s.tenant, s.app, w.From, w.To, cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t TaskSummary
			if err := rows.Scan(&t.TaskID, &t.State, &t.Reason, &t.SkillName, &t.SkillRevision, &t.CreatedAt, &t.UpdatedAt); err != nil {
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
		return s.recordRead(ctx, q, "tasks", len(page.Tasks))
	})
	if err != nil {
		return TaskPage{}, err
	}
	return page, nil
}

// Stats aggregates only recorded facts; it does not estimate labour or value.
func (s *Store) Stats(ctx context.Context, external identity.ExternalIdentity, w ReportWindow) (TaskStats, error) {
	if err := w.Validate(); err != nil {
		return TaskStats{}, err
	}
	out := TaskStats{From: w.From, To: w.To, ByState: map[string]int64{}, Approvals: map[string]int64{}}
	err := s.Operation(ctx, external, "audit.read", func(q database.Queries) error {
		collect := func(sql string, into map[string]int64) error {
			rows, err := q.Query(ctx, sql, s.tenant, s.app, w.From, w.To)
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
		if err := collect(`SELECT state,count(*) FROM channel.tasks WHERE tenant_id=$1 AND app_id=$2 AND created_at>=$3 AND created_at<$4 GROUP BY state`, out.ByState); err != nil {
			return err
		}
		if err := collect(`SELECT a.status,count(*) FROM channel.approvals a JOIN channel.tasks t ON (t.tenant_id,t.id)=(a.tenant_id,a.task_id)
   WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.created_at>=$3 AND t.created_at<$4 GROUP BY a.status`, out.Approvals); err != nil {
			return err
		}
		for _, n := range out.ByState {
			out.Tasks += n
		}
		return s.recordRead(ctx, q, "stats", int(out.Tasks))
	})
	if err != nil {
		return TaskStats{}, err
	}
	return out, nil
}

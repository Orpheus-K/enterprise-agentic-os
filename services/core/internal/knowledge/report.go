package knowledge

import (
	"context"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

// AuditWindow selects recorded facts in [From,To), at PostgreSQL precision.
type AuditWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func (w AuditWindow) validate() error {
	if w.From.IsZero() || !w.To.After(w.From) || w.To.Sub(w.From) > 92*24*time.Hour || w.From.Nanosecond()%1000 != 0 || w.To.Nanosecond()%1000 != 0 {
		return ErrInvalid
	}
	return nil
}

type AuditQuery struct {
	AuditWindow
	Kind  string    `json:"kind"`
	After uuid.UUID `json:"after,omitempty"`
	Limit int       `json:"limit"`
}

// AuditEvent exposes identifiers and counters, not member identities, source
// bodies, search vectors or credentials. Source metadata remains privileged.
type AuditEvent struct {
	ID         uuid.UUID  `json:"id"`
	RecordedAt time.Time  `json:"recorded_at"`
	Action     string     `json:"action"`
	SourceID   *uuid.UUID `json:"source_id,omitempty"`
	DocumentID string     `json:"document_id,omitempty"`
	Revision   int64      `json:"revision,omitempty"`
	SpaceID    *uuid.UUID `json:"space_id,omitempty"`
	Requested  *int       `json:"requested,omitempty"`
	Returned   *int       `json:"returned,omitempty"`
}
type AuditPage struct {
	Events    []AuditEvent `json:"events"`
	NextAfter *uuid.UUID   `json:"next_after,omitempty"`
}
type UsageStats struct {
	AuditWindow
	ReadChecks          int64 `json:"read_checks"`
	Searches            int64 `json:"searches"`
	EmptySearches       int64 `json:"empty_searches"`
	RequestedCandidates int64 `json:"requested_candidates"`
	ReturnedCandidates  int64 `json:"returned_candidates"`
}

func (s *Store) auditWithin(ctx context.Context, actor identity.ExternalIdentity, fn func(database.Queries) error) error {
	return s.within(ctx, actor, func(q database.Queries) error {
		before, err := s.repo.RequireCapabilityIn(ctx, q, actor, s.app, "knowledge.audit.read")
		if err != nil {
			return err
		}
		if err = fn(q); err != nil {
			return err
		}
		after, err := s.repo.RequireCapabilityIn(ctx, q, actor, s.app, "knowledge.audit.read")
		if err != nil {
			return err
		}
		if before != after {
			return identity.ErrUnauthenticated
		}
		return nil
	})
}

func (s *Store) auditReportRead(ctx context.Context, q database.Queries, w AuditWindow, kind string, rows int) error {
	_, err := q.Exec(ctx, `INSERT INTO knowledge.audit_reads(id,tenant_id,app_id,member_id,kind,window_from,window_to,row_count) VALUES($1,$2,$3,current_setting('agentos.member_id')::uuid,$4,$5,$6,$7)`, uuid.New(), s.tenant, s.app, kind, w.From, w.To, rows)
	return err
}

func (s *Store) ReadAudit(ctx context.Context, actor identity.ExternalIdentity, in AuditQuery) (AuditPage, error) {
	if in.AuditWindow.validate() != nil || in.Limit < 1 || in.Limit > 100 || (in.Kind != "events" && in.Kind != "searches") {
		return AuditPage{}, ErrInvalid
	}
	page := AuditPage{Events: []AuditEvent{}}
	err := s.auditWithin(ctx, actor, func(q database.Queries) error {
		// Only these fixed projections/table names enter SQL; user values remain parameters.
		table, projection := "knowledge.audit", "a.id,a.recorded_at,a.action,a.source_id,a.document_id,a.revision,NULL::uuid,NULL::integer,NULL::integer"
		if in.Kind == "searches" {
			table = "knowledge.search_audit"
			projection = "a.id,a.recorded_at,'search',NULL::uuid,'',0,a.space_id,a.requested,a.returned"
		}
		var cursor *uuid.UUID
		if in.After != uuid.Nil {
			cursor = &in.After
		}
		rows, err := q.Query(ctx, `SELECT `+projection+` FROM `+table+` a WHERE a.tenant_id=$1 AND a.app_id=$2 AND a.recorded_at>=$3 AND a.recorded_at<$4
 AND ($5::uuid IS NULL OR (a.recorded_at,a.id)>(SELECT c.recorded_at,c.id FROM `+table+` c WHERE c.tenant_id=$1 AND c.app_id=$2 AND c.id=$5 AND c.recorded_at>=$3 AND c.recorded_at<$4))
 ORDER BY a.recorded_at,a.id LIMIT $6`, s.tenant, s.app, in.From, in.To, cursor, in.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event AuditEvent
			if err := rows.Scan(&event.ID, &event.RecordedAt, &event.Action, &event.SourceID, &event.DocumentID, &event.Revision, &event.SpaceID, &event.Requested, &event.Returned); err != nil {
				return err
			}
			page.Events = append(page.Events, event)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Events) > in.Limit {
			page.Events = page.Events[:in.Limit]
			last := page.Events[in.Limit-1].ID
			page.NextAfter = &last
		}
		return s.auditReportRead(ctx, q, in.AuditWindow, in.Kind, len(page.Events))
	})
	if err != nil {
		return AuditPage{}, err
	}
	return page, nil
}

// Usage counts successful committed checks/searches. VerifyIn also emits read
// checks, so this is not a count of distinct users, documents or business tasks.
func (s *Store) Usage(ctx context.Context, actor identity.ExternalIdentity, w AuditWindow) (UsageStats, error) {
	if w.validate() != nil {
		return UsageStats{}, ErrInvalid
	}
	out := UsageStats{AuditWindow: w}
	err := s.auditWithin(ctx, actor, func(q database.Queries) error {
		// One statement gives both tables the same MVCC snapshot.
		err := q.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM knowledge.audit WHERE tenant_id=$1 AND app_id=$2 AND recorded_at>=$3 AND recorded_at<$4 AND action='read'),
 count(*),count(*) FILTER(WHERE returned=0),coalesce(sum(requested),0),coalesce(sum(returned),0)
 FROM knowledge.search_audit WHERE tenant_id=$1 AND app_id=$2 AND recorded_at>=$3 AND recorded_at<$4`, s.tenant, s.app, w.From, w.To).Scan(&out.ReadChecks, &out.Searches, &out.EmptySearches, &out.RequestedCandidates, &out.ReturnedCandidates)
		if err != nil {
			return err
		}
		return s.auditReportRead(ctx, q, w, "stats", 1)
	})
	if err != nil {
		return UsageStats{}, err
	}
	return out, nil
}

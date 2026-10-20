package knowledge

import (
	"context"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
)

// ReportSyncHealth serializes health observations from valid sync leases. The
// source-wide sequence is allocated under its row lock, not from a document's
// unrelated native revision. Stale workers cannot restore source availability.
func (s *Store) ReportSyncHealth(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease, healthy bool, until time.Time) (SourceHealth, error) {
	if until.IsZero() {
		return SourceHealth{}, ErrInvalid
	}
	var out SourceHealth
	err := s.withSyncLease(ctx, actor, lease, func(q database.Queries, _, _ []byte, _ bool) error {
		if _, err := q.Exec(ctx, `INSERT INTO knowledge.source_health(tenant_id,app_id,source_id)
VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, s.tenant, s.app, lease.SourceID); err != nil {
			return err
		}
		var revision int64
		if err := q.QueryRow(ctx, `SELECT revision FROM knowledge.source_health
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 FOR UPDATE`, s.tenant, s.app, lease.SourceID).Scan(&revision); err != nil {
			return err
		}
		var active bool
		if err := q.QueryRow(ctx, `SELECT lease_until>clock_timestamp() FROM knowledge.sync_jobs
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`,
			s.tenant, s.app, lease.SourceID, lease.DocumentID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
		state := "unavailable"
		if healthy {
			state = "healthy"
		}
		var err error
		out, err = s.updateHealth(ctx, q, HealthUpdate{SourceID: lease.SourceID, Revision: revision + 1, State: state, ValidUntil: until})
		return err
	})
	if err != nil {
		return SourceHealth{}, err
	}
	return out, nil
}

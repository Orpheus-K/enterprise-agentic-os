package knowledge

import (
	"context"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"github.com/google/uuid"
)

// CheckSyncSource validates the configured source actor without contacting a
// provider or creating work. Runtime authority is checked again on each action.
func (s *Store) CheckSyncSource(ctx context.Context, actor identity.ExternalIdentity, source uuid.UUID) error {
	if source == uuid.Nil {
		return ErrInvalid
	}
	return s.within(ctx, actor, func(q database.Queries) error { return s.syncAllowed(ctx, q, source) })
}

// PollSync schedules the next observation only after completion, or on a
// configuration change. Active, expired and unknown same-config work retains
// its request revision so workers can resume it under the existing fences.
func (s *Store) PollSync(ctx context.Context, actor identity.ExternalIdentity, source uuid.UUID, document, config string) error {
	if source == uuid.Nil || !label(document, 256) || !configHashValid(config) {
		return ErrInvalid
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		if err := s.syncAllowed(ctx, q, source); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `INSERT INTO knowledge.sync_jobs(tenant_id,app_id,source_id,document_id,request_revision,config_hash,state)
VALUES($1,$2,$3,$4,1,$5,'pending') ON CONFLICT DO NOTHING`, s.tenant, s.app, source, document, config)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return s.audit(ctx, q, source, document, "sync.schedule", 1)
		}
		var revision int64
		var oldConfig, state string
		var pending, active bool
		if err := q.QueryRow(ctx, `SELECT request_revision,config_hash,state,embedding_pending,lease_until>clock_timestamp() FROM knowledge.sync_jobs
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 FOR UPDATE`,
			s.tenant, s.app, source, document).Scan(&revision, &oldConfig, &state, &pending, &active); err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, source); err != nil {
			return err
		}
		if oldConfig == config && state != "completed" {
			return nil
		}
		if pending {
			return ErrConflict
		}
		// Deployment must stop old configurations first. A live lease or
		// unknown operation cannot be replaced merely by editing config.
		if oldConfig != config && state == "running" && active {
			return identitypg.ErrForbidden
		}
		if _, err := q.Exec(ctx, `UPDATE knowledge.sync_jobs SET request_revision=request_revision+1,
config_hash=$5,state='pending',generation=generation+1,lease_token=NULL,lease_until='-infinity',publication_hash='',
embedding_input=CASE WHEN config_hash=$5 THEN embedding_input ELSE NULL END,
embedding_vectors=CASE WHEN config_hash=$5 THEN embedding_vectors ELSE NULL END
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`,
			s.tenant, s.app, source, document, config); err != nil {
			return err
		}
		return s.audit(ctx, q, source, document, "sync.schedule", revision+1)
	})
}

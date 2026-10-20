package knowledge

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SyncLease struct {
	SourceID        uuid.UUID
	DocumentID      string
	RequestRevision int64
	ConfigHash      string
	Generation      int64
	Token           uuid.UUID
	Until           time.Time
}

func configHashValid(hash string) bool {
	raw, err := hex.DecodeString(hash)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == hash
}

// ScheduleSync persists desired work. Revision is an administrator/scheduler
// sequence, separate from native document revisions. Same revision/hash is a
// no-op; a newer request fences any current worker without extending its lease.
func (s *Store) ScheduleSync(ctx context.Context, actor identity.ExternalIdentity, source uuid.UUID, document string, revision int64, configHash string) error {
	if source == uuid.Nil || !label(document, 256) || revision < 1 || !configHashValid(configHash) {
		return ErrInvalid
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		if err := s.syncAllowed(ctx, q, source); err != nil {
			return err
		}
		inserted, err := q.Exec(ctx, `INSERT INTO knowledge.sync_jobs(tenant_id,app_id,source_id,document_id,request_revision,config_hash,state)
VALUES($1,$2,$3,$4,$5,$6,'pending') ON CONFLICT DO NOTHING`, s.tenant, s.app, source, document, revision, configHash)
		if err != nil {
			return err
		}
		var oldRevision int64
		var oldHash string
		var externalPending bool
		if err := q.QueryRow(ctx, `SELECT request_revision,config_hash,embedding_pending FROM knowledge.sync_jobs
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 FOR UPDATE`, s.tenant, s.app, source, document).Scan(&oldRevision, &oldHash, &externalPending); err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, source); err != nil {
			return err
		}
		if revision < oldRevision || revision == oldRevision && configHash != oldHash {
			return ErrConflict
		}
		if revision == oldRevision {
			if inserted.RowsAffected() == 1 {
				return s.audit(ctx, q, source, document, "sync.schedule", revision)
			}
			return nil
		}
		if externalPending {
			return ErrConflict
		}
		if _, err := q.Exec(ctx, `UPDATE knowledge.sync_jobs SET request_revision=$5,config_hash=$6,state='pending',
generation=generation+1,lease_token=NULL,lease_until='-infinity',publication_hash='',
embedding_input=NULL,embedding_vectors=NULL,embedding_pending=false
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`, s.tenant, s.app, source, document, revision, configHash); err != nil {
			return err
		}
		return s.audit(ctx, q, source, document, "sync.schedule", revision)
	})
}

// ClaimSync recovers expired work by incrementing the persistent fencing
// generation. It never assumes an in-memory cursor or a previous worker result.
func (s *Store) ClaimSync(ctx context.Context, actor identity.ExternalIdentity, source uuid.UUID, configHash string, duration time.Duration) (SyncLease, error) {
	if source == uuid.Nil || !configHashValid(configHash) || duration < time.Second || duration > 5*time.Minute {
		return SyncLease{}, ErrInvalid
	}
	out := SyncLease{SourceID: source, ConfigHash: configHash, Token: uuid.New()}
	err := s.within(ctx, actor, func(q database.Queries) error {
		if err := s.syncAllowed(ctx, q, source); err != nil {
			return err
		}
		err := q.QueryRow(ctx, `SELECT document_id,request_revision FROM knowledge.sync_jobs
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND config_hash=$4
AND NOT embedding_pending
AND (state='pending' OR (state='running' AND lease_until<=clock_timestamp()))
ORDER BY document_id FOR UPDATE SKIP LOCKED LIMIT 1`, s.tenant, s.app, source, configHash).Scan(&out.DocumentID, &out.RequestRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnavailable
		}
		if err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, source); err != nil {
			return err
		}
		if err := q.QueryRow(ctx, `UPDATE knowledge.sync_jobs SET state='running',generation=generation+1,
lease_token=$5,lease_until=clock_timestamp()+$6*interval '1 millisecond',publication_hash=''
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 RETURNING generation,lease_until`,
			s.tenant, s.app, source, out.DocumentID, out.Token, duration.Milliseconds()).Scan(&out.Generation, &out.Until); err != nil {
			return err
		}
		return s.audit(ctx, q, source, out.DocumentID, "sync.claim", out.Generation)
	})
	if err != nil {
		return SyncLease{}, err
	}
	return out, nil
}

// PublishSync atomically completes the job with the publication. A retry after
// an unknown commit succeeds only for the same lease and exact publication.
// Expired or superseded workers cannot modify content or acknowledge newer work.
func (s *Store) PublishSync(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease, p Publication) error {
	return s.publishSync(ctx, actor, lease, p, nil)
}

func (s *Store) publishSync(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease, p Publication, check func(database.Queries) error) error {
	if p.validate() != nil || lease.Token == uuid.Nil || lease.Generation < 1 || lease.RequestRevision < 1 ||
		!configHashValid(lease.ConfigHash) || p.Content.SourceID != lease.SourceID || p.Content.DocumentID != lease.DocumentID {
		return ErrInvalid
	}
	hash, err := digest(p)
	if err != nil {
		return err
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		var state, published string
		var active, pending bool
		var input, vectors []byte
		err := q.QueryRow(ctx, `SELECT state,publication_hash,lease_until>clock_timestamp(),embedding_pending,embedding_input,embedding_vectors FROM knowledge.sync_jobs
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4
AND request_revision=$5 AND config_hash=$6 AND generation=$7 AND lease_token=$8 FOR UPDATE`,
			s.tenant, s.app, lease.SourceID, lease.DocumentID, lease.RequestRevision, lease.ConfigHash, lease.Generation, lease.Token).Scan(&state, &published, &active, &pending, &input, &vectors)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, lease.SourceID); err != nil {
			return err
		}
		if check != nil {
			// The job lock alone does not exclude waiting on a separately
			// updated document. Identity proof must follow both lock waits.
			if err := s.lock(ctx, q, lease.SourceID, lease.DocumentID); err != nil {
				return err
			}
			if err := check(q); err != nil {
				return err
			}
		}
		if state == "completed" && published == hash {
			return nil
		}
		if state != "running" || !active || pending {
			return ErrConflict
		}
		if len(input) != 0 {
			checkpoint, err := decodeEmbedding(input, vectors)
			if err != nil {
				return err
			}
			if !sameDigest(checkpoint.Content, p.Content) || !sameDigest(checkpoint.Vectors, p.Vectors) {
				return ErrConflict
			}
		}
		if err := s.publish(ctx, q, p); err != nil {
			return err
		}
		if check != nil {
			if err := check(q); err != nil {
				return err
			}
		}
		tag, err := q.Exec(ctx, `UPDATE knowledge.sync_jobs SET state='completed',publication_hash=$5
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 AND lease_until>clock_timestamp()`,
			s.tenant, s.app, lease.SourceID, lease.DocumentID, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return s.audit(ctx, q, lease.SourceID, lease.DocumentID, "sync.complete", lease.Generation)
	})
}

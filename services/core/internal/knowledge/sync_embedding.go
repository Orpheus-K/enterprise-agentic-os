package knowledge

import (
	"context"
	"encoding/json"
	"errors"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type embeddingInput struct {
	Content Content
	Space   Space
}

// EmbeddingCheckpoint excludes ACL: recovery must check current native
// permissions and obtain a fresh health/ACL proof before publication.
type EmbeddingCheckpoint struct {
	Content Content
	Vectors VectorSet
}

func sameDigest(a, b any) bool {
	x, err := digest(a)
	if err != nil {
		return false
	}
	y, err := digest(b)
	return err == nil && x == y
}

func decodeEmbedding(input, vectors []byte) (EmbeddingCheckpoint, error) {
	var in embeddingInput
	var v VectorSet
	if len(vectors) == 0 {
		return EmbeddingCheckpoint{}, ErrUnavailable
	}
	if json.Unmarshal(input, &in) != nil || json.Unmarshal(vectors, &v) != nil ||
		in.Content.validate() != nil || in.Content.Deleted || !in.Space.validate() ||
		!vectorsMatch(in, v) {
		return EmbeddingCheckpoint{}, ErrInvalid
	}
	return EmbeddingCheckpoint{Content: in.Content, Vectors: v}, nil
}

func vectorsMatch(in embeddingInput, v VectorSet) bool {
	if v.validate() != nil || v.SourceID != in.Content.SourceID || v.DocumentID != in.Content.DocumentID ||
		v.ContentRevision != in.Content.Revision || v.Space != in.Space || len(v.Vectors) != len(in.Content.Chunks) {
		return false
	}
	chunks := map[string]bool{}
	for _, c := range in.Content.Chunks {
		chunks[c.ID] = true
	}
	for _, vector := range v.Vectors {
		if !chunks[vector.ChunkID] {
			return false
		}
	}
	return true
}

func (s *Store) withSyncLease(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease, fn func(database.Queries, []byte, []byte, bool) error) error {
	if lease.Token == uuid.Nil || lease.Generation < 1 || lease.RequestRevision < 1 || lease.SourceID == uuid.Nil ||
		!label(lease.DocumentID, 256) || !configHashValid(lease.ConfigHash) {
		return ErrInvalid
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		var input, vectors []byte
		var pending bool
		err := q.QueryRow(ctx, `SELECT embedding_input,embedding_vectors,embedding_pending FROM knowledge.sync_jobs
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 AND request_revision=$5
AND config_hash=$6 AND generation=$7 AND lease_token=$8 AND state='running' FOR UPDATE`,
			s.tenant, s.app, lease.SourceID, lease.DocumentID, lease.RequestRevision, lease.ConfigHash, lease.Generation, lease.Token).Scan(&input, &vectors, &pending)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, lease.SourceID); err != nil {
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
		return fn(q, input, vectors, pending)
	})
}

// BeginSyncEmbedding must commit before sending the batch's first request.
// A repeated begin is a conflict, never permission to repeat an external call.
func (s *Store) BeginSyncEmbedding(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease, content Content, space Space) error {
	if content.validate() != nil || content.Deleted || !space.validate() ||
		content.SourceID != lease.SourceID || content.DocumentID != lease.DocumentID {
		return ErrInvalid
	}
	raw, err := json.Marshal(embeddingInput{Content: content, Space: space})
	if err != nil || len(raw) > 16<<20 {
		return ErrInvalid
	}
	return s.withSyncLease(ctx, actor, lease, func(q database.Queries, input, vectors []byte, pending bool) error {
		if len(input) != 0 || pending {
			return ErrConflict
		}
		if _, err := q.Exec(ctx, `UPDATE knowledge.sync_jobs SET embedding_pending=true,embedding_input=$5
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`,
			s.tenant, s.app, lease.SourceID, lease.DocumentID, raw); err != nil {
			return err
		}
		return s.audit(ctx, q, lease.SourceID, lease.DocumentID, "sync.embedding_intent", lease.Generation)
	})
}

// SaveSyncEmbedding records only a complete, validated batch. A crash or failed
// save leaves the intent pending, preventing automatic lease recovery/replay.
func (s *Store) SaveSyncEmbedding(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease, result VectorSet) error {
	if result.validate() != nil {
		return ErrInvalid
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 16<<20 {
		return ErrInvalid
	}
	return s.withSyncLease(ctx, actor, lease, func(q database.Queries, input, vectors []byte, pending bool) error {
		var in embeddingInput
		if json.Unmarshal(input, &in) != nil || !vectorsMatch(in, result) {
			return ErrConflict
		}
		if len(vectors) != 0 {
			var existing VectorSet
			if json.Unmarshal(vectors, &existing) != nil || !sameDigest(existing, result) {
				return ErrConflict
			}
			return nil
		}
		if !pending {
			return ErrConflict
		}
		if _, err := q.Exec(ctx, `UPDATE knowledge.sync_jobs SET embedding_pending=false,embedding_vectors=$5
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`,
			s.tenant, s.app, lease.SourceID, lease.DocumentID, raw); err != nil {
			return err
		}
		return s.audit(ctx, q, lease.SourceID, lease.DocumentID, "sync.embedding_saved", lease.Generation)
	})
}

func (s *Store) LoadSyncEmbedding(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease) (EmbeddingCheckpoint, error) {
	var out EmbeddingCheckpoint
	err := s.withSyncLease(ctx, actor, lease, func(q database.Queries, input, vectors []byte, pending bool) error {
		if pending {
			return ErrUnavailable
		}
		var err error
		out, err = decodeEmbedding(input, vectors)
		if err != nil {
			return err
		}
		return s.audit(ctx, q, lease.SourceID, lease.DocumentID, "sync.embedding_read", lease.Generation)
	})
	if err != nil {
		return EmbeddingCheckpoint{}, err
	}
	return out, nil
}

// RefreshSyncEmbedding discards only a known complete result whose source
// content has changed. It never clears an unresolved external call.
func (s *Store) RefreshSyncEmbedding(ctx context.Context, actor identity.ExternalIdentity, lease SyncLease, content Content) error {
	if content.validate() != nil || content.Deleted || content.SourceID != lease.SourceID || content.DocumentID != lease.DocumentID {
		return ErrInvalid
	}
	return s.withSyncLease(ctx, actor, lease, func(q database.Queries, input, vectors []byte, pending bool) error {
		if pending {
			return ErrConflict
		}
		checkpoint, err := decodeEmbedding(input, vectors)
		if err != nil {
			return err
		}
		if sameDigest(checkpoint.Content, content) {
			return ErrConflict
		}
		if _, err := q.Exec(ctx, `UPDATE knowledge.sync_jobs SET embedding_input=NULL,embedding_vectors=NULL
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`,
			s.tenant, s.app, lease.SourceID, lease.DocumentID); err != nil {
			return err
		}
		return s.audit(ctx, q, lease.SourceID, lease.DocumentID, "sync.embedding_refresh", lease.Generation)
	})
}

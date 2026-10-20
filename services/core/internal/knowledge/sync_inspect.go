package knowledge

import (
	"context"
	"encoding/json"
	"errors"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SyncStatus struct {
	SourceID        uuid.UUID          `json:"source_id"`
	DocumentID      string             `json:"document_id"`
	RequestRevision int64              `json:"request_revision"`
	Generation      int64              `json:"generation"`
	ConfigHash      string             `json:"config_hash"`
	State           string             `json:"state"`
	LeaseActive     bool               `json:"lease_active"`
	EmbeddingState  string             `json:"embedding_state"`
	Embedding       *SyncInputMetadata `json:"embedding,omitempty"`
}

type SyncInputMetadata struct {
	Hash            string   `json:"hash"`
	Space           Space    `json:"space"`
	ContentRevision int64    `json:"content_revision"`
	ChunkIDs        []string `json:"chunk_ids"`
}

// InspectSync is an observation, not evidence of a provider outcome. It omits
// content, vectors and lease tokens. Reconciliation must recheck all fences.
func (s *Store) InspectSync(ctx context.Context, actor identity.ExternalIdentity, source uuid.UUID, document string) (SyncStatus, error) {
	if source == uuid.Nil || !label(document, 256) {
		return SyncStatus{}, ErrInvalid
	}
	out := SyncStatus{SourceID: source, DocumentID: document}
	err := s.within(ctx, actor, func(q database.Queries) error {
		if err := s.syncAllowed(ctx, q, source); err != nil {
			return err
		}
		principal, err := s.repo.ResolveIn(ctx, q, actor)
		if err != nil {
			return err
		}
		var allowed bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.operation_grants
WHERE tenant_id=$1 AND member_id=$2 AND app_id=$3 AND capability='knowledge.reconcile' AND enabled)`,
			s.tenant, principal.MemberID, s.app).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return identitypg.ErrForbidden
		}
		var pending, saved bool
		var input []byte
		err = q.QueryRow(ctx, `SELECT request_revision,generation,config_hash,state,
lease_until>clock_timestamp(),embedding_pending,embedding_vectors IS NOT NULL,embedding_input
FROM knowledge.sync_jobs WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`,
			s.tenant, s.app, source, document).Scan(&out.RequestRevision, &out.Generation, &out.ConfigHash,
			&out.State, &out.LeaseActive, &pending, &saved, &input)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnavailable
		}
		if err != nil {
			return err
		}
		out.EmbeddingState = "none"
		if pending {
			out.EmbeddingState = "pending"
		} else if saved {
			out.EmbeddingState = "saved"
		}
		if len(input) != 0 {
			var in embeddingInput
			if json.Unmarshal(input, &in) != nil || in.Content.validate() != nil || !in.Space.validate() {
				return ErrInvalid
			}
			hash, err := digest(in)
			if err != nil {
				return err
			}
			meta := &SyncInputMetadata{Hash: hash, Space: in.Space, ContentRevision: in.Content.Revision, ChunkIDs: []string{}}
			for _, chunk := range in.Content.Chunks {
				meta.ChunkIDs = append(meta.ChunkIDs, chunk.ID)
			}
			out.Embedding = meta
		}
		return nil
	})
	if err != nil {
		return SyncStatus{}, err
	}
	return out, nil
}

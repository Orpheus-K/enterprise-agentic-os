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

type SyncReconciliation struct {
	ID              uuid.UUID  `json:"id"`
	SourceID        uuid.UUID  `json:"source_id"`
	DocumentID      string     `json:"document_id"`
	RequestRevision int64      `json:"request_revision"`
	Generation      int64      `json:"generation"`
	ConfigHash      string     `json:"config_hash"`
	Outcome         string     `json:"outcome"`
	EvidenceHash    string     `json:"evidence_hash"`
	Vectors         *VectorSet `json:"vectors,omitempty"`
}

// ReconcileSync accepts an operator's explicit evidence-backed determination,
// never an inferred timeout outcome. Both source authority and a separate
// knowledge.reconcile grant are required, including on idempotent retries.
func (s *Store) ReconcileSync(ctx context.Context, actor identity.ExternalIdentity, in SyncReconciliation) error {
	if in.ID == uuid.Nil || in.SourceID == uuid.Nil || !label(in.DocumentID, 256) ||
		in.RequestRevision < 1 || in.Generation < 1 || !configHashValid(in.ConfigHash) || !configHashValid(in.EvidenceHash) ||
		(in.Outcome != "not_sent" && in.Outcome != "completed") ||
		(in.Outcome == "not_sent" && in.Vectors != nil) ||
		(in.Outcome == "completed" && (in.Vectors == nil || in.Vectors.validate() != nil)) {
		return ErrInvalid
	}
	hash, err := digest(in)
	if err != nil {
		return err
	}
	var result []byte
	if in.Vectors != nil {
		result, err = json.Marshal(in.Vectors)
		if err != nil || len(result) > 16<<20 {
			return ErrInvalid
		}
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		var revision, generation int64
		var config, state string
		var pending bool
		var input []byte
		err := q.QueryRow(ctx, `SELECT request_revision,generation,config_hash,state,embedding_pending,embedding_input
FROM knowledge.sync_jobs WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 FOR UPDATE`,
			s.tenant, s.app, in.SourceID, in.DocumentID).Scan(&revision, &generation, &config, &state, &pending, &input)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		// Re-resolve after lock waits, then check the current grant in this tx.
		principal, err := s.repo.ResolveIn(ctx, q, actor)
		if err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, in.SourceID); err != nil {
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
		var previous string
		err = q.QueryRow(ctx, `SELECT request_hash FROM knowledge.sync_reconciliations
WHERE tenant_id=$1 AND app_id=$2 AND id=$3`, s.tenant, s.app, in.ID).Scan(&previous)
		if err == nil {
			if previous == hash {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if revision != in.RequestRevision || generation != in.Generation || config != in.ConfigHash || state != "running" || !pending {
			return ErrConflict
		}
		if in.Outcome == "completed" {
			var original embeddingInput
			if json.Unmarshal(input, &original) != nil || !vectorsMatch(original, *in.Vectors) {
				return ErrConflict
			}
		} else {
			input = nil
		}
		// An active worker cannot be reconciled out from underneath its request.
		tag, err := q.Exec(ctx, `UPDATE knowledge.sync_jobs SET state='pending',generation=generation+1,
lease_token=NULL,lease_until='-infinity',embedding_pending=false,embedding_input=$5,embedding_vectors=$6
WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 AND lease_until<=clock_timestamp()`,
			s.tenant, s.app, in.SourceID, in.DocumentID, input, result)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if _, err := q.Exec(ctx, `INSERT INTO knowledge.sync_reconciliations
(tenant_id,app_id,id,source_id,document_id,request_revision,generation,request_hash,outcome,evidence_hash,member_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			s.tenant, s.app, in.ID, in.SourceID, in.DocumentID, in.RequestRevision, in.Generation, hash, in.Outcome, in.EvidenceHash, principal.MemberID); err != nil {
			return err
		}
		return s.audit(ctx, q, in.SourceID, in.DocumentID, "sync.reconcile", in.Generation)
	})
}

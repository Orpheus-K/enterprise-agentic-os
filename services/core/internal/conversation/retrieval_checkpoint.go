package conversation

import (
	"context"
	"encoding/json"
	"slices"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/knowledge"
)

// SaveKnowledgeReferences pins the evidence set before its text reaches the model.
// ModelRunning already records an external intent (including the embedding request);
// a crash here must be reconciled rather than silently repeating provider calls.
// nil is reserved for "not prepared"; an empty, non-nil slice records zero results.
// The checkpoint is immutable, including across tool turns and delivery recovery.
func (s *Store) SaveKnowledgeReferences(ctx context.Context, t *Task, refs []knowledge.Reference) error {
	if t.State != ModelRunning || t.KnowledgeRefs != nil || refs == nil || len(refs) > 50 {
		return ErrInvalidTransition
	}
	seen := map[knowledge.Reference]bool{}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil || seen[ref] {
			return knowledge.ErrInvalid
		}
		seen[ref] = true
	}
	raw, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	err = s.scoped(ctx, func(q database.Queries) error {
		if err := s.lockTask(ctx, q, t); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `UPDATE channel.tasks SET knowledge_refs=$8,
 step_version=step_version+1,updated_at=clock_timestamp()
 WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4 AND lease_owner=$5
 AND lease_generation=$6 AND step_version=$7 AND state='model_running'
 AND lease_until>clock_timestamp() AND knowledge_refs IS NULL`,
			s.tenant, s.app, s.revision, t.ID, s.owner, t.Generation, t.Version, raw)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return audit(ctx, q, s.tenant, t.ID, string(ModelRunning), "knowledge_checkpoint")
	})
	if err == nil {
		t.KnowledgeRefs = slices.Clone(refs)
		t.Version++
	}
	return err
}

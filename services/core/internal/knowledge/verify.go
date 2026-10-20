package knowledge

import (
	"context"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

// VerifyIn checks the complete dependency set under the caller's transaction,
// after any task lock wait. It installs actor's local identity context and requires
// the original mapped member. Callers must not release content on denial and
// must propagate storage/audit failures so the transaction rolls back.
// No body is returned; authorization and read audit use exactly Read's predicate.
func (s *Store) VerifyIn(ctx context.Context, q database.Queries, actor identity.ExternalIdentity, member uuid.UUID, refs []Reference) error {
	if actor.TenantID != s.tenant || member == uuid.Nil || len(refs) > 50 {
		return ErrInvalid
	}
	p, err := s.repo.ResolveIn(ctx, q, actor)
	if err != nil {
		return err
	}
	if p.MemberID != member {
		return identity.ErrUnauthenticated
	}
	if _, err := q.Exec(ctx, "SELECT set_config('agentos.app_id',$1,true)", s.app); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return err
		}
		if _, err := s.read(ctx, q, ref); err != nil {
			return err
		}
	}
	return nil
}

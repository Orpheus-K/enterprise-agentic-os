package knowledge

import (
	"context"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

// MemberProof binds a source-checked external identity to its observed IAM
// member. It is supplied by a trusted adapter, never an employee request.
type MemberProof struct {
	Actor     identity.ExternalIdentity
	Principal identity.Principal
}

// PublishSyncForMembers rechecks the synchronization actor and every permitted
// member after job/document lock waits and again before completion. Native
// source access must already have been checked by the adapter; this operation
// does not turn an IAM mapping into proof of access to a source document.
func (s *Store) PublishSyncForMembers(ctx context.Context, sync MemberProof, lease SyncLease, p Publication, recipients []MemberProof) error {
	valid := func(proof MemberProof) bool {
		return proof.Actor.Validate() == nil && proof.Principal.Validate() == nil &&
			proof.Actor.TenantID == s.tenant && proof.Principal.TenantID == s.tenant
	}
	if !valid(sync) || len(recipients) != len(p.ACL.Allow) || len(recipients) > 1000 {
		return ErrInvalid
	}
	allowed := map[uuid.UUID]bool{}
	for _, id := range p.ACL.Allow {
		allowed[id] = true
	}
	for _, proof := range recipients {
		if !valid(proof) || !allowed[proof.Principal.MemberID] {
			return ErrInvalid
		}
		delete(allowed, proof.Principal.MemberID)
	}
	if len(allowed) != 0 {
		return ErrInvalid
	}
	check := func(q database.Queries) error {
		for _, proof := range recipients {
			current, err := s.repo.ResolveIn(ctx, q, proof.Actor)
			if err != nil {
				return err
			}
			if current != proof.Principal {
				return identity.ErrUnauthenticated
			}
		}
		// ResolveIn changes transaction-local identity; restore the exact
		// source actor before any write, RLS check or audit.
		current, err := s.repo.ResolveIn(ctx, q, sync.Actor)
		if err != nil {
			return err
		}
		if current != sync.Principal {
			return identity.ErrUnauthenticated
		}
		return s.syncAllowed(ctx, q, lease.SourceID)
	}
	return s.publishSync(ctx, sync.Actor, lease, p, check)
}

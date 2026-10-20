package sqlite

import (
	"agentos.local/core/internal/identity"
	"context"
	"database/sql"
)

func (s *Inbox) AuthorizeOperation(ctx context.Context, actor identity.ExternalIdentity, capability string) error {
	if actor.TenantID != s.tenant {
		return identity.ErrUnauthenticated
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := s.identities.RequireCapabilityIn(ctx, tx, actor, s.app, capability)
		return err
	})
}

package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"context"
)

func (s *Store) AuthorizeOperation(ctx context.Context, actor identity.ExternalIdentity, capability string) error {
	return s.Operation(ctx, actor, capability, func(database.Queries) error { return nil })
}

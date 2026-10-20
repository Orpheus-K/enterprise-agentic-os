//go:build integration

package postgres

import (
	"context"
	"testing"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/identity/contracttest"
)

func TestCapabilityContract(t *testing.T) {
	f, _ := setup(t)
	contracttest.RunCapabilities(t, func(ctx context.Context, external identity.ExternalIdentity, app, capability string) (identity.Principal, error) {
		var p identity.Principal
		err := f.db.Transaction(ctx, func(q database.Queries) error {
			var err error
			p, err = f.repo.RequireCapabilityIn(ctx, q, external, app, capability)
			return err
		})
		if err != nil {
			return identity.Principal{}, err
		}
		return p, nil
	}, func(query string, args ...any) error {
		_, err := f.Admin.Exec(t.Context(), query, args...)
		return err
	})
}

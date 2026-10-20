//go:build integration

package postgres

import (
	"context"
	"testing"

	"agentos.local/core/internal/identity/contracttest"
)

func TestResolveContract(t *testing.T) {
	f, ctx := setup(t)
	contracttest.Run(t, ctx, f.repo, func(ctx context.Context, query string, args ...any) error {
		_, err := f.Admin.Exec(ctx, query, args...)
		return err
	})
}

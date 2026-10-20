package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/identity/contracttest"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func TestResolveContract(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity #%.db")
	if err := sqlitedb.Migrate(ctx, path); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	contracttest.Run(t, ctx, New(db), func(ctx context.Context, query string, args ...any) error {
		_, err := db.ExecContext(ctx, strings.ReplaceAll(query, "iam.", "iam_"), args...)
		return err
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	principal, err := New(db).Resolve(ctx, identity.ExternalIdentity{
		TenantID: uuid.New(), Provider: "oidc", Issuer: "https://issuer.example", Subject: "external",
	})
	if err == nil || errors.Is(err, identity.ErrUnauthenticated) || principal != (identity.Principal{}) {
		t.Fatalf("infrastructure failure was hidden: %v %v", principal, err)
	}
}

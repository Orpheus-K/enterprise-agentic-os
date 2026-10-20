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
)

func TestCapabilityContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capability.db")
	if err := sqlitedb.Migrate(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := New(db)
	check := func(ctx context.Context, external identity.ExternalIdentity, app, capability string) (identity.Principal, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return identity.Principal{}, err
		}
		p, err := repo.RequireCapabilityIn(ctx, tx, external, app, capability)
		if err != nil {
			return identity.Principal{}, errors.Join(err, tx.Rollback())
		}
		if err := tx.Commit(); err != nil {
			return identity.Principal{}, err
		}
		return p, nil
	}
	contracttest.RunCapabilities(t, check, func(query string, args ...any) error {
		_, err := db.ExecContext(t.Context(), strings.ReplaceAll(query, "iam.", "iam_"), args...)
		return err
	})
	if _, err := db.ExecContext(t.Context(), `INSERT INTO iam_operation_grants(tenant_id,member_id,app_id,capability)
SELECT tenant_id,member_id,'knowledge-app','knowledge.reconcile' FROM iam_identity_links LIMIT 1`); err == nil {
		t.Fatal("accepted unsupported knowledge capability")
	}
	if _, err := db.ExecContext(t.Context(), "DROP TABLE iam_operation_grants"); err != nil {
		t.Fatal(err)
	}
	// A valid identity with an unavailable authorization table must fail as
	// infrastructure failure, not as successful authorization or a missing grant.
	var tenant string
	if err := db.QueryRowContext(t.Context(), "SELECT tenant_id FROM iam_identity_links LIMIT 1").Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	key := identity.ExternalIdentity{Provider: "operator", Issuer: "issuer", Subject: "subject"}
	if err := key.TenantID.Scan(tenant); err != nil {
		t.Fatal(err)
	}
	if p, err := check(t.Context(), key, "app", "task.approve"); err == nil || errors.Is(err, identity.ErrForbidden) || p != (identity.Principal{}) {
		t.Fatalf("hidden database failure: %v %v", p, err)
	}
}

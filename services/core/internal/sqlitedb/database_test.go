package sqlitedb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitMigrationAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "identity #%.db")
	if db, err := Open(ctx, path); err == nil {
		db.Close()
		t.Fatal("opened nonexistent database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("runtime created file: %v", err)
	}
	for range 2 {
		if err := Migrate(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO iam_tenants(id,name) VALUES('tenant','persisted')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRowContext(ctx, "SELECT name FROM iam_tenants WHERE id='tenant'").Scan(&name); err != nil || name != "persisted" {
		t.Fatalf("reopen: %q %v", name, err)
	}
	// Force replacement connections: foreign keys must apply to each, not just
	// the connection on which the migration or startup probe ran.
	db.SetMaxIdleConns(0)
	for range 2 {
		if _, err := db.ExecContext(ctx, "INSERT INTO iam_members(tenant_id,id,employee_id) VALUES('absent','m','e')"); err == nil {
			t.Fatal("foreign key not enforced")
		}
	}
	for _, query := range []string{
		"INSERT INTO iam_tenants(id,name) VALUES('other','other')",
		"INSERT INTO iam_members(tenant_id,id,employee_id) VALUES('tenant','m','e')",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO iam_identity_links(tenant_id,provider,issuer,subject,member_id) VALUES('other','oidc','issuer','subject','m')"); err == nil {
		t.Fatal("accepted cross-tenant member reference")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO goose_db_version(version_id,is_applied) VALUES(999,1)"); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(ctx, path); err == nil {
		other.Close()
		t.Fatal("opened future schema")
	}
	if err := Migrate(ctx, path); err == nil {
		t.Fatal("migrated future schema")
	}
}

func TestRejectUnmigratedAndInvalidPaths(t *testing.T) {
	ctx := context.Background()
	for _, path := range []string{"", ":memory:", "relative.db", "file:test.db?mode=memory"} {
		if err := Migrate(ctx, path); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(ctx, path); err == nil {
		db.Close()
		t.Fatal("opened unmigrated database")
	}
}

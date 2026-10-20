package sqlitedb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestUpgradePreservesIdentity(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := connect(ctx, path, "rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, err := migrations.ReadFile("migrations/00001_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := migrations.ReadFile("migrations/00002_inbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	old, err := goose.NewProvider(goose.DialectSQLite3, db,
		fstest.MapFS{"00001_identity.sql": &fstest.MapFile{Data: source}, "00002_inbox.sql": &fstest.MapFile{Data: inbox}},
		goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO iam_tenants(id,name) VALUES('tenant','retained')"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO iam_members(tenant_id,id,employee_id) VALUES('tenant','member','employee')",
		"INSERT INTO channel_tasks(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt) VALUES('tenant','task','member','app','issuer','subject','event','message','hash','revision','retained prompt')",
		"INSERT INTO channel_audit(tenant_id,task_id,state,reason) VALUES('tenant','task','pending','')",
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, path); err != nil {
		t.Fatal(err)
	}
	current, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	var name string
	if err := current.QueryRowContext(ctx, "SELECT name FROM iam_tenants WHERE id='tenant'").Scan(&name); err != nil || name != "retained" {
		t.Fatalf("identity lost during migration: %q %v", name, err)
	}
	var count int
	if err := current.QueryRowContext(ctx, "SELECT count(*) FROM channel_tasks WHERE prompt='retained prompt' AND state='pending' AND step_version=1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("task table unavailable after upgrade: %d %v", count, err)
	}
	if err := current.QueryRowContext(ctx, "SELECT count(*) FROM channel_audit WHERE task_id='task' AND step_version=1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit lost during upgrade: %d %v", count, err)
	}
}

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

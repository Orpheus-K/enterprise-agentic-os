//go:build integration

package database

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"agentos.local/core/internal/testpg"
)

func TestConcurrentMigrationsAndRepeat(t *testing.T) {
	f := testpg.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			m, err := NewMigrator(ctx, f.MigrationURL)
			if err != nil {
				errors <- err
				return
			}
			_, err = m.Up(ctx)
			errors <- err
			errors <- m.Close()
		})
	}
	// Drain while both independent migrators run to avoid buffering the test itself.
	go func() { wg.Wait(); close(errors) }()
	for err := range errors {
		if err != nil {
			t.Error(err)
		}
	}
	m, err := NewMigrator(ctx, f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	}()
	count, err := m.Up(ctx)
	if err != nil || count != 0 {
		t.Fatalf("repeat migration: count=%d err=%v", count, err)
	}
	var protected int
	if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='iam' AND c.relkind='r' AND c.relrowsecurity AND c.relforcerowsecurity`).Scan(&protected); err != nil {
		t.Fatal(err)
	}
	if protected != 5 {
		t.Fatalf("want 4 forced-RLS tables, got %d", protected)
	}
}

func TestFailedMigrationRollsBackDDLAndVersion(t *testing.T) {
	f := testpg.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first, err := fs.ReadFile(migrationFiles, "migrations/00001_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{
		"00001_identity.sql": {Data: first},
		"00002_failure.sql":  {Data: []byte("-- +goose Up\nCREATE TABLE public.should_rollback(id int);\nSELECT 1/0;\n")},
	}
	m, err := newMigrator(ctx, f.MigrationURL, files)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := m.Up(ctx); err == nil {
		t.Fatal("expected failed migration")
	}
	var rolledBack bool
	if err := f.Admin.QueryRow(ctx, `SELECT to_regclass('public.should_rollback') IS NULL AND NOT EXISTS(SELECT 1 FROM public.goose_db_version WHERE version_id=2 AND is_applied)`).Scan(&rolledBack); err != nil {
		t.Fatal(err)
	}
	if !rolledBack {
		t.Fatal("failed migration leaked DDL or version")
	}
}

func TestUpgradePreservesIdentity(t *testing.T) {
	f := testpg.New(t)
	ctx := t.Context()
	first, err := fs.ReadFile(migrationFiles, "migrations/00001_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	old, err := newMigrator(ctx, f.MigrationURL, fstest.MapFS{"00001_identity.sql": {Data: first}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(ctx, "INSERT INTO iam.tenants(id,name) VALUES('10000000-0000-0000-0000-000000000001','preserved')"); err != nil {
		t.Fatal(err)
	}
	m, err := NewMigrator(ctx, f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	count, migrateErr := m.Up(ctx)
	closeErr := m.Close()
	if migrateErr != nil || closeErr != nil || count != SchemaVersion-1 {
		t.Fatalf("upgrade count=%d migration=%v close=%v", count, migrateErr, closeErr)
	}
	var name string
	if err := f.Admin.QueryRow(ctx, "SELECT name FROM iam.tenants").Scan(&name); err != nil || name != "preserved" {
		t.Fatalf("identity changed: %v", err)
	}
}

func TestUpgradePreservesLegacyTasksAndAudit(t *testing.T) {
	f := testpg.New(t)
	ctx := t.Context()
	files := fstest.MapFS{}
	for _, name := range []string{"00001_identity.sql", "00002_channel_tasks.sql"} {
		body, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: body}
	}
	old, err := newMigrator(ctx, f.MigrationURL, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = f.Admin.Exec(ctx, `
 INSERT INTO iam.tenants(id,name) VALUES('10000000-0000-0000-0000-000000000001','preserved');
 INSERT INTO iam.members(tenant_id,id,employee_id) SELECT id,id,'employee' FROM iam.tenants;
 INSERT INTO channel.tasks(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt)
 SELECT id,id,id,'app','issuer','subject','event','message','fingerprint','revision','legacy-prompt' FROM iam.tenants;
 INSERT INTO channel.audit(tenant_id,task_id,state,reason) SELECT tenant_id,id,'pending','' FROM channel.tasks;`)
	if err != nil {
		t.Fatal(err)
	}
	if db, err := Open(ctx, f.RuntimeURL, 1); err == nil {
		db.Close()
		t.Fatal("accepted outdated schema")
	}
	m, err := NewMigrator(ctx, f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	count, upErr := m.Up(ctx)
	closeErr := m.Close()
	if upErr != nil || closeErr != nil || count != SchemaVersion-2 {
		t.Fatalf("upgrade count=%d up=%v close=%v", count, upErr, closeErr)
	}
	var preserved bool
	if err := f.Admin.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(t.prompt='legacy-prompt' AND t.state='pending' AND t.ingress_trace_id='' AND a.id>0 AND a.trace_id='') FROM channel.tasks t JOIN channel.audit a ON t.id=a.task_id AND t.tenant_id=a.tenant_id`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("legacy state changed: %v", err)
	}
}

func TestUpgradeQuarantinesUnownedAttempts(t *testing.T) {
	f := testpg.New(t)
	ctx := t.Context()
	files := fstest.MapFS{}
	for _, name := range []string{"00001_identity.sql", "00002_channel_tasks.sql", "00003_observability.sql"} {
		body, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = &fstest.MapFile{Data: body}
	}
	old, err := newMigrator(ctx, f.MigrationURL, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = f.Admin.Exec(ctx, `INSERT INTO iam.tenants(id,name) VALUES('10000000-0000-0000-0000-000000000001','legacy');
 INSERT INTO iam.members(tenant_id,id,employee_id) SELECT id,id,'legacy' FROM iam.tenants;
 INSERT INTO channel.tasks(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,response,state)
 SELECT t.id,gen_random_uuid(),t.id,'app','issuer','subject',s.state,s.state,'fingerprint','v1','synthetic','saved',s.state FROM iam.tenants t CROSS JOIN (VALUES('pending'),('model_running'),('delivery_running'),('succeeded')) s(state);
 INSERT INTO channel.audit(tenant_id,task_id,state,reason) SELECT tenant_id,id,state,'' FROM channel.tasks;`)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMigrator(ctx, f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	count, err := m.Up(ctx)
	if err != nil || count != SchemaVersion-3 {
		t.Fatalf("migration: %d %v", count, err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	var preserved bool
	err = f.Admin.QueryRow(ctx, `SELECT count(*)=4 AND count(*) FILTER(WHERE state='unknown' AND reason='migration_unowned_attempt')=2 AND count(*) FILTER(WHERE state='pending')=1 AND count(*) FILTER(WHERE state='succeeded')=1 AND bool_and(lease_owner IS NULL AND lease_until IS NULL AND response='saved' AND model_revision='v1') FROM channel.tasks`).Scan(&preserved)
	if err != nil || !preserved {
		t.Fatalf("legacy attempts not quarantined: %v", err)
	}
	var protected bool
	if err := f.Admin.QueryRow(ctx, `SELECT count(*)=14 AND bool_and(c.relrowsecurity AND c.relforcerowsecurity) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='channel' AND c.relkind='r'`).Scan(&protected); err != nil || !protected {
		t.Fatal("migration did not restore forced RLS")
	}
	var events int
	err = f.Admin.QueryRow(ctx, "SELECT count(*) FROM channel.audit").Scan(&events)
	if err != nil || events != 6 {
		t.Fatal("legacy events lost or recovery events missing")
	}
}

func TestVectorMigrationRequiresAdministratorExtension(t *testing.T) {
	f := testpg.New(t)
	ctx := t.Context()
	if _, err := f.Admin.Exec(ctx, "DROP EXTENSION vector"); err != nil {
		t.Fatal(err)
	}
	m, err := NewMigrator(ctx, f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err == nil {
		t.Fatal("schema 17 applied without the administrator-installed extension")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	var version int64
	var extension bool
	if err := f.Admin.QueryRow(ctx, `SELECT max(version_id),EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector') FROM public.goose_db_version WHERE is_applied`).Scan(&version, &extension); err != nil || version != 16 || extension {
		t.Fatalf("version=%d extension=%v err=%v", version, extension, err)
	}
	if db, err := Open(ctx, f.RuntimeURL, 1); err == nil {
		db.Close()
		t.Fatal("runtime accepted schema 16")
	}
	if _, err := f.Admin.Exec(ctx, "CREATE EXTENSION vector WITH SCHEMA public VERSION '0.8.7'"); err != nil {
		t.Fatal(err)
	}
	m, err = NewMigrator(ctx, f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	count, upErr := m.Up(ctx)
	if err := errors.Join(upErr, m.Close()); err != nil || count != SchemaVersion-16 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	db, err := Open(ctx, f.RuntimeURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

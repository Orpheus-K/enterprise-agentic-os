//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fixture struct {
	*testpg.Fixture
	db              *database.DB
	repo            *Repository
	a, b, colleague identity.Principal
	external        identity.ExternalIdentity
}

func setup(t *testing.T) (*fixture, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := &fixture{Fixture: testpg.New(t)}
	m, err := database.NewMigrator(ctx, f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, migrateErr := m.Up(ctx)
	if err := errors.Join(migrateErr, m.Close()); err != nil {
		t.Fatal(err)
	}
	f.a = identity.Principal{TenantID: uuid.New(), MemberID: uuid.New()}
	f.b = identity.Principal{TenantID: uuid.New(), MemberID: uuid.New()}
	f.colleague = identity.Principal{TenantID: f.a.TenantID, MemberID: uuid.New()}
	for _, p := range []identity.Principal{f.a, f.b} {
		if _, err := f.Admin.Exec(ctx, "INSERT INTO iam.tenants(id,name) VALUES($1,$2)", p.TenantID, "test tenant"); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []identity.Principal{f.a, f.b, f.colleague} {
		if _, err := f.Admin.Exec(ctx, "INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,$3)", p.TenantID, p.MemberID, p.MemberID.String()); err != nil {
			t.Fatal(err)
		}
	}
	f.external = identity.ExternalIdentity{TenantID: f.a.TenantID, Provider: "oidc", Issuer: "https://trusted.example", Subject: "external-user"}
	if _, err := f.Admin.Exec(ctx, "INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,$2,$3,$4,$5)", f.external.TenantID, f.external.Provider, f.external.Issuer, f.external.Subject, f.a.MemberID); err != nil {
		t.Fatal(err)
	}
	// Test-only domain records prove write isolation without inventing a product API.
	_, err = f.Admin.Exec(ctx, `CREATE SCHEMA probe;
        CREATE TABLE probe.entries(tenant_id uuid NOT NULL, member_id uuid NOT NULL, value text NOT NULL,
          FOREIGN KEY(tenant_id,member_id) REFERENCES iam.members(tenant_id,id));
        ALTER TABLE probe.entries ENABLE ROW LEVEL SECURITY;
        ALTER TABLE probe.entries FORCE ROW LEVEL SECURITY;
        CREATE POLICY scope ON probe.entries USING (
          tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
          AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
        ) WITH CHECK (
          tenant_id=nullif(current_setting('agentos.tenant_id',true),'')::uuid
          AND member_id=nullif(current_setting('agentos.member_id',true),'')::uuid
        );
        GRANT USAGE ON SCHEMA probe TO agentos_runtime;
        GRANT SELECT,INSERT,UPDATE,DELETE ON probe.entries TO agentos_runtime;`)
	if err != nil {
		t.Fatal(err)
	}
	f.db, err = database.Open(ctx, f.RuntimeURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.db.Close)
	f.repo = New(f.db)
	return f, ctx
}

func insert(ctx context.Context, q database.Queries, p identity.Principal, value string) error {
	_, err := q.Exec(ctx, "INSERT INTO probe.entries VALUES($1,$2,$3)", p.TenantID, p.MemberID, value)
	return err
}

func requireSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("want SQLSTATE %s, got %v", code, err)
	}
}

func TestTenantAndMemberIsolationWithPoolReuse(t *testing.T) {
	f, ctx := setup(t)
	for _, p := range []identity.Principal{f.a, f.b, f.colleague} {
		if err := f.repo.Within(ctx, p, func(q database.Queries) error { return insert(ctx, q, p, p.MemberID.String()) }); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []identity.Principal{f.a, f.b, f.colleague, f.a} {
		err := f.repo.Within(ctx, p, func(q database.Queries) error {
			var value string
			var count int
			if err := q.QueryRow(ctx, "SELECT count(*) FROM iam.members").Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Errorf("want one visible IAM member, got %d", count)
			}
			if err := q.QueryRow(ctx, "SELECT count(*) FROM probe.entries").Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Errorf("want exactly one visible record, got %d", count)
			}
			if err := q.QueryRow(ctx, "SELECT value FROM probe.entries").Scan(&value); err != nil {
				return err
			}
			if value != p.MemberID.String() {
				t.Error("cross-principal record exposed")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	err := f.repo.Within(ctx, f.a, func(q database.Queries) error { return insert(ctx, q, f.b, "forbidden") })
	requireSQLState(t, err, "42501")
	err = f.repo.Within(ctx, f.a, func(q database.Queries) error {
		result, err := q.Exec(ctx, "UPDATE probe.entries SET value='forbidden' WHERE tenant_id=$1", f.b.TenantID)
		if err == nil && result.RowsAffected() != 0 {
			t.Error("cross-tenant update succeeded")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Same pool, no identity: SET LOCAL from all prior transactions must not leak.
	err = f.db.Transaction(ctx, func(q database.Queries) error {
		var count int
		if err := q.QueryRow(ctx, "SELECT count(*) FROM probe.entries").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Error("pooled identity leaked into unscoped transaction")
		}
		return insert(ctx, q, f.a, "unscoped")
	})
	requireSQLState(t, err, "42501")
}

func TestIdentityResolutionAndRevocation(t *testing.T) {
	f, ctx := setup(t)
	p, err := f.repo.Resolve(ctx, f.external)
	if err != nil || p != f.a {
		t.Fatalf("resolve failed: %v", err)
	}
	for _, field := range []string{"issuer", "subject", "tenant"} {
		input := f.external
		switch field {
		case "issuer":
			input.Issuer = "https://untrusted.example"
		case "subject":
			input.Subject = "unknown"
		case "tenant":
			input.TenantID = f.b.TenantID
		}
		if _, err := f.repo.Resolve(ctx, input); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatalf("accepted wrong %s: %v", field, err)
		}
	}
	if _, err := f.Admin.Exec(ctx, "UPDATE iam.members SET enabled=false WHERE tenant_id=$1 AND id=$2", p.TenantID, p.MemberID); err != nil {
		t.Fatal(err)
	}
	called := false
	err = f.repo.Within(ctx, p, func(database.Queries) error { called = true; return nil })
	if !errors.Is(err, identity.ErrUnauthenticated) || called {
		t.Fatal("revoked membership was allowed")
	}
	if _, err := f.repo.Resolve(ctx, f.external); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("resolved revoked member: %v", err)
	}
	if _, err := f.Admin.Exec(ctx, "UPDATE iam.members SET enabled=true WHERE tenant_id=$1 AND id=$2", p.TenantID, p.MemberID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(ctx, "UPDATE iam.tenants SET enabled=false WHERE id=$1", p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Within(ctx, p, func(database.Queries) error { return nil }); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("accepted disabled tenant: %v", err)
	}
}

func TestIdentityMappingConstraints(t *testing.T) {
	f, ctx := setup(t)
	query := "INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,$2,$3,$4,$5)"
	_, err := f.Admin.Exec(ctx, query, f.a.TenantID, f.external.Provider, f.external.Issuer, f.external.Subject, f.colleague.MemberID)
	requireSQLState(t, err, "23505")
	_, err = f.Admin.Exec(ctx, query, f.a.TenantID, f.external.Provider, f.external.Issuer, "cross-tenant", f.b.MemberID)
	requireSQLState(t, err, "23503")
	_, err = f.Admin.Exec(ctx, query, f.b.TenantID, f.external.Provider, f.external.Issuer, f.external.Subject, f.b.MemberID)
	if err != nil {
		t.Fatal(err)
	}
	other := f.external
	other.TenantID = f.b.TenantID
	if p, err := f.repo.Resolve(ctx, other); err != nil || p != f.b {
		t.Fatalf("tenant-scoped mapping failed: %v", err)
	}
	if _, err := f.Admin.Exec(ctx, "UPDATE iam.identity_links SET enabled=false WHERE tenant_id=$1", f.a.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.Resolve(ctx, f.external); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("accepted disabled identity mapping: %v", err)
	}
	if err := f.db.Transaction(ctx, func(q database.Queries) error {
		var count int
		if err := q.QueryRow(ctx, "SELECT count(*) FROM iam.identity_links").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Error("identity mapping leaked without context")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAtomicCommitRollbackAndPanic(t *testing.T) {
	f, ctx := setup(t)
	abort := errors.New("cancel second write")
	err := f.repo.Within(ctx, f.a, func(q database.Queries) error {
		if err := insert(ctx, q, f.a, "first"); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() != "abort" {
				t.Error("panic not propagated")
			}
		}()
		_ = f.repo.Within(ctx, f.a, func(q database.Queries) error {
			if err := insert(ctx, q, f.a, "panic"); err != nil {
				t.Fatal(err)
			}
			panic("abort")
		})
	}()
	ctxCancelled, cancel := context.WithCancel(ctx)
	err = f.repo.Within(ctxCancelled, f.a, func(q database.Queries) error {
		if err := insert(ctxCancelled, q, f.a, "cancel"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("committed cancelled transaction")
	}
	err = f.repo.Within(ctx, f.a, func(q database.Queries) error {
		var count int
		if err := q.QueryRow(ctx, "SELECT count(*) FROM probe.entries").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("rollback leaked %d rows", count)
		}
		if err := insert(ctx, q, f.a, "committed"); err != nil {
			return err
		}
		return insert(ctx, q, f.a, "also committed")
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.Admin.QueryRow(ctx, "SELECT count(*) FROM probe.entries").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("want atomic commit of 2 rows, got %d", count)
	}
}

func TestRuntimeCannotEscalateOrMigrate(t *testing.T) {
	f, ctx := setup(t)
	for _, privilegedURL := range []string{f.AdminURL, f.MigrationURL} {
		db, err := database.Open(ctx, privilegedURL, 1)
		if err == nil {
			db.Close()
			t.Fatal("privileged runtime connection accepted")
		}
	}
	err := f.repo.Within(ctx, f.a, func(q database.Queries) error {
		_, err := q.Exec(ctx, "UPDATE iam.members SET enabled=true")
		return err
	})
	requireSQLState(t, err, "42501")
	err = f.db.Transaction(ctx, func(q database.Queries) error { _, err := q.Exec(ctx, "SET ROLE agentos_migrator"); return err })
	requireSQLState(t, err, "42501")
	err = f.db.Transaction(ctx, func(q database.Queries) error {
		_, err := q.Exec(ctx, "CREATE TABLE public.forbidden(id int)")
		return err
	})
	requireSQLState(t, err, "42501")
	// Even the table owner is subject to FORCE RLS when no context is present.
	config, err := pgx.ParseConfig(f.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["role"] = "agentos_migrator"
	owner, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	var count int
	if err := owner.QueryRow(ctx, "SELECT count(*) FROM iam.members").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("table owner bypassed FORCE RLS")
	}
}

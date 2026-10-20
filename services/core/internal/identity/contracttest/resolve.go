// Package contracttest defines the identity behavior required of every driver.
package contracttest

import (
	"context"
	"errors"
	"testing"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type Resolver interface {
	Resolve(context.Context, identity.ExternalIdentity) (identity.Principal, error)
}

// Run uses an isolated database and a privileged fixture writer. SQL uses IAM
// table names; adapters only translate table names and execute against real DBs.
func Run(t *testing.T, ctx context.Context, resolver Resolver, exec func(context.Context, string, ...any) error) {
	t.Helper()
	write := func(t *testing.T, query string, args ...any) {
		t.Helper()
		if err := exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	a := identity.Principal{TenantID: uuid.New(), MemberID: uuid.New()}
	b := identity.Principal{TenantID: uuid.New(), MemberID: a.MemberID}
	colleague := uuid.New()
	for _, p := range []identity.Principal{a, b} {
		write(t, "INSERT INTO iam.tenants(id,name) VALUES($1,$2)", p.TenantID.String(), "contract")
		write(t, "INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,$3)", p.TenantID.String(), p.MemberID.String(), "employee")
		write(t, "INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,$2,$3,$4,$5)",
			p.TenantID.String(), "oidc", "https://issuer.example", "external", p.MemberID.String())
	}
	write(t, "INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,$3)", a.TenantID.String(), colleague.String(), "colleague")
	key := identity.ExternalIdentity{TenantID: a.TenantID, Provider: "oidc", Issuer: "https://issuer.example", Subject: "external"}
	check := func(t *testing.T, ctx context.Context, key identity.ExternalIdentity, want identity.Principal, wantErr error) {
		t.Helper()
		got, err := resolver.Resolve(ctx, key)
		if !errors.Is(err, wantErr) || got != want {
			t.Fatalf("Resolve: principal=%v err=%v; want %v, %v", got, err, want, wantErr)
		}
	}
	check(t, ctx, key, a, nil)
	other := key
	other.TenantID = b.TenantID
	check(t, ctx, other, b, nil)
	for _, field := range []string{"tenant", "provider", "issuer", "subject"} {
		t.Run("exact_"+field, func(t *testing.T) {
			wrong := key
			switch field {
			case "tenant":
				wrong.TenantID = uuid.New()
			case "provider":
				wrong.Provider = "OIDC"
			case "issuer":
				wrong.Issuer += "/"
			case "subject":
				wrong.Subject = "External"
			}
			check(t, ctx, wrong, identity.Principal{}, identity.ErrUnauthenticated)
		})
	}
	for _, table := range []string{"tenants", "members", "identity_links"} {
		t.Run("disable_"+table, func(t *testing.T) {
			predicate := "tenant_id=$2"
			if table == "tenants" {
				predicate = "id=$2"
			}
			query := "UPDATE iam." + table + " SET enabled=$1 WHERE " + predicate
			write(t, query, false, a.TenantID.String())
			check(t, ctx, key, identity.Principal{}, identity.ErrUnauthenticated)
			check(t, ctx, other, b, nil)
			write(t, query, true, a.TenantID.String())
			check(t, ctx, key, a, nil)
		})
	}
	write(t, "UPDATE iam.identity_links SET member_id=$1 WHERE tenant_id=$2", colleague.String(), a.TenantID.String())
	check(t, ctx, key, identity.Principal{TenantID: a.TenantID, MemberID: colleague}, nil)
	write(t, "DELETE FROM iam.identity_links WHERE tenant_id=$1", a.TenantID.String())
	check(t, ctx, key, identity.Principal{}, identity.ErrUnauthenticated)
	check(t, ctx, other, b, nil)
	invalid := key
	invalid.Subject = " external "
	check(t, ctx, invalid, identity.Principal{}, identity.ErrInvalidIdentity)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	check(t, cancelled, other, identity.Principal{}, context.Canceled)
}

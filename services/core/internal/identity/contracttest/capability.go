package contracttest

import (
	"context"
	"errors"
	"testing"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type CapabilityCheck func(context.Context, identity.ExternalIdentity, string, string) (identity.Principal, error)

func RunCapabilities(t *testing.T, check CapabilityCheck, exec func(string, ...any) error) {
	t.Helper()
	ctx := t.Context()
	tenant, other, member, replacement := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	write := func(query string, args ...any) {
		t.Helper()
		if err := exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uuid.UUID{tenant, other} {
		write("INSERT INTO iam.tenants(id,name) VALUES($1,'capabilities')", id.String())
		write("INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'employee')", id.String(), member.String())
		write("INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,'operator','issuer','subject',$2)", id.String(), member.String())
	}
	write("INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'replacement')", tenant.String(), replacement.String())
	key := identity.ExternalIdentity{TenantID: tenant, Provider: "operator", Issuer: "issuer", Subject: "subject"}
	want := identity.Principal{TenantID: tenant, MemberID: member}
	verify := func(key identity.ExternalIdentity, app, capability string, p identity.Principal, wantErr error) {
		t.Helper()
		got, err := check(ctx, key, app, capability)
		if got != p || !errors.Is(err, wantErr) {
			t.Fatalf("capability %s/%s: %v %v, want %v %v", app, capability, got, err, p, wantErr)
		}
	}
	verify(key, "app", "task.approve", identity.Principal{}, identity.ErrForbidden)
	for _, capability := range []string{"audit.read", "status.read", "task.reconcile", "task.approve"} {
		write("INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'app',$3)", tenant.String(), member.String(), capability)
		verify(key, "app", capability, want, nil)
	}
	verify(key, "another-app", "task.approve", identity.Principal{}, identity.ErrForbidden)
	verify(key, "app", "unknown", identity.Principal{}, identity.ErrForbidden)
	verify(key, "app", "Task.approve", identity.Principal{}, identity.ErrForbidden)
	cross := key
	cross.TenantID = other
	verify(cross, "app", "task.approve", identity.Principal{}, identity.ErrForbidden)
	absent := key
	absent.Subject = "absent"
	verify(absent, "app", "task.approve", identity.Principal{}, identity.ErrUnauthenticated)
	for _, table := range []string{"operation_grants", "identity_links", "members", "tenants"} {
		column := "tenant_id"
		if table == "tenants" {
			column = "id"
		}
		write("UPDATE iam."+table+" SET enabled=$1 WHERE "+column+"=$2", false, tenant.String())
		wantErr := identity.ErrUnauthenticated
		if table == "operation_grants" {
			wantErr = identity.ErrForbidden
		}
		verify(key, "app", "task.approve", identity.Principal{}, wantErr)
		write("UPDATE iam."+table+" SET enabled=$1 WHERE "+column+"=$2", true, tenant.String())
		verify(key, "app", "task.approve", want, nil)
	}
	write("UPDATE iam.identity_links SET member_id=$1 WHERE tenant_id=$2", replacement.String(), tenant.String())
	verify(key, "app", "task.approve", identity.Principal{}, identity.ErrForbidden)
	write("INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'app','task.approve')", tenant.String(), replacement.String())
	verify(key, "app", "task.approve", identity.Principal{TenantID: tenant, MemberID: replacement}, nil)
	write("DELETE FROM iam.operation_grants WHERE tenant_id=$1 AND member_id=$2", tenant.String(), replacement.String())
	verify(key, "app", "task.approve", identity.Principal{}, identity.ErrForbidden)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := check(cancelled, key, "app", "task.approve"); got != (identity.Principal{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authorization: %v %v", got, err)
	}
}

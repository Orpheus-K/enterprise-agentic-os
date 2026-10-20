package feishudocs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type resolveFunc func(context.Context, identity.ExternalIdentity) (identity.Principal, error)

func (f resolveFunc) Resolve(ctx context.Context, actor identity.ExternalIdentity) (identity.Principal, error) {
	return f(ctx, actor)
}

func TestMappedReadRejectsIAMChanges(t *testing.T) {
	offline := errors.New("IAM unavailable")
	for _, mode := range []string{"valid", "revoked_before", "revoked_after", "remapped", "wrong_tenant", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			reader, requests := source(t, "valid")
			tenant, member := uuid.New(), uuid.New()
			calls := 0
			resolver := resolveFunc(func(_ context.Context, actor identity.ExternalIdentity) (identity.Principal, error) {
				calls++
				want := identity.ExternalIdentity{TenantID: tenant, Provider: "feishu", Issuer: "feishu:cli_test:tenant", Subject: "ou_reader"}
				if actor != want {
					t.Fatal("untrusted installation or user mapping")
				}
				if mode == "revoked_before" || mode == "revoked_after" && calls == 2 {
					return identity.Principal{}, identity.ErrUnauthenticated
				}
				if mode == "unavailable" {
					return identity.Principal{}, offline
				}
				p := identity.Principal{TenantID: tenant, MemberID: member}
				if mode == "remapped" && calls == 2 {
					p.MemberID = uuid.New()
				}
				if mode == "wrong_tenant" {
					p.TenantID = uuid.New()
				}
				return p, nil
			})
			mapped, err := NewMappedReader(reader, resolver, tenant, "tenant")
			if err != nil {
				t.Fatal(err)
			}
			snapshot, access, err := mapped.Read(t.Context(), credential(t), "doxcTest")
			if mode == "valid" {
				if err != nil || snapshot.DocumentID != "doxcTest" || access.Principal.MemberID != member || calls != 2 {
					t.Fatal("valid current mapping failed", err)
				}
				return
			}
			if err == nil || !reflect.DeepEqual(snapshot, Snapshot{}) || access != (Access{}) {
				t.Fatal("failed IAM returned content or identity")
			}
			if (mode == "revoked_before" || mode == "wrong_tenant" || mode == "unavailable") && requests.Load() != 0 {
				t.Fatal("denied local identity caused remote request")
			}
			if mode == "unavailable" && !errors.Is(err, offline) {
				t.Fatal("infrastructure failure hidden as denial")
			}
		})
	}
}

func TestMappedAccessBindsInstallationWithoutContent(t *testing.T) {
	reader, calls := source(t, "valid")
	tenant := uuid.New()
	p := identity.Principal{TenantID: tenant, MemberID: uuid.New()}
	resolver := resolveFunc(func(context.Context, identity.ExternalIdentity) (identity.Principal, error) { return p, nil })
	mapped, err := NewMappedReader(reader, resolver, tenant, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	wrong := credential(t)
	wrong.user.TenantKey = "another"
	if _, err := mapped.CheckAccess(t.Context(), wrong, "doxcTest"); !errors.Is(err, ErrDenied) || calls.Load() != 0 {
		t.Fatal("credential crossed tenant installation")
	}
	access, err := mapped.CheckAccess(t.Context(), credential(t), "doxcTest")
	if err != nil || access.Principal != p || calls.Load() != 2 {
		t.Fatal("access probe read content or lost member binding", err)
	}
}

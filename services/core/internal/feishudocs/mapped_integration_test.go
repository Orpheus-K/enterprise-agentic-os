//go:build integration

package feishudocs

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
)

func TestMappedReaderWithPostgresIdentity(t *testing.T) {
	pg := testpg.New(t)
	migrator, err := database.NewMigrator(t.Context(), pg.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = migrator.Up(t.Context())
	if err = errors.Join(err, migrator.Close()); err != nil {
		t.Fatal(err)
	}
	tenant, member, replacement := uuid.New(), uuid.New(), uuid.New()
	binding := feishu.Binding{TenantID: tenant, AppID: "cli_test", TenantKey: "tenant"}
	if _, err := pg.Admin.Exec(t.Context(), "INSERT INTO iam.tenants VALUES($1,'synthetic',true)", tenant); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{member, replacement} {
		if _, err := pg.Admin.Exec(t.Context(), "INSERT INTO iam.members VALUES($1,$2,$3,true)", tenant, id, "synthetic-"+id.String()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pg.Admin.Exec(t.Context(), "INSERT INTO iam.identity_links VALUES($1,'feishu',$2,'ou_reader',$3,true)", tenant, binding.Issuer(), member); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(t.Context(), pg.RuntimeURL, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := identitypg.New(db)
	for _, mode := range []string{"valid", "disabled_member", "revoked_link", "remapped"} {
		t.Run(mode, func(t *testing.T) {
			if _, err := pg.Admin.Exec(t.Context(), "UPDATE iam.members SET enabled=true WHERE tenant_id=$1", tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=true,member_id=$2 WHERE tenant_id=$1", tenant, member); err != nil {
				t.Fatal(err)
			}
			reader, _ := source(t, "valid")
			calls := 0
			resolver := resolveFunc(func(ctx context.Context, actor identity.ExternalIdentity) (identity.Principal, error) {
				calls++
				if calls == 2 {
					var err error
					switch mode {
					case "disabled_member":
						_, err = pg.Admin.Exec(ctx, "UPDATE iam.members SET enabled=false WHERE tenant_id=$1 AND id=$2", tenant, member)
					case "revoked_link":
						_, err = pg.Admin.Exec(ctx, "UPDATE iam.identity_links SET enabled=false WHERE tenant_id=$1", tenant)
					case "remapped":
						_, err = pg.Admin.Exec(ctx, "UPDATE iam.identity_links SET member_id=$2 WHERE tenant_id=$1", tenant, replacement)
					}
					if err != nil {
						return identity.Principal{}, err
					}
				}
				return repo.Resolve(ctx, actor)
			})
			mapped, err := NewMappedReader(reader, resolver, tenant, binding.TenantKey)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, access, err := mapped.Read(t.Context(), credential(t), "doxcTest")
			if mode == "valid" {
				if err != nil || snapshot.DocumentID != "doxcTest" || access.Principal.MemberID != member {
					t.Fatal("native user did not map to channel IAM identity", err)
				}
			} else if err == nil || !reflect.DeepEqual(snapshot, Snapshot{}) || access != (Access{}) {
				t.Fatal("IAM change released stale content")
			}
		})
	}
}

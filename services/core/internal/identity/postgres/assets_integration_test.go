//go:build integration

package postgres

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/skills"
	"errors"
	"strings"
	"testing"
)

func TestAssetGrantsAreScopedRevocableAndReadOnly(t *testing.T) {
	f, ctx := setup(t)
	ref := skills.Ref{Name: "quotation", Revision: strings.Repeat("a", 64)}
	_, err := f.Admin.Exec(ctx, `INSERT INTO iam.asset_grants(tenant_id,member_id,app_id,kind,name,revision) VALUES($1,$2,'app_a','skill',$3,$4)`, f.a.TenantID, f.a.MemberID, ref.Name, ref.Revision)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAssetAuthorizer(f.repo, "app_a")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := auth.Skill(ctx, f.a, ref); err != nil || !ok {
		t.Fatal("valid grant denied", err)
	}
	for _, p := range []identity.Principal{f.b, f.colleague} {
		if ok, err := auth.Skill(ctx, p, ref); err != nil || ok {
			t.Fatal("foreign grant visible", err)
		}
	}
	other, err := NewAssetAuthorizer(f.repo, "app_b")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := other.Skill(ctx, f.a, ref); err != nil || ok {
		t.Fatal("cross installation allowed")
	}
	if ok, err := auth.Tool(ctx, f.a, skills.ToolRef{Name: ref.Name, Revision: ref.Revision}); err != nil || ok {
		t.Fatal("skill granted tool implicitly")
	}
	changed := ref
	changed.Revision = strings.Repeat("b", 64)
	if ok, err := auth.Skill(ctx, f.a, changed); err != nil || ok {
		t.Fatal("new revision inherited grant")
	}
	err = f.repo.Within(ctx, f.a, func(q database.Queries) error {
		_, err := q.Exec(ctx, "UPDATE iam.asset_grants SET enabled=true")
		return err
	})
	requireSQLState(t, err, "42501")
	var count int
	if err = f.db.Transaction(ctx, func(q database.Queries) error {
		return q.QueryRow(ctx, "SELECT count(*) FROM iam.asset_grants").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatal("pooled actor leaked")
	}
	if _, err = f.Admin.Exec(ctx, "UPDATE iam.asset_grants SET enabled=false"); err != nil {
		t.Fatal(err)
	}
	if ok, err := auth.Skill(ctx, f.a, ref); err != nil || ok {
		t.Fatal("revocation ignored")
	}
	if _, err = f.Admin.Exec(ctx, "UPDATE iam.members SET enabled=false WHERE id=$1", f.a.MemberID); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.Skill(ctx, f.a, ref); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("inactive member accepted", err)
	}
}

//go:build integration

package feishudocs

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
)

func TestOAuthLifecyclePostgres(t *testing.T) {
	pg := testpg.New(t)
	m, err := database.NewMigrator(t.Context(), pg.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Up(t.Context())
	if err = errors.Join(err, m.Close()); err != nil {
		t.Fatal(err)
	}
	fixture, tokens, revokes := oauthFlow(t, "valid")
	member := uuid.New()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO iam.tenants VALUES($1,'synthetic',true)`, []any{fixture.tenant}},
		{`INSERT INTO iam.members VALUES($1,$2,'synthetic',true)`, []any{fixture.tenant, member}},
		{`INSERT INTO iam.identity_links VALUES($1,'feishu',$2,$3,$4,true)`, []any{fixture.tenant, "feishu:" + fixture.user.AppID + ":" + fixture.user.TenantKey, fixture.user.OpenID, member}},
	} {
		if _, err := pg.Admin.Exec(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	db, err := database.Open(t.Context(), pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	open := func(keys *OAuthKeys) *OAuth {
		t.Helper()
		o, err := NewOAuth(db, fixture.tenant, fixture.user, "synthetic-app-secret", keys)
		if err != nil {
			t.Fatal(err)
		}
		o.provider = fixture.provider
		o.reader = fixture.reader
		return o
	}
	o := open(fixture.keys)
	if err := o.CheckReady(t.Context()); !errors.Is(err, ErrOAuthMissing) {
		t.Fatal(err)
	}
	if err := o.Complete(t.Context(), oauthBegin(t, o), "code"); err != nil {
		t.Fatal(err)
	}
	if err := o.CheckReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	oldReference := o.Credential()
	// A fresh object reads only persisted state and rotates to the current key.
	o = open(oauthTestKeys(t, "new"))
	now := o.provider.now()
	o.provider.now = func() time.Time { return now.Add(time.Hour) }
	if err := o.reader.CheckAccess(t.Context(), o.Credential(), "doxcTest"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Fatal("incorrect exchange/refresh count")
	}
	var keyID string
	var ciphertext []byte
	if err := pg.Admin.QueryRow(t.Context(), `SELECT key_id,ciphertext FROM channel.oauth_credentials`).Scan(&keyID, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if keyID != "new" || bytes.Contains(ciphertext, []byte("synthetic-secret")) {
		t.Fatal("rotation or encryption failed")
	}
	if err := o.Revoke(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := o.reader.CheckAccess(t.Context(), oldReference, "doxcTest"); !errors.Is(err, ErrOAuthRevoked) {
		t.Fatal("old instance retained access", err)
	}
	if revokes.Load() != 2 {
		t.Fatal("not both tokens revoked")
	}
	if err := open(oauthTestKeys(t, "new")).CheckReady(t.Context()); !errors.Is(err, ErrOAuthRevoked) {
		t.Fatal("revocation not persisted", err)
	}
}

func TestOAuthLedgerEncryptedCASAndAuthorization(t *testing.T) {
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
	user := User{AppID: "cli_test", OpenID: "ou_reader", TenantKey: "tenant_test"}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pg.Admin.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO iam.tenants VALUES($1,'synthetic',true)`, tenant)
	for _, id := range []uuid.UUID{member, replacement} {
		exec(`INSERT INTO iam.members VALUES($1,$2,$3,true)`, tenant, id, id.String())
	}
	exec(`INSERT INTO iam.identity_links VALUES($1,'feishu',$2,$3,$4,true)`, tenant, "feishu:"+user.AppID+":"+user.TenantKey, user.OpenID, member)
	db, err := database.Open(t.Context(), pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newOAuthLedger(db, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	keys := oauthTestKeys(t, "old")
	seal := func(revision int64, value string) oauthEnvelope {
		t.Helper()
		e, err := keys.seal(oauthSecretBinding{Tenant: tenant, User: user, Revision: revision}, []byte(value))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	if _, _, err := ledger.load(t.Context()); !errors.Is(err, ErrOAuthMissing) {
		t.Fatal(err)
	}
	first := seal(1, "access-secret refresh-secret")
	if err := ledger.save(t.Context(), 0, first, "authorize"); err != nil {
		t.Fatal(err)
	}
	if err := ledger.save(t.Context(), 0, first, "authorize"); !errors.Is(err, ErrOAuthConflict) {
		t.Fatal("duplicate initialization", err)
	}
	revision, envelope, err := ledger.load(t.Context())
	if err != nil || revision != 1 || !bytes.Equal(envelope.Ciphertext, first.Ciphertext) {
		t.Fatal("encrypted persistence", err)
	}
	second := seal(2, "rotated-secret")
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- ledger.save(t.Context(), 1, second, "exchange") }()
	}
	success, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrOAuthConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("CAS winners %d conflicts %d", success, conflicts)
	}
	var auditCount int
	if err := pg.Admin.QueryRow(t.Context(), `SELECT count(*) FROM channel.oauth_audit WHERE tenant_id=$1`, tenant).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatal("audit not atomic", err, auditCount)
	}
	// Audit failure must roll back the encrypted state transition.
	exec(`ALTER TABLE channel.oauth_audit ADD CONSTRAINT synthetic_failure CHECK(action<>'refresh')`)
	if err := ledger.save(t.Context(), 2, seal(3, "must-not-commit"), "refresh"); err == nil {
		t.Fatal("audit failure swallowed")
	}
	revision, _, err = ledger.load(t.Context())
	if err != nil || revision != 2 {
		t.Fatal("audit failure committed generation", err)
	}
	// No context means no ciphertext, even for the runtime role.
	if err := db.Transaction(t.Context(), func(q database.Queries) error {
		var count int
		if err := q.QueryRow(t.Context(), `SELECT count(*) FROM channel.oauth_credentials`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Error("RLS exposed credentials")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"member", "link", "tenant", "remap"} {
		t.Run(mode, func(t *testing.T) {
			exec(`UPDATE iam.tenants SET enabled=true WHERE id=$1`, tenant)
			exec(`UPDATE iam.members SET enabled=true WHERE tenant_id=$1`, tenant)
			exec(`UPDATE iam.identity_links SET enabled=true,member_id=$2 WHERE tenant_id=$1`, tenant, member)
			switch mode {
			case "member":
				exec(`UPDATE iam.members SET enabled=false WHERE tenant_id=$1 AND id=$2`, tenant, member)
			case "link":
				exec(`UPDATE iam.identity_links SET enabled=false WHERE tenant_id=$1`, tenant)
			case "tenant":
				exec(`UPDATE iam.tenants SET enabled=false WHERE id=$1`, tenant)
			case "remap":
				exec(`UPDATE iam.identity_links SET member_id=$2 WHERE tenant_id=$1`, tenant, replacement)
			}
			_, e, err := ledger.load(t.Context())
			if err == nil || len(e.Ciphertext) != 0 {
				t.Fatal("revoked/remapped identity obtained ciphertext")
			}
			if err := ledger.save(t.Context(), 2, seal(3, "blocked"), "revoke"); err == nil {
				t.Fatal("revoked/remapped identity changed credentials")
			}
			if mode == "remap" {
				if err := ledger.save(t.Context(), 0, seal(1, "hijack"), "authorize"); !errors.Is(err, ErrOAuthConflict) {
					t.Fatal("remap overwrote prior owner", err)
				}
			} else if !errors.Is(err, identity.ErrUnauthenticated) {
				t.Fatal("unexpected identity failure", err)
			}
		})
	}
}

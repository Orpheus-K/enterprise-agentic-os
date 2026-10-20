//go:build integration

package knowledge

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/testpg"
	"errors"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	pg                        *testpg.Fixture
	db                        *database.DB
	store                     *Store
	owner, reader             identity.ExternalIdentity
	ownerID, readerID, source uuid.UUID
	content                   Content
	acl                       ACL
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pg := testpg.New(t)
	m, err := database.NewMigrator(t.Context(), pg.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Up(t.Context())
	if err = errors.Join(err, m.Close()); err != nil {
		t.Fatal(err)
	}
	f := &fixture{pg: pg, ownerID: uuid.New(), readerID: uuid.New(), source: uuid.New()}
	tenant := uuid.New()
	f.owner = identity.ExternalIdentity{TenantID: tenant, Provider: "synthetic", Issuer: "knowledge-test", Subject: "sync"}
	f.reader = f.owner
	f.reader.Subject = "reader"
	if _, err = pg.Admin.Exec(t.Context(), "INSERT INTO iam.tenants VALUES($1,'synthetic',true)", tenant); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id    uuid.UUID
		actor identity.ExternalIdentity
	}{{f.ownerID, f.owner}, {f.readerID, f.reader}} {
		if _, err = pg.Admin.Exec(t.Context(), "INSERT INTO iam.members VALUES($1,$2,$3,true)", tenant, row.id, row.actor.Subject); err != nil {
			t.Fatal(err)
		}
		if _, err = pg.Admin.Exec(t.Context(), "INSERT INTO iam.identity_links VALUES($1,$2,$3,$4,$5,true)", tenant, row.actor.Provider, row.actor.Issuer, row.actor.Subject, row.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pg.Admin.Exec(t.Context(), "INSERT INTO knowledge.sources VALUES($1,'app',$2,$3,true)", tenant, f.source, f.ownerID); err != nil {
		t.Fatal(err)
	}
	f.db, err = database.Open(t.Context(), pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.db.Close)
	f.store, err = New(f.db, tenant, "app")
	if err != nil {
		t.Fatal(err)
	}
	f.content = Content{SourceID: f.source, DocumentID: "synthetic-doc", Revision: 1, Title: "Synthetic title", Chunks: []Chunk{{ID: "p1", Body: "Synthetic evidence", Anchor: "paragraph:1"}, {ID: "p2", Body: "Restricted evidence", Anchor: "paragraph:2"}}}
	if _, err := f.store.UpdateHealth(t.Context(), f.owner, HealthUpdate{SourceID: f.source, Revision: 1, State: "healthy", ValidUntil: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	f.acl = ACL{SourceEpoch: 1, SourceID: f.source, DocumentID: f.content.DocumentID, Revision: 1, ContentRevision: 1, Allow: []uuid.UUID{f.readerID}, ValidUntil: time.Now().UTC().Add(time.Hour)}
	return f
}
func (f *fixture) ref() Reference {
	return Reference{f.source, f.content.DocumentID, "p1", f.content.Revision, f.acl.Revision}
}
func (f *fixture) publish(t *testing.T) {
	t.Helper()
	if err := f.store.PutContent(t.Context(), f.owner, f.content); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) read(t *testing.T, want bool) {
	t.Helper()
	out, err := f.store.Read(t.Context(), f.reader, f.ref())
	if want {
		if err != nil || out.Body != "Synthetic evidence" || out.Title != "Synthetic title" || out.Anchor != "paragraph:1" {
			t.Fatal(out, err)
		}
	} else if !errors.Is(err, ErrUnavailable) || out.Body != "" || out.Title != "" {
		t.Fatal("unauthorized content", out, err)
	}
}

func TestKnowledgeIndependentVersions(t *testing.T) {
	for _, order := range []string{"content_first", "acl_first"} {
		t.Run(order, func(t *testing.T) {
			f := setup(t)
			if order == "content_first" {
				if err := f.store.PutContent(t.Context(), f.owner, f.content); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
					t.Fatal(err)
				}
			}
			f.read(t, false)
			f.publish(t)
			f.read(t, true)
			old := f.acl
			f.acl.Revision = 2
			f.acl.Allow = nil
			if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
				t.Fatal(err)
			}
			f.read(t, false)
			if err := f.store.PutACL(t.Context(), f.owner, old); !errors.Is(err, ErrConflict) {
				t.Fatal("old ACL restored", err)
			}
			var revision int64
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT content_revision FROM knowledge.documents").Scan(&revision); err != nil || revision != 1 {
				t.Fatal("ACL changed content", revision, err)
			}
			f.acl.Revision = 3
			f.acl.Allow = []uuid.UUID{f.readerID}
			f.acl.ContentRevision = 2
			if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
				t.Fatal(err)
			}
			f.read(t, false)
			f.content.Revision = 2
			if err := f.store.PutContent(t.Context(), f.owner, f.content); err != nil {
				t.Fatal(err)
			}
			f.read(t, true)
			f.content.Revision = 3
			f.content.Deleted = true
			f.content.Title = ""
			f.content.Chunks = nil
			if err := f.store.PutContent(t.Context(), f.owner, f.content); err != nil {
				t.Fatal(err)
			}
			f.read(t, false)
		})
	}
}

func TestKnowledgeAuthorizationAndFreshness(t *testing.T) {
	for _, mode := range []string{"document_deny", "chunk_deny", "chunk_empty_allow", "chunk_not_wider", "expired", "source_disabled", "mapping_revoked", "member_revoked", "other_app", "other_tenant", "stale_reference"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			f.publish(t)
			switch mode {
			case "document_deny":
				f.acl.Deny = []uuid.UUID{f.readerID}
			case "chunk_deny":
				f.acl.Chunks = map[string]ChunkACL{"p1": {Deny: []uuid.UUID{f.readerID}}}
			case "chunk_empty_allow":
				f.acl.Chunks = map[string]ChunkACL{"p1": {Allow: []uuid.UUID{}}}
			case "chunk_not_wider":
				f.acl.Allow = nil
				f.acl.Chunks = map[string]ChunkACL{"p1": {Allow: []uuid.UUID{f.readerID}}}
			case "expired":
				f.acl.ValidUntil = time.Now().UTC().Add(-time.Hour)
			case "source_disabled":
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.sources SET enabled=false"); err != nil {
					t.Fatal(err)
				}
			case "mapping_revoked":
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.readerID); err != nil {
					t.Fatal(err)
				}
			case "member_revoked":
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.members SET enabled=false WHERE id=$1", f.readerID); err != nil {
					t.Fatal(err)
				}
			case "other_app":
				var err error
				f.store, err = New(f.db, f.reader.TenantID, "other-app")
				if err != nil {
					t.Fatal(err)
				}
			case "other_tenant":
				f.reader.TenantID = uuid.New()
			case "stale_reference":
				f.acl.Revision = 2
			}
			if mode == "document_deny" || mode == "chunk_deny" || mode == "chunk_empty_allow" || mode == "chunk_not_wider" || mode == "expired" {
				f.acl.Revision++
				if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.store.Read(t.Context(), f.reader, f.ref())
			if err == nil || out.Title != "" || out.Body != "" {
				t.Fatal("leaked evidence", out, err)
			}
		})
	}
}

func TestKnowledgeWriteIsolationIdempotencyAndRollback(t *testing.T) {
	f := setup(t)
	if err := f.store.PutContent(t.Context(), f.reader, f.content); !errors.Is(err, identitypg.ErrForbidden) {
		t.Fatal("reader wrote", err)
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if err := f.store.PutContent(t.Context(), f.owner, f.content); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f.publish(t)
	var events int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM knowledge.audit WHERE action='content'").Scan(&events); err != nil || events != 1 {
		t.Fatal(events, err)
	}
	changed := f.content
	changed.Title = "changed"
	if err := f.store.PutContent(t.Context(), f.owner, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("same version conflict ignored", err)
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit ADD CONSTRAINT synthetic_failure CHECK(action<>'content') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	changed.Revision = 2
	if err := f.store.PutContent(t.Context(), f.owner, changed); err == nil {
		t.Fatal("audit failure swallowed")
	}
	f.read(t, true)
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit ADD CONSTRAINT synthetic_read_failure CHECK(action<>'read') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	out, err := f.store.Read(t.Context(), f.reader, f.ref())
	if err == nil || out.Body != "" {
		t.Fatal("read released without audit")
	}
}

func TestKnowledgeRLSFiltersBeforeApplication(t *testing.T) {
	f := setup(t)
	f.acl.Allow = nil
	f.publish(t)
	err := f.store.within(t.Context(), f.reader, func(q database.Queries) error {
		var docs, chunks int
		if err := q.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM knowledge.documents),(SELECT count(*) FROM knowledge.chunks)").Scan(&docs, &chunks); err != nil {
			return err
		}
		if docs != 0 || chunks != 0 {
			t.Fatal("RLS leaked counts", docs, chunks)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeTenantIsolationAndRestart(t *testing.T) {
	f := setup(t)
	f.publish(t)
	tenant, member := uuid.New(), uuid.New()
	if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO iam.tenants VALUES($1,'other',true)", tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO iam.members VALUES($1,$2,'other-reader',true)", tenant, member); err != nil {
		t.Fatal(err)
	}
	other := f.reader
	other.TenantID = tenant
	if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO iam.identity_links VALUES($1,$2,$3,$4,$5,true)", tenant, other.Provider, other.Issuer, other.Subject, member); err != nil {
		t.Fatal(err)
	}
	foreign, err := New(f.db, tenant, "app")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := foreign.Read(t.Context(), other, f.ref()); !errors.Is(err, ErrUnavailable) {
			t.Fatal("foreign tenant evidence", err)
		}
		f.read(t, true)
	}
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f.store, err = New(db, f.reader.TenantID, "app")
	if err != nil {
		t.Fatal(err)
	}
	f.read(t, true)
	var protected int
	if err := f.pg.Admin.QueryRow(t.Context(), `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='knowledge' AND c.relkind='r' AND c.relrowsecurity AND c.relforcerowsecurity`).Scan(&protected); err != nil || protected != 11 {
		t.Fatal("knowledge RLS missing", protected, err)
	}
}

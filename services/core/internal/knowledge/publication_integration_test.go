//go:build integration

package knowledge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPublicationAtomicAndIdempotent(t *testing.T) {
	f := setup(t)
	space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
	p := Publication{Content: f.content, ACL: f.acl, Vectors: f.vectors(space)}
	for i := 0; i < 2; i++ {
		if err := f.store.Publish(t.Context(), f.owner, p); err != nil {
			t.Fatal(err)
		}
	}
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action IN ('content','vectors','acl')", 3)
	if got := f.search(t, f.reader, space, 10); len(got) != 2 {
		t.Fatal("published document not searchable")
	}
	for _, mode := range []string{"vectors", "acl_conflict", "expired", "epoch", "audit"} {
		t.Run(mode, func(t *testing.T) {
			next := p
			next.Content.Revision = 2
			next.Content.Title = "replacement"
			next.Vectors.ContentRevision = 2
			next.ACL.ContentRevision = 2
			next.ACL.Revision = 2
			switch mode {
			case "vectors":
				next.Vectors.Vectors = []ChunkVector{{ChunkID: "missing", Embedding: []float32{1, 0, 0}}}
			case "acl_conflict":
				next.ACL.Revision = 1
			case "expired":
				next.ACL.ValidUntil = time.Now().Add(-time.Minute)
			case "epoch":
				next.ACL.SourceEpoch++
			case "audit":
				// Fail the last channel's audit after content and vectors changed.
				if _, err := f.pg.Admin.Exec(t.Context(), `CREATE FUNCTION knowledge.reject_publication_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='acl' THEN RAISE EXCEPTION 'synthetic audit fault'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_publication_audit BEFORE INSERT ON knowledge.audit FOR EACH ROW EXECUTE FUNCTION knowledge.reject_publication_audit()`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := f.pg.Admin.Exec(context.Background(), "DROP TRIGGER reject_publication_audit ON knowledge.audit; DROP FUNCTION knowledge.reject_publication_audit()"); err != nil {
						t.Error(err)
					}
				})
			}
			if err := f.store.Publish(t.Context(), f.owner, next); err == nil {
				t.Fatal("incomplete publication succeeded")
			}
			evidence, err := f.store.Read(t.Context(), f.reader, f.ref())
			if err != nil || evidence.Title != f.content.Title {
				t.Fatal("failed publication replaced old content", err)
			}
			if got := f.search(t, f.reader, space, 10); len(got) != 2 || got[0].Reference.ContentRevision != 1 {
				t.Fatal("failed publication changed old vectors or ACL")
			}
			f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action IN ('content','vectors','acl')", 3)
		})
	}
	invalid := p
	invalid.ACL.SourceID = uuid.New()
	if err := f.store.Publish(t.Context(), f.owner, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal("mixed-source publication accepted")
	}
	p.Content.Revision = 2
	p.Content.Title = "replacement"
	p.ACL.Revision = 2
	p.ACL.ContentRevision = 2
	p.Vectors.ContentRevision = 2
	if err := f.store.Publish(t.Context(), f.owner, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Read(t.Context(), f.reader, f.ref()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("old reference survived successful replacement")
	}
	if got := f.search(t, f.reader, space, 10); len(got) != 2 || got[0].Reference.ContentRevision != 2 || got[0].Reference.ACLRevision != 2 {
		t.Fatal("replacement did not publish all channels")
	}
}

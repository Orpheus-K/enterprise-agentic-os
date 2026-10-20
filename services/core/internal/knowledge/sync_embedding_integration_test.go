//go:build integration

package knowledge

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSyncEmbeddingUnknownCannotReplay(t *testing.T) {
	f := setup(t)
	hash := strings.Repeat("a", 64)
	space := f.addSpace(t, f.owner.TenantID, "app", uuid.New(), true)
	if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 1, hash); err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.BeginSyncEmbedding(t.Context(), f.owner, lease, f.content, space); err != nil {
		t.Fatal(err)
	}
	if err := f.store.BeginSyncEmbedding(t.Context(), f.owner, lease, f.content, space); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate begin authorized replay")
	}
	p := Publication{Content: f.content, ACL: f.acl, Vectors: f.vectors(space)}
	if err := f.store.PublishSync(t.Context(), f.owner, lease, p); !errors.Is(err, ErrConflict) {
		t.Fatal("pending external call published")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.sync_jobs SET lease_until=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unknown batch automatically recovered")
	}
	if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 2, hash); !errors.Is(err, ErrConflict) {
		t.Fatal("new scheduling request erased unknown operation")
	}
	if err := f.store.SaveSyncEmbedding(t.Context(), f.owner, lease, p.Vectors); !errors.Is(err, ErrConflict) {
		t.Fatal("late result accepted expired worker")
	}
	f.count(t, "SELECT count(*) FROM knowledge.documents", 0)
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='sync.embedding_intent'", 1)
}

func TestSyncEmbeddingCheckpointRecovery(t *testing.T) {
	f := setup(t)
	hash := strings.Repeat("b", 64)
	space := f.addSpace(t, f.owner.TenantID, "app", uuid.New(), true)
	if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 1, hash); err != nil {
		t.Fatal(err)
	}
	first, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.BeginSyncEmbedding(t.Context(), f.owner, first, f.content, space); err != nil {
		t.Fatal(err)
	}
	vectors := f.vectors(space)
	bad := vectors
	bad.Vectors = vectors.Vectors[:1]
	if err := f.store.SaveSyncEmbedding(t.Context(), f.owner, first, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("partial batch checkpoint accepted")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), `CREATE FUNCTION knowledge.reject_embedding_save() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='sync.embedding_saved' THEN RAISE EXCEPTION 'synthetic'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_embedding_save BEFORE INSERT ON knowledge.audit FOR EACH ROW EXECUTE FUNCTION knowledge.reject_embedding_save()`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveSyncEmbedding(t.Context(), f.owner, first, vectors); err == nil {
		t.Fatal("checkpoint audit failure ignored")
	}
	f.count(t, "SELECT count(*) FROM knowledge.sync_jobs WHERE embedding_pending AND embedding_vectors IS NULL", 1)
	if _, err := f.pg.Admin.Exec(t.Context(), "DROP TRIGGER reject_embedding_save ON knowledge.audit; DROP FUNCTION knowledge.reject_embedding_save()"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := f.store.SaveSyncEmbedding(t.Context(), f.owner, first, vectors); err != nil {
			t.Fatal(err)
		}
	}
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='sync.embedding_saved'", 1)
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.sync_jobs SET lease_until=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	second, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := f.store.LoadSyncEmbedding(t.Context(), f.owner, second)
	if err != nil || !reflect.DeepEqual(checkpoint.Content, f.content) || !reflect.DeepEqual(checkpoint.Vectors, vectors) {
		t.Fatal("saved result lost across takeover", err)
	}
	if err := f.store.BeginSyncEmbedding(t.Context(), f.owner, second, f.content, space); !errors.Is(err, ErrConflict) {
		t.Fatal("recovery repeated a completed embedding batch")
	}
	changed := f.content
	changed.Title = "tampered"
	if err := f.store.PublishSync(t.Context(), f.owner, second, Publication{Content: changed, ACL: f.acl, Vectors: vectors}); !errors.Is(err, ErrConflict) {
		t.Fatal("checkpoint did not bind publication content")
	}
	if err := f.store.PublishSync(t.Context(), f.owner, second, Publication{Content: checkpoint.Content, ACL: f.acl, Vectors: checkpoint.Vectors}); err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, f.reader, space, 10); len(got) != 2 {
		t.Fatal("recovered checkpoint not published")
	}
}

//go:build integration

package knowledge

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"errors"
	"github.com/google/uuid"
	"math"
	"sync"
	"testing"
	"time"
)

// Synthetic three-dimensional vectors check retrieval semantics only, not embedding quality.
func (f *fixture) addSpace(t *testing.T, tenant uuid.UUID, app string, id uuid.UUID, enabled bool) Space {
	t.Helper()
	if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO knowledge.embedding_spaces VALUES($1,$2,$3,'synthetic-embedding','v1',3,$4)", tenant, app, id, enabled); err != nil {
		t.Fatal(err)
	}
	return Space{ID: id, Model: "synthetic-embedding", ModelRevision: "v1"}
}
func (f *fixture) vectors(space Space) VectorSet {
	return VectorSet{Space: space, SourceID: f.source, DocumentID: f.content.DocumentID, ContentRevision: f.content.Revision, Vectors: []ChunkVector{
		{ChunkID: "p1", Embedding: []float32{0.9, 0.1, 0}}, {ChunkID: "p2", Embedding: []float32{1, 0, 0}},
	}}
}
func (f *fixture) search(t *testing.T, actor identity.ExternalIdentity, space Space, limit int) []Candidate {
	t.Helper()
	out, err := f.store.Search(t.Context(), actor, SearchQuery{Space: space, Embedding: []float32{1, 0, 0}, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func chunks(c []Candidate) []string {
	out := []string{}
	for _, x := range c {
		out = append(out, x.Reference.DocumentID+"/"+x.Reference.ChunkID)
	}
	return out
}
func (f *fixture) count(t *testing.T, query string, want int) {
	t.Helper()
	var got int
	if err := f.pg.Admin.QueryRow(t.Context(), query).Scan(&got); err != nil || got != want {
		t.Fatalf("%s: got %d want %d (%v)", query, got, want, err)
	}
}

func TestVectorSearchAuthorizesBeforeRanking(t *testing.T) {
	f := setup(t)
	space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
	f.acl.Chunks = map[string]ChunkACL{"p2": {Deny: []uuid.UUID{f.readerID}}}
	f.publish(t)
	if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
		t.Fatal(err)
	}
	// A denied document holds an exact match for the query; it must neither appear nor displace p1.
	denied := Content{SourceID: f.source, DocumentID: "denied-doc", Revision: 1, Title: "Denied title", Chunks: []Chunk{{ID: "c1", Body: "Denied body", Anchor: "paragraph:1"}}}
	if err := f.store.PutContent(t.Context(), f.owner, denied); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutACL(t.Context(), f.owner, ACL{SourceEpoch: 1, SourceID: f.source, DocumentID: "denied-doc", Revision: 1, ContentRevision: 1, Allow: []uuid.UUID{f.ownerID}, ValidUntil: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PublishVectors(t.Context(), f.owner, VectorSet{Space: space, SourceID: f.source, DocumentID: "denied-doc", ContentRevision: 1, Vectors: []ChunkVector{{ChunkID: "c1", Embedding: []float32{1, 0, 0}}}}); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, maxCandidates} {
		got := f.search(t, f.reader, space, limit)
		if len(got) != 1 || got[0].Reference != f.ref() || got[0].Distance <= 0 || got[0].Distance >= 0.01 {
			t.Fatalf("limit %d: %+v", limit, got)
		}
		if _, err := f.store.Read(t.Context(), f.reader, got[0].Reference); err != nil {
			t.Fatal("candidate not readable", err)
		}
	}
	// The sync identity sees only documents its own ACL grants, not everything it synchronizes.
	if got := chunks(f.search(t, f.owner, space, maxCandidates)); len(got) != 1 || got[0] != "denied-doc/c1" {
		t.Fatal("sync identity bypassed member ACL", got)
	}
	err := f.store.within(t.Context(), f.reader, func(q database.Queries) error {
		var vectors, sets int
		if err := q.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM knowledge.chunk_vectors),(SELECT count(*) FROM knowledge.vector_sets)").Scan(&vectors, &sets); err != nil {
			return err
		}
		if vectors != 1 || sets != 1 {
			t.Fatal("RLS leaked vector rows", vectors, sets)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.count(t, "SELECT count(*) FROM knowledge.search_audit WHERE returned=1", 3)
}

func TestVectorPublishRejectsInconsistentSets(t *testing.T) {
	f := setup(t)
	space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
	disabled := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), false)
	f.publish(t)
	for name, c := range map[string]struct {
		actor  identity.ExternalIdentity
		change func(*VectorSet)
		want   error
	}{
		"reader":         {f.reader, func(*VectorSet) {}, identitypg.ErrForbidden},
		"partial":        {f.owner, func(v *VectorSet) { v.Vectors = v.Vectors[:1] }, ErrInvalid},
		"unknown_chunk":  {f.owner, func(v *VectorSet) { v.Vectors[1].ChunkID = "p3" }, ErrInvalid},
		"duplicate":      {f.owner, func(v *VectorSet) { v.Vectors[1].ChunkID = "p1" }, ErrInvalid},
		"wrong_dims":     {f.owner, func(v *VectorSet) { v.Vectors[0].Embedding = []float32{1, 0}; v.Vectors[1].Embedding = []float32{0, 1} }, ErrInvalid},
		"mixed_dims":     {f.owner, func(v *VectorSet) { v.Vectors[0].Embedding = []float32{1, 0} }, ErrInvalid},
		"nan":            {f.owner, func(v *VectorSet) { v.Vectors[0].Embedding[0] = float32(math.NaN()) }, ErrInvalid},
		"infinite":       {f.owner, func(v *VectorSet) { v.Vectors[0].Embedding[0] = float32(math.Inf(1)) }, ErrInvalid},
		"zero":           {f.owner, func(v *VectorSet) { v.Vectors[0].Embedding = []float32{0, 0, 0} }, ErrInvalid},
		"other_model":    {f.owner, func(v *VectorSet) { v.Model = "other-embedding" }, ErrConflict},
		"other_revision": {f.owner, func(v *VectorSet) { v.ModelRevision = "v2" }, ErrConflict},
		"future_content": {f.owner, func(v *VectorSet) { v.ContentRevision = 2 }, ErrConflict},
		"missing_doc":    {f.owner, func(v *VectorSet) { v.DocumentID = "missing" }, ErrConflict},
		"unknown_space":  {f.owner, func(v *VectorSet) { v.ID = uuid.New() }, ErrUnavailable},
		"disabled_space": {f.owner, func(v *VectorSet) { v.Space = disabled }, ErrUnavailable},
	} {
		in := f.vectors(space)
		c.change(&in)
		if err := f.store.PublishVectors(t.Context(), c.actor, in); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", name, err, c.want)
		}
	}
	f.count(t, "SELECT count(*) FROM knowledge.chunk_vectors", 0)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='vectors'", 1)
	changed := f.vectors(space)
	changed.Vectors[0].Embedding = []float32{0, 1, 0}
	if err := f.store.PublishVectors(t.Context(), f.owner, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("same revision rewritten", err)
	}
	for name, c := range map[string]struct {
		query SearchQuery
		want  error
	}{
		"wrong_dims":   {SearchQuery{Space: space, Embedding: []float32{1, 0}, Limit: 1}, ErrInvalid},
		"zero":         {SearchQuery{Space: space, Embedding: []float32{0, 0, 0}, Limit: 1}, ErrInvalid},
		"no_limit":     {SearchQuery{Space: space, Embedding: []float32{1, 0, 0}}, ErrInvalid},
		"over_limit":   {SearchQuery{Space: space, Embedding: []float32{1, 0, 0}, Limit: maxCandidates + 1}, ErrInvalid},
		"other_model":  {SearchQuery{Space: Space{ID: space.ID, Model: "other-embedding", ModelRevision: "v1"}, Embedding: []float32{1, 0, 0}, Limit: 1}, ErrConflict},
		"disabled":     {SearchQuery{Space: disabled, Embedding: []float32{1, 0, 0}, Limit: 1}, ErrUnavailable},
		"foreign_user": {SearchQuery{Space: space, Embedding: []float32{1, 0, 0}, Limit: 1}, identity.ErrUnauthenticated},
	} {
		actor := f.reader
		if name == "foreign_user" {
			actor.TenantID = uuid.New()
		}
		out, err := f.store.Search(t.Context(), actor, c.query)
		if !errors.Is(err, c.want) || out != nil {
			t.Errorf("%s: got %v %v want %v", name, out, err, c.want)
		}
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.embedding_spaces SET model_revision='v2' WHERE id=$1", space.ID); err == nil {
		t.Fatal("embedding space reinterpreted in place")
	}
	// Audit failures roll back the whole vector set and withhold search results.
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit ADD CONSTRAINT synthetic_vectors CHECK(action<>'vectors') NOT VALID; ALTER TABLE knowledge.search_audit ADD CONSTRAINT synthetic_search CHECK(false) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.embedding_spaces SET enabled=true WHERE id=$1", disabled.ID); err != nil {
		t.Fatal(err)
	}
	in := f.vectors(disabled)
	if err := f.store.PublishVectors(t.Context(), f.owner, in); err == nil {
		t.Fatal("vector audit failure swallowed")
	}
	f.count(t, "SELECT count(*) FROM knowledge.chunk_vectors", 2)
	f.count(t, "SELECT count(*) FROM knowledge.vector_sets", 1)
	if out, err := f.store.Search(t.Context(), f.reader, SearchQuery{Space: space, Embedding: []float32{1, 0, 0}, Limit: 1}); err == nil || out != nil {
		t.Fatal("search released without audit", out)
	}
}

func TestVectorCandidatesFollowCurrentAuthorization(t *testing.T) {
	f := setup(t)
	space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
	f.publish(t)
	if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
		t.Fatal(err)
	}
	first := f.search(t, f.reader, space, 5)
	if got := chunks(first); len(got) != 2 || got[0] != "synthetic-doc/p2" || got[1] != "synthetic-doc/p1" {
		t.Fatal("exact cosine order", got)
	}
	expectEmpty := func(step string) {
		t.Helper()
		if got := f.search(t, f.reader, space, 5); len(got) != 0 {
			t.Fatal(step, "leaked candidates", got)
		}
		for _, c := range first {
			if out, err := f.store.Read(t.Context(), f.reader, c.Reference); !errors.Is(err, ErrUnavailable) || out.Body != "" {
				t.Fatal(step, "old candidate readable", err)
			}
		}
	}
	f.acl.Revision = 2
	f.acl.Allow = nil
	if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
		t.Fatal(err)
	}
	expectEmpty("revoked")
	f.acl.Revision = 3
	f.acl.Allow = []uuid.UUID{f.readerID}
	if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
		t.Fatal(err)
	}
	// Restored access yields new references; the pre-revocation ones stay unusable.
	if got := f.search(t, f.reader, space, 5); len(got) != 2 || got[0].Reference.ACLRevision != 3 {
		t.Fatal("restored ACL", got)
	}
	if _, err := f.store.Read(t.Context(), f.reader, first[0].Reference); !errors.Is(err, ErrUnavailable) {
		t.Fatal("stale candidate reference", err)
	}
	if _, err := f.store.UpdateHealth(t.Context(), f.owner, HealthUpdate{SourceID: f.source, Revision: 2, State: "unavailable", ValidUntil: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, f.reader, space, 5); len(got) != 0 {
		t.Fatal("unhealthy source searched", got)
	}
	health, err := f.store.UpdateHealth(t.Context(), f.owner, HealthUpdate{SourceID: f.source, Revision: 3, State: "healthy", ValidUntil: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, f.reader, space, 5); len(got) != 0 {
		t.Fatal("recovery reused pre-outage ACL", got)
	}
	f.acl.Revision = 4
	f.acl.SourceEpoch = health.Epoch
	if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, f.reader, space, 5); len(got) != 2 {
		t.Fatal("fresh generation ACL", got)
	}
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='vectors'", 1)
	// A new content revision drops vectors in the same transaction; old vectors cannot be republished.
	f.content.Revision = 2
	f.content.Chunks[0].Body = "Updated evidence"
	if err := f.store.PutContent(t.Context(), f.owner, f.content); err != nil {
		t.Fatal(err)
	}
	f.count(t, "SELECT count(*) FROM knowledge.chunk_vectors", 0)
	f.acl.Revision = 5
	f.acl.ContentRevision = 2
	if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, f.reader, space, 5); len(got) != 0 {
		t.Fatal("content without vectors searched", got)
	}
	stale := f.vectors(space)
	stale.ContentRevision = 1
	if err := f.store.PublishVectors(t.Context(), f.owner, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale vectors accepted", err)
	}
	if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
		t.Fatal(err)
	}
	current := f.search(t, f.reader, space, 5)
	if len(current) != 2 || current[0].Reference.ContentRevision != 2 || current[0].Reference.ACLRevision != 5 {
		t.Fatal("current revision", current)
	}
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if f.store, err = New(db, f.reader.TenantID, "app"); err != nil {
		t.Fatal(err)
	}
	if got := f.search(t, f.reader, space, 5); len(got) != 2 || got[0].Reference != current[0].Reference {
		t.Fatal("restart changed candidates", got)
	}
	f.content = Content{SourceID: f.source, DocumentID: f.content.DocumentID, Revision: 3, Deleted: true}
	if err := f.store.PutContent(t.Context(), f.owner, f.content); err != nil {
		t.Fatal(err)
	}
	f.count(t, "SELECT count(*) FROM knowledge.chunk_vectors", 0)
	if got := f.search(t, f.reader, space, 5); len(got) != 0 {
		t.Fatal("deleted document searched", got)
	}
}

func TestVectorSearchIsolation(t *testing.T) {
	for _, mode := range []string{"other_app", "other_tenant", "space_disabled", "source_disabled", "mapping_revoked", "acl_expired", "chunk_empty_allow"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
			f.publish(t)
			if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
				t.Fatal(err)
			}
			store, actor, want := f.store, f.reader, error(nil)
			switch mode {
			case "other_app":
				f.addSpace(t, f.reader.TenantID, "other-app", space.ID, true)
				var err error
				if store, err = New(f.db, f.reader.TenantID, "other-app"); err != nil {
					t.Fatal(err)
				}
			case "other_tenant":
				tenant, member := uuid.New(), uuid.New()
				actor.TenantID = tenant
				if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO iam.tenants VALUES($1,'other',true)", tenant); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO iam.members VALUES($1,$2,'reader',true)", tenant, member); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO iam.identity_links VALUES($1,'synthetic','knowledge-test','reader',$2,true)", tenant, member); err != nil {
					t.Fatal(err)
				}
				f.addSpace(t, tenant, "app", space.ID, true)
				var err error
				if store, err = New(f.db, tenant, "app"); err != nil {
					t.Fatal(err)
				}
			case "space_disabled":
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.embedding_spaces SET enabled=false"); err != nil {
					t.Fatal(err)
				}
				want = ErrUnavailable
			case "source_disabled":
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.sources SET enabled=false"); err != nil {
					t.Fatal(err)
				}
			case "mapping_revoked":
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.readerID); err != nil {
					t.Fatal(err)
				}
				want = identity.ErrUnauthenticated
			case "acl_expired", "chunk_empty_allow":
				f.acl.Revision++
				if mode == "acl_expired" {
					f.acl.ValidUntil = time.Now().UTC().Add(-time.Second)
				} else {
					f.acl.Chunks = map[string]ChunkACL{"p1": {Allow: []uuid.UUID{}}, "p2": {Allow: []uuid.UUID{}}}
				}
				if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
					t.Fatal(err)
				}
			}
			out, err := store.Search(t.Context(), actor, SearchQuery{Space: space, Embedding: []float32{1, 0, 0}, Limit: maxCandidates})
			if want != nil {
				if !errors.Is(err, want) || out != nil {
					t.Fatal("got", out, err, "want", want)
				}
				return
			}
			if err != nil || len(out) != 0 {
				t.Fatal("isolation leaked", out, err)
			}
		})
	}
}

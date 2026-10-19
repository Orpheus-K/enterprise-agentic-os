//go:build integration

package operations

import (
	"agentos.local/core/internal/knowledge"
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"net/http/httptest"
	"testing"
	"time"
)

func TestKnowledgeControlledHTTP(t *testing.T) {
	f := setup(t)
	store, err := knowledge.New(f.db, f.tenant, "app_a")
	if err != nil {
		t.Fatal(err)
	}
	f.api = f.api.WithKnowledge(store)
	source := uuid.New()
	if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO knowledge.sources VALUES($1,'app_a',$2,$3,true)", f.tenant, source, f.member); err != nil {
		t.Fatal(err)
	}
	request := func(path string, in any, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/ops/knowledge/"+path, bytes.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer "+opsToken)
		}
		w := httptest.NewRecorder()
		f.api.ServeHTTP(w, r)
		return w
	}
	health := knowledge.HealthUpdate{SourceID: source, Revision: 1, State: "healthy", ValidUntil: time.Now().UTC().Add(time.Hour)}
	if w := request("health", health, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := request("health", health, true)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var state knowledge.SourceHealth
	if json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Epoch != 1 {
		t.Fatal("invalid health response")
	}
	content := knowledge.Content{SourceID: source, DocumentID: "synthetic", Revision: 1, Title: "Synthetic title", Chunks: []knowledge.Chunk{{ID: "p1", Body: "Synthetic body", Anchor: "paragraph:1"}}}
	if w := request("content", content, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	ref := knowledge.Reference{SourceID: source, DocumentID: "synthetic", ChunkID: "p1", ContentRevision: 1, ACLRevision: 1}
	if w := request("read", ref, true); w.Code != 404 {
		t.Fatal("content before ACL visible", w.Code)
	}
	acl := knowledge.ACL{SourceID: source, SourceEpoch: 1, DocumentID: "synthetic", Revision: 1, ContentRevision: 1, Allow: []uuid.UUID{f.member}, ValidUntil: health.ValidUntil}
	if w := request("acl", acl, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = request("read", ref, true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !bytes.Contains(w.Body.Bytes(), []byte("Synthetic body")) {
		t.Fatal(w.Code)
	}
	space := knowledge.Space{ID: uuid.New(), Model: "synthetic-embedding", ModelRevision: "v1"}
	if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO knowledge.embedding_spaces VALUES($1,'app_a',$2,$3,$4,3,true)", f.tenant, space.ID, space.Model, space.ModelRevision); err != nil {
		t.Fatal(err)
	}
	vectors := knowledge.VectorSet{Space: space, SourceID: source, DocumentID: "synthetic", ContentRevision: 1, Vectors: []knowledge.ChunkVector{{ChunkID: "p1", Embedding: []float32{1, 0, 0}}}}
	if w := request("vectors", vectors, true); w.Code != 200 {
		t.Fatal("publish vectors", w.Code)
	}
	query := knowledge.SearchQuery{Space: space, Embedding: []float32{1, 0, 0}, Limit: 5}
	w = request("search", query, true)
	var found struct {
		Candidates []knowledge.Candidate `json:"candidates"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &found) != nil || len(found.Candidates) != 1 || found.Candidates[0].Reference != ref || bytes.Contains(w.Body.Bytes(), []byte("Synthetic body")) {
		t.Fatal("search response carries text or wrong candidates", w.Code, w.Body.String())
	}
	for name, c := range map[string]struct {
		path string
		in   any
		code int
	}{
		"wrong_dims":   {"search", knowledge.SearchQuery{Space: space, Embedding: []float32{1, 0}, Limit: 5}, 400},
		"wrong_model":  {"search", knowledge.SearchQuery{Space: knowledge.Space{ID: space.ID, Model: "other", ModelRevision: "v1"}, Embedding: []float32{1, 0, 0}, Limit: 5}, 409},
		"no_space":     {"search", knowledge.SearchQuery{Space: knowledge.Space{ID: uuid.New(), Model: "synthetic-embedding", ModelRevision: "v1"}, Embedding: []float32{1, 0, 0}, Limit: 5}, 404},
		"stale_vector": {"vectors", knowledge.VectorSet{Space: space, SourceID: source, DocumentID: "synthetic", ContentRevision: 2, Vectors: vectors.Vectors}, 409},
	} {
		if w := request(c.path, c.in, true); w.Code != c.code {
			t.Errorf("%s: %d want %d", name, w.Code, c.code)
		}
	}
	if w := request("search", map[string]any{"space_id": space.ID, "model": space.Model, "model_revision": space.ModelRevision, "embedding": []float32{1, 0, 0}, "limit": 5, "member_id": f.member}, true); w.Code != 400 {
		t.Fatal("caller identity accepted in search", w.Code)
	}
	if w := request("health", map[string]any{"source_id": source, "tenant_id": f.tenant}, true); w.Code != 400 {
		t.Fatal("caller identity accepted", w.Code)
	}
	content.Title = "changed"
	for _, raw := range []string{`{} {}`, `{"source_id":"invalid"}`, `{"unknown":true}`} {
		r := httptest.NewRequest("POST", "/api/ops/knowledge/health", bytes.NewBufferString(raw))
		r.Header.Set("Authorization", "Bearer "+opsToken)
		w := httptest.NewRecorder()
		f.api.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("invalid body accepted", w.Code)
		}
	}
	foreign := health
	foreign.SourceID = uuid.New()
	if w := request("health", foreign, true); w.Code != 403 {
		t.Fatal("unbound source accepted", w.Code)
	}
	if w := request("content", content, true); w.Code != 409 {
		t.Fatal("content conflict", w.Code)
	}
	health.Revision = 2
	health.State = "unavailable"
	if w := request("health", health, true); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request("read", ref, true); w.Code != 404 || bytes.Contains(w.Body.Bytes(), []byte("Synthetic body")) {
		t.Fatal("unhealthy read", w.Code)
	}
	if w := request("search", query, true); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"candidates":[]`)) {
		t.Fatal("unhealthy search", w.Code, w.Body.String())
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.member); err != nil {
		t.Fatal(err)
	}
	if w := request("health", health, true); w.Code != 401 {
		t.Fatal("revoked identity", w.Code)
	}
}

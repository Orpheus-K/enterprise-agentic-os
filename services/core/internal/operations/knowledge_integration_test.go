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
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.member); err != nil {
		t.Fatal(err)
	}
	if w := request("health", health, true); w.Code != 401 {
		t.Fatal("revoked identity", w.Code)
	}
}

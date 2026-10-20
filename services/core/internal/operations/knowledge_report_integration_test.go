//go:build integration

package operations

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

func TestKnowledgeReportHTTP(t *testing.T) {
	f := setup(t)
	store, err := knowledge.New(f.db, f.tenant, "app_a")
	if err != nil {
		t.Fatal(err)
	}
	f.api = f.api.WithKnowledge(store)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	window := knowledge.AuditWindow{From: from, To: from.Add(time.Hour)}
	post := func(path string, input any, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/ops/knowledge/"+path, bytes.NewReader(raw))
		if auth {
			r.Header.Set("Authorization", "Bearer "+opsToken)
		}
		w := httptest.NewRecorder()
		f.api.ServeHTTP(w, r)
		return w
	}
	if w := post("stats", window, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := post("stats", window, true); w.Code != 403 {
		t.Fatal("task audit grant authorized knowledge stats", w.Code)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.pg.Admin.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO iam.operation_grants VALUES($1,$2,'app_a','knowledge.audit.read',true)`, f.tenant, f.member)
	exec(`INSERT INTO knowledge.audit VALUES($1,$2,'app_a',$3,'synthetic-document',$4,'read',1,$5)`, uuid.New(), f.tenant, uuid.New(), f.member, from)
	w := post("stats", window, true)
	var stats knowledge.UsageStats
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &stats) != nil || stats.ReadChecks != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("statistics response", w.Code, w.Body.String())
	}
	w = post("audit", knowledge.AuditQuery{AuditWindow: window, Kind: "events", Limit: 10}, true)
	var page knowledge.AuditPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Events) != 1 {
		t.Fatal("audit response", w.Code)
	}
	for _, bad := range []any{map[string]any{"from": window.From, "to": window.To, "member_id": f.member}, map[string]any{"from": window.From, "to": window.From}, map[string]any{"from": "invalid", "to": window.To}} {
		if w := post("stats", bad, true); w.Code != 400 {
			t.Fatal("bad report accepted", w.Code)
		}
	}
	exec(`ALTER TABLE knowledge.audit_reads ADD CONSTRAINT synthetic_report_failure CHECK(false) NOT VALID`)
	if w := post("stats", window, true); w.Code != 503 || w.Body.Len() != 0 {
		t.Fatal("partial report on audit failure", w.Code)
	}
	exec(`ALTER TABLE knowledge.audit_reads DROP CONSTRAINT synthetic_report_failure`)
	exec(`UPDATE iam.operation_grants SET enabled=false WHERE capability='knowledge.audit.read'`)
	if w := post("audit", knowledge.AuditQuery{AuditWindow: window, Kind: "events", Limit: 10}, true); w.Code != 403 || w.Body.Len() != 0 {
		t.Fatal("revoked report access", w.Code)
	}
	exec(`UPDATE iam.identity_links SET enabled=false WHERE member_id=$1`, f.member)
	if w := post("stats", window, true); w.Code != 401 {
		t.Fatal("revoked identity", w.Code)
	}
}

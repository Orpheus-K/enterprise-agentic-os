package operations

import (
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/telemetry"
	"context"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerOnlyAuthentication(t *testing.T) {
	tel, err := telemetry.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	api, err := New(strings.Repeat("s", 32), identity.ExternalIdentity{TenantID: uuid.New(), Provider: "operator", Issuer: "agentos:ops:app", Subject: "auditor"}, nil, nil, tel)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "Basic secret", "Bearer wrong"} {
		r := httptest.NewRequest("GET", "/api/ops/audit?token="+strings.Repeat("s", 32), nil)
		r.Header.Set("Authorization", header)
		r.Header.Set("X-Member-ID", "admin")
		r.Header.Set("Cookie", "session=admin")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("untrusted credential accepted")
		}
	}
	r := httptest.NewRequest("GET", "/api/ops/status", nil)
	r.Header.Add("Authorization", "Bearer "+strings.Repeat("s", 32))
	r.Header.Add("Authorization", "Bearer "+strings.Repeat("s", 32))
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("ambiguous authorization accepted")
	}
}
func TestAuditQueryBounds(t *testing.T) {
	id := uuid.NewString()
	for _, query := range []string{"", "task_id=invalid", "task_id=" + id + "&tenant_id=other", "task_id=" + id + "&limit=101", "task_id=" + id + "&limit=0", "task_id=" + id + "&after=-1", "task_id=" + id + "&limit=1&limit=2"} {
		if _, err := parseAudit(httptest.NewRequest("GET", "/api/ops/audit?"+query, nil)); err == nil {
			t.Fatal("invalid query accepted: " + query)
		}
	}
	if _, err := parseAudit(httptest.NewRequest("GET", "/api/ops/audit?task_id="+id+"&limit=2&after=1", nil)); err != nil {
		t.Fatal(err)
	}
}

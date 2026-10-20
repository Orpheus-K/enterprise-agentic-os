package operations

import (
	"agentos.local/core/internal/conversation"
	conversationsqlite "agentos.local/core/internal/conversation/sqlite"
	"agentos.local/core/internal/health"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/sqlitedb"
	"agentos.local/core/internal/telemetry"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var _ Store = (*conversationsqlite.Inbox)(nil)

func TestSQLiteOperationsHTTP(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "ops.db")
	if err := sqlitedb.Migrate(ctx, path); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenant, member := uuid.New(), uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO iam_tenants(id,name) VALUES(?,'ops')", tenant.String())
	exec("INSERT INTO iam_members(tenant_id,id,employee_id) VALUES(?,?,'employee')", tenant.String(), member.String())
	exec("INSERT INTO iam_identity_links(tenant_id,provider,issuer,subject,member_id) VALUES(?,'operator','agentos:ops:app','operator',?)", tenant.String(), member.String())
	exec("INSERT INTO iam_identity_links(tenant_id,provider,issuer,subject,member_id) VALUES(?,'feishu','issuer','worker',?)", tenant.String(), member.String())
	for _, cap := range []string{"audit.read", "status.read", "task.approve", "task.reconcile"} {
		exec("INSERT INTO iam_operation_grants(tenant_id,member_id,app_id,capability) VALUES(?,?,'app',?)", tenant.String(), member.String(), cap)
	}
	store := conversationsqlite.NewInbox(db, tenant, "app", "revision")
	if err := store.Enqueue(ctx, conversation.Message{EventID: "e", MessageID: "m", Text: "secret prompt", Fingerprint: "h", Identity: identity.ExternalIdentity{TenantID: tenant, Provider: "feishu", Issuer: "issuer", Subject: "worker"}}); err != nil {
		t.Fatal(err)
	}
	var task string
	if err := db.QueryRowContext(ctx, "SELECT id FROM channel_tasks").Scan(&task); err != nil {
		t.Fatal(err)
	}
	tel, err := telemetry.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close(context.Background())
	token := strings.Repeat("x", 32)
	api, err := New(token, identity.ExternalIdentity{TenantID: tenant, Provider: "operator", Issuer: "agentos:ops:app", Subject: "operator"}, store, health.NewReadiness(db.PingContext), tel)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, auth string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret prompt") || strings.Contains(w.Body.String(), token) {
			t.Fatal("sensitive data in response")
		}
		return w
	}
	window := "?from=" + time.Now().UTC().Add(-time.Hour).Format(time.RFC3339) + "&to=" + time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	request("GET", "/api/ops/tasks"+window, "", "", 401)
	page := request("GET", "/api/ops/tasks"+window, "", token, 200)
	var tasks conversation.TaskPage
	if json.Unmarshal(page.Body.Bytes(), &tasks) != nil || len(tasks.Tasks) != 1 || tasks.Tasks[0].TaskID.String() != task {
		t.Fatal("task missing")
	}
	stats := request("GET", "/api/ops/stats"+window, "", token, 200)
	var counts conversation.TaskStats
	if json.Unmarshal(stats.Body.Bytes(), &counts) != nil || counts.Tasks != 1 {
		t.Fatal("wrong stats")
	}
	request("GET", "/api/ops/audit?task_id="+task, "", token, 200)
	request("GET", "/api/ops/recovery?task_id="+task, "", token, 200)
	request("GET", "/api/ops/approval?task_id="+task, "", token, 404)
	request("GET", "/api/ops/status", "", token, 200)
	request("GET", "/api/ops/metrics", "", token, 200)
	for _, route := range []string{"quotation", "notice", "knowledge/read"} {
		method := "GET"
		if route == "knowledge/read" {
			method = "POST"
		}
		request(method, "/api/ops/"+route, "", token, 404)
	}
	request("POST", "/api/ops/recovery", `{"task_id":"`+task+`","request_id":"`+uuid.NewString()+`","expected_version":1,"decision":"unresolved","evidence_id":"`+uuid.NewString()+`"}`, token, 409)
	exec("UPDATE iam_operation_grants SET enabled=0 WHERE capability IN ('audit.read','status.read')")
	request("GET", "/api/ops/tasks"+window, "", token, 403)
	request("GET", "/api/ops/status", "", token, 403)
	exec("UPDATE iam_operation_grants SET enabled=1")
	exec("CREATE TRIGGER fail_read BEFORE INSERT ON channel_audit_reads BEGIN SELECT RAISE(ABORT,'private database error'); END")
	failed := request("GET", "/api/ops/stats"+window, "", token, 503)
	if failed.Body.Len() != 0 {
		t.Fatal("failed read returned data")
	}
	exec("UPDATE iam_identity_links SET enabled=0 WHERE provider='operator'")
	request("GET", "/api/ops/tasks"+window, "", token, 401)
}

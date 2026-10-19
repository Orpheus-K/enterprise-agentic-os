//go:build integration

package operations

import (
	"agentos.local/core/internal/conversation"
	"encoding/json"
	"github.com/google/uuid"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func reportPath(path string, from, to time.Time, extra string) string {
	p := path + "?from=" + url.QueryEscape(from.Format(time.RFC3339)) + "&to=" + url.QueryEscape(to.Format(time.RFC3339))
	if extra != "" {
		p += "&" + extra
	}
	return p
}

func TestTaskListAndStatsStayInScope(t *testing.T) {
	f := setup(t)
	ctx := t.Context()
	extra := []uuid.UUID{uuid.New(), uuid.New()}
	for i, id := range extra {
		if _, err := f.pg.Admin.Exec(ctx, `INSERT INTO channel.tasks(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,state,created_at) VALUES($1,$2,$3,'app_a','private-issuer','private-subject',$4,$4,'fingerprint','revision','secret-prompt','failed',now()+$5::int*interval '1 second')`, f.tenant, id, f.member, id.String(), i+1); err != nil {
			t.Fatal(err)
		}
	}
	// Approvals on the in-scope task and on another tenant's task; only the first may be counted.
	if _, err := f.pg.Admin.Exec(ctx, `INSERT INTO channel.approvals(tenant_id,task_id,id,member_id,revision,content_hash,waiting_version,expires_at,status)
  SELECT t.tenant_id,t.id,gen_random_uuid(),t.member_id,'r','h',1,now()+interval '1 hour',CASE WHEN t.id=$1 THEN 'pending' ELSE 'approved' END
  FROM channel.tasks t WHERE t.id IN ($1,$2,$3)`, f.task, f.otherTenant, f.otherApp); err != nil {
		t.Fatal(err)
	}
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	first := request(f, reportPath("/api/ops/tasks", from, to, "limit=2"))
	var page conversation.TaskPage
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Tasks) != 2 || page.NextAfter == nil || page.Tasks[0].TaskID != f.task {
		t.Fatalf("first page %d %s", first.Code, first.Body.String())
	}
	for _, secret := range []string{"secret-prompt", "secret-output", "private-subject", "private-issuer", f.member.String(), f.otherApp.String(), f.otherTenant.String()} {
		if strings.Contains(first.Body.String(), secret) {
			t.Fatal("task list leaked", secret)
		}
	}
	second := request(f, reportPath("/api/ops/tasks", from, to, "limit=2&after="+page.NextAfter.String()))
	page = conversation.TaskPage{}
	if json.Unmarshal(second.Body.Bytes(), &page) != nil || len(page.Tasks) != 1 || page.Tasks[0].TaskID != extra[1] || page.NextAfter != nil {
		t.Fatal("unstable task paging", second.Body.String())
	}
	// Foreign cursors cannot probe other apps or tenants.
	for _, cursor := range []uuid.UUID{f.otherApp, f.otherTenant, uuid.New()} {
		page = conversation.TaskPage{}
		w := request(f, reportPath("/api/ops/tasks", from, to, "after="+cursor.String()))
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Tasks) != 0 {
			t.Fatal("foreign cursor exposed tasks", cursor, w.Body.String())
		}
	}

	var stats conversation.TaskStats
	w := request(f, reportPath("/api/ops/stats", from, to, ""))
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &stats) != nil {
		t.Fatalf("stats %d %s", w.Code, w.Body.String())
	}
	if stats.Tasks != 3 || stats.ByState["succeeded"] != 0 || stats.ByState["pending"] != 1 || stats.ByState["failed"] != 2 || len(stats.Approvals) != 1 || stats.Approvals["pending"] != 1 {
		t.Fatalf("stats out of scope: %+v", stats)
	}
	if w := request(f, reportPath("/api/ops/stats", to, to.Add(time.Hour), "")); w.Code != 200 || !strings.Contains(w.Body.String(), `"tasks":0`) {
		t.Fatal("empty window", w.Body.String())
	}

	var reads, scoped int
	if err := f.pg.Admin.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE task_id IS NULL AND member_id=$1) FROM channel.audit_reads WHERE scope IN ('tasks','stats')", f.member).Scan(&reads, &scoped); err != nil || reads != 7 || scoped != 7 {
		t.Fatalf("report reads=%d scoped=%d err=%v", reads, scoped, err)
	}
	if _, err := f.pg.Admin.Exec(ctx, "INSERT INTO channel.audit_reads(id,tenant_id,member_id,app_id,task_id,row_count,trace_id,scope) VALUES($1,$2,$3,'app_a',NULL,0,'','task')", uuid.New(), f.tenant, f.member); err == nil {
		t.Fatal("single-task read stored without task id")
	}
}

func TestReportInputsAndFailClosed(t *testing.T) {
	f := setup(t)
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for name, path := range map[string]string{
		"missing_to":     "/api/ops/stats?from=" + url.QueryEscape(from.Format(time.RFC3339)),
		"not_rfc3339":    "/api/ops/stats?from=2026-10-01&to=2026-10-02",
		"reversed":       reportPath("/api/ops/stats", to, from, ""),
		"empty":          reportPath("/api/ops/stats", from, from, ""),
		"too_wide":       reportPath("/api/ops/tasks", from, from.Add(conversation.MaxReportWindow+time.Second), ""),
		"stats_paging":   reportPath("/api/ops/stats", from, to, "limit=5"),
		"unknown":        reportPath("/api/ops/tasks", from, to, "member_id="+f.member.String()),
		"limit":          reportPath("/api/ops/tasks", from, to, "limit=101"),
		"nil_cursor":     reportPath("/api/ops/tasks", from, to, "after="+uuid.Nil.String()),
		"repeated_param": reportPath("/api/ops/tasks", from, to, "limit=1&limit=2"),
	} {
		if w := request(f, path); w.Code != 400 || w.Body.Len() != 0 {
			t.Errorf("%s: status=%d", name, w.Code)
		}
	}
	r := httptest.NewRequest("POST", reportPath("/api/ops/stats", from, to, ""), nil)
	r.Header.Set("Authorization", "Bearer "+opsToken)
	rec := httptest.NewRecorder()
	f.api.ServeHTTP(rec, r)
	if rec.Code != 405 {
		t.Fatal("report accepted POST", rec.Code)
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit_reads ADD CONSTRAINT reject_report_reads CHECK(scope='task') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/ops/tasks", "/api/ops/stats"} {
		if w := request(f, reportPath(path, from, to, "")); w.Code != 503 || w.Body.Len() != 0 {
			t.Fatal(path, "returned data despite failed read audit", w.Code)
		}
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.operation_grants SET enabled=false WHERE capability='audit.read'"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/ops/tasks", "/api/ops/stats"} {
		if w := request(f, reportPath(path, from, to, "")); w.Code != 403 || w.Body.Len() != 0 {
			t.Fatal(path, "revoked auditor read report", w.Code)
		}
	}
}

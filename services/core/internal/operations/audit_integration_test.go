//go:build integration

package operations

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/health"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/telemetry"
	"agentos.local/core/internal/testpg"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

const opsToken = "test-operator-token-at-least-32-bytes"

type fixture struct {
	pg                                          *testpg.Fixture
	db                                          *database.DB
	api                                         *API
	store                                       *conversation.Store
	tenant, member, task, otherApp, otherTenant uuid.UUID
	external                                    identity.ExternalIdentity
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	pg := testpg.New(t)
	m, err := database.NewMigrator(ctx, pg.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, upErr := m.Up(ctx)
	if err := errors.Join(upErr, m.Close()); err != nil {
		t.Fatal(err)
	}
	f := &fixture{pg: pg, tenant: uuid.New(), member: uuid.New(), task: uuid.New(), otherApp: uuid.New(), otherTenant: uuid.New()}
	tenantB, memberB := uuid.New(), uuid.New()
	for _, p := range []identity.Principal{{TenantID: f.tenant, MemberID: f.member}, {TenantID: tenantB, MemberID: memberB}} {
		if _, err := pg.Admin.Exec(ctx, "INSERT INTO iam.tenants(id,name) VALUES($1,'test')", p.TenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.Admin.Exec(ctx, "INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'operator')", p.TenantID, p.MemberID); err != nil {
			t.Fatal(err)
		}
	}
	f.external = identity.ExternalIdentity{TenantID: f.tenant, Provider: "operator", Issuer: "agentos:ops:app_a", Subject: "auditor"}
	if _, err := pg.Admin.Exec(ctx, "INSERT INTO iam.identity_links VALUES($1,$2,$3,$4,$5,true)", f.tenant, f.external.Provider, f.external.Issuer, f.external.Subject, f.member); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Admin.Exec(ctx, "INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'app_a','audit.read'),($1,$2,'app_a','status.read')", f.tenant, f.member); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		tenant, member, task uuid.UUID
		app                  string
	}{{f.tenant, f.member, f.task, "app_a"}, {f.tenant, f.member, f.otherApp, "app_b"}, {tenantB, memberB, f.otherTenant, "app_a"}} {
		if _, err := pg.Admin.Exec(ctx, `INSERT INTO channel.tasks(tenant_id,id,member_id,app_id,issuer,subject,event_id,message_id,fingerprint,model_revision,prompt,response) VALUES($1,$2,$3,$4,'private-issuer','private-subject',$5,$5,'fingerprint','revision','secret-prompt','secret-output')`, row.tenant, row.task, row.member, row.app, row.task.String()); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"pending", "model_running", "succeeded"} {
			if _, err := pg.Admin.Exec(ctx, "INSERT INTO channel.audit(tenant_id,task_id,state,reason) VALUES($1,$2,$3,'')", row.tenant, row.task, state); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.db, err = database.Open(ctx, pg.RuntimeURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.db.Close)
	f.store = conversation.NewStore(f.db, f.tenant, "app_a", "revision")
	tel, err := telemetry.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ready := health.NewReadiness(f.store.Ready)
	ready.SetTransport(health.Connected)
	ready.SetWorker(true)
	f.api, err = New(opsToken, f.external, f.store, ready, tel)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func request(f *fixture, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+opsToken)
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}

func TestAuthorizedAuditPaginationAndIsolation(t *testing.T) {
	f := setup(t)
	path := "/api/ops/audit?task_id=" + f.task.String()
	first := request(f, path+"&limit=2")
	if first.Code != 200 {
		t.Fatalf("status=%d", first.Code)
	}
	var page conversation.AuditPage
	if json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Entries) != 2 || page.NextAfter != 2 {
		t.Fatalf("invalid page %s", first.Body.String())
	}
	for _, secret := range []string{"secret-prompt", "secret-output", "private-subject", "private-issuer", opsToken, f.member.String()} {
		if strings.Contains(first.Body.String(), secret) {
			t.Fatal("audit leaked unapproved field")
		}
	}
	second := request(f, path+"&limit=2&after=2")
	page = conversation.AuditPage{}
	if json.Unmarshal(second.Body.Bytes(), &page) != nil || len(page.Entries) != 1 || page.Entries[0].ID != 3 || page.NextAfter != 0 {
		t.Fatal("unstable pagination")
	}
	for _, id := range []uuid.UUID{f.otherApp, f.otherTenant, uuid.New()} {
		page = conversation.AuditPage{}
		w := request(f, "/api/ops/audit?task_id="+id.String())
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Entries) != 0 {
			t.Fatal("cross-scope record or count exposed")
		}
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit_reads WHERE tenant_id=$1 AND member_id=$2 AND trace_id<>''", f.tenant, f.member).Scan(&count); err != nil || count != 5 {
		t.Fatalf("query audit count=%d err=%v", count, err)
	}
	// Same pooled connection without an actor cannot expose grants or read-history.
	if err := f.db.Transaction(t.Context(), func(q database.Queries) error {
		return q.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM iam.operation_grants)+(SELECT count(*) FROM channel.audit_reads)").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatal("pooled operator identity leaked")
	}
}

func TestOperatorRevocationAndScope(t *testing.T) {
	for _, revocation := range []string{"grant", "member", "mapping", "tenant"} {
		t.Run(revocation, func(t *testing.T) {
			f := setup(t)
			path := "/api/ops/audit?task_id=" + f.task.String()
			if request(f, path).Code != 200 {
				t.Fatal("initial access rejected")
			}
			query := ""
			expected := 401
			switch revocation {
			case "grant":
				query = "UPDATE iam.operation_grants SET enabled=false WHERE capability='audit.read'"
				expected = 403
			case "member":
				query = "UPDATE iam.members SET enabled=false"
			case "mapping":
				query = "UPDATE iam.identity_links SET enabled=false"
			case "tenant":
				query = "UPDATE iam.tenants SET enabled=false"
			}
			if _, err := f.pg.Admin.Exec(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if w := request(f, path); w.Code != expected || w.Body.Len() != 0 {
				t.Fatalf("revoked status=%d", w.Code)
			}
		})
	}
	f := setup(t)
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.operation_grants SET app_id='other_app'"); err != nil {
		t.Fatal(err)
	}
	if request(f, "/api/ops/audit?task_id="+f.task.String()).Code != 403 {
		t.Fatal("other app grant authorized query")
	}
	// Untrusted identity values cannot replace the configured operator.
	r := httptest.NewRequest("GET", "/api/ops/audit?task_id="+f.task.String(), nil)
	r.Header.Set("Authorization", "Bearer "+opsToken)
	r.Header.Set("X-Tenant-ID", f.tenant.String())
	r.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("header elevated permissions")
	}
}

func TestStatusMetricsAndReadAuditFailClosed(t *testing.T) {
	f := setup(t)
	// The current schema must be ready; a stale version check would keep every release at 503.
	if s := f.api.readiness.Snapshot(t.Context()); !s.Ready || s.Storage != "available" {
		t.Fatalf("current schema not ready: %+v", s)
	}
	for _, path := range []string{"/api/ops/status", "/api/ops/metrics"} {
		if w := request(f, path); w.Code != 200 {
			t.Fatalf("%s status=%d", path, w.Code)
		}
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.operation_grants SET enabled=false WHERE capability='status.read'"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/ops/status", "/api/ops/metrics"} {
		if request(f, path).Code != 403 {
			t.Fatal("missing status capability accepted")
		}
	}
	if request(f, "/api/ops/audit?task_id="+f.task.String()).Code != 200 {
		t.Fatal("status revocation affected independent audit grant")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit_reads ADD CONSTRAINT reject_reads CHECK(row_count<0) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if w := request(f, "/api/ops/audit?task_id="+f.task.String()); w.Code != 503 || w.Body.Len() != 0 {
		t.Fatal("returned data despite failed read audit")
	}
	// A tenant that is disabled remains alive but cannot be marked channel-ready.
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.tenants SET enabled=false WHERE id=$1", f.tenant); err != nil {
		t.Fatal(err)
	}
	if f.api.readiness.Snapshot(t.Context()).Ready {
		t.Fatal("disabled installation ready")
	}
}

func TestRuntimeCannotGrantItselfAuditAccess(t *testing.T) {
	f := setup(t)
	repo := identitypg.New(f.db)
	p, err := repo.Resolve(t.Context(), f.external)
	if err != nil {
		t.Fatal(err)
	}
	err = repo.Within(t.Context(), p, func(q database.Queries) error {
		_, err := q.Exec(t.Context(), "UPDATE iam.operation_grants SET enabled=true")
		return err
	})
	if err == nil {
		t.Fatal("runtime can grant privileges")
	}
	other := f.external
	other.TenantID = uuid.New()
	if _, err := f.store.ReadAudit(t.Context(), other, f.task, 0, 10); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("cross-tenant operator accepted")
	}
}

package contracttest

import (
	"context"
	"errors"
	"time"

	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type ReportStore interface {
	Leases
	ListTasks(context.Context, identity.ExternalIdentity, conversation.ReportWindow, uuid.UUID, int) (conversation.TaskPage, error)
	Stats(context.Context, identity.ExternalIdentity, conversation.ReportWindow) (conversation.TaskStats, error)
}

type ReportFixture struct {
	New          func(uuid.UUID, string, string) ReportStore
	Exec         func(string, ...any) error
	Receipts     func(uuid.UUID, uuid.UUID) (count, returned, misattributed int, err error)
	SeedApproval func(uuid.UUID, uuid.UUID, uuid.UUID) error
	FailReads    func() error
}

func RunReports(t *testing.T, f ReportFixture) {
	t.Helper()
	ctx := t.Context()
	tenant, other, worker, auditor := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	write := func(query string, args ...any) {
		t.Helper()
		if err := f.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uuid.UUID{tenant, other} {
		write("INSERT INTO iam.tenants(id,name) VALUES($1,'audit')", id.String())
		for _, member := range []uuid.UUID{worker, auditor} {
			write("INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,$3)", id.String(), member.String(), member.String())
			write("INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,'feishu','issuer',$2,$3)", id.String(), member.String(), member.String())
		}
	}
	for _, app := range []string{"app", "other-app"} {
		write("INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'audit.read')", tenant.String(), auditor.String(), app)
	}
	enqueue := func(id uuid.UUID, app string) ReportStore {
		t.Helper()
		store := f.New(id, app, "revision")
		err := store.Enqueue(ctx, conversation.Message{EventID: "event", MessageID: "message", Text: "secret prompt", Fingerprint: "hash",
			Identity: identity.ExternalIdentity{TenantID: id, Provider: "feishu", Issuer: "issuer", Subject: worker.String()}})
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	store := enqueue(tenant, "app")
	otherApp := enqueue(tenant, "other-app")
	otherTenant := enqueue(other, "app")
	claim := func(store ReportStore) *conversation.Task {
		t.Helper()
		task, err := store.Claim(ctx)
		if err != nil || task == nil {
			t.Fatalf("claim: %v", err)
		}
		return task
	}
	task := claim(store)
	otherAppTask, otherTenantTask := claim(otherApp), claim(otherTenant)

	reader := f.New(tenant, "app", "new-revision")
	actor := identity.ExternalIdentity{TenantID: tenant, Provider: "feishu", Issuer: "issuer", Subject: auditor.String()}
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	// Stable timestamps exercise both half-open boundaries and tie-breaking.
	for _, taskID := range []uuid.UUID{task.ID, otherAppTask.ID, otherTenantTask.ID} {
		write("UPDATE channel.tasks SET created_at=$1 WHERE id=$2", now.Format("2006-01-02T15:04:05.000Z"), taskID.String())
	}
	w := conversation.ReportWindow{From: now, To: now.Add(time.Hour)}
	page, err := reader.ListTasks(ctx, actor, w, uuid.Nil, 1)
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].TaskID != task.ID || page.NextAfter != nil {
		t.Fatalf("scoped page: %v %v", page, err)
	}
	if !page.Tasks[0].CreatedAt.Equal(now) || page.Tasks[0].UpdatedAt.IsZero() || page.Tasks[0].SkillName != nil {
		t.Fatalf("metadata: %v", page.Tasks[0])
	}
	stats, err := reader.Stats(ctx, actor, w)
	if err != nil || stats.Tasks != 1 || stats.ByState["claimed"] != 1 || len(stats.Approvals) != 0 {
		t.Fatalf("stats: %v %v", stats, err)
	}
	for _, cursor := range []uuid.UUID{task.ID, otherAppTask.ID, otherTenantTask.ID, uuid.New()} {
		page, err = reader.ListTasks(ctx, actor, w, cursor, 1)
		if err != nil || len(page.Tasks) != 0 || page.NextAfter != nil {
			t.Fatalf("cursor leaked: %v %v", page, err)
		}
	}
	for _, window := range []conversation.ReportWindow{{From: now.Add(time.Microsecond), To: w.To}, {From: now.Add(-time.Hour), To: now}} {
		page, err = reader.ListTasks(ctx, actor, window, uuid.Nil, 10)
		if err != nil || len(page.Tasks) != 0 {
			t.Fatalf("boundary: %v %v", page, err)
		}
		stats, err = reader.Stats(ctx, actor, window)
		if err != nil || stats.Tasks != 0 {
			t.Fatalf("stats boundary: %v %v", stats, err)
		}
	}
	count, total, wrong, err := f.Receipts(tenant, auditor)
	if err != nil || count != 10 || total != 2 || wrong != 0 {
		t.Fatalf("receipts %d %d %d %v", count, total, wrong, err)
	}

	// A second task at the same timestamp proves UUID tie-breaking and pagination.
	if err := store.Enqueue(ctx, conversation.Message{EventID: "second", MessageID: "second", Text: "secret", Fingerprint: "hash", Identity: identity.ExternalIdentity{TenantID: tenant, Provider: "feishu", Issuer: "issuer", Subject: worker.String()}}); err != nil {
		t.Fatal(err)
	}
	second := claim(store)
	write("UPDATE channel.tasks SET created_at=$1 WHERE id=$2", now.Format("2006-01-02T15:04:05.000Z"), second.ID.String())
	firstPage, err := reader.ListTasks(ctx, actor, w, uuid.Nil, 1)
	if err != nil || len(firstPage.Tasks) != 1 || firstPage.NextAfter == nil {
		t.Fatalf("first page: %v %v", firstPage, err)
	}
	lastPage, err := reader.ListTasks(ctx, actor, w, *firstPage.NextAfter, 1)
	if err != nil || len(lastPage.Tasks) != 1 || lastPage.NextAfter != nil || firstPage.Tasks[0].TaskID.String() >= lastPage.Tasks[0].TaskID.String() {
		t.Fatalf("last page: %v %v", lastPage, err)
	}
	for _, pair := range []struct{ tenant, task uuid.UUID }{{tenant, task.ID}, {tenant, otherAppTask.ID}, {other, otherTenantTask.ID}} {
		if err := f.SeedApproval(pair.tenant, pair.task, worker); err != nil {
			t.Fatal(err)
		}
	}
	stats, err = reader.Stats(ctx, actor, w)
	if err != nil || stats.Tasks != 2 || stats.ByState["claimed"] != 2 || stats.Approvals["pending"] != 1 {
		t.Fatalf("approval scope: %v %v", stats, err)
	}
	for _, limit := range []int{0, 101} {
		if _, err := reader.ListTasks(ctx, actor, w, uuid.Nil, limit); !errors.Is(err, conversation.ErrInvalidReport) {
			t.Fatalf("limit: %v", err)
		}
	}
	for _, window := range []conversation.ReportWindow{{}, {From: now, To: now}, {From: now, To: now.Add(conversation.MaxReportWindow + time.Second)}} {
		if _, err := reader.Stats(ctx, actor, window); !errors.Is(err, conversation.ErrInvalidReport) {
			t.Fatalf("invalid window: %v", err)
		}
	}
	write("UPDATE iam.operation_grants SET enabled=$1 WHERE tenant_id=$2 AND member_id=$3", false, tenant.String(), auditor.String())
	if page, err := reader.ListTasks(ctx, actor, w, uuid.Nil, 10); !errors.Is(err, identity.ErrForbidden) || page.Tasks != nil {
		t.Fatalf("revoked page: %v %v", page, err)
	}
	if stats, err := reader.Stats(ctx, actor, w); !errors.Is(err, identity.ErrForbidden) || stats.ByState != nil {
		t.Fatalf("revoked stats: %v %v", stats, err)
	}
	write("UPDATE iam.operation_grants SET enabled=$1 WHERE tenant_id=$2 AND member_id=$3", true, tenant.String(), auditor.String())
	if err := f.FailReads(); err != nil {
		t.Fatal(err)
	}
	if page, err := reader.ListTasks(ctx, actor, w, uuid.Nil, 10); err == nil || page.Tasks != nil {
		t.Fatalf("unrecorded page: %v %v", page, err)
	}
	if stats, err := reader.Stats(ctx, actor, w); err == nil || stats.ByState != nil {
		t.Fatalf("unrecorded stats: %v %v", stats, err)
	}
}

package contracttest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type AuditStore interface {
	Leases
	ReadAudit(context.Context, identity.ExternalIdentity, uuid.UUID, int64, int) (conversation.AuditPage, error)
}

type AuditFixture struct {
	New       func(uuid.UUID, string, string) AuditStore
	Exec      func(string, ...any) error
	Receipts  func(uuid.UUID, uuid.UUID) (count, returned, misattributed int, err error)
	FailReads func() error
}

func RunAudit(t *testing.T, f AuditFixture) {
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
	enqueue := func(id uuid.UUID, app string) AuditStore {
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
	claim := func(store AuditStore) *conversation.Task {
		t.Helper()
		task, err := store.Claim(ctx)
		if err != nil || task == nil {
			t.Fatalf("claim: %v", err)
		}
		return task
	}
	task := claim(store)
	otherAppTask, otherTenantTask := claim(otherApp), claim(otherTenant)
	for _, step := range []struct {
		state conversation.State
		text  string
	}{{conversation.ModelRunning, ""}, {conversation.DeliveryPending, "secret response"}, {conversation.DeliveryRunning, ""}, {conversation.Succeeded, ""}} {
		if err := store.Transition(ctx, task, step.state, "fixture", step.text); err != nil {
			t.Fatal(err)
		}
	}
	// Audit history spans execution revisions, unlike execution/approval actions.
	reader := f.New(tenant, "app", "new-revision")
	actor := identity.ExternalIdentity{TenantID: tenant, Provider: "feishu", Issuer: "issuer", Subject: auditor.String()}
	count, returned := 0, 0
	receipts := func() {
		t.Helper()
		got, total, wrong, err := f.Receipts(tenant, auditor)
		if err != nil || got != count || total != returned || wrong != 0 {
			t.Fatalf("receipts=%d rows=%d wrong_actor=%d err=%v; want %d/%d", got, total, wrong, err, count, returned)
		}
	}
	read := func(id uuid.UUID, after int64, limit int) conversation.AuditPage {
		t.Helper()
		page, err := reader.ReadAudit(ctx, actor, id, after, limit)
		if err != nil || page.TaskID != id || page.Entries == nil {
			t.Fatalf("audit page: %v %v", page, err)
		}
		count++
		returned += len(page.Entries)
		receipts()
		encoded, err := json.Marshal(page)
		if err != nil || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), auditor.String()) || strings.Contains(string(encoded), worker.String()) {
			t.Fatalf("audit exposed non-audit data: %v", err)
		}
		return page
	}
	for index, state := range []string{"pending", "model_running", "delivery_running"} {
		page := read(task.ID, int64(index*2), 2)
		next := int64((index + 1) * 2)
		if index == 2 {
			next = 0
		}
		if len(page.Entries) != 2 || page.Entries[0].ID != int64(index*2+1) || page.Entries[0].State != state || page.NextAfter != next {
			t.Fatalf("task-local pagination: %v", page)
		}
		for _, entry := range page.Entries {
			if entry.RecordedAt.IsZero() || entry.OperationID != nil {
				t.Fatalf("invalid base-task audit metadata: %v", entry)
			}
		}
	}
	for _, id := range []uuid.UUID{uuid.New(), otherAppTask.ID, otherTenantTask.ID} {
		page := read(id, 0, 100)
		if len(page.Entries) != 0 || page.NextAfter != 0 {
			t.Fatalf("out-of-scope audit: %v", page)
		}
	}
	if page := read(task.ID, 6, 100); len(page.Entries) != 0 || page.NextAfter != 0 {
		t.Fatalf("end cursor: %v", page)
	}
	for _, input := range []struct {
		id    uuid.UUID
		after int64
		limit int
	}{{uuid.Nil, 0, 1}, {task.ID, -1, 1}, {task.ID, 0, 0}, {task.ID, 0, 101}} {
		if page, err := reader.ReadAudit(ctx, actor, input.id, input.after, input.limit); err == nil || len(page.Entries) != 0 {
			t.Fatalf("accepted invalid query: %v %v", page, err)
		}
	}
	receipts()
	write("UPDATE iam.operation_grants SET enabled=$1 WHERE tenant_id=$2", false, tenant.String())
	if page, err := reader.ReadAudit(ctx, actor, task.ID, 0, 100); !errors.Is(err, identity.ErrForbidden) || len(page.Entries) != 0 {
		t.Fatalf("revoked audit read: %v %v", page, err)
	}
	write("UPDATE iam.operation_grants SET enabled=$1 WHERE tenant_id=$2", true, tenant.String())
	write("UPDATE iam.identity_links SET enabled=$1 WHERE tenant_id=$2 AND subject=$3", false, tenant.String(), actor.Subject)
	if page, err := reader.ReadAudit(ctx, actor, task.ID, 0, 100); !errors.Is(err, identity.ErrUnauthenticated) || len(page.Entries) != 0 {
		t.Fatalf("revoked identity read: %v %v", page, err)
	}
	write("UPDATE iam.identity_links SET enabled=$1 WHERE tenant_id=$2 AND subject=$3", true, tenant.String(), actor.Subject)
	write("UPDATE iam.identity_links SET member_id=$1 WHERE tenant_id=$2 AND subject=$3", worker.String(), tenant.String(), actor.Subject)
	if page, err := reader.ReadAudit(ctx, actor, task.ID, 0, 100); !errors.Is(err, identity.ErrForbidden) || len(page.Entries) != 0 {
		t.Fatalf("remapped identity inherited audit capability: %v %v", page, err)
	}
	write("UPDATE iam.identity_links SET member_id=$1 WHERE tenant_id=$2 AND subject=$3", auditor.String(), tenant.String(), actor.Subject)
	cross := actor
	cross.TenantID = other
	if page, err := reader.ReadAudit(ctx, cross, task.ID, 0, 100); !errors.Is(err, identity.ErrUnauthenticated) || len(page.Entries) != 0 {
		t.Fatalf("cross-tenant actor: %v %v", page, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if page, err := reader.ReadAudit(cancelled, actor, task.ID, 0, 100); !errors.Is(err, context.Canceled) || len(page.Entries) != 0 {
		t.Fatalf("cancelled audit read: %v %v", page, err)
	}
	receipts()
	if err := f.FailReads(); err != nil {
		t.Fatal(err)
	}
	if page, err := reader.ReadAudit(ctx, actor, task.ID, 0, 100); err == nil || len(page.Entries) != 0 || page.NextAfter != 0 {
		t.Fatalf("audit failure returned data: %v %v", page, err)
	}
	receipts()
}

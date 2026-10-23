// Package contracttest exercises conversation storage against real databases.
package contracttest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type Inbox interface {
	Enqueue(context.Context, conversation.Message) error
}

type InboxFixture struct {
	New       func(uuid.UUID, string, string) Inbox
	Exec      func(string, ...any) error
	Counts    func() (tasks, audits int, err error)
	FailAudit func() error
	Read      func(tenant uuid.UUID, app, event string) (revision, prompt, state string, err error)
}

func RunInbox(t *testing.T, f InboxFixture) {
	t.Helper()
	ctx := t.Context()
	tenant, other, member, replacement := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	write := func(query string, args ...any) {
		t.Helper()
		if err := f.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uuid.UUID{tenant, other} {
		write("INSERT INTO iam.tenants(id,name) VALUES($1,'contract')", id.String())
		write("INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'member')", id.String(), member.String())
		write("INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,'contract-channel','issuer','subject',$2)", id.String(), member.String())
	}
	write("INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'replacement')", tenant.String(), replacement.String())
	m := conversation.Message{EventID: "event", MessageID: "message", Text: "private fixture", Fingerprint: "fingerprint",
		Identity: identity.ExternalIdentity{TenantID: tenant, Provider: "contract-channel", Issuer: "issuer", Subject: "subject"}}
	store := f.New(tenant, "app", "revision")
	second := f.New(tenant, "app", "revision")
	var wg sync.WaitGroup
	for i := range 6 {
		inbox := store
		if i%2 == 1 {
			inbox = second
		}
		wg.Go(func() {
			if err := inbox.Enqueue(ctx, m); err != nil {
				t.Errorf("concurrent duplicate: %v", err)
			}
		})
	}
	wg.Wait()
	counts := func(want int) {
		t.Helper()
		tasks, audits, err := f.Counts()
		if err != nil || tasks != want || audits != want {
			t.Fatalf("tasks=%d audits=%d error=%v; want %d each", tasks, audits, err, want)
		}
	}
	counts(1)
	if err := f.New(tenant, "app", "new-revision").Enqueue(ctx, m); err != nil {
		t.Fatalf("restart duplicate: %v", err)
	}
	duplicate := m
	duplicate.EventID = "new-event"
	if err := store.Enqueue(ctx, duplicate); err != nil {
		t.Fatalf("same message under new delivery: %v", err)
	}
	for _, field := range []string{"fingerprint", "message"} {
		conflict := m
		if field == "fingerprint" {
			conflict.Fingerprint = "changed"
		} else {
			conflict.MessageID = "changed"
		}
		if err := store.Enqueue(ctx, conflict); err == nil {
			t.Fatalf("accepted conflicting %s", field)
		}
	}
	counts(1)
	revision, prompt, state, err := f.Read(tenant, "app", m.EventID)
	if err != nil || revision != "revision" || prompt != m.Text || state != "pending" {
		t.Fatalf("duplicate changed persisted task: %q %q %q %v", revision, prompt, state, err)
	}
	cross := m
	cross.Identity.TenantID = other
	if err := store.Enqueue(ctx, cross); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("cross tenant: %v", err)
	}
	if err := f.New(other, "app", "revision").Enqueue(ctx, cross); err != nil {
		t.Fatal(err)
	}
	if err := f.New(tenant, "another-app", "revision").Enqueue(ctx, m); err != nil {
		t.Fatal(err)
	}
	counts(3)
	for _, table := range []string{"tenants", "members", "identity_links"} {
		key := "tenant_id"
		if table == "tenants" {
			key = "id"
		}
		query := "UPDATE iam." + table + " SET enabled=$1 WHERE " + key + "=$2"
		write(query, false, tenant.String())
		if err := store.Enqueue(ctx, m); !errors.Is(err, identity.ErrUnauthenticated) {
			t.Fatalf("disabled %s duplicate: %v", table, err)
		}
		write(query, true, tenant.String())
	}
	write("UPDATE iam.identity_links SET member_id=$1 WHERE tenant_id=$2", replacement.String(), tenant.String())
	if err := store.Enqueue(ctx, m); err == nil {
		t.Fatal("remapped identity took over old task")
	}
	counts(3)
	fresh := m
	fresh.EventID, fresh.MessageID = "fresh-event", "fresh-message"
	if err := store.Enqueue(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	counts(4)
	mixed := fresh
	mixed.EventID = m.EventID
	if err := store.Enqueue(ctx, mixed); err == nil {
		t.Fatal("accepted event and message belonging to different tasks")
	}
	counts(4)
	if err := f.FailAudit(); err != nil {
		t.Fatal(err)
	}
	fresh.EventID, fresh.MessageID = "failure-event", "failure-message"
	if err := store.Enqueue(ctx, fresh); err == nil {
		t.Fatal("acknowledged task without audit")
	}
	counts(4)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Enqueue(cancelled, m); err == nil {
		t.Fatal("acknowledged cancelled request")
	}
	counts(4)
}

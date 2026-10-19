//go:build integration

package feishu

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
)

type modelFunc func(context.Context, string) (string, error)

func (f modelFunc) Complete(ctx context.Context, text string) (string, error) { return f(ctx, text) }

type senderFunc func(context.Context, string, string, string) error

func (f senderFunc) Reply(ctx context.Context, message, id, text string) error {
	return f(ctx, message, id, text)
}

type chainFixture struct {
	pg      *testpg.Fixture
	db      *database.DB
	store   *conversation.Store
	adapter *Adapter
	member  uuid.UUID
	logger  *slog.Logger
}

func chain(t *testing.T) *chainFixture {
	t.Helper()
	ctx := t.Context()
	pg := testpg.New(t)
	migrator, err := database.NewMigrator(ctx, pg.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, migrateErr := migrator.Up(ctx)
	if err := errors.Join(migrateErr, migrator.Close()); err != nil {
		t.Fatal(err)
	}
	member := uuid.New()
	binding := testBinding()
	if _, err := pg.Admin.Exec(ctx, "INSERT INTO iam.tenants(id,name) VALUES($1,'test')", binding.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Admin.Exec(ctx, "INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'employee')", binding.TenantID, member); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Admin.Exec(ctx, "INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES($1,'feishu',$2,'ou_user',$3)", binding.TenantID, binding.Issuer(), member); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	store := conversation.NewStore(db, binding.TenantID, binding.AppID, "approved-revision")
	return &chainFixture{pg, db, store, testAdapter(t, store), member, slog.New(slog.NewTextHandler(io.Discard, nil))}
}
func (f *chainFixture) state(t *testing.T, want string) {
	t.Helper()
	var state string
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT state FROM channel.tasks").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != want {
		t.Fatalf("state=%s want=%s", state, want)
	}
}
func enqueue(t *testing.T, f *chainFixture) {
	t.Helper()
	if webhookStatus(t, f.adapter, encryptedRequest(t, eventBody(t, nil))) != 200 {
		t.Fatal("event not accepted")
	}
}

func TestDurablePrivateMessageChain(t *testing.T) {
	f := chain(t)
	// Concurrent delivery and subsequent restart reuse one persisted task.
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if webhookStatus(t, f.adapter, encryptedRequest(t, eventBody(t, nil))) != 200 {
				t.Error("duplicate rejected")
			}
		})
	}
	wg.Wait()
	restarted := testAdapter(t, conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "approved-revision"))
	if webhookStatus(t, restarted, encryptedRequest(t, eventBody(t, nil))) != 200 {
		t.Fatal("durable duplicate not acknowledged")
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"answer"}}]}`))
	}))
	defer provider.Close()
	modelClient, err := model.New(model.Config{Endpoint: provider.URL + "/v1/chat/completions", Name: "test-approved", AllowHTTP: true, MaxTokens: 64}, "")
	if err != nil {
		t.Fatal(err)
	}
	replies := 0
	worker := conversation.Worker{Store: f.store, Model: modelClient, Logger: f.logger, Telemetry: f.adapter.telemetry, Sender: senderFunc(func(_ context.Context, message, id, text string) error {
		replies++
		if message != "om_message" || text != "answer" {
			t.Error("reply target/content changed")
		}
		if _, err := uuid.Parse(id); err != nil {
			t.Error("missing stable delivery key")
		}
		return nil
	})}
	if worked, err := worker.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	if worked, err := worker.ProcessOne(t.Context()); err != nil || worked {
		t.Fatal("processed duplicate")
	}
	f.state(t, "succeeded")
	var correlated bool
	if err := f.pg.Admin.QueryRow(t.Context(), `SELECT length(t.ingress_trace_id)=32 AND length(t.ingress_span_id)=16 AND length(a.trace_id)=32 AND t.ingress_trace_id<>a.trace_id
 FROM channel.tasks t JOIN channel.audit a ON a.tenant_id=t.tenant_id AND a.task_id=t.id WHERE a.state='succeeded'`).Scan(&correlated); err != nil || !correlated {
		t.Fatalf("missing persisted trace link: %v", err)
	}
	if calls.Load() != 1 || replies != 1 {
		t.Fatal("duplicate external operation")
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit").Scan(&count); err != nil || count != 6 {
		t.Fatalf("audit transitions=%d err=%v", count, err)
	}
	// An authenticated replay with changed content must not create or alter work.
	if webhookStatus(t, f.adapter, encryptedRequest(t, eventBody(t, func(e *envelope) { e.Event.Message.Content = `{"text":"changed"}` }))) == 200 {
		t.Fatal("conflicting replay accepted")
	}
}

func TestRevocationBlocksModelAndDelivery(t *testing.T) {
	for _, phase := range []string{"before_model", "before_delivery"} {
		t.Run(phase, func(t *testing.T) {
			f := chain(t)
			enqueue(t, f)
			calls, replies := 0, 0
			revoke := func() {
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false"); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "before_model" {
				revoke()
			}
			worker := conversation.Worker{Store: f.store, Logger: f.logger, Telemetry: f.adapter.telemetry, Model: modelFunc(func(context.Context, string) (string, error) { calls++; revoke(); return "answer", nil }), Sender: senderFunc(func(context.Context, string, string, string) error { replies++; return nil })}
			if _, err := worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if replies != 0 || (phase == "before_model" && calls != 0) {
				t.Fatal("revocation failed to block external operation")
			}
			f.state(t, "failed")
		})
	}
}

func TestUnknownResultAndInterruptedTaskNeverAutoRetry(t *testing.T) {
	for _, phase := range []string{"model", "delivery", "interrupted"} {
		t.Run(phase, func(t *testing.T) {
			f := chain(t)
			enqueue(t, f)
			if phase == "interrupted" {
				task, err := f.store.Claim(t.Context())
				if err != nil || task == nil {
					t.Fatal("claim failed")
				}
			}
			modelCalls, replies := 0, 0
			worker := conversation.Worker{Store: f.store, Logger: f.logger, Telemetry: f.adapter.telemetry, Model: modelFunc(func(context.Context, string) (string, error) {
				modelCalls++
				if phase == "model" {
					return "", errors.New("timeout")
				}
				return "answer", nil
			}), Sender: senderFunc(func(context.Context, string, string, string) error {
				replies++
				return errors.New("connection lost after send")
			})}
			if _, err := worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if worked, err := worker.ProcessOne(t.Context()); err != nil || worked {
				t.Fatal("retried uncertain operation")
			}
			if phase == "interrupted" {
				f.state(t, "claimed")
				if modelCalls != 0 || replies != 0 {
					t.Fatal("re-executed claimed task")
				}
			} else {
				f.state(t, "unknown")
				if modelCalls != 1 {
					t.Fatal("unexpected model attempts")
				}
			}
		})
	}
}

func TestChannelTenantIsolationAndRevisionPinning(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	other := conversation.NewStore(f.db, uuid.New(), testBinding().AppID, "approved-revision")
	if task, err := other.Claim(t.Context()); err != nil || task != nil {
		t.Fatal("cross-tenant claim")
	}
	changed := conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "new-model-revision")
	if task, err := changed.Claim(t.Context()); err != nil || task != nil {
		t.Fatal("old task routed to new model")
	}
	var count int
	if err := f.db.Transaction(t.Context(), func(q database.Queries) error {
		return q.QueryRow(t.Context(), "SELECT count(*) FROM channel.tasks").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatal("unscoped task data leaked")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.members SET enabled=false"); err != nil {
		t.Fatal(err)
	}
	if webhookStatus(t, f.adapter, encryptedRequest(t, eventBody(t, func(e *envelope) { e.Header.EventID = "event_2"; e.Event.Message.MessageID = "om_second" }))) == 200 {
		t.Fatal("revoked ingress identity accepted")
	}
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			task, err := f.store.Claim(t.Context())
			if err != nil {
				t.Error(err)
			}
			if task != nil {
				claimed.Add(1)
			}
		})
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("concurrent claims=%d", claimed.Load())
	}
}

func TestTaskAndAuditRollbackTogether(t *testing.T) {
	f := chain(t)
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit ADD CONSTRAINT reject_test CHECK (state <> 'pending')"); err != nil {
		t.Fatal(err)
	}
	if webhookStatus(t, f.adapter, encryptedRequest(t, eventBody(t, nil))) == 200 {
		t.Fatal("acknowledged partial persistence")
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.tasks").Scan(&count); err != nil || count != 0 {
		t.Fatal("task leaked from failed audit transaction")
	}
}

//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQuoteApprovalCardAmendRestartDelivery(t *testing.T) {
	f, a, draft := quotePending(t, true)
	cards := make(chan string, 2)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"synthetic","expire":7200}`))
			return
		}
		var body struct {
			Content   string `json:"content"`
			Type      string `json:"msg_type"`
			Recipient string `json:"receive_id"`
		}
		if r.URL.Path != "/open-apis/im/v1/messages" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Type != "interactive" || body.Recipient != "ou_approver" {
			t.Error("invalid card destination")
			w.WriteHeader(400)
			return
		}
		cards <- body.Content
		_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_synthetic_card"}}`))
	}))
	defer remote.Close()
	sender := &Sender{lark.NewClient("cli_"+uuid.NewString(), "synthetic", lark.WithOpenBaseUrl(remote.URL), lark.WithHttpClient(remote.Client()), lark.WithLogger(sdkLogger{slog.New(slog.NewTextHandler(io.Discard, nil))}))}
	if worked, err := f.store.NotifyOne(t.Context(), sender); err != nil || !worked {
		t.Fatal(worked, err)
	}
	first := <-cards
	if !strings.Contains(first, "75.00") || !strings.Contains(first, "150.00") {
		t.Fatal("original quotation absent")
	}
	value := cardDecision(t, first, 0)
	callbackBody := func(value map[string]any, id string) []byte {
		return cardBody(t, conversation.ApprovalDecision{}, func(e map[string]any) {
			e["header"].(map[string]any)["event_id"] = id
			e["event"].(map[string]any)["action"].(map[string]any)["value"] = value
		})
	}
	if _, err := f.store.AmendQuotation(t.Context(), f.actor, quoteEdit(a, draft)); err != nil {
		t.Fatal(err)
	}
	adapter := testAdapter(t, f.store).WithApprovals(f.store)
	if status := webhookStatus(t, adapter, encryptedRequest(t, callbackBody(value, "stale-card"))); status == 200 {
		t.Fatal("old card approved changed quote")
	}
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	if worked, err := store.NotifyOne(t.Context(), sender); err != nil || !worked {
		t.Fatal(worked, err)
	}
	second := <-cards
	if !strings.Contains(second, "85.00") || !strings.Contains(second, "7500 → 8500") || !strings.Contains(second, "15000 → 17000") {
		t.Fatal("modified quotation or comparison absent")
	}
	adapter = testAdapter(t, store).WithApprovals(store)
	body := callbackBody(cardDecision(t, second, 0), "current-card")
	for range 2 {
		if status := webhookStatus(t, adapter, encryptedRequest(t, body)); status != 200 {
			t.Fatal("current card or repeat rejected", status)
		}
	}
	f.worker.Store = store
	f.worker.Sender = senderFunc(func(_ context.Context, _, _, text string) error {
		if !strings.Contains(text, "报价初稿 · V2") || !strings.Contains(text, "85.00") || !strings.Contains(text, "170.00") {
			t.Error("delivery differs from approved quotation")
		}
		f.sends.Add(1)
		return nil
	})
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatal("resume", worked, err)
	}
	if f.sends.Load() != 1 {
		t.Fatal("delivery count", f.sends.Load())
	}
	var state string
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT state FROM channel.tasks").Scan(&state); err != nil || state != "succeeded" {
		t.Fatal(state, err)
	}
	if worked, err := store.NotifyOne(t.Context(), sender); err != nil || worked {
		t.Fatal("duplicate notification", worked, err)
	}
}

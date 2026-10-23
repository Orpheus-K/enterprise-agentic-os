//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
)

type retrievalTestSender func(context.Context, string, string, string) error

func (f retrievalTestSender) Reply(ctx context.Context, message, id, text string) error {
	return f(ctx, message, id, text)
}

func TestOpenRetrievalValidatesSpaceAndRunsWorker(t *testing.T) {
	for _, mode := range []string{"valid", "dimensions", "model", "revision", "disabled", "installation"} {
		t.Run(mode, func(t *testing.T) {
			pg := testpg.New(t)
			m, err := database.NewMigrator(t.Context(), pg.MigrationURL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Up(t.Context())
			if err = errors.Join(err, m.Close()); err != nil {
				t.Fatal(err)
			}
			tenantID, appID := uuid.New(), "cli_test"
			b := feishu.Binding{TenantID: tenantID, AppID: appID, TenantKey: "tenant"}
			member, spaceID := uuid.New(), uuid.New()
			for _, s := range []struct {
				query string
				args  []any
			}{
				{"INSERT INTO iam.tenants VALUES($1,'test',true)", []any{b.TenantID}},
				{"INSERT INTO iam.members VALUES($1,$2,'test',true)", []any{b.TenantID, member}},
				{"INSERT INTO iam.identity_links VALUES($1,'feishu',$2,'ou_test',$3,true)", []any{b.TenantID, b.Issuer(), member}},
			} {
				if _, err := pg.Admin.Exec(t.Context(), s.query, s.args...); err != nil {
					t.Fatal(err)
				}
			}
			dims, name, revision, app, enabled := 3, "embed", "v1", b.AppID, true
			switch mode {
			case "dimensions":
				dims = 4
			case "model":
				name = "other"
			case "revision":
				revision = "v2"
			case "disabled":
				enabled = false
			case "installation":
				app = "other"
			}
			if _, err := pg.Admin.Exec(t.Context(), "INSERT INTO knowledge.embedding_spaces VALUES($1,$2,$3,$4,$5,$6,$7)", b.TenantID, app, spaceID, name, revision, dims, enabled); err != nil {
				t.Fatal(err)
			}
			var embeds, chats atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/embeddings":
					embeds.Add(1)
					if r.Header.Get("Authorization") != "Bearer synthetic-embed" {
						t.Error("embedding used wrong credential")
					}
					_, _ = w.Write([]byte(`{"data":[{"embedding":[1,0,0]}]}`))
				case "/chat/completions":
					chats.Add(1)
					if r.Header.Get("Authorization") != "Bearer synthetic-chat" {
						t.Error("chat used wrong credential")
					}
					raw, err := io.ReadAll(r.Body)
					if err != nil || !strings.Contains(string(raw), "没有可用资料") {
						t.Error("retrieval instructions missing")
					}
					_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"没有可用资料"}}]}`))
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			t.Setenv("AGENTOS_BOOT_RETRIEVAL_DB", pg.RuntimeURL)
			t.Setenv("AGENTOS_BOOT_RETRIEVAL_APP", "synthetic-app")
			t.Setenv("AGENTOS_BOOT_RETRIEVAL_EMBED", "synthetic-embed")
			t.Setenv("AGENTOS_BOOT_RETRIEVAL_CHAT", "synthetic-chat")
			c := Config{TenantID: tenantID, AppID: appID, Channel: testFeishu("websocket", "AGENTOS_BOOT_RETRIEVAL_APP", "", ""), DatabaseURLEnv: "AGENTOS_BOOT_RETRIEVAL_DB",
				Model:     model.Config{Endpoint: server.URL + "/chat/completions", Name: "chat", MaxTokens: 64, AllowHTTP: true, APIKeyEnv: "AGENTOS_BOOT_RETRIEVAL_CHAT"},
				Embedding: &model.EmbeddingConfig{Endpoint: server.URL + "/embeddings", Name: "embed", Revision: "v1", Dimensions: 3, AllowHTTP: true, APIKeyEnv: "AGENTOS_BOOT_RETRIEVAL_EMBED"},
				Retrieval: &conversation.RetrievalPolicy{Space: knowledge.Space{ID: spaceID, Model: "embed", ModelRevision: "v1"}, Limit: 3}}
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "channel.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			channel, err := Open(t.Context(), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if mode != "valid" {
				if err == nil {
					channel.Close()
					t.Fatal("invalid space started")
				}
				if embeds.Load() != 0 || chats.Load() != 0 {
					t.Fatal("startup sent provider request")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer channel.Close()
			if embeds.Load() != 0 || chats.Load() != 0 {
				t.Fatal("startup contacted model")
			}
			sends := 0
			channel.worker.Sender = retrievalTestSender(func(context.Context, string, string, string) error { sends++; return nil })
			inbox, ok := channel.worker.Store.(interface {
				Enqueue(context.Context, conversation.Message) error
			})
			if !ok {
				t.Fatal("configured store has no inbox")
			}
			if err := inbox.Enqueue(t.Context(), conversation.Message{EventID: "event", MessageID: "message", Text: "synthetic query", Fingerprint: "synthetic", Identity: identity.ExternalIdentity{TenantID: b.TenantID, Provider: "feishu", Issuer: b.Issuer(), Subject: "ou_test"}}); err != nil {
				t.Fatal(err)
			}
			if worked, err := channel.worker.ProcessOne(t.Context()); err != nil || !worked {
				t.Fatalf("worker: %v", err)
			}
			if embeds.Load() != 1 || chats.Load() != 1 || sends != 1 {
				t.Fatal("bootstrap did not wire complete retrieval chain")
			}
		})
	}
}

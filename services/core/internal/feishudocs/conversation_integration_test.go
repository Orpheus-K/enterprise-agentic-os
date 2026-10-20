//go:build integration

package feishudocs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/telemetry"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
)

type sourceModel func(context.Context, string, string) (string, error)

func (sourceModel) Complete(context.Context, string) (string, error) {
	return "", errors.New("source evidence bypassed")
}
func (f sourceModel) CompleteSkill(ctx context.Context, instructions, prompt string) (string, error) {
	return f(ctx, instructions, prompt)
}

type sourceSender func(context.Context, string, string, string) error

func (f sourceSender) Reply(ctx context.Context, message, id, text string) error {
	return f(ctx, message, id, text)
}

// Uses the native sync fixture's published records, never manual content/ACL
// writes. Only provider HTTP and outbound delivery are synthetic.
func verifySyncedConversation(t *testing.T, db *database.DB, pg *testpg.Fixture, tenant uuid.UUID, actor identity.ExternalIdentity, space knowledge.Space, endpoint, mode string, afterModel func()) {
	t.Helper()
	embedder, err := model.NewEmbedder(model.EmbeddingConfig{Endpoint: endpoint + "/embeddings", Name: space.Model, Revision: space.ModelRevision, Dimensions: 3, AllowHTTP: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := conversation.NewStore(db, tenant, "cli_test", "synthetic-source-conversation-v1").WithRetrieval(conversation.RetrievalPolicy{Space: space, Limit: 1}, embedder)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(t.Context(), conversation.Message{EventID: "source-event", MessageID: "source-message", Text: "Summarize the evidence", Fingerprint: strings.Repeat("a", 64), Identity: actor}); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tel, err := telemetry.New(logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tel.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	models, sends := 0, 0
	worker := conversation.Worker{Store: store, Logger: logger, Telemetry: tel,
		Model: sourceModel(func(_ context.Context, instructions, prompt string) (string, error) {
			models++
			if !strings.Contains(instructions, "Synthetic evidence") || strings.Contains(instructions, "Old evidence") {
				t.Error("native publication not used as model evidence")
			}
			afterModel()
			return "Grounded answer [1]", nil
		}),
		Sender: sourceSender(func(_ context.Context, message, id, text string) error {
			sends++
			if message != "source-message" || !strings.Contains(text, "Synthetic") || !strings.Contains(text, "Grounded answer") {
				t.Error("source footer or delivery target missing")
			}
			return nil
		}),
	}
	if worked, err := worker.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatal("conversation failed", worked, err)
	}
	wantState, wantSends := "succeeded", 1
	if strings.HasPrefix(mode, "delivery_") {
		wantState, wantSends = "failed", 0
	}
	var state string
	var refs []byte
	if err := pg.Admin.QueryRow(t.Context(), "SELECT state,knowledge_refs FROM channel.tasks WHERE event_id='source-event'").Scan(&state, &refs); err != nil {
		t.Fatal(err)
	}
	if state != wantState || models != 1 || sends != wantSends || !strings.Contains(string(refs), "doxcTest") {
		t.Fatalf("source-to-delivery state=%s model=%d sends=%d refs=%s", state, models, sends, refs)
	}
	if worked, err := worker.ProcessOne(t.Context()); err != nil || worked {
		t.Fatal("completed or revoked conversation replayed", worked, err)
	}
}

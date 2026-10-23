package sqlite

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/sqlitedb"
	"agentos.local/core/internal/telemetry"
	"context"
	"errors"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

var _ conversation.ExecutionStore = (*Inbox)(nil)

type textModel func(context.Context, string) (string, error)

func (f textModel) Complete(ctx context.Context, s string) (string, error) { return f(ctx, s) }

type textSender func(context.Context, string, string, string) error

func (f textSender) Reply(ctx context.Context, a, b, c string) error { return f(ctx, a, b, c) }
func TestSharedWorkerWithSQLite(t *testing.T) {
	for _, scenario := range []string{"success", "model_unknown", "delivery_unknown", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "worker.db")
			if err := sqlitedb.Migrate(ctx, path); err != nil {
				t.Fatal(err)
			}
			db, err := sqlitedb.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			tenant, member := uuid.New(), uuid.New()
			for index, q := range []string{"INSERT INTO iam_tenants(id,name) VALUES(?,'worker')", "INSERT INTO iam_members(tenant_id,id,employee_id) VALUES(?,?,'employee')", "INSERT INTO iam_identity_links(tenant_id,provider,issuer,subject,member_id) VALUES(?,'contract-channel','issuer','subject',?)"} {
				args := []any{tenant.String()}
				if index > 0 {
					args = append(args, member.String())
				}
				if _, err := db.ExecContext(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			store := NewInbox(db, tenant, "app", "revision")
			if err := store.Enqueue(ctx, conversation.Message{EventID: "e", MessageID: "m", Text: "prompt", Fingerprint: "h", Identity: identity.ExternalIdentity{TenantID: tenant, Provider: "contract-channel", Issuer: "issuer", Subject: "subject"}}); err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			tel, err := telemetry.New(logger)
			if err != nil {
				t.Fatal(err)
			}
			defer tel.Close(context.Background())
			models, sends := 0, 0
			worker := conversation.Worker{Store: store, Logger: logger, Telemetry: tel, Model: textModel(func(ctx context.Context, prompt string) (string, error) {
				models++
				if prompt != "prompt" {
					t.Fatal("prompt changed")
				}
				if scenario == "model_unknown" {
					return "", errors.New("unknown")
				}
				if scenario == "revoked" {
					if _, err := db.ExecContext(ctx, "UPDATE iam_members SET enabled=0 WHERE tenant_id=?", tenant.String()); err != nil {
						t.Fatal(err)
					}
				}
				return "response", nil
			}), Sender: textSender(func(ctx context.Context, message, key, text string) error {
				sends++
				if message != "m" || text != "response" || key == "" {
					t.Fatal("delivery changed")
				}
				if scenario == "delivery_unknown" {
					return errors.New("unknown")
				}
				return nil
			})}
			if worked, err := worker.ProcessOne(ctx); err != nil || !worked {
				t.Fatalf("process: %v %v", worked, err)
			}
			// Rebuild the store as after restart; terminal/unknown tasks cannot be replayed.
			worker.Store = NewInbox(db, tenant, "app", "revision")
			if worked, err := worker.ProcessOne(ctx); err != nil || worked {
				t.Fatalf("replayed: %v %v", worked, err)
			}
			want := "succeeded"
			if scenario == "model_unknown" || scenario == "delivery_unknown" {
				want = "unknown"
			}
			if scenario == "revoked" {
				want = "failed"
			}
			var state string
			if err := db.QueryRowContext(ctx, "SELECT state FROM channel_tasks").Scan(&state); err != nil || state != want {
				t.Fatalf("state %s %v", state, err)
			}
			expectedSends := 1
			if scenario == "model_unknown" || scenario == "revoked" {
				expectedSends = 0
			}
			if models != 1 || sends != expectedSends {
				t.Fatalf("external calls %d %d", models, sends)
			}
			// A restored successful, unknown or revoked task must not replay effects.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(t.TempDir(), "snapshot")
			restoredPath := filepath.Join(t.TempDir(), "restored.db")
			if err := sqlitedb.Backup(ctx, path, backup); err != nil {
				t.Fatal(err)
			}
			if err := sqlitedb.Restore(ctx, backup, restoredPath); err != nil {
				t.Fatal(err)
			}
			restored, err := sqlitedb.Open(ctx, restoredPath)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			worker.Store = NewInbox(restored, tenant, "app", "revision")
			if worked, err := worker.ProcessOne(ctx); err != nil || worked {
				t.Fatalf("restored task replayed: %v %v", worked, err)
			}
			if models != 1 || sends != expectedSends {
				t.Fatal("restore repeated external call")
			}
		})
	}
}

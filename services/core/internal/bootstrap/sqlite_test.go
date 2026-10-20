package bootstrap

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/health"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/quotation"
	"agentos.local/core/internal/sqlitedb"
	"github.com/google/uuid"
)

func sqliteConfig(t *testing.T) Config {
	t.Helper()
	t.Setenv("SQLITE_BOOT_APP", "synthetic-app")
	t.Setenv("SQLITE_BOOT_TOKEN", "synthetic-token")
	t.Setenv("SQLITE_BOOT_KEY", "synthetic-key")
	t.Setenv("SQLITE_BOOT_OPS", strings.Repeat("x", 32))
	return Config{StorageDriver: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "runtime.db"),
		Binding: feishu.Binding{TenantID: uuid.New(), AppID: "cli_test", TenantKey: "tenant"},
		Mode:    "webhook", AppSecretEnv: "SQLITE_BOOT_APP", VerificationTokenEnv: "SQLITE_BOOT_TOKEN", EncryptKeyEnv: "SQLITE_BOOT_KEY",
		Model:      model.Config{Endpoint: "https://approved.example/chat/completions", Name: "approved", MaxTokens: 64},
		Operations: &OperationsConfig{TokenEnv: "SQLITE_BOOT_OPS", Subject: "operator"}}
}

func writeSQLiteConfig(t *testing.T, c Config) string {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "channel.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSQLiteConfigurationRejectsUnsupportedBeforeOpen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"driver", func(c *Config) { c.StorageDriver = "unknown" }},
		{"ambiguous", func(c *Config) { c.DatabaseURLEnv = "MISSING_DB" }},
		{"relative", func(c *Config) { c.SQLitePath = "relative.db" }},
		{"implicit", func(c *Config) { c.StorageDriver = "" }},
		{"embedding", func(c *Config) { c.Embedding = &model.EmbeddingConfig{} }},
		{"retrieval", func(c *Config) { c.Retrieval = &conversation.RetrievalPolicy{} }},
		{"sources", func(c *Config) { c.Sources = []SourceConfig{{}} }},
		{"skills", func(c *Config) { c.Skills = &SkillsConfig{} }},
		{"tools", func(c *Config) { c.Tools = &ToolsConfig{} }},
		{"quotation", func(c *Config) { c.QuotationWASMSHA256 = quotation.ApprovalModuleSHA256 }},
		{"notice", func(c *Config) {
			c.Approval = &conversation.ApprovalPolicy{MemberID: uuid.New(), TTLSeconds: 60, NotifySubject: "ou_manager"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sqliteConfig(t)
			tc.change(&c)
			channel, err := Open(t.Context(), writeSQLiteConfig(t, c), slog.Default())
			if err == nil {
				channel.Close()
				t.Fatal("unsupported configuration started")
			}
			if strings.Contains(err.Error(), "startup failed") || strings.Contains(err.Error(), "secret environment") {
				t.Fatalf("configuration was not rejected before resource access: %v", err)
			}
		})
	}
}

func TestSQLiteStartupRequiresMigratedFile(t *testing.T) {
	for _, state := range []string{"missing", "empty", "old", "future"} {
		t.Run(state, func(t *testing.T) {
			c := sqliteConfig(t)
			if state == "empty" {
				if err := os.WriteFile(c.SQLitePath, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if state != "missing" {
				if err := sqlitedb.Migrate(t.Context(), c.SQLitePath); err != nil {
					t.Fatal(err)
				}
				db, err := sqlitedb.Open(t.Context(), c.SQLitePath)
				if err != nil {
					t.Fatal(err)
				}
				if state == "old" {
					_, err = db.Exec("DELETE FROM goose_db_version WHERE version_id=?", sqlitedb.SchemaVersion)
				} else {
					_, err = db.Exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES(?,1)", sqlitedb.SchemaVersion+1)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			channel, err := Open(t.Context(), writeSQLiteConfig(t, c), slog.Default())
			if err == nil {
				channel.Close()
				t.Fatal("invalid file started")
			}
			if !strings.Contains(err.Error(), "SQLite database startup failed") {
				t.Fatalf("did not reach SQLite open: %v", err)
			}
			if state == "missing" {
				if _, err := os.Stat(c.SQLitePath); !os.IsNotExist(err) {
					t.Fatal("startup created database")
				}
			}
		})
	}
}

type sqliteSender func(context.Context, string, string, string) error

func (f sqliteSender) Reply(ctx context.Context, message, id, text string) error {
	return f(ctx, message, id, text)
}

func sqliteEvent(t *testing.T) *http.Request {
	t.Helper()
	plain := []byte(`{"schema":"2.0","header":{"app_id":"cli_test","tenant_key":"tenant","event_id":"e","event_type":"im.message.receive_v1","token":"synthetic-token"},"event":{"sender":{"sender_id":{"open_id":"ou_user"},"sender_type":"user","tenant_key":"tenant"},"message":{"message_id":"m","chat_id":"chat","chat_type":"p2p","message_type":"text","content":"{\"text\":\"synthetic question\"}"}}}`)
	key := sha256.Sum256([]byte("synthetic-key"))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	// Fixed IV is confined to this synthetic transport fixture.
	encrypted := make([]byte, aes.BlockSize+len(padded))
	cipher.NewCBCEncrypter(block, encrypted[:aes.BlockSize]).CryptBlocks(encrypted[aes.BlockSize:], padded)
	body, err := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(encrypted)})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/channels/feishu/events", bytes.NewReader(body))
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	r.Header.Set("X-Lark-Request-Timestamp", stamp)
	r.Header.Set("X-Lark-Request-Nonce", "synthetic")
	hash := sha256.Sum256(append([]byte(stamp+"synthetic"+"synthetic-key"), body...))
	r.Header.Set("X-Lark-Signature", hex.EncodeToString(hash[:]))
	return r
}

func TestSQLiteOpenApprovalRestartAndRevocation(t *testing.T) {
	for _, scenario := range []string{"success", "approver_revoked", "sender_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			c := sqliteConfig(t)
			member := uuid.New()
			c.Approval = &conversation.ApprovalPolicy{MemberID: member, TTLSeconds: 3600}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"approved response"}}]}`)
			}))
			defer server.Close()
			c.Model.Endpoint, c.Model.AllowHTTP = server.URL+"/chat/completions", true
			if err := sqlitedb.Migrate(t.Context(), c.SQLitePath); err != nil {
				t.Fatal(err)
			}
			db, err := sqlitedb.Open(t.Context(), c.SQLitePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO iam_tenants(id,name) VALUES(?,'test')", c.Binding.TenantID.String())
			exec("INSERT INTO iam_members(tenant_id,id,employee_id) VALUES(?,?,'employee')", c.Binding.TenantID.String(), member.String())
			exec("INSERT INTO iam_identity_links(tenant_id,provider,issuer,subject,member_id) VALUES(?,'feishu',?,'ou_user',?)", c.Binding.TenantID.String(), c.Binding.Issuer(), member.String())
			exec("INSERT INTO iam_identity_links(tenant_id,provider,issuer,subject,member_id) VALUES(?,'operator',?,'operator',?)", c.Binding.TenantID.String(), c.operator().Issuer, member.String())
			exec("INSERT INTO iam_operation_grants(tenant_id,member_id,app_id,capability) VALUES(?,?,?,'task.approve')", c.Binding.TenantID.String(), member.String(), c.Binding.AppID)
			path := writeSQLiteConfig(t, c)
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			var sends int
			open := func() *Channel {
				t.Helper()
				channel, err := Open(t.Context(), path, logger)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(channel.Close)
				channel.worker.Sender = sqliteSender(func(_ context.Context, message, id, text string) error {
					if message != "m" || id == "" || text != "approved response" {
						t.Error("wrong delivery")
					}
					sends++
					return nil
				})
				return channel
			}
			channel := open()
			if calls.Load() != 0 {
				t.Fatal("startup called provider")
			}
			channel.Readiness.SetTransport(health.Listening)
			channel.Readiness.SetWorker(true)
			if !channel.Readiness.Snapshot(t.Context()).Ready {
				t.Fatal("configured storage not ready")
			}
			exec("UPDATE iam_tenants SET enabled=0")
			if channel.Readiness.Snapshot(t.Context()).Ready {
				t.Fatal("disabled tenant ready")
			}
			exec("UPDATE iam_tenants SET enabled=1")
			w := httptest.NewRecorder()
			channel.Handler.ServeHTTP(w, sqliteEvent(t))
			if w.Code != 200 {
				t.Fatalf("ingress: %d %s", w.Code, w.Body.String())
			}
			if worked, err := channel.worker.ProcessOne(t.Context()); err != nil || !worked {
				t.Fatalf("worker: %v", err)
			}
			if calls.Load() != 1 || sends != 0 {
				t.Fatal("approval did not suspend delivery")
			}
			var task string
			if err := db.QueryRow("SELECT id FROM channel_tasks").Scan(&task); err != nil {
				t.Fatal(err)
			}
			channel.Close()
			if channel.Readiness.Snapshot(t.Context()).Storage != "unavailable" {
				t.Fatal("closed database ready")
			}
			channel = open()
			request := func(method, path string, body []byte) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
				w := httptest.NewRecorder()
				channel.Operations.ServeHTTP(w, r)
				return w
			}
			w = request("GET", "/api/ops/approval?task_id="+task, nil)
			var approval conversation.Approval
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &approval) != nil || approval.Response != "approved response" {
				t.Fatalf("inspect: %d %s", w.Code, w.Body.String())
			}
			decision, _ := json.Marshal(conversation.ApprovalDecision{TaskID: approval.TaskID, ID: approval.ID, RequestID: uuid.New(), Revision: approval.Revision, Version: approval.Version, ContentHash: approval.ContentHash, Decision: "approve"})
			if scenario == "approver_revoked" {
				exec("UPDATE iam_operation_grants SET enabled=0")
			}
			w = request("POST", "/api/ops/approval", decision)
			want := 200
			if scenario == "approver_revoked" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("decision: %d %s", w.Code, w.Body.String())
			}
			if scenario == "sender_revoked" {
				exec("UPDATE iam_identity_links SET enabled=0 WHERE provider='feishu'")
			}
			if _, err := channel.worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			wantSends := 0
			if scenario == "success" {
				wantSends = 1
			}
			if calls.Load() != 1 || sends != wantSends {
				t.Fatalf("calls=%d sends=%d", calls.Load(), sends)
			}
			var state string
			if err := db.QueryRow("SELECT state FROM channel_tasks WHERE id=?", task).Scan(&state); err != nil {
				t.Fatal(err)
			}
			wantState := "succeeded"
			if scenario == "approver_revoked" {
				wantState = "waiting_approval"
			} else if scenario == "sender_revoked" {
				wantState = "failed"
			}
			if state != wantState {
				t.Fatalf("state=%s want=%s", state, wantState)
			}
			channel.Close()
			channel = open()
			if _, err := channel.worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || sends != wantSends {
				t.Fatal("restart replayed external effect")
			}
			exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES(?,1)", sqlitedb.SchemaVersion+1)
			if channel.Readiness.Snapshot(t.Context()).Storage != "unavailable" {
				t.Fatal("wrong schema ready")
			}
		})
	}
}

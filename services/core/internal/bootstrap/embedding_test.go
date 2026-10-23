package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/model"
	"github.com/google/uuid"
)

func TestEmbeddingConfiguration(t *testing.T) {
	base := Config{
		TenantID: uuid.New(), AppID: "cli_test", Channel: testFeishu("websocket", "APP_SECRET", "", ""),
		DatabaseURLEnv: "DB_URL",
		Model:          model.Config{Endpoint: "https://approved.example/v1/chat/completions", Name: "chat", MaxTokens: 64},
	}
	for _, tc := range []struct {
		name   string
		config *model.EmbeddingConfig
		valid  bool
	}{
		{"absent", nil, true},
		{"valid", &model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Revision: "r1", Dimensions: 3}, true},
		{"empty", &model.EmbeddingConfig{}, false},
		{"unapproved HTTP", &model.EmbeddingConfig{Endpoint: "http://approved.example/v1/embeddings", Name: "embed", Revision: "r1", Dimensions: 3}, false},
		{"missing revision", &model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Dimensions: 3}, false},
		{"invalid dimensions", &model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Revision: "r1", Dimensions: 4097}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			c.Embedding = tc.config
			raw, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("parse: %v", err)
			}
			if err == nil && tc.config != nil && *parsed.Embedding != *tc.config {
				t.Fatal("embedding configuration changed")
			}
		})
	}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"embedding":{"unexpected":true}}`)...)
	if _, err := Parse(raw); err == nil {
		t.Fatal("accepted unknown embedding field")
	}
}

func TestOpenEmbedderCredentials(t *testing.T) {
	if client, err := openEmbedder(nil); err != nil || client != nil {
		t.Fatalf("absent configuration: %v", err)
	}
	const env = "AGENTOS_TEST_EMBEDDING_KEY"
	t.Setenv(env, "")
	c := model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Revision: "r1", Dimensions: 3, APIKeyEnv: env}
	if client, err := openEmbedder(&c); err == nil || client != nil {
		t.Fatal("missing named credential accepted")
	}
	for _, authenticated := range []bool{false, true} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			want := ""
			if authenticated {
				want = "Bearer embedding-only-secret"
			}
			if r.Header.Get("Authorization") != want {
				t.Error("incorrect credential binding")
			}
			var body struct {
				Model string `json:"model"`
				Input string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "embed" || body.Input != "synthetic query" {
				t.Error("incorrect embedding request")
			}
			_, _ = w.Write([]byte(`{"data":[{"embedding":[1,2,3]}]}`))
		}))
		c.Endpoint, c.AllowHTTP = server.URL+"/embeddings", true
		c.APIKeyEnv = ""
		if authenticated {
			c.APIKeyEnv = env
			t.Setenv(env, "embedding-only-secret")
		}
		client, err := openEmbedder(&c)
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		if calls != 0 {
			t.Fatal("startup contacted provider")
		}
		vector, err := client.Embed(context.Background(), "synthetic query")
		server.Close()
		if err != nil || len(vector) != 3 || calls != 1 {
			t.Fatalf("embed: %v", err)
		}
	}
	c.APIKeyEnv = env
	t.Setenv(env, "")
	_, err := openEmbedder(&c)
	if err == nil || strings.Contains(err.Error(), env) || strings.Contains(err.Error(), "embedding-only-secret") {
		t.Fatal("credential failure missing or not redacted")
	}
}

func TestChannelRejectsMissingEmbeddingSecretBeforeDatabase(t *testing.T) {
	t.Setenv("AGENTOS_TEST_BOOT_DB", "invalid-database-url")
	t.Setenv("AGENTOS_TEST_BOOT_APP", "synthetic-app-secret")
	t.Setenv("AGENTOS_TEST_BOOT_EMBED", "")
	c := Config{
		TenantID: uuid.New(), AppID: "cli_test", Channel: testFeishu("websocket", "AGENTOS_TEST_BOOT_APP", "", ""),
		DatabaseURLEnv: "AGENTOS_TEST_BOOT_DB",
		Model:          model.Config{Endpoint: "https://approved.example/v1/chat/completions", Name: "chat", MaxTokens: 64},
		Embedding:      &model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Revision: "r1", Dimensions: 3, APIKeyEnv: "AGENTOS_TEST_BOOT_EMBED"},
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "channel.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	channel, err := Open(context.Background(), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if channel != nil {
		channel.Close()
		t.Fatal("started without embedding secret")
	}
	if err == nil || err.Error() != "required channel secret environment variable is missing" {
		t.Fatalf("startup did not reject embedding secret before database access: %v", err)
	}
}

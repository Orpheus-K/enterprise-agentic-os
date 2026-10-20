package bootstrap

import (
	"encoding/json"
	"testing"

	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"github.com/google/uuid"
)

func sourceTestConfig() Config {
	return Config{Binding: feishu.Binding{TenantID: uuid.New(), AppID: "cli_test", TenantKey: "tenant"},
		Mode: "websocket", DatabaseURLEnv: "SOURCE_DB", AppSecretEnv: "SOURCE_APP",
		Model:      model.Config{Endpoint: "https://approved.example/v1/chat/completions", Name: "chat", MaxTokens: 64},
		Embedding:  &model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Revision: "v1", Dimensions: 3},
		Operations: &OperationsConfig{TokenEnv: "SOURCE_OPS", Subject: "operator"},
		Sources: []SourceConfig{{SourceID: uuid.New(), Documents: []string{"doxcTest"},
			Owner: SourceUser{OpenID: "ou_sync", AccessTokenEnv: "SOURCE_OWNER"}, Recipients: []SourceUser{{OpenID: "ou_employee", AccessTokenEnv: "SOURCE_READER"}},
			Space: knowledge.Space{ID: uuid.New(), Model: "embed", ModelRevision: "v1"}, PollSeconds: 10}}}
}

func TestSourceConfiguration(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"no_embedding":       func(c *Config) { c.Embedding = nil },
		"no_operations":      func(c *Config) { c.Operations = nil },
		"bad_model":          func(c *Config) { c.Sources[0].Space.Model = "other" },
		"duplicate_document": func(c *Config) { c.Sources[0].Documents = []string{"a", "a"} },
		"url_document":       func(c *Config) { c.Sources[0].Documents = []string{"https://example.com/document"} },
		"empty_documents":    func(c *Config) { c.Sources[0].Documents = nil },
		"bad_poll":           func(c *Config) { c.Sources[0].PollSeconds = 0 },
		"raw_secret":         func(c *Config) { c.Sources[0].Owner.AccessTokenEnv = "Bearer secret" },
		"bad_owner":          func(c *Config) { c.Sources[0].Owner.OpenID = "other" },
		"duplicate_source":   func(c *Config) { c.Sources = append(c.Sources, c.Sources[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			c := sourceTestConfig()
			change(&c)
			raw, _ := json.Marshal(c)
			if _, err := Parse(raw); err == nil {
				t.Fatal("invalid source configuration accepted")
			}
		})
	}
	c := sourceTestConfig()
	raw, _ := json.Marshal(c)
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOURCE_OWNER", "synthetic")
	t.Setenv("SOURCE_READER", "")
	if _, err := c.sourceCredentials(); err == nil {
		t.Fatal("missing recipient token accepted")
	}
	c.Sources = nil
	if prepared, err := c.sourceCredentials(); err != nil || len(prepared) != 0 {
		t.Fatal("absent sources loaded credentials")
	}
}

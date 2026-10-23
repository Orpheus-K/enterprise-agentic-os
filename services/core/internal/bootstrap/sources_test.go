package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"github.com/google/uuid"
)

func sourceTestConfig() Config {
	return Config{TenantID: uuid.New(), AppID: "cli_test", Channel: testFeishu("websocket", "SOURCE_APP", "", ""),
		DatabaseURLEnv: "SOURCE_DB",
		Model:          model.Config{Endpoint: "https://approved.example/v1/chat/completions", Name: "chat", MaxTokens: 64},
		Embedding:      &model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Revision: "v1", Dimensions: 3},
		Operations:     &OperationsConfig{TokenEnv: "SOURCE_OPS", Subject: "operator"},
		Sources: []SourceConfig{{SourceID: uuid.New(), Documents: []string{"doxcTest"},
			Owner: SourceUser{OpenID: "ou_sync", AccessTokenEnv: "SOURCE_OWNER"}, Recipients: []SourceUser{{OpenID: "ou_employee", AccessTokenEnv: "SOURCE_READER"}},
			Space: knowledge.Space{ID: uuid.New(), Model: "embed", ModelRevision: "v1"}, PollSeconds: 10}}}
}

func TestSourceOAuthConfiguration(t *testing.T) {
	c := sourceTestConfig()
	c.OAuth = &OAuthConfig{ActiveKeyID: "v1", KeyEnvs: map[string]string{"v1": "SOURCE_OAUTH_KEY"}}
	c.Sources[0].Owner = SourceUser{OpenID: "ou_sync", UseOAuth: true}
	c.Sources[0].Recipients = nil
	raw, _ := json.Marshal(c)
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOURCE_OAUTH_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	prepared, err := c.sourceCredentials()
	if err != nil || len(prepared) != 1 || prepared[0].oauthKeys == nil {
		t.Fatal("OAuth key preparation", err)
	}
	c.Sources[0].Owner.AccessTokenEnv = "STATIC_TOKEN"
	raw, _ = json.Marshal(c)
	if _, err := Parse(raw); err == nil {
		t.Fatal("ambiguous static/OAuth accepted")
	}
	c.Sources[0].Owner.AccessTokenEnv = ""
	t.Setenv("SOURCE_OAUTH_KEY", "invalid-key")
	if _, err := c.sourceCredentials(); err == nil {
		t.Fatal("invalid encryption key accepted")
	}
	c.OAuth = nil
	raw, _ = json.Marshal(c)
	if _, err := Parse(raw); err == nil {
		t.Fatal("OAuth without keys accepted")
	}
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

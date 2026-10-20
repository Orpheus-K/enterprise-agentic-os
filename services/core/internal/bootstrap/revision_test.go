package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"github.com/google/uuid"
)

func TestRetrievalConfigurationAndExecutionRevision(t *testing.T) {
	base := Config{Binding: feishu.Binding{TenantID: uuid.New(), AppID: "cli_test", TenantKey: "tenant"}, Mode: "websocket", DatabaseURLEnv: "DB", AppSecretEnv: "SECRET",
		Model: model.Config{Endpoint: "https://approved.example/v1/chat/completions", Name: "chat", MaxTokens: 64}}
	legacy, _ := json.Marshal(struct {
		Binding feishu.Binding
		Model   model.Config
	}{base.Binding, base.Model})
	sum := sha256.Sum256(legacy)
	if base.executionRevision() != hex.EncodeToString(sum[:]) {
		t.Fatal("pure-chat fingerprint changed")
	}
	e := model.EmbeddingConfig{Endpoint: "https://approved.example/v1/embeddings", Name: "embed", Revision: "v1", Dimensions: 3}
	c := base
	c.Embedding = &e
	if c.executionRevision() != base.executionRevision() {
		t.Fatal("unused embedding changed pure-chat execution")
	}
	p := conversation.RetrievalPolicy{Space: knowledge.Space{ID: uuid.New(), Model: "embed", ModelRevision: "v1"}, Limit: 4}
	c.Retrieval = &p
	original := c.executionRevision()
	if original == base.executionRevision() {
		t.Fatal("retrieval not bound to execution")
	}
	for _, tc := range []struct {
		name    string
		change  func(*Config)
		valid   bool
		changed bool
	}{
		{"same", func(*Config) {}, true, false},
		{"transport", func(c *Config) { c.Mode = "webhook"; c.VerificationTokenEnv = "VERIFY"; c.EncryptKeyEnv = "ENCRYPT" }, true, false},
		{"space", func(c *Config) { c.Retrieval.Space.ID = uuid.New() }, true, true},
		{"limit", func(c *Config) { c.Retrieval.Limit = 5 }, true, true},
		{"endpoint", func(c *Config) { c.Embedding.Endpoint = "https://another-approved.example/v1/embeddings" }, true, true},
		{"dimension", func(c *Config) { c.Embedding.Dimensions = 4 }, true, true},
		{"credential binding", func(c *Config) { c.Embedding.APIKeyEnv = "EMBED_KEY" }, true, true},
		{"model mismatch", func(c *Config) { c.Embedding.Name = "other" }, false, true},
		{"revision mismatch", func(c *Config) { c.Embedding.Revision = "v2" }, false, true},
		{"missing embedder", func(c *Config) { c.Embedding = nil }, false, true},
		{"missing space", func(c *Config) { c.Retrieval.Space.ID = uuid.Nil }, false, true},
		{"unbounded retrieval", func(c *Config) { c.Retrieval.Limit = 51 }, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			test := c
			ep, policy := e, p
			test.Embedding, test.Retrieval = &ep, &policy
			tc.change(&test)
			raw, err := json.Marshal(test)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Parse(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("parse: %v", err)
			}
			if (test.executionRevision() != original) != tc.changed {
				t.Fatal("incorrect execution binding")
			}
		})
	}
}

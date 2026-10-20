package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/model"
)

func (c Config) executionRevision() string {
	// Omitted retrieval fields preserve the existing pure-chat fingerprint.
	// Credential values never enter a revision; the configured binding does.
	var embedding *model.EmbeddingConfig
	if c.Retrieval != nil {
		embedding = c.Embedding
	}
	raw, _ := json.Marshal(struct {
		Binding   feishu.Binding
		Model     model.Config
		Retrieval *conversation.RetrievalPolicy `json:",omitempty"`
		Embedding *model.EmbeddingConfig        `json:",omitempty"`
	}{c.Binding, c.Model, c.Retrieval, embedding})
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

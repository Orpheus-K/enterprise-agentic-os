package model

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EmbeddingConfig names the approved embedding endpoint and the exact space it produces.
// Revision is declared by the administrator because the API does not report one; it must match
// the knowledge.embedding_spaces row, so a silently upgraded remote model is a configuration change.
type EmbeddingConfig struct {
	Endpoint   string `json:"endpoint"`
	Name       string `json:"name"`
	Revision   string `json:"revision"`
	Dimensions int    `json:"dimensions"`
	APIKeyEnv  string `json:"api_key_env"`
	AllowHTTP  bool   `json:"allow_http"`
}

func (c EmbeddingConfig) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.AllowHTTP && u.Scheme == "http")) || !strings.HasSuffix(u.Path, "/embeddings") {
		return errors.New("embedding requires an approved embeddings URL; HTTP needs explicit approval")
	}
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.Revision) == "" || c.Dimensions < 1 || c.Dimensions > 4096 {
		return errors.New("embedding name, revision and dimensions (1..4096) required")
	}
	return nil
}

type Embedder struct {
	config EmbeddingConfig
	key    string
	http   *http.Client
}

func NewEmbedder(c EmbeddingConfig, key string) (*Embedder, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Embedder{c, key, &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("model redirects forbidden") }}}, nil
}

// Embed returns the vector for one text. A response that is not exactly one finite vector of the
// configured dimension fails; nothing is truncated, padded or averaged.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if len(text) == 0 || len(text) > 16000 {
		return nil, errors.New("embedding input outside text limit")
	}
	body, err := json.Marshal(struct {
		Model          string `json:"model"`
		Input          string `json:"input"`
		EncodingFormat string `json:"encoding_format"`
	}{e.config.Name, text, "float"})
	if err != nil {
		return nil, err
	}
	// Dimension-sized floats stay far below 1 MiB even at the 4096 cap.
	raw, err := post(ctx, e.http, e.config.Endpoint, e.key, body, 1<<20)
	if err != nil {
		return nil, err
	}
	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Data) != 1 || len(result.Data[0].Embedding) != e.config.Dimensions {
		return nil, errors.New("embedding response violates contract")
	}
	out := make([]float32, len(result.Data[0].Embedding))
	for i, v := range result.Data[0].Embedding {
		f := float32(v)
		if math.IsNaN(v) || math.IsInf(v, 0) || math.IsInf(float64(f), 0) {
			return nil, errors.New("embedding response violates contract")
		}
		out[i] = f
	}
	return out, nil
}

package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func embedder(t *testing.T, status int, body string, dims int) (*Embedder, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Model          string `json:"model"`
			Input          string `json:"input"`
			EncodingFormat string `json:"encoding_format"`
		}
		if r.URL.Path != "/v1/embeddings" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret-key" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "approved-embed" || request.Input != "query text" || request.EncodingFormat != "float" {
			t.Error("embedding request mismatch")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	e, err := NewEmbedder(EmbeddingConfig{Endpoint: server.URL + "/v1/embeddings", Name: "approved-embed", Revision: "r1", Dimensions: dims, AllowHTTP: true}, "secret-key")
	if err != nil {
		t.Fatal(err)
	}
	return e, &calls
}

func TestEmbedReturnsExactlyOneConfiguredVector(t *testing.T) {
	e, calls := embedder(t, 200, `{"data":[{"embedding":[0.5,-1,2]}]}`, 3)
	got, err := e.Embed(context.Background(), "query text")
	if err != nil || len(got) != 3 || got[0] != 0.5 || got[1] != -1 || got[2] != 2 || *calls != 1 {
		t.Fatal("embedding", got, err, *calls)
	}
}

func TestEmbedRejectsContractViolationsWithoutLeaking(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"short":       {200, `{"data":[{"embedding":[1,2]}]}`},
		"long":        {200, `{"data":[{"embedding":[1,2,3,4]}]}`},
		"two_items":   {200, `{"data":[{"embedding":[1,2,3]},{"embedding":[1,2,3]}]}`},
		"none":        {200, `{"data":[]}`},
		"overflow":    {200, `{"data":[{"embedding":[1e39,0,0]}]}`},
		"not_numbers": {200, `{"data":[{"embedding":["a","b","c"]}]}`},
		"invalid":     {200, `not json`},
		"oversized":   {200, strings.Repeat("x", 1<<20+1)},
		"limited":     {429, `secret error body`},
		"down":        {503, `secret error body`},
	} {
		e, _ := embedder(t, c.status, c.body, 3)
		got, err := e.Embed(context.Background(), "query text")
		if err == nil || got != nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "query text") {
			t.Errorf("%s: got %v %v", name, got, err)
		}
	}
}

func TestEmbedInputAndConfigBounds(t *testing.T) {
	e, calls := embedder(t, 200, `{"data":[{"embedding":[1,2,3]}]}`, 3)
	for _, text := range []string{"", strings.Repeat("x", 16001)} {
		if _, err := e.Embed(context.Background(), text); err == nil {
			t.Error("input outside limit accepted")
		}
	}
	if *calls != 0 {
		t.Fatal("oversized input reached the endpoint")
	}
	good := EmbeddingConfig{Endpoint: "https://models.example/v1/embeddings", Name: "m", Revision: "r", Dimensions: 3}
	for name, change := range map[string]func(*EmbeddingConfig){
		"http_unapproved": func(c *EmbeddingConfig) { c.Endpoint = "http://models.example/v1/embeddings" },
		"wrong_path":      func(c *EmbeddingConfig) { c.Endpoint = "https://models.example/v1/chat/completions" },
		"credentials":     func(c *EmbeddingConfig) { c.Endpoint = "https://u:p@models.example/v1/embeddings" },
		"query":           func(c *EmbeddingConfig) { c.Endpoint += "?x=1" },
		"no_revision":     func(c *EmbeddingConfig) { c.Revision = " " },
		"no_dimensions":   func(c *EmbeddingConfig) { c.Dimensions = 0 },
		"too_many_dims":   func(c *EmbeddingConfig) { c.Dimensions = 4097 },
	} {
		c := good
		change(&c)
		if _, err := NewEmbedder(c, ""); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := NewEmbedder(good, ""); err != nil {
		t.Fatal(err)
	}
}

package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTextModelContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"text", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"answer"}}]}`, 200, true},
		{"truncated", `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}]}`, 200, false},
		{"tool", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"answer","tool_calls":[{}]}}]}`, 200, false},
		{"function", `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"answer","function_call":{}}}]}`, 200, false},
		{"empty", `{"choices":[]}`, 200, false},
		{"invalid", `not json`, 200, false},
		{"oversized", strings.Repeat("x", 65537), 200, false},
		{"unavailable", `secret error body`, 503, false},
		{"rate limited", `secret error body`, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer secret-key" {
					t.Error("request target/auth mismatch")
				}
				var request struct {
					Model     string              `json:"model"`
					Stream    bool                `json:"stream"`
					MaxTokens int                 `json:"max_tokens"`
					Messages  []map[string]string `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "approved" || request.Stream || request.MaxTokens != 64 || len(request.Messages) != 1 || request.Messages[0]["content"] != "prompt" {
					t.Error("invalid text request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := New(Config{Endpoint: server.URL + "/v1/chat/completions", Name: "approved", MaxTokens: 64, AllowHTTP: true}, "secret-key")
			if err != nil {
				t.Fatal(err)
			}
			answer, err := client.Complete(context.Background(), "prompt")
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected result: %v", err)
			}
			if tc.ok && answer != "answer" {
				t.Fatal("lost text")
			}
			if err != nil && (strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "prompt")) {
				t.Fatal("error leaked body or credentials")
			}
			if calls.Load() != 1 {
				t.Fatal("unexpected retry")
			}
		})
	}
}

func TestRedirectAndCancellation(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	client, err := New(Config{Endpoint: source.URL + "/chat/completions", Name: "approved", MaxTokens: 64, AllowHTTP: true}, "key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), "prompt"); err == nil || leaked.Load() != 0 {
		t.Fatal("followed redirect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Complete(ctx, "prompt"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestRejectUnsafeModelConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://model/v1/chat/completions", "https://user:password@model/chat/completions", "https://model/chat/completions?token=secret", "https://model/other", "file:///chat/completions"} {
		if _, err := New(Config{Endpoint: endpoint, Name: "model", MaxTokens: 64}, ""); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}

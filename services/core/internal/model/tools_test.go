package model

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestToolTurnRejectsUnsafeResponses(t *testing.T) {
	for _, tc := range []struct {
		name, finish, role, content, tool, id, args string
		count                                       int
		reused, ok                                  bool
	}{
		{"valid", "tool_calls", "assistant", "", "price", "c1", `{"qty":2}`, 1, false, true},
		{"unknown", "tool_calls", "assistant", "", "delete", "c1", `{}`, 1, false, false},
		{"parallel", "tool_calls", "assistant", "", "price", "c1", `{}`, 2, false, false},
		{"truncated", "length", "assistant", "", "price", "c1", `{}`, 1, false, false},
		{"mixed", "tool_calls", "assistant", "answer", "price", "c1", `{}`, 1, false, false},
		{"role", "tool_calls", "system", "", "price", "c1", `{}`, 1, false, false},
		{"arguments", "tool_calls", "assistant", "", "price", "c1", `broken`, 1, false, false},
		{"reused", "tool_calls", "assistant", "", "price", "c1", `{}`, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []Message
					Parallel bool `json:"parallel_tool_calls"`
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Parallel || len(request.Messages) < 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" {
					t.Error("invalid role or tool policy")
				}
				calls := make([]ToolCall, tc.count)
				for i := range calls {
					calls[i] = ToolCall{ID: tc.id, Type: "function", Function: FunctionCall{Name: tc.tool, Arguments: tc.args}}
				}
				response := map[string]any{"choices": []any{map[string]any{"finish_reason": tc.finish, "message": map[string]any{"role": tc.role, "content": tc.content, "tool_calls": calls}}}}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			c, err := New(Config{Endpoint: server.URL + "/v1/chat/completions", Name: "synthetic", MaxTokens: 64, AllowHTTP: true}, "")
			if err != nil {
				t.Fatal(err)
			}
			var history []Message
			if tc.reused {
				history = []Message{{Role: "assistant", ToolCalls: []ToolCall{{ID: tc.id}}}}
			}
			_, err = c.Turn(t.Context(), "instructions", "synthetic", history, []Tool{{Name: "price", Parameters: json.RawMessage(`{"type":"object"}`)}})
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected validation: %v", err)
			}
		})
	}
}

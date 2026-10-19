package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type TurnResult struct {
	Text string
	Call *ToolCall
}

func (c *Client) Turn(ctx context.Context, instructions, text string, history []Message, tools []Tool) (TurnResult, error) {
	if instructions == "" || len(instructions) > 131072 || text == "" || len(text) > 16000 || len(history) > 8 || len(tools) > 32 {
		return TurnResult{}, errors.New("tool conversation exceeds limits")
	}
	messages := []Message{{Role: "system", Content: instructions}, {Role: "user", Content: text}}
	messages = append(messages, history...)
	type function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	type definition struct {
		Type     string   `json:"type"`
		Function function `json:"function"`
	}
	definitions := []definition{}
	names := map[string]bool{}
	for _, t := range tools {
		if t.Name == "" || names[t.Name] || !json.Valid(t.Parameters) {
			return TurnResult{}, errors.New("invalid model tool definitions")
		}
		names[t.Name] = true
		definitions = append(definitions, definition{"function", function{t.Name, t.Description, t.Parameters}})
	}
	body, err := json.Marshal(struct {
		Model     string       `json:"model"`
		Messages  []Message    `json:"messages"`
		Tools     []definition `json:"tools,omitempty"`
		Parallel  bool         `json:"parallel_tool_calls"`
		MaxTokens int          `json:"max_tokens"`
		Stream    bool         `json:"stream"`
	}{c.config.Name, messages, definitions, false, c.config.MaxTokens, false})
	if err != nil || len(body) > 1048576 {
		return TurnResult{}, errors.New("model tool request invalid or oversized")
	}
	raw, err := c.request(ctx, body)
	if err != nil {
		return TurnResult{}, err
	}
	var result struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Role         string          `json:"role"`
				Content      string          `json:"content"`
				ToolCalls    []ToolCall      `json:"tool_calls"`
				Refusal      *string         `json:"refusal"`
				FunctionCall json.RawMessage `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Choices) != 1 {
		return TurnResult{}, errors.New("invalid model tool response")
	}
	choice := result.Choices[0]
	m := choice.Message
	if m.Role != "assistant" || m.Refusal != nil || (len(m.FunctionCall) > 0 && string(m.FunctionCall) != "null") {
		return TurnResult{}, errors.New("unsupported model response")
	}
	if choice.Finish == "stop" && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" && len(m.Content) <= 16000 {
		return TurnResult{Text: m.Content}, nil
	}
	if choice.Finish != "tool_calls" || len(m.ToolCalls) != 1 || strings.TrimSpace(m.Content) != "" {
		return TurnResult{}, errors.New("expected one bounded tool call")
	}
	call := m.ToolCalls[0]
	if call.Type != "function" || call.ID == "" || len(call.ID) > 128 || !names[call.Function.Name] || len(call.Function.Arguments) > 16384 || !json.Valid([]byte(call.Function.Arguments)) {
		return TurnResult{}, errors.New("unapproved model tool call")
	}
	for _, message := range history {
		for _, prior := range message.ToolCalls {
			if prior.ID == call.ID {
				return TurnResult{}, errors.New("reused model tool call id")
			}
		}
	}
	return TurnResult{Call: &call}, nil
}

// Package model implements the approved non-streaming chat-completions contract.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Endpoint  string `json:"endpoint"`
	Name      string `json:"name"`
	APIKeyEnv string `json:"api_key_env"`
	MaxTokens int    `json:"max_tokens"`
	AllowHTTP bool   `json:"allow_http"`
}

func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.AllowHTTP && u.Scheme == "http")) || !strings.HasSuffix(u.Path, "/chat/completions") {
		return errors.New("model requires an approved chat-completions URL; HTTP needs explicit approval")
	}
	if strings.TrimSpace(c.Name) == "" || c.MaxTokens < 1 || c.MaxTokens > 4096 {
		return errors.New("model name and max_tokens (1..4096) required")
	}
	return nil
}

type Client struct {
	config Config
	key    string
	http   *http.Client
}

func New(c Config, key string) (*Client, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Client{c, key, &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("model redirects forbidden") }}}, nil
}

func (c *Client) Complete(ctx context.Context, text string) (string, error) {
	return c.complete(ctx, "", text)
}
func (c *Client) CompleteSkill(ctx context.Context, instructions, text string) (string, error) {
	if strings.TrimSpace(instructions) == "" || len(instructions) > 131072 {
		return "", errors.New("invalid skill instructions")
	}
	return c.complete(ctx, instructions, text)
}
func (c *Client) complete(ctx context.Context, instructions, text string) (string, error) {
	if len(text) == 0 || len(text) > 16000 {
		return "", errors.New("model input outside text limit")
	}
	messages := []map[string]string{}
	if instructions != "" {
		messages = append(messages, map[string]string{"role": "system", "content": instructions})
	}
	messages = append(messages, map[string]string{"role": "user", "content": text})
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		Messages  []map[string]string `json:"messages"`
		MaxTokens int                 `json:"max_tokens"`
		Stream    bool                `json:"stream"`
	}{c.config.Name, messages, c.config.MaxTokens, false})
	if err != nil {
		return "", err
	}
	raw, err := c.request(ctx, body)
	if err != nil {
		return "", err
	}
	var result struct {
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message struct {
				Role         string          `json:"role"`
				Content      string          `json:"content"`
				ToolCalls    json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage `json:"function_call"`
				Refusal      *string         `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Choices) != 1 {
		return "", errors.New("model response violates text contract")
	}
	choice := result.Choices[0]
	if choice.Finish != "stop" || choice.Message.Role != "assistant" || strings.TrimSpace(choice.Message.Content) == "" || len(choice.Message.Content) > 16000 || choice.Message.Refusal != nil || (len(choice.Message.ToolCalls) > 0 && string(choice.Message.ToolCalls) != "null" && string(choice.Message.ToolCalls) != "[]") || (len(choice.Message.FunctionCall) > 0 && string(choice.Message.FunctionCall) != "null") {
		return "", errors.New("model returned unsupported or incomplete content")
	}
	return choice.Message.Content, nil
}

func (c *Client) request(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid model request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("model transport failed; result may be unknown")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model HTTP status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, errors.New("model response unreadable or oversized")
	}
	return raw, nil
}

package toolgateway

import (
	"encoding/json"
	"testing"
)

func TestRejectUnboundedTargetsAndSchemas(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	base := Spec{Name: "test", RemoteName: "test", Endpoint: "https://tools.example/mcp", Description: "test", Owner: "test", Risk: "read", TimeoutSeconds: 2, InputSchema: schema, OutputSchema: schema}
	if _, err := compile(base); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://tools.example/mcp", "https://user:password@tools.example/mcp", "https://tools.example/mcp?token=secret", "https://tools.example/mcp#fragment", "file:///host"} {
		s := base
		s.Endpoint = endpoint
		if _, err := compile(s); err == nil {
			t.Fatal("unbounded target accepted")
		}
	}
	for _, raw := range []string{`{}`, `{"type":"object"}`, `{"type":"object","additionalProperties":false,"properties":{"item":{"$ref":"https://untrusted.example/schema"}}}`} {
		s := base
		s.InputSchema = json.RawMessage(raw)
		if _, err := compile(s); err == nil {
			t.Fatal("unbounded schema accepted")
		}
	}
}

package bootstrap

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/model"
	"encoding/json"
	"github.com/google/uuid"
	"testing"
)

func TestConfigurationFailsClosed(t *testing.T) {
	valid := Config{Binding: feishu.Binding{TenantID: uuid.New(), AppID: "cli_test", TenantKey: "tenant"}, Mode: "websocket", DatabaseURLEnv: "DB_URL", AppSecretEnv: "APP_SECRET", Model: model.Config{Endpoint: "https://approved.example/v1/chat/completions", Name: "approved", MaxTokens: 64}}
	for _, tc := range []struct {
		name   string
		change func(*Config)
		valid  bool
	}{
		{"tools without skills", func(c *Config) { c.Tools = &ToolsConfig{} }, false},
		{"approval without callback", func(c *Config) { c.Approval = &conversation.ApprovalPolicy{MemberID: uuid.New(), TTLSeconds: 60} }, false},
		{"approval invalid member", func(c *Config) {
			c.Operations = &OperationsConfig{TokenEnv: "OPS_TOKEN", Subject: "approver"}
			c.Approval = &conversation.ApprovalPolicy{TTLSeconds: 60}
		}, false},
		{"approval invalid expiry", func(c *Config) {
			c.Operations = &OperationsConfig{TokenEnv: "OPS_TOKEN", Subject: "approver"}
			c.Approval = &conversation.ApprovalPolicy{MemberID: uuid.New(), TTLSeconds: 604801}
		}, false},
		{"approval enabled", func(c *Config) {
			c.Operations = &OperationsConfig{TokenEnv: "OPS_TOKEN", Subject: "approver"}
			c.Approval = &conversation.ApprovalPolicy{MemberID: uuid.New(), TTLSeconds: 3600}
		}, true},
		{"valid", func(*Config) {}, true},
		{"mode", func(c *Config) { c.Mode = "auto" }, false},
		{"tenant", func(c *Config) { c.Binding.TenantID = uuid.Nil }, false},
		{"webhook secrets", func(c *Config) { c.Mode = "webhook" }, false},
		{"database", func(c *Config) { c.DatabaseURLEnv = "" }, false},
		{"model", func(c *Config) { c.Model.Name = "" }, false},
		{"operations secret", func(c *Config) { c.Operations = &OperationsConfig{Subject: "auditor"} }, false},
		{"operations identity", func(c *Config) { c.Operations = &OperationsConfig{TokenEnv: "OPS_TOKEN", Subject: " "} }, false},
		{"operations enabled", func(c *Config) { c.Operations = &OperationsConfig{TokenEnv: "OPS_TOKEN", Subject: "auditor"} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.change(&c)
			raw, _ := json.Marshal(c)
			_, err := Parse(raw)
			if (err == nil) != tc.valid {
				t.Fatalf("configuration result: %v", err)
			}
		})
	}
	raw, _ := json.Marshal(valid)
	if _, err := Parse(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("accepted trailing data")
	}
	if _, err := Parse([]byte(`{"unknown":true}`)); err == nil {
		t.Fatal("accepted unknown configuration")
	}
}

//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/skills"
	"agentos.local/core/internal/toolgateway"
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type toolLoopFixture struct {
	*chainFixture
	worker                       *conversation.Worker
	registry                     *skills.Registry
	ref                          skills.Ref
	spec                         toolgateway.Spec
	credentials                  *toolgateway.Credentials
	modelCalls, toolCalls, sends atomic.Int32
}

func toolLoop(t *testing.T, repeat bool) *toolLoopFixture {
	t.Helper()
	f := &toolLoopFixture{chainFixture: chain(t)}
	sdk := mcp.NewServer(&mcp.Implementation{Name: "synthetic", Version: "1"}, nil)
	type Input struct {
		Qty int `json:"qty"`
	}
	type Output struct {
		Total string `json:"total"`
	}
	mcp.AddTool(sdk, &mcp.Tool{Name: "price"}, func(context.Context, *mcp.CallToolRequest, Input) (*mcp.CallToolResult, Output, error) {
		f.toolCalls.Add(1)
		return nil, Output{"200"}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	schema := json.RawMessage(`{"type":"object","properties":{"qty":{"type":"integer"}},"required":["qty"],"additionalProperties":false}`)
	output := json.RawMessage(`{"type":"object","properties":{"total":{"type":"string"}},"required":["total"],"additionalProperties":false}`)
	f.spec = toolgateway.Spec{Name: "price", RemoteName: "price", Endpoint: server.URL, Owner: "test", Description: "Synthetic price", Risk: "read", TimeoutSeconds: 3, AllowLocalHTTP: true, InputSchema: schema, OutputSchema: output}
	revision, err := toolgateway.Revision(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	manifest := skills.Manifest{Format: 1, Name: "quote", Publisher: "test", Summary: "quote", Trigger: "explicit", InputContract: "quantity", OutputContract: "draft", FailurePolicy: "stop", Tools: []skills.ToolRef{{Name: "price", Revision: revision}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	instructions := []byte("# Synthetic quote. Use price before producing a draft.")
	f.ref = skills.Ref{Name: "quote", Revision: skills.Revision(raw, instructions)}
	f.registry, err = skills.New([]skills.Bundle{{Manifest: raw, Instructions: instructions, ExpectedRevision: f.ref.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ kind, name, revision string }{{"skill", "quote", f.ref.Revision}, {"tool", "price", revision}} {
		loopSQL(t, f, `INSERT INTO iam.asset_grants(tenant_id,member_id,app_id,kind,name,revision) VALUES($1,$2,$3,$4,$5,$6)`, testBinding().TenantID, f.member, testBinding().AppID, entry.kind, entry.name, entry.revision)
	}
	t.Setenv("AGENTOS_LOOP_TOKEN", "synthetic-loop-credential-only")
	f.credentials, err = toolgateway.NewCredentials([]toolgateway.CredentialBinding{{Principal: identity.Principal{TenantID: testBinding().TenantID, MemberID: f.member}, Endpoint: server.URL, TokenEnv: "AGENTOS_LOOP_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.modelCalls.Add(1)
		var request struct {
			Messages []model.Message
			Tools    []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(request.Tools) != 1 {
			t.Error("tool contract not exposed")
		}
		hasResult := false
		for _, message := range request.Messages {
			if message.Role == "tool" {
				hasResult = true
				if !strings.Contains(message.Content, "200") {
					t.Error("stored tool result missing")
				}
			}
		}
		if !hasResult || repeat {
			response := map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []model.ToolCall{{ID: "call_" + strconv.Itoa(int(n)), Type: "function", Function: model.FunctionCall{Name: "price", Arguments: `{"qty":2}`}}}}}}}
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Error(err)
			}
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"synthetic quote 200"}}]}`))
	}))
	t.Cleanup(provider.Close)
	client, err := model.New(model.Config{Endpoint: provider.URL + "/v1/chat/completions", Name: "synthetic", AllowHTTP: true, MaxTokens: 128}, "")
	if err != nil {
		t.Fatal(err)
	}
	f.worker = &conversation.Worker{Model: client, Logger: f.logger, Telemetry: f.adapter.telemetry, Sender: senderFunc(func(context.Context, string, string, string) error { f.sends.Add(1); return nil })}
	f.bind(t, f.db)
	if err = f.store.Enqueue(t.Context(), skillMessage()); err != nil {
		t.Fatal(err)
	}
	return f
}

func loopSQL(t *testing.T, f *toolLoopFixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.pg.Admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *toolLoopFixture) bind(t *testing.T, db *database.DB) {
	t.Helper()
	var err error
	f.store, err = conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision").WithSkills(f.registry, f.ref)
	if err != nil {
		t.Fatal(err)
	}
	f.worker.Store = f.store
	f.worker.Tools, err = toolgateway.New(db, testBinding().TenantID, testBinding().AppID, f.registry, []toolgateway.Spec{f.spec}, f.credentials)
	if err != nil {
		t.Fatal(err)
	}
}
func stepLoop(t *testing.T, f *toolLoopFixture, want string) {
	t.Helper()
	worked, err := f.worker.ProcessOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("step: %v %v", worked, err)
	}
	f.state(t, want)
}

func TestToolLoopPersistsSelectionResultAndResponse(t *testing.T) {
	f := toolLoop(t, false)
	stepLoop(t, f, "tool_pending")
	stepLoop(t, f, "pending")
	stepLoop(t, f, "succeeded")
	if f.modelCalls.Load() != 2 || f.toolCalls.Load() != 1 || f.sends.Load() != 1 {
		t.Fatal("wrong side effect counts")
	}
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || worked {
		t.Fatal("completed loop repeated")
	}
}

func TestToolResultRestoresAfterCheckpointFailureAndRestart(t *testing.T) {
	f := toolLoop(t, false)
	stepLoop(t, f, "tool_pending")
	loopSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT reject_checkpoint CHECK(reason<>'tool_checkpoint') NOT VALID")
	if _, err := f.worker.ProcessOne(t.Context()); err == nil {
		t.Fatal("checkpoint audit failure hidden")
	}
	f.state(t, "tool_running")
	loopSQL(t, f, "ALTER TABLE channel.audit DROP CONSTRAINT reject_checkpoint")
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	f.bind(t, db)
	expireLease(t, f.chainFixture)
	stepLoop(t, f, "pending")
	stepLoop(t, f, "succeeded")
	if f.toolCalls.Load() != 1 || f.modelCalls.Load() != 2 || f.sends.Load() != 1 {
		t.Fatal("saved tool result was re-executed")
	}
}

func TestToolIntentWithoutResultStopsUnknown(t *testing.T) {
	f := toolLoop(t, false)
	stepLoop(t, f, "tool_pending")
	task, err := f.store.Claim(t.Context())
	if err != nil || task == nil {
		t.Fatal(err)
	}
	if err = f.store.Transition(t.Context(), task, conversation.ToolRunning, "", ""); err != nil {
		t.Fatal(err)
	}
	expireLease(t, f.chainFixture)
	stepLoop(t, f, "unknown")
	if f.toolCalls.Load() != 0 || f.sends.Load() != 0 {
		t.Fatal("unknown intent blindly executed")
	}
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || worked {
		t.Fatal("unknown task reclaimed")
	}
}

func TestToolHistoryRevocationAndBudget(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		f := toolLoop(t, false)
		stepLoop(t, f, "tool_pending")
		stepLoop(t, f, "pending")
		loopSQL(t, f, "UPDATE iam.asset_grants SET enabled=false WHERE kind='tool'")
		stepLoop(t, f, "failed")
		if f.modelCalls.Load() != 1 || f.sends.Load() != 0 {
			t.Fatal("revoked history sent to model")
		}
	})
	t.Run("bounded", func(t *testing.T) {
		f := toolLoop(t, true)
		for range 4 {
			stepLoop(t, f, "tool_pending")
			stepLoop(t, f, "pending")
		}
		stepLoop(t, f, "failed")
		if f.toolCalls.Load() != 4 || f.sends.Load() != 0 {
			t.Fatal("unbounded tool execution")
		}
	})
}

func TestToolCheckpointFencesOldOwner(t *testing.T) {
	f := toolLoop(t, false)
	stepLoop(t, f, "tool_pending")
	task, err := f.store.Claim(t.Context())
	if err != nil || task == nil {
		t.Fatal(err)
	}
	if err = f.store.Transition(t.Context(), task, conversation.ToolRunning, "", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := f.worker.Tools.Call(t.Context(), task.Message.Identity, task.Tool.Operation, task.Skill, task.Tool.Ref, json.RawMessage(task.Tool.Call.Function.Arguments))
	if err != nil {
		t.Fatal(err)
	}
	old := f.store
	expireLease(t, f.chainFixture)
	f.bind(t, f.db)
	stepLoop(t, f, "pending")
	if err = old.CompleteTool(t.Context(), task, raw); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("late owner committed", err)
	}
}

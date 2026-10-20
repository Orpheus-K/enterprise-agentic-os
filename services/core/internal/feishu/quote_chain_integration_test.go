//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/quotation"
	"agentos.local/core/internal/skills"
	"agentos.local/core/internal/toolgateway"
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQuoteMessageToolApprovalAmendDelivery(t *testing.T) {
	for _, mode := range []string{"normal", "checkpoint_failure", "model_bypass", "revoked", "boundary", "retrieval", "retrieval_revoked_amend"} {
		t.Run(mode, func(t *testing.T) {
			f := approvalEnvironment(t)
			var calls, models atomic.Int32
			sdk := mcp.NewServer(&mcp.Implementation{Name: "synthetic-quotation-source", Version: "1"}, nil)
			type Input struct {
				Quantity  int64 `json:"quantity"`
				UnitMinor int64 `json:"unit_minor"`
			}
			mcp.AddTool(sdk, &mcp.Tool{Name: "quote_data"}, func(_ context.Context, _ *mcp.CallToolRequest, in Input) (*mcp.CallToolResult, quotation.ToolInput, error) {
				calls.Add(1)
				return nil, quotation.ToolInput{
					Request:   quotation.Request{Customer: "synthetic-authorized-customer", Evidence: []string{"synthetic-minute-v1"}, Items: []quotation.Item{{SKU: "test", Quantity: in.Quantity, UnitMinor: in.UnitMinor}}},
					Catalogue: quotation.Catalogue{Revision: strings.Repeat("a", 64), Currency: "CNY", Basis: "net_no_fees", Prices: map[string]int64{"test": 10000}},
					Policy:    quotation.Policy{Revision: strings.Repeat("b", 64), Currency: "CNY", Basis: "net_no_fees", ApprovalBelowBPS: 8000},
				}, nil
			})
			remote := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
			t.Cleanup(remote.Close)
			spec := toolgateway.Spec{Name: "quote_data", RemoteName: "quote_data", Endpoint: remote.URL, Description: "Authorized synthetic catalogue and proposal", Owner: "test", Risk: "read", TimeoutSeconds: 5, AllowLocalHTTP: true,
				InputSchema:  json.RawMessage(`{"type":"object","properties":{"quantity":{"type":"integer","minimum":1},"unit_minor":{"type":"integer","minimum":1}},"required":["quantity","unit_minor"],"additionalProperties":false}`),
				OutputSchema: json.RawMessage(`{"type":"object","properties":{"request":{"type":"object"},"catalogue":{"type":"object"},"policy":{"type":"object"}},"required":["request","catalogue","policy"],"additionalProperties":false}`)}
			toolRevision, err := toolgateway.Revision(spec)
			if err != nil {
				t.Fatal(err)
			}
			manifest := skills.Manifest{Format: 1, Name: "quote", Publisher: "test", Summary: "Synthetic quotation", Trigger: "quotation request", InputContract: "quantity and proposed price", OutputContract: "verified draft", FailurePolicy: "stop", Tools: []skills.ToolRef{{Name: spec.Name, Revision: toolRevision}}, QuotationTool: spec.Name}
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			instructions := []byte("Use the authorized quote data tool. Do not invent catalogue prices.")
			ref := skills.Ref{Name: "quote", Revision: skills.Revision(raw, instructions)}
			registry, err := skills.New([]skills.Bundle{{Manifest: raw, Instructions: instructions, ExpectedRevision: ref.Revision}})
			if err != nil {
				t.Fatal(err)
			}
			grantSkill(t, f.chainFixture, ref)
			approvalSQL(t, f, `INSERT INTO iam.asset_grants(tenant_id,member_id,app_id,kind,name,revision) VALUES($1,$2,$3,'tool',$4,$5)`, testBinding().TenantID, f.member, testBinding().AppID, spec.Name, toolRevision)
			f.store, err = f.store.WithSkills(registry, ref)
			if err != nil {
				t.Fatal(err)
			}
			var retrieval *retrievalFixture
			if strings.HasPrefix(mode, "retrieval") {
				retrieval = addRetrieval(t, f.chainFixture, f.approver)
			}
			t.Setenv("QUOTE_CHAIN_TOKEN", "synthetic-only-token")
			creds, err := toolgateway.NewCredentials([]toolgateway.CredentialBinding{{Principal: identity.Principal{TenantID: testBinding().TenantID, MemberID: f.member}, Endpoint: remote.URL, TokenEnv: "QUOTE_CHAIN_TOKEN"}})
			if err != nil {
				t.Fatal(err)
			}
			gateway, err := toolgateway.New(f.db, testBinding().TenantID, testBinding().AppID, registry, []toolgateway.Spec{spec}, creds)
			if err != nil {
				t.Fatal(err)
			}
			prompt := "Create a draft for synthetic-authorized-customer, test product, quantity 2, unit price 75.00 CNY."
			if mode == "boundary" {
				prompt = strings.ReplaceAll(prompt, "75.00", "80.00")
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Messages []model.Message }
				if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 2 || request.Messages[1].Content != prompt {
					t.Error("quotation message not passed to model")
				}
				if retrieval != nil && (len(request.Messages) == 0 || !strings.Contains(request.Messages[0].Content, "Authorized evidence 42") || strings.Contains(request.Messages[0].Content, "Forbidden")) {
					t.Error("quotation model did not receive authorized evidence")
				}
				models.Add(1)
				body := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"quote-call","type":"function","function":{"name":"quote_data","arguments":"{\"quantity\":2,\"unit_minor\":7500}"}}]}}]}`
				if mode == "model_bypass" {
					body = `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Invented approved quote"}}]}`
				}
				if mode == "boundary" {
					body = strings.ReplaceAll(body, "7500", "8000")
				}
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(provider.Close)
			client, err := model.New(model.Config{Endpoint: provider.URL + "/v1/chat/completions", Name: "synthetic", MaxTokens: 128, AllowHTTP: true}, "")
			if err != nil {
				t.Fatal(err)
			}
			f.worker.Store = f.store
			f.worker.Tools = gateway
			f.worker.Model = client
			f.adapter = testAdapter(t, f.store)
			body := eventBody(t, func(e *envelope) {
				raw, err := json.Marshal(map[string]string{"text": prompt})
				if err != nil {
					t.Fatal(err)
				}
				e.Event.Message.Content = string(raw)
			})
			if webhookStatus(t, f.adapter, encryptedRequest(t, body)) != 200 {
				t.Fatal("quotation message not accepted")
			}
			if err = f.pg.Admin.QueryRow(t.Context(), "SELECT id FROM channel.tasks").Scan(&f.task); err != nil {
				t.Fatal(err)
			}
			step := func(want string) {
				t.Helper()
				if worked, err := f.worker.ProcessOne(t.Context()); err != nil || !worked {
					t.Fatal("worker", err)
				}
				f.state(t, want)
			}
			if mode == "model_bypass" {
				step("failed")
				if calls.Load() != 0 || f.sends.Load() != 0 {
					t.Fatal("bypass called tool or sent")
				}
				return
			}
			step("tool_pending")
			if mode == "boundary" {
				f.worker.Sender = senderFunc(func(_ context.Context, _, _, text string) error {
					if !strings.Contains(text, "报价总额：160.00") || !strings.Contains(text, "未触发本条特批") {
						t.Error("threshold quote incorrect")
					}
					f.sends.Add(1)
					return nil
				})
				step("succeeded")
				var approvals int
				if err = f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.approvals").Scan(&approvals); err != nil || approvals != 0 || f.sends.Load() != 1 {
					t.Fatal("boundary triggered discount approval", err)
				}
				return
			}
			if mode == "revoked" {
				approvalSQL(t, f, "UPDATE iam.asset_grants SET enabled=false WHERE kind='tool'")
				step("failed")
				if calls.Load() != 0 {
					t.Fatal("revoked tool called")
				}
				return
			}
			if mode == "checkpoint_failure" {
				approvalSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT block_quote CHECK(reason<>'quotation_saved') NOT VALID")
				if _, err = f.worker.ProcessOne(t.Context()); err == nil {
					t.Fatal("save failure hidden")
				}
				f.state(t, "tool_running")
				approvalSQL(t, f, "ALTER TABLE channel.audit DROP CONSTRAINT block_quote")
				expireLease(t, f.chainFixture)
			}
			step("waiting_approval")
			a, err := f.store.InspectApproval(t.Context(), f.actor, f.task)
			if err != nil {
				t.Fatal(err)
			}
			var draft quotation.Draft
			if json.Unmarshal([]byte(a.Response), &draft) != nil || draft.TotalMinor != 15000 || !draft.RequiresApproval {
				t.Fatal("wrong computed quote")
			}
			edit := quoteEdit(a, draft)
			edit.Changes[0].UnitMinor = 8500
			if mode == "retrieval_revoked_amend" {
				retrieval.revoke(t)
				if out, err := f.store.AmendQuotation(t.Context(), f.actor, edit); !errors.Is(err, identitypg.ErrForbidden) || out.Response != "" {
					t.Fatal("amendment ignored revoked knowledge", err)
				}
				f.state(t, "waiting_approval")
				return
			}
			changed, err := f.store.AmendQuotation(t.Context(), f.actor, edit)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.DecideApproval(t.Context(), f.actor, decision(a)); !errors.Is(err, conversation.ErrApprovalConflict) {
				t.Fatal("old card accepted", err)
			}
			f.worker.Sender = senderFunc(func(_ context.Context, _, _, text string) error {
				if !strings.Contains(text, "报价初稿 · V2") || !strings.Contains(text, "报价总额：170.00") || !strings.Contains(text, "未用章") {
					t.Error("wrong final version")
				}
				if retrieval != nil && (!strings.Contains(text, "Authorized source") || !strings.Contains(text, "paragraph:1")) {
					t.Error("quotation omitted grounded sources")
				}
				f.sends.Add(1)
				return nil
			})
			if _, err = f.store.DecideApproval(t.Context(), f.actor, decision(changed)); err != nil {
				t.Fatal(err)
			}
			step("succeeded")
			if calls.Load() != 1 || models.Load() != 1 || f.sends.Load() != 1 {
				t.Fatal("duplicate external work", calls.Load(), models.Load(), f.sends.Load())
			}
			if retrieval != nil && retrieval.embeds != 1 {
				t.Fatal("quotation repeated embedding")
			}
		})
	}
}

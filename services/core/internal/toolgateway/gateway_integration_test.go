//go:build integration

package toolgateway

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/skills"
	"agentos.local/core/internal/testpg"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const syntheticToken = "synthetic-mcp-credential-for-isolation-only"

func TestSavedResultRequiresExactIntentAndCurrentGrant(t *testing.T) {
	f := gatewaySetup(t, "ok")
	id := uuid.New()
	args := json.RawMessage(`{"qty":2}`)
	if _, err := f.gateway.Call(t.Context(), f.actor, id, f.skill, f.tool, args); err != nil {
		t.Fatal(err)
	}
	if raw, err := f.gateway.Result(t.Context(), f.actor, id, f.skill, f.tool, args); err != nil || !strings.Contains(string(raw), "200") {
		t.Fatal("saved result missing", err)
	}
	if _, err := f.gateway.Result(t.Context(), f.actor, id, f.skill, f.tool, json.RawMessage(`{"qty":3}`)); !errors.Is(err, ErrUnknown) {
		t.Fatal("changed intent read result", err)
	}
	gatewaySQL(t, f, "UPDATE iam.asset_grants SET enabled=false WHERE kind='tool'")
	if _, err := f.gateway.Result(t.Context(), f.actor, id, f.skill, f.tool, args); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked grant read result", err)
	}
	if f.requests.Load() != 1 {
		t.Fatal("result retrieval called remote")
	}
}

type gatewayFixture struct {
	pg          *testpg.Fixture
	db          *database.DB
	gateway     *Gateway
	actor       identity.ExternalIdentity
	principal   identity.Principal
	spec        Spec
	skill       skills.Ref
	tool        skills.ToolRef
	registry    *skills.Registry
	credentials *Credentials
	requests    atomic.Int32
}

func gatewaySQL(t *testing.T, f *gatewayFixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.pg.Admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func gatewaySetup(t *testing.T, mode string) *gatewayFixture {
	t.Helper()
	f := &gatewayFixture{pg: testpg.New(t)}
	m, err := database.NewMigrator(t.Context(), f.pg.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Up(t.Context())
	if closeErr := m.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	f.principal = identity.Principal{TenantID: uuid.New(), MemberID: uuid.New()}
	f.actor = identity.ExternalIdentity{TenantID: f.principal.TenantID, Provider: "operator", Issuer: "synthetic", Subject: "tester"}
	gatewaySQL(t, f, "INSERT INTO iam.tenants(id,name) VALUES($1,'synthetic')", f.principal.TenantID)
	gatewaySQL(t, f, "INSERT INTO iam.members(tenant_id,id,employee_id) VALUES($1,$2,'synthetic')", f.principal.TenantID, f.principal.MemberID)
	gatewaySQL(t, f, "INSERT INTO iam.identity_links VALUES($1,$2,$3,$4,$5,true)", f.actor.TenantID, f.actor.Provider, f.actor.Issuer, f.actor.Subject, f.principal.MemberID)
	sdk := mcp.NewServer(&mcp.Implementation{Name: "synthetic", Version: "1"}, nil)
	type Input struct {
		Qty int `json:"qty"`
	}
	type Output struct {
		Total string `json:"total"`
	}
	mcp.AddTool(sdk, &mcp.Tool{Name: "price"}, func(_ context.Context, _ *mcp.CallToolRequest, in Input) (*mcp.CallToolResult, Output, error) {
		if mode == "oversize" {
			return nil, Output{strings.Repeat("x", 300000)}, nil
		}
		if mode == "echo" {
			return nil, Output{syntheticToken}, nil
		}
		if mode == "revoke" {
			gatewaySQL(t, f, "UPDATE iam.asset_grants SET enabled=false WHERE kind='tool'")
		}
		return nil, Output{strconv.Itoa(in.Qty * 100)}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+syntheticToken {
			t.Error("target credential not injected")
			w.WriteHeader(401)
			return
		}
		if mode == "redirect" {
			http.Redirect(w, r, "http://127.0.0.1:1/forbidden", 307)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var message struct{ Method string }
		if json.Unmarshal(raw, &message) != nil {
			t.Error("invalid SDK request")
			return
		}
		if message.Method == "tools/call" {
			f.requests.Add(1)
			if mode == "drop" {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				if err = connection.Close(); err != nil {
					t.Error(err)
				}
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	f.spec = Spec{Name: "price", RemoteName: "price", Endpoint: server.URL, Description: "Synthetic price", Owner: "test", Risk: "read", TimeoutSeconds: 3, AllowLocalHTTP: true,
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"qty":{"type":"integer","minimum":1,"maximum":100}},"required":["qty"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"total":{"type":"string"}},"required":["total"],"additionalProperties":false}`)}
	if mode == "write" {
		f.spec.Risk = "write"
	}
	revision, err := Revision(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	f.tool = skills.ToolRef{Name: f.spec.Name, Revision: revision}
	manifest := skills.Manifest{Format: 1, Name: "quote", Publisher: "test", Summary: "Synthetic quote", Trigger: "Explicit request", InputContract: "quantity", OutputContract: "draft", FailurePolicy: "stop", Tools: []skills.ToolRef{f.tool}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	instructions := []byte("# synthetic quote")
	f.skill = skills.Ref{Name: "quote", Revision: skills.Revision(raw, instructions)}
	f.registry, err = skills.New([]skills.Bundle{{Manifest: raw, Instructions: instructions, ExpectedRevision: f.skill.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []struct{ kind, name, revision string }{{"skill", f.skill.Name, f.skill.Revision}, {"tool", f.tool.Name, f.tool.Revision}} {
		gatewaySQL(t, f, `INSERT INTO iam.asset_grants(tenant_id,member_id,app_id,kind,name,revision) VALUES($1,$2,'app',$3,$4,$5)`, f.principal.TenantID, f.principal.MemberID, grant.kind, grant.name, grant.revision)
	}
	t.Setenv("AGENTOS_MCP_TEST_TOKEN", syntheticToken)
	f.credentials, err = NewCredentials([]CredentialBinding{{f.principal, f.spec.Endpoint, "AGENTOS_MCP_TEST_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	f.db, err = database.Open(t.Context(), f.pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.db.Close)
	f.gateway, err = New(f.db, f.principal.TenantID, "app", f.registry, []Spec{f.spec}, f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *gatewayFixture) call(ctx context.Context, id uuid.UUID) (json.RawMessage, error) {
	return f.gateway.Call(ctx, f.actor, id, f.skill, f.tool, json.RawMessage(`{"qty":2}`))
}

func TestMCPCallContractCredentialIsolationAndDuplicate(t *testing.T) {
	f := gatewaySetup(t, "")
	definitions, definitionErr := f.gateway.Definitions(t.Context(), f.actor, f.skill)
	if definitionErr != nil || len(definitions) != 1 {
		t.Fatal("tool disclosure failed", definitionErr)
	}
	description, definitionErr := json.Marshal(definitions)
	if definitionErr != nil || bytes.Contains(description, []byte(f.spec.Endpoint)) || bytes.Contains(description, []byte(syntheticToken)) {
		t.Fatal("tool metadata exposed endpoint or credential")
	}
	id := uuid.New()
	raw, err := f.call(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"total":"200"}` || strings.Contains(string(raw), syntheticToken) {
		t.Fatal("invalid tool output")
	}
	if _, err = f.call(t.Context(), id); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("duplicate %v", err)
	}
	if f.requests.Load() != 1 {
		t.Fatal("duplicate external call")
	}
	var state, input, output string
	if err = f.pg.Admin.QueryRow(t.Context(), "SELECT state,input_hash,output_hash FROM channel.tool_operations WHERE id=$1", id).Scan(&state, &input, &output); err != nil || state != "succeeded" || len(input) != 64 || len(output) != 64 {
		t.Fatal("audit missing", err)
	}
	var count int
	if err = f.db.Transaction(t.Context(), func(q database.Queries) error {
		return q.QueryRow(t.Context(), "SELECT count(*) FROM channel.tool_operations").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatal("tenant leaked through pool")
	}
}

func TestMCPRevocationAndIsolation(t *testing.T) {
	for _, kind := range []string{"skill", "tool", "mapping", "member", "tenant", "other_app", "other_tenant", "credential"} {
		t.Run(kind, func(t *testing.T) {
			f := gatewaySetup(t, "")
			switch kind {
			case "skill", "tool":
				gatewaySQL(t, f, "UPDATE iam.asset_grants SET enabled=false WHERE kind=$1", kind)
			case "mapping":
				gatewaySQL(t, f, "UPDATE iam.identity_links SET enabled=false")
			case "member":
				gatewaySQL(t, f, "UPDATE iam.members SET enabled=false")
			case "tenant":
				gatewaySQL(t, f, "UPDATE iam.tenants SET enabled=false")
			case "other_app":
				var err error
				f.gateway, err = New(f.db, f.principal.TenantID, "other", f.registry, []Spec{f.spec}, f.credentials)
				if err != nil {
					t.Fatal(err)
				}
			case "other_tenant":
				f.actor.TenantID = uuid.New()
			case "credential":
				var err error
				f.credentials, err = NewCredentials(nil)
				if err != nil {
					t.Fatal(err)
				}
				f.gateway, err = New(f.db, f.principal.TenantID, "app", f.registry, []Spec{f.spec}, f.credentials)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.call(t.Context(), uuid.New()); err == nil {
				t.Fatal("unauthorized invocation")
			}
			if f.requests.Load() != 0 {
				t.Fatal("unauthorized server request")
			}
		})
	}
}

func TestMCPUnknownResultNoRetryAndSecretEcho(t *testing.T) {
	for _, mode := range []string{"drop", "echo", "redirect", "revoke", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			f := gatewaySetup(t, mode)
			id := uuid.New()
			raw, err := f.call(t.Context(), id)
			if err == nil || raw != nil || strings.Contains(err.Error(), syntheticToken) {
				t.Fatal("unsafe result exposed")
			}
			if f.requests.Load() > 1 {
				t.Fatal("SDK replayed tool call")
			}
			if mode == "drop" && !errors.Is(err, ErrUnknown) {
				t.Fatalf("unknown lost: %v", err)
			}
			if mode == "echo" && !errors.Is(err, ErrRejected) {
				t.Fatal("credential echoed")
			}
			if mode == "revoke" && !errors.Is(err, ErrDenied) {
				t.Fatal("revoked result exposed")
			}
			if mode != "revoke" {
				if _, err = f.call(t.Context(), id); !errors.Is(err, ErrAlreadyStarted) {
					t.Fatal("uncertain operation repeated", err)
				}
			}
		})
	}
}

func TestMCPConcurrentOperationAndAuditFailure(t *testing.T) {
	f := gatewaySetup(t, "")
	id := uuid.New()
	var success atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			_, err := f.call(t.Context(), id)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrAlreadyStarted) {
				t.Errorf("call %v", err)
			}
		})
	}
	wg.Wait()
	if success.Load() != 1 || f.requests.Load() != 1 {
		t.Fatal("operation id did not fence calls")
	}
	gatewaySQL(t, f, "ALTER TABLE channel.tool_operations ADD CONSTRAINT reject_result CHECK(state<>'succeeded') NOT VALID")
	raw, err := f.call(t.Context(), uuid.New())
	if !errors.Is(err, ErrAudit) || raw != nil {
		t.Fatal("result returned without final audit")
	}
	gatewaySQL(t, f, "ALTER TABLE channel.tool_operations ADD CONSTRAINT reject_start CHECK(state<>'started') NOT VALID")
	before := f.requests.Load()
	if _, err = f.call(t.Context(), uuid.New()); !errors.Is(err, ErrAudit) {
		t.Fatal("start audit failure hidden")
	}
	if f.requests.Load() != before {
		t.Fatal("called despite failed intent")
	}
}

func TestMCPRejectUnapprovedArgumentsAndWrites(t *testing.T) {
	f := gatewaySetup(t, "")
	for _, args := range []string{`{"qty":0}`, `{"qty":2,"url":"https://other.example"}`, `{"qty":"two"}`, `null`, `{} {}`} {
		if _, err := f.gateway.Call(t.Context(), f.actor, uuid.New(), f.skill, f.tool, json.RawMessage(args)); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid tool arguments accepted", err)
		}
	}
	wrong := f.tool
	wrong.Revision = strings.Repeat("a", 64)
	if _, err := f.gateway.Call(t.Context(), f.actor, uuid.New(), f.skill, wrong, json.RawMessage(`{"qty":2}`)); !errors.Is(err, ErrDenied) {
		t.Fatal("unregistered version accepted")
	}
	if f.requests.Load() != 0 {
		t.Fatal("rejected request reached tool")
	}
	writer := gatewaySetup(t, "write")
	if _, err := writer.call(t.Context(), uuid.New()); !errors.Is(err, ErrWriteRestricted) {
		t.Fatal("unapproved write called")
	}
	if writer.requests.Load() != 0 {
		t.Fatal("write reached server")
	}
}

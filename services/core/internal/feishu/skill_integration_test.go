//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/skills"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func skillBundles(t *testing.T) (*skills.Registry, skills.Ref, skills.Ref) {
	t.Helper()
	raw, err := os.ReadFile("../../../../examples/skills/synthetic-quotation/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("# version-one synthetic instructions")
	second := []byte("# version-two synthetic instructions")
	a := skills.Bundle{Manifest: raw, Instructions: first, ExpectedRevision: skills.Revision(raw, first)}
	b := skills.Bundle{Manifest: raw, Instructions: second, ExpectedRevision: skills.Revision(raw, second)}
	r, err := skills.New([]skills.Bundle{a, b})
	if err != nil {
		t.Fatal(err)
	}
	return r, skills.Ref{Name: "synthetic-quotation", Revision: a.ExpectedRevision}, skills.Ref{Name: "synthetic-quotation", Revision: b.ExpectedRevision}
}
func grantSkill(t *testing.T, f *chainFixture, ref skills.Ref) {
	t.Helper()
	if _, err := f.pg.Admin.Exec(t.Context(), `INSERT INTO iam.asset_grants(tenant_id,member_id,app_id,kind,name,revision) VALUES($1,$2,$3,'skill',$4,$5)`, testBinding().TenantID, f.member, testBinding().AppID, ref.Name, ref.Revision); err != nil {
		t.Fatal(err)
	}
}
func skillMessage() conversation.Message {
	return conversation.Message{EventID: uuid.NewString(), MessageID: uuid.NewString(), Text: "synthetic user input", Fingerprint: "synthetic", Identity: identity.ExternalIdentity{TenantID: testBinding().TenantID, Provider: "feishu", Issuer: testBinding().Issuer(), Subject: "ou_user"}}
}
func TestTaskSkillVersionsSurviveConfigurationChangeAndRestart(t *testing.T) {
	f := chain(t)
	registry, v1, v2 := skillBundles(t)
	grantSkill(t, f, v1)
	grantSkill(t, f, v2)
	first, err := f.store.WithSkills(registry, v1)
	if err != nil {
		t.Fatal(err)
	}
	message := skillMessage()
	if err = first.Enqueue(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	task, err := first.Claim(t.Context())
	if err != nil || task == nil || task.Skill != v1 {
		t.Fatal("task did not pin v1", err)
	}
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	restarted, err := conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision").WithSkills(registry, v2)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Enqueue(t.Context(), message); err != nil {
		t.Fatal("replay failed", err)
	}
	if err = restarted.Enqueue(t.Context(), skillMessage()); err != nil {
		t.Fatal(err)
	}
	expireLease(t, f)
	seen := map[string]int{}
	var seenMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Messages) != 2 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" || request.Messages[1].Content != "synthetic user input" {
			t.Error("role boundary lost")
		}
		seenMu.Lock()
		defer seenMu.Unlock()
		text := request.Messages[0].Content
		if strings.Contains(text, "version-one") {
			seen["v1"]++
		} else if strings.Contains(text, "version-two") {
			seen["v2"]++
		} else {
			t.Error("missing fixed instructions")
		}
		if !strings.Contains(text, "failure_policy") {
			t.Error("skill contract missing")
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"synthetic draft"}}]}`))
	}))
	defer server.Close()
	client, err := model.New(model.Config{Endpoint: server.URL + "/v1/chat/completions", Name: "synthetic", AllowHTTP: true, MaxTokens: 64}, "")
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	worker := conversation.Worker{Store: restarted, Model: client, Logger: f.logger, Telemetry: f.adapter.telemetry, Sender: senderFunc(func(context.Context, string, string, string) error { sends++; return nil })}
	for range 2 {
		if worked, err := worker.ProcessOne(t.Context()); err != nil || !worked {
			t.Fatalf("execution %v", err)
		}
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if seen["v1"] != 1 || seen["v2"] != 1 || sends != 2 {
		t.Fatalf("version drift: %v sends %d", seen, sends)
	}
	if worked, err := worker.ProcessOne(t.Context()); err != nil || worked {
		t.Fatal("duplicate task")
	}
}
func TestSkillRevocationAndMissingRevisionStopExecution(t *testing.T) {
	for _, scenario := range []string{"revoked_before_model", "revoked_before_send", "configuration_removed"} {
		t.Run(scenario, func(t *testing.T) {
			f := chain(t)
			registry, v1, _ := skillBundles(t)
			grantSkill(t, f, v1)
			store, err := f.store.WithSkills(registry, v1)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Enqueue(t.Context(), skillMessage()); err != nil {
				t.Fatal(err)
			}
			revoke := func() {
				t.Helper()
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.asset_grants SET enabled=false"); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			sends := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if scenario == "revoked_before_send" {
					revoke()
				}
				_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"synthetic draft"}}]}`))
			}))
			defer server.Close()
			client, err := model.New(model.Config{Endpoint: server.URL + "/v1/chat/completions", Name: "synthetic", AllowHTTP: true, MaxTokens: 64}, "")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "revoked_before_model" {
				revoke()
			}
			if scenario == "configuration_removed" {
				store = conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "approved-revision")
			}
			worker := conversation.Worker{Store: store, Model: client, Logger: f.logger, Telemetry: f.adapter.telemetry, Sender: senderFunc(func(context.Context, string, string, string) error { sends++; return nil })}
			if _, err = worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.state(t, "failed")
			want := 0
			if scenario == "revoked_before_send" {
				want = 1
			}
			if sends != 0 || int(calls.Load()) != want {
				t.Fatal("unauthorized side effect")
			}
		})
	}
}
func TestUnapprovedSelectedSkillCannotEnqueue(t *testing.T) {
	f := chain(t)
	registry, v1, v2 := skillBundles(t)
	store, err := f.store.WithSkills(registry, v1)
	if err != nil {
		t.Fatal(err)
	}
	message := skillMessage()
	if err = store.Enqueue(t.Context(), message); err == nil {
		t.Fatal("unapproved skill queued")
	}
	var count int
	if err = f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.tasks").Scan(&count); err != nil || count != 0 {
		t.Fatal("denied enqueue persisted")
	}
	grantSkill(t, f, v1)
	if err = store.Enqueue(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	changed, err := f.store.WithSkills(registry, v2)
	if err != nil {
		t.Fatal(err)
	}
	if err = changed.Enqueue(t.Context(), message); err != nil {
		t.Fatal("existing replay used new policy", err)
	}
	if err = changed.Enqueue(t.Context(), skillMessage()); err == nil {
		t.Fatal("new version inherited authorization")
	}
}

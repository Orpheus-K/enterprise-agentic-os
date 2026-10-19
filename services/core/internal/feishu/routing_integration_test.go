//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/skills"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type routingModel struct {
	call func(string, string) (string, error)
}

func (m routingModel) Complete(context.Context, string) (string, error) {
	return "", errors.New("unexpected unskilled model call")
}
func (m routingModel) CompleteSkill(_ context.Context, instructions, text string) (string, error) {
	return m.call(instructions, text)
}

func TestSkillRoutePinsVersionBeforeLoadingInstructions(t *testing.T) {
	f := chain(t)
	registry, v1, v2 := skillBundles(t)
	grantSkill(t, f, v1)
	grantSkill(t, f, v2)
	store, err := f.store.WithRoutes(registry, []skills.Ref{v1})
	if err != nil {
		t.Fatal(err)
	}
	message := skillMessage()
	if err = store.Enqueue(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	// New deployment still contains the old asset, but advertises V2 to new tasks.
	store, err = f.store.WithRoutes(registry, []skills.Ref{v2})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Enqueue(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	calls, sends := 0, 0
	w := &conversation.Worker{Store: store, Logger: f.logger, Telemetry: f.adapter.telemetry,
		Model: routingModel{call: func(instructions, text string) (string, error) {
			calls++
			if text != message.Text {
				t.Error("user role changed")
			}
			if calls == 1 {
				if strings.Contains(instructions, "version-one synthetic instructions") || strings.Contains(instructions, v2.Revision) || !strings.Contains(instructions, v1.Revision) {
					t.Error("routing disclosed SOP or changed candidates")
				}
				raw, _ := json.Marshal(v1)
				return string(raw), nil
			}
			if !strings.Contains(instructions, "version-one synthetic instructions") {
				t.Error("pinned SOP not loaded")
			}
			return "synthetic draft", nil
		}}, Sender: senderFunc(func(context.Context, string, string, string) error { sends++; return nil })}
	if worked, err := w.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	f.state(t, "pending")
	// Reconstruct worker/store after the selected checkpoint; do not reroute.
	w.Store, err = conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "approved-revision").WithRoutes(registry, []skills.Ref{v2})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := w.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	f.state(t, "succeeded")
	if calls != 2 || sends != 1 {
		t.Fatal("selection repeated or delivery missing")
	}
}

func TestSkillRouteRejectsUnlistedRevokedAndMalformedSelection(t *testing.T) {
	for _, mode := range []string{"unlisted", "revoked", "malformed", "no_match", "identity_revoked", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := chain(t)
			registry, v1, v2 := skillBundles(t)
			grantSkill(t, f, v1)
			grantSkill(t, f, v2)
			store, err := f.store.WithRoutes(registry, []skills.Ref{v1})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Enqueue(t.Context(), skillMessage()); err != nil {
				t.Fatal(err)
			}
			w := &conversation.Worker{Store: store, Logger: f.logger, Telemetry: f.adapter.telemetry,
				Model: routingModel{call: func(string, string) (string, error) {
					ref := v1
					switch mode {
					case "unlisted":
						ref = v2
					case "revoked":
						if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.asset_grants SET enabled=false"); err != nil {
							t.Fatal(err)
						}
					case "identity_revoked":
						if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false"); err != nil {
							t.Fatal(err)
						}
					case "malformed":
						return `{"name":"synthetic-quotation","extra":true}`, nil
					case "no_match":
						return "null", nil
					case "unknown":
						return "", errors.New("synthetic transport failure")
					}
					raw, _ := json.Marshal(ref)
					return string(raw), nil
				}}, Sender: senderFunc(func(context.Context, string, string, string) error { t.Error("denied route delivered"); return nil })}
			if _, err = w.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if mode == "unknown" {
				want = "unknown"
			}
			f.state(t, want)
			if worked, err := w.ProcessOne(t.Context()); err != nil || worked {
				t.Fatal("failed routing retried")
			}
		})
	}
}

func TestSkillRouteCheckpointFailureRollsBackBinding(t *testing.T) {
	f := chain(t)
	registry, v1, _ := skillBundles(t)
	grantSkill(t, f, v1)
	store, err := f.store.WithRoutes(registry, []skills.Ref{v1})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Enqueue(t.Context(), skillMessage()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit ADD CONSTRAINT reject_route CHECK(reason<>'skill_selected') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	w := &conversation.Worker{Store: store, Logger: f.logger, Telemetry: f.adapter.telemetry, Model: routingModel{call: func(string, string) (string, error) { raw, _ := json.Marshal(v1); return string(raw), nil }}}
	if _, err = w.ProcessOne(t.Context()); err == nil {
		t.Fatal("route audit failure hidden")
	}
	var unbound bool
	if err = f.pg.Admin.QueryRow(t.Context(), "SELECT skill_name IS NULL AND state='model_running' FROM channel.tasks").Scan(&unbound); err != nil || !unbound {
		t.Fatal("partial route commit", err)
	}
	expireLease(t, f)
	if _, err = w.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.state(t, "unknown")
}

func TestSkillRouteRequiresCurrentGrantAtEnqueueAndResume(t *testing.T) {
	f := chain(t)
	registry, v1, _ := skillBundles(t)
	store, err := f.store.WithRoutes(registry, []skills.Ref{v1})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Enqueue(t.Context(), skillMessage()); !errors.Is(err, conversation.ErrSkillDenied) {
		t.Fatal("ungranted route enqueued", err)
	}
	grantSkill(t, f, v1)
	if err = store.Enqueue(t.Context(), skillMessage()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.Admin.Exec(t.Context(), "UPDATE iam.asset_grants SET enabled=false"); err != nil {
		t.Fatal(err)
	}
	w := &conversation.Worker{Store: store, Logger: f.logger, Telemetry: f.adapter.telemetry, Model: routingModel{call: func(string, string) (string, error) { t.Error("revoked summary sent to model"); return "null", nil }}}
	if _, err = w.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.state(t, "failed")
}

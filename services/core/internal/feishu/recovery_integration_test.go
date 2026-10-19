//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"context"
	"errors"
	"github.com/google/uuid"
	"sync"
	"sync/atomic"
	"testing"
)

func expireLease(t *testing.T, f *chainFixture) {
	t.Helper()
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE channel.tasks SET lease_until=clock_timestamp()-interval '1 second' WHERE lease_until IS NOT NULL"); err != nil {
		t.Fatal(err)
	}
}
func anotherStore(f *chainFixture) *conversation.Store {
	return conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "approved-revision")
}
func mustClaim(t *testing.T, s *conversation.Store) *conversation.Task {
	t.Helper()
	task, err := s.Claim(t.Context())
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	return task
}
func step(t *testing.T, s *conversation.Store, task *conversation.Task, to conversation.State, response string) {
	t.Helper()
	if err := s.Transition(t.Context(), task, to, "", response); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredCheckpointFencesOldOwner(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	old := mustClaim(t, f.store)
	if err := f.store.Renew(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	expireLease(t, f)
	if err := f.store.Renew(t.Context(), old); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatalf("expired renewal: %v", err)
	}
	if err := f.store.Transition(t.Context(), old, conversation.ModelRunning, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatalf("expired transition: %v", err)
	}
	s := anotherStore(f)
	fresh := mustClaim(t, s)
	if fresh.Generation != old.Generation+1 || fresh.Version <= old.Version {
		t.Fatal("fence did not advance")
	}
	if err := f.store.Transition(t.Context(), old, conversation.ModelRunning, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("old owner committed")
	}
	stale := *fresh
	step(t, s, fresh, conversation.ModelRunning, "")
	if err := s.Transition(t.Context(), &stale, conversation.ModelRunning, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("old step version committed")
	}
	other := conversation.NewStore(f.db, testBinding().TenantID, "different-app", "approved-revision")
	if err := other.Transition(t.Context(), fresh, conversation.Unknown, "", ""); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("cross-installation transition")
	}
	step(t, s, fresh, conversation.DeliveryPending, "saved")
	if err := s.Transition(t.Context(), fresh, conversation.Succeeded, "", ""); !errors.Is(err, conversation.ErrInvalidTransition) {
		t.Fatal("skipped delivery intent")
	}
}

func TestCheckpointResumeUsesSavedResponseAndCurrentIdentity(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "revoked"}[revoked], func(t *testing.T) {
			f := chain(t)
			enqueue(t, f)
			task := mustClaim(t, f.store)
			step(t, f.store, task, conversation.ModelRunning, "")
			step(t, f.store, task, conversation.DeliveryPending, "persisted answer")
			expireLease(t, f)
			if revoked {
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false"); err != nil {
					t.Fatal(err)
				}
			}
			modelCalls, replies := 0, 0
			// Reopen a pool to exercise restoration from PostgreSQL, not retained Go task state.
			db, err := database.Open(t.Context(), f.pg.RuntimeURL, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			w := conversation.Worker{Store: conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision"), Logger: f.logger, Telemetry: f.adapter.telemetry,
				Model: modelFunc(func(context.Context, string) (string, error) { modelCalls++; return "unexpected", nil }), Sender: senderFunc(func(_ context.Context, _ string, id, text string) error {
					replies++
					if text != "persisted answer" || id != task.ID.String() {
						t.Error("checkpoint/operation key changed")
					}
					return nil
				})}
			if worked, err := w.ProcessOne(t.Context()); err != nil || !worked {
				t.Fatalf("resume: %v", err)
			}
			if modelCalls != 0 {
				t.Fatal("model repeated")
			}
			if revoked {
				f.state(t, "failed")
				if replies != 0 {
					t.Fatal("revocation bypassed")
				}
			} else {
				f.state(t, "succeeded")
				if replies != 1 {
					t.Fatal("reply count")
				}
			}
			if worked, err := w.ProcessOne(t.Context()); err != nil || worked {
				t.Fatal("terminal task replayed")
			}
		})
	}
}

func TestExpiredExternalIntentRequiresReconciliation(t *testing.T) {
	for _, phase := range []conversation.State{conversation.ModelRunning, conversation.DeliveryRunning} {
		t.Run(string(phase), func(t *testing.T) {
			f := chain(t)
			enqueue(t, f)
			old := mustClaim(t, f.store)
			step(t, f.store, old, conversation.ModelRunning, "")
			if phase == conversation.DeliveryRunning {
				step(t, f.store, old, conversation.DeliveryPending, "saved")
				step(t, f.store, old, phase, "")
			}
			expireLease(t, f)
			s := anotherStore(f)
			if err := s.RecoverExpired(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.state(t, "unknown")
			if task, err := s.Claim(t.Context()); err != nil || task != nil {
				t.Fatal("unknown external effect reclaimed")
			}
			target := conversation.DeliveryPending
			response := "late"
			if phase == conversation.DeliveryRunning {
				target = conversation.Succeeded
				response = ""
			}
			if err := f.store.Transition(t.Context(), old, target, "", response); !errors.Is(err, conversation.ErrLeaseLost) {
				t.Fatal("late external result overwrote recovery")
			}
			if err := s.RecoverExpired(t.Context()); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit WHERE state='unknown'").Scan(&count); err != nil || count != 1 {
				t.Fatal("repeated recovery event")
			}
		})
	}
}

func TestIndependentWorkersReclaimOnce(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	mustClaim(t, f.store)
	expireLease(t, f)
	var count atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			task, err := anotherStore(f).Claim(t.Context())
			if err != nil {
				t.Error(err)
			}
			if task != nil {
				count.Add(1)
			}
		})
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("claims=%d", count.Load())
	}
}

func TestLeaseTransitionAndRecoveryAuditAtomicity(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	task := mustClaim(t, f.store)
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit ADD CONSTRAINT reject_intent CHECK(state <> 'model_running')"); err != nil {
		t.Fatal(err)
	}
	before := task.Version
	if err := f.store.Transition(t.Context(), task, conversation.ModelRunning, "", ""); err == nil {
		t.Fatal("partial intent committed")
	}
	f.state(t, "claimed")
	if task.Version != before {
		t.Fatal("memory version advanced on rollback")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit DROP CONSTRAINT reject_intent"); err != nil {
		t.Fatal(err)
	}
	step(t, f.store, task, conversation.ModelRunning, "")
	expireLease(t, f)
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit ADD CONSTRAINT reject_recovery CHECK(state <> 'unknown')"); err != nil {
		t.Fatal(err)
	}
	if err := anotherStore(f).RecoverExpired(t.Context()); err == nil {
		t.Fatal("partial recovery committed")
	}
	f.state(t, "model_running")
}

func TestExpiredWorkStaysInInstallationRevision(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	task := mustClaim(t, f.store)
	step(t, f.store, task, conversation.ModelRunning, "")
	expireLease(t, f)
	for _, s := range []*conversation.Store{conversation.NewStore(f.db, uuid.New(), testBinding().AppID, "approved-revision"), conversation.NewStore(f.db, testBinding().TenantID, "other", "approved-revision"), conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "new-revision")} {
		if err := s.RecoverExpired(t.Context()); err != nil {
			t.Fatal(err)
		}
		if task, err := s.Claim(t.Context()); err != nil || task != nil {
			t.Fatal("foreign revision claimed")
		}
	}
	f.state(t, "model_running")
}

func TestWorkerDiscardsLateExternalCompletion(t *testing.T) {
	for _, phase := range []string{"model", "delivery"} {
		t.Run(phase, func(t *testing.T) {
			f := chain(t)
			enqueue(t, f)
			models, replies := 0, 0
			expire := func() {
				expireLease(t, f)
				if err := anotherStore(f).RecoverExpired(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			w := conversation.Worker{Store: f.store, Logger: f.logger, Telemetry: f.adapter.telemetry,
				Model: modelFunc(func(context.Context, string) (string, error) {
					models++
					if phase == "model" {
						expire()
					}
					return "late result", nil
				}),
				Sender: senderFunc(func(context.Context, string, string, string) error { replies++; expire(); return nil })}
			if worked, err := w.ProcessOne(t.Context()); err != nil || !worked {
				t.Fatalf("late completion: %v", err)
			}
			f.state(t, "unknown")
			if models != 1 || (phase == "model" && replies != 0) || (phase == "delivery" && replies != 1) {
				t.Fatal("unexpected external calls")
			}
			if worked, err := w.ProcessOne(t.Context()); err != nil || worked {
				t.Fatal("late completion retried")
			}
		})
	}
}

func TestClaimedCheckpointResumesAfterExpiry(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	mustClaim(t, f.store)
	expireLease(t, f)
	models, replies := 0, 0
	w := conversation.Worker{Store: anotherStore(f), Logger: f.logger, Telemetry: f.adapter.telemetry, Model: modelFunc(func(context.Context, string) (string, error) { models++; return "answer", nil }), Sender: senderFunc(func(context.Context, string, string, string) error { replies++; return nil })}
	if worked, err := w.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	f.state(t, "succeeded")
	if models != 1 || replies != 1 {
		t.Fatal("safe step not resumed once")
	}
}

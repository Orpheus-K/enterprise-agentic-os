//go:build integration

package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

type embedFunc func(context.Context, string) ([]float32, error)

func (f embedFunc) Embed(ctx context.Context, text string) ([]float32, error) { return f(ctx, text) }

type groundedModel func(context.Context, string, string) (string, error)

func (f groundedModel) Complete(context.Context, string) (string, error) {
	return "", errors.New("retrieval bypassed grounded model")
}
func (f groundedModel) CompleteSkill(ctx context.Context, instructions, text string) (string, error) {
	return f(ctx, instructions, text)
}

type retrievalFixture struct {
	*chainFixture
	k                 *knowledge.Store
	actor             identity.ExternalIdentity
	content           knowledge.Content
	acl               knowledge.ACL
	policy            conversation.RetrievalPolicy
	embeds            int
	beforeEmbedReturn func()
}

func addRetrieval(t *testing.T, f *chainFixture, extraReaders ...uuid.UUID) *retrievalFixture {
	t.Helper()
	binding := testBinding()
	syncMember, source := uuid.New(), uuid.New()
	r := &retrievalFixture{chainFixture: f}
	r.actor = identity.ExternalIdentity{TenantID: binding.TenantID, Provider: "synthetic", Issuer: "retrieval-test", Subject: "sync"}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO iam.members VALUES($1,$2,'retrieval-sync',true)", []any{binding.TenantID, syncMember}},
		{"INSERT INTO iam.identity_links VALUES($1,$2,$3,$4,$5,true)", []any{binding.TenantID, r.actor.Provider, r.actor.Issuer, r.actor.Subject, syncMember}},
		{"INSERT INTO knowledge.sources VALUES($1,$2,$3,$4,true)", []any{binding.TenantID, binding.AppID, source, syncMember}},
	} {
		if _, err := f.pg.Admin.Exec(t.Context(), statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	space := knowledge.Space{ID: uuid.New(), Model: "synthetic-embedding", ModelRevision: "v1"}
	if _, err := f.pg.Admin.Exec(t.Context(), "INSERT INTO knowledge.embedding_spaces VALUES($1,$2,$3,$4,$5,3,true)", binding.TenantID, binding.AppID, space.ID, space.Model, space.ModelRevision); err != nil {
		t.Fatal(err)
	}
	var err error
	r.k, err = knowledge.New(f.db, binding.TenantID, binding.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.k.UpdateHealth(t.Context(), r.actor, knowledge.HealthUpdate{SourceID: source, Revision: 1, State: "healthy", ValidUntil: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.content = knowledge.Content{SourceID: source, DocumentID: "allowed", Revision: 1, Title: "Authorized source", Chunks: []knowledge.Chunk{{ID: "p1", Body: "Authorized evidence 42", Anchor: "paragraph:1"}}}
	r.acl = knowledge.ACL{SourceID: source, DocumentID: "allowed", Revision: 1, ContentRevision: 1, SourceEpoch: 1, Allow: append([]uuid.UUID{f.member}, extraReaders...), ValidUntil: time.Now().Add(time.Hour)}
	if err = r.k.PutContent(t.Context(), r.actor, r.content); err != nil {
		t.Fatal(err)
	}
	if err = r.k.PutACL(t.Context(), r.actor, r.acl); err != nil {
		t.Fatal(err)
	}
	if err = r.k.PublishVectors(t.Context(), r.actor, knowledge.VectorSet{Space: space, SourceID: source, DocumentID: "allowed", ContentRevision: 1, Vectors: []knowledge.ChunkVector{{ChunkID: "p1", Embedding: []float32{1, 1, 0}}}}); err != nil {
		t.Fatal(err)
	}
	// A nearer, unauthorized document must not displace the permitted candidate.
	secret := r.content
	secret.DocumentID, secret.Title = "secret", "Forbidden title"
	secret.Chunks = []knowledge.Chunk{{ID: "p1", Body: "Forbidden body", Anchor: "secret:1"}}
	acl := r.acl
	acl.DocumentID, acl.Allow = "secret", []uuid.UUID{syncMember}
	if err = r.k.PutContent(t.Context(), r.actor, secret); err != nil {
		t.Fatal(err)
	}
	if err = r.k.PutACL(t.Context(), r.actor, acl); err != nil {
		t.Fatal(err)
	}
	if err = r.k.PublishVectors(t.Context(), r.actor, knowledge.VectorSet{Space: space, SourceID: source, DocumentID: "secret", ContentRevision: 1, Vectors: []knowledge.ChunkVector{{ChunkID: "p1", Embedding: []float32{1, 0, 0}}}}); err != nil {
		t.Fatal(err)
	}
	r.policy = conversation.RetrievalPolicy{Space: space, Limit: 1}
	f.store, err = f.store.WithRetrieval(r.policy, embedFunc(func(context.Context, string) ([]float32, error) {
		r.embeds++
		if r.beforeEmbedReturn != nil {
			r.beforeEmbedReturn()
		}
		return []float32{1, 0, 0}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	f.adapter = testAdapter(t, f.store)
	return r
}

func (r *retrievalFixture) revoke(t *testing.T) {
	t.Helper()
	r.acl.Revision++
	r.acl.Allow = nil
	if err := r.k.PutACL(t.Context(), r.actor, r.acl); err != nil {
		t.Fatal(err)
	}
}

func TestRetrievalConversationAndRevocation(t *testing.T) {
	for _, mode := range []string{"success", "identity_after_embedding", "acl_after_model", "content_after_model", "health_after_model", "read_audit_failure", "empty_results"} {
		t.Run(mode, func(t *testing.T) {
			r := addRetrieval(t, chain(t))
			if mode == "empty_results" {
				r.revoke(t)
			}
			if mode == "identity_after_embedding" {
				r.beforeEmbedReturn = func() {
					if _, err := r.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", r.member); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "read_audit_failure" {
				if _, err := r.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit ADD CONSTRAINT synthetic_read_failure CHECK(action<>'read') NOT VALID"); err != nil {
					t.Fatal(err)
				}
			}
			enqueue(t, r.chainFixture)
			models, sends := 0, 0
			w := conversation.Worker{Store: r.store, Logger: r.logger, Telemetry: r.adapter.telemetry,
				Model: groundedModel(func(_ context.Context, instructions, text string) (string, error) {
					models++
					if strings.Contains(instructions, "Forbidden") {
						t.Error("unauthorized evidence reached model")
					}
					if mode != "empty_results" && !strings.Contains(instructions, "Authorized evidence 42") {
						t.Error("missing evidence")
					}
					if mode == "empty_results" && strings.Contains(instructions, "Authorized evidence") {
						t.Error("revoked evidence in empty result")
					}
					switch mode {
					case "acl_after_model":
						r.revoke(t)
					case "content_after_model":
						r.content.Revision++
						if err := r.k.PutContent(t.Context(), r.actor, r.content); err != nil {
							t.Fatal(err)
						}
					case "health_after_model":
						if _, err := r.k.UpdateHealth(t.Context(), r.actor, knowledge.HealthUpdate{SourceID: r.content.SourceID, Revision: 2, State: "unavailable", ValidUntil: time.Now().Add(time.Hour)}); err != nil {
							t.Fatal(err)
						}
					}
					return "Answer [1]", nil
				}),
				Sender: senderFunc(func(_ context.Context, _, _, text string) error {
					sends++
					if mode == "success" && (!strings.Contains(text, "Authorized source") || !strings.Contains(text, "paragraph:1")) {
						t.Error("missing authoritative source footer")
					}
					if strings.Contains(text, "Forbidden") {
						t.Error("forbidden content sent")
					}
					return nil
				})}
			_, err := w.ProcessOne(t.Context())
			if mode == "read_audit_failure" {
				if err == nil || models != 0 || sends != 0 {
					t.Fatalf("audit failure released output: %v %d %d", err, models, sends)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantModels, wantSends := 1, 0
			if mode == "identity_after_embedding" {
				wantModels = 0
			}
			if mode == "success" || mode == "empty_results" {
				wantSends = 1
			}
			if r.embeds != 1 || models != wantModels || sends != wantSends {
				t.Fatalf("calls embed/model/send: %d/%d/%d", r.embeds, models, sends)
			}
			if wantSends == 1 {
				r.state(t, "succeeded")
			} else {
				r.state(t, "failed")
			}
		})
	}
}

func TestRetrievalDeliveryRecoveryRechecksSavedEvidence(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "revoked"}[revoked], func(t *testing.T) {
			r := addRetrieval(t, chain(t))
			enqueue(t, r.chainFixture)
			task := mustClaim(t, r.store)
			step(t, r.store, task, conversation.ModelRunning, "")
			refs := []knowledge.Reference{{SourceID: r.content.SourceID, DocumentID: "allowed", ChunkID: "p1", ContentRevision: 1, ACLRevision: 1}}
			if err := r.store.SaveKnowledgeReferences(t.Context(), task, refs); err != nil {
				t.Fatal(err)
			}
			step(t, r.store, task, conversation.DeliveryPending, "Saved grounded answer")
			expireLease(t, r.chainFixture)
			if revoked {
				r.revoke(t)
			}
			db, err := database.Open(t.Context(), r.pg.RuntimeURL, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			// Recovery must check persisted references even when no embedder is installed.
			s := conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
			sends := 0
			w := conversation.Worker{Store: s, Logger: r.logger, Telemetry: r.adapter.telemetry, Model: modelFunc(func(context.Context, string) (string, error) { t.Error("replayed model"); return "", nil }),
				Sender: senderFunc(func(context.Context, string, string, string) error { sends++; return nil })}
			if _, err := w.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if r.embeds != 0 || sends != map[bool]int{false: 1, true: 0}[revoked] {
				t.Fatal("recovery repeated provider call or bypassed ACL")
			}
		})
	}
}

func TestRetrievalApprovalRecipientAndNotification(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "recipient_denied", true: "recipient_allowed"}[allowed], func(t *testing.T) {
			f := approvalEnvironment(t)
			approvalSQL(t, f, "INSERT INTO iam.identity_links VALUES($1,'feishu',$2,'ou_approver',$3,true)", testBinding().TenantID, testBinding().Issuer(), f.approver)
			var err error
			f.store, err = f.store.WithApproval(conversation.ApprovalPolicy{MemberID: f.approver, TTLSeconds: 3600, NotifySubject: "ou_approver"})
			if err != nil {
				t.Fatal(err)
			}
			readers := []uuid.UUID{}
			if allowed {
				readers = append(readers, f.approver)
			}
			r := addRetrieval(t, f.chainFixture, readers...)
			f.worker.Store = r.store
			f.worker.Model = groundedModel(func(context.Context, string, string) (string, error) { return "Evidence-derived response", nil })
			enqueue(t, r.chainFixture)
			if err := r.pg.Admin.QueryRow(t.Context(), "SELECT id FROM channel.tasks").Scan(&f.task); err != nil {
				t.Fatal(err)
			}
			if _, err := f.worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			r.state(t, "waiting_approval")
			a, err := r.store.InspectApproval(t.Context(), f.actor, f.task)
			if allowed {
				if err != nil || !strings.Contains(a.Response, "Evidence-derived") {
					t.Fatal("authorized approval unavailable", err)
				}
			} else if !errors.Is(err, identitypg.ErrForbidden) || a.Response != "" {
				t.Fatal("approval leaked without source permission", err)
			}
			notices := 0
			if _, err := r.store.NotifyOne(t.Context(), notifyFunc(func(context.Context, string, string, string) error { notices++; return nil })); err != nil {
				t.Fatal(err)
			}
			if notices != map[bool]int{false: 0, true: 1}[allowed] {
				t.Fatal("notification bypassed source permissions")
			}
			if allowed {
				r.revoke(t)
				if a, err := r.store.InspectApproval(t.Context(), f.actor, f.task); !errors.Is(err, identitypg.ErrForbidden) || a.Response != "" {
					t.Fatal("revoked source still visible", err)
				}
				decision := conversation.ApprovalDecision{TaskID: f.task, ID: a.ID, RequestID: uuid.New(), Revision: a.Revision, Version: a.Version, ContentHash: a.ContentHash, Decision: "approve"}
				if _, err := r.store.DecideApproval(t.Context(), f.actor, decision); !errors.Is(err, identitypg.ErrForbidden) {
					t.Fatal("revoked source approval accepted", err)
				}
			}
		})
	}
}

func TestRetrievalToolLoopReusesAndRechecksEvidence(t *testing.T) {
	for _, phase := range []string{"success", "before_tool", "after_tool"} {
		t.Run(phase, func(t *testing.T) {
			f := toolLoop(t, false)
			r := addRetrieval(t, f.chainFixture)
			f.worker.Store = r.store
			stepLoop(t, f, "tool_pending")
			if phase == "before_tool" {
				r.revoke(t)
				stepLoop(t, f, "failed")
			} else {
				stepLoop(t, f, "pending")
				if phase == "after_tool" {
					r.revoke(t)
					stepLoop(t, f, "failed")
				} else {
					stepLoop(t, f, "succeeded")
				}
			}
			if r.embeds != 1 {
				t.Fatal("embedding repeated across tool turns")
			}
			wantModels, wantTools, wantSends := int32(1), int32(1), int32(0)
			if phase == "success" {
				wantModels, wantSends = 2, 1
			}
			if phase == "before_tool" {
				wantTools = 0
			}
			if f.modelCalls.Load() != wantModels || f.toolCalls.Load() != wantTools || f.sends.Load() != wantSends {
				t.Fatal("revoked evidence reached subsequent provider or delivery")
			}
		})
	}
}

func TestRetrievalRevocationDuringDeliveryLockWait(t *testing.T) {
	for _, phase := range []string{"delivery", "renewal"} {
		t.Run(phase, func(t *testing.T) {
			r := addRetrieval(t, chain(t))
			enqueue(t, r.chainFixture)
			task := mustClaim(t, r.store)
			step(t, r.store, task, conversation.ModelRunning, "")
			if err := r.store.SaveKnowledgeReferences(t.Context(), task, []knowledge.Reference{{SourceID: r.content.SourceID, DocumentID: "allowed", ChunkID: "p1", ContentRevision: 1, ACLRevision: 1}}); err != nil {
				t.Fatal(err)
			}
			if phase == "delivery" {
				step(t, r.store, task, conversation.DeliveryPending, "saved answer")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			tx, err := r.pg.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var pid int
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid() FROM channel.tasks WHERE id=$1 FOR UPDATE", task.ID).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if phase == "renewal" {
					done <- r.store.Renew(ctx, task)
				} else {
					done <- r.store.Transition(ctx, task, conversation.DeliveryRunning, "", "")
				}
			}()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err := r.pg.Admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))", pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("did not wait for task lock: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			r.revoke(t)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, conversation.ErrKnowledgeDenied) {
				t.Fatal("used pre-lock authorization", err)
			}
			r.state(t, map[string]string{"delivery": "delivery_pending", "renewal": "model_running"}[phase])
		})
	}
}

func TestRetrievalProviderFailureAndDeliveryAuditFailure(t *testing.T) {
	for _, phase := range []string{"embedding", "delivery_audit"} {
		t.Run(phase, func(t *testing.T) {
			r := addRetrieval(t, chain(t))
			if phase == "embedding" {
				var err error
				r.store, err = r.store.WithRetrieval(r.policy, embedFunc(func(context.Context, string) ([]float32, error) { return nil, errors.New("synthetic provider failure") }))
				if err != nil {
					t.Fatal(err)
				}
				r.adapter = testAdapter(t, r.store)
			}
			enqueue(t, r.chainFixture)
			w := conversation.Worker{Store: r.store, Logger: r.logger, Telemetry: r.adapter.telemetry,
				Model: groundedModel(func(context.Context, string, string) (string, error) {
					if phase == "embedding" {
						t.Error("chat ran after embedding failure")
					}
					if _, err := r.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit ADD CONSTRAINT synthetic_delivery_audit CHECK(action<>'read') NOT VALID"); err != nil {
						t.Fatal(err)
					}
					return "answer", nil
				}),
				Sender: senderFunc(func(context.Context, string, string, string) error { t.Error("failure released delivery"); return nil })}
			_, err := w.ProcessOne(t.Context())
			if phase == "embedding" {
				if err != nil {
					t.Fatal(err)
				}
				r.state(t, "unknown")
				if task, err := r.store.Claim(t.Context()); err != nil || task != nil {
					t.Fatal("unknown embedding replayed")
				}
			} else {
				if err == nil {
					t.Fatal("delivery audit failure swallowed")
				}
				r.state(t, "delivery_pending")
			}
		})
	}
}

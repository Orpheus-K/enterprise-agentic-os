//go:build integration

package feishu

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

func checkpointReferences() []knowledge.Reference {
	return []knowledge.Reference{{SourceID: uuid.New(), DocumentID: "synthetic", ChunkID: "paragraph-1", ContentRevision: 2, ACLRevision: 3}}
}

func TestKnowledgeCheckpointRestoredWithDelivery(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "references", true: "no_results"}[empty], func(t *testing.T) {
			f := chain(t)
			enqueue(t, f)
			task := mustClaim(t, f.store)
			if task.KnowledgeRefs != nil {
				t.Fatal("new task already has a checkpoint")
			}
			step(t, f.store, task, conversation.ModelRunning, "")
			refs := checkpointReferences()
			if empty {
				refs = []knowledge.Reference{}
			}
			want := slices.Clone(refs)
			if err := f.store.SaveKnowledgeReferences(t.Context(), task, refs); err != nil {
				t.Fatal(err)
			}
			if !empty {
				refs[0].ACLRevision++ // Caller-owned memory cannot change the saved checkpoint.
			}
			if !slices.Equal(task.KnowledgeRefs, want) || task.KnowledgeRefs == nil {
				t.Fatal("checkpoint aliases input or loses empty-set marker")
			}
			if err := f.store.SaveKnowledgeReferences(t.Context(), task, want); !errors.Is(err, conversation.ErrInvalidTransition) {
				t.Fatal("checkpoint overwritten", err)
			}
			step(t, f.store, task, conversation.DeliveryPending, "persisted answer")
			expireLease(t, f)
			db, err := database.Open(t.Context(), f.pg.RuntimeURL, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			restarted := conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
			resumed := mustClaim(t, restarted)
			if resumed.State != conversation.DeliveryPending || resumed.Response != "persisted answer" ||
				resumed.KnowledgeRefs == nil || !slices.Equal(resumed.KnowledgeRefs, want) {
				t.Fatal("delivery recovery lost response or reference set")
			}
			var count int
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit WHERE reason='knowledge_checkpoint'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("checkpoint audit: %d %v", count, err)
			}
		})
	}
}

func TestKnowledgeCheckpointFencesAndAuditRollback(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	task := mustClaim(t, f.store)
	refs := checkpointReferences()
	if err := f.store.SaveKnowledgeReferences(t.Context(), task, refs); !errors.Is(err, conversation.ErrInvalidTransition) {
		t.Fatal("checkpoint without external intent accepted", err)
	}
	step(t, f.store, task, conversation.ModelRunning, "")
	for _, store := range []*conversation.Store{
		anotherStore(f),
		conversation.NewStore(f.db, testBinding().TenantID, "other-app", "approved-revision"),
		conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "other-revision"),
		conversation.NewStore(f.db, uuid.New(), testBinding().AppID, "approved-revision"),
	} {
		if err := store.SaveKnowledgeReferences(t.Context(), task, refs); !errors.Is(err, conversation.ErrLeaseLost) {
			t.Fatal("foreign owner/installation/revision/tenant wrote checkpoint", err)
		}
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit ADD CONSTRAINT synthetic_checkpoint_failure CHECK(reason<>'knowledge_checkpoint') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	version := task.Version
	if err := f.store.SaveKnowledgeReferences(t.Context(), task, refs); err == nil {
		t.Fatal("checkpoint survived audit failure")
	}
	var absent bool
	var savedVersion int64
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT knowledge_refs IS NULL,step_version FROM channel.tasks").Scan(&absent, &savedVersion); err != nil {
		t.Fatal(err)
	}
	if !absent || task.KnowledgeRefs != nil || task.Version != version || savedVersion != version {
		t.Fatal("audit failure partially committed checkpoint")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.audit DROP CONSTRAINT synthetic_checkpoint_failure"); err != nil {
		t.Fatal(err)
	}
	stale := *task
	if err := f.store.SaveKnowledgeReferences(t.Context(), task, refs); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveKnowledgeReferences(t.Context(), &stale, checkpointReferences()); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("stale step replaced evidence", err)
	}
}

func TestKnowledgeCheckpointExpiredIntentIsNotReplayed(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	task := mustClaim(t, f.store)
	step(t, f.store, task, conversation.ModelRunning, "")
	expireLease(t, f)
	if err := f.store.SaveKnowledgeReferences(t.Context(), task, checkpointReferences()); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("expired lease wrote checkpoint", err)
	}
	restarted := anotherStore(f)
	if err := restarted.RecoverExpired(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.state(t, "unknown")
	if got, err := restarted.Claim(t.Context()); err != nil || got != nil {
		t.Fatal("unknown external intent was replayable", err)
	}
}

func TestKnowledgeCheckpointRejectsInvalidReferences(t *testing.T) {
	f := chain(t)
	enqueue(t, f)
	task := mustClaim(t, f.store)
	step(t, f.store, task, conversation.ModelRunning, "")
	ref := checkpointReferences()[0]
	bad := ref
	bad.ACLRevision = 0
	for _, refs := range [][]knowledge.Reference{nil, {bad}, {ref, ref}, make([]knowledge.Reference, 51)} {
		if err := f.store.SaveKnowledgeReferences(t.Context(), task, refs); err == nil {
			t.Fatal("invalid reference checkpoint accepted")
		}
	}
	var absent bool
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT knowledge_refs IS NULL FROM channel.tasks").Scan(&absent); err != nil || !absent {
		t.Fatal("invalid input modified checkpoint", err)
	}
	// The migration also rejects JSON null, objects and oversized arrays.
	for _, raw := range []string{"null", "{}", "[" + strings.Repeat("{},", 50) + "{}]"} {
		if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE channel.tasks SET knowledge_refs=$1::jsonb", raw); err == nil {
			t.Fatal("invalid checkpoint shape accepted")
		}
	}
}

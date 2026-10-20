//go:build integration

package operations

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

func TestSyncReconciliationHTTP(t *testing.T) {
	for _, outcome := range []string{"not_sent", "completed"} {
		t.Run(outcome, func(t *testing.T) {
			f := setup(t)
			store, err := knowledge.New(f.db, f.tenant, "app_a")
			if err != nil {
				t.Fatal(err)
			}
			f.api = f.api.WithKnowledge(store)
			source := uuid.New()
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := f.pg.Admin.Exec(t.Context(), sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO knowledge.sources VALUES($1,'app_a',$2,$3,true)", f.tenant, source, f.member)
			hash := strings.Repeat("a", 64)
			content := knowledge.Content{SourceID: source, DocumentID: "document", Revision: 1, Title: "Synthetic",
				Chunks: []knowledge.Chunk{{ID: "p1", Body: "Synthetic evidence", Anchor: "block:p1"}}}
			space := knowledge.Space{ID: uuid.New(), Model: "synthetic", ModelRevision: "v1"}
			if err := store.ScheduleSync(t.Context(), f.external, source, content.DocumentID, 1, hash); err != nil {
				t.Fatal(err)
			}
			lease, err := store.ClaimSync(t.Context(), f.external, source, hash, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.BeginSyncEmbedding(t.Context(), f.external, lease, content, space); err != nil {
				t.Fatal(err)
			}
			vectors := knowledge.VectorSet{Space: space, SourceID: source, DocumentID: content.DocumentID, ContentRevision: 1,
				Vectors: []knowledge.ChunkVector{{ChunkID: "p1", Embedding: []float32{1, 0, 0}}}}
			in := knowledge.SyncReconciliation{ID: uuid.New(), SourceID: source, DocumentID: content.DocumentID,
				RequestRevision: 1, Generation: lease.Generation, ConfigHash: hash, Outcome: outcome, EvidenceHash: strings.Repeat("b", 64)}
			if outcome == "completed" {
				in.Vectors = &vectors
			}
			request := func(value any, auth bool) int {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", "/api/ops/knowledge/sync/reconcile", bytes.NewReader(raw))
				if auth {
					r.Header.Set("Authorization", "Bearer "+opsToken)
				}
				w := httptest.NewRecorder()
				f.api.ServeHTTP(w, r)
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("reconciliation response cacheable")
				}
				return w.Code
			}
			inspect := func(want int) knowledge.SyncStatus {
				raw, _ := json.Marshal(map[string]any{"source_id": source, "document_id": content.DocumentID})
				r := httptest.NewRequest("POST", "/api/ops/knowledge/sync/inspect", bytes.NewReader(raw))
				r.Header.Set("Authorization", "Bearer "+opsToken)
				w := httptest.NewRecorder()
				f.api.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("inspect status %d, want %d", w.Code, want)
				}
				var status knowledge.SyncStatus
				if want == 200 {
					if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
						t.Fatal(err)
					}
					for _, secret := range []string{content.Title, content.Chunks[0].Body, lease.Token.String(), "embedding_vectors", "lease_token"} {
						if strings.Contains(w.Body.String(), secret) {
							t.Fatal("inspect exposed private data")
						}
					}
				}
				return status
			}
			inspect(403)
			if got := request(in, false); got != 401 {
				t.Fatal("missing authentication", got)
			}
			if got := request(in, true); got != 403 {
				t.Fatal("missing explicit capability", got)
			}
			exec("INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'app_a','knowledge.reconcile')", f.tenant, f.member)
			originalDocument := content.DocumentID
			content.DocumentID = "missing"
			inspect(404)
			content.DocumentID = originalDocument
			originalSource := source
			source = uuid.New()
			inspect(403)
			source = originalSource
			status := inspect(200)
			if status.State != "running" || !status.LeaseActive || status.EmbeddingState != "pending" || status.Generation != lease.Generation || status.ConfigHash != hash || status.Embedding == nil || len(status.Embedding.Hash) != 64 || len(status.Embedding.ChunkIDs) != 1 || status.Embedding.ChunkIDs[0] != "p1" {
				t.Fatal("invalid active snapshot", status)
			}
			if got := request(in, true); got != 409 {
				t.Fatal("live worker reconciled", got)
			}
			exec("UPDATE knowledge.sync_jobs SET lease_until=clock_timestamp()-interval '1 second'")
			if status := inspect(200); status.LeaseActive || status.EmbeddingState != "pending" {
				t.Fatal("expired intent not preserved")
			}
			bad := in
			bad.EvidenceHash = ""
			if got := request(bad, true); got != 400 {
				t.Fatal("missing evidence accepted", got)
			}
			bad = in
			bad.Generation++
			if got := request(bad, true); got != 409 {
				t.Fatal("wrong generation accepted", got)
			}
			exec("ALTER TABLE knowledge.audit ADD CONSTRAINT reject_sync_reconcile CHECK(action<>'sync.reconcile') NOT VALID")
			if got := request(in, true); got != 503 {
				t.Fatal("audit failure ignored", got)
			}
			var count int
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM knowledge.sync_jobs WHERE embedding_pending").Scan(&count); err != nil || count != 1 {
				t.Fatal("audit failure cleared unknown state", err)
			}
			exec("ALTER TABLE knowledge.audit DROP CONSTRAINT reject_sync_reconcile")
			for i := 0; i < 2; i++ {
				if got := request(in, true); got != 200 {
					t.Fatal("reconciliation/retry failed", got)
				}
			}
			status = inspect(200)
			if status.State != "pending" || status.Generation <= lease.Generation || status.LeaseActive {
				t.Fatal("reconciled state not visible")
			}
			if outcome == "completed" && status.EmbeddingState != "saved" {
				t.Fatal("saved result missing")
			}
			if outcome == "not_sent" && (status.EmbeddingState != "none" || status.Embedding != nil) {
				t.Fatal("cleared intent still visible")
			}
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM knowledge.sync_reconciliations").Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate reconciliation receipt", err)
			}
			bad = in
			bad.EvidenceHash = strings.Repeat("c", 64)
			if got := request(bad, true); got != 409 {
				t.Fatal("idempotency key reused with different evidence", got)
			}
			if err := store.SaveSyncEmbedding(t.Context(), f.external, lease, vectors); !errors.Is(err, knowledge.ErrConflict) {
				t.Fatal("old worker overwrote reconciliation")
			}
			next, err := store.ClaimSync(t.Context(), f.external, source, hash, time.Minute)
			if err != nil || next.Generation <= lease.Generation {
				t.Fatal("reconciled task not recoverable", err)
			}
			if outcome == "completed" {
				if saved, err := store.LoadSyncEmbedding(t.Context(), f.external, next); err != nil || saved.Content.Title != content.Title {
					t.Fatal("confirmed result lost", err)
				}
			} else if err := store.BeginSyncEmbedding(t.Context(), f.external, next, content, space); err != nil {
				t.Fatal("confirmed not-sent task not retryable", err)
			}
			exec("UPDATE iam.operation_grants SET enabled=false WHERE capability='knowledge.reconcile'")
			inspect(403)
			if got := request(in, true); got != 403 {
				t.Fatal("receipt retry bypassed revoked capability", got)
			}
			exec("UPDATE iam.operation_grants SET enabled=true WHERE capability='knowledge.reconcile'")
			exec("UPDATE knowledge.sources SET enabled=false WHERE id=$1", source)
			inspect(403)
		})
	}
}

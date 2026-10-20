//go:build integration

package feishudocs

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
)

func TestNativeSyncPipeline(t *testing.T) {
	for _, mode := range []string{"valid", "recipient_denied", "owner_revoked", "embedding_unknown", "saved_recovery"} {
		t.Run(mode, func(t *testing.T) {
			pg := testpg.New(t)
			m, err := database.NewMigrator(t.Context(), pg.MigrationURL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Up(t.Context())
			if err = errors.Join(err, m.Close()); err != nil {
				t.Fatal(err)
			}
			tenant, ownerID, readerID, sourceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			exec := func(sql string, args ...any) {
				t.Helper()
				if _, err := pg.Admin.Exec(t.Context(), sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("INSERT INTO iam.tenants VALUES($1,'synthetic',true)", tenant)
			for id, subject := range map[uuid.UUID]string{ownerID: "ou_sync", readerID: "ou_employee"} {
				exec("INSERT INTO iam.members VALUES($1,$2,$3,true)", tenant, id, subject)
				exec("INSERT INTO iam.identity_links VALUES($1,'feishu','feishu:cli_test:tenant',$2,$3,true)", tenant, subject, id)
			}
			exec("INSERT INTO knowledge.sources VALUES($1,'cli_test',$2,$3,true)", tenant, sourceID, ownerID)
			space := knowledge.Space{ID: uuid.New(), Model: "synthetic", ModelRevision: "v1"}
			exec("INSERT INTO knowledge.embedding_spaces VALUES($1,'cli_test',$2,'synthetic','v1',3,true)", tenant, space.ID)
			db, err := database.Open(t.Context(), pg.RuntimeURL, 3)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store, err := knowledge.New(db, tenant, "cli_test")
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var nativeRevision atomic.Int64
			nativeRevision.Store(7)
			var failReads, revoked atomic.Bool
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if user != "ou_sync" && user != "ou_employee" {
					t.Error("unbound credential")
				}
				w.Header().Set("Content-Type", "application/json")
				var data any
				switch r.URL.Path {
				case "/open-apis/authen/v1/user_info":
					data = map[string]any{"open_id": user, "tenant_key": "tenant"}
				case "/open-apis/drive/v1/permissions/doxcTest/members/auth":
					data = map[string]any{"auth_result": !(revoked.Load() || mode == "recipient_denied" && user == "ou_employee")}
				case "/open-apis/docx/v1/documents/doxcTest":
					if failReads.Load() {
						w.WriteHeader(503)
						return
					}
					data = map[string]any{"document": map[string]any{"document_id": "doxcTest", "revision_id": nativeRevision.Load(), "title": "Synthetic"}}
				case "/open-apis/docx/v1/documents/doxcTest/blocks":
					if r.URL.Query().Get("document_revision_id") != strconv.FormatInt(nativeRevision.Load(), 10) {
						t.Error("unpinned source")
					}
					data = map[string]any{"has_more": false, "items": []json.RawMessage{
						textBlock("root", "", 1, "page", "", "p1"), textBlock("p1", "root", 2, "text", "Synthetic evidence"),
					}}
				default:
					t.Error("unexpected source route", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}); err != nil {
					t.Error(err)
				}
			}))
			defer source.Close()
			modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "embedding_unknown" {
					w.WriteHeader(503)
					return
				}
				if mode == "owner_revoked" {
					revoked.Store(true)
				}
				if mode == "saved_recovery" {
					failReads.Store(true)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"embedding":[1,0,0]}]}`))
			}))
			defer modelServer.Close()
			reader, err := NewMappedReader(newReader("cli_test", source.URL, source.Client().Transport), identitypg.New(db), tenant, "tenant")
			if err != nil {
				t.Fatal(err)
			}
			credential := func(subject string) Credential {
				c, err := NewCredential(User{AppID: "cli_test", TenantKey: "tenant", OpenID: subject}, subject)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			worker, err := NewSyncer(reader, store, sourceID, credential("ou_sync"), []Credential{credential("ou_employee")},
				model.EmbeddingConfig{Endpoint: modelServer.URL + "/embeddings", Name: "synthetic", Revision: "v1", Dimensions: 3, AllowHTTP: true}, "", space)
			if err != nil {
				t.Fatal(err)
			}
			actor := identity.ExternalIdentity{TenantID: tenant, Provider: "feishu", Issuer: "feishu:cli_test:tenant", Subject: "ou_sync"}
			health, err := store.UpdateHealth(t.Context(), actor, knowledge.HealthUpdate{
				SourceID: sourceID, Revision: 1, State: "healthy", ValidUntil: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			old := knowledge.Content{SourceID: sourceID, DocumentID: "doxcTest", Revision: 6, Title: "Old",
				Chunks: []knowledge.Chunk{{ID: "p1", Body: "Old evidence", Anchor: "old:p1"}}}
			if err := store.Publish(t.Context(), actor, knowledge.Publication{
				Content: old, ACL: knowledge.ACL{SourceID: sourceID, DocumentID: "doxcTest", Revision: 1,
					ContentRevision: 6, SourceEpoch: health.Epoch, Allow: []uuid.UUID{readerID}, ValidUntil: health.ValidUntil},
				Vectors: knowledge.VectorSet{Space: space, SourceID: sourceID, DocumentID: "doxcTest", ContentRevision: 6,
					Vectors: []knowledge.ChunkVector{{ChunkID: "p1", Embedding: []float32{1, 0, 0}}}},
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.ScheduleSync(t.Context(), actor, sourceID, "doxcTest", 2, worker.ConfigHash()); err != nil {
				t.Fatal(err)
			}
			worked, err := worker.RunOnce(t.Context())
			success := mode == "valid" || mode == "recipient_denied"
			if !worked || (err == nil) != success || calls.Load() != 1 {
				t.Fatal("pipeline outcome", worked, err, calls.Load())
			}
			if !success {
				var state string
				if err := pg.Admin.QueryRow(t.Context(), "SELECT state FROM knowledge.source_health").Scan(&state); err != nil || state != "unavailable" {
					t.Fatal("failure did not invalidate source", state, err)
				}
			}
			if mode == "embedding_unknown" || mode == "saved_recovery" {
				exec("UPDATE knowledge.sync_jobs SET lease_until=clock_timestamp()-interval '1 second'")
				failReads.Store(false)
				worked, err := worker.RunOnce(t.Context())
				if mode == "embedding_unknown" && (worked || err != nil) || mode == "saved_recovery" && (!worked || err != nil) || calls.Load() != 1 {
					t.Fatal("unsafe replay or lost checkpoint", worked, err, calls.Load())
				}
			}
			if mode == "valid" {
				for _, revision := range []int64{3, 4} {
					if revision == 4 {
						nativeRevision.Store(8)
					}
					if err := store.ScheduleSync(t.Context(), actor, sourceID, "doxcTest", revision, worker.ConfigHash()); err != nil {
						t.Fatal(err)
					}
					worked, err := worker.RunOnce(t.Context())
					wantCalls := int32(revision - 2)
					if !worked || err != nil || calls.Load() != wantCalls {
						t.Fatal("unchanged content re-embedded or changed content not refreshed", worked, err, calls.Load())
					}
				}
			}
			actor.Subject = "ou_employee"
			found, err := store.Search(t.Context(), actor, knowledge.SearchQuery{Space: space, Embedding: []float32{1, 0, 0}, Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "valid" || mode == "saved_recovery" {
				want = 1
			}
			if len(found) != want {
				t.Fatal("published ACL did not match native access", len(found), want)
			}
		})
	}
}

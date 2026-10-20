//go:build integration

package knowledge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPollSyncPreservesInflightAndAdvancesCompleted(t *testing.T) {
	f := setup(t)
	hash := strings.Repeat("a", 64)
	poll := func() {
		t.Helper()
		if err := f.store.PollSync(t.Context(), f.owner, f.source, f.content.DocumentID, hash); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	poll()
	lease, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil || lease.RequestRevision != 1 {
		t.Fatal(lease, err)
	}
	poll()
	space := f.addSpace(t, f.owner.TenantID, "app", uuid.New(), true)
	if err := f.store.PublishSync(t.Context(), f.owner, lease, Publication{Content: f.content, ACL: f.acl, Vectors: f.vectors(space)}); err != nil {
		t.Fatal("poll fenced active worker", err)
	}
	poll()
	poll()
	next, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil || next.RequestRevision != 2 {
		t.Fatal("poll did not allocate exactly one next request", next, err)
	}
	if err := f.store.BeginSyncEmbedding(t.Context(), f.owner, next, f.content, space); err != nil {
		t.Fatal(err)
	}
	poll()
	if err := f.store.PollSync(t.Context(), f.owner, f.source, f.content.DocumentID, strings.Repeat("b", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("poll overwrote unknown embedding", err)
	}
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='sync.schedule'", 2)
}

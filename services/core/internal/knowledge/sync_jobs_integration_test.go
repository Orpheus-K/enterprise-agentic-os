//go:build integration

package knowledge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"agentos.local/core/internal/database"
	"github.com/google/uuid"
)

func TestSyncLeaseRecoveryAndPublication(t *testing.T) {
	f := setup(t)
	hash := strings.Repeat("a", 64)
	space := f.addSpace(t, f.owner.TenantID, "app", uuid.New(), true)
	p := Publication{Content: f.content, ACL: f.acl, Vectors: f.vectors(space)}
	schedule := func(revision int64) {
		t.Helper()
		if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, revision, hash); err != nil {
			t.Fatal(err)
		}
	}
	schedule(1)
	schedule(1)
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='sync.schedule'", 1)
	if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 1, strings.Repeat("c", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("same request revision changed configuration")
	}
	if _, err := f.store.ClaimSync(t.Context(), f.owner, f.source, strings.Repeat("c", 64), time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatal("worker claimed a different configuration")
	}
	if err := f.store.within(t.Context(), f.reader, func(q database.Queries) error {
		var count int
		if err := q.QueryRow(t.Context(), "SELECT count(*) FROM knowledge.sync_jobs").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("RLS exposed sync leases to document reader")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reopen the runtime pool: scheduling must not depend on process memory.
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := New(db, f.owner.TenantID, "app")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute); !errors.Is(err, ErrUnavailable) {
		t.Fatal("active lease stolen")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.sync_jobs SET lease_until=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	second, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil || second.Generation <= first.Generation || second.Token == first.Token {
		t.Fatal("expired work did not receive new fence", err)
	}
	if err := store.PublishSync(t.Context(), f.owner, first, p); !errors.Is(err, ErrConflict) {
		t.Fatal("old worker published")
	}
	// Even the current worker must roll back publication if completion audit fails.
	if _, err := f.pg.Admin.Exec(t.Context(), `CREATE FUNCTION knowledge.reject_sync_complete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='sync.complete' THEN RAISE EXCEPTION 'synthetic'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_sync_complete BEFORE INSERT ON knowledge.audit FOR EACH ROW EXECUTE FUNCTION knowledge.reject_sync_complete()`); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishSync(t.Context(), f.owner, second, p); err == nil {
		t.Fatal("completion audit failure ignored")
	}
	f.count(t, "SELECT count(*) FROM knowledge.documents", 0)
	f.count(t, "SELECT count(*) FROM knowledge.sync_jobs WHERE state='running'", 1)
	if _, err := f.pg.Admin.Exec(t.Context(), "DROP TRIGGER reject_sync_complete ON knowledge.audit; DROP FUNCTION knowledge.reject_sync_complete()"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.PublishSync(t.Context(), f.owner, second, p); err != nil {
			t.Fatal(err)
		}
	}
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='sync.complete'", 1)
	if got := f.search(t, f.reader, space, 10); len(got) != 2 {
		t.Fatal("completed job not searchable")
	}
	changed := p
	changed.Content.Title = "changed"
	if err := store.PublishSync(t.Context(), f.owner, second, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("completion replay accepted different publication")
	}
	schedule(2)
	if err := store.PublishSync(t.Context(), f.owner, second, p); !errors.Is(err, ErrConflict) {
		t.Fatal("old completion acknowledged newer request")
	}
	if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 1, hash); !errors.Is(err, ErrConflict) {
		t.Fatal("old scheduling request restored")
	}
	if _, err := store.ClaimSync(t.Context(), f.reader, f.source, hash, time.Minute); err == nil {
		t.Fatal("reader claimed sync work")
	}
}

func TestSyncClaimConcurrentAndExpiryAtCommit(t *testing.T) {
	f := setup(t)
	hash := strings.Repeat("b", 64)
	if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 1, hash); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	leases := make(chan SyncLease, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Go(func() {
			lease, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
			if err == nil {
				leases <- lease
			} else {
				errs <- err
			}
		})
	}
	wg.Wait()
	if len(leases) != 1 || len(errs) != 1 || !errors.Is(<-errs, ErrUnavailable) {
		t.Fatal("concurrent claim had multiple owners")
	}
	lease := <-leases
	space := f.addSpace(t, f.owner.TenantID, "app", uuid.New(), true)
	p := Publication{Content: f.content, ACL: f.acl, Vectors: f.vectors(space)}
	// Expire the lease inside the publication transaction without timing sleeps.
	if _, err := f.pg.Admin.Exec(t.Context(), `CREATE FUNCTION knowledge.expire_sync_lease() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='acl' THEN UPDATE knowledge.sync_jobs SET lease_until=clock_timestamp()-interval '1 second'; END IF; RETURN NEW; END $$;
CREATE TRIGGER expire_sync_lease BEFORE INSERT ON knowledge.audit FOR EACH ROW EXECUTE FUNCTION knowledge.expire_sync_lease()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := f.pg.Admin.Exec(context.Background(), "DROP TRIGGER expire_sync_lease ON knowledge.audit; DROP FUNCTION knowledge.expire_sync_lease()")
		if err != nil {
			t.Error(err)
		}
	})
	if err := f.store.PublishSync(t.Context(), f.owner, lease, p); !errors.Is(err, ErrConflict) {
		t.Fatal("lease expiring before completion committed publication", err)
	}
	f.count(t, "SELECT count(*) FROM knowledge.documents", 0)
}

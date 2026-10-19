//go:build integration

package knowledge

import (
	"agentos.local/core/internal/database"
	identitypg "agentos.local/core/internal/identity/postgres"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSourceHealthConcurrentReplayAndRestart(t *testing.T) {
	f := setup(t)
	f.publish(t)
	in := HealthUpdate{SourceID: f.source, Revision: 2, State: "unavailable", ValidUntil: time.Now().UTC()}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			out, err := f.store.UpdateHealth(t.Context(), f.owner, in)
			if err != nil || out.Epoch != 2 {
				t.Errorf("concurrent replay: %+v %v", out, err)
			}
		})
	}
	wg.Wait()
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f.store, err = New(db, f.reader.TenantID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := f.store.UpdateHealth(t.Context(), f.owner, in); err != nil || out.Epoch != 2 {
		t.Fatal("restarted replay", out, err)
	}
	f.read(t, false)
	var events int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM knowledge.audit WHERE action='source_health' AND revision=2").Scan(&events); err != nil || events != 1 {
		t.Fatal("duplicate health event", events, err)
	}
}

func TestSourceRecoveryRequiresFreshACLGeneration(t *testing.T) {
	for _, mode := range []string{"unavailable", "expired", "admin_disabled"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			f.publish(t)
			f.read(t, true)
			in := HealthUpdate{SourceID: f.source, Revision: 2, State: "unavailable", ValidUntil: time.Now().UTC()}
			if mode == "unavailable" {
				if _, err := f.store.UpdateHealth(t.Context(), f.owner, in); err != nil {
					t.Fatal(err)
				}
			} else if mode == "admin_disabled" {
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.sources SET enabled=false"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.sources SET enabled=true"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE knowledge.source_health SET valid_until=clock_timestamp()-interval '1 second'"); err != nil {
					t.Fatal(err)
				}
			}
			f.read(t, false)
			if err := f.store.PutACL(t.Context(), f.owner, f.acl); !errors.Is(err, ErrUnavailable) {
				t.Fatal("ACL accepted while unhealthy", err)
			}
			in.Revision = 3
			in.State = "healthy"
			in.ValidUntil = time.Now().UTC().Add(time.Hour)
			out, err := f.store.UpdateHealth(t.Context(), f.owner, in)
			if err != nil || out.Epoch <= 1 {
				t.Fatal(out, err)
			}
			f.read(t, false)
			f.acl.SourceEpoch = out.Epoch
			if err := f.store.PutACL(t.Context(), f.owner, f.acl); !errors.Is(err, ErrConflict) {
				t.Fatal("old ACL version refreshed", err)
			}
			f.acl.Revision++
			if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
				t.Fatal(err)
			}
			f.read(t, true)
			if again, err := f.store.UpdateHealth(t.Context(), f.owner, in); err != nil || again.Epoch != out.Epoch {
				t.Fatal("duplicate changed epoch", again, err)
			}
			in.Revision = 2
			if _, err := f.store.UpdateHealth(t.Context(), f.owner, in); !errors.Is(err, ErrConflict) {
				t.Fatal("old health replay", err)
			}
			var revision int64
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT content_revision FROM knowledge.documents").Scan(&revision); err != nil || revision != 1 {
				t.Fatal("recovery rebuilt content", revision, err)
			}
		})
	}
}

func TestSourceHealthAuthorizationHeartbeatAndAtomicity(t *testing.T) {
	f := setup(t)
	f.publish(t)
	in := HealthUpdate{SourceID: f.source, Revision: 2, State: "healthy", ValidUntil: time.Now().UTC().Add(time.Hour)}
	if _, err := f.store.UpdateHealth(t.Context(), f.reader, in); !errors.Is(err, identitypg.ErrForbidden) {
		t.Fatal("reader updated health", err)
	}
	out, err := f.store.UpdateHealth(t.Context(), f.owner, in)
	if err != nil || out.Epoch != 1 {
		t.Fatal("heartbeat invalidates current ACL", out, err)
	}
	f.read(t, true)
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit ADD CONSTRAINT fail_health CHECK(action<>'source_health') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	in.Revision = 3
	in.State = "unavailable"
	if _, err := f.store.UpdateHealth(t.Context(), f.owner, in); err == nil {
		t.Fatal("audit failure swallowed")
	}
	f.read(t, true)
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.UpdateHealth(t.Context(), f.owner, in); err == nil {
		t.Fatal("revoked sync identity wrote")
	}
}

//go:build integration

package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestSyncPublicationMemberProofs(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "wrong_member", "revoked", "late_revoke"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			hash := strings.Repeat("a", 64)
			space := f.addSpace(t, f.owner.TenantID, "app", uuid.New(), true)
			p := Publication{Content: f.content, ACL: f.acl, Vectors: f.vectors(space)}
			if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 1, hash); err != nil {
				t.Fatal(err)
			}
			lease, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			sync := MemberProof{Actor: f.owner, Principal: identity.Principal{TenantID: f.owner.TenantID, MemberID: f.ownerID}}
			readers := []MemberProof{{Actor: f.reader, Principal: identity.Principal{TenantID: f.reader.TenantID, MemberID: f.readerID}}}
			switch mode {
			case "missing":
				readers = nil
			case "wrong_member":
				readers[0].Actor = f.owner
			case "revoked":
				if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.readerID); err != nil {
					t.Fatal(err)
				}
			case "late_revoke":
				if _, err := f.pg.Admin.Exec(t.Context(), `CREATE FUNCTION knowledge.revoke_sync_recipient() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$ BEGIN IF NEW.action='acl' THEN UPDATE iam.identity_links SET enabled=false WHERE subject='reader'; END IF; RETURN NEW; END $$;
CREATE TRIGGER revoke_sync_recipient BEFORE INSERT ON knowledge.audit FOR EACH ROW EXECUTE FUNCTION knowledge.revoke_sync_recipient()`); err != nil {
					t.Fatal(err)
				}
			}
			err = f.store.PublishSyncForMembers(t.Context(), sync, lease, p, readers)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				var auditMember uuid.UUID
				if err := f.pg.Admin.QueryRow(t.Context(), "SELECT member_id FROM knowledge.audit WHERE action='sync.complete'").Scan(&auditMember); err != nil || auditMember != f.ownerID {
					t.Fatal("recipient context leaked into publication audit", err)
				}
			} else {
				if err == nil {
					t.Fatal("unverified member publication succeeded")
				}
				f.count(t, "SELECT count(*) FROM knowledge.documents", 0)
				f.count(t, "SELECT count(*) FROM knowledge.sync_jobs WHERE state='running'", 1)
			}
		})
	}
}

func TestSyncRecipientRevokedDuringDocumentLockWait(t *testing.T) {
	f := setup(t)
	hash := strings.Repeat("b", 64)
	space := f.addSpace(t, f.owner.TenantID, "app", uuid.New(), true)
	f.publish(t)
	p := Publication{Content: f.content, ACL: f.acl, Vectors: f.vectors(space)}
	if err := f.store.ScheduleSync(t.Context(), f.owner, f.source, f.content.DocumentID, 1, hash); err != nil {
		t.Fatal(err)
	}
	lease, err := f.store.ClaimSync(t.Context(), f.owner, f.source, hash, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := f.pg.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	observer, err := pgx.Connect(t.Context(), f.pg.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close(context.Background())
	if _, err := lock.Exec(t.Context(), "SELECT id FROM knowledge.documents FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	sync := MemberProof{Actor: f.owner, Principal: identity.Principal{TenantID: f.owner.TenantID, MemberID: f.ownerID}}
	readers := []MemberProof{{Actor: f.reader, Principal: identity.Principal{TenantID: f.reader.TenantID, MemberID: f.readerID}}}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() { done <- f.store.PublishSyncForMembers(ctx, sync, lease, p, readers) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
AND wait_event_type='Lock' AND query LIKE '%knowledge.documents%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatal("publication did not wait on document lock", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if _, err := observer.Exec(ctx, "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.readerID); err != nil {
		t.Fatal(err)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal("revocation during lock wait was ignored", err)
	}
	f.count(t, "SELECT count(*) FROM knowledge.vector_sets", 0)
}

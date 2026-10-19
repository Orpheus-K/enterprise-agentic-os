//go:build integration

package knowledge

import (
	"agentos.local/core/internal/identity"
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func references(c []Candidate) []Reference {
	out := make([]Reference, 0, len(c))
	for _, x := range c {
		out = append(out, x.Reference)
	}
	return out
}

func TestGroundRereadsCandidatesBeforeModelAndDelivery(t *testing.T) {
	f := setup(t)
	space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
	f.publish(t)
	if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
		t.Fatal(err)
	}
	refs := references(f.search(t, f.reader, space, 5))
	got, err := f.store.Ground(t.Context(), f.reader, refs, Audience{})
	if err != nil || got.Dropped != 0 || len(got.Evidence) != 2 {
		t.Fatal("authorized grounding", got, err)
	}
	prompt, err := ModelContext(got.Evidence)
	if err != nil || !strings.Contains(prompt, "Synthetic evidence") || !strings.Contains(prompt, "paragraph:1") {
		t.Fatal("prompt lacks cited evidence", prompt, err)
	}
	// Revocation between search and delivery: the old references must yield no body at all.
	f.acl.Revision = 2
	f.acl.Allow = nil
	if err := f.store.PutACL(t.Context(), f.owner, f.acl); err != nil {
		t.Fatal(err)
	}
	got, err = f.store.Ground(t.Context(), f.reader, refs, Audience{})
	if err != nil || got.Dropped != len(refs) || len(got.Evidence) != 0 {
		t.Fatal("revoked references still grounded", got, err)
	}
	if prompt, err := ModelContext(got.Evidence); err != nil || prompt != "" {
		t.Fatal("revoked body reached prompt", prompt, err)
	}
}

func TestGroundGroupAudienceAndIdentityFailures(t *testing.T) {
	f := setup(t)
	space := f.addSpace(t, f.reader.TenantID, "app", uuid.New(), true)
	f.publish(t)
	if err := f.store.PublishVectors(t.Context(), f.owner, f.vectors(space)); err != nil {
		t.Fatal(err)
	}
	refs := references(f.search(t, f.reader, space, 5))
	// The unconfirmed gate is checked before any read, so no read audit rows exist afterwards.
	if _, err := f.store.Ground(t.Context(), f.reader, refs, Audience{Group: true}); !errors.Is(err, ErrAudienceUnconfirmed) {
		t.Fatal("group body loaded without confirmed scope", err)
	}
	f.count(t, "SELECT count(*) FROM knowledge.audit WHERE action='read'", 0)
	got, err := f.store.Ground(t.Context(), f.reader, refs, Audience{Group: true, RecipientsConfirmed: true})
	if err != nil || len(got.Evidence) != 2 {
		t.Fatal("confirmed group scope", got, err)
	}
	// Audit failure withholds evidence rather than returning unrecorded reads.
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit ADD CONSTRAINT synthetic_ground_read CHECK(action<>'read') NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.Ground(t.Context(), f.reader, refs, Audience{}); err == nil || got.Evidence != nil {
		t.Fatal("read audit failure released evidence", got, err)
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE knowledge.audit DROP CONSTRAINT synthetic_ground_read"); err != nil {
		t.Fatal(err)
	}
	// A revoked identity is an authentication failure, not a silent drop.
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.readerID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.Ground(t.Context(), f.reader, refs, Audience{}); !errors.Is(err, identity.ErrUnauthenticated) || got.Evidence != nil || got.Dropped != 0 {
		t.Fatal("revoked identity grounded", got, err)
	}
}

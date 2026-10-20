package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type syntheticAudience struct {
	scope     GroupScope
	reads     int
	checked   []identity.Principal
	change    func(*syntheticAudience)
	denied    uuid.UUID
	err       error
	authError error
}

func (a *syntheticAudience) Scope(context.Context, uuid.UUID, string, string) (GroupScope, error) {
	a.reads++
	if a.reads == 2 && a.change != nil {
		a.change(a)
	}
	return a.scope, a.err
}
func (a *syntheticAudience) Authorize(_ context.Context, d GroupDelivery, p identity.Principal) error {
	if a.authError != nil {
		return a.authError
	}
	if len(d.References) != 1 || d.Revision != "v1" || d.ContentSHA256 != strings.Repeat("a", 64) {
		return ErrInvalid
	}
	a.checked = append(a.checked, p)
	if p.MemberID == a.denied {
		return identity.ErrForbidden
	}
	return nil
}
func TestGroupAudienceProof(t *testing.T) {
	for _, scenario := range []string{"valid", "reordered", "missing", "extra", "duplicate", "cross_tenant", "partial", "expired", "wrong_app", "wrong_group", "revoked", "changed", "late_expiry", "provider_error", "audit_error", "cancelled", "bad_digest", "bad_reference"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
			tenant := uuid.New()
			members := []identity.Principal{{TenantID: tenant, MemberID: uuid.New()}, {TenantID: tenant, MemberID: uuid.New()}}
			d := GroupDelivery{TenantID: tenant, AppID: "app", GroupID: "group", Revision: "v1", ContentSHA256: strings.Repeat("a", 64), Recipients: members, References: []Reference{{SourceID: uuid.New(), DocumentID: "doc", ChunkID: "chunk", ContentRevision: 1, ACLRevision: 1}}}
			a := &syntheticAudience{scope: GroupScope{TenantID: tenant, AppID: "app", GroupID: "group", Version: "1", Complete: true, ValidUntil: now.Add(time.Minute), Recipients: append([]identity.Principal(nil), members...)}}
			ctx := t.Context()
			switch scenario {
			case "reordered":
				a.scope.Recipients = []identity.Principal{members[1], members[0]}
			case "missing":
				a.scope.Recipients = a.scope.Recipients[:1]
			case "extra":
				a.scope.Recipients = append(a.scope.Recipients, identity.Principal{TenantID: tenant, MemberID: uuid.New()})
			case "duplicate":
				d.Recipients = []identity.Principal{members[0], members[0]}
			case "cross_tenant":
				a.scope.Recipients[1].TenantID = uuid.New()
			case "partial":
				a.scope.Complete = false
			case "expired":
				a.scope.ValidUntil = now
			case "wrong_app":
				a.scope.AppID = "other"
			case "wrong_group":
				a.scope.GroupID = "other"
			case "revoked":
				a.denied = members[1].MemberID
			case "changed":
				a.change = func(a *syntheticAudience) { a.scope.Version = "2" }
			case "late_expiry":
				a.change = func(a *syntheticAudience) { now = a.scope.ValidUntil }
			case "provider_error":
				a.err = errors.New("unavailable")
			case "audit_error":
				a.authError = errors.New("audit write failed")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "bad_digest":
				d.ContentSHA256 = "bad"
			case "bad_reference":
				d.References[0].ACLRevision = 0
			}
			proof, err := VerifyGroupAudience(ctx, a, d, func() time.Time { return now })
			if scenario == "valid" || scenario == "reordered" {
				if err != nil || proof.contentSHA256 != d.ContentSHA256 || proof.revision != d.Revision || len(a.checked) != 2 || a.reads != 2 {
					t.Fatalf("proof not complete: %+v %v", proof, err)
				}
			} else if err == nil || proof != (AudienceProof{}) {
				t.Fatalf("unsafe proof: %+v %v", proof, err)
			}
		})
	}
}

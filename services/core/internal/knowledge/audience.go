package knowledge

import (
	"context"
	"encoding/hex"
	"time"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

// GroupDelivery binds the proof to one immutable output and its dependencies.
// This library does not send messages or enable a channel's group route.
type GroupDelivery struct {
	TenantID      uuid.UUID
	AppID         string
	GroupID       string
	Revision      string
	ContentSHA256 string
	References    []Reference
	Recipients    []identity.Principal
}

// GroupScope must come from a trusted, complete membership enumeration. A
// partial page or an @mention list is never a complete scope.
type GroupScope struct {
	TenantID   uuid.UUID
	AppID      string
	GroupID    string
	Version    string
	Complete   bool
	ValidUntil time.Time
	Recipients []identity.Principal
}

type AudienceAuthority interface {
	Scope(context.Context, uuid.UUID, string, string) (GroupScope, error)
	// Authorize rechecks current identity and access to every dependency, and
	// records the read decision. Errors (including audit errors) withhold proof.
	Authorize(context.Context, GroupDelivery, identity.Principal) error
}

// AudienceProof is a point-in-time receipt, not a reusable grant or a promise
// that membership cannot change after this call. Its contents are not mutable.
type AudienceProof struct {
	contentSHA256 string
	revision      string
	scopeVersion  string
	verifiedAt    time.Time
}

func VerifyGroupAudience(ctx context.Context, authority AudienceAuthority, delivery GroupDelivery, now func() time.Time) (AudienceProof, error) {
	deny := AudienceProof{}
	digest, err := hex.DecodeString(delivery.ContentSHA256)
	if authority == nil || now == nil || delivery.TenantID == uuid.Nil || !label(delivery.AppID, 128) || !label(delivery.GroupID, 256) || !label(delivery.Revision, 128) || err != nil || len(digest) != 32 || len(delivery.References) > 50 {
		return deny, ErrInvalid
	}
	wanted, ok := audienceMembers(delivery.TenantID, delivery.Recipients)
	if !ok {
		return deny, ErrAudienceUnconfirmed
	}
	for _, ref := range delivery.References {
		if err := ref.Validate(); err != nil {
			return deny, err
		}
	}
	// Own the input slices so a provider cannot replace caller data in place.
	delivery.Recipients = append([]identity.Principal(nil), delivery.Recipients...)
	delivery.References = append([]Reference(nil), delivery.References...)
	check := func(scope GroupScope) bool {
		actual, valid := audienceMembers(delivery.TenantID, scope.Recipients)
		if !valid || !scope.Complete || scope.TenantID != delivery.TenantID || scope.AppID != delivery.AppID || scope.GroupID != delivery.GroupID || !label(scope.Version, 128) || !now().Before(scope.ValidUntil) || len(actual) != len(wanted) {
			return false
		}
		for member := range wanted {
			if !actual[member] {
				return false
			}
		}
		return true
	}
	if err := ctx.Err(); err != nil {
		return deny, err
	}
	before, err := authority.Scope(ctx, delivery.TenantID, delivery.AppID, delivery.GroupID)
	if err != nil {
		return deny, err
	}
	if !check(before) {
		return deny, ErrAudienceUnconfirmed
	}
	for _, recipient := range delivery.Recipients {
		if err := ctx.Err(); err != nil {
			return deny, err
		}
		request := delivery
		request.Recipients = append([]identity.Principal(nil), delivery.Recipients...)
		request.References = append([]Reference(nil), delivery.References...)
		if err := authority.Authorize(ctx, request, recipient); err != nil {
			return deny, err
		}
	}
	after, err := authority.Scope(ctx, delivery.TenantID, delivery.AppID, delivery.GroupID)
	if err != nil {
		return deny, err
	}
	if err := ctx.Err(); err != nil {
		return deny, err
	}
	if !check(after) || after.Version != before.Version {
		return deny, ErrAudienceUnconfirmed
	}
	return AudienceProof{delivery.ContentSHA256, delivery.Revision, after.Version, now()}, nil
}

func audienceMembers(tenant uuid.UUID, recipients []identity.Principal) (map[uuid.UUID]bool, bool) {
	if len(recipients) == 0 || len(recipients) > 1000 {
		return nil, false
	}
	seen := make(map[uuid.UUID]bool, len(recipients))
	for _, p := range recipients {
		if p.Validate() != nil || p.TenantID != tenant || seen[p.MemberID] {
			return nil, false
		}
		seen[p.MemberID] = true
	}
	return seen, true
}

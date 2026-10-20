package identity

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

var ErrUnauthenticated = errors.New("identity or tenant membership is not active")
var ErrInvalidIdentity = errors.New("identity fields must be non-empty and canonical")
var ErrForbidden = errors.New("operation capability denied")

// ExternalIdentity is a mapping key, NOT proof of authentication. Only a verified
// channel/OIDC adapter may supply it; TenantID comes from trusted installation config.
type ExternalIdentity struct {
	TenantID uuid.UUID
	Provider string
	Issuer   string
	Subject  string
}

func (e ExternalIdentity) Validate() error {
	if e.TenantID == uuid.Nil {
		return ErrInvalidIdentity
	}
	for _, value := range []string{e.Provider, e.Issuer, e.Subject} {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsRune(value, 0) {
			return ErrInvalidIdentity
		}
	}
	return nil
}

// Principal identifies a mapped employee. Each protected transaction must recheck it.
type Principal struct {
	TenantID uuid.UUID
	MemberID uuid.UUID
}

func (p Principal) Validate() error {
	if p.TenantID == uuid.Nil || p.MemberID == uuid.Nil {
		return ErrInvalidIdentity
	}
	return nil
}

package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"agentos.local/core/internal/identity"
	"github.com/google/uuid"
)

type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

// Resolve checks the complete mapping and active membership in one snapshot.
// It grants no business capability; each domain transaction must reauthorize.
func (r *Repository) Resolve(ctx context.Context, external identity.ExternalIdentity) (identity.Principal, error) {
	return resolve(ctx, r.db, external)
}

// ResolveIn checks identity in a domain-owned transaction. It supplies no SQL
// isolation context: the caller must still scope every business query explicitly.
func (r *Repository) ResolveIn(ctx context.Context, tx *sql.Tx, external identity.ExternalIdentity) (identity.Principal, error) {
	return resolve(ctx, tx, external)
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolve(ctx context.Context, q rowQuerier, external identity.ExternalIdentity) (identity.Principal, error) {
	if err := external.Validate(); err != nil {
		return identity.Principal{}, err
	}
	var member uuid.UUID
	err := q.QueryRowContext(ctx, `
SELECT l.member_id FROM iam_identity_links l
JOIN iam_members m ON m.tenant_id=l.tenant_id AND m.id=l.member_id
JOIN iam_tenants t ON t.id=l.tenant_id
WHERE l.tenant_id=? AND l.provider=? AND l.issuer=? AND l.subject=?
AND l.enabled=1 AND m.enabled=1 AND t.enabled=1`,
		external.TenantID.String(), external.Provider, external.Issuer, external.Subject).Scan(&member)
	if errors.Is(err, sql.ErrNoRows) {
		err = identity.ErrUnauthenticated
	}
	if err != nil {
		return identity.Principal{}, err
	}
	principal := identity.Principal{TenantID: external.TenantID, MemberID: member}
	if err := principal.Validate(); err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}

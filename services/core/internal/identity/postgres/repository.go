package postgres

import (
	"context"
	"errors"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"github.com/jackc/pgx/v5"
)

type Repository struct{ db *database.DB }

var ErrForbidden = errors.New("operation capability denied")

// WithCapability checks the current member and explicit application grant in the query transaction.
func (r *Repository) WithCapability(ctx context.Context, p identity.Principal, app, capability string, fn func(database.Queries) error) error {
	return r.Within(ctx, p, func(q database.Queries) error {
		var allowed bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.operation_grants WHERE tenant_id=$1 AND member_id=$2 AND app_id=$3 AND capability=$4 AND enabled)`, p.TenantID, p.MemberID, app, capability).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		return fn(q)
	})
}

func New(db *database.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Resolve(ctx context.Context, external identity.ExternalIdentity) (identity.Principal, error) {
	if err := external.Validate(); err != nil {
		return identity.Principal{}, err
	}
	principal := identity.Principal{TenantID: external.TenantID}
	err := r.db.Transaction(ctx, func(q database.Queries) error {
		_, err := q.Exec(ctx, `SELECT set_config('agentos.tenant_id', $1, true),
            set_config('agentos.provider', $2, true), set_config('agentos.issuer', $3, true),
            set_config('agentos.subject', $4, true)`, external.TenantID.String(), external.Provider, external.Issuer, external.Subject)
		if err != nil {
			return err
		}
		err = q.QueryRow(ctx, `SELECT member_id FROM iam.identity_links
            WHERE tenant_id=$1 AND provider=$2 AND issuer=$3 AND subject=$4`,
			external.TenantID, external.Provider, external.Issuer, external.Subject).Scan(&principal.MemberID)
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		return authorize(ctx, q, principal)
	})
	if err != nil {
		return identity.Principal{}, err
	}
	return principal, nil
}

func (r *Repository) Within(ctx context.Context, principal identity.Principal, fn func(database.Queries) error) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(q database.Queries) error {
		if err := authorize(ctx, q, principal); err != nil {
			return err
		}
		return fn(q)
	})
}

func authorize(ctx context.Context, q database.Queries, p identity.Principal) error {
	_, err := q.Exec(ctx, `SELECT set_config('agentos.tenant_id', $1, true), set_config('agentos.member_id', $2, true)`, p.TenantID.String(), p.MemberID.String())
	if err != nil {
		return err
	}
	var active bool
	err = q.QueryRow(ctx, `SELECT EXISTS (
        SELECT 1 FROM iam.members m JOIN iam.tenants t ON t.id=m.tenant_id
        WHERE m.tenant_id=$1 AND m.id=$2 AND m.enabled AND t.enabled
    )`, p.TenantID, p.MemberID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return identity.ErrUnauthenticated
	}
	return nil
}

package feishudocs

import (
	"context"
	"errors"
	"math"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrOAuthMissing  = errors.New("OAuth authorization required")
	ErrOAuthConflict = errors.New("OAuth credential generation changed")
)

// oauthLedger owns ciphertext only. Every operation resolves the bound external
// identity again. It cannot be used to transfer credentials to a remapped member.
type oauthLedger struct {
	db     *database.DB
	repo   *identitypg.Repository
	tenant uuid.UUID
	user   User
}

func newOAuthLedger(db *database.DB, tenant uuid.UUID, user User) (*oauthLedger, error) {
	if db == nil || tenant == uuid.Nil {
		return nil, ErrOAuthInvalid
	}
	if _, err := NewCredential(user, "binding-validation"); err != nil {
		return nil, ErrOAuthInvalid
	}
	return &oauthLedger{db: db, repo: identitypg.New(db), tenant: tenant, user: user}, nil
}

func (s *oauthLedger) within(ctx context.Context, fn func(database.Queries, identity.Principal) error) error {
	actor := identity.ExternalIdentity{TenantID: s.tenant, Provider: "feishu", Issuer: "feishu:" + s.user.AppID + ":" + s.user.TenantKey, Subject: s.user.OpenID}
	return s.db.Transaction(ctx, func(q database.Queries) error {
		before, err := s.repo.ResolveIn(ctx, q, actor)
		if err != nil {
			return err
		}
		if _, err = q.Exec(ctx, `SELECT set_config('agentos.app_id',$1,true)`, s.user.AppID); err != nil {
			return err
		}
		if err = fn(q, before); err != nil {
			return err
		}
		after, err := s.repo.ResolveIn(ctx, q, actor)
		if err != nil {
			return err
		}
		if before != after {
			return identity.ErrUnauthenticated
		}
		return nil
	})
}

func (s *oauthLedger) load(ctx context.Context) (int64, oauthEnvelope, error) {
	var revision int64
	var envelope oauthEnvelope
	err := s.within(ctx, func(q database.Queries, _ identity.Principal) error {
		return q.QueryRow(ctx, `SELECT revision,key_id,ciphertext FROM channel.oauth_credentials WHERE tenant_id=$1 AND app_id=$2 AND tenant_key=$3 AND open_id=$4`, s.tenant, s.user.AppID, s.user.TenantKey, s.user.OpenID).Scan(&revision, &envelope.KeyID, &envelope.Ciphertext)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrOAuthMissing
	}
	if err != nil {
		return 0, oauthEnvelope{}, err
	}
	return revision, envelope, nil
}

func (s *oauthLedger) save(ctx context.Context, expected int64, envelope oauthEnvelope, action string) error {
	if expected < 0 || expected == math.MaxInt64 || !identifier(envelope.KeyID) || len(envelope.Ciphertext) < 29 || len(envelope.Ciphertext) > maxOAuthSecrets+28 {
		return ErrOAuthInvalid
	}
	switch action {
	case "authorize", "exchange", "activate", "refresh", "revoke", "revoked":
	default:
		return ErrOAuthInvalid
	}
	return s.within(ctx, func(q database.Queries, p identity.Principal) error {
		var rows int64
		if expected == 0 {
			result, err := q.Exec(ctx, `INSERT INTO channel.oauth_credentials(tenant_id,app_id,tenant_key,open_id,member_id,revision,key_id,ciphertext) VALUES($1,$2,$3,$4,$5,1,$6,$7) ON CONFLICT DO NOTHING`, s.tenant, s.user.AppID, s.user.TenantKey, s.user.OpenID, p.MemberID, envelope.KeyID, envelope.Ciphertext)
			if err != nil {
				return err
			}
			rows = result.RowsAffected()
		} else {
			result, err := q.Exec(ctx, `UPDATE channel.oauth_credentials SET revision=revision+1,key_id=$6,ciphertext=$7,updated_at=clock_timestamp() WHERE tenant_id=$1 AND app_id=$2 AND tenant_key=$3 AND open_id=$4 AND revision=$5`, s.tenant, s.user.AppID, s.user.TenantKey, s.user.OpenID, expected, envelope.KeyID, envelope.Ciphertext)
			if err != nil {
				return err
			}
			rows = result.RowsAffected()
		}
		if rows != 1 {
			return ErrOAuthConflict
		}
		_, err := q.Exec(ctx, `INSERT INTO channel.oauth_audit(tenant_id,app_id,tenant_key,open_id,member_id,revision,action) VALUES($1,$2,$3,$4,$5,$6,$7)`, s.tenant, s.user.AppID, s.user.TenantKey, s.user.OpenID, p.MemberID, expected+1, action)
		return err
	})
}

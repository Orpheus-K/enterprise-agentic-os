package knowledge

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"context"
	"github.com/google/uuid"
	"time"
)

type HealthUpdate struct {
	SourceID   uuid.UUID `json:"source_id"`
	Revision   int64     `json:"revision"`
	State      string    `json:"state"`
	ValidUntil time.Time `json:"valid_until"`
}
type SourceHealth struct {
	SourceID   uuid.UUID `json:"source_id"`
	Revision   int64     `json:"revision"`
	Epoch      int64     `json:"epoch"`
	State      string    `json:"state"`
	ValidUntil time.Time `json:"valid_until"`
}

// Recovery changes the epoch. A heartbeat cannot revive pre-outage ACL snapshots.
func (s *Store) UpdateHealth(ctx context.Context, actor identity.ExternalIdentity, in HealthUpdate) (SourceHealth, error) {
	if in.SourceID == uuid.Nil || in.Revision < 1 || (in.State != "healthy" && in.State != "unavailable") || in.ValidUntil.IsZero() {
		return SourceHealth{}, ErrInvalid
	}
	hash, err := digest(in)
	if err != nil {
		return SourceHealth{}, err
	}
	out := SourceHealth{SourceID: in.SourceID}
	err = s.within(ctx, actor, func(q database.Queries) error {
		if err := s.syncAllowed(ctx, q, in.SourceID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `INSERT INTO knowledge.source_health(tenant_id,app_id,source_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, s.tenant, s.app, in.SourceID); err != nil {
			return err
		}
		var oldHash string
		var active bool
		if err := q.QueryRow(ctx, `SELECT revision,epoch,state,request_hash,state='healthy' AND valid_until>clock_timestamp() FROM knowledge.source_health WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 FOR UPDATE`, s.tenant, s.app, in.SourceID).Scan(&out.Revision, &out.Epoch, &out.State, &oldHash, &active); err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, in.SourceID); err != nil {
			return err
		}
		if in.Revision <= out.Revision {
			if in.Revision != out.Revision || hash != oldHash {
				return ErrConflict
			}
			return q.QueryRow(ctx, `SELECT valid_until FROM knowledge.source_health WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3`, s.tenant, s.app, in.SourceID).Scan(&out.ValidUntil)
		}
		if in.State == "healthy" {
			var fresh bool
			if err := q.QueryRow(ctx, "SELECT $1::timestamptz>clock_timestamp()", in.ValidUntil).Scan(&fresh); err != nil {
				return err
			}
			if !fresh {
				return ErrInvalid
			}
		}
		if !active || in.State != "healthy" {
			out.Epoch++
		}
		out.Revision = in.Revision
		out.State = in.State
		out.ValidUntil = in.ValidUntil
		if _, err := q.Exec(ctx, `UPDATE knowledge.source_health SET revision=$4,epoch=$5,state=$6,valid_until=$7,request_hash=$8 WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3`, s.tenant, s.app, in.SourceID, out.Revision, out.Epoch, out.State, out.ValidUntil, hash); err != nil {
			return err
		}
		return s.audit(ctx, q, in.SourceID, "", "source_health", in.Revision)
	})
	if err != nil {
		return SourceHealth{}, err
	}
	return out, nil
}

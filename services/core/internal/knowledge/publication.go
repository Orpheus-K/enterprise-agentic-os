package knowledge

import (
	"context"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
)

// Publication is a fully prepared document in one embedding space. External
// reads, permission checks and embedding calls finish before this transaction.
// ACL is still a trusted sync input, not permission inferred from app access.
type Publication struct {
	Content Content
	ACL     ACL
	Vectors VectorSet
}

func (p Publication) validate() error {
	c, a, v := p.Content, p.ACL, p.Vectors
	if c.validate() != nil || a.validate() != nil || v.validate() != nil || c.Deleted ||
		c.SourceID != a.SourceID || c.SourceID != v.SourceID ||
		c.DocumentID != a.DocumentID || c.DocumentID != v.DocumentID ||
		c.Revision != a.ContentRevision || c.Revision != v.ContentRevision {
		return ErrInvalid
	}
	return nil
}

// Publish commits content, vectors, ACL and their audit records together.
// Replaying identical inputs remains idempotent; any conflict or late failure
// rolls back all three channels, including replacement of previous content.
func (s *Store) Publish(ctx context.Context, actor identity.ExternalIdentity, p Publication) error {
	if err := p.validate(); err != nil {
		return err
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		if err := s.putContent(ctx, q, p.Content); err != nil {
			return err
		}
		if err := s.publishVectors(ctx, q, p.Vectors); err != nil {
			return err
		}
		if err := s.putACL(ctx, q, p.ACL); err != nil {
			return err
		}
		if err := s.syncAllowed(ctx, q, p.Content.SourceID); err != nil {
			return err
		}
		var current bool
		if err := q.QueryRow(ctx, `SELECT knowledge.source_ready($1,$2) AND $3::timestamptz>clock_timestamp()`,
			p.Content.SourceID, p.ACL.SourceEpoch, p.ACL.ValidUntil).Scan(&current); err != nil {
			return err
		}
		if !current {
			return ErrUnavailable
		}
		return nil
	})
}

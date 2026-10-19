package knowledge

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Store struct {
	repo   *identitypg.Repository
	tenant uuid.UUID
	app    string
}

func New(db *database.DB, tenant uuid.UUID, app string) (*Store, error) {
	if db == nil || tenant == uuid.Nil || !label(app, 256) {
		return nil, ErrInvalid
	}
	return &Store{identitypg.New(db), tenant, app}, nil
}
func (s *Store) within(ctx context.Context, actor identity.ExternalIdentity, fn func(database.Queries) error) error {
	if actor.TenantID != s.tenant {
		return identity.ErrUnauthenticated
	}
	p, err := s.repo.Resolve(ctx, actor)
	if err != nil {
		return err
	}
	return s.repo.Within(ctx, p, func(q database.Queries) error {
		_, err := q.Exec(ctx, `SELECT set_config('agentos.app_id',$1,true),set_config('agentos.provider',$2,true),set_config('agentos.issuer',$3,true),set_config('agentos.subject',$4,true)`, s.app, actor.Provider, actor.Issuer, actor.Subject)
		if err != nil {
			return err
		}
		return fn(q)
	})
}
func digest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func (s *Store) syncAllowed(ctx context.Context, q database.Queries, source uuid.UUID) error {
	var ok bool
	if err := q.QueryRow(ctx, "SELECT knowledge.can_sync($1)", source).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return identitypg.ErrForbidden
	}
	return nil
}
func (s *Store) lock(ctx context.Context, q database.Queries, source uuid.UUID, doc string) error {
	if err := s.syncAllowed(ctx, q, source); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO knowledge.documents(tenant_id,app_id,source_id,id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, s.tenant, s.app, source, doc); err != nil {
		return err
	}
	var id string
	if err := q.QueryRow(ctx, `SELECT id FROM knowledge.documents WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND id=$4 FOR UPDATE`, s.tenant, s.app, source, doc).Scan(&id); err != nil {
		return err
	}
	return s.syncAllowed(ctx, q, source)
}
func (s *Store) audit(ctx context.Context, q database.Queries, source uuid.UUID, doc, action string, revision int64) error {
	_, err := q.Exec(ctx, `INSERT INTO knowledge.audit(id,tenant_id,app_id,source_id,document_id,member_id,action,revision) VALUES($1,$2,$3,$4,$5,current_setting('agentos.member_id')::uuid,$6,$7)`, uuid.New(), s.tenant, s.app, source, doc, action, revision)
	return err
}
func (s *Store) PutContent(ctx context.Context, actor identity.ExternalIdentity, c Content) error {
	if err := c.validate(); err != nil {
		return err
	}
	hash, err := digest(c)
	if err != nil {
		return err
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		if err := s.lock(ctx, q, c.SourceID, c.DocumentID); err != nil {
			return err
		}
		var revision int64
		var old string
		if err := q.QueryRow(ctx, `SELECT content_revision,content_hash FROM knowledge.documents WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND id=$4`, s.tenant, s.app, c.SourceID, c.DocumentID).Scan(&revision, &old); err != nil {
			return err
		}
		if c.Revision <= revision {
			if c.Revision == revision && hash == old {
				return nil
			}
			return ErrConflict
		}
		if _, err := q.Exec(ctx, `UPDATE knowledge.documents SET content_revision=$5,content_hash=$6,title=$7,deleted=$8 WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND id=$4`, s.tenant, s.app, c.SourceID, c.DocumentID, c.Revision, hash, c.Title, c.Deleted); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM knowledge.chunks WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4`, s.tenant, s.app, c.SourceID, c.DocumentID); err != nil {
			return err
		}
		for _, chunk := range c.Chunks {
			if _, err := q.Exec(ctx, `INSERT INTO knowledge.chunks VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, s.tenant, s.app, c.SourceID, c.DocumentID, chunk.ID, c.Revision, chunk.Body, chunk.Anchor); err != nil {
				return err
			}
		}
		return s.audit(ctx, q, c.SourceID, c.DocumentID, "content", c.Revision)
	})
}
func (s *Store) PutACL(ctx context.Context, actor identity.ExternalIdentity, a ACL) error {
	if err := a.validate(); err != nil {
		return err
	}
	hash, err := digest(a)
	if err != nil {
		return err
	}
	// A present empty allow list means deny all; nil means inherit the document.
	chunks := map[string]map[string]any{}
	for id, c := range a.Chunks {
		v := map[string]any{"deny": c.Deny}
		if c.Allow != nil {
			v["allow"] = c.Allow
		}
		chunks[id] = v
	}
	raw, err := json.Marshal(chunks)
	if err != nil {
		return err
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		if err := s.lock(ctx, q, a.SourceID, a.DocumentID); err != nil {
			return err
		}
		var revision int64
		var old string
		if err := q.QueryRow(ctx, `SELECT acl_revision,acl_hash FROM knowledge.documents WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND id=$4`, s.tenant, s.app, a.SourceID, a.DocumentID).Scan(&revision, &old); err != nil {
			return err
		}
		if a.Revision <= revision {
			if a.Revision == revision && hash == old {
				return nil
			}
			return ErrConflict
		}
		allow, deny := a.Allow, a.Deny
		if allow == nil {
			allow = []uuid.UUID{}
		}
		if deny == nil {
			deny = []uuid.UUID{}
		}
		if _, err := q.Exec(ctx, `UPDATE knowledge.documents SET acl_revision=$5,acl_hash=$6,acl_content_revision=$7,allow_members=$8,deny_members=$9,chunk_acl=$10,valid_until=$11 WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND id=$4`, s.tenant, s.app, a.SourceID, a.DocumentID, a.Revision, hash, a.ContentRevision, allow, deny, raw, a.ValidUntil); err != nil {
			return err
		}
		return s.audit(ctx, q, a.SourceID, a.DocumentID, "acl", a.Revision)
	})
}

func (s *Store) Read(ctx context.Context, actor identity.ExternalIdentity, ref Reference) (Evidence, error) {
	if ref.SourceID == uuid.Nil || !label(ref.DocumentID, 256) || !label(ref.ChunkID, 128) || ref.ContentRevision < 1 || ref.ACLRevision < 1 {
		return Evidence{}, ErrInvalid
	}
	out := Evidence{Reference: ref}
	err := s.within(ctx, actor, func(q database.Queries) error {
		err := q.QueryRow(ctx, `SELECT d.title,c.body,c.anchor FROM knowledge.chunks c JOIN knowledge.documents d ON (d.tenant_id,d.app_id,d.source_id,d.id)=(c.tenant_id,c.app_id,c.source_id,c.document_id) WHERE c.tenant_id=$1 AND c.app_id=$2 AND c.source_id=$3 AND c.document_id=$4 AND c.id=$5 AND c.content_revision=$6 AND d.acl_revision=$7 AND d.acl_content_revision=$6 AND NOT d.deleted AND d.valid_until>clock_timestamp() AND current_setting('agentos.member_id')::uuid=ANY(d.allow_members) AND NOT current_setting('agentos.member_id')::uuid=ANY(d.deny_members) AND coalesce((d.chunk_acl->c.id->'allow') ? current_setting('agentos.member_id'),true) AND NOT coalesce((d.chunk_acl->c.id->'deny') ? current_setting('agentos.member_id'),false)`, s.tenant, s.app, ref.SourceID, ref.DocumentID, ref.ChunkID, ref.ContentRevision, ref.ACLRevision).Scan(&out.Title, &out.Body, &out.Anchor)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnavailable
		}
		if err != nil {
			return err
		}
		return s.audit(ctx, q, ref.SourceID, ref.DocumentID, "read", ref.ACLRevision)
	})
	if err != nil {
		return Evidence{}, err
	}
	return out, nil
}

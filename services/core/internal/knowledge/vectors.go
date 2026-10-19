package knowledge

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"math"
	"slices"
	"strconv"
	"strings"
)

const (
	maxDimensions = 4096
	maxCandidates = 50
)

// Space identifies the administrator-configured embedding model a vector was computed with.
// Vectors from a different model, revision or dimension are rejected, never adapted.
type Space struct {
	ID            uuid.UUID `json:"space_id"`
	Model         string    `json:"model"`
	ModelRevision string    `json:"model_revision"`
}
type ChunkVector struct {
	ChunkID   string    `json:"chunk_id"`
	Embedding []float32 `json:"embedding"`
}

// VectorSet must cover every chunk of exactly the current content revision.
type VectorSet struct {
	Space
	SourceID        uuid.UUID     `json:"source_id"`
	DocumentID      string        `json:"document_id"`
	ContentRevision int64         `json:"content_revision"`
	Vectors         []ChunkVector `json:"vectors"`
}
type SearchQuery struct {
	Space
	Embedding []float32 `json:"embedding"`
	Limit     int       `json:"limit"`
}

// Candidate carries no text: consumers must call Read, which re-checks current authorization.
type Candidate struct {
	Reference Reference `json:"reference"`
	Distance  float64   `json:"distance"`
}

func (s Space) validate() bool {
	return s.ID != uuid.Nil && label(s.Model, 256) && label(s.ModelRevision, 256)
}

// Zero vectors have no cosine direction; non-finite values cannot be ranked.
func embedding(v []float32) bool {
	if len(v) == 0 || len(v) > maxDimensions {
		return false
	}
	nonzero := false
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return false
		}
		nonzero = nonzero || x != 0
	}
	return nonzero
}
func literal(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
func (v VectorSet) validate() error {
	if !v.Space.validate() || v.SourceID == uuid.Nil || !label(v.DocumentID, 256) || v.ContentRevision < 1 || len(v.Vectors) == 0 || len(v.Vectors) > 256 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range v.Vectors {
		if !label(c.ChunkID, 128) || seen[c.ChunkID] || !embedding(c.Embedding) || len(c.Embedding) != len(v.Vectors[0].Embedding) {
			return ErrInvalid
		}
		seen[c.ChunkID] = true
	}
	return nil
}
func (q SearchQuery) validate() error {
	if !q.Space.validate() || !embedding(q.Embedding) || q.Limit < 1 || q.Limit > maxCandidates {
		return ErrInvalid
	}
	return nil
}

// space resolves an enabled space visible to the current installation.
func (s *Store) space(ctx context.Context, q database.Queries, in Space, dimensions int) error {
	var model, revision string
	var want int
	err := q.QueryRow(ctx, `SELECT model,model_revision,dimensions FROM knowledge.embedding_spaces WHERE tenant_id=$1 AND app_id=$2 AND id=$3`, s.tenant, s.app, in.ID).Scan(&model, &revision, &want)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnavailable
	}
	if err != nil {
		return err
	}
	if model != in.Model || revision != in.ModelRevision {
		return ErrConflict
	}
	if want != dimensions {
		return ErrInvalid
	}
	return nil
}

// PublishVectors atomically replaces the document's vectors in one space. ACL changes do not
// touch vectors; a new content revision removes them through the chunk cascade.
func (s *Store) PublishVectors(ctx context.Context, actor identity.ExternalIdentity, in VectorSet) error {
	if err := in.validate(); err != nil {
		return err
	}
	hash, err := digest(in)
	if err != nil {
		return err
	}
	return s.within(ctx, actor, func(q database.Queries) error {
		if err := s.syncAllowed(ctx, q, in.SourceID); err != nil {
			return err
		}
		var revision int64
		var deleted bool
		err := q.QueryRow(ctx, `SELECT content_revision,deleted FROM knowledge.documents WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND id=$4 FOR UPDATE`, s.tenant, s.app, in.SourceID, in.DocumentID).Scan(&revision, &deleted)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		// Recheck after the lock wait: the sync identity may have been revoked meanwhile.
		if err := s.syncAllowed(ctx, q, in.SourceID); err != nil {
			return err
		}
		if deleted || revision != in.ContentRevision {
			return ErrConflict
		}
		if err := s.space(ctx, q, in.Space, len(in.Vectors[0].Embedding)); err != nil {
			return err
		}
		var published int64
		var old string
		err = q.QueryRow(ctx, `SELECT content_revision,request_hash FROM knowledge.vector_sets WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 AND space_id=$5`, s.tenant, s.app, in.SourceID, in.DocumentID, in.ID).Scan(&published, &old)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case published == in.ContentRevision && old == hash:
			return nil
		case published == in.ContentRevision:
			return ErrConflict
		}
		var chunks []string
		if err := q.QueryRow(ctx, `SELECT coalesce(array_agg(id ORDER BY id),'{}') FROM knowledge.chunks WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 AND content_revision=$5`, s.tenant, s.app, in.SourceID, in.DocumentID, in.ContentRevision).Scan(&chunks); err != nil {
			return err
		}
		given := make([]string, len(in.Vectors))
		for i, v := range in.Vectors {
			given[i] = v.ChunkID
		}
		slices.Sort(given)
		if !slices.Equal(chunks, given) {
			return ErrInvalid
		}
		if _, err := q.Exec(ctx, `DELETE FROM knowledge.chunk_vectors WHERE tenant_id=$1 AND app_id=$2 AND source_id=$3 AND document_id=$4 AND space_id=$5`, s.tenant, s.app, in.SourceID, in.DocumentID, in.ID); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `INSERT INTO knowledge.vector_sets VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,app_id,source_id,document_id,space_id) DO UPDATE SET content_revision=EXCLUDED.content_revision,request_hash=EXCLUDED.request_hash`, s.tenant, s.app, in.SourceID, in.DocumentID, in.ID, in.ContentRevision, hash); err != nil {
			return err
		}
		for _, v := range in.Vectors {
			if _, err := q.Exec(ctx, `INSERT INTO knowledge.chunk_vectors VALUES($1,$2,$3,$4,$5,$6,$7,$8,CAST($9::text AS public.vector))`, s.tenant, s.app, in.SourceID, in.DocumentID, v.ChunkID, in.ID, len(v.Embedding), in.ContentRevision, literal(v.Embedding)); err != nil {
				return err
			}
		}
		return s.audit(ctx, q, in.SourceID, in.DocumentID, "vectors", in.ContentRevision)
	})
}

// Search ranks by exact cosine distance after authorization inside the same query, so
// unauthorized rows can neither displace nor reveal authorized ones. No ANN index is used.
func (s *Store) Search(ctx context.Context, actor identity.ExternalIdentity, in SearchQuery) ([]Candidate, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	var out []Candidate
	err := s.within(ctx, actor, func(q database.Queries) error {
		if err := s.space(ctx, q, in.Space, len(in.Embedding)); err != nil {
			return err
		}
		rows, err := q.Query(ctx, `SELECT c.source_id,c.document_id,c.id,c.content_revision,d.acl_revision,v.embedding OPERATOR(public.<=>) CAST($4::text AS public.vector) AS distance
 FROM knowledge.chunk_vectors v
 JOIN knowledge.vector_sets s ON (s.tenant_id,s.app_id,s.source_id,s.document_id,s.space_id)=(v.tenant_id,v.app_id,v.source_id,v.document_id,v.space_id) AND s.content_revision=v.content_revision
 JOIN knowledge.chunks c ON (c.tenant_id,c.app_id,c.source_id,c.document_id,c.id)=(v.tenant_id,v.app_id,v.source_id,v.document_id,v.chunk_id) AND c.content_revision=v.content_revision
 JOIN knowledge.documents d ON (d.tenant_id,d.app_id,d.source_id,d.id)=(c.tenant_id,c.app_id,c.source_id,c.document_id)
 WHERE v.tenant_id=$1 AND v.app_id=$2 AND v.space_id=$3 AND `+visibleChunk+`
 ORDER BY distance,c.source_id,c.document_id,c.id LIMIT $5`, s.tenant, s.app, in.ID, literal(in.Embedding), in.Limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candidate, error) {
			var c Candidate
			err := row.Scan(&c.Reference.SourceID, &c.Reference.DocumentID, &c.Reference.ChunkID, &c.Reference.ContentRevision, &c.Reference.ACLRevision, &c.Distance)
			return c, err
		})
		if err != nil {
			return err
		}
		_, err = q.Exec(ctx, `INSERT INTO knowledge.search_audit(id,tenant_id,app_id,space_id,member_id,requested,returned) VALUES($1,$2,$3,$4,current_setting('agentos.member_id')::uuid,$5,$6)`, uuid.New(), s.tenant, s.app, in.ID, in.Limit, len(out))
		return err
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Candidate{}
	}
	return out, nil
}

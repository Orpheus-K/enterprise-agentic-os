// Package knowledge owns versioned source content and effective access snapshots.
package knowledge

import (
	"errors"
	"github.com/google/uuid"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid knowledge input")
var ErrConflict = errors.New("knowledge version conflict")
var ErrUnavailable = errors.New("knowledge reference unavailable")

type Chunk struct {
	ID     string
	Body   string
	Anchor string
}
type Content struct {
	SourceID   uuid.UUID
	DocumentID string
	Revision   int64
	Title      string
	Deleted    bool
	Chunks     []Chunk
}
type ChunkACL struct {
	Allow []uuid.UUID `json:"allow"`
	Deny  []uuid.UUID `json:"deny"`
}
type ACL struct {
	SourceID        uuid.UUID
	DocumentID      string
	Revision        int64
	ContentRevision int64
	Allow           []uuid.UUID
	Deny            []uuid.UUID
	Chunks          map[string]ChunkACL
	ValidUntil      time.Time
}
type Reference struct {
	SourceID        uuid.UUID
	DocumentID      string
	ChunkID         string
	ContentRevision int64
	ACLRevision     int64
}
type Evidence struct {
	Reference Reference
	Title     string
	Body      string
	Anchor    string
}

func label(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.TrimSpace(s) == s && !strings.ContainsRune(s, 0)
}
func members(ids []uuid.UUID) bool {
	if len(ids) > 1000 {
		return false
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func (c Content) validate() error {
	if c.SourceID == uuid.Nil || !label(c.DocumentID, 256) || c.Revision < 1 || len(c.Chunks) > 256 {
		return ErrInvalid
	}
	if c.Deleted {
		if c.Title != "" || len(c.Chunks) != 0 {
			return ErrInvalid
		}
		return nil
	}
	if !label(c.Title, 512) || len(c.Chunks) == 0 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, chunk := range c.Chunks {
		if !label(chunk.ID, 128) || !label(chunk.Body, 16000) || !label(chunk.Anchor, 512) || seen[chunk.ID] {
			return ErrInvalid
		}
		seen[chunk.ID] = true
	}
	return nil
}
func (a ACL) validate() error {
	if a.SourceID == uuid.Nil || !label(a.DocumentID, 256) || a.Revision < 1 || a.ContentRevision < 1 || a.ValidUntil.IsZero() || !members(a.Allow) || !members(a.Deny) || len(a.Chunks) > 256 {
		return ErrInvalid
	}
	for id, c := range a.Chunks {
		if !label(id, 128) || !members(c.Allow) || !members(c.Deny) {
			return ErrInvalid
		}
	}
	return nil
}

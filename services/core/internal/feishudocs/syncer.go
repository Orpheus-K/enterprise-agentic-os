package feishudocs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"github.com/google/uuid"
)

// Syncer executes one persisted document request. Construction and scheduling
// never contact the source or embedding provider.
type Syncer struct {
	reader     *MappedReader
	store      *knowledge.Store
	embedder   *model.Embedder
	source     uuid.UUID
	owner      Credential
	recipients []Credential
	space      knowledge.Space
	hash       string
}

func NewSyncer(reader *MappedReader, store *knowledge.Store, source uuid.UUID, owner Credential, recipients []Credential, embedding model.EmbeddingConfig, key string, space knowledge.Space) (*Syncer, error) {
	if reader == nil || store == nil || source == uuid.Nil || space.ID == uuid.Nil ||
		space.Model != embedding.Name || space.ModelRevision != embedding.Revision || len(recipients) > 1000 {
		return nil, ErrInvalid
	}
	users := make([]User, 0, len(recipients))
	seen := map[User]bool{}
	for _, c := range append([]Credential{owner}, recipients...) {
		if reader.reader.validate(c, "configuration") != nil || c.user.TenantKey != reader.tenantKey {
			return nil, ErrInvalid
		}
	}
	for _, c := range recipients {
		if seen[c.user] {
			return nil, ErrInvalid
		}
		seen[c.user] = true
		users = append(users, c.user)
	}
	embedder, err := model.NewEmbedder(embedding, key)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(struct {
		Version    string
		Source     uuid.UUID
		Owner      User
		Recipients []User
		Embedding  model.EmbeddingConfig
		Space      knowledge.Space
	}{"feishu-docx-sync-v1", source, owner.user, users, embedding, space})
	if err != nil {
		return nil, ErrInvalid
	}
	hash := sha256.Sum256(raw)
	return &Syncer{reader: reader, store: store, embedder: embedder, source: source, owner: owner,
		recipients: append([]Credential(nil), recipients...), space: space, hash: hex.EncodeToString(hash[:])}, nil
}

func (s *Syncer) ConfigHash() string { return s.hash }

func proof(access Access) knowledge.MemberProof {
	return knowledge.MemberProof{Actor: access.Actor, Principal: access.Principal}
}

func sameContent(a, b knowledge.Content) bool {
	left, e1 := json.Marshal(a)
	right, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && sha256.Sum256(left) == sha256.Sum256(right)
}

// RunOnce returns false only when this configuration has no claimable work.
// Source/model failures are explicit and mark source health unavailable where
// the lease still permits it. Unknown embedding calls remain fenced pending.
func (s *Syncer) RunOnce(ctx context.Context) (worked bool, result error) {
	owner, err := s.reader.resolve(ctx, s.owner, "configuration")
	if err != nil {
		return false, err
	}
	lease, err := s.store.ClaimSync(ctx, owner.Actor, s.source, s.hash, 5*time.Minute)
	if errors.Is(err, knowledge.ErrUnavailable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Keep a bounded cleanup window for a provider cancellation/failure.
	defer func() {
		if result != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, healthErr := s.store.ReportSyncHealth(cleanup, owner.Actor, lease, false, time.Now().UTC())
			result = errors.Join(result, healthErr)
		}
	}()
	ctx, cancel := context.WithDeadline(ctx, lease.Until)
	defer cancel()
	checkpoint, err := s.store.LoadSyncEmbedding(ctx, owner.Actor, lease)
	haveCheckpoint := err == nil
	if err != nil && !errors.Is(err, knowledge.ErrUnavailable) {
		return true, err
	}
	snapshot, initialAccess, err := s.reader.Read(ctx, s.owner, lease.DocumentID)
	if err != nil {
		return true, err
	}
	if initialAccess != owner {
		return true, ErrDenied
	}
	content, err := Normalize(s.source, snapshot)
	if err != nil {
		return true, err
	}
	vectors := checkpoint.Vectors
	if haveCheckpoint {
		if vectors.Space != s.space {
			return true, ErrChanged
		}
		if !sameContent(checkpoint.Content, content) {
			if err := s.store.RefreshSyncEmbedding(ctx, owner.Actor, lease, content); err != nil {
				return true, err
			}
			haveCheckpoint = false
		}
	}
	if !haveCheckpoint {
		if err := s.store.BeginSyncEmbedding(ctx, owner.Actor, lease, content, s.space); err != nil {
			return true, err
		}
		vectors = knowledge.VectorSet{Space: s.space, SourceID: s.source, DocumentID: content.DocumentID, ContentRevision: content.Revision}
		for _, chunk := range content.Chunks {
			vector, err := s.embedder.Embed(ctx, chunk.Body)
			if err != nil {
				return true, ErrRemote
			}
			vectors.Vectors = append(vectors.Vectors, knowledge.ChunkVector{ChunkID: chunk.ID, Embedding: vector})
		}
		if err := s.store.SaveSyncEmbedding(ctx, owner.Actor, lease, vectors); err != nil {
			return true, err
		}
	}
	// Permissions are checked after embedding, with a bounded validity window
	// measured before the first final source check, never extended per user.
	until := time.Now().UTC().Add(time.Minute)
	finalSnapshot, finalOwner, err := s.reader.Read(ctx, s.owner, lease.DocumentID)
	if err != nil {
		return true, err
	}
	finalContent, err := Normalize(s.source, finalSnapshot)
	if err != nil || !sameContent(content, finalContent) || finalOwner != owner {
		return true, ErrChanged
	}
	var recipients []knowledge.MemberProof
	var allow []uuid.UUID
	seenMembers := map[uuid.UUID]bool{}
	for _, credential := range s.recipients {
		access, err := s.reader.CheckAccess(ctx, credential, lease.DocumentID)
		if errors.Is(err, ErrDenied) || errors.Is(err, identity.ErrUnauthenticated) {
			continue
		}
		if err != nil {
			return true, err
		}
		if seenMembers[access.Principal.MemberID] {
			return true, ErrInvalid
		}
		seenMembers[access.Principal.MemberID] = true
		allow = append(allow, access.Principal.MemberID)
		recipients = append(recipients, proof(access))
	}
	if !time.Now().Before(until) {
		return true, ErrRemote
	}
	health, err := s.store.ReportSyncHealth(ctx, owner.Actor, lease, true, until)
	if err != nil {
		return true, err
	}
	acl := knowledge.ACL{SourceID: s.source, DocumentID: content.DocumentID, Revision: lease.RequestRevision,
		ContentRevision: content.Revision, SourceEpoch: health.Epoch, Allow: allow, ValidUntil: until}
	return true, s.store.PublishSyncForMembers(ctx, proof(owner), lease,
		knowledge.Publication{Content: content, ACL: acl, Vectors: vectors}, recipients)
}

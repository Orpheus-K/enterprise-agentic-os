package quotation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"slices"
	"time"
)

var ErrSealDenied = errors.New("seal policy or document binding denied")

// SealPolicy comes from the administrator's current policy store, never a model.
// IAM quotation.seal and publication checks remain the caller's responsibility.
type SealPolicy struct {
	TenantID     uuid.UUID   `json:"tenant_id"`
	AppID        string      `json:"app_id"`
	SealID       string      `json:"seal_id"`
	Revision     string      `json:"revision"`
	Members      []uuid.UUID `json:"members"`
	MaximumMinor int64       `json:"maximum_minor"`
	NotBefore    time.Time   `json:"not_before"`
	ExpiresAt    time.Time   `json:"expires_at"`
}

func NewSealPolicy(p SealPolicy) (SealPolicy, error) {
	if p.TenantID == uuid.Nil || !label(p.AppID) || !label(p.SealID) || p.MaximumMinor < 1 || p.MaximumMinor > MaxMinor || len(p.Members) == 0 || len(p.Members) > 128 || p.NotBefore.IsZero() || !p.ExpiresAt.After(p.NotBefore) {
		return SealPolicy{}, ErrSealDenied
	}
	p.Members = slices.Clone(p.Members)
	slices.SortFunc(p.Members, func(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) })
	for i, member := range p.Members {
		if member == uuid.Nil || (i > 0 && member == p.Members[i-1]) {
			return SealPolicy{}, ErrSealDenied
		}
	}
	p.NotBefore = p.NotBefore.UTC()
	p.ExpiresAt = p.ExpiresAt.UTC()
	p.Revision = ""
	var err error
	p.Revision, err = sealHash(p)
	if err != nil {
		return SealPolicy{}, errors.Join(ErrSealDenied, err)
	}
	return p, nil
}

type SealBinding struct {
	TenantID       uuid.UUID `json:"tenant_id"`
	AppID          string    `json:"app_id"`
	SealID         string    `json:"seal_id"`
	PolicyRevision string    `json:"policy_revision"`
	MemberID       uuid.UUID `json:"member_id"`
	QuoteID        uuid.UUID `json:"quote_id"`
	QuoteVersion   int64     `json:"quote_version"`
	QuoteHash      string    `json:"quote_hash"`
	PDFHash        string    `json:"pdf_hash"`
	EvidenceHash   string    `json:"evidence_hash"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// BindSeal records a decision against current, already authenticated policy inputs.
// It does not sign, publish, or treat a hash as proof of authorization.
func BindSeal(p SealPolicy, tenant, member uuid.UUID, app string, d Draft, pdfHash, evidenceHash string, now time.Time) (SealBinding, error) {
	canonical, err := NewSealPolicy(p)
	if err != nil || p.Revision != canonical.Revision || tenant != p.TenantID || app != p.AppID || !slices.Contains(p.Members, member) || now.Before(p.NotBefore) || !now.Before(p.ExpiresAt) || Verify(d) != nil || d.TotalMinor > p.MaximumMinor || !revision(pdfHash) || !revision(evidenceHash) {
		return SealBinding{}, ErrSealDenied
	}
	return SealBinding{TenantID: tenant, AppID: app, SealID: p.SealID, PolicyRevision: p.Revision, MemberID: member, QuoteID: d.ID, QuoteVersion: d.Version, QuoteHash: d.Hash, PDFHash: pdfHash, EvidenceHash: evidenceHash, ExpiresAt: canonical.ExpiresAt}, nil
}

func (b SealBinding) Digest() (string, error) { return sealHash(b) }

func CheckSealBinding(p SealPolicy, b SealBinding, d Draft, pdfHash string, now time.Time) error {
	expected, err := BindSeal(p, b.TenantID, b.MemberID, b.AppID, d, pdfHash, b.EvidenceHash, now)
	if err != nil {
		return ErrSealDenied
	}
	want, wantErr := expected.Digest()
	got, gotErr := b.Digest()
	if wantErr != nil || gotErr != nil || want != got {
		return ErrSealDenied
	}
	return nil
}

func sealHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

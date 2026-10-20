//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/quotation"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type sealObjects struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (o *sealObjects) Put(_ context.Context, tenant, attempt uuid.UUID, data []byte) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ref := attempt.String()
	key := tenant.String() + ":" + ref
	if _, exists := o.data[key]; exists {
		return "", errors.New("duplicate immutable write")
	}
	o.data[key] = bytes.Clone(data)
	return ref, nil
}
func (o *sealObjects) Get(_ context.Context, tenant uuid.UUID, ref string, limit int64) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	data, ok := o.data[tenant.String()+":"+ref]
	if !ok || int64(len(data)) > limit {
		return nil, errors.New("object unavailable")
	}
	return bytes.Clone(data), nil
}

type sealFunc func(context.Context, quotation.SealBinding, []byte) ([]byte, error)

func (f sealFunc) Seal(ctx context.Context, b quotation.SealBinding, data []byte) ([]byte, error) {
	return f(ctx, b, data)
}

func readySeal(t *testing.T) (*approvalFixture, conversation.QuoteSealRequest, *sealObjects) {
	t.Helper()
	f, a, d := quotePending(t)
	approvalSQL(t, f, `INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'quotation.publish'),($1,$2,$3,'quotation.seal')`, testBinding().TenantID, f.approver, testBinding().AppID)
	if _, err := f.store.DecideApproval(t.Context(), f.actor, decision(a)); err != nil {
		t.Fatal(err)
	}
	font, err := os.ReadFile("../quotation/testdata/quote-fixture.ttf")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(font)
	renderer, err := quotation.NewPDFRenderer(font, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	document := conversation.QuoteDocument{TaskID: d.ID, Version: d.Version, ContentHash: d.Hash, RendererHash: renderer.Revision()}
	objects := &sealObjects{data: map[string][]byte{}}
	pdf, err := f.store.GenerateQuotePDF(t.Context(), f.actor, document, renderer, objects)
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(pdf)
	document.PDFHash = hex.EncodeToString(sum[:])
	now := time.Now().UTC()
	policy, err := quotation.NewSealPolicy(quotation.SealPolicy{TenantID: testBinding().TenantID, AppID: testBinding().AppID, SealID: "synthetic-seal", Members: []uuid.UUID{f.approver}, MaximumMinor: d.TotalMinor, NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	approvalSQL(t, f, `INSERT INTO channel.seal_policies(tenant_id,app_id,seal_id,revision,policy) VALUES($1,$2,$3,$4,$5)`, policy.TenantID, policy.AppID, policy.SealID, policy.Revision, policy)
	return f, conversation.QuoteSealRequest{Document: document, SealID: policy.SealID, PolicyRevision: policy.Revision, EvidenceHash: strings.Repeat("e", 64)}, objects
}

func TestQuoteSealSyntheticOnceAndRevocation(t *testing.T) {
	f, in, objects := readySeal(t)
	calls := 0
	signer := sealFunc(func(_ context.Context, b quotation.SealBinding, data []byte) ([]byte, error) {
		calls++
		digest, err := b.Digest()
		if err != nil {
			return nil, err
		}
		return append(data, []byte("\n% SYNTHETIC ONLY "+digest+"\n")...), nil
	})
	first, err := f.store.SealQuotePDF(t.Context(), f.actor, in, signer, objects)
	if err != nil {
		t.Fatal(err)
	}
	restarted := conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	second, err := restarted.SealQuotePDF(t.Context(), f.actor, in, signer, objects)
	if err != nil || calls != 1 || !bytes.Equal(first, second) {
		t.Fatal("repeated signer", calls, err)
	}
	var events int
	if err := f.pg.Admin.QueryRow(t.Context(), `SELECT count(*) FROM channel.quote_seal_events`).Scan(&events); err != nil || events != 4 {
		t.Fatal("missing audit", events, err)
	}
	approvalSQL(t, f, `UPDATE channel.seal_policies SET enabled=false`)
	if _, err := restarted.SealQuotePDF(t.Context(), f.actor, in, signer, objects); !errors.Is(err, quotation.ErrSealDenied) {
		t.Fatal("disabled seal accepted", err)
	}
	if calls != 1 {
		t.Fatal("revocation invoked signer")
	}
}

func TestQuoteSealUnknownDoesNotReplay(t *testing.T) {
	f, in, objects := readySeal(t)
	calls := 0
	signer := sealFunc(func(context.Context, quotation.SealBinding, []byte) ([]byte, error) {
		calls++
		return nil, errors.New("synthetic unknown outcome")
	})
	for range 2 {
		if _, err := f.store.SealQuotePDF(t.Context(), f.actor, in, signer, objects); !errors.Is(err, conversation.ErrSealPending) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("unknown signer replayed", calls)
	}
}

func TestQuoteSealScopeAndAuditFailure(t *testing.T) {
	f, in, objects := readySeal(t)
	calls := 0
	signer := sealFunc(func(_ context.Context, _ quotation.SealBinding, data []byte) ([]byte, error) {
		calls++
		return append(data, []byte("\n% SYNTHETIC ONLY\n")...), nil
	})
	approvalSQL(t, f, `UPDATE iam.operation_grants SET enabled=false WHERE capability='quotation.seal'`)
	if _, err := f.store.SealQuotePDF(t.Context(), f.actor, in, signer, objects); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("publication granted seal authority", err)
	}
	approvalSQL(t, f, `UPDATE iam.operation_grants SET enabled=true WHERE capability='quotation.seal'`)
	wrong := in
	wrong.Document.PDFHash = strings.Repeat("f", 64)
	if _, err := f.store.SealQuotePDF(t.Context(), f.actor, wrong, signer, objects); !errors.Is(err, quotation.ErrSealDenied) {
		t.Fatal("wrong PDF authorized", err)
	}
	approvalSQL(t, f, `REVOKE INSERT ON channel.quote_seal_events FROM agentos_runtime`)
	if _, err := f.store.SealQuotePDF(t.Context(), f.actor, in, signer, objects); err == nil {
		t.Fatal("unaudited signing")
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), `SELECT count(*) FROM channel.quote_seals`).Scan(&count); err != nil || count != 0 || calls != 0 {
		t.Fatal("failed intent not rolled back", count, calls, err)
	}
}

func TestQuoteSealRevocationAndAuditFailureDuringSigner(t *testing.T) {
	for _, kind := range []string{"revoked", "audit"} {
		t.Run(kind, func(t *testing.T) {
			f, in, objects := readySeal(t)
			calls := 0
			signer := sealFunc(func(_ context.Context, _ quotation.SealBinding, data []byte) ([]byte, error) {
				calls++
				if kind == "revoked" {
					approvalSQL(t, f, `UPDATE channel.seal_policies SET enabled=false`)
				} else {
					approvalSQL(t, f, `REVOKE INSERT ON channel.quote_seal_events FROM agentos_runtime`)
				}
				return append(data, []byte("\n% SYNTHETIC ONLY\n")...), nil
			})
			if output, err := f.store.SealQuotePDF(t.Context(), f.actor, in, signer, objects); err == nil || len(output) != 0 {
				t.Fatal("returned output after failed authorization/audit", err)
			}
			var state string
			if err := f.pg.Admin.QueryRow(t.Context(), `SELECT state FROM channel.quote_seals`).Scan(&state); err != nil || state != "reserved" {
				t.Fatal("unknown result not quarantined", state, err)
			}
			approvalSQL(t, f, `UPDATE channel.seal_policies SET enabled=true`)
			approvalSQL(t, f, `GRANT INSERT ON channel.quote_seal_events TO agentos_runtime`)
			if _, err := f.store.SealQuotePDF(t.Context(), f.actor, in, signer, objects); !errors.Is(err, conversation.ErrSealPending) {
				t.Fatal("unknown result silently retried", err)
			}
			if calls != 1 {
				t.Fatal("signer repeated", calls)
			}
		})
	}
}

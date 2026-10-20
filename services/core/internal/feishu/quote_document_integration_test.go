//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
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
	"sync/atomic"
	"testing"
)

type countedPDF struct {
	*quotation.PDFRenderer
	calls int
}

func (r *countedPDF) Render(ctx context.Context, d quotation.Draft) ([]byte, error) {
	r.calls++
	return r.PDFRenderer.Render(ctx, d)
}

type pdfObjects struct {
	data    []byte
	tenant  uuid.UUID
	attempt uuid.UUID
	puts    int
	corrupt bool
}

func (o *pdfObjects) Put(_ context.Context, tenant, attempt uuid.UUID, data []byte) (string, error) {
	if o.puts != 0 {
		return "", errors.New("duplicate write")
	}
	o.puts++
	o.tenant = tenant
	o.attempt = attempt
	o.data = bytes.Clone(data)
	return "synthetic-pdf", nil
}
func (o *pdfObjects) Get(_ context.Context, tenant uuid.UUID, ref string, limit int64) ([]byte, error) {
	if tenant != o.tenant || ref != "synthetic-pdf" || int64(len(o.data)) > limit {
		return nil, identity.ErrForbidden
	}
	if o.corrupt {
		return []byte("tampered"), nil
	}
	return bytes.Clone(o.data), nil
}

func TestQuoteDocumentGeneratesRealPDFOnce(t *testing.T) {
	f, a, d := quotePending(t)
	approvalSQL(t, f, `INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'quotation.publish')`, testBinding().TenantID, f.approver, testBinding().AppID)
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
	counted := &countedPDF{PDFRenderer: renderer}
	objects := &pdfObjects{}
	in := conversation.QuoteDocument{TaskID: d.ID, Version: d.Version, ContentHash: d.Hash, RendererHash: renderer.Revision()}
	first, err := f.store.GenerateQuotePDF(t.Context(), f.actor, in, counted, objects)
	if err != nil {
		t.Fatal(err)
	}
	restarted := conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	second, err := restarted.GenerateQuotePDF(t.Context(), f.actor, in, counted, objects)
	if err != nil || !bytes.Equal(first, second) || !bytes.HasPrefix(first, []byte("%PDF-")) || counted.calls != 1 || objects.puts != 1 {
		t.Fatal("PDF regenerated or bytes changed", err, counted.calls, objects.puts)
	}
	objects.corrupt = true
	if _, err := restarted.GenerateQuotePDF(t.Context(), f.actor, in, counted, objects); !errors.Is(err, conversation.ErrDocumentConflict) {
		t.Fatal("tampered object accepted", err)
	}
	if counted.calls != 1 {
		t.Fatal("tampering triggered new generation")
	}
}

func TestQuoteDocumentDurableSingleReservation(t *testing.T) {
	f, a, d := quotePending(t)
	in := conversation.QuoteDocument{TaskID: d.ID, Version: d.Version, ContentHash: d.Hash, RendererHash: strings.Repeat("c", 64)}
	if _, _, _, err := f.store.ReserveQuoteDocument(t.Context(), f.actor, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("approval grants publication", err)
	}
	approvalSQL(t, f, `INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'quotation.publish')`, testBinding().TenantID, f.approver, testBinding().AppID)
	if _, _, _, err := f.store.ReserveQuoteDocument(t.Context(), f.actor, in); !errors.Is(err, conversation.ErrApprovalDenied) {
		t.Fatal("unapproved draft published", err)
	}
	if _, err := f.store.DecideApproval(t.Context(), f.actor, decision(a)); err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	claims := make(chan conversation.QuoteDocument, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			out, _, acquired, err := f.store.ReserveQuoteDocument(t.Context(), f.actor, in)
			if acquired {
				count.Add(1)
				claims <- out
			} else if !errors.Is(err, conversation.ErrDocumentPending) {
				t.Errorf("competing reserve: %v", err)
			}
		})
	}
	wg.Wait()
	close(claims)
	if count.Load() != 1 {
		t.Fatal("multiple render reservations", count.Load())
	}
	reserved := <-claims
	restarted := conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	if _, _, _, err := restarted.ReserveQuoteDocument(t.Context(), f.actor, in); !errors.Is(err, conversation.ErrDocumentPending) {
		t.Fatal("restart repeated render", err)
	}
	reserved.ObjectRef = "synthetic-object"
	reserved.PDFHash = strings.Repeat("d", 64)
	reserved.PDFBytes = 200
	if err := restarted.CompleteQuoteDocument(t.Context(), f.actor, reserved); err != nil {
		t.Fatal(err)
	}
	out, _, acquired, err := restarted.ReserveQuoteDocument(t.Context(), f.actor, in)
	if err != nil || acquired || out != reserved {
		t.Fatal("ready artifact not reused", out, err)
	}
	if err := restarted.CompleteQuoteDocument(t.Context(), f.actor, reserved); !errors.Is(err, conversation.ErrDocumentConflict) {
		t.Fatal("ready metadata overwritten", err)
	}
	approvalSQL(t, f, `INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'other-app','quotation.publish')`, testBinding().TenantID, f.approver)
	if err := f.store.Operation(t.Context(), f.actor, "quotation.publish", func(q database.Queries) error {
		if _, err := q.Exec(t.Context(), `SELECT set_config('agentos.app_id','other-app',true)`); err != nil {
			return err
		}
		var visible int
		if err := q.QueryRow(t.Context(), `SELECT count(*) FROM channel.quote_documents`).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			t.Fatal("RLS leaked another application's artifact")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	approvalSQL(t, f, `UPDATE iam.operation_grants SET enabled=false WHERE capability='quotation.publish'`)
	if _, _, _, err := restarted.ReserveQuoteDocument(t.Context(), f.actor, in); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("revoked publisher read artifact", err)
	}
	var audits int
	if err := f.pg.Admin.QueryRow(t.Context(), `SELECT count(*) FROM channel.quote_document_events WHERE event IN ('reserved','ready')`).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("missing atomic document audit", audits, err)
	}
}

func TestQuoteDocumentAuthorizationAndAuditRollback(t *testing.T) {
	f, a, d := quotePending(t)
	approvalSQL(t, f, `INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'quotation.publish')`, testBinding().TenantID, f.approver, testBinding().AppID)
	if _, err := f.store.DecideApproval(t.Context(), f.actor, decision(a)); err != nil {
		t.Fatal(err)
	}
	in := conversation.QuoteDocument{TaskID: d.ID, Version: d.Version, ContentHash: d.Hash, RendererHash: strings.Repeat("c", 64)}
	approvalSQL(t, f, `REVOKE INSERT ON channel.quote_document_events FROM agentos_runtime`)
	if _, _, acquired, err := f.store.ReserveQuoteDocument(t.Context(), f.actor, in); err == nil || acquired {
		t.Fatal("audit failure accepted", err)
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), `SELECT count(*) FROM channel.quote_documents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("reservation not rolled back", count, err)
	}
	approvalSQL(t, f, `GRANT INSERT ON channel.quote_document_events TO agentos_runtime`)
	out, _, acquired, err := f.store.ReserveQuoteDocument(t.Context(), f.actor, in)
	if err != nil || !acquired {
		t.Fatal(err)
	}
	out.ObjectRef = "synthetic"
	out.PDFHash = strings.Repeat("d", 64)
	out.PDFBytes = 100
	approvalSQL(t, f, `UPDATE iam.operation_grants SET enabled=false WHERE capability='task.approve'`)
	if err := f.store.CompleteQuoteDocument(t.Context(), f.actor, out); !errors.Is(err, conversation.ErrApprovalDenied) {
		t.Fatal("revoked approval authority accepted", err)
	}
	approvalSQL(t, f, `UPDATE iam.operation_grants SET enabled=true WHERE capability='task.approve'`)
	wrong := out
	wrong.ContentHash = strings.Repeat("e", 64)
	if err := f.store.CompleteQuoteDocument(t.Context(), f.actor, wrong); !errors.Is(err, conversation.ErrDocumentConflict) {
		t.Fatal("wrong quote accepted", err)
	}
	approvalSQL(t, f, `REVOKE INSERT ON channel.quote_document_events FROM agentos_runtime`)
	if err := f.store.CompleteQuoteDocument(t.Context(), f.actor, out); err == nil {
		t.Fatal("ready without audit")
	}
	var state string
	if err := f.pg.Admin.QueryRow(t.Context(), `SELECT state FROM channel.quote_documents`).Scan(&state); err != nil || state != "reserved" {
		t.Fatal("ready update not rolled back", state, err)
	}
	approvalSQL(t, f, `GRANT INSERT ON channel.quote_document_events TO agentos_runtime`)
	approvalSQL(t, f, `UPDATE iam.operation_grants SET enabled=false WHERE capability='quotation.publish'`)
	if err := f.store.CompleteQuoteDocument(t.Context(), f.actor, out); !errors.Is(err, identity.ErrForbidden) {
		t.Fatal("revoked publisher accepted", err)
	}
}

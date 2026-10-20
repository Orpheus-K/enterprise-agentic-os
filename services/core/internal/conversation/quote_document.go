package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/quotation"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
)

// QuotePDFRenderer is a trusted pinned implementation; no model text is executable.
type QuotePDFRenderer interface {
	Revision() string
	Render(context.Context, quotation.Draft) ([]byte, error)
}

// QuotePDFObjects must persist immutable bytes under the tenant/attempt scope and
// enforce the read limit before allocation. It never sends a document to a customer.
type QuotePDFObjects interface {
	Put(context.Context, uuid.UUID, uuid.UUID, []byte) (string, error)
	Get(context.Context, uuid.UUID, string, int64) ([]byte, error)
}

// GenerateQuotePDF reserves before rendering. An uncertain render, write or commit
// leaves the durable reservation, so a retry cannot repeat generation silently.
func (s *Store) GenerateQuotePDF(ctx context.Context, actor identity.ExternalIdentity, in QuoteDocument, renderer QuotePDFRenderer, objects QuotePDFObjects) ([]byte, error) {
	if renderer == nil || objects == nil || renderer.Revision() != in.RendererHash {
		return nil, ErrDocumentConflict
	}
	out, d, acquired, err := s.ReserveQuoteDocument(ctx, actor, in)
	if err != nil {
		return nil, err
	}
	if acquired {
		data, err := renderer.Render(ctx, d)
		if err != nil {
			return nil, errors.Join(ErrDocumentPending, err)
		}
		if len(data) == 0 || len(data) > 8<<20 || !bytes.HasPrefix(data, []byte("%PDF-")) {
			return nil, errors.Join(ErrDocumentPending, ErrDocumentConflict)
		}
		out.PDFBytes = int64(len(data))
		out.PDFHash = contentHash(string(data))
		out.ObjectRef, err = objects.Put(ctx, s.tenant, out.AttemptID, data)
		if err != nil {
			return nil, errors.Join(ErrDocumentPending, err)
		}
		stored, err := objects.Get(ctx, s.tenant, out.ObjectRef, 8<<20)
		if err != nil {
			return nil, errors.Join(ErrDocumentPending, err)
		}
		if !bytes.Equal(data, stored) {
			return nil, errors.Join(ErrDocumentPending, ErrDocumentConflict)
		}
		if err = s.CompleteQuoteDocument(ctx, actor, out); err != nil {
			return nil, errors.Join(ErrDocumentPending, err)
		}
	}
	data, err := objects.Get(ctx, s.tenant, out.ObjectRef, 8<<20)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != out.PDFBytes || contentHash(string(data)) != out.PDFHash {
		return nil, ErrDocumentConflict
	}
	// Recheck current authorization after object I/O before returning any bytes.
	current, _, again, err := s.ReserveQuoteDocument(ctx, actor, in)
	if err != nil {
		return nil, err
	}
	if again || current != out {
		return nil, ErrDocumentConflict
	}
	err = s.Operation(ctx, actor, "quotation.publish", func(q database.Queries) error {
		if _, err := q.Exec(ctx, `SELECT set_config('agentos.app_id',$1,true)`, s.app); err != nil {
			return err
		}
		if _, err := s.lockPublication(ctx, q, actor, in); err != nil {
			return err
		}
		return s.documentAudit(ctx, q, out, "read")
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

var ErrDocumentPending = errors.New("quotation PDF generation reserved; reconcile before any retry")
var ErrDocumentConflict = errors.New("quotation document version, renderer or content mismatch")

func (s *Store) documentAudit(ctx context.Context, q database.Queries, in QuoteDocument, event string) error {
	_, err := q.Exec(ctx, `INSERT INTO channel.quote_document_events(tenant_id,task_id,version,attempt_id,member_id,event) VALUES($1,$2,$3,$4,nullif(current_setting('agentos.member_id',true),'')::uuid,$5)`, s.tenant, in.TaskID, in.Version, in.AttemptID, event)
	return err
}

type QuoteDocument struct {
	TaskID       uuid.UUID
	Version      int64
	ContentHash  string
	RendererHash string
	AttemptID    uuid.UUID
	ObjectRef    string
	PDFHash      string
	PDFBytes     int64
}

func documentDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}

// lockPublication holds the task lock and then rechecks the caller's current mapping/grant.
// Pricing approval alone never grants quotation.publish. No send or seal is performed.
func (s *Store) lockPublication(ctx context.Context, q database.Queries, actor identity.ExternalIdentity, in QuoteDocument) (quotation.Draft, error) {
	var d quotation.Draft
	var response, state string
	var approved bool
	err := q.QueryRow(ctx, `SELECT v.draft,t.response,t.state,
 (t.approval_member IS NULL AND a.task_id IS NULL)
 FROM channel.tasks t JOIN channel.quote_versions v ON (v.tenant_id,v.task_id,v.version)=(t.tenant_id,t.id,t.quote_version)
 LEFT JOIN channel.approvals a ON (a.tenant_id,a.task_id)=(t.tenant_id,t.id)
 WHERE t.tenant_id=$1 AND t.app_id=$2 AND t.model_revision=$3 AND t.id=$4 AND t.quote_version=$5
 FOR UPDATE OF t`, s.tenant, s.app, s.revision, in.TaskID, in.Version).Scan(&d, &response, &state, &approved)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrDocumentConflict
	}
	if err != nil {
		return d, err
	}
	if _, err = s.identities.RequireCapabilityIn(ctx, q, actor, s.app, "quotation.publish"); err != nil {
		return d, err
	}
	// Approval binds serialized task response; the Draft hash binds the structured quote.
	var approvedDraft quotation.Draft
	if json.Unmarshal([]byte(response), &approvedDraft) != nil || quotation.Verify(approvedDraft) != nil || approvedDraft.Hash != d.Hash || quotation.Verify(d) != nil || d.Hash != in.ContentHash || d.ID != in.TaskID || d.Version != in.Version {
		return d, ErrDocumentConflict
	}
	if state != string(DeliveryPending) && state != string(DeliveryRunning) && state != string(Succeeded) {
		return d, ErrApprovalDenied
	}
	if d.RequiresApproval || !approved {
		var valid bool
		if err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel.approvals WHERE tenant_id=$1 AND task_id=$2 AND status='approved' AND expires_at>clock_timestamp() AND content_hash=$3)`, s.tenant, in.TaskID, contentHash(response)).Scan(&valid); err != nil {
			return d, err
		}
		if !valid {
			return d, ErrApprovalDenied
		}
		approver := identity.ExternalIdentity{TenantID: s.tenant}
		var member uuid.UUID
		if err = q.QueryRow(ctx, `SELECT actor_provider,actor_issuer,actor_subject,member_id FROM channel.approvals WHERE tenant_id=$1 AND task_id=$2`, s.tenant, in.TaskID).Scan(&approver.Provider, &approver.Issuer, &approver.Subject, &member); err != nil {
			return d, err
		}
		principal, approvalErr := s.identities.RequireCapabilityIn(ctx, q, approver, s.app, "task.approve")
		if approvalErr != nil || principal.MemberID != member {
			return d, ErrApprovalDenied
		}
		// Resolving the approver changes transaction identity context; restore publisher.
		if _, err = s.identities.RequireCapabilityIn(ctx, q, actor, s.app, "quotation.publish"); err != nil {
			return d, err
		}
	}
	return d, nil
}

// ReserveQuoteDocument is the single durable generation gate. acquired=false returns
// a prior ready artifact. Reserved/unknown attempts never acquire a second render.
func (s *Store) ReserveQuoteDocument(ctx context.Context, actor identity.ExternalIdentity, in QuoteDocument) (out QuoteDocument, d quotation.Draft, acquired bool, err error) {
	if in.TaskID == uuid.Nil || in.Version < 1 || !documentDigest(in.ContentHash) || !documentDigest(in.RendererHash) {
		err = ErrDocumentConflict
		return
	}
	err = s.Operation(ctx, actor, "quotation.publish", func(q database.Queries) error {
		var e error
		if _, e = q.Exec(ctx, `SELECT set_config('agentos.app_id',$1,true)`, s.app); e != nil {
			return e
		}
		d, e = s.lockPublication(ctx, q, actor, in)
		if e != nil {
			return e
		}
		var state string
		out = in
		e = q.QueryRow(ctx, `SELECT content_hash,renderer_hash,attempt_id,state,coalesce(object_ref,''),coalesce(pdf_hash,''),coalesce(pdf_bytes,0) FROM channel.quote_documents WHERE tenant_id=$1 AND task_id=$2 AND version=$3`, s.tenant, in.TaskID, in.Version).Scan(&out.ContentHash, &out.RendererHash, &out.AttemptID, &state, &out.ObjectRef, &out.PDFHash, &out.PDFBytes)
		if e == nil {
			if out.ContentHash != in.ContentHash || out.RendererHash != in.RendererHash {
				return ErrDocumentConflict
			}
			if state != "ready" {
				return ErrDocumentPending
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		out.AttemptID = uuid.New()
		_, e = q.Exec(ctx, `INSERT INTO channel.quote_documents(tenant_id,task_id,version,content_hash,renderer_hash,attempt_id,member_id,state) VALUES($1,$2,$3,$4,$5,$6,nullif(current_setting('agentos.member_id',true),'')::uuid,'reserved')`, s.tenant, in.TaskID, in.Version, in.ContentHash, in.RendererHash, out.AttemptID)
		if e != nil {
			return e
		}
		acquired = true
		return s.documentAudit(ctx, q, out, "reserved")
	})
	if err != nil {
		return QuoteDocument{}, quotation.Draft{}, false, err
	}
	return
}

// CompleteQuoteDocument commits immutable object metadata, never caller-provided PDF bytes.
// The object adapter must verify stored bytes before this call; failure leaves reservation.
func (s *Store) CompleteQuoteDocument(ctx context.Context, actor identity.ExternalIdentity, in QuoteDocument) error {
	if in.AttemptID == uuid.Nil || !documentDigest(in.PDFHash) || in.PDFBytes < 1 || in.PDFBytes > 8<<20 || len(in.ObjectRef) < 1 || len(in.ObjectRef) > 256 || strings.ContainsAny(in.ObjectRef, "\x00\r\n") {
		return ErrDocumentConflict
	}
	return s.Operation(ctx, actor, "quotation.publish", func(q database.Queries) error {
		if _, err := q.Exec(ctx, `SELECT set_config('agentos.app_id',$1,true)`, s.app); err != nil {
			return err
		}
		if _, err := s.lockPublication(ctx, q, actor, in); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `UPDATE channel.quote_documents SET state='ready',object_ref=$7,pdf_hash=$8,pdf_bytes=$9 WHERE tenant_id=$1 AND task_id=$2 AND version=$3 AND content_hash=$4 AND renderer_hash=$5 AND attempt_id=$6 AND state='reserved'`, s.tenant, in.TaskID, in.Version, in.ContentHash, in.RendererHash, in.AttemptID, in.ObjectRef, in.PDFHash, in.PDFBytes)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrDocumentConflict
		}
		return s.documentAudit(ctx, q, in, "ready")
	})
}

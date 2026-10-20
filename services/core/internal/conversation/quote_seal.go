package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/quotation"
	"bytes"
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

// QuoteSealSigner is an injected trusted adapter. This repository provides no
// production signer, key material, image stamp, or external signing transport.
type QuoteSealSigner interface {
	Seal(context.Context, quotation.SealBinding, []byte) ([]byte, error)
}

func (s *Store) verifySealAttempt(ctx context.Context, actor identity.ExternalIdentity, in QuoteSealRequest, out QuoteSeal, state string, event string) error {
	return s.Operation(ctx, actor, "quotation.seal", func(q database.Queries) error {
		binding, _, err := s.lockSeal(ctx, q, actor, in)
		if err != nil {
			return err
		}
		expected, err := binding.Digest()
		if err != nil {
			return err
		}
		actual, err := out.Binding.Digest()
		if err != nil {
			return err
		}
		if expected != actual {
			return quotation.ErrSealDenied
		}
		var valid bool
		if err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channel.quote_seals WHERE tenant_id=$1 AND attempt_id=$2 AND state=$3 AND binding_hash=$4)`, s.tenant, out.AttemptID, state, expected).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return quotation.ErrSealDenied
		}
		if event != "" {
			return s.sealAudit(ctx, q, out, event)
		}
		return nil
	})
}

func (s *Store) SealQuotePDF(ctx context.Context, actor identity.ExternalIdentity, in QuoteSealRequest, signer QuoteSealSigner, objects QuotePDFObjects) ([]byte, error) {
	if signer == nil || objects == nil {
		return nil, quotation.ErrSealDenied
	}
	out, source, acquired, err := s.ReserveQuoteSeal(ctx, actor, in)
	if err != nil {
		return nil, err
	}
	if acquired {
		data, err := objects.Get(ctx, s.tenant, source.ObjectRef, 8<<20)
		if err != nil {
			return nil, errors.Join(ErrSealPending, err)
		}
		if int64(len(data)) != source.PDFBytes || contentHash(string(data)) != source.PDFHash {
			return nil, errors.Join(ErrSealPending, ErrDocumentConflict)
		}
		if err = s.verifySealAttempt(ctx, actor, in, out, "reserved", ""); err != nil {
			return nil, errors.Join(ErrSealPending, err)
		}
		signed, err := signer.Seal(ctx, out.Binding, bytes.Clone(data))
		if err != nil {
			return nil, errors.Join(ErrSealPending, err)
		}
		if len(signed) > 8<<20 || !bytes.HasPrefix(signed, []byte("%PDF-")) || bytes.Equal(signed, data) {
			return nil, errors.Join(ErrSealPending, ErrDocumentConflict)
		}
		out.OutputHash = contentHash(string(signed))
		out.OutputBytes = int64(len(signed))
		out.ObjectRef, err = objects.Put(ctx, s.tenant, out.AttemptID, signed)
		if err != nil {
			return nil, errors.Join(ErrSealPending, err)
		}
		stored, err := objects.Get(ctx, s.tenant, out.ObjectRef, 8<<20)
		if err != nil {
			return nil, errors.Join(ErrSealPending, err)
		}
		if !bytes.Equal(stored, signed) {
			return nil, errors.Join(ErrSealPending, ErrDocumentConflict)
		}
		if err = s.CompleteQuoteSeal(ctx, actor, in, out); err != nil {
			return nil, errors.Join(ErrSealPending, err)
		}
	}
	data, err := objects.Get(ctx, s.tenant, out.ObjectRef, 8<<20)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != out.OutputBytes || contentHash(string(data)) != out.OutputHash {
		return nil, ErrDocumentConflict
	}
	if err = s.verifySealAttempt(ctx, actor, in, out, "ready", "read"); err != nil {
		return nil, err
	}
	return data, nil
}

var ErrSealPending = errors.New("seal attempt reserved; reconcile unknown outcome before retry")

type QuoteSealRequest struct {
	Document                             QuoteDocument
	SealID, PolicyRevision, EvidenceHash string
}
type QuoteSeal struct {
	AttemptID             uuid.UUID
	Binding               quotation.SealBinding
	ObjectRef, OutputHash string
	OutputBytes           int64
}

func (s *Store) lockSeal(ctx context.Context, q database.Queries, actor identity.ExternalIdentity, in QuoteSealRequest) (quotation.SealBinding, QuoteDocument, error) {
	denied := quotation.SealBinding{}
	if in.SealID == "" || len(in.SealID) > 128 || strings.TrimSpace(in.SealID) != in.SealID || !documentDigest(in.PolicyRevision) || !documentDigest(in.EvidenceHash) || !documentDigest(in.Document.PDFHash) {
		return denied, QuoteDocument{}, quotation.ErrSealDenied
	}
	if _, err := q.Exec(ctx, `SELECT set_config('agentos.app_id',$1,true)`, s.app); err != nil {
		return denied, QuoteDocument{}, err
	}
	draft, err := s.lockPublication(ctx, q, actor, in.Document)
	if err != nil {
		return denied, QuoteDocument{}, err
	}
	principal, err := s.identities.RequireCapabilityIn(ctx, q, actor, s.app, "quotation.seal")
	if err != nil {
		return denied, QuoteDocument{}, err
	}
	var policy quotation.SealPolicy
	var policyRevision string
	var now time.Time
	err = q.QueryRow(ctx, `SELECT policy,revision,clock_timestamp() FROM channel.seal_policies WHERE tenant_id=$1 AND app_id=$2 AND seal_id=$3 AND enabled`, s.tenant, s.app, in.SealID).Scan(&policy, &policyRevision, &now)
	if errors.Is(err, pgx.ErrNoRows) {
		return denied, QuoteDocument{}, quotation.ErrSealDenied
	}
	if err != nil {
		return denied, QuoteDocument{}, err
	}
	if policy.Revision != policyRevision || policyRevision != in.PolicyRevision || policy.SealID != in.SealID {
		return denied, QuoteDocument{}, quotation.ErrSealDenied
	}
	document := in.Document
	err = q.QueryRow(ctx, `SELECT content_hash,renderer_hash,attempt_id,object_ref,pdf_hash,pdf_bytes FROM channel.quote_documents WHERE tenant_id=$1 AND task_id=$2 AND version=$3 AND state='ready'`, s.tenant, document.TaskID, document.Version).Scan(&document.ContentHash, &document.RendererHash, &document.AttemptID, &document.ObjectRef, &document.PDFHash, &document.PDFBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return denied, QuoteDocument{}, quotation.ErrSealDenied
	}
	if err != nil {
		return denied, QuoteDocument{}, err
	}
	if document.ContentHash != in.Document.ContentHash || document.RendererHash != in.Document.RendererHash || document.PDFHash != in.Document.PDFHash {
		return denied, QuoteDocument{}, quotation.ErrSealDenied
	}
	binding, err := quotation.BindSeal(policy, s.tenant, principal.MemberID, s.app, draft, document.PDFHash, in.EvidenceHash, now)
	return binding, document, err
}

func (s *Store) sealAudit(ctx context.Context, q database.Queries, seal QuoteSeal, event string) error {
	_, err := q.Exec(ctx, `INSERT INTO channel.quote_seal_events(tenant_id,attempt_id,member_id,event) VALUES($1,$2,nullif(current_setting('agentos.member_id',true),'')::uuid,$3)`, s.tenant, seal.AttemptID, event)
	return err
}

func (s *Store) ReserveQuoteSeal(ctx context.Context, actor identity.ExternalIdentity, in QuoteSealRequest) (out QuoteSeal, document QuoteDocument, acquired bool, err error) {
	err = s.Operation(ctx, actor, "quotation.seal", func(q database.Queries) error {
		binding, source, e := s.lockSeal(ctx, q, actor, in)
		if e != nil {
			return e
		}
		document = source
		digest, e := binding.Digest()
		if e != nil {
			return e
		}
		var prior, state string
		e = q.QueryRow(ctx, `SELECT attempt_id,binding,binding_hash,state,coalesce(object_ref,''),coalesce(output_hash,''),coalesce(output_bytes,0) FROM channel.quote_seals WHERE tenant_id=$1 AND task_id=$2 AND version=$3 AND seal_id=$4`, s.tenant, in.Document.TaskID, in.Document.Version, in.SealID).Scan(&out.AttemptID, &out.Binding, &prior, &state, &out.ObjectRef, &out.OutputHash, &out.OutputBytes)
		if e == nil {
			actual, e := out.Binding.Digest()
			if e != nil {
				return e
			}
			if actual != digest || prior != digest {
				return quotation.ErrSealDenied
			}
			if state != "ready" {
				return ErrSealPending
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		out = QuoteSeal{AttemptID: uuid.New(), Binding: binding}
		_, e = q.Exec(ctx, `INSERT INTO channel.quote_seals(tenant_id,app_id,task_id,version,seal_id,attempt_id,binding,binding_hash,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'reserved')`, s.tenant, s.app, in.Document.TaskID, in.Document.Version, in.SealID, out.AttemptID, binding, digest)
		if e != nil {
			return e
		}
		acquired = true
		return s.sealAudit(ctx, q, out, "reserved")
	})
	if err != nil {
		return QuoteSeal{}, QuoteDocument{}, false, err
	}
	return
}

func (s *Store) CompleteQuoteSeal(ctx context.Context, actor identity.ExternalIdentity, in QuoteSealRequest, out QuoteSeal) error {
	if out.AttemptID == uuid.Nil || !documentDigest(out.OutputHash) || out.OutputBytes < 1 || out.OutputBytes > 8<<20 || len(out.ObjectRef) < 1 || len(out.ObjectRef) > 256 || strings.ContainsAny(out.ObjectRef, "\x00\r\n") {
		return quotation.ErrSealDenied
	}
	return s.Operation(ctx, actor, "quotation.seal", func(q database.Queries) error {
		binding, _, err := s.lockSeal(ctx, q, actor, in)
		if err != nil {
			return err
		}
		digest, err := binding.Digest()
		if err != nil {
			return err
		}
		actual, err := out.Binding.Digest()
		if err != nil {
			return err
		}
		if digest != actual {
			return quotation.ErrSealDenied
		}
		result, err := q.Exec(ctx, `UPDATE channel.quote_seals SET state='ready',object_ref=$7,output_hash=$8,output_bytes=$9 WHERE tenant_id=$1 AND task_id=$2 AND version=$3 AND seal_id=$4 AND attempt_id=$5 AND binding_hash=$6 AND state='reserved'`, s.tenant, in.Document.TaskID, in.Document.Version, in.SealID, out.AttemptID, digest, out.ObjectRef, out.OutputHash, out.OutputBytes)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return quotation.ErrSealDenied
		}
		return s.sealAudit(ctx, q, out, "ready")
	})
}

package operations

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"context"
	"github.com/google/uuid"
)

// Store exposes authorized domain operations, never a raw database callback.
type Store interface {
	AuthorizeOperation(context.Context, identity.ExternalIdentity, string) error
	ReadAudit(context.Context, identity.ExternalIdentity, uuid.UUID, int64, int) (conversation.AuditPage, error)
	ListTasks(context.Context, identity.ExternalIdentity, conversation.ReportWindow, uuid.UUID, int) (conversation.TaskPage, error)
	Stats(context.Context, identity.ExternalIdentity, conversation.ReportWindow) (conversation.TaskStats, error)
	InspectApproval(context.Context, identity.ExternalIdentity, uuid.UUID) (conversation.Approval, error)
	DecideApproval(context.Context, identity.ExternalIdentity, conversation.ApprovalDecision) (conversation.Approval, error)
	InspectRecovery(context.Context, identity.ExternalIdentity, uuid.UUID) (conversation.RecoverySnapshot, error)
	Reconcile(context.Context, identity.ExternalIdentity, conversation.Reconciliation) (conversation.RecoverySnapshot, error)
}
type QuotationStore interface {
	AmendQuotation(context.Context, identity.ExternalIdentity, conversation.QuoteEdit) (conversation.Approval, error)
}
type NoticeStore interface {
	InspectNotice(context.Context, identity.ExternalIdentity, uuid.UUID, uuid.UUID) (conversation.NoticeSnapshot, error)
	ReconcileNotice(context.Context, identity.ExternalIdentity, conversation.NoticeReconciliation) (conversation.NoticeSnapshot, error)
}

var _ Store = (*conversation.Store)(nil)
var _ QuotationStore = (*conversation.Store)(nil)
var _ NoticeStore = (*conversation.Store)(nil)

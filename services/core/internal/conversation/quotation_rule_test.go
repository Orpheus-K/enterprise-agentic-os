package conversation

import (
	"context"
	"errors"
	"testing"

	"agentos.local/core/internal/quotation"
	"agentos.local/core/internal/sandbox"
)

func TestQuotationSandboxCannotFallBack(t *testing.T) {
	base := &Store{}
	if _, err := base.WithQuotationSandbox("unapproved"); err == nil {
		t.Fatal("unapproved module accepted")
	}
	store, err := base.WithQuotationSandbox(quotation.ApprovalModuleSHA256)
	if err != nil {
		t.Fatal(err)
	}
	d := quotation.Draft{StandardTotalMinor: 10000, TotalMinor: 7500,
		Policy: quotation.Policy{ApprovalBelowBPS: 8000}, RequiresApproval: true}
	if err := store.checkQuotationRule(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	d.RequiresApproval = false
	if err := store.checkQuotationRule(t.Context(), d); !errors.Is(err, ErrInvalidApproval) {
		t.Fatal("host/module disagreement ignored", err)
	}
	d.RequiresApproval = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.checkQuotationRule(ctx, d); !errors.Is(err, sandbox.ErrTimeout) {
		t.Fatal("cancelled sandbox fell back to host result", err)
	}
	if err := base.checkQuotationRule(ctx, d); err != nil {
		t.Fatal("enabling sandbox mutated base store", err)
	}
}

//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/operations"
	"agentos.local/core/internal/quotation"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func quotePending(t *testing.T, notices ...bool) (*approvalFixture, conversation.Approval, quotation.Draft) {
	t.Helper()
	var f *approvalFixture
	if len(notices) > 0 && notices[0] {
		f = noticeEnvironment(t)
	} else {
		f = approvalChain(t)
	}
	task, err := f.store.Claim(t.Context())
	if err != nil || task == nil {
		t.Fatal(err)
	}
	if err = f.store.Transition(t.Context(), task, conversation.ModelRunning, "", ""); err != nil {
		t.Fatal(err)
	}
	d, err := quotation.New(task.ID, quotation.Request{Customer: "synthetic", Evidence: []string{"minute-v1"}, Items: []quotation.Item{{SKU: "test", Quantity: 2, UnitMinor: 7500}}}, quotation.Catalogue{Revision: strings.Repeat("a", 64), Currency: "CNY", Basis: "net_no_fees", Prices: map[string]int64{"test": 10000}}, quotation.Policy{Revision: strings.Repeat("b", 64), Currency: "CNY", Basis: "net_no_fees", ApprovalBelowBPS: 8000})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.SaveQuotation(t.Context(), task, d); err != nil {
		t.Fatal(err)
	}
	if waiting, err := f.store.SuspendApproval(t.Context(), task); err != nil || !waiting {
		t.Fatal("quote did not suspend", err)
	}
	a, err := f.store.InspectApproval(t.Context(), f.actor, f.task)
	if err != nil {
		t.Fatal(err)
	}
	return f, a, d
}
func quoteEdit(a conversation.Approval, d quotation.Draft) conversation.QuoteEdit {
	in := conversation.QuoteEdit{ApprovalDecision: decision(a), QuoteHash: d.Hash, Changes: []quotation.PriceChange{{SKU: "test", UnitMinor: 8500}}}
	in.Decision = "amend"
	return in
}

func TestQuoteAmendConcurrentReplayAndRestart(t *testing.T) {
	f, a, d := quotePending(t)
	in := quoteEdit(a, d)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if out, err := f.store.AmendQuotation(t.Context(), f.actor, in); err != nil || out.ID == a.ID || out.Status != "pending" {
				t.Errorf("amend: %v", err)
			}
		})
	}
	wg.Wait()
	var versions, edits int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM channel.quote_versions),(SELECT count(*) FROM channel.quote_edits)").Scan(&versions, &edits); err != nil || versions != 2 || edits != 1 {
		t.Fatal("duplicate revision", versions, edits, err)
	}
	if _, err := f.store.DecideApproval(t.Context(), f.actor, decision(a)); !errors.Is(err, conversation.ErrApprovalConflict) {
		t.Fatal("old card approved new quote", err)
	}
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	f.store = conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	current, err := f.store.InspectApproval(t.Context(), f.actor, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var next quotation.Draft
	if json.Unmarshal([]byte(current.Response), &next) != nil || quotation.Verify(next) != nil || next.Version != 2 || next.TotalMinor != 17000 || next.RequiresApproval {
		t.Fatal("amend was not recomputed")
	}
	f.worker.Store = f.store
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || worked {
		t.Fatal("waiting quote executed")
	}
	f.worker.Store = f.store
	f.worker.Sender = senderFunc(func(_ context.Context, _, _, body string) error {
		expected, err := quotation.RenderText(next)
		if err != nil || body != expected {
			t.Error("wrong quote delivered")
		}
		f.sends.Add(1)
		return nil
	})
	if _, err = f.store.DecideApproval(t.Context(), f.actor, decision(current)); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.worker.ProcessOne(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	f.state(t, "succeeded")
	if f.sends.Load() != 1 || f.models.Load() != 0 {
		t.Fatal("quote regenerated or sent twice")
	}
}

func TestQuoteAmendIsolationRevocationAndRollback(t *testing.T) {
	for _, mode := range []string{"app", "revision", "revoked", "expired", "audit", "changed_replay"} {
		t.Run(mode, func(t *testing.T) {
			f, a, d := quotePending(t)
			in := quoteEdit(a, d)
			store := f.store
			switch mode {
			case "app":
				store = conversation.NewStore(f.db, testBinding().TenantID, "other-installation", "approved-revision")
			case "revision":
				store = conversation.NewStore(f.db, testBinding().TenantID, testBinding().AppID, "other-revision")
			case "revoked":
				approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false")
			case "expired":
				approvalSQL(t, f, "UPDATE channel.approvals SET expires_at=clock_timestamp()-interval '1 second'")
			case "audit":
				approvalSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT fail_amend CHECK(reason<>'quotation_amended') NOT VALID")
			case "changed_replay":
				if _, err := store.AmendQuotation(t.Context(), f.actor, in); err != nil {
					t.Fatal(err)
				}
				in.Changes[0].UnitMinor = 9000
			}
			if _, err := store.AmendQuotation(t.Context(), f.actor, in); err == nil {
				t.Fatal("invalid amendment accepted")
			}
			var count int
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.quote_versions").Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "changed_replay" {
				want = 2
			}
			if count != want {
				t.Fatal("partial or unauthorized version persisted")
			}
		})
	}
}

func TestQuoteAmendReplayStillRequiresAuthorization(t *testing.T) {
	f, a, d := quotePending(t)
	in := quoteEdit(a, d)
	if _, err := f.store.AmendQuotation(t.Context(), f.actor, in); err != nil {
		t.Fatal(err)
	}
	approvalSQL(t, f, "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.approver)
	if _, err := f.store.AmendQuotation(t.Context(), f.actor, in); err == nil {
		t.Fatal("revoked identity replayed amendment")
	}
}

func TestQuoteSaveRejectsLateLease(t *testing.T) {
	f := approvalChain(t)
	task, err := f.store.Claim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Transition(t.Context(), task, conversation.ModelRunning, "", ""); err != nil {
		t.Fatal(err)
	}
	d, err := quotation.New(task.ID, quotation.Request{Customer: "synthetic", Evidence: []string{"source"}, Items: []quotation.Item{{SKU: "test", Quantity: 1, UnitMinor: 75}}}, quotation.Catalogue{Revision: strings.Repeat("a", 64), Currency: "CNY", Basis: "net_no_fees", Prices: map[string]int64{"test": 100}}, quotation.Policy{Revision: strings.Repeat("b", 64), Currency: "CNY", Basis: "net_no_fees", ApprovalBelowBPS: 8000})
	if err != nil {
		t.Fatal(err)
	}
	approvalSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT fail_quote CHECK(reason<>'quotation_saved') NOT VALID")
	if err = f.store.SaveQuotation(t.Context(), task, d); err == nil {
		t.Fatal("quote audit failure hidden")
	}
	approvalSQL(t, f, "ALTER TABLE channel.audit DROP CONSTRAINT fail_quote")
	expireLease(t, f.chainFixture)
	if err = f.store.SaveQuotation(t.Context(), task, d); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("late quote persisted", err)
	}
	var count int
	if err = f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.quote_versions").Scan(&count); err != nil || count != 0 {
		t.Fatal("late quote row exists", err)
	}

}

func TestQuoteAmendHTTPBoundary(t *testing.T) {
	f, a, d := quotePending(t)
	in := quoteEdit(a, d)
	token := strings.Repeat("q", 32)
	api, err := operations.New(token, f.actor, f.store, nil, f.adapter.telemetry)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body  []byte
		token string
		want  int
	}{
		{raw, "bad", 401},
		{append(append([]byte(nil), raw...), []byte(" {}")...), token, 400},
		{[]byte(`{"unknown":true}`), token, 400},
		{raw, token, 200},
		{raw, token, 200},
	} {
		r := httptest.NewRequest("POST", "/api/ops/quotation", bytes.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("HTTP status %d, want %d", w.Code, tc.want)
		}
	}
}

func TestQuoteDocumentRequiresLiveAuthorizationAndReadAudit(t *testing.T) {
	for _, mode := range []string{"current", "revoked", "audit_failure", "unauthenticated"} {
		t.Run(mode, func(t *testing.T) {
			f, a, d := quotePending(t)
			token := strings.Repeat("q", 32)
			if mode == "current" {
				if _, err := f.store.AmendQuotation(t.Context(), f.actor, quoteEdit(a, d)); err != nil {
					t.Fatal(err)
				}
			}
			api, err := operations.New(token, f.actor, f.store, nil, f.adapter.telemetry)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "revoked" {
				approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false")
			}
			if mode == "audit_failure" {
				approvalSQL(t, f, "ALTER TABLE channel.audit_reads ADD CONSTRAINT fail_read CHECK(false) NOT VALID")
			}
			r := httptest.NewRequest("GET", "/api/ops/quotation?task_id="+f.task.String(), nil)
			if mode != "unauthenticated" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if mode != "current" {
				if w.Code == 200 || strings.Contains(w.Body.String(), "synthetic") {
					t.Fatal("denied download disclosed content")
				}
				return
			}
			if w.Code != 200 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Disposition"), "-v2.txt") || !strings.Contains(w.Body.String(), "报价总额：170.00") || !strings.Contains(w.Body.String(), "未用章") {
				t.Fatal("invalid quotation document", w.Code)
			}
			var reads int
			if err = f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit_reads").Scan(&reads); err != nil || reads < 2 {
				t.Fatal("download not audited", err)
			}
		})
	}
}

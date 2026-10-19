//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"testing"
)

func TestCardApprovalDatabaseBoundary(t *testing.T) {
	for _, tc := range []string{"approve_restart", "reject", "version", "hash", "revoke", "mapping", "other_install"} {
		t.Run(tc, func(t *testing.T) {
			f := noticeEnvironment(t)
			approval := approvalPending(t, f)
			decision := decision(approval)
			decision.RequestID = [16]byte{}
			switch tc {
			case "reject":
				decision.Decision = "reject"
			case "version":
				decision.Version++
			case "hash":
				decision.ContentHash = "0000000000000000000000000000000000000000000000000000000000000000"
			case "revoke":
				approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false WHERE member_id=$1", f.approver)
			case "mapping":
				approvalSQL(t, f, "UPDATE iam.identity_links SET enabled=false WHERE subject='ou_approver'")
			}
			store := f.store
			if tc == "other_install" {
				store = conversation.NewStore(f.db, testBinding().TenantID, "other-app", "approved-revision")
			}
			a := testAdapter(t, store).WithApprovals(store)
			body := cardBody(t, decision, nil)
			want := 503
			if tc == "approve_restart" || tc == "reject" {
				want = 200
			}
			if status := webhookStatus(t, a, encryptedRequest(t, body)); status != want {
				t.Fatal(status, want)
			}
			if want != 200 {
				f.state(t, "waiting_approval")
				return
			}
			f.db.Close()
			db, err := database.Open(t.Context(), f.pg.RuntimeURL, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store = conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
			a = testAdapter(t, store).WithApprovals(store)
			if status := webhookStatus(t, a, encryptedRequest(t, body)); status != 200 {
				t.Fatal("duplicate after restart", status)
			}
			var count int
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit WHERE reason IN ('approval_approved','approval_rejected')").Scan(&count); err != nil || count != 1 {
				t.Fatal(err, count)
			}
			f.worker.Store = store
			// A decision cancels its unsent notice before the next task claim.
			if worked, err := f.worker.ProcessOne(t.Context()); err != nil || !worked {
				t.Fatal("notice cancellation", worked, err)
			}
			noticeState(t, f, "cancelled")
			worked, err := f.worker.ProcessOne(t.Context())
			if err != nil || worked != (tc == "approve_restart") {
				t.Fatal("resume outcome", worked, err)
			}
			if tc == "approve_restart" && f.sends.Load() != 1 {
				t.Fatal("approved task not delivered once")
			}
			approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false WHERE member_id=$1", f.approver)
			if status := webhookStatus(t, a, encryptedRequest(t, body)); status == 200 {
				t.Fatal("revoked replay accepted")
			}
		})
	}
}

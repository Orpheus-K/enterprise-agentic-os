//go:build integration

package operations

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"net/http/httptest"
	"sync"
	"testing"
)

func prepareRecovery(t *testing.T, f *fixture, stage string) conversation.Reconciliation {
	t.Helper()
	_, err := f.pg.Admin.Exec(t.Context(), `INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,'app_a','task.reconcile');`, f.tenant, f.member)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pg.Admin.Exec(t.Context(), `UPDATE channel.tasks SET state='unknown',step_version=5 WHERE id=$1`, f.task)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pg.Admin.Exec(t.Context(), `INSERT INTO channel.audit(tenant_id,task_id,state,reason,step_version) VALUES($1,$2,$3,'synthetic',4)`, f.tenant, f.task, stage)
	if err != nil {
		t.Fatal(err)
	}
	return conversation.Reconciliation{TaskID: f.task, RequestID: uuid.New(), ExpectedVersion: 5, Decision: "not_executed", EvidenceID: uuid.New()}
}
func reconcileRequest(t *testing.T, f *fixture, in conversation.Reconciliation) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/ops/recovery", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+opsToken)
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}
func TestRecoveryDecisionsAndIdempotency(t *testing.T) {
	for _, tc := range []struct {
		stage, decision string
		want            conversation.State
	}{{"model_running", "not_executed", conversation.Pending}, {"delivery_running", "not_executed", conversation.DeliveryPending}, {"delivery_running", "delivered", conversation.Succeeded}, {"delivery_running", "unresolved", conversation.Unknown}} {
		t.Run(tc.stage+tc.decision, func(t *testing.T) {
			f := setup(t)
			in := prepareRecovery(t, f, tc.stage)
			in.Decision = tc.decision
			if w := request(f, "/api/ops/recovery?task_id="+f.task.String()); w.Code != 200 {
				t.Fatalf("inspect %d", w.Code)
			}
			first := reconcileRequest(t, f, in)
			if first.Code != 200 {
				t.Fatalf("reconcile %d", first.Code)
			}
			var out conversation.RecoverySnapshot
			if err := json.Unmarshal(first.Body.Bytes(), &out); err != nil || out.State != tc.want || out.Version != 6 {
				t.Fatalf("result %s", first.Body.String())
			}
			again := reconcileRequest(t, f, in)
			if again.Code != 200 || again.Body.String() != first.Body.String() {
				t.Fatal("duplicate changed result")
			}
			in.EvidenceID = uuid.New()
			if reconcileRequest(t, f, in).Code != 409 {
				t.Fatal("conflicting key accepted")
			}
			in.RequestID = uuid.New()
			if reconcileRequest(t, f, in).Code != 409 {
				t.Fatal("stale version accepted")
			}
			task, err := f.store.Claim(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == conversation.Pending || tc.want == conversation.DeliveryPending {
				if task == nil || task.Generation != 1 {
					t.Fatal("recovery not claimable")
				}
				if tc.want == conversation.DeliveryPending && task.Response != "secret-output" {
					t.Fatal("checkpoint lost")
				}
			} else if task != nil {
				t.Fatal("unknown or completed reclaimed")
			}
			var count int
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.reconciliations").Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate ledger")
			}
		})
	}
}
func TestRecoveryAuthorizationAndIsolation(t *testing.T) {
	for _, scope := range []string{"grant", "member", "mapping", "tenant", "app", "foreign_tenant", "revision"} {
		t.Run(scope, func(t *testing.T) {
			f := setup(t)
			in := prepareRecovery(t, f, "delivery_running")
			want := 404
			switch scope {
			case "grant":
				execRecovery(t, f, "UPDATE iam.operation_grants SET enabled=false WHERE capability='task.reconcile'")
				want = 403
			case "member":
				execRecovery(t, f, "UPDATE iam.members SET enabled=false")
				want = 401
			case "mapping":
				execRecovery(t, f, "UPDATE iam.identity_links SET enabled=false")
				want = 401
			case "tenant":
				execRecovery(t, f, "UPDATE iam.tenants SET enabled=false")
				want = 401
			case "app":
				in.TaskID = f.otherApp
			case "foreign_tenant":
				in.TaskID = f.otherTenant
			case "revision":
				execRecovery(t, f, "UPDATE channel.tasks SET model_revision='v2' WHERE id=$1", f.task)
			}
			if w := reconcileRequest(t, f, in); w.Code != want {
				t.Fatalf("status %d want %d", w.Code, want)
			}
			if w := request(f, "/api/ops/recovery?task_id="+in.TaskID.String()); w.Code != want {
				t.Fatalf("inspect %d", w.Code)
			}
		})
	}
	f := setup(t)
	in := prepareRecovery(t, f, "delivery_running")
	if reconcileRequest(t, f, in).Code != 200 {
		t.Fatal("initial")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "UPDATE iam.operation_grants SET enabled=false WHERE capability='task.reconcile'"); err != nil {
		t.Fatal(err)
	}
	if reconcileRequest(t, f, in).Code != 403 {
		t.Fatal("revoked replay accepted")
	}
}
func TestRecoveryAtomicityAndUnknownStage(t *testing.T) {
	f := setup(t)
	in := prepareRecovery(t, f, "model_running")
	in.Decision = "delivered"
	if reconcileRequest(t, f, in).Code != 409 {
		t.Fatal("model accepted delivery confirmation")
	}
	in.Decision = "not_executed"
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.reconciliations ADD CONSTRAINT reject_write CHECK(result_version<0) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if reconcileRequest(t, f, in).Code != 503 {
		t.Fatal("ledger failure hidden")
	}
	var state string
	var version int64
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT state,step_version FROM channel.tasks WHERE id=$1", f.task).Scan(&state, &version); err != nil || state != "unknown" || version != 5 {
		t.Fatal("failed ledger changed task")
	}
	if _, err := f.pg.Admin.Exec(t.Context(), "ALTER TABLE channel.reconciliations DROP CONSTRAINT reject_write; DELETE FROM channel.audit"); err != nil {
		t.Fatal(err)
	}
	if reconcileRequest(t, f, in).Code != 409 {
		t.Fatal("missing intent guessed")
	}
	in.Decision = "unresolved"
	if reconcileRequest(t, f, in).Code != 200 {
		t.Fatal("unknown cannot be recorded")
	}
}

func TestConcurrentRecoveryCommitsOnce(t *testing.T) {
	f := setup(t)
	in := prepareRecovery(t, f, "delivery_running")
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- reconcileRequest(t, f, in).Code }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("duplicate status %d", code)
		}
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.audit WHERE task_id=$1 AND step_version=6", f.task).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate transition")
	}
}
func TestRecoveryRejectsInvalidPayload(t *testing.T) {
	f := setup(t)
	for _, body := range []string{`{}`, `{"decision":"retry"}`, `{"extra":true}`, `{} {}`, string(make([]byte, 4097))} {
		r := httptest.NewRequest("POST", "/api/ops/recovery", bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+opsToken)
		w := httptest.NewRecorder()
		f.api.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid status %d", w.Code)
		}
	}
}

func execRecovery(t *testing.T, f *fixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.pg.Admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryEvidenceAndAuditFailClosed(t *testing.T) {
	f := setup(t)
	in := prepareRecovery(t, f, "delivery_running")
	execRecovery(t, f, "UPDATE channel.tasks SET response=NULL WHERE id=$1", f.task)
	if reconcileRequest(t, f, in).Code != 409 {
		t.Fatal("missing checkpoint accepted")
	}
	in.Decision = "unresolved"
	execRecovery(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT reject_reconcile CHECK(reason NOT LIKE 'reconciled_%') NOT VALID")
	if reconcileRequest(t, f, in).Code != 503 {
		t.Fatal("audit failure hidden")
	}
	var version int64
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT step_version FROM channel.tasks WHERE id=$1", f.task).Scan(&version); err != nil || version != 5 {
		t.Fatal("audit rollback failed")
	}
	execRecovery(t, f, "ALTER TABLE channel.audit DROP CONSTRAINT reject_reconcile")
	if reconcileRequest(t, f, in).Code != 200 {
		t.Fatal("unresolved failed")
	}
	var count int
	if err := f.db.Transaction(t.Context(), func(q database.Queries) error {
		return q.QueryRow(t.Context(), "SELECT count(*) FROM channel.reconciliations").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatal("pooled operator leaked ledger")
	}
	// A later resolution must not change the result returned for the earlier request.
	next := in
	next.RequestID = uuid.New()
	next.ExpectedVersion = 6
	next.Decision = "delivered"
	if reconcileRequest(t, f, next).Code != 200 {
		t.Fatal("resolution failed")
	}
	replay := reconcileRequest(t, f, in)
	var out conversation.RecoverySnapshot
	if replay.Code != 200 || json.Unmarshal(replay.Body.Bytes(), &out) != nil || out.State != conversation.Unknown || out.Version != 6 {
		t.Fatal("original result changed")
	}
}

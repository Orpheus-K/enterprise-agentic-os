//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/operations"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func noticeUnknown(t *testing.T) (*approvalFixture, conversation.NoticeSnapshot) {
	t.Helper()
	f := noticeEnvironment(t)
	a := approvalPending(t, f)
	approvalSQL(t, f, "INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'task.reconcile')", testBinding().TenantID, f.approver, testBinding().AppID)
	if _, err := f.store.NotifyOne(t.Context(), notifyFunc(func(context.Context, string, string, string) error { return errors.New("synthetic uncertain result") })); err != nil {
		t.Fatal(err)
	}
	out, err := f.store.InspectNotice(t.Context(), f.actor, f.task, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, out
}

func reconcileNoticeInput(n conversation.NoticeSnapshot, decision string) conversation.NoticeReconciliation {
	return conversation.NoticeReconciliation{TaskID: n.TaskID, NoticeID: n.NoticeID, RequestID: uuid.New(), ExpectedVersion: n.Version, Decision: decision, EvidenceID: uuid.New()}
}

func TestNoticeReconciliationDecisionsAndReplay(t *testing.T) {
	for _, decision := range []string{"not_executed", "delivered", "unresolved"} {
		t.Run(decision, func(t *testing.T) {
			f, n := noticeUnknown(t)
			in := reconcileNoticeInput(n, decision)
			want := "unknown"
			if decision == "not_executed" {
				want = "pending"
			}
			if decision == "delivered" {
				want = "sent"
			}
			var wg sync.WaitGroup
			for range 5 {
				wg.Go(func() {
					out, err := f.store.ReconcileNotice(t.Context(), f.actor, in)
					if err != nil || out.State != want || out.Version != n.Version+1 {
						t.Errorf("reconcile: %+v %v", out, err)
					}
				})
			}
			wg.Wait()
			var count int
			if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.notice_reconciliations WHERE evidence_id=$1", in.EvidenceID).Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate or missing evidence", err)
			}
			in.EvidenceID = uuid.New()
			if _, err := f.store.ReconcileNotice(t.Context(), f.actor, in); !errors.Is(err, conversation.ErrReconcileConflict) {
				t.Fatal("changed request accepted", err)
			}
			if decision == "not_executed" {
				calls := 0
				if _, err := f.store.NotifyOne(t.Context(), notifyFunc(func(context.Context, string, string, string) error { calls++; return nil })); err != nil || calls != 1 {
					t.Fatal("confirmed non-execution not resumed", err)
				}
				noticeState(t, f, "sent")
			}
		})
	}
}

func TestNoticeReconciliationDeniesAndRollsBack(t *testing.T) {
	for _, mode := range []string{"grant", "identity", "installation", "version", "audit", "revoked_replay"} {
		t.Run(mode, func(t *testing.T) {
			f, n := noticeUnknown(t)
			in := reconcileNoticeInput(n, "delivered")
			store := f.store
			switch mode {
			case "grant":
				approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false WHERE capability='task.reconcile'")
			case "identity":
				approvalSQL(t, f, "UPDATE iam.identity_links SET enabled=false WHERE member_id=$1", f.approver)
			case "installation":
				store = conversation.NewStore(f.db, testBinding().TenantID, "other", "approved-revision")
			case "version":
				in.ExpectedVersion++
			case "audit":
				approvalSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT fail_reconcile CHECK(reason<>'reconciled_delivered') NOT VALID")
			case "revoked_replay":
				if _, err := store.ReconcileNotice(t.Context(), f.actor, in); err != nil {
					t.Fatal(err)
				}
				approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false WHERE capability='task.reconcile'")
			}
			if _, err := store.ReconcileNotice(t.Context(), f.actor, in); err == nil {
				t.Fatal("invalid reconciliation accepted")
			}
			want := "unknown"
			if mode == "revoked_replay" {
				want = "sent"
			}
			noticeState(t, f, want)
		})
	}
}

func TestNoticeReconciliationHTTP(t *testing.T) {
	f, n := noticeUnknown(t)
	token := strings.Repeat("n", 32)
	api, err := operations.New(token, f.actor, f.store, nil, f.adapter.telemetry)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/ops/notice?task_id=" + n.TaskID.String() + "&notice_id=" + n.NoticeID.String()
	for _, auth := range []bool{false, true} {
		r := httptest.NewRequest("GET", path, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		want := 401
		if auth {
			want = 200
		}
		if w.Code != want {
			t.Fatal("inspect status", w.Code)
		}
	}
	raw, err := json.Marshal(reconcileNoticeInput(n, "delivered"))
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []bool{true, false} {
		body := append([]byte(nil), raw...)
		if extra {
			body = append(body, []byte(" {}")...)
		}
		r := httptest.NewRequest("POST", "/api/ops/notice", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		want := 200
		if extra {
			want = 400
		}
		if w.Code != want {
			t.Fatal("decision status", w.Code)
		}
	}
}

func TestNoticeLateResultCannotFinishNewAttempt(t *testing.T) {
	f := noticeEnvironment(t)
	a := approvalPending(t, f)
	approvalSQL(t, f, "INSERT INTO iam.operation_grants(tenant_id,member_id,app_id,capability) VALUES($1,$2,$3,'task.reconcile')", testBinding().TenantID, f.approver, testBinding().AppID)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	oldStart, oldRelease := make(chan struct{}), make(chan struct{})
	newStart, newRelease := make(chan struct{}), make(chan struct{})
	var oldOnce, newOnce sync.Once
	defer oldOnce.Do(func() { close(oldRelease) })
	defer newOnce.Do(func() { close(newRelease) })
	oldResult, newResult := make(chan error, 1), make(chan error, 1)
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal("sender did not reach barrier")
		}
	}
	go func() {
		_, err := f.store.NotifyOne(ctx, notifyFunc(func(context.Context, string, string, string) error {
			close(oldStart)
			select {
			case <-oldRelease:
			case <-ctx.Done():
			}
			return nil
		}))
		oldResult <- err
	}()
	wait(oldStart)
	approvalSQL(t, f, "UPDATE channel.approval_notices SET lease_until=clock_timestamp()-interval '1 second'")
	if _, err := f.store.NotifyOne(ctx, nil); err != nil {
		t.Fatal(err)
	}
	n, err := f.store.InspectNotice(ctx, f.actor, f.task, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReconcileNotice(ctx, f.actor, reconcileNoticeInput(n, "not_executed")); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := f.store.NotifyOne(ctx, notifyFunc(func(context.Context, string, string, string) error {
			close(newStart)
			select {
			case <-newRelease:
			case <-ctx.Done():
			}
			return nil
		}))
		newResult <- err
	}()
	wait(newStart)
	oldOnce.Do(func() { close(oldRelease) })
	select {
	case err := <-oldResult:
		if !errors.Is(err, conversation.ErrLeaseLost) {
			t.Fatal("old result accepted", err)
		}
	case <-ctx.Done():
		t.Fatal("old attempt stuck")
	}
	noticeState(t, f, "sending")
	newOnce.Do(func() { close(newRelease) })
	select {
	case err := <-newResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("new attempt stuck")
	}
	noticeState(t, f, "sent")
}

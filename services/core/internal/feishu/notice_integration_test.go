//go:build integration

package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type notifyFunc func(context.Context, string, string, string) error

func (f notifyFunc) Notify(ctx context.Context, recipient, id string, notice conversation.ApprovalNotice) error {
	text, err := json.Marshal(notice)
	if err != nil {
		return err
	}
	return f(ctx, recipient, id, string(text))
}

func noticeEnvironment(t *testing.T) *approvalFixture {
	t.Helper()
	f := approvalEnvironment(t)
	approvalSQL(t, f, "INSERT INTO iam.identity_links VALUES($1,'feishu',$2,'ou_approver',$3,true)", testBinding().TenantID, testBinding().Issuer(), f.approver)
	var err error
	f.store, err = f.store.WithApproval(conversation.ApprovalPolicy{MemberID: f.approver, TTLSeconds: 3600, NotifySubject: "ou_approver"})
	if err != nil {
		t.Fatal(err)
	}
	f.worker.Store = f.store
	f.adapter = testAdapter(t, f.store)
	enqueue(t, f.chainFixture)
	if err = f.pg.Admin.QueryRow(t.Context(), "SELECT id FROM channel.tasks").Scan(&f.task); err != nil {
		t.Fatal(err)
	}
	return f
}

func noticeState(t *testing.T, f *approvalFixture, want string) {
	t.Helper()
	var got string
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT state FROM channel.approval_notices").Scan(&got); err != nil || got != want {
		t.Fatalf("notice=%s want=%s err=%v", got, want, err)
	}
}

func TestApprovalNoticeConcurrentAndRestart(t *testing.T) {
	f := noticeEnvironment(t)
	a := approvalPending(t, f)
	f.db.Close()
	db, err := database.Open(t.Context(), f.pg.RuntimeURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	store := conversation.NewStore(db, testBinding().TenantID, testBinding().AppID, "approved-revision")
	var calls atomic.Int32
	sender := notifyFunc(func(_ context.Context, recipient, id, text string) error {
		calls.Add(1)
		if recipient != "ou_approver" || id != a.ID.String() || !strings.Contains(text, a.TaskID.String()) || !strings.Contains(text, a.ContentHash) {
			t.Error("notice identity/version or minimal content mismatch")
		}
		return nil
	})
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := store.NotifyOne(t.Context(), sender); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	noticeState(t, f, "sent")
	if calls.Load() != 1 {
		t.Fatal("duplicate notice")
	}
	if worked, err := store.NotifyOne(t.Context(), sender); err != nil || worked {
		t.Fatal("sent notice retried")
	}
}

func TestApprovalNoticeDenialUnknownAndExpiredIntent(t *testing.T) {
	for _, mode := range []string{"identity", "grant", "stale", "failed", "expired_intent", "wrong_installation", "wrong_member"} {
		t.Run(mode, func(t *testing.T) {
			f := noticeEnvironment(t)
			a := approvalPending(t, f)
			store := f.store
			switch mode {
			case "identity":
				approvalSQL(t, f, "UPDATE iam.identity_links SET enabled=false WHERE subject='ou_approver'")
			case "wrong_member":
				approvalSQL(t, f, "UPDATE iam.identity_links SET member_id=$1 WHERE subject='ou_approver'", f.member)
			case "grant":
				approvalSQL(t, f, "UPDATE iam.operation_grants SET enabled=false")
			case "stale":
				if _, err := store.DecideApproval(t.Context(), f.actor, decision(a)); err != nil {
					t.Fatal(err)
				}
			case "expired_intent":
				approvalSQL(t, f, "UPDATE channel.approval_notices SET state='sending',owner=id,lease_until=clock_timestamp()-interval '1 second'")
			case "wrong_installation":
				store = conversation.NewStore(f.db, testBinding().TenantID, "other", "approved-revision")
			}
			calls := 0
			sender := notifyFunc(func(context.Context, string, string, string) error {
				calls++
				return errors.New("synthetic uncertain send")
			})
			if _, err := store.NotifyOne(t.Context(), sender); err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if mode == "failed" || mode == "expired_intent" {
				want = "unknown"
			}
			if mode == "wrong_installation" {
				want = "pending"
			}
			noticeState(t, f, want)
			if mode == "failed" && calls != 1 || mode != "failed" && calls != 0 {
				t.Fatal("unexpected external call")
			}
			if worked, err := store.NotifyOne(t.Context(), sender); err != nil || worked {
				t.Fatal("terminal notice replayed")
			}
		})
	}
}

func TestApprovalAndNoticeCommitTogether(t *testing.T) {
	f := noticeEnvironment(t)
	approvalSQL(t, f, "ALTER TABLE channel.approval_notices ADD CONSTRAINT reject_notice CHECK(false) NOT VALID")
	if _, err := f.worker.ProcessOne(t.Context()); err == nil {
		t.Fatal("notice failure hidden")
	}
	var count int
	if err := f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM channel.approvals").Scan(&count); err != nil || count != 0 {
		t.Fatal("approval committed without notice", err)
	}
	f.state(t, "delivery_pending")
	approvalSQL(t, f, "ALTER TABLE channel.approval_notices DROP CONSTRAINT reject_notice")
	expireLease(t, f.chainFixture)
	approvalPending(t, f)
	noticeState(t, f, "pending")
}

func TestApprovalNoticeCommitFailureNeverResends(t *testing.T) {
	f := noticeEnvironment(t)
	approvalPending(t, f)
	approvalSQL(t, f, "ALTER TABLE channel.audit ADD CONSTRAINT reject_notice_sent CHECK(NOT(state='approval_notice' AND reason='sent')) NOT VALID")
	calls := 0
	sender := notifyFunc(func(context.Context, string, string, string) error { calls++; return nil })
	if _, err := f.store.NotifyOne(t.Context(), sender); err == nil {
		t.Fatal("notification outcome commit failure hidden")
	}
	noticeState(t, f, "sending")
	approvalSQL(t, f, "ALTER TABLE channel.audit DROP CONSTRAINT reject_notice_sent")
	approvalSQL(t, f, "UPDATE channel.approval_notices SET lease_until=clock_timestamp()-interval '1 second'")
	if _, err := f.store.NotifyOne(t.Context(), sender); err != nil {
		t.Fatal(err)
	}
	noticeState(t, f, "unknown")
	if calls != 1 {
		t.Fatal("unconfirmed notice sent again")
	}
}

func TestApprovalNoticeAmendCancelsOldVersion(t *testing.T) {
	f, a, d := quotePending(t, true)
	current, err := f.store.AmendQuotation(t.Context(), f.actor, quoteEdit(a, d))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	sender := notifyFunc(func(_ context.Context, _, id, _ string) error {
		calls++
		if id != current.ID.String() {
			t.Error("superseded notice sent")
		}
		return nil
	})
	for range 2 {
		if _, err = f.store.NotifyOne(t.Context(), sender); err != nil {
			t.Fatal(err)
		}
	}
	var cancelled, sent int
	if err = f.pg.Admin.QueryRow(t.Context(), "SELECT count(*) FILTER(WHERE state='cancelled'),count(*) FILTER(WHERE state='sent') FROM channel.approval_notices").Scan(&cancelled, &sent); err != nil || cancelled != 1 || sent != 1 || calls != 1 {
		t.Fatal("wrong notice versions", err)
	}
}

func TestApprovalNoticeFencesLateCompletion(t *testing.T) {
	f := noticeEnvironment(t)
	approvalPending(t, f)
	sender := notifyFunc(func(context.Context, string, string, string) error {
		approvalSQL(t, f, "UPDATE channel.approval_notices SET lease_until=clock_timestamp()-interval '1 second'")
		if _, err := f.store.NotifyOne(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	if _, err := f.store.NotifyOne(t.Context(), sender); !errors.Is(err, conversation.ErrLeaseLost) {
		t.Fatal("late notice overwrote unknown", err)
	}
	noticeState(t, f, "unknown")
}

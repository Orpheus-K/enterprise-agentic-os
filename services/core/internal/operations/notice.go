package operations

import (
	"agentos.local/core/internal/conversation"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
)

func (a *API) noticeOperation(ctx context.Context, r *http.Request, w http.ResponseWriter) (conversation.NoticeSnapshot, error) {
	if r.Method == http.MethodPost {
		var in conversation.NoticeReconciliation
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if r.URL.RawQuery != "" || d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
			return conversation.NoticeSnapshot{}, conversation.ErrInvalidReconciliation
		}
		return a.store.ReconcileNotice(ctx, a.identity, in)
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(values) != 2 || len(values["task_id"]) != 1 || len(values["notice_id"]) != 1 {
		return conversation.NoticeSnapshot{}, conversation.ErrInvalidReconciliation
	}
	task, err := uuid.Parse(values.Get("task_id"))
	if err != nil {
		return conversation.NoticeSnapshot{}, conversation.ErrInvalidReconciliation
	}
	id, err := uuid.Parse(values.Get("notice_id"))
	if err != nil {
		return conversation.NoticeSnapshot{}, conversation.ErrInvalidReconciliation
	}
	return a.store.InspectNotice(ctx, a.identity, task, id)
}

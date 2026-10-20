package operations

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/health"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/telemetry"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type API struct {
	token     [32]byte
	identity  identity.ExternalIdentity
	store     Store
	readiness *health.Readiness
	telemetry *telemetry.Telemetry
	knowledge *knowledge.Store
}

func New(token string, external identity.ExternalIdentity, store Store, ready *health.Readiness, tel *telemetry.Telemetry) (*API, error) {
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("operations token must contain at least 32 non-whitespace bytes")
	}
	if err := external.Validate(); err != nil {
		return nil, err
	}
	return &API{token: sha256.Sum256([]byte(token)), identity: external, store: store, readiness: ready, telemetry: tel}, nil
}

func (a *API) WithKnowledge(store *knowledge.Store) *API {
	copy := *a
	copy.knowledge = store
	return &copy
}
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	operation := "ops.unknown"
	switch r.URL.Path {
	case "/api/ops/knowledge/content", "/api/ops/knowledge/acl", "/api/ops/knowledge/health", "/api/ops/knowledge/read", "/api/ops/knowledge/vectors", "/api/ops/knowledge/search", "/api/ops/knowledge/sync/reconcile", "/api/ops/knowledge/sync/inspect", "/api/ops/knowledge/audit", "/api/ops/knowledge/stats":
		operation = "ops.knowledge"
	case "/api/ops/notice":
		operation = "ops.notice"
	case "/api/ops/quotation":
		operation = "ops.quotation"
	case "/api/ops/approval":
		operation = "ops.approval"
	case "/api/ops/recovery":
		operation = "ops.recovery"
	case "/api/ops/audit":
		operation = "ops.audit"
	case "/api/ops/tasks":
		operation = "ops.tasks"
	case "/api/ops/stats":
		operation = "ops.stats"
	case "/api/ops/status":
		operation = "ops.status"
	case "/api/ops/metrics":
		operation = "ops.metrics"
	}
	ctx, finish := a.telemetry.Start(r.Context(), operation)
	ok := false
	defer func() { finish(ok) }()
	// No cookies, query tokens, identity headers or remote trace propagation.
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(401)
		return
	}
	hash := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
	if subtle.ConstantTimeCompare(hash[:], a.token[:]) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(401)
		return
	}
	if (operation == "ops.knowledge" && r.Method != http.MethodPost) || (r.Method != http.MethodGet && !((operation == "ops.recovery" || operation == "ops.approval" || operation == "ops.quotation" || operation == "ops.notice" || operation == "ops.knowledge") && r.Method == http.MethodPost)) {
		w.WriteHeader(405)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if operation == "ops.unknown" {
		w.WriteHeader(404)
		return
	}
	var result any
	var err error
	if operation == "ops.knowledge" {
		if a.knowledge == nil {
			w.WriteHeader(404)
			return
		}
		result, err = a.knowledgeOperation(ctx, r, w)
	} else if operation == "ops.notice" {
		noticeStore, supported := a.store.(NoticeStore)
		if !supported {
			w.WriteHeader(404)
			return
		}
		result, err = a.noticeOperation(ctx, r, w, noticeStore)
	} else if operation == "ops.quotation" {
		quotationStore, supported := a.store.(QuotationStore)
		if !supported {
			w.WriteHeader(404)
			return
		}
		if r.Method == http.MethodGet {
			result, err = a.quotationDocument(ctx, r)
		} else {
			var in conversation.QuoteEdit
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
			decoder.DisallowUnknownFields()
			if r.URL.RawQuery != "" || decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF {
				w.WriteHeader(400)
				return
			}
			result, err = quotationStore.AmendQuotation(ctx, a.identity, in)
		}
	} else if operation == "ops.approval" {
		if r.Method == http.MethodPost {
			var in conversation.ApprovalDecision
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if r.URL.RawQuery != "" || decoder.Decode(&in) != nil {
				w.WriteHeader(400)
				return
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				w.WriteHeader(400)
				return
			}
			result, err = a.store.DecideApproval(ctx, a.identity, in)
		} else {
			query, parseErr := parseAudit(r)
			if parseErr != nil || len(r.URL.Query()) != 1 {
				w.WriteHeader(400)
				return
			}
			result, err = a.store.InspectApproval(ctx, a.identity, query.task)
		}
	} else if operation == "ops.recovery" {
		if r.Method == http.MethodPost {
			var in conversation.Reconciliation
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			decoder.DisallowUnknownFields()
			if r.URL.RawQuery != "" || decoder.Decode(&in) != nil {
				w.WriteHeader(400)
				return
			}
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				w.WriteHeader(400)
				return
			}
			result, err = a.store.Reconcile(ctx, a.identity, in)
		} else {
			query, parseErr := parseAudit(r)
			if parseErr != nil || len(r.URL.Query()) != 1 {
				w.WriteHeader(400)
				return
			}
			result, err = a.store.InspectRecovery(ctx, a.identity, query.task)
		}
	} else if operation == "ops.audit" {
		query, parseErr := parseAudit(r)
		if parseErr != nil {
			w.WriteHeader(400)
			return
		}
		result, err = a.store.ReadAudit(ctx, a.identity, query.task, query.after, query.limit)
	} else if operation == "ops.tasks" || operation == "ops.stats" {
		query, parseErr := parseReport(r, operation == "ops.tasks")
		if parseErr != nil {
			w.WriteHeader(400)
			return
		}
		if operation == "ops.tasks" {
			result, err = a.store.ListTasks(ctx, a.identity, query.window, query.after, query.limit)
		} else {
			result, err = a.store.Stats(ctx, a.identity, query.window)
		}
	} else {
		if r.URL.RawQuery != "" {
			w.WriteHeader(400)
			return
		}
		err = a.store.AuthorizeOperation(ctx, a.identity, "status.read")
		if err == nil {
			if operation == "ops.status" {
				result = a.readiness.Snapshot(ctx)
			} else {
				result, err = a.telemetry.Snapshot(ctx)
			}
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, conversation.ErrInvalidApproval), errors.Is(err, conversation.ErrInvalidReconciliation), errors.Is(err, conversation.ErrInvalidReport), errors.Is(err, knowledge.ErrInvalid):
			w.WriteHeader(400)
		case errors.Is(err, conversation.ErrTaskNotFound), errors.Is(err, knowledge.ErrUnavailable):
			w.WriteHeader(404)
		case errors.Is(err, conversation.ErrApprovalConflict), errors.Is(err, conversation.ErrReconcileConflict), errors.Is(err, knowledge.ErrConflict):
			w.WriteHeader(409)
		case errors.Is(err, identity.ErrUnauthenticated):
			w.WriteHeader(401)
		case errors.Is(err, identity.ErrForbidden):
			w.WriteHeader(403)
		default:
			w.WriteHeader(503)
		}
		return
	}
	if document, isDocument := result.(quotationDocument); isDocument {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+document.filename+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, err := io.WriteString(w, document.text)
		ok = err == nil
		return
	}
	ok = json.NewEncoder(w).Encode(result) == nil
}

type reportQuery struct {
	window conversation.ReportWindow
	after  uuid.UUID
	limit  int
}

// parseReport requires from/to as RFC 3339 instants; after/limit are accepted only for paging.
func parseReport(r *http.Request, paged bool) (reportQuery, error) {
	q := reportQuery{limit: 50}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, errors.New("invalid query encoding")
	}
	for key, v := range values {
		known := key == "from" || key == "to" || (paged && (key == "after" || key == "limit"))
		if len(v) != 1 || !known {
			return q, errors.New("invalid query")
		}
	}
	if q.window.From, err = time.Parse(time.RFC3339, values.Get("from")); err != nil {
		return q, errors.New("from required")
	}
	if q.window.To, err = time.Parse(time.RFC3339, values.Get("to")); err != nil {
		return q, errors.New("to required")
	}
	if values.Has("after") {
		if q.after, err = uuid.Parse(values.Get("after")); err != nil || q.after == uuid.Nil {
			return q, errors.New("invalid cursor")
		}
	}
	if values.Has("limit") {
		if q.limit, err = strconv.Atoi(values.Get("limit")); err != nil || q.limit < 1 || q.limit > 100 {
			return q, errors.New("invalid limit")
		}
	}
	return q, nil
}

type auditQuery struct {
	task  uuid.UUID
	after int64
	limit int
}

func parseAudit(r *http.Request) (auditQuery, error) {
	q := auditQuery{limit: 50}
	values, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil {
		return q, errors.New("invalid query encoding")
	}
	for key, v := range values {
		if len(v) != 1 || (key != "task_id" && key != "after" && key != "limit") {
			return q, errors.New("invalid query")
		}
	}
	var err error
	q.task, err = uuid.Parse(values.Get("task_id"))
	if err != nil || q.task == uuid.Nil {
		return q, errors.New("task_id required")
	}
	if values.Has("after") {
		q.after, err = strconv.ParseInt(values.Get("after"), 10, 64)
		if err != nil || q.after < 0 {
			return q, errors.New("invalid cursor")
		}
	}
	if values.Has("limit") {
		q.limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || q.limit < 1 || q.limit > 100 {
			return q, errors.New("invalid limit")
		}
	}
	return q, nil
}

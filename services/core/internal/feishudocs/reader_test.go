package feishudocs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func credential(t *testing.T) Credential {
	t.Helper()
	c, err := NewCredential(User{AppID: "cli_test", OpenID: "ou_reader", TenantKey: "tenant"}, "synthetic-secret")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func source(t *testing.T, mode string) (*Reader, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	var mu sync.Mutex
	userCalls, authCalls, metaCalls, pages := 0, 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic-secret" {
			t.Error("wrong method or token")
		}
		write := func(data any) {
			if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}); err != nil {
				t.Error(err)
			}
		}
		if mode == "remote_error" {
			_, _ = w.Write([]byte(`{"code":99991663,"msg":"synthetic-secret confidential-body"}`))
			return
		}
		if mode == "bad_json" {
			_, _ = w.Write([]byte(`{"secret":`))
			return
		}
		if mode == "missing_code" {
			_, _ = w.Write([]byte(`{"data":{"open_id":"ou_reader","tenant_key":"tenant"}}`))
			return
		}
		if mode == "oversized_response" {
			_, _ = w.Write([]byte(`{"code":0,"padding":"` + strings.Repeat("x", maxResponse) + `"}`))
			return
		}
		if mode == "http_error" {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		switch r.URL.Path {
		case "/open-apis/authen/v1/user_info":
			userCalls++
			user, tenant := "ou_reader", "tenant"
			if mode == "wrong_user" || mode == "identity_changed" && userCalls == 2 {
				user = "ou_other"
			}
			if mode == "wrong_tenant" {
				tenant = "other"
			}
			write(map[string]any{"open_id": user, "tenant_key": tenant})
		case "/open-apis/drive/v1/permissions/doxcTest/members/auth":
			authCalls++
			if r.URL.Query().Get("type") != "docx" || r.URL.Query().Get("action") != "view" {
				t.Error("permission target changed")
			}
			if mode == "missing_auth" {
				write(map[string]any{})
				return
			}
			allowed := mode != "denied" && !(mode == "revoked" && authCalls == 2)
			write(map[string]any{"auth_result": allowed})
		case "/open-apis/docx/v1/documents/doxcTest":
			metaCalls++
			rev, title, id := 7, "Synthetic document", "doxcTest"
			if mode == "changed_revision" && metaCalls == 2 {
				rev++
			}
			if mode == "changed_title" && metaCalls == 2 {
				title = "new title"
			}
			if mode == "wrong_document" {
				id = "other"
			}
			if mode == "zero_revision" {
				rev = 0
			}
			write(map[string]any{"document": map[string]any{"document_id": id, "revision_id": rev, "title": title}})
		case "/open-apis/docx/v1/documents/doxcTest/blocks":
			pages++
			if r.URL.Query().Get("document_revision_id") != "7" || r.URL.Query().Get("page_size") != "500" {
				t.Error("unpinned or unbounded page")
			}
			root := map[string]any{"block_id": "root", "parent_id": "", "block_type": 1, "children": []string{"p1"}, "page": map[string]any{"elements": []any{}}}
			leaf := map[string]any{"block_id": "p1", "parent_id": "root", "block_type": 2, "text": map[string]any{"elements": []any{map[string]any{"text_run": map[string]any{"content": "Synthetic body"}}}}, "future_field": map[string]any{"must_preserve": true}}
			if mode == "missing_child" {
				root["children"] = []string{"absent"}
			}
			if mode == "orphan" {
				leaf["parent_id"] = "other"
			}
			if mode == "duplicate" {
				leaf["block_id"] = "root"
			}
			var items []any
			next, more := "", false
			if pages == 1 {
				items = []any{root}
				next, more = "next", true
			} else {
				items = []any{leaf}
			}
			if mode == "empty_page" {
				items = []any{}
			}
			if mode == "repeated_cursor" {
				next, more = "next", true
			}
			if mode == "missing_cursor" {
				next, more = "", true
			}
			if mode == "page_limit" {
				next, more = strconv.Itoa(pages), true
				items = []any{leaf}
			}
			if mode == "block_limit" {
				items = make([]any, maxBlocks+1)
				for i := range items {
					items[i] = map[string]any{"block_id": "b" + strconv.Itoa(i), "block_type": 2}
				}
				more = false
			}
			if mode == "snapshot_limit" {
				items = []any{map[string]any{"block_id": "b" + strconv.Itoa(pages), "block_type": 2, "future_field": strings.Repeat("x", 1800000)}}
				next, more = strconv.Itoa(pages), true
			}
			if pages > 1 && mode == "valid" && r.URL.Query().Get("page_token") != "next" {
				t.Error("cursor not forwarded")
			}
			data := map[string]any{"items": items, "has_more": more, "page_token": next}
			if mode == "missing_more" {
				delete(data, "has_more")
			}
			write(data)
		default:
			t.Error("unexpected endpoint (including token fallback)", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(server.Close)
	return newReader("cli_test", server.URL, server.Client().Transport), &calls
}

func TestReadPinnedCompleteSnapshot(t *testing.T) {
	r, calls := source(t, "valid")
	got, err := r.Read(t.Context(), credential(t), "doxcTest")
	if err != nil {
		t.Fatal(err)
	}
	if got.DocumentID != "doxcTest" || got.Revision != 7 || got.Title != "Synthetic document" || len(got.Blocks) != 2 || got.User.OpenID != "ou_reader" || calls.Load() != 8 {
		t.Fatal("incomplete snapshot or missing final checks")
	}
	if !strings.Contains(string(got.Blocks[1]), `"future_field"`) || !strings.Contains(string(got.Blocks[1]), "Synthetic body") {
		t.Fatal("SDK dropped unknown block data")
	}
}

func TestReadRejectsPartialUntrustedAndChangedSnapshots(t *testing.T) {
	for _, mode := range []string{"denied", "revoked", "wrong_user", "wrong_tenant", "identity_changed", "changed_revision", "changed_title", "wrong_document", "zero_revision", "remote_error", "bad_json", "missing_code", "http_error", "oversized_response", "missing_auth", "empty_page", "missing_more", "repeated_cursor", "missing_cursor", "duplicate", "orphan", "missing_child", "page_limit", "block_limit", "snapshot_limit"} {
		t.Run(mode, func(t *testing.T) {
			r, calls := source(t, mode)
			got, err := r.Read(t.Context(), credential(t), "doxcTest")
			if err == nil || got.DocumentID != "" || got.Title != "" || got.Blocks != nil {
				t.Fatal("partial or unauthorized snapshot returned")
			}
			if strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "confidential-body") {
				t.Fatal("provider error leaked")
			}
			if mode == "remote_error" && calls.Load() != 1 {
				t.Fatal("invalid user token retried or fell back")
			}
		})
	}
}

func TestCheckAccessDoesNotReadContent(t *testing.T) {
	r, calls := source(t, "valid")
	if err := r.CheckAccess(t.Context(), credential(t), "doxcTest"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("permission probe read content")
	}
}

func TestInputCancellationAndCredentialRedaction(t *testing.T) {
	r, calls := source(t, "valid")
	for _, id := range []string{"", "../other", "x?token=secret", "https://other/doc", "has space"} {
		if _, err := r.Read(t.Context(), credential(t), id); err == nil {
			t.Fatal("unsafe document ID accepted")
		}
	}
	if _, err := r.Read(t.Context(), Credential{}, "doxcTest"); err == nil {
		t.Fatal("missing credential accepted")
	}
	wrongApp := credential(t)
	wrongApp.user.AppID = "cli_other"
	if _, err := r.Read(t.Context(), wrongApp, "doxcTest"); err == nil {
		t.Fatal("credential crossed application binding")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input made request")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Read(ctx, credential(t), "doxcTest"); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	c := credential(t)
	raw, err := json.Marshal(c)
	if err != nil || strings.Contains(string(raw), "synthetic-secret") || strings.Contains(fmt.Sprintf("%+v %#v", c, c), "synthetic-secret") {
		t.Fatal("credential formatting leaked")
	}
}

func TestRedirectCannotReceiveCredential(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	r := newReader("cli_test", redirect.URL, redirect.Client().Transport)
	if _, err := r.Read(t.Context(), credential(t), "doxcTest"); err == nil || hits.Load() != 0 {
		t.Fatal("redirect followed")
	}
}

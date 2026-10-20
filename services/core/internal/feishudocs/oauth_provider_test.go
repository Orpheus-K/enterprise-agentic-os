package feishudocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const oauthTestScope = "docx:document:readonly offline_access"

func oauthTestResponse() map[string]any {
	return map[string]any{"code": 0, "access_token": "access-secret", "refresh_token": "refresh-secret", "expires_in": 3600, "refresh_token_expires_in": 86400, "token_type": "Bearer", "scope": oauthTestScope}
}

func testOAuthProvider(t *testing.T, handler http.HandlerFunc) *oauthProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p, err := newOAuthProvider("cli_test", "application-secret")
	if err != nil {
		t.Fatal(err)
	}
	p.tokenURL = server.URL + "/token"
	p.revokeURL = server.URL + "/revoke"
	p.now = func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) }
	return p
}

func TestOAuthProviderExchangeRefreshRevoke(t *testing.T) {
	calls := 0
	p := testOAuthProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Authorization") != "" {
			t.Error("incorrect OAuth request headers")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.Form.Get("client_id") != "cli_test" || r.Form.Get("client_secret") != "application-secret" {
			t.Error("missing confidential client credentials")
		}
		switch calls {
		case 1:
			if r.URL.Path != "/token" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "one-use-code" || r.Form.Get("redirect_uri") != "https://example.test/callback" || r.Form.Get("code_verifier") != strings.Repeat("v", 43) {
				t.Error("incorrect exchange")
			}
		case 2:
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-secret" {
				t.Error("incorrect refresh")
			}
		case 3, 4:
			if r.URL.Path != "/revoke" || r.Form.Get("token") == "" || r.Form.Get("token_type_hint") == "" {
				t.Error("incorrect revocation")
			}
			return // RFC7009 empty HTTP200, including already-revoked tokens.
		default:
			t.Error("unexpected retry")
		}
		if r.Form.Get("scope") != oauthTestScope {
			t.Error("scope mismatch")
		}
		if err := json.NewEncoder(w).Encode(oauthTestResponse()); err != nil {
			t.Error(err)
		}
	})
	tokens, err := p.exchange(t.Context(), "one-use-code", "https://example.test/callback", strings.Repeat("v", 43), oauthTestScope)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.Access != "access-secret" || tokens.Refresh != "refresh-secret" || !tokens.ExpiresAt.Equal(p.now().Add(time.Hour)) || !tokens.RefreshExpiresAt.Equal(p.now().Add(24*time.Hour)) {
		t.Fatal("incorrect token lifetime")
	}
	if _, err = p.refresh(t.Context(), tokens.Refresh, tokens.Scope); err != nil {
		t.Fatal(err)
	}
	if err = p.revoke(t.Context(), tokens.Access, "access_token"); err != nil {
		t.Fatal(err)
	}
	if err = p.revoke(t.Context(), tokens.Refresh, "refresh_token"); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
	for _, value := range []any{p, tokens} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), "secret") {
				t.Fatal("logging exposed credential")
			}
		}
	}
}

func TestOAuthProviderRejectsAmbiguousResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		edit   func(map[string]any)
		body   string
		want   error
	}{
		{"missing-code", 200, func(v map[string]any) { delete(v, "code") }, "", ErrOAuthUncertain},
		{"expired-code", 200, func(v map[string]any) { v["code"] = 20004 }, "", ErrOAuthExpired},
		{"expired-refresh", 400, func(v map[string]any) { v["code"] = 20037 }, "", ErrOAuthExpired},
		{"revoked-refresh", 400, func(v map[string]any) { v["code"] = 20064 }, "", ErrOAuthRevoked},
		{"denied", 400, func(v map[string]any) { v["code"] = 20010 }, "", ErrOAuthRejected},
		{"server-failure", 503, nil, "", ErrOAuthUncertain},
		{"malformed", 200, nil, "{", ErrOAuthUncertain},
		{"oversize", 200, nil, strings.Repeat("x", 65537), ErrOAuthUncertain},
		{"scope-expansion", 200, func(v map[string]any) { v["scope"] = oauthTestScope + " unauthorized" }, "", ErrOAuthUncertain},
		{"duplicate-scope", 200, func(v map[string]any) { v["scope"] = oauthTestScope + " offline_access" }, "", ErrOAuthUncertain},
		{"empty-token", 200, func(v map[string]any) { v["access_token"] = "" }, "", ErrOAuthUncertain},
		{"token-type", 200, func(v map[string]any) { v["token_type"] = "MAC" }, "", ErrOAuthUncertain},
		{"expired", 200, func(v map[string]any) { v["expires_in"] = 0 }, "", ErrOAuthUncertain},
		{"overflow", 200, func(v map[string]any) { v["expires_in"] = int64(1<<63 - 1) }, "", ErrOAuthUncertain},
		{"missing-refresh-expiry", 200, func(v map[string]any) { delete(v, "refresh_token_expires_in") }, "", ErrOAuthUncertain},
		{"refresh-without-offline", 200, func(v map[string]any) { v["scope"] = "docx:document:readonly" }, "", ErrOAuthUncertain},
		{"expiry-without-refresh", 200, func(v map[string]any) { delete(v, "refresh_token") }, "", ErrOAuthUncertain},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := testOAuthProvider(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				if tc.body != "" {
					if _, err := w.Write([]byte(tc.body)); err != nil {
						t.Error(err)
					}
					return
				}
				v := oauthTestResponse()
				if tc.edit != nil {
					tc.edit(v)
				}
				v["error_description"] = "access-secret refresh-secret application-secret"
				if err := json.NewEncoder(w).Encode(v); err != nil {
					t.Error(err)
				}
			})
			got, err := p.refresh(t.Context(), "refresh-secret", oauthTestScope)
			if !errors.Is(err, tc.want) || got.Access != "" || got.Refresh != "" {
				t.Fatalf("unexpected response: %v", err)
			}
			if strings.Contains(err.Error(), "secret") || calls != 1 {
				t.Fatal("credential leakage or retry")
			}
		})
	}
}

func TestOAuthProviderCancellationRedirectAndValidation(t *testing.T) {
	calls := 0
	p := testOAuthProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "https://example.test/leak", http.StatusTemporaryRedirect)
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.refresh(ctx, "refresh-secret", oauthTestScope); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("canceled request sent")
	}
	if _, err := p.exchange(t.Context(), "code", "http://example.test/callback", strings.Repeat("v", 43), oauthTestScope); !errors.Is(err, ErrOAuthInvalid) {
		t.Fatal(err)
	}
	if _, err := p.refresh(t.Context(), "refresh-secret", oauthTestScope+" "); !errors.Is(err, ErrOAuthInvalid) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("invalid request sent")
	}
	if _, err := p.refresh(t.Context(), "refresh-secret", oauthTestScope); !errors.Is(err, ErrOAuthUncertain) {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("redirect followed or retried")
	}
}

func TestOAuthProviderRevokeFailure(t *testing.T) {
	for _, body := range []string{`{"code":20010,"error_description":"application-secret"}`, `{`, strings.Repeat("x", 65537)} {
		p := testOAuthProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			if _, err := w.Write([]byte(body)); err != nil {
				t.Error(err)
			}
		})
		err := p.revoke(t.Context(), "access-secret", "access_token")
		if !errors.Is(err, ErrOAuthUncertain) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe revocation result: %v", err)
		}
	}
}

func TestOAuthProviderLifetimeAndScopeReduction(t *testing.T) {
	p := testOAuthProvider(t, func(w http.ResponseWriter, r *http.Request) {
		v := oauthTestResponse()
		delete(v, "refresh_token")
		delete(v, "refresh_token_expires_in")
		v["scope"] = "docx:document:readonly"
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Error(err)
		}
	})
	got, err := p.refresh(t.Context(), "refresh-secret", oauthTestScope)
	if err != nil || got.Refresh != "" || !got.RefreshExpiresAt.IsZero() || got.Scope != "docx:document:readonly" {
		t.Fatalf("scope reduction: %v", err)
	}
	start := p.now()
	reads := 0
	p.now = func() time.Time {
		reads++
		if reads == 1 {
			return start
		}
		return start.Add(time.Hour)
	}
	if _, err := p.refresh(t.Context(), "refresh-secret", oauthTestScope); !errors.Is(err, ErrOAuthExpired) {
		t.Fatalf("token expired in transit: %v", err)
	}
}

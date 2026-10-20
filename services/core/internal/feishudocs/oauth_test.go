package feishudocs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

type oauthRoundTrip func(*http.Request) (*http.Response, error)

func (f oauthRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type oauthMemory struct {
	mu         sync.Mutex
	revision   int64
	envelope   oauthEnvelope
	failAction string
}

func (m *oauthMemory) load(context.Context) (int64, oauthEnvelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.revision == 0 {
		return 0, oauthEnvelope{}, ErrOAuthMissing
	}
	return m.revision, oauthEnvelope{KeyID: m.envelope.KeyID, Ciphertext: bytes.Clone(m.envelope.Ciphertext)}, nil
}
func (m *oauthMemory) save(_ context.Context, revision int64, envelope oauthEnvelope, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAction == action {
		return errors.New("synthetic storage failure")
	}
	if m.revision != revision {
		return ErrOAuthConflict
	}
	m.revision++
	m.envelope = oauthEnvelope{KeyID: envelope.KeyID, Ciphertext: bytes.Clone(envelope.Ciphertext)}
	return nil
}

func oauthFlow(t *testing.T, mode string) (*OAuth, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var tokens, revokes atomic.Int32
	p := testOAuthProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/revoke" {
			revokes.Add(1)
			if mode == "revoke-failed" {
				w.WriteHeader(503)
			}
			return
		}
		n := tokens.Add(1)
		if mode == "unknown" {
			w.WriteHeader(503)
			return
		}
		v := oauthTestResponse()
		v["access_token"] = "synthetic-secret"
		v["refresh_token"] = fmt.Sprintf("refresh-%d", n)
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Error(err)
		}
	})
	readerMode := "valid"
	if mode == "wrong-user" {
		readerMode = "wrong_user"
	}
	reader, _ := source(t, readerMode)
	o := &OAuth{store: &oauthMemory{}, keys: oauthTestKeys(t, "old"), provider: p, reader: reader, tenant: uuid.New(), user: credential(t).user}
	return o, &tokens, &revokes
}

func oauthBegin(t *testing.T, o *OAuth) string {
	t.Helper()
	address, err := o.Begin(t.Context(), "https://example.test/callback", oauthTestScope)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	_, record, err := o.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	challenge := sha256.Sum256([]byte(record.Verifier))
	if u.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("PKCE not bound")
	}
	return u.Query().Get("state")
}

func TestOAuthLifecycleAndReader(t *testing.T) {
	o, tokens, revokes := oauthFlow(t, "valid")
	state := oauthBegin(t, o)
	if err := o.Complete(t.Context(), "wrong-state", "code"); !errors.Is(err, ErrOAuthInvalid) || tokens.Load() != 0 {
		t.Fatal("invalid state sent code", err)
	}
	if err := o.Complete(t.Context(), state, "code"); err != nil {
		t.Fatal(err)
	}
	if err := o.Complete(t.Context(), state, "code"); !errors.Is(err, ErrOAuthConflict) || tokens.Load() != 1 {
		t.Fatal("replayed callback", err)
	}
	if _, err := o.Begin(t.Context(), "https://example.test/callback", oauthTestScope); !errors.Is(err, ErrOAuthConflict) {
		t.Fatal("active authorization overwritten", err)
	}
	if _, err := o.reader.Read(t.Context(), o.Credential(), "doxcTest"); err != nil {
		t.Fatal("reader did not consume OAuth", err)
	}
	before := o.provider.now()
	o.provider.now = func() time.Time { return before.Add(time.Hour) }
	if err := o.reader.CheckAccess(t.Context(), o.Credential(), "doxcTest"); err != nil {
		t.Fatal("expired token did not refresh", err)
	}
	if tokens.Load() != 2 {
		t.Fatal("refresh count", tokens.Load())
	}
	// A new instance resumes the encrypted ledger, with no in-memory token cache.
	restarted := *o
	if err := restarted.reader.CheckAccess(t.Context(), restarted.Credential(), "doxcTest"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Revoke(t.Context()); err != nil {
		t.Fatal(err)
	}
	if revokes.Load() != 2 {
		t.Fatal("both tokens must be revoked")
	}
	if err := restarted.Revoke(t.Context()); err != nil || revokes.Load() != 2 {
		t.Fatal("completed revoke replayed", err)
	}
	if err := o.reader.CheckAccess(t.Context(), o.Credential(), "doxcTest"); !errors.Is(err, ErrOAuthRevoked) {
		t.Fatal("old reference survived revoke", err)
	}
	_, record, err := o.load(t.Context())
	if err != nil || record.Tokens.Access != "" || record.Tokens.Refresh != "" {
		t.Fatal("completed revoke retained tokens", err)
	}
	if _, err := o.Begin(t.Context(), "https://example.test/callback", oauthTestScope); err != nil {
		t.Fatal("reauthorization denied", err)
	}
}

func TestOAuthIntentFailureNeverReplays(t *testing.T) {
	for _, mode := range []string{"unknown", "wrong-user", "intent-write", "result-write"} {
		t.Run(mode, func(t *testing.T) {
			o, tokens, _ := oauthFlow(t, mode)
			state := oauthBegin(t, o)
			m := o.store.(*oauthMemory)
			if mode == "intent-write" {
				m.failAction = "exchange"
			}
			if mode == "result-write" {
				m.failAction = "activate"
			}
			if err := o.Complete(t.Context(), state, "code"); err == nil {
				t.Fatal("failure accepted")
			}
			if mode == "intent-write" {
				if tokens.Load() != 0 {
					t.Fatal("external call before durable intent")
				}
				return
			}
			if _, err := o.access(t.Context()); err == nil {
				t.Fatal("unverified/unknown token released")
			}
			if err := o.Complete(t.Context(), state, "code"); !errors.Is(err, ErrOAuthConflict) || tokens.Load() != 1 {
				t.Fatal("unknown result replayed", err)
			}
		})
	}
}

func TestOAuthExpiryAndRevocationFailure(t *testing.T) {
	t.Run("expired-session", func(t *testing.T) {
		o, calls, _ := oauthFlow(t, "valid")
		state := oauthBegin(t, o)
		now := o.provider.now()
		o.provider.now = func() time.Time { return now.Add(5 * time.Minute) }
		if err := o.Complete(t.Context(), state, "code"); !errors.Is(err, ErrOAuthExpired) || calls.Load() != 0 {
			t.Fatal(err)
		}
	})
	t.Run("expired-refresh", func(t *testing.T) {
		o, calls, _ := oauthFlow(t, "valid")
		if err := o.Complete(t.Context(), oauthBegin(t, o), "code"); err != nil {
			t.Fatal(err)
		}
		now := o.provider.now()
		o.provider.now = func() time.Time { return now.Add(24 * time.Hour) }
		if _, err := o.access(t.Context()); !errors.Is(err, ErrOAuthExpired) || calls.Load() != 1 {
			t.Fatal("expired refresh replayed", err)
		}
	})
	t.Run("remote-revoke-failed", func(t *testing.T) {
		o, _, calls := oauthFlow(t, "revoke-failed")
		if err := o.Complete(t.Context(), oauthBegin(t, o), "code"); err != nil {
			t.Fatal(err)
		}
		if err := o.Revoke(t.Context()); err == nil {
			t.Fatal("remote failure hidden")
		}
		if _, err := o.access(t.Context()); !errors.Is(err, ErrOAuthRevoked) {
			t.Fatal("local token usable after revoke started", err)
		}
		if calls.Load() != 2 {
			t.Fatal("did not attempt both revocations")
		}
	})
	t.Run("unknown-issued-token", func(t *testing.T) {
		o, _, _ := oauthFlow(t, "unknown")
		if err := o.Complete(t.Context(), oauthBegin(t, o), "code"); err == nil {
			t.Fatal("expected failure")
		}
		if err := o.Revoke(t.Context()); !errors.Is(err, ErrOAuthUncertain) {
			t.Fatal("unknown remote token claimed revoked", err)
		}
		if _, err := o.Begin(t.Context(), "https://example.test/callback", oauthTestScope); !errors.Is(err, ErrOAuthConflict) {
			t.Fatal("unknown authorization silently replaced", err)
		}
	})
}

func TestOAuthConcurrentRefreshAndMidReadRevocation(t *testing.T) {
	o, calls, _ := oauthFlow(t, "valid")
	if err := o.Complete(t.Context(), oauthBegin(t, o), "code"); err != nil {
		t.Fatal(err)
	}
	now := o.provider.now()
	o.provider.now = func() time.Time { return now.Add(time.Hour) }
	entered, release := make(chan struct{}), make(chan struct{})
	o.provider.client.Transport = oauthRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/token" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	result := make(chan error, 1)
	go func() { _, err := o.access(t.Context()); result <- err }()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("refresh did not start")
	}
	_, blocked := o.access(t.Context())
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if !errors.Is(blocked, ErrOAuthUncertain) || calls.Load() != 2 {
		t.Fatal("concurrent refresh was replayed", blocked)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var data any
		if r.URL.Path == "/open-apis/authen/v1/user_info" {
			data = map[string]string{"open_id": o.user.OpenID, "tenant_key": o.user.TenantKey}
		} else {
			if err := o.Revoke(t.Context()); err != nil {
				t.Error(err)
			}
			data = map[string]bool{"auth_result": true}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	reader := newReader(o.user.AppID, server.URL, http.DefaultTransport)
	if err := reader.CheckAccess(t.Context(), o.Credential(), "doxcTest"); !errors.Is(err, ErrOAuthRevoked) {
		t.Fatal("revocation during read accepted", err)
	}
}

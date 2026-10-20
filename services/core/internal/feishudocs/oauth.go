package feishudocs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"time"

	"agentos.local/core/internal/database"
	"github.com/google/uuid"
)

type oauthPersistence interface {
	load(context.Context) (int64, oauthEnvelope, error)
	save(context.Context, int64, oauthEnvelope, string) error
}

type oauthState struct {
	Stage         string
	Tokens        oauthTokens
	StateHash     []byte
	Verifier      string
	Redirect      string
	Scope         string
	Deadline      time.Time
	RemoteUnknown bool
}

func (oauthState) String() string   { return "[OAuth state redacted]" }
func (oauthState) GoString() string { return "[OAuth state redacted]" }

// OAuth is bound by trusted installation configuration, never callback input.
// The host must authenticate the initiating user and callback to this binding.
// No public HTTP endpoint is registered by this library.
type OAuth struct {
	store    oauthPersistence
	keys     *OAuthKeys
	provider *oauthProvider
	reader   *Reader
	tenant   uuid.UUID
	user     User
}

func (OAuth) String() string   { return "[OAuth credentials redacted]" }
func (OAuth) GoString() string { return "[OAuth credentials redacted]" }

func NewOAuth(db *database.DB, tenant uuid.UUID, user User, appSecret string, keys *OAuthKeys) (*OAuth, error) {
	if keys == nil || keys.keys[keys.active] == nil {
		return nil, ErrOAuthSecrets
	}
	store, err := newOAuthLedger(db, tenant, user)
	if err != nil {
		return nil, err
	}
	provider, err := newOAuthProvider(user.AppID, appSecret)
	if err != nil {
		return nil, err
	}
	reader, err := New(user.AppID)
	if err != nil {
		return nil, err
	}
	return &OAuth{store: store, keys: keys, provider: provider, reader: reader, tenant: tenant, user: user}, nil
}

func (o *OAuth) load(ctx context.Context) (int64, oauthState, error) {
	revision, envelope, err := o.store.load(ctx)
	if err != nil {
		return 0, oauthState{}, err
	}
	plain, err := o.keys.open(oauthSecretBinding{o.tenant, o.user, revision}, envelope)
	if err != nil {
		return 0, oauthState{}, err
	}
	defer clear(plain)
	var state oauthState
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF {
		return 0, oauthState{}, ErrOAuthSecrets
	}
	switch state.Stage {
	case "pending", "exchanging", "verifying", "active", "refreshing", "revoking", "revoked":
	default:
		return 0, oauthState{}, ErrOAuthSecrets
	}
	return revision, state, nil
}

func (o *OAuth) save(ctx context.Context, revision int64, state oauthState, action string) error {
	plain, err := json.Marshal(state)
	if err != nil {
		return ErrOAuthSecrets
	}
	defer clear(plain)
	envelope, err := o.keys.seal(oauthSecretBinding{o.tenant, o.user, revision + 1}, plain)
	if err != nil {
		return err
	}
	return o.store.save(ctx, revision, envelope, action)
}

// Begin persists state and PKCE before returning the authorization URL. A new
// session cannot silently replace usable credentials or an unknown operation.
func (o *OAuth) Begin(ctx context.Context, redirect, scope string) (string, error) {
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", ErrOAuthInvalid
	}
	if _, ok := oauthScopes(scope); !ok {
		return "", ErrOAuthInvalid
	}
	revision, old, err := o.load(ctx)
	if err != nil && !errors.Is(err, ErrOAuthMissing) {
		return "", err
	}
	if err == nil && !(old.Stage == "revoked" && !old.RemoteUnknown || old.Stage == "pending" && !o.provider.now().Before(old.Deadline)) {
		return "", ErrOAuthConflict
	}
	stateBytes, verifierBytes := make([]byte, 32), make([]byte, 32)
	if _, err = rand.Read(stateBytes); err != nil {
		return "", ErrOAuthInvalid
	}
	if _, err = rand.Read(verifierBytes); err != nil {
		return "", ErrOAuthInvalid
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	hash := sha256.Sum256([]byte(state))
	challenge := sha256.Sum256([]byte(verifier))
	record := oauthState{Stage: "pending", StateHash: hash[:], Verifier: verifier, Redirect: redirect, Scope: scope, Deadline: o.provider.now().Add(5 * time.Minute)}
	if err = o.save(ctx, revision, record, "authorize"); err != nil {
		return "", err
	}
	query := url.Values{"client_id": {o.user.AppID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {scope}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	return "https://accounts.feishu.cn/open-apis/authen/v1/authorize?" + query.Encode(), nil
}

func (o *OAuth) Complete(ctx context.Context, state, code string) error {
	if !oauthSecret(state) || !oauthSecret(code) {
		return ErrOAuthInvalid
	}
	revision, record, err := o.load(ctx)
	if err != nil {
		return err
	}
	if record.Stage != "pending" {
		return ErrOAuthConflict
	}
	hash := sha256.Sum256([]byte(state))
	if subtle.ConstantTimeCompare(hash[:], record.StateHash) != 1 {
		return ErrOAuthInvalid
	}
	if !o.provider.now().Before(record.Deadline) {
		return ErrOAuthExpired
	}
	record.Stage = "exchanging"
	if err = o.save(ctx, revision, record, "exchange"); err != nil {
		return err
	}
	tokens, err := o.provider.exchange(ctx, code, record.Redirect, record.Verifier, record.Scope)
	if err != nil {
		return err
	} // Durable intent deliberately remains non-replayable.
	return o.activate(ctx, revision+1, tokens)
}

// Preserve returned secrets before identity verification, so a failed identity
// check can still revoke the known tokens. No token is usable in verifying.
func (o *OAuth) activate(ctx context.Context, revision int64, tokens oauthTokens) error {
	record := oauthState{Stage: "verifying", Tokens: tokens}
	if err := o.save(ctx, revision, record, "activate"); err != nil {
		return errors.Join(ErrOAuthUncertain, err)
	}
	c, err := NewCredential(o.user, tokens.Access)
	if err != nil {
		return err
	}
	if err = o.reader.verifyUser(ctx, c); err != nil {
		return err
	}
	if !o.provider.now().Before(tokens.ExpiresAt) {
		return ErrOAuthExpired
	}
	record.Stage = "active"
	return o.save(ctx, revision+1, record, "activate")
}

// Credential returns a reference, not a snapshot of an access token. Reader
// resolves it at the start and verifies its generation before returning data.
func (o *OAuth) Credential() Credential { return Credential{user: o.user, oauth: o} }

// CheckReady verifies a stored authorization without contacting the provider.
// Expired access with a still-valid refresh token is ready for a lazy refresh.
func (o *OAuth) CheckReady(ctx context.Context) error {
	_, record, err := o.load(ctx)
	if err != nil {
		return err
	}
	if record.Stage == "revoked" || record.Stage == "revoking" {
		return ErrOAuthRevoked
	}
	if record.Stage != "active" {
		return ErrOAuthUncertain
	}
	if !oauthSecret(record.Tokens.Access) {
		return ErrOAuthSecrets
	}
	if !o.provider.now().Before(record.Tokens.ExpiresAt) && (record.Tokens.Refresh == "" || !o.provider.now().Before(record.Tokens.RefreshExpiresAt)) {
		return ErrOAuthExpired
	}
	return nil
}

func (o *OAuth) access(ctx context.Context) (Credential, error) {
	revision, record, err := o.load(ctx)
	if err != nil {
		return Credential{}, err
	}
	if record.Stage == "revoked" || record.Stage == "revoking" {
		return Credential{}, ErrOAuthRevoked
	}
	if record.Stage != "active" {
		return Credential{}, ErrOAuthUncertain
	}
	if !o.provider.now().Before(record.Tokens.ExpiresAt) {
		if record.Tokens.Refresh == "" || !o.provider.now().Before(record.Tokens.RefreshExpiresAt) {
			return Credential{}, ErrOAuthExpired
		}
		record.Stage = "refreshing"
		if err = o.save(ctx, revision, record, "refresh"); err != nil {
			return Credential{}, err
		}
		tokens, err := o.provider.refresh(ctx, record.Tokens.Refresh, record.Tokens.Scope)
		if err != nil {
			return Credential{}, err
		}
		if tokens.Refresh != "" && tokens.Refresh == record.Tokens.Refresh {
			return Credential{}, ErrOAuthUncertain
		}
		if err = o.activate(ctx, revision+1, tokens); err != nil {
			return Credential{}, err
		}
		revision += 3
		record = oauthState{Stage: "active", Tokens: tokens}
		if err = o.current(ctx, revision); err != nil {
			return Credential{}, err
		}
	}
	c, err := NewCredential(o.user, record.Tokens.Access)
	if err != nil {
		return Credential{}, ErrOAuthSecrets
	}
	c.oauth = o
	c.generation = revision
	return c, nil
}

func (o *OAuth) current(ctx context.Context, generation int64) error {
	revision, record, err := o.load(ctx)
	if err != nil {
		return err
	}
	if record.Stage == "revoked" || record.Stage == "revoking" {
		return ErrOAuthRevoked
	}
	if revision != generation || record.Stage != "active" {
		return ErrOAuthConflict
	}
	if !o.provider.now().Before(record.Tokens.ExpiresAt) {
		return ErrOAuthExpired
	}
	return nil
}

// Revoke denies local use before remote calls. RFC7009 revocations are safe to
// repeat; exchange/refresh intents are not. Unknown issued tokens require
// provider-side authorization review and never produce a successful logout.
func (o *OAuth) Revoke(ctx context.Context) error {
	revision, record, err := o.load(ctx)
	if err != nil {
		return err
	}
	if record.Stage == "revoked" {
		if record.RemoteUnknown {
			return ErrOAuthUncertain
		}
		return nil
	}
	if record.Stage != "revoking" {
		record.RemoteUnknown = record.Stage == "exchanging" || record.Stage == "refreshing"
		record.Stage = "revoking"
		record.StateHash = nil
		record.Verifier = ""
		if err = o.save(ctx, revision, record, "revoke"); err != nil {
			return err
		}
		revision++
	}
	var failures []error
	if record.Tokens.Refresh != "" {
		failures = append(failures, o.provider.revoke(ctx, record.Tokens.Refresh, "refresh_token"))
	}
	if record.Tokens.Access != "" {
		failures = append(failures, o.provider.revoke(ctx, record.Tokens.Access, "access_token"))
	}
	if err = errors.Join(failures...); err != nil {
		return err
	}
	unknown := record.RemoteUnknown
	if err = o.save(ctx, revision, oauthState{Stage: "revoked", RemoteUnknown: unknown}, "revoked"); err != nil {
		return err
	}
	if unknown {
		return ErrOAuthUncertain
	}
	return nil
}

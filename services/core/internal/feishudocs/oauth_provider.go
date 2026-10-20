package feishudocs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrOAuthInvalid   = errors.New("invalid OAuth request or response")
	ErrOAuthExpired   = errors.New("OAuth credential expired; authorization required")
	ErrOAuthRevoked   = errors.New("OAuth authorization revoked")
	ErrOAuthUncertain = errors.New("OAuth result unknown; do not replay the credential")
	ErrOAuthRejected  = errors.New("OAuth provider rejected request")
)

// OAuthError deliberately excludes remote text, request bodies and credentials.
type OAuthError struct {
	Status, Code int
	cause        error
}

func (e *OAuthError) Error() string {
	return fmt.Sprintf("OAuth HTTP %d code %d: %v", e.Status, e.Code, e.cause)
}
func (e *OAuthError) Unwrap() error { return e.cause }

type oauthTokens struct {
	Access           string
	Refresh          string
	Scope            string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

func (oauthTokens) String() string   { return "[OAuth credentials redacted]" }
func (oauthTokens) GoString() string { return "[OAuth credentials redacted]" }

type oauthProvider struct {
	appID, secret       string
	tokenURL, revokeURL string
	client              *http.Client
	now                 func() time.Time
}

func (oauthProvider) String() string   { return "[OAuth provider credentials redacted]" }
func (oauthProvider) GoString() string { return "[OAuth provider credentials redacted]" }

func newOAuthProvider(appID, secret string) (*oauthProvider, error) {
	if !identifier(appID) || !strings.HasPrefix(appID, "cli_") || !oauthSecret(secret) {
		return nil, ErrOAuthInvalid
	}
	return &oauthProvider{appID: appID, secret: secret, tokenURL: "https://accounts.feishu.cn/oauth/v3/token", revokeURL: "https://accounts.feishu.cn/oauth/v1/revoke", now: time.Now,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrOAuthInvalid }}}, nil
}

func oauthSecret(s string) bool {
	if len(s) == 0 || len(s) > 8192 {
		return false
	}
	for _, c := range s {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func oauthScopes(s string) (map[string]bool, bool) {
	if len(s) == 0 || len(s) > 8192 || strings.Join(strings.Fields(s), " ") != s {
		return nil, false
	}
	set := map[string]bool{}
	for _, scope := range strings.Split(s, " ") {
		if !oauthSecret(scope) || set[scope] {
			return nil, false
		}
		set[scope] = true
	}
	return set, true
}

func (p *oauthProvider) exchange(ctx context.Context, code, redirect, verifier, scope string) (oauthTokens, error) {
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || !oauthSecret(code) || len(verifier) < 43 || len(verifier) > 128 {
		return oauthTokens{}, ErrOAuthInvalid
	}
	for _, c := range verifier {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return oauthTokens{}, ErrOAuthInvalid
		}
	}
	return p.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "scope": {scope}})
}

func (p *oauthProvider) refresh(ctx context.Context, refresh, scope string) (oauthTokens, error) {
	if !oauthSecret(refresh) {
		return oauthTokens{}, ErrOAuthInvalid
	}
	return p.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "scope": {scope}})
}

func (p *oauthProvider) token(ctx context.Context, form url.Values) (oauthTokens, error) {
	wanted, ok := oauthScopes(form.Get("scope"))
	if !ok {
		return oauthTokens{}, ErrOAuthInvalid
	}
	started := p.now()
	body, status, err := p.post(ctx, p.tokenURL, form)
	if err != nil {
		return oauthTokens{}, err
	}
	var response struct {
		Code           *int   `json:"code"`
		Access         string `json:"access_token"`
		Refresh        string `json:"refresh_token"`
		Expires        int64  `json:"expires_in"`
		RefreshExpires int64  `json:"refresh_token_expires_in"`
		Type           string `json:"token_type"`
		Scope          string `json:"scope"`
		Error          string `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || response.Code == nil {
		return oauthTokens{}, ErrOAuthUncertain
	}
	if status != http.StatusOK || *response.Code != 0 || response.Error != "" {
		cause := ErrOAuthRejected
		switch *response.Code {
		case 20004, 20037:
			cause = ErrOAuthExpired
		case 20064:
			cause = ErrOAuthRevoked
		}
		if status >= 500 {
			cause = ErrOAuthUncertain
		}
		return oauthTokens{}, &OAuthError{status, *response.Code, cause}
	}
	actual, ok := oauthScopes(response.Scope)
	if !ok || !oauthSecret(response.Access) || response.Type != "Bearer" || response.Expires <= 0 || response.Expires > int64((1<<63-1)/time.Second) {
		return oauthTokens{}, ErrOAuthUncertain
	}
	for scope := range actual {
		if !wanted[scope] {
			return oauthTokens{}, ErrOAuthUncertain
		}
	}
	result := oauthTokens{Access: response.Access, Scope: response.Scope, ExpiresAt: started.Add(time.Duration(response.Expires) * time.Second)}
	if response.Refresh != "" {
		if !actual["offline_access"] || !oauthSecret(response.Refresh) || response.RefreshExpires <= 0 || response.RefreshExpires > int64((1<<63-1)/time.Second) {
			return oauthTokens{}, ErrOAuthUncertain
		}
		result.Refresh = response.Refresh
		result.RefreshExpiresAt = started.Add(time.Duration(response.RefreshExpires) * time.Second)
	} else if response.RefreshExpires != 0 {
		return oauthTokens{}, ErrOAuthUncertain
	}
	if !p.now().Before(result.ExpiresAt) {
		return oauthTokens{}, ErrOAuthExpired
	}
	return result, nil
}

// Both tokens must be revoked: the provider only promises to disable refresh
// capability when revoking a refresh token, not every issued access token.
func (p *oauthProvider) revoke(ctx context.Context, token, kind string) error {
	if !oauthSecret(token) || kind != "access_token" && kind != "refresh_token" {
		return ErrOAuthInvalid
	}
	body, status, err := p.post(ctx, p.revokeURL, url.Values{"token": {token}, "token_type_hint": {kind}})
	if err != nil {
		return err
	}
	if status == http.StatusOK && len(body) == 0 {
		return nil
	}
	var response struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(body, &response) != nil {
		return ErrOAuthUncertain
	}
	return &OAuthError{status, response.Code, ErrOAuthUncertain}
}

func (p *oauthProvider) post(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	form.Set("client_id", p.appID)
	form.Set("client_secret", p.secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, ErrOAuthInvalid
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, errors.Join(ErrOAuthUncertain, ctx.Err())
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	err = errors.Join(readErr, resp.Body.Close())
	if err != nil || len(body) > 64<<10 {
		return nil, resp.StatusCode, ErrOAuthUncertain
	}
	return body, resp.StatusCode, nil
}

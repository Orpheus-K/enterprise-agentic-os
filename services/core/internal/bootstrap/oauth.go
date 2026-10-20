package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/feishudocs"
	"agentos.local/core/internal/identity"
)

type oauthRequest struct {
	Action         string `json:"action"`
	OpenID         string `json:"open_id"`
	RedirectURI    string `json:"redirect_uri,omitempty"`
	Scope          string `json:"scope,omitempty"`
	State          string `json:"state,omitempty"`
	Code           string `json:"code,omitempty"`
	Revision       int64  `json:"revision,omitempty"`
	EvidenceSHA256 string `json:"evidence_sha256,omitempty"`
}

func (oauthRequest) String() string   { return "[OAuth request redacted]" }
func (oauthRequest) GoString() string { return "[OAuth request redacted]" }

func readOAuthRequest(input io.Reader) (oauthRequest, error) {
	var request oauthRequest
	raw, err := io.ReadAll(io.LimitReader(input, (32<<10)+1))
	if err != nil || len(raw) > 32<<10 {
		return request, feishudocs.ErrOAuthInvalid
	}
	defer clear(raw)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || !sourceIdentifier.MatchString(request.OpenID) {
		return oauthRequest{}, feishudocs.ErrOAuthInvalid
	}
	grantFields := request.RedirectURI != "" || request.Scope != ""
	callbackFields := request.State != "" || request.Code != ""
	reconcileFields := request.Revision != 0 || request.EvidenceSHA256 != ""
	switch request.Action {
	case "begin":
		if request.RedirectURI == "" || request.Scope == "" || callbackFields || reconcileFields {
			return oauthRequest{}, feishudocs.ErrOAuthInvalid
		}
	case "complete":
		if request.State == "" || request.Code == "" || grantFields || reconcileFields {
			return oauthRequest{}, feishudocs.ErrOAuthInvalid
		}
	case "status", "revoke":
		if grantFields || callbackFields || reconcileFields {
			return oauthRequest{}, feishudocs.ErrOAuthInvalid
		}
	case "confirm-provider-revoked":
		if request.Revision < 1 || len(request.EvidenceSHA256) != 64 || grantFields || callbackFields {
			return oauthRequest{}, feishudocs.ErrOAuthInvalid
		}
	default:
		return oauthRequest{}, feishudocs.ErrOAuthInvalid
	}
	return request, nil
}

type oauthAdministration interface {
	Begin(context.Context, string, string) (string, error)
	Complete(context.Context, string, string) error
	Inspect(context.Context) (feishudocs.OAuthStatus, error)
	Revoke(context.Context) error
	ConfirmProviderRevoked(context.Context, int64, string) error
}

func executeOAuth(ctx context.Context, request oauthRequest, client oauthAdministration, output io.Writer) error {
	var result any = struct {
		OK bool `json:"ok"`
	}{true}
	var err error
	switch request.Action {
	case "begin":
		var address string
		address, err = client.Begin(ctx, request.RedirectURI, request.Scope)
		result = struct {
			URL string `json:"authorization_url"`
		}{address}
	case "complete":
		err = client.Complete(ctx, request.State, request.Code)
	case "status":
		result, err = client.Inspect(ctx)
	case "revoke":
		err = client.Revoke(ctx)
	case "confirm-provider-revoked":
		err = client.ConfirmProviderRevoked(ctx, request.Revision, request.EvidenceSHA256)
	default:
		return feishudocs.ErrOAuthInvalid
	}
	if err != nil {
		return safeOAuthError(err)
	}
	if json.NewEncoder(output).Encode(result) != nil {
		return errors.New("cannot write OAuth result; inspect persisted state before retrying")
	}
	return nil
}

func safeOAuthError(err error) error {
	for _, kind := range []error{feishudocs.ErrOAuthUncertain, feishudocs.ErrOAuthExpired, feishudocs.ErrOAuthRevoked, feishudocs.ErrOAuthConflict, feishudocs.ErrOAuthMissing, feishudocs.ErrOAuthInvalid, feishudocs.ErrOAuthSecrets, feishudocs.ErrOAuthRejected, feishudocs.ErrDenied, feishudocs.ErrRemote, identity.ErrUnauthenticated, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, kind) {
			return kind
		}
	}
	return errors.New("OAuth administration failed; inspect persisted state before retrying")
}

// RunOAuth is a local administrator command, not an HTTP handler. Possession of
// the trusted config, runtime DB credentials and encryption key authorizes this
// delegated action; current IAM for the configured owner is still mandatory.
// Codes/state are read from stdin so they never enter process arguments or logs.
func RunOAuth(ctx context.Context, path string, input io.Reader, output io.Writer) error {
	request, err := readOAuthRequest(input)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("cannot read channel configuration")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > 1<<20 {
		return errors.New("cannot read bounded channel configuration")
	}
	c, err := Parse(raw)
	if err != nil {
		return err
	}
	if c.OAuth == nil || c.StorageDriver == "sqlite" {
		return errors.New("OAuth administration requires configured PostgreSQL sources")
	}
	configured := false
	for _, source := range c.Sources {
		for _, user := range append([]SourceUser{source.Owner}, source.Recipients...) {
			if user.OpenID == request.OpenID && user.UseOAuth {
				configured = true
			}
		}
	}
	if !configured {
		return errors.New("OAuth user is not configured for this installation")
	}
	keys, err := c.loadOAuthKeys()
	if err != nil {
		return err
	}
	appSecret, err := secret(c.AppSecretEnv)
	if err != nil {
		return err
	}
	databaseURL, err := secret(c.DatabaseURLEnv)
	if err != nil {
		return err
	}
	db, err := database.Open(ctx, databaseURL, 2)
	if err != nil {
		return safeOAuthError(err)
	}
	defer db.Close()
	client, err := feishudocs.NewOAuth(db, c.Binding.TenantID, feishudocs.User{AppID: c.Binding.AppID, TenantKey: c.Binding.TenantKey, OpenID: request.OpenID}, appSecret, keys)
	if err != nil {
		return safeOAuthError(err)
	}
	return executeOAuth(ctx, request, client, output)
}

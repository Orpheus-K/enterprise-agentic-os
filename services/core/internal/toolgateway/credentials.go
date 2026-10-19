package toolgateway

import (
	"agentos.local/core/internal/identity"
	"context"
	"errors"
	"os"
	"strings"
)

var ErrCredential = errors.New("target credential unavailable")

// A binding is an administrator-approved delegation, not an incoming user token.
type CredentialBinding struct {
	Principal identity.Principal `json:"principal"`
	Endpoint  string             `json:"endpoint"`
	TokenEnv  string             `json:"token_env"`
}
type credentialKey struct {
	principal identity.Principal
	endpoint  string
}
type Credentials struct{ refs map[credentialKey]string }

func NewCredentials(bindings []CredentialBinding) (*Credentials, error) {
	refs := map[credentialKey]string{}
	for _, b := range bindings {
		if b.Principal.Validate() != nil || b.Endpoint == "" || b.TokenEnv == "" || strings.TrimSpace(b.TokenEnv) != b.TokenEnv {
			return nil, ErrCredential
		}
		key := credentialKey{b.Principal, b.Endpoint}
		if _, ok := refs[key]; ok {
			return nil, ErrCredential
		}
		refs[key] = b.TokenEnv
	}
	return &Credentials{refs}, nil
}
func (c *Credentials) token(ctx context.Context, p identity.Principal, endpoint string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ref, ok := c.refs[credentialKey{p, endpoint}]
	if !ok {
		return "", ErrCredential
	}
	token := os.Getenv(ref)
	if len(token) < 16 || strings.ContainsAny(token, " \t\r\n") {
		return "", ErrCredential
	}
	return token, nil
}

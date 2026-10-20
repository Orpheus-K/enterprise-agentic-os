package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"agentos.local/core/internal/feishudocs"
)

type oauthAdminFake struct {
	action string
	fail   error
}

func (f *oauthAdminFake) Begin(context.Context, string, string) (string, error) {
	f.action = "begin"
	return "https://example.test/authorize", f.fail
}
func (f *oauthAdminFake) Complete(context.Context, string, string) error {
	f.action = "complete"
	return f.fail
}
func (f *oauthAdminFake) Inspect(context.Context) (feishudocs.OAuthStatus, error) {
	f.action = "status"
	return feishudocs.OAuthStatus{Revision: 7, Stage: "revoked", RemoteUnknown: true}, f.fail
}
func (f *oauthAdminFake) Revoke(context.Context) error { f.action = "revoke"; return f.fail }
func (f *oauthAdminFake) ConfirmProviderRevoked(context.Context, int64, string) error {
	f.action = "confirm-provider-revoked"
	return f.fail
}

func TestOAuthCommandInputAndDispatch(t *testing.T) {
	for _, raw := range []string{
		`{"action":"begin","open_id":"ou_test","redirect_uri":"https://example.test/callback","scope":"offline_access"}`,
		`{"action":"complete","open_id":"ou_test","state":"private-state","code":"private-code"}`,
		`{"action":"status","open_id":"ou_test"}`,
		`{"action":"revoke","open_id":"ou_test"}`,
		`{"action":"confirm-provider-revoked","open_id":"ou_test","revision":7,"evidence_sha256":"` + strings.Repeat("a", 64) + `"}`,
	} {
		request, err := readOAuthRequest(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		fake := &oauthAdminFake{}
		var output bytes.Buffer
		if err := executeOAuth(t.Context(), request, fake, &output); err != nil || fake.action != request.Action {
			t.Fatal("dispatch failed", err)
		}
		if strings.Contains(output.String(), "private-") || strings.Contains(fmt.Sprintf("%+v", request), "private-") {
			t.Fatal("command leaked callback secrets")
		}
		fake.fail = errors.New("database failure containing private-code")
		output.Reset()
		err = executeOAuth(t.Context(), request, fake, &output)
		if err == nil || strings.Contains(err.Error(), "private-code") || output.Len() != 0 {
			t.Fatal("failure leaked secret or success output", err)
		}
	}
}

func TestOAuthCommandRejectsAmbiguousOrExcessInput(t *testing.T) {
	for _, raw := range []string{
		`{`, `{ "action":"unknown","open_id":"ou_test" }`,
		`{"action":"revoke","open_id":"ou_test","code":"secret"}`,
		`{"action":"begin","open_id":"ou_test","scope":"offline_access"}`,
		`{"action":"complete","open_id":"ou_test","state":"secret"}`,
		`{"action":"confirm-provider-revoked","open_id":"ou_test","revision":0,"evidence_sha256":"x"}`,
		`{"action":"status","open_id":"ou_test","access_token":"secret"}`,
		`{"action":"status","open_id":"ou_test"}{}`,
		strings.Repeat(" ", 32<<10) + `{}`,
	} {
		if _, err := readOAuthRequest(strings.NewReader(raw)); !errors.Is(err, feishudocs.ErrOAuthInvalid) {
			t.Fatal("invalid command accepted", err)
		}
	}
}

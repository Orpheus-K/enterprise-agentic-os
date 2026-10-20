package feishudocs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func oauthTestKeys(t *testing.T, active string) *OAuthKeys {
	t.Helper()
	keys, err := NewOAuthKeys(active, map[string][]byte{"old": bytes.Repeat([]byte{1}, 32), "new": bytes.Repeat([]byte{2}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func oauthTestBinding() oauthSecretBinding {
	return oauthSecretBinding{Tenant: uuid.MustParse("00000000-0000-0000-0000-000000000001"), User: User{AppID: "cli_test", OpenID: "ou_test", TenantKey: "tenant_test"}, Revision: 1}
}

func TestOAuthSecretsRoundTripAndRotation(t *testing.T) {
	binding := oauthTestBinding()
	oldKeys := oauthTestKeys(t, "old")
	plain := []byte(`{"access":"access-secret","refresh":"refresh-secret"}`)
	first, err := oldKeys.seal(binding, plain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := oldKeys.seal(binding, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("reused nonce")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("access-secret")) || bytes.Contains(encoded, []byte("refresh-secret")) {
		t.Fatal("plaintext stored")
	}
	rotated := oauthTestKeys(t, "new")
	got, err := rotated.open(binding, first)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("old key cannot decrypt: %v", err)
	}
	binding.Revision++
	updated, err := rotated.seal(binding, got)
	if err != nil || updated.KeyID != "new" {
		t.Fatalf("new writes did not rotate: %v", err)
	}
	newOnly, err := NewOAuthKeys("new", map[string][]byte{"new": bytes.Repeat([]byte{2}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	got, err = newOnly.open(binding, updated)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("rotated ciphertext: %v", err)
	}
	if _, err = newOnly.open(oauthTestBinding(), first); !errors.Is(err, ErrOAuthSecrets) {
		t.Fatal("missing key accepted")
	}
	for _, v := range []any{oldKeys, first} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if bytes.Contains([]byte(fmt.Sprintf(format, v)), []byte("secret")) {
				t.Fatal("logging leaked secrets")
			}
		}
	}
}

func TestOAuthSecretsBindingAndTampering(t *testing.T) {
	keys := oauthTestKeys(t, "old")
	binding := oauthTestBinding()
	envelope, err := keys.seal(binding, []byte("sensitive-value"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		modify func(*oauthSecretBinding, *oauthEnvelope)
	}{
		{"tenant", func(b *oauthSecretBinding, _ *oauthEnvelope) { b.Tenant = uuid.New() }},
		{"app", func(b *oauthSecretBinding, _ *oauthEnvelope) { b.User.AppID = "cli_other" }},
		{"provider-tenant", func(b *oauthSecretBinding, _ *oauthEnvelope) { b.User.TenantKey = "other" }},
		{"user", func(b *oauthSecretBinding, _ *oauthEnvelope) { b.User.OpenID = "ou_other" }},
		{"revision", func(b *oauthSecretBinding, _ *oauthEnvelope) { b.Revision++ }},
		{"key-id", func(_ *oauthSecretBinding, e *oauthEnvelope) { e.KeyID = "new" }},
		{"nonce", func(_ *oauthSecretBinding, e *oauthEnvelope) { e.Ciphertext[0] ^= 1 }},
		{"payload", func(_ *oauthSecretBinding, e *oauthEnvelope) { e.Ciphertext[len(e.Ciphertext)-1] ^= 1 }},
		{"truncated", func(_ *oauthSecretBinding, e *oauthEnvelope) { e.Ciphertext = e.Ciphertext[:20] }},
		{"oversize", func(_ *oauthSecretBinding, e *oauthEnvelope) { e.Ciphertext = make([]byte, maxOAuthSecrets+100) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := binding
			e := oauthEnvelope{KeyID: envelope.KeyID, Ciphertext: bytes.Clone(envelope.Ciphertext)}
			tc.modify(&b, &e)
			got, err := keys.open(b, e)
			if !errors.Is(err, ErrOAuthSecrets) || len(got) != 0 {
				t.Fatal("invalid credential returned plaintext")
			}
		})
	}
}

func TestOAuthSecretsInputsAreOwnedAndBounded(t *testing.T) {
	material := map[string][]byte{"key": bytes.Repeat([]byte{3}, 32)}
	keys, err := NewOAuthKeys("key", material)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keys.seal(oauthTestBinding(), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	clear(material["key"])
	delete(material, "key")
	if got, err := keys.open(oauthTestBinding(), envelope); err != nil || string(got) != "secret" {
		t.Fatalf("retained mutable input: %v", err)
	}
	for _, plain := range [][]byte{nil, {}, make([]byte, maxOAuthSecrets+1)} {
		if _, err := keys.seal(oauthTestBinding(), plain); !errors.Is(err, ErrOAuthSecrets) {
			t.Fatal("invalid size accepted")
		}
	}
	for _, size := range []int{0, 16, 24, 31, 33} {
		if _, err := NewOAuthKeys("key", map[string][]byte{"key": make([]byte, size)}); !errors.Is(err, ErrOAuthSecrets) {
			t.Fatal("non AES256 key accepted")
		}
	}
	if _, err := NewOAuthKeys("missing", map[string][]byte{"key": make([]byte, 32)}); !errors.Is(err, ErrOAuthSecrets) {
		t.Fatal("missing active key accepted")
	}
}

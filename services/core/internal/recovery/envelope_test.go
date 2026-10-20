package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type testKeys struct {
	key          [32]byte
	checks       int
	failAt       int
	resolveError bool
}

func (k *testKeys) Check(context.Context, []KeyReference) error {
	k.checks++
	if k.failAt != 0 && k.checks >= k.failAt {
		return errors.New("secret-value-must-not-leak")
	}
	return nil
}

func (k *testKeys) WrappingKey(context.Context, KeyReference) ([32]byte, error) {
	if k.resolveError {
		return [32]byte{}, errors.New("secret-value-must-not-leak")
	}
	return k.key, nil
}

func fixture(driver string) (Manifest, []byte, *testKeys) {
	return Manifest{
		Version:             Version{Driver: driver, Schema: 8, ArtifactSHA256: Digest([]byte("binary"))},
		AuthorizationSHA256: Digest([]byte("current authorization facts")),
		WrappingKey:         KeyReference{Binding: "backup-key", ID: "vault/backup", Version: "v1"},
		Secrets:             []KeyReference{{Binding: "APP_SECRET", ID: "vault/feishu", Version: "v3"}},
		Database:            []Artifact{{Name: "database.dump", SHA256: Digest([]byte("database bytes")), Bytes: 14}},
	}, []byte(`{"app_secret_env":"APP_SECRET","sensitive_configuration":"private-value"}`), &testKeys{key: [32]byte{1, 2, 3}}
}

func TestEnvelopeRoundTripAndNoPlaintext(t *testing.T) {
	for _, driver := range []string{"postgres", "sqlite"} {
		t.Run(driver, func(t *testing.T) {
			m, configuration, keys := fixture(driver)
			envelope, err := Seal(t.Context(), m, configuration, keys)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte("private-value")) || bytes.Contains(encoded, keys.key[:]) {
				t.Fatal("plaintext in envelope")
			}
			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			authority := func(context.Context) (string, error) { return m.AuthorizationSHA256, nil }
			actual, err := Open(t.Context(), decoded, m.Version, authority, keys)
			if err != nil || !bytes.Equal(actual, configuration) {
				t.Fatalf("roundtrip: %v", err)
			}
			second, err := Seal(t.Context(), m, configuration, keys)
			if err != nil || bytes.Equal(second.Nonce, envelope.Nonce) || bytes.Equal(second.Ciphertext, envelope.Ciphertext) {
				t.Fatal("nonce reused")
			}
			// Retained old version decrypts; replacing the wrapping key under that
			// version is an integrity failure, not a silent key rotation.
			keys.key[0]++
			if out, err := Open(t.Context(), decoded, m.Version, authority, keys); !errors.Is(err, ErrIntegrity) || out != nil {
				t.Fatalf("wrong key: %v", err)
			}
		})
	}
}

func TestEnvelopeRejectsTamperedEvidence(t *testing.T) {
	for _, scenario := range []string{"artifact", "schema", "driver", "binary", "config", "secret", "wrapping", "authorization", "nonce", "ciphertext", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			m, configuration, keys := fixture("sqlite")
			envelope, err := Seal(t.Context(), m, configuration, keys)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "artifact":
				envelope.Manifest.Database[0].SHA256 = Digest([]byte("replacement"))
			case "schema":
				envelope.Manifest.Version.Schema++
			case "driver":
				envelope.Manifest.Version.Driver = "postgres"
			case "binary":
				envelope.Manifest.Version.ArtifactSHA256 = Digest([]byte("new binary"))
			case "config":
				envelope.Manifest.ConfigurationSHA256 = Digest([]byte("different config"))
			case "secret":
				envelope.Manifest.Secrets[0].Version = "v4"
			case "wrapping":
				envelope.Manifest.WrappingKey.Version = "v2"
			case "authorization":
				envelope.Manifest.AuthorizationSHA256 = Digest([]byte("later revocation"))
			case "nonce":
				envelope.Nonce[0]++
			case "ciphertext":
				envelope.Ciphertext[0]++
			case "truncated":
				envelope.Ciphertext = nil
			}
			// Even accepting attacker-edited metadata as expected state cannot
			// authenticate it: all fields are cryptographically bound.
			authority := func(context.Context) (string, error) { return envelope.Manifest.AuthorizationSHA256, nil }
			out, err := Open(t.Context(), envelope, envelope.Manifest.Version, authority, keys)
			if out != nil || err == nil {
				t.Fatal("tampered evidence accepted")
			}
		})
	}
}

func TestEnvelopeCurrentFactsAndKeysFailClosed(t *testing.T) {
	for _, scenario := range []string{"missing-authority", "revoked", "revoked-during-open", "authority-error", "version", "key-revoked", "key-revoked-during-open", "key-error", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			m, configuration, keys := fixture("postgres")
			envelope, err := Seal(t.Context(), m, configuration, keys)
			if err != nil {
				t.Fatal(err)
			}
			keys.checks = 0
			calls := 0
			authority := Authority(func(context.Context) (string, error) {
				calls++
				if scenario == "authority-error" {
					return "", errors.New("secret-value-must-not-leak")
				}
				if scenario == "revoked" || (scenario == "revoked-during-open" && calls > 1) {
					return Digest([]byte("current revoked")), nil
				}
				return m.AuthorizationSHA256, nil
			})
			ctx := t.Context()
			switch scenario {
			case "missing-authority":
				authority = nil
			case "version":
				m.Version.Schema++
			case "key-revoked":
				keys.failAt = 1
			case "key-revoked-during-open":
				keys.failAt = 2
			case "key-error":
				keys.resolveError = true
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			out, err := Open(ctx, envelope, m.Version, authority, keys)
			if out != nil || err == nil {
				t.Fatal("unverified configuration returned")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("provider error leaked")
			}
		})
	}
}

func TestEnvelopeRejectsInvalidInputs(t *testing.T) {
	m, configuration, keys := fixture("sqlite")
	for _, input := range [][]byte{nil, []byte("not json"), bytes.Repeat([]byte(" "), MaxConfiguration+1)} {
		if _, err := Seal(t.Context(), m, input, keys); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid config: %v", err)
		}
	}
	for _, scenario := range []string{"duplicate-key", "floating-key", "path", "duplicate-file", "hash", "empty-file"} {
		t.Run(scenario, func(t *testing.T) {
			m, configuration, keys := fixture("sqlite")
			switch scenario {
			case "duplicate-key":
				m.Secrets = append(m.Secrets, m.Secrets[0])
			case "floating-key":
				m.Secrets[0].Version = "latest"
			case "path":
				m.Database[0].Name = "../escaped"
			case "duplicate-file":
				m.Database = append(m.Database, m.Database[0])
			case "hash":
				m.Database[0].SHA256 = "bad"
			case "empty-file":
				m.Database[0].Bytes = 0
			}
			if _, err := Seal(t.Context(), m, configuration, keys); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid metadata: %v", err)
			}
		})
	}
	envelope, err := Seal(t.Context(), m, configuration, keys)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(envelope)
	for _, raw := range [][]byte{append(encoded, []byte(" {}")...), append([]byte(`{"unknown":1,`), encoded[1:]...), bytes.Repeat([]byte(" "), 2*MaxConfiguration+1)} {
		if _, err := Decode(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid decode: %v", err)
		}
	}
}

package feishudocs

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

var ErrOAuthSecrets = errors.New("OAuth credential encryption or integrity check failed")

const maxOAuthSecrets = 64 << 10

// OAuthKeys holds only expanded ciphers. The caller supplies external key
// material; neither application configuration nor the credential ledger stores
// the key. Retained old keys can decrypt existing rows; writes use the active ID.
type OAuthKeys struct {
	active string
	keys   map[string]cipher.AEAD
}

func (OAuthKeys) String() string   { return "[OAuth encryption keys redacted]" }
func (OAuthKeys) GoString() string { return "[OAuth encryption keys redacted]" }

func NewOAuthKeys(active string, material map[string][]byte) (*OAuthKeys, error) {
	if !identifier(active) || len(material) == 0 || len(material) > 16 {
		return nil, ErrOAuthSecrets
	}
	keys := make(map[string]cipher.AEAD, len(material))
	for id, key := range material {
		if !identifier(id) || len(key) != 32 {
			return nil, ErrOAuthSecrets
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, ErrOAuthSecrets
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, ErrOAuthSecrets
		}
		keys[id] = aead
	}
	if keys[active] == nil {
		return nil, ErrOAuthSecrets
	}
	return &OAuthKeys{active: active, keys: keys}, nil
}

// oauthSecretBinding is authenticated with the ciphertext. Revision is the
// durable ledger generation, not a provider token version. The ledger must
// independently enforce monotonic CAS; AEAD alone cannot prevent DB rollback.
type oauthSecretBinding struct {
	Tenant   uuid.UUID
	User     User
	Revision int64
}

type oauthEnvelope struct {
	KeyID      string
	Ciphertext []byte
}

func (oauthEnvelope) String() string   { return "[OAuth encrypted credential]" }
func (oauthEnvelope) GoString() string { return "[OAuth encrypted credential]" }

func (b oauthSecretBinding) associatedData(keyID string) ([]byte, error) {
	if b.Tenant == uuid.Nil || b.Revision < 1 {
		return nil, ErrOAuthSecrets
	}
	if _, err := NewCredential(b.User, "binding-validation"); err != nil {
		return nil, ErrOAuthSecrets
	}
	data, err := json.Marshal(struct {
		Domain  string
		Version int
		KeyID   string
		Binding oauthSecretBinding
	}{"agentos.feishu.oauth", 1, keyID, b})
	if err != nil {
		return nil, ErrOAuthSecrets
	}
	return data, nil
}

func (k *OAuthKeys) seal(binding oauthSecretBinding, plain []byte) (oauthEnvelope, error) {
	if k == nil || len(plain) == 0 || len(plain) > maxOAuthSecrets {
		return oauthEnvelope{}, ErrOAuthSecrets
	}
	data, err := binding.associatedData(k.active)
	if err != nil {
		return oauthEnvelope{}, err
	}
	aead := k.keys[k.active]
	if aead == nil {
		return oauthEnvelope{}, ErrOAuthSecrets
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return oauthEnvelope{}, ErrOAuthSecrets
	}
	return oauthEnvelope{KeyID: k.active, Ciphertext: aead.Seal(nonce, nonce, plain, data)}, nil
}

func (k *OAuthKeys) open(binding oauthSecretBinding, envelope oauthEnvelope) ([]byte, error) {
	if k == nil {
		return nil, ErrOAuthSecrets
	}
	aead := k.keys[envelope.KeyID]
	if aead == nil {
		return nil, ErrOAuthSecrets
	}
	minimum := aead.NonceSize() + aead.Overhead()
	if len(envelope.Ciphertext) <= minimum || len(envelope.Ciphertext) > minimum+maxOAuthSecrets {
		return nil, ErrOAuthSecrets
	}
	data, err := binding.associatedData(envelope.KeyID)
	if err != nil {
		return nil, err
	}
	nonce := envelope.Ciphertext[:aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, envelope.Ciphertext[aead.NonceSize():], data)
	if err != nil {
		return nil, ErrOAuthSecrets
	}
	return plain, nil
}

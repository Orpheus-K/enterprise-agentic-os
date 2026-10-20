// Package recovery binds database artifacts, encrypted configuration and external
// key references. A manifest alone is never evidence of current authorization.
package recovery

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
)

const MaxConfiguration = 1024 * 1024

type Version struct {
	Driver         string `json:"driver"`
	Schema         int    `json:"schema"`
	ArtifactSHA256 string `json:"artifact_sha256"`
}

// KeyReference identifies an immutable version in an independently restored key
// store. Binding names the configuration's secret reference, never its value.
type KeyReference struct {
	Binding string `json:"binding"`
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Manifest struct {
	Format              int            `json:"format"`
	Version             Version        `json:"version"`
	ConfigurationSHA256 string         `json:"configuration_sha256"`
	AuthorizationSHA256 string         `json:"authorization_sha256"`
	WrappingKey         KeyReference   `json:"wrapping_key"`
	Secrets             []KeyReference `json:"secret_references"`
	Database            []Artifact     `json:"database"`
}

type Envelope struct {
	Manifest   Manifest `json:"manifest"`
	Nonce      []byte   `json:"nonce"`
	Ciphertext []byte   `json:"configuration_ciphertext"`
}

// KeyStore must reject unavailable/revoked versions, not resolve aliases such as
// "latest". Check verifies every site secret without exporting its plaintext.
type KeyStore interface {
	Check(context.Context, []KeyReference) error
	WrappingKey(context.Context, KeyReference) ([32]byte, error)
}

// Authority reads an independent current source, never the backup being restored.
// The operator must freeze authorization changes for the joint restore window.
type Authority func(context.Context) (string, error)

var (
	ErrInvalid    = errors.New("invalid joint backup envelope")
	ErrVersion    = errors.New("joint restore version mismatch")
	ErrAuthority  = errors.New("current authorization facts unavailable or changed")
	ErrKeys       = errors.New("required external key version unavailable or revoked")
	ErrIntegrity  = errors.New("joint backup authentication failed")
	referencePart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	artifactName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func validReference(ref KeyReference) bool {
	return referencePart.MatchString(ref.Binding) && referencePart.MatchString(ref.ID) &&
		referencePart.MatchString(ref.Version) && ref.Version != "latest" && ref.Version != "current"
}

func (m Manifest) validate() error {
	if m.Format != 1 || (m.Version.Driver != "postgres" && m.Version.Driver != "sqlite") ||
		m.Version.Schema < 1 || !validDigest(m.Version.ArtifactSHA256) ||
		!validDigest(m.ConfigurationSHA256) || !validDigest(m.AuthorizationSHA256) ||
		!validReference(m.WrappingKey) || len(m.Secrets) > 128 || len(m.Database) == 0 || len(m.Database) > 16 {
		return ErrInvalid
	}
	bindings := map[string]bool{m.WrappingKey.Binding: true}
	for _, ref := range m.Secrets {
		if !validReference(ref) || bindings[ref.Binding] {
			return ErrInvalid
		}
		bindings[ref.Binding] = true
	}
	names := map[string]bool{}
	for _, file := range m.Database {
		if !artifactName.MatchString(file.Name) || file.Name == "." || file.Name == ".." ||
			!validDigest(file.SHA256) || file.Bytes < 1 || names[file.Name] {
			return ErrInvalid
		}
		names[file.Name] = true
	}
	return nil
}

func cloneManifest(m Manifest) Manifest {
	m.Secrets = slices.Clone(m.Secrets)
	m.Database = slices.Clone(m.Database)
	return m
}

func authenticatedData(m Manifest) ([]byte, error) {
	encoded, err := json.Marshal(m)
	return append([]byte("agentos-joint-backup-v1\x00"), encoded...), err
}

func cipherFor(ctx context.Context, store KeyStore, manifest Manifest) (cipher.AEAD, error) {
	if store == nil {
		return nil, ErrKeys
	}
	refs := append(slices.Clone(manifest.Secrets), manifest.WrappingKey)
	if err := store.Check(ctx, refs); err != nil {
		return nil, ErrKeys
	}
	key, err := store.WrappingKey(ctx, manifest.WrappingKey)
	defer clear(key[:])
	if err != nil {
		return nil, ErrKeys
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts configuration and authenticates every manifest field, including
// database hashes. Database artifact creation and hashing are the driver's job.
func Seal(ctx context.Context, manifest Manifest, configuration []byte, store KeyStore) (Envelope, error) {
	if err := ctx.Err(); err != nil {
		return Envelope{}, err
	}
	if len(configuration) == 0 || len(configuration) > MaxConfiguration || !json.Valid(configuration) {
		return Envelope{}, ErrInvalid
	}
	manifest = cloneManifest(manifest)
	manifest.Format = 1
	manifest.ConfigurationSHA256 = Digest(configuration)
	if err := manifest.validate(); err != nil {
		return Envelope{}, err
	}
	aead, err := cipherFor(ctx, store, manifest)
	if err != nil {
		return Envelope{}, err
	}
	aad, err := authenticatedData(manifest)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return Envelope{}, err
	}
	ciphertext := aead.Seal(nil, nonce, configuration, aad)
	if err = ctx.Err(); err != nil {
		return Envelope{}, err
	}
	return Envelope{Manifest: manifest, Nonce: nonce, Ciphertext: ciphertext}, nil
}

func checkAuthority(ctx context.Context, authority Authority, expected string) error {
	if authority == nil {
		return ErrAuthority
	}
	current, err := authority(ctx)
	if err != nil || !validDigest(current) || current != expected {
		return ErrAuthority
	}
	return nil
}

// Open checks the trusted target version and current authorization before
// releasing configuration. Callers still verify database bytes and repeat the
// authority check after restoration, before making any restored output usable.
func Open(ctx context.Context, envelope Envelope, expected Version, authority Authority, store KeyStore) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifest := cloneManifest(envelope.Manifest)
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if manifest.Version != expected {
		return nil, ErrVersion
	}
	if err := checkAuthority(ctx, authority, manifest.AuthorizationSHA256); err != nil {
		return nil, err
	}
	aead, err := cipherFor(ctx, store, manifest)
	if err != nil {
		return nil, err
	}
	if len(envelope.Nonce) != aead.NonceSize() || len(envelope.Ciphertext) < aead.Overhead() ||
		len(envelope.Ciphertext) > MaxConfiguration+aead.Overhead() {
		return nil, ErrInvalid
	}
	aad, err := authenticatedData(manifest)
	if err != nil {
		return nil, err
	}
	configuration, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, aad)
	if err != nil {
		return nil, ErrIntegrity
	}
	if Digest(configuration) != manifest.ConfigurationSHA256 || !json.Valid(configuration) {
		clear(configuration)
		return nil, ErrIntegrity
	}
	if err = checkAuthority(ctx, authority, manifest.AuthorizationSHA256); err != nil {
		clear(configuration)
		return nil, err
	}
	if err = store.Check(ctx, append(slices.Clone(manifest.Secrets), manifest.WrappingKey)); err != nil {
		clear(configuration)
		return nil, ErrKeys
	}
	if err = ctx.Err(); err != nil {
		clear(configuration)
		return nil, err
	}
	return configuration, nil
}

// Decode rejects oversized, unknown and trailing fields before cryptographic use.
func Decode(data []byte) (Envelope, error) {
	if len(data) > 2*MaxConfiguration {
		return Envelope{}, ErrInvalid
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Envelope{}, ErrInvalid
	}
	return envelope, nil
}

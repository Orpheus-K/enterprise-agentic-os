package recovery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// EnvironmentKeys is a local administrator adapter. The external catalog holds
// only immutable IDs/versions and enabled state; secret values remain in the
// process environment. The catalog is reread on every check, never cached.
type EnvironmentKeys struct {
	CatalogPath string
	Getenv      func(string) string
}

type CatalogEntry struct {
	KeyReference
	Enabled bool `json:"enabled"`
}

type KeyCatalog struct {
	Format int            `json:"format"`
	Keys   []CatalogEntry `json:"keys"`
}

var environmentBinding = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func (s EnvironmentKeys) catalog() (map[string]CatalogEntry, error) {
	if !filepath.IsAbs(s.CatalogPath) || s.Getenv == nil {
		return nil, ErrKeys
	}
	info, err := os.Lstat(s.CatalogPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024 {
		return nil, ErrKeys
	}
	file, err := os.Open(s.CatalogPath)
	if err != nil {
		return nil, ErrKeys
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 128*1024+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > 128*1024 {
		return nil, ErrKeys
	}
	var catalog KeyCatalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF || catalog.Format != 1 || len(catalog.Keys) == 0 || len(catalog.Keys) > 256 {
		return nil, ErrKeys
	}
	entries := map[string]CatalogEntry{}
	for _, entry := range catalog.Keys {
		if !validReference(entry.KeyReference) || !environmentBinding.MatchString(entry.Binding) {
			return nil, ErrKeys
		}
		if _, exists := entries[entry.Binding]; exists {
			return nil, ErrKeys
		}
		entries[entry.Binding] = entry
	}
	return entries, nil
}

// References resolves the current catalog versions for exact configuration
// bindings. Retained wrapping-key versions use distinct environment bindings.
func (s EnvironmentKeys) References(ctx context.Context, bindings []string) ([]KeyReference, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := s.catalog()
	if err != nil {
		return nil, err
	}
	refs := make([]KeyReference, 0, len(bindings))
	seen := map[string]bool{}
	for _, binding := range bindings {
		entry, ok := entries[binding]
		if !ok || !entry.Enabled || seen[binding] || s.Getenv(binding) == "" {
			return nil, ErrKeys
		}
		seen[binding] = true
		refs = append(refs, entry.KeyReference)
	}
	return refs, nil
}

func (s EnvironmentKeys) Check(ctx context.Context, refs []KeyReference) error {
	bindings := make([]string, len(refs))
	for i, ref := range refs {
		bindings[i] = ref.Binding
	}
	current, err := s.References(ctx, bindings)
	if err != nil {
		return err
	}
	for i, ref := range refs {
		if current[i] != ref {
			return ErrKeys
		}
	}
	return nil
}

func (s EnvironmentKeys) WrappingKey(ctx context.Context, ref KeyReference) (key [32]byte, err error) {
	if err = s.Check(ctx, []KeyReference{ref}); err != nil {
		return key, err
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(s.Getenv(ref.Binding))
	defer clear(decoded)
	if err != nil || len(decoded) != len(key) {
		return key, ErrKeys
	}
	copy(key[:], decoded)
	return key, nil
}

// ReadConfiguration authenticates a bundle for a trusted preflight consumer.
// It does not restore files or authorize runtime activation.
func ReadConfiguration(ctx context.Context, directory string, expected Version, authority Authority, keys KeyStore) ([]byte, Manifest, error) {
	envelope, err := readBundle(ctx, directory)
	if err != nil {
		return nil, Manifest{}, err
	}
	configuration, err := Open(ctx, envelope, expected, authority, keys)
	if err != nil {
		return nil, Manifest{}, err
	}
	return configuration, cloneManifest(envelope.Manifest), nil
}

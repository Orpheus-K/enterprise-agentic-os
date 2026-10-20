package recovery

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvironmentKeysRereadsVersionsAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	ref := KeyReference{Binding: "BACKUP_KEY_V1", ID: "environment/backup", Version: "v1"}
	catalog := KeyCatalog{Format: 1, Keys: []CatalogEntry{{KeyReference: ref, Enabled: true}}}
	write := func() {
		data, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	value := base64.StdEncoding.EncodeToString(make([]byte, 32))
	store := EnvironmentKeys{CatalogPath: path, Getenv: func(name string) string {
		if name != ref.Binding {
			t.Fatal("unexpected environment reference")
		}
		return value
	}}
	refs, err := store.References(t.Context(), []string{ref.Binding})
	if err != nil || len(refs) != 1 || refs[0] != ref {
		t.Fatalf("resolve: %v", err)
	}
	if _, err = store.WrappingKey(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	catalog.Keys[0].Enabled = false
	write()
	if err = store.Check(t.Context(), refs); !errors.Is(err, ErrKeys) {
		t.Fatal("cached enabled state")
	}
	catalog.Keys[0].Enabled = true
	catalog.Keys[0].Version = "v2"
	write()
	if err = store.Check(t.Context(), refs); !errors.Is(err, ErrKeys) {
		t.Fatal("old version silently replaced")
	}
	catalog.Keys[0].Version = "v1"
	write()
	value = "not-a-256-bit-key"
	if _, err = store.WrappingKey(t.Context(), ref); !errors.Is(err, ErrKeys) {
		t.Fatal("invalid wrapping key")
	}
	value = ""
	if err = store.Check(t.Context(), refs); !errors.Is(err, ErrKeys) {
		t.Fatal("missing secret")
	}
}

func TestEnvironmentCatalogRejectsPlaintextAndAmbiguity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	store := EnvironmentKeys{CatalogPath: path, Getenv: func(string) string { return "synthetic" }}
	entry := `{"binding":"APP","id":"env/app","version":"v1","enabled":true}`
	for _, input := range []string{
		`{"format":1,"keys":[` + entry + `],"secret":"plaintext"}`,
		`{"format":1,"keys":[` + entry + `,` + entry + `]}`,
		`{"format":1,"keys":[]} {}`,
		`{"format":1,"keys":[{"binding":"APP","id":"env/app","version":"latest","enabled":true}]}`,
	} {
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.References(t.Context(), []string{"APP"}); !errors.Is(err, ErrKeys) {
			t.Fatal("invalid key catalog accepted")
		}
	}
}

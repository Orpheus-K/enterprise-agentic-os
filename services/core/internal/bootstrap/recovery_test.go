package bootstrap

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agentos.local/core/internal/model"
	"agentos.local/core/internal/recovery"
	"agentos.local/core/internal/sqlitedb"
	"agentos.local/core/internal/toolgateway"
)

func writeRecoveryJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func sqliteRecoverySite(t *testing.T) (recoveryPlan, recovery.KeyCatalog, []byte) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	if err := sqlitedb.Migrate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `INSERT INTO iam_tenants(id,name) VALUES('tenant','synthetic')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	configuration := map[string]any{
		"storage_driver": "sqlite", "sqlite_path": source,
		"tenant_id": "10000000-0000-0000-0000-000000000001", "app_id": "cli_test",
		"channel": map[string]any{"type": "feishu", "tenant_key": "test", "mode": "webhook", "app_secret_env": "APP", "verification_token_env": "TOKEN", "encrypt_key_env": "KEY"},
		"model":   map[string]any{"endpoint": "http://127.0.0.1:1/chat/completions", "name": "synthetic", "max_tokens": 64, "allow_http": true},
	}
	plan := recoveryPlan{Format: 1, Driver: "sqlite", ChannelConfig: filepath.Join(root, "channel.json"), KeyCatalog: filepath.Join(root, "keys.json"), WrappingKeyBinding: "BACKUP_KEY_V1", AuthoritySQLitePath: source}
	writeRecoveryJSON(t, plan.ChannelConfig, configuration)
	raw, err := os.ReadFile(plan.ChannelConfig)
	if err != nil {
		t.Fatal(err)
	}
	catalog := recovery.KeyCatalog{Format: 1}
	for _, binding := range []string{"APP", "TOKEN", "KEY", "BACKUP_KEY_V1"} {
		catalog.Keys = append(catalog.Keys, recovery.CatalogEntry{KeyReference: recovery.KeyReference{Binding: binding, ID: "environment/" + binding, Version: "v1"}, Enabled: true})
		t.Setenv(binding, "synthetic-private-value")
	}
	t.Setenv("BACKUP_KEY_V1", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	writeRecoveryJSON(t, plan.KeyCatalog, catalog)
	return plan, catalog, raw
}

type failingRecoveryOutput struct{}

func (failingRecoveryOutput) Write([]byte) (int, error) {
	return 0, errors.New("synthetic writer failure")
}

func invokeRecovery(t *testing.T, plan recoveryPlan, request recoveryRequest, output io.Writer) error {
	t.Helper()
	planPath := filepath.Join(filepath.Dir(plan.KeyCatalog), "plan.json")
	writeRecoveryJSON(t, planPath, plan)
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return RunRecovery(t.Context(), planPath, bytes.NewReader(raw), output)
}

func TestSQLiteRecoveryAdministration(t *testing.T) {
	for _, scenario := range []string{"valid", "lost-configuration", "restored-secrets", "revoked", "key-disabled", "key-version-changed", "missing-reference", "self-authority", "output-failure"} {
		t.Run(scenario, func(t *testing.T) {
			plan, fixture, configuration := sqliteRecoverySite(t)
			root := filepath.Dir(plan.KeyCatalog)
			bundle := filepath.Join(root, "bundle")
			target := filepath.Join(root, "restored")
			var output bytes.Buffer
			if err := invokeRecovery(t, plan, recoveryRequest{Action: "backup", Directory: bundle}, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), `"verified":true`) || strings.Contains(output.String(), "synthetic-private-value") {
				t.Fatal("invalid backup response")
			}
			switch scenario {
			case "lost-configuration":
				if err := os.Remove(plan.ChannelConfig); err != nil {
					t.Fatal(err)
				}
			case "restored-secrets":
				t.Setenv("APP", "")
				if err := invokeRecovery(t, plan, recoveryRequest{Action: "restore", Directory: bundle, TargetDirectory: target}, io.Discard); !errors.Is(err, recovery.ErrKeys) {
					t.Fatalf("missing external secret: %v", err)
				}
				t.Setenv("APP", "synthetic-private-value")
			case "revoked":
				db, err := sqlitedb.Open(t.Context(), plan.AuthoritySQLitePath)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.ExecContext(t.Context(), `UPDATE iam_tenants SET enabled=0`)
				closeErr := db.Close()
				if err != nil || closeErr != nil {
					t.Fatal("revoke failed")
				}
			case "key-disabled":
				fixture.Keys[0].Enabled = false
				writeRecoveryJSON(t, plan.KeyCatalog, fixture)
			case "key-version-changed":
				fixture.Keys[0].Version = "v2"
				writeRecoveryJSON(t, plan.KeyCatalog, fixture)
			case "self-authority":
				plan.AuthoritySQLitePath = filepath.Join(bundle, "database", "database.sqlite")
			case "missing-reference":
				// A cryptographically valid but incomplete library-produced bundle
				// must still fail the configuration consumer's completeness check.
				path := filepath.Join(bundle, "joint.json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				envelope, err := recovery.Decode(raw)
				if err != nil {
					t.Fatal(err)
				}
				envelope.Manifest.Secrets = envelope.Manifest.Secrets[1:]
				keys := recovery.EnvironmentKeys{CatalogPath: plan.KeyCatalog, Getenv: os.Getenv}
				envelope, err = recovery.Seal(t.Context(), envelope.Manifest, configuration, keys)
				if err != nil {
					t.Fatal(err)
				}
				writeRecoveryJSON(t, path, envelope)
			}
			output.Reset()
			var writer io.Writer = &output
			if scenario == "output-failure" {
				writer = failingRecoveryOutput{}
			}
			err := invokeRecovery(t, plan, recoveryRequest{Action: "restore", Directory: bundle, TargetDirectory: target}, writer)
			if scenario == "valid" || scenario == "lost-configuration" || scenario == "restored-secrets" || scenario == "output-failure" {
				if scenario == "output-failure" {
					if err == nil || !strings.Contains(err.Error(), "inspect target") {
						t.Fatal("output ambiguity hidden")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				restored, err := os.ReadFile(filepath.Join(target, "configuration.json"))
				if err != nil || !bytes.Equal(restored, configuration) {
					t.Fatalf("configuration not restored: %v", err)
				}
				if scenario != "output-failure" && !strings.Contains(output.String(), `"runtime_start_allowed":false`) {
					t.Fatal("activation boundary missing")
				}
			} else {
				if err == nil || output.Len() != 0 {
					t.Fatal("unsafe recovery succeeded")
				}
				if strings.Contains(err.Error(), "synthetic-private-value") {
					t.Fatal("secret leaked")
				}
				if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
					t.Fatal("unsafe target published")
				}
			}
		})
	}
}

func TestRecoveryAdministrationRejectsInvalidInputs(t *testing.T) {
	plan, fixture, _ := sqliteRecoverySite(t)
	root := filepath.Dir(plan.KeyCatalog)
	planPath := filepath.Join(root, "plan.json")
	writeRecoveryJSON(t, planPath, plan)
	for _, request := range []string{`{"action":"backup","secret":"synthetic-private-value"}`, `{} {}`, strings.Repeat(" ", 16*1024+1)} {
		var output bytes.Buffer
		if err := RunRecovery(t.Context(), planPath, strings.NewReader(request), &output); err == nil || output.Len() != 0 {
			t.Fatal("invalid input accepted")
		}
	}
	fixture.Keys = fixture.Keys[1:]
	writeRecoveryJSON(t, plan.KeyCatalog, fixture)
	bundle := filepath.Join(root, "missing-key-bundle")
	if err := invokeRecovery(t, plan, recoveryRequest{Action: "backup", Directory: bundle}, io.Discard); !errors.Is(err, recovery.ErrKeys) {
		t.Fatalf("missing reference: %v", err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatal("backup started before secret verification")
	}
}

func TestRecoveryBindingsIncludeAllConfiguredCredentialFamilies(t *testing.T) {
	c := Config{
		DatabaseURLEnv: "DB", Channel: testFeishu("webhook", "APP", "TOKEN", "KEY"),
		Model: model.Config{APIKeyEnv: "MODEL"}, Embedding: &model.EmbeddingConfig{APIKeyEnv: "EMBED"},
		Operations: &OperationsConfig{TokenEnv: "OPS"},
		Tools:      &ToolsConfig{Credentials: []toolgateway.CredentialBinding{{TokenEnv: "TOOL"}}},
		OAuth:      &OAuthConfig{KeyEnvs: map[string]string{"old": "OAUTH_OLD", "new": "OAUTH_NEW"}},
		Sources:    []SourceConfig{{Owner: SourceUser{AccessTokenEnv: "OWNER"}, Recipients: []SourceUser{{AccessTokenEnv: "RECIPIENT"}, {AccessTokenEnv: "OWNER"}}}},
	}
	expected := []string{"APP", "DB", "EMBED", "KEY", "MODEL", "OAUTH_NEW", "OAUTH_OLD", "OPS", "OWNER", "RECIPIENT", "TOKEN", "TOOL"}
	if !slices.Equal(c.recoveryBindings(), expected) {
		t.Fatal("configured credential family missing or duplicated")
	}
}

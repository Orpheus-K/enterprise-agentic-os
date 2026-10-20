//go:build integration

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/recovery"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPostgresRecoveryAdministration(t *testing.T) {
	for _, scenario := range []string{"valid", "source-mismatch", "key-version-changed", "missing-persisted-key"} {
		t.Run(scenario, func(t *testing.T) {
			f := testpg.New(t)
			ctx := t.Context()
			m, err := database.NewMigrator(ctx, f.MigrationURL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.Up(ctx)
			closeErr := m.Close()
			if err != nil || closeErr != nil {
				t.Fatal("migration failed")
			}
			plan, catalog, raw := sqliteRecoverySite(t)
			var configuration map[string]any
			if err = json.Unmarshal(raw, &configuration); err != nil {
				t.Fatal(err)
			}
			delete(configuration, "sqlite_path")
			configuration["storage_driver"] = "postgres"
			configuration["database_url_env"] = "RUNTIME_DB"
			writeRecoveryJSON(t, plan.ChannelConfig, configuration)
			catalog.Keys = append(catalog.Keys, recovery.CatalogEntry{KeyReference: recovery.KeyReference{Binding: "RUNTIME_DB", ID: "environment/runtime-db", Version: "v1"}, Enabled: true})
			writeRecoveryJSON(t, plan.KeyCatalog, catalog)
			t.Setenv("RUNTIME_DB", f.RuntimeURL)
			t.Setenv("BACKUP_DB", f.AdminURL)
			t.Setenv("AUTHORITY_DB", f.AdminURL)
			plan.Driver = "postgres"
			plan.AdminURLEnv = "BACKUP_DB"
			plan.AuthorityURLEnv = "AUTHORITY_DB"
			plan.AuthoritySQLitePath = ""
			find := func(name string) string {
				if directory := os.Getenv("AGENTOS_TEST_PG_BIN"); directory != "" {
					if runtime.GOOS == "windows" {
						name += ".exe"
					}
					return filepath.Join(directory, name)
				}
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatal("PG 17 client tools required")
				}
				path, err = filepath.Abs(path)
				if err != nil {
					t.Fatal(err)
				}
				return path
			}
			plan.PGDump = find("pg_dump")
			plan.PGRestore = find("pg_restore")
			root := filepath.Dir(plan.KeyCatalog)
			bundle, target := filepath.Join(root, "bundle"), filepath.Join(root, "restored")
			if scenario == "source-mismatch" {
				other := testpg.New(t)
				t.Setenv("BACKUP_DB", other.AdminURL)
			}
			if scenario == "missing-persisted-key" {
				for _, query := range []string{
					`INSERT INTO iam.tenants(id,name) VALUES('10000000-0000-0000-0000-000000000001','synthetic')`,
					`INSERT INTO iam.members(tenant_id,id,employee_id) VALUES('10000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001','employee')`,
					`INSERT INTO channel.oauth_credentials(tenant_id,app_id,tenant_key,open_id,member_id,revision,key_id,ciphertext) VALUES('10000000-0000-0000-0000-000000000001','app','tenant','open-id','20000000-0000-0000-0000-000000000001',1,'missing_key',decode(repeat('01',29),'hex'))`,
				} {
					if _, err = f.Admin.Exec(ctx, query); err != nil {
						t.Fatal(err)
					}
				}
			}
			var output bytes.Buffer
			err = invokeRecovery(t, plan, recoveryRequest{Action: "backup", Directory: bundle}, &output)
			if scenario == "source-mismatch" || scenario == "missing-persisted-key" {
				if err == nil || output.Len() != 0 {
					t.Fatal("different database/config bound together")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "key-version-changed" {
				catalog.Keys[len(catalog.Keys)-1].Version = "v2"
				writeRecoveryJSON(t, plan.KeyCatalog, catalog)
			}
			targetDB := "agentos_test_admin_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if _, err := f.Admin.Exec(cleanup, `DROP DATABASE IF EXISTS `+pgx.Identifier{targetDB}.Sanitize()+` WITH (FORCE)`); err != nil {
					t.Error(err)
				}
			})
			output.Reset()
			err = invokeRecovery(t, plan, recoveryRequest{Action: "restore", Directory: bundle, TargetDirectory: target, TargetDatabase: targetDB}, &output)
			if scenario == "key-version-changed" {
				if err == nil || output.Len() != 0 {
					t.Fatal("changed runtime credential version accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), `"database_quarantined":true`) || !strings.Contains(output.String(), `"runtime_start_allowed":false`) {
				t.Fatal("quarantine not reported")
			}
			actual, err := os.ReadFile(filepath.Join(target, "configuration.json"))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(plan.ChannelConfig)
			if err != nil || !bytes.Equal(actual, expected) {
				t.Fatal("configuration not recovered")
			}
		})
	}
}

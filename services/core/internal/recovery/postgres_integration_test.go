//go:build integration

package recovery

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/quotation"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func postgresTestTools(t *testing.T) PostgresTools {
	t.Helper()
	find := func(name string) string {
		if directory := os.Getenv("AGENTOS_TEST_PG_BIN"); directory != "" {
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			return filepath.Join(directory, name)
		}
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal("PostgreSQL client tools required for joint recovery integration tests")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		return absolute
	}
	return PostgresTools{Dump: find("pg_dump"), Restore: find("pg_restore")}
}

func TestPostgresJointRecovery(t *testing.T) {
	tools := postgresTestTools(t)
	for _, scenario := range []string{"valid", "member-revoked", "acl-revoked", "oauth-changed", "seal-revoked", "revoked-during-restore", "key-revoked", "key-revoked-during-restore", "version", "tampered", "existing", "runtime-role", "restore-command-failed"} {
		t.Run(scenario, func(t *testing.T) {
			f := testpg.New(t)
			ctx := t.Context()
			migrator, err := database.NewMigrator(ctx, f.MigrationURL)
			if err != nil {
				t.Fatal(err)
			}
			_, err = migrator.Up(ctx)
			closeErr := migrator.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("migrate: %v %v", err, closeErr)
			}
			for _, query := range []string{
				`INSERT INTO iam.tenants(id,name) VALUES('10000000-0000-0000-0000-000000000001','synthetic')`,
				`INSERT INTO iam.members(tenant_id,id,employee_id) VALUES('10000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001','employee')`,
				`INSERT INTO iam.identity_links(tenant_id,provider,issuer,subject,member_id) VALUES('10000000-0000-0000-0000-000000000001','feishu','app','open-id','20000000-0000-0000-0000-000000000001')`,
				`INSERT INTO knowledge.sources(tenant_id,app_id,id,sync_member,enabled) VALUES('10000000-0000-0000-0000-000000000001','app','30000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001',true)`,
				`INSERT INTO knowledge.documents(tenant_id,app_id,source_id,id) VALUES('10000000-0000-0000-0000-000000000001','app','30000000-0000-0000-0000-000000000001','document')`,
				`INSERT INTO channel.oauth_credentials(tenant_id,app_id,tenant_key,open_id,member_id,revision,key_id,ciphertext) VALUES('10000000-0000-0000-0000-000000000001','app','tenant','open-id','20000000-0000-0000-0000-000000000001',1,'key',decode(repeat('01',29),'hex'))`,
			} {
				if _, err = f.Admin.Exec(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			policy, err := quotation.NewSealPolicy(quotation.SealPolicy{TenantID: uuid.MustParse("10000000-0000-0000-0000-000000000001"), AppID: "app", SealID: "synthetic", Members: []uuid.UUID{uuid.MustParse("20000000-0000-0000-0000-000000000001")}, MaximumMinor: 100, NotBefore: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Admin.Exec(ctx, `INSERT INTO channel.seal_policies(tenant_id,app_id,seal_id,revision,policy) VALUES($1,$2,$3,$4,$5)`, policy.TenantID, policy.AppID, policy.SealID, policy.Revision, policy); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			bundle, target := filepath.Join(root, "bundle"), filepath.Join(root, "restored")
			manifest, configuration, keys := fixture("postgres")
			manifest.Version.Schema = database.SchemaVersion
			manifest.OAuthKeyBindings = map[string]string{"key": "APP_SECRET"}
			sourceURL := f.AdminURL
			if scenario == "runtime-role" {
				sourceURL = f.RuntimeURL
			}
			err = BackupPostgres(ctx, sourceURL, bundle, configuration, manifest, keys, tools)
			if scenario == "runtime-role" {
				if err == nil {
					t.Fatal("RLS-scoped backup accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			keys.checks = 0
			targetDB := "agentos_test_recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if _, err := f.Admin.Exec(cleanup, `DROP DATABASE IF EXISTS `+pgx.Identifier{targetDB}.Sanitize()+` WITH (FORCE)`); err != nil {
					t.Error(err)
				}
			})
			calls := 0
			revoke := func() {
				if _, err := f.Admin.Exec(ctx, `UPDATE iam.members SET enabled=false`); err != nil {
					t.Fatal(err)
				}
			}
			authority := func(ctx context.Context) (string, error) {
				calls++
				if scenario == "revoked-during-restore" && calls == 3 {
					revoke()
				}
				return PostgresAuthorization(ctx, f.AdminURL)
			}
			restoreTools := tools
			switch scenario {
			case "seal-revoked":
				if _, err = f.Admin.Exec(ctx, `UPDATE channel.seal_policies SET enabled=false`); err != nil {
					t.Fatal(err)
				}
			case "member-revoked":
				revoke()
			case "acl-revoked":
				if _, err = f.Admin.Exec(ctx, `UPDATE knowledge.documents SET deleted=true,acl_revision=acl_revision+1`); err != nil {
					t.Fatal(err)
				}
			case "oauth-changed":
				if _, err = f.Admin.Exec(ctx, `UPDATE channel.oauth_credentials SET revision=revision+1,ciphertext=decode(repeat('02',29),'hex')`); err != nil {
					t.Fatal(err)
				}
			case "key-revoked":
				keys.failAt = 1
			case "key-revoked-during-restore":
				keys.failAt = 3
			case "version":
				manifest.Version.Schema++
			case "tampered":
				if err = os.WriteFile(filepath.Join(bundle, "database", "database.dump"), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing":
				if _, err = f.Admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{targetDB}.Sanitize()); err != nil {
					t.Fatal(err)
				}
			case "restore-command-failed":
				restoreTools.Restore = filepath.Join(root, "missing-client")
			}
			err = RestorePostgres(ctx, f.AdminURL, bundle, targetDB, target, manifest.Version, authority, keys, restoreTools)
			var exists bool
			if queryErr := f.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, targetDB).Scan(&exists); queryErr != nil {
				t.Fatal(queryErr)
			}
			if scenario != "valid" {
				if err == nil {
					t.Fatal("unsafe restore accepted")
				}
				if scenario != "existing" && exists {
					t.Fatal("failed restore left database")
				}
				if scenario == "existing" && !exists {
					t.Fatal("existing database removed")
				}
				if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
					t.Fatal("failed restore published configuration")
				}
				return
			}
			if err != nil || !exists {
				t.Fatalf("restore: %v", err)
			}
			actual, err := os.ReadFile(filepath.Join(target, "configuration.json"))
			if err != nil || !bytes.Equal(actual, configuration) {
				t.Fatalf("config: %v", err)
			}
			var quarantined bool
			if err = f.Admin.QueryRow(ctx, `SELECT datconnlimit=0 AND NOT has_database_privilege('agentos_runtime',oid,'CONNECT') AND NOT has_database_privilege('agentos_migrator',oid,'CONNECT') FROM pg_database WHERE datname=$1`, targetDB).Scan(&quarantined); err != nil || !quarantined {
				t.Fatalf("not quarantined: %v", err)
			}
			parsed, _ := url.Parse(f.RuntimeURL)
			parsed.Path = "/" + targetDB
			if connection, err := pgx.Connect(ctx, parsed.String()); err == nil {
				connection.Close(ctx)
				t.Fatal("runtime entered recovery database")
			}
			parsed, _ = url.Parse(f.AdminURL)
			parsed.Path = "/" + targetDB
			facts, err := PostgresAuthorization(ctx, parsed.String())
			if err != nil {
				t.Fatal(err)
			}
			current, err := PostgresAuthorization(ctx, f.AdminURL)
			if err != nil || facts != current {
				t.Fatalf("authorization changed: %v", err)
			}
		})
	}
}

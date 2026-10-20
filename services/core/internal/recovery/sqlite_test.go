package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agentos.local/core/internal/sqlitedb"
)

func TestSQLiteJointRecovery(t *testing.T) {
	for _, online := range []bool{false, true} {
		name := "stopped"
		if online {
			name = "wal"
		}
		t.Run(name, func(t *testing.T) {
			for _, scenario := range []string{"valid", "revoked", "revoked-during-restore", "key-revoked", "key-revoked-during-restore", "wrong-version", "tampered-database", "tampered-config", "missing-key-store", "existing-target", "missing-authority", "extra-file"} {
				t.Run(scenario, func(t *testing.T) {
					ctx := t.Context()
					root := t.TempDir()
					source, bundle, target := filepath.Join(root, "source.db"), filepath.Join(root, "bundle"), filepath.Join(root, "restored")
					if err := sqlitedb.Migrate(ctx, source); err != nil {
						t.Fatal(err)
					}
					db, err := sqlitedb.Open(ctx, source)
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					for _, query := range []string{
						`INSERT INTO iam_tenants(id,name) VALUES('tenant','synthetic')`,
						`INSERT INTO iam_members(tenant_id,id,employee_id) VALUES('tenant','member','employee')`,
						`INSERT INTO iam_identity_links(tenant_id,provider,issuer,subject,member_id) VALUES('tenant','feishu','app','open-id','member')`,
						`INSERT INTO iam_operation_grants(tenant_id,member_id,app_id,capability) VALUES('tenant','member','app','task.approve')`,
					} {
						if _, err = db.ExecContext(ctx, query); err != nil {
							t.Fatal(err)
						}
					}
					if online {
						if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
							t.Fatal(err)
						}
					}
					manifest, configuration, keys := fixture("sqlite")
					manifest.Version.Schema = sqlitedb.SchemaVersion
					if err = BackupSQLite(ctx, source, bundle, online, configuration, manifest, keys); err != nil {
						t.Fatal(err)
					}
					// Only encrypted configuration and external references leave the
					// machine. No plaintext secret/config value occurs in any file.
					if err = filepath.WalkDir(bundle, func(path string, entry os.DirEntry, walkErr error) error {
						if walkErr != nil {
							return walkErr
						}
						if entry.IsDir() {
							return nil
						}
						data, readErr := os.ReadFile(path)
						if bytes.Contains(data, []byte("private-value")) {
							t.Fatal("configuration plaintext exported")
						}
						return readErr
					}); err != nil {
						t.Fatal(err)
					}
					keys.checks = 0
					var store KeyStore = keys
					calls := 0
					revoke := func() {
						if _, err := db.ExecContext(ctx, `UPDATE iam_operation_grants SET enabled=0`); err != nil {
							t.Fatal(err)
						}
					}
					authority := Authority(func(ctx context.Context) (string, error) {
						calls++
						if scenario == "revoked-during-restore" && calls == 3 {
							revoke()
						}
						return SQLiteAuthorization(ctx, source)
					})
					switch scenario {
					case "revoked":
						revoke()
					case "key-revoked":
						keys.failAt = 1
					case "key-revoked-during-restore":
						keys.failAt = 3
					case "wrong-version":
						manifest.Version.ArtifactSHA256 = Digest([]byte("different version"))
					case "missing-key-store":
						store = nil
					case "missing-authority":
						authority = nil
					case "existing-target":
						if err = os.Mkdir(target, 0700); err != nil {
							t.Fatal(err)
						}
					case "extra-file":
						if err = os.WriteFile(filepath.Join(bundle, "extra"), []byte("extra"), 0600); err != nil {
							t.Fatal(err)
						}
					case "tampered-database":
						if err = os.WriteFile(filepath.Join(bundle, "database", "database.sqlite"), []byte("bad"), 0600); err != nil {
							t.Fatal(err)
						}
					case "tampered-config":
						filename := filepath.Join(bundle, "joint.json")
						raw, readErr := os.ReadFile(filename)
						if readErr != nil {
							t.Fatal(readErr)
						}
						envelope, decodeErr := Decode(raw)
						if decodeErr != nil {
							t.Fatal(decodeErr)
						}
						envelope.Ciphertext[0]++
						raw, err = json.Marshal(envelope)
						if err != nil {
							t.Fatal(err)
						}
						if err = os.WriteFile(filename, raw, 0600); err != nil {
							t.Fatal(err)
						}
					}
					err = RestoreSQLite(ctx, bundle, target, manifest.Version, authority, store)
					if scenario != "valid" {
						if err == nil {
							t.Fatal("unsafe restore accepted")
						}
						if scenario != "existing-target" {
							if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
								t.Fatal("failed restore published output")
							}
						}
						staging, _ := filepath.Glob(filepath.Join(root, ".joint-restore-*"))
						if len(staging) != 0 {
							t.Fatal("staging output retained")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					actual, err := os.ReadFile(filepath.Join(target, "configuration.json"))
					if err != nil || !bytes.Equal(actual, configuration) {
						t.Fatalf("configuration recovery: %v", err)
					}
					expectedFacts, err := SQLiteAuthorization(ctx, source)
					if err != nil {
						t.Fatal(err)
					}
					restoredFacts, err := SQLiteAuthorization(ctx, filepath.Join(target, "database.sqlite"))
					if err != nil || restoredFacts != expectedFacts {
						t.Fatalf("authorization recovery: %v", err)
					}
					if err = RestoreSQLite(ctx, bundle, target, manifest.Version, authority, keys); err == nil {
						t.Fatal("existing restore overwritten")
					}
				})
			}
		})
	}
}

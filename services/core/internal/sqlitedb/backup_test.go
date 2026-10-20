package sqlitedb

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupRestoreAndFailures(t *testing.T) {
	for _, scenario := range []string{"valid", "tampered", "missing", "manifest", "existing", "future", "foreign_key", "wal", "busy", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			source, backup, target := filepath.Join(root, "source.db"), filepath.Join(root, "backup"), filepath.Join(root, "restored.db")
			if err := Migrate(ctx, source); err != nil {
				t.Fatal(err)
			}
			db, err := Open(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.ExecContext(ctx, "INSERT INTO iam_tenants(id,name) VALUES('tenant','retained')"); err != nil {
				t.Fatal(err)
			}
			if scenario == "wal" {
				if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "future" {
				if _, err := db.ExecContext(ctx, "INSERT INTO goose_db_version(version_id,is_applied) VALUES(999,1)"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "foreign_key" {
				if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, "INSERT INTO iam_members(tenant_id,id,employee_id) VALUES('absent','orphan','orphan')"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "busy" {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
			}
			if scenario == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			started := time.Now()
			err = Backup(ctx, source, backup)
			backupTime := time.Since(started)
			if scenario == "wal" || scenario == "future" || scenario == "foreign_key" || scenario == "busy" || scenario == "cancelled" {
				if err == nil {
					t.Fatal("unsafe backup accepted")
				}
				if _, err := os.Stat(filepath.Join(backup, "manifest.json")); !os.IsNotExist(err) {
					t.Fatal("failed backup has completion manifest")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Backup(ctx, source, backup); err == nil {
				t.Fatal("overwrote backup")
			}
			switch scenario {
			case "tampered":
				if err := os.WriteFile(filepath.Join(backup, "database.sqlite"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(filepath.Join(backup, "database.sqlite")); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				if err := os.WriteFile(filepath.Join(backup, "manifest.json"), []byte(`{"format":2}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing":
				if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			started = time.Now()
			err = Restore(ctx, backup, target)
			restoreTime := time.Since(started)
			if scenario != "valid" {
				if err == nil {
					t.Fatal("invalid restore accepted")
				}
				if scenario == "existing" {
					raw, err := os.ReadFile(target)
					if err != nil || string(raw) != "preserve" {
						t.Fatal("overwrote target")
					}
				} else if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("failed restore left target")
				}
				return
			}
			restored, err := Open(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			var name string
			if err := restored.QueryRowContext(ctx, "SELECT name FROM iam_tenants WHERE id='tenant'").Scan(&name); err != nil || name != "retained" {
				t.Fatalf("restore data: %s %v", name, err)
			}
			if err := Restore(ctx, backup, target); err == nil {
				t.Fatal("restored over existing database")
			}
			var manifest backupManifest
			raw, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != SchemaVersion || manifest.Bytes == 0 {
				t.Fatal("missing backup identity")
			}
			t.Logf("local drill: bytes=%d backup=%s restore=%s; timings exclude operator/config/credential recovery", manifest.Bytes, backupTime, restoreTime)
		})
	}
}

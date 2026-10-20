package sqlitedb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOnlineBackup(t *testing.T) {
	for _, scenario := range []string{"valid", "writer", "delete", "future", "foreign_key", "corrupt", "cancelled", "tampered", "occupied"} {
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
			exec := func(query string) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "delete" {
				exec("PRAGMA journal_mode=WAL")
			}
			exec("INSERT INTO iam_tenants(id,name) VALUES('tenant','committed')")
			switch scenario {
			case "writer":
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.ExecContext(ctx, "UPDATE iam_tenants SET name='uncommitted'"); err != nil {
					t.Fatal(err)
				}
			case "future":
				exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES(999,1)")
			case "foreign_key":
				exec("PRAGMA foreign_keys=OFF")
				exec("INSERT INTO iam_members(tenant_id,id,employee_id) VALUES('absent','orphan','orphan')")
			case "corrupt":
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(source, []byte("invalid database"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "occupied":
				if err := os.Mkdir(backup, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(backup, "keep"), []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = BackupOnline(ctx, source, backup)
			switch scenario {
			case "delete", "future", "foreign_key", "corrupt", "cancelled", "occupied":
				if err == nil {
					t.Fatal("unsafe backup succeeded")
				}
				if _, err := os.Stat(filepath.Join(backup, "manifest.json")); !os.IsNotExist(err) {
					t.Fatal("failed backup published manifest")
				}
				if scenario == "occupied" {
					raw, err := os.ReadFile(filepath.Join(backup, "keep"))
					if err != nil || string(raw) != "preserve" {
						t.Fatal("changed occupied directory")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := BackupOnline(ctx, source, backup); err == nil {
				t.Fatal("overwrote backup")
			}
			if scenario == "tampered" {
				if err := os.WriteFile(filepath.Join(backup, "database.sqlite"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := Restore(ctx, backup, target); err == nil {
					t.Fatal("restored tampered snapshot")
				}
				return
			}
			if err := Restore(ctx, backup, target); err != nil {
				t.Fatal(err)
			}
			restored, err := Open(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			var name string
			if err := restored.QueryRowContext(ctx, "SELECT name FROM iam_tenants WHERE id='tenant'").Scan(&name); err != nil || name != "committed" {
				t.Fatalf("inconsistent snapshot: %s %v", name, err)
			}
		})
	}
}

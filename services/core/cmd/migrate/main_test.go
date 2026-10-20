package main

import (
	"agentos.local/core/internal/sqlitedb"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func emptyEnvironment(string) string { return "" }
func TestSQLiteMigrationCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks #%.db")
	for range 2 {
		var output bytes.Buffer
		if err := execute([]string{"--sqlite", path}, emptyEnvironment, &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "SQLite schema") {
			t.Fatal("missing successful completion")
		}
		db, err := sqlitedb.Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), "INSERT INTO iam_tenants(id,name) VALUES('tenant','retained') ON CONFLICT DO NOTHING"); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	db, err := sqlitedb.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var name string
	if err := db.QueryRowContext(t.Context(), "SELECT name FROM iam_tenants WHERE id='tenant'").Scan(&name); err != nil || name != "retained" {
		t.Fatalf("lost data: %s %v", name, err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO goose_db_version(version_id,is_applied) VALUES(999,1)"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := execute([]string{"--sqlite", path}, emptyEnvironment, &output); err == nil || output.Len() != 0 {
		t.Fatalf("future schema accepted: %v %s", err, output.String())
	}
}
func TestMigrationTargetValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untouched.db")
	for _, args := range [][]string{{"--sqlite"}, {"--sqlite", ""}, {"--sqlite", "relative.db"}, {"--sqlite", path, "extra"}, {"--unknown"}} {
		var output bytes.Buffer
		if err := execute(args, emptyEnvironment, &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid command accepted: %v %v", args, err)
		}
	}
	var output bytes.Buffer
	secret := "postgres://private:secret@invalid/db"
	err := execute([]string{"--sqlite", path}, func(string) string { return secret }, &output)
	if err == nil || strings.Contains(err.Error(), secret) || output.Len() != 0 {
		t.Fatalf("ambiguous target: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("validation created file: %v", err)
	}
	if err := execute([]string{"--version"}, func(string) string { t.Fatal("version read environment"); return "" }, &output); err != nil {
		t.Fatal(err)
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, context.Canceled }
func TestMigrationOutputFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	if err := execute([]string{"--sqlite", path}, emptyEnvironment, failedOutput{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost output error: %v", err)
	}
}

func TestSQLiteBackupCommands(t *testing.T) {
	for _, online := range []bool{false, true} {
		t.Run(fmt.Sprint(online), func(t *testing.T) { testSQLiteBackupCommands(t, online) })
	}
}

func testSQLiteBackupCommands(t *testing.T, online bool) {
	root := t.TempDir()
	source, backup, target := filepath.Join(root, "source.db"), filepath.Join(root, "backup"), filepath.Join(root, "restored.db")
	if err := sqlitedb.Migrate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	command := "--sqlite-backup"
	if online {
		command = "--sqlite-backup-online"
		db, err := sqlitedb.Open(t.Context(), source)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(t.Context(), "PRAGMA journal_mode=WAL")
		err = errors.Join(err, db.Close())
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{command, source, backup}, {"--sqlite-restore", backup, target}} {
		var output bytes.Buffer
		if err := execute(args, emptyEnvironment, &output); err != nil || !strings.Contains(output.String(), "verified") {
			t.Fatalf("command failed: %v %s", err, output.String())
		}
		output.Reset()
		if err := execute(args, emptyEnvironment, &output); err == nil || output.Len() != 0 {
			t.Fatal("overwrote existing backup/restore target")
		}
	}
	for _, args := range [][]string{{"--sqlite-backup", source}, {"--sqlite-restore", backup}, {"--sqlite-backup", "relative", filepath.Join(root, "invalid")}, {"--sqlite-restore", backup, "relative"}} {
		var output bytes.Buffer
		if err := execute(args, emptyEnvironment, &output); err == nil || output.Len() != 0 {
			t.Fatal("invalid backup arguments accepted")
		}
	}
	var output bytes.Buffer
	if err := execute([]string{"--sqlite-backup", source, filepath.Join(root, "ambiguous")}, func(string) string { return "private-pg-url" }, &output); err == nil || output.Len() != 0 {
		t.Fatal("ambiguous driver accepted")
	}
}

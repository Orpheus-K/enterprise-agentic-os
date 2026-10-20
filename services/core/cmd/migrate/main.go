package main

import (
	"agentos.local/core/internal/buildinfo"
	"agentos.local/core/internal/sqlitedb"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"agentos.local/core/internal/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	return execute(os.Args[1:], os.Getenv, os.Stdout)
}

func execute(args []string, getenv func(string) string, output io.Writer) (err error) {
	if len(args) == 1 && args[0] == "--version" {
		return buildinfo.Write(output, "agentos-migrate")
	}
	sqlitePath := ""
	action, target := "", ""
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "--sqlite" && args[1] != "":
		sqlitePath = args[1]
	case len(args) == 3 && (args[0] == "--sqlite-backup" || args[0] == "--sqlite-restore") && args[1] != "" && args[2] != "":
		action, sqlitePath, target = args[0], args[1], args[2]
	default:
		return errors.New("migrate accepts --version, --sqlite <absolute path>, --sqlite-backup <source file> <new directory>, --sqlite-restore <backup directory> <new file>, or no arguments")
	}
	migrationURL := getenv("AGENTOS_MIGRATION_DATABASE_URL")
	if sqlitePath != "" && migrationURL != "" {
		return errors.New("--sqlite conflicts with AGENTOS_MIGRATION_DATABASE_URL; select one migration target")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if action != "" {
		if action == "--sqlite-backup" {
			err = sqlitedb.Backup(ctx, sqlitePath, target)
		} else {
			err = sqlitedb.Restore(ctx, sqlitePath, target)
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "SQLite backup/restore verified")
		return err
	}
	if sqlitePath != "" {
		if err := sqlitedb.Migrate(ctx, sqlitePath); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "SQLite schema %d ready\n", sqlitedb.SchemaVersion)
		return err
	}
	migrator, err := database.NewMigrator(ctx, migrationURL)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, migrator.Close()) }()
	count, err := migrator.Up(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Applied %d migrations\n", count)
	return err
}

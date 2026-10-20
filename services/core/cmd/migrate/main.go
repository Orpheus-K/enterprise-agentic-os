package main

import (
	"agentos.local/core/internal/buildinfo"
	"context"
	"errors"
	"fmt"
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

func run() (err error) {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		return buildinfo.Write(os.Stdout, "agentos-migrate")
	}
	if len(os.Args) != 1 {
		return errors.New("migrate accepts only --version or no arguments; only forward migrations are supported")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	migrator, err := database.NewMigrator(ctx, os.Getenv("AGENTOS_MIGRATION_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, migrator.Close()) }()
	count, err := migrator.Up(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Applied %d migrations\n", count)
	return nil
}

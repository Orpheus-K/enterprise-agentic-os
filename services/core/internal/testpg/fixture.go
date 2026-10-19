//go:build integration

// Package testpg provisions disposable databases on an explicitly supplied test cluster.
package testpg

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Fixture struct {
	Admin        *pgx.Conn
	AdminURL     string
	MigrationURL string
	RuntimeURL   string
}

func New(t *testing.T) *Fixture {
	t.Helper()
	adminURL := os.Getenv("AGENTOS_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Fatal("AGENTOS_TEST_ADMIN_DATABASE_URL is required; integration tests never skip")
	}
	base, err := url.Parse(adminURL)
	if err != nil || (base.Scheme != "postgres" && base.Scheme != "postgresql") {
		t.Fatal("test administrator must use a PostgreSQL connection URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	dbName, migratorName, runtimeName := "agentos_test_"+suffix, "m_"+suffix, "r_"+suffix
	migrationPassword, runtimePassword := uuid.NewString(), uuid.NewString()
	var databaseAdmin *pgx.Conn
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if databaseAdmin != nil {
			if err := databaseAdmin.Close(cleanup); err != nil {
				t.Error(err)
			}
		}
		for _, statement := range []string{
			"DROP DATABASE IF EXISTS " + pgx.Identifier{dbName}.Sanitize() + " WITH (FORCE)",
			"DROP ROLE IF EXISTS " + pgx.Identifier{runtimeName}.Sanitize(),
			"DROP ROLE IF EXISTS " + pgx.Identifier{migratorName}.Sanitize(),
		} {
			if _, err := admin.Exec(cleanup, statement); err != nil {
				t.Error(err)
			}
		}
		if err := admin.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	// Passwords are generated UUIDs, never inputs or log output. Role names are quoted identifiers.
	for _, statement := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE agentos_migrator", pgx.Identifier{migratorName}.Sanitize(), migrationPassword),
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE agentos_runtime", pgx.Identifier{runtimeName}.Sanitize(), runtimePassword),
		"CREATE DATABASE " + pgx.Identifier{dbName}.Sanitize() + " OWNER agentos_migrator",
		"REVOKE ALL ON DATABASE " + pgx.Identifier{dbName}.Sanitize() + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + pgx.Identifier{dbName}.Sanitize() + " TO agentos_runtime, agentos_migrator",
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	base.Path = "/" + dbName
	databaseAdmin, err = pgx.Connect(ctx, base.String())
	if err != nil {
		t.Fatal(err)
	}
	// Mirrors deploy/postgres/vector.sql: the administrator, not the migrator, owns the extension.
	if _, err := databaseAdmin.Exec(ctx, "CREATE EXTENSION vector WITH SCHEMA public VERSION '0.8.7'"); err != nil {
		t.Fatal(err)
	}
	fixture := &Fixture{Admin: databaseAdmin, AdminURL: base.String()}
	base.User = url.UserPassword(migratorName, migrationPassword)
	fixture.MigrationURL = base.String()
	base.User = url.UserPassword(runtimeName, runtimePassword)
	fixture.RuntimeURL = base.String()
	return fixture
}

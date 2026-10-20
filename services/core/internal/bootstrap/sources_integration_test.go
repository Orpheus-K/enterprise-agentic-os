//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/testpg"
	"github.com/google/uuid"
)

func TestOpenSourcesValidatesWithoutScheduling(t *testing.T) {
	pg := testpg.New(t)
	m, err := database.NewMigrator(t.Context(), pg.MigrationURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Up(t.Context())
	if err = errors.Join(err, m.Close()); err != nil {
		t.Fatal(err)
	}
	c := sourceTestConfig()
	c.Mode = "webhook"
	c.VerificationTokenEnv, c.EncryptKeyEnv = "SOURCE_VERIFY", "SOURCE_ENCRYPT"
	member := uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pg.Admin.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO iam.tenants VALUES($1,'synthetic',true)", c.Binding.TenantID)
	exec("INSERT INTO iam.members VALUES($1,$2,'sync',true)", c.Binding.TenantID, member)
	exec("INSERT INTO iam.identity_links VALUES($1,'feishu',$2,'ou_sync',$3,true)", c.Binding.TenantID, c.Binding.Issuer(), member)
	exec("INSERT INTO knowledge.sources VALUES($1,'cli_test',$2,$3,true)", c.Binding.TenantID, c.Sources[0].SourceID, member)
	exec("INSERT INTO knowledge.embedding_spaces VALUES($1,'cli_test',$2,'embed','v1',3,true)", c.Binding.TenantID, c.Sources[0].Space.ID)
	for name, value := range map[string]string{"SOURCE_DB": pg.RuntimeURL, "SOURCE_APP": "synthetic", "SOURCE_OWNER": "synthetic", "SOURCE_READER": "synthetic", "SOURCE_VERIFY": "synthetic", "SOURCE_ENCRYPT": "synthetic", "SOURCE_OPS": "synthetic-operator-token-at-least-32-bytes"} {
		t.Setenv(name, value)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	raw, _ := json.Marshal(c)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	channel, err := Open(t.Context(), path, logger)
	if err != nil {
		t.Fatal(err)
	}
	if len(channel.sources) != 1 {
		t.Fatal("source runner missing")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := channel.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	channel.Close()
	var count int
	if err := pg.Admin.QueryRow(t.Context(), "SELECT count(*) FROM knowledge.sync_jobs").Scan(&count); err != nil || count != 0 {
		t.Fatal("open/cancel scheduled source work", count, err)
	}
	exec("UPDATE knowledge.embedding_spaces SET enabled=false")
	if channel, err := Open(t.Context(), path, logger); err == nil {
		channel.Close()
		t.Fatal("disabled space accepted")
	}
	exec("UPDATE knowledge.embedding_spaces SET enabled=true")
	exec("UPDATE knowledge.sources SET enabled=false")
	if channel, err := Open(t.Context(), path, logger); err == nil {
		channel.Close()
		t.Fatal("disabled source accepted")
	}
}

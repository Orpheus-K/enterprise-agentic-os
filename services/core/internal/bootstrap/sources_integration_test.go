//go:build integration

package bootstrap

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/feishudocs"
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
	c.Channel = testFeishu("webhook", "SOURCE_APP", "SOURCE_VERIFY", "SOURCE_ENCRYPT")
	member := uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pg.Admin.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO iam.tenants VALUES($1,'synthetic',true)", c.TenantID)
	exec("INSERT INTO iam.members VALUES($1,$2,'sync',true)", c.TenantID, member)
	exec("INSERT INTO iam.identity_links VALUES($1,'feishu',$2,'ou_sync',$3,true)", c.TenantID, c.feishuBinding().Issuer(), member)
	exec("INSERT INTO knowledge.sources VALUES($1,'cli_test',$2,$3,true)", c.TenantID, c.Sources[0].SourceID, member)
	exec("INSERT INTO knowledge.embedding_spaces VALUES($1,'cli_test',$2,'embed','v1',3,true)", c.TenantID, c.Sources[0].Space.ID)
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
	exec("UPDATE knowledge.sources SET enabled=true")
	c.OAuth = &OAuthConfig{ActiveKeyID: "v1", KeyEnvs: map[string]string{"v1": "SOURCE_OAUTH_KEY"}}
	c.Sources[0].Owner = SourceUser{OpenID: "ou_sync", UseOAuth: true}
	t.Setenv("SOURCE_OAUTH_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	raw, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if channel, err := Open(t.Context(), path, logger); !errors.Is(err, feishudocs.ErrOAuthMissing) {
		if channel != nil {
			channel.Close()
		}
		t.Fatal("missing OAuth authorization did not fail startup", err)
	}
	var output bytes.Buffer
	begin := `{"action":"begin","open_id":"ou_sync","redirect_uri":"https://example.test/callback","scope":"offline_access"}`
	if err := RunOAuth(t.Context(), path, strings.NewReader(begin), &output); err != nil {
		t.Fatal("local OAuth begin", err)
	}
	if !strings.Contains(output.String(), "authorization_url") {
		t.Fatal("authorization URL missing")
	}
	output.Reset()
	if err := RunOAuth(t.Context(), path, strings.NewReader(`{"action":"status","open_id":"ou_sync"}`), &output); err != nil || !strings.Contains(output.String(), `"stage":"pending"`) {
		t.Fatal("persisted status", err)
	}
	output.Reset()
	if err := RunOAuth(t.Context(), path, strings.NewReader(`{"action":"complete","open_id":"ou_sync","state":"wrong-state","code":"synthetic-code"}`), &output); !errors.Is(err, feishudocs.ErrOAuthInvalid) || output.Len() != 0 {
		t.Fatal("wrong callback accepted", err)
	}
	if err := RunOAuth(t.Context(), path, strings.NewReader(`{"action":"status","open_id":"ou_unconfigured"}`), &output); err == nil {
		t.Fatal("unconfigured user accepted")
	}
	if err := RunOAuth(t.Context(), path, strings.NewReader(`{"action":"revoke","open_id":"ou_sync"}`), &output); err != nil {
		t.Fatal("pending session revoke", err)
	}
	if channel, err := Open(t.Context(), path, logger); !errors.Is(err, feishudocs.ErrOAuthRevoked) {
		if channel != nil {
			channel.Close()
		}
		t.Fatal("command revocation not consumed", err)
	}
}

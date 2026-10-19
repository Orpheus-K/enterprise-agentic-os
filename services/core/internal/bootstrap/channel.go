// Package bootstrap composes the optional installation-scoped M1 channel chain.
package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/health"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/operations"
	"agentos.local/core/internal/telemetry"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	Binding              feishu.Binding    `json:"binding"`
	Mode                 string            `json:"mode"`
	DatabaseURLEnv       string            `json:"database_url_env"`
	AppSecretEnv         string            `json:"app_secret_env"`
	VerificationTokenEnv string            `json:"verification_token_env"`
	EncryptKeyEnv        string            `json:"encrypt_key_env"`
	Model                model.Config      `json:"model"`
	Operations           *OperationsConfig `json:"operations,omitempty"`
}
type OperationsConfig struct {
	TokenEnv string `json:"token_env"`
	Subject  string `json:"subject"`
}

func Parse(raw []byte) (Config, error) {
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, errors.New("invalid channel configuration")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return c, errors.New("unexpected trailing configuration")
	}
	if err := c.Binding.Validate(); err != nil {
		return c, err
	}
	if err := c.Model.Validate(); err != nil {
		return c, err
	}
	if c.Mode != "websocket" && c.Mode != "webhook" {
		return c, errors.New("channel mode must be websocket or webhook")
	}
	if c.DatabaseURLEnv == "" || c.AppSecretEnv == "" {
		return c, errors.New("database and app-secret environment references required")
	}
	if c.Mode == "webhook" && (c.VerificationTokenEnv == "" || c.EncryptKeyEnv == "") {
		return c, errors.New("webhook authentication references required")
	}
	if c.Operations != nil {
		if c.Operations.TokenEnv == "" {
			return c, errors.New("operations token environment reference required")
		}
		if err := c.operator().Validate(); err != nil {
			return c, err
		}
	}
	return c, nil
}
func (c Config) operator() identity.ExternalIdentity {
	return identity.ExternalIdentity{TenantID: c.Binding.TenantID, Provider: "operator", Issuer: "agentos:ops:" + c.Binding.AppID, Subject: c.Operations.Subject}
}

func secret(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", errors.New("required channel secret environment variable is missing")
	}
	return value, nil
}

type Channel struct {
	db              *database.DB
	adapter         *feishu.Adapter
	worker          *conversation.Worker
	mode, appSecret string
	logger          *slog.Logger
	Handler         http.Handler
	Operations      http.Handler
	Readiness       *health.Readiness
	telemetry       *telemetry.Telemetry
}

func Open(ctx context.Context, path string, logger *slog.Logger) (*Channel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read channel configuration")
	}
	c, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	dbURL, err := secret(c.DatabaseURLEnv)
	if err != nil {
		return nil, err
	}
	appSecret, err := secret(c.AppSecretEnv)
	if err != nil {
		return nil, err
	}
	var token, key, modelKey string
	if c.Mode == "webhook" {
		token, err = secret(c.VerificationTokenEnv)
		if err != nil {
			return nil, err
		}
		key, err = secret(c.EncryptKeyEnv)
		if err != nil {
			return nil, err
		}
	}
	if c.Model.APIKeyEnv != "" {
		modelKey, err = secret(c.Model.APIKeyEnv)
		if err != nil {
			return nil, err
		}
	}
	modelClient, err := model.New(c.Model, modelKey)
	if err != nil {
		return nil, err
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := database.Open(openCtx, dbURL, 4)
	if err != nil {
		return nil, errors.New("channel database startup failed")
	}
	tel, err := telemetry.New(logger)
	if err != nil {
		db.Close()
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			db.Close()
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tel.Close(cleanup); err != nil {
				logger.Error("telemetry shutdown failed")
			}
		}
	}()
	// Bind queued work to installation and approved model configuration, independent of transport mode.
	revisionData, _ := json.Marshal(struct {
		Binding feishu.Binding
		Model   model.Config
	}{c.Binding, c.Model})
	hash := sha256.Sum256(revisionData)
	store := conversation.NewStore(db, c.Binding.TenantID, c.Binding.AppID, hex.EncodeToString(hash[:]))
	adapter, err := feishu.New(c.Binding, token, key, store, tel)
	if err != nil {
		return nil, err
	}
	channel := &Channel{db: db, adapter: adapter, mode: c.Mode, appSecret: appSecret, logger: logger, telemetry: tel, Readiness: health.NewReadiness(store.Ready)}
	channel.worker = &conversation.Worker{Store: store, Model: modelClient, Sender: feishu.NewSender(c.Binding.AppID, appSecret, logger), Logger: logger, Telemetry: tel}
	if c.Mode == "webhook" {
		channel.Handler, err = adapter.Webhook()
		if err != nil {
			return nil, err
		}
	}
	if c.Operations != nil {
		token, err := secret(c.Operations.TokenEnv)
		if err != nil {
			return nil, err
		}
		channel.Operations, err = operations.New(token, c.operator(), store, channel.Readiness, tel)
		if err != nil {
			return nil, err
		}
	}
	opened = true
	return channel, nil
}
func (c *Channel) Close() {
	c.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.telemetry.Close(ctx); err != nil {
		c.logger.Error("telemetry shutdown failed")
	}
}
func (c *Channel) Run(ctx context.Context) error {
	defer c.Readiness.SetTransport(health.Stopped)
	if c.mode == "webhook" {
		c.Readiness.SetTransport(health.Listening)
	}
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		c.Readiness.SetWorker(true)
		defer c.Readiness.SetWorker(false)
		return c.worker.Run(ctx)
	})
	if c.mode == "websocket" {
		group.Go(func() error { return c.adapter.LongConnection(ctx, c.appSecret, c.logger, c.Readiness.SetTransport) })
	}
	return group.Wait()
}

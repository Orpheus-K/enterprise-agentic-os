// Package bootstrap composes the optional installation-scoped M1 channel chain.
package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"agentos.local/core/internal/conversation"
	conversationsqlite "agentos.local/core/internal/conversation/sqlite"
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/health"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/operations"
	"agentos.local/core/internal/quotation"
	"agentos.local/core/internal/sqlitedb"
	"agentos.local/core/internal/telemetry"
	"agentos.local/core/internal/toolgateway"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	StorageDriver       string                        `json:"storage_driver,omitempty"`
	SQLitePath          string                        `json:"sqlite_path,omitempty"`
	QuotationWASMSHA256 string                        `json:"quotation_wasm_sha256,omitempty"`
	Tools               *ToolsConfig                  `json:"tools,omitempty"`
	Skills              *SkillsConfig                 `json:"skills,omitempty"`
	Approval            *conversation.ApprovalPolicy  `json:"approval,omitempty"`
	TenantID            uuid.UUID                     `json:"tenant_id"`
	AppID               string                        `json:"app_id"`
	Channel             ChannelConfig                 `json:"channel"`
	DatabaseURLEnv      string                        `json:"database_url_env"`
	Model               model.Config                  `json:"model"`
	Embedding           *model.EmbeddingConfig        `json:"embedding,omitempty"`
	Retrieval           *conversation.RetrievalPolicy `json:"retrieval,omitempty"`
	Operations          *OperationsConfig             `json:"operations,omitempty"`
	Sources             []SourceConfig                `json:"sources,omitempty"`
	OAuth               *OAuthConfig                  `json:"oauth,omitempty"`
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
	switch c.StorageDriver {
	case "", "postgres":
		if c.SQLitePath != "" || c.DatabaseURLEnv == "" {
			return c, errors.New("PostgreSQL requires database_url_env and forbids sqlite_path")
		}
	case "sqlite":
		if !filepath.IsAbs(c.SQLitePath) || c.DatabaseURLEnv != "" {
			return c, errors.New("SQLite requires an absolute sqlite_path and forbids database_url_env")
		}
		if c.Embedding != nil || c.Retrieval != nil || len(c.Sources) != 0 || c.OAuth != nil || c.Skills != nil || c.Tools != nil || c.QuotationWASMSHA256 != "" || (c.Approval != nil && c.Approval.NotifySubject != "") {
			return c, errors.New("SQLite does not support knowledge, vectors, skills, tools, quotations or notifications")
		}
	default:
		return c, errors.New("unsupported storage_driver")
	}
	if err := c.validateChannel(); err != nil {
		return c, err
	}
	if err := c.Model.Validate(); err != nil {
		return c, err
	}
	if c.QuotationWASMSHA256 != "" && c.QuotationWASMSHA256 != quotation.ApprovalModuleSHA256 {
		return c, errors.New("unsupported quotation WASM digest")
	}
	if c.Embedding != nil {
		if err := c.Embedding.Validate(); err != nil {
			return c, err
		}
	}
	if c.Retrieval != nil {
		if err := c.Retrieval.Validate(); err != nil {
			return c, err
		}
		if c.Embedding == nil || c.Retrieval.Space.Model != c.Embedding.Name || c.Retrieval.Space.ModelRevision != c.Embedding.Revision {
			return c, errors.New("retrieval requires matching approved embedding configuration")
		}
	}
	if c.Operations != nil {
		if c.Operations.TokenEnv == "" {
			return c, errors.New("operations token environment reference required")
		}
		if err := c.operator().Validate(); err != nil {
			return c, err
		}
	}
	if c.Approval != nil {
		if c.Operations == nil {
			return c, errors.New("approval requires operations endpoint")
		}
		if err := c.Approval.Validate(); err != nil {
			return c, err
		}
	}
	if c.Tools != nil && c.Skills == nil {
		return c, errors.New("tools require configured skills")
	}
	if err := c.validateSources(); err != nil {
		return c, err
	}
	if c.Skills != nil {
		if err := c.Skills.Validate(); err != nil {
			return c, err
		}
	}
	return c, nil
}
func (c Config) operator() identity.ExternalIdentity {
	return identity.ExternalIdentity{TenantID: c.TenantID, Provider: "operator", Issuer: "agentos:ops:" + c.AppID, Subject: c.Operations.Subject}
}

func secret(name string) (string, error) {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", errors.New("required channel secret environment variable is missing")
	}
	return value, nil
}

type Channel struct {
	closeStorage func()
	transport    transport
	worker       *conversation.Worker
	logger       *slog.Logger
	// Provider names the channel for routing; Handler is nil unless it uses webhook ingress.
	Provider   string
	Handler    http.Handler
	Operations http.Handler
	Readiness  *health.Readiness
	telemetry  *telemetry.Telemetry
	sources    []sourceRunner
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
	channelSecrets, err := c.Channel.Feishu.loadSecrets()
	if err != nil {
		return nil, err
	}
	appSecret := channelSecrets.appSecret
	var modelKey string
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
	embedder, err := openEmbedder(c.Embedding)
	if err != nil {
		return nil, err
	}
	preparedSources, err := c.sourceCredentials()
	if err != nil {
		return nil, err
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var db *database.DB
	var store channelStore
	var closeStorage func()
	if c.StorageDriver == "sqlite" {
		sqliteDB, openErr := sqlitedb.Open(openCtx, c.SQLitePath)
		if openErr != nil {
			return nil, errors.New("channel SQLite database startup failed; require an existing migrated file")
		}
		closeStorage = func() {
			if err := sqliteDB.Close(); err != nil {
				logger.Error("SQLite shutdown failed")
			}
		}
		sqliteStore := conversationsqlite.NewInbox(sqliteDB, c.TenantID, c.AppID, c.executionRevision())
		if c.Approval != nil {
			sqliteStore, err = sqliteStore.WithApproval(*c.Approval)
			if err != nil {
				closeStorage()
				return nil, err
			}
		}
		store = sqliteStore
	} else {
		dbURL, secretErr := secret(c.DatabaseURLEnv)
		if secretErr != nil {
			return nil, secretErr
		}
		db, err = database.Open(openCtx, dbURL, 4)
		if err != nil {
			return nil, errors.New("channel database startup failed")
		}
		closeStorage = db.Close
	}
	tel, err := telemetry.New(logger)
	if err != nil {
		closeStorage()
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			closeStorage()
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tel.Close(cleanup); err != nil {
				logger.Error("telemetry shutdown failed")
			}
		}
	}()
	// Bind queued work to installation and approved model configuration, independent of transport mode.
	var gateway *toolgateway.Gateway
	if db != nil {
		pgStore := conversation.NewStore(db, c.TenantID, c.AppID, c.executionRevision())
		if c.QuotationWASMSHA256 != "" {
			pgStore, err = pgStore.WithQuotationSandbox(c.QuotationWASMSHA256)
			if err != nil {
				return nil, err
			}
		}
		if c.Retrieval != nil {
			pgStore, err = pgStore.WithRetrieval(*c.Retrieval, embedder)
			if err != nil {
				return nil, err
			}
			k, err := knowledge.New(db, c.TenantID, c.AppID)
			if err != nil {
				return nil, err
			}
			if err := db.Transaction(ctx, func(q database.Queries) error {
				return k.CheckSpaceIn(ctx, q, c.Retrieval.Space, c.Embedding.Dimensions)
			}); err != nil {
				return nil, errors.New("retrieval embedding space unavailable or inconsistent")
			}
		}
		if c.Approval != nil {
			pgStore, err = pgStore.WithApproval(*c.Approval)
			if err != nil {
				return nil, err
			}
		}
		if c.Skills != nil {
			registry, loadErr := c.Skills.Load()
			if loadErr != nil {
				return nil, loadErr
			}
			if c.Tools != nil {
				credentials, credentialErr := toolgateway.NewCredentials(c.Tools.Credentials)
				if credentialErr != nil {
					return nil, credentialErr
				}
				gateway, err = toolgateway.New(db, c.TenantID, c.AppID, registry, c.Tools.Specs, credentials)
				if err != nil {
					return nil, err
				}
			}
			if len(c.Skills.Routes) > 0 {
				pgStore, err = pgStore.WithRoutes(registry, c.Skills.Routes)
			} else {
				pgStore, err = pgStore.WithSkills(registry, c.Skills.Selected)
			}
			if err != nil {
				return nil, err
			}
		}
		store = pgStore
	}
	ingress, err := newFeishuTransport(c, channelSecrets, store, tel, logger)
	if err != nil {
		return nil, err
	}
	channel := &Channel{closeStorage: closeStorage, transport: ingress, Provider: ingress.Provider(), Handler: ingress.Handler(), logger: logger, telemetry: tel, Readiness: health.NewReadiness(store.Ready)}
	if db != nil {
		channel.sources, err = c.openSources(ctx, db, preparedSources, appSecret)
		if err != nil {
			return nil, err
		}
	}
	channel.worker = &conversation.Worker{Store: store, Tools: gateway, Model: modelClient, Sender: ingress, Logger: logger, Telemetry: tel}
	if db != nil {
		channel.worker.Notifier = ingress
	}
	if c.Operations != nil {
		token, err := secret(c.Operations.TokenEnv)
		if err != nil {
			return nil, err
		}
		operationsAPI, err := operations.New(token, c.operator(), store, channel.Readiness, tel)
		if err != nil {
			return nil, err
		}
		channel.Operations = operationsAPI
		if db != nil {
			knowledgeStore, err := knowledge.New(db, c.TenantID, c.AppID)
			if err != nil {
				return nil, err
			}
			channel.Operations = operationsAPI.WithKnowledge(knowledgeStore)
		}
	}
	opened = true
	return channel, nil
}
func (c *Channel) Close() {
	c.closeStorage()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.telemetry.Close(ctx); err != nil {
		c.logger.Error("telemetry shutdown failed")
	}
}
func (c *Channel) Run(ctx context.Context) error {
	defer c.Readiness.SetTransport(health.Stopped)
	group, ctx := errgroup.WithContext(ctx)
	for _, runner := range c.sources {
		group.Go(func() error { return c.runSource(ctx, runner) })
	}
	group.Go(func() error {
		c.Readiness.SetWorker(true)
		defer c.Readiness.SetWorker(false)
		return c.worker.Run(ctx)
	})
	group.Go(func() error { return c.transport.Run(ctx, c.Readiness.SetTransport) })
	return group.Wait()
}

type ToolsConfig struct {
	Specs       []toolgateway.Spec              `json:"specs"`
	Credentials []toolgateway.CredentialBinding `json:"credentials"`
}

type channelStore interface {
	conversation.ExecutionStore
	operations.Store
	Enqueue(context.Context, conversation.Message) error
	Ready(context.Context) error
}

package bootstrap

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	"agentos.local/core/internal/database"
	"agentos.local/core/internal/feishudocs"
	"agentos.local/core/internal/identity"
	identitypg "agentos.local/core/internal/identity/postgres"
	"agentos.local/core/internal/knowledge"
	"github.com/google/uuid"
)

type SourceUser struct {
	OpenID         string `json:"open_id"`
	AccessTokenEnv string `json:"access_token_env"`
	UseOAuth       bool   `json:"use_oauth,omitempty"`
}

type OAuthConfig struct {
	ActiveKeyID string            `json:"active_key_id"`
	KeyEnvs     map[string]string `json:"key_envs"`
}

type SourceConfig struct {
	SourceID    uuid.UUID       `json:"source_id"`
	Documents   []string        `json:"documents"`
	Owner       SourceUser      `json:"owner"`
	Recipients  []SourceUser    `json:"recipients"`
	Space       knowledge.Space `json:"space"`
	PollSeconds int             `json:"poll_seconds"`
}

var sourceIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var secretReference = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func (c Config) validateSources() error {
	if c.OAuth != nil {
		if len(c.Sources) == 0 || !sourceIdentifier.MatchString(c.OAuth.ActiveKeyID) || len(c.OAuth.KeyEnvs) == 0 || len(c.OAuth.KeyEnvs) > 16 || c.OAuth.KeyEnvs[c.OAuth.ActiveKeyID] == "" {
			return errors.New("invalid source OAuth key references")
		}
		for id, env := range c.OAuth.KeyEnvs {
			if !sourceIdentifier.MatchString(id) || !secretReference.MatchString(env) {
				return errors.New("invalid source OAuth key reference")
			}
		}
	}
	if len(c.Sources) == 0 {
		return nil
	}
	if c.Embedding == nil || c.Operations == nil || len(c.Sources) > 32 {
		return errors.New("sources require embedding and operations configuration; maximum 32 sources")
	}
	seen := map[uuid.UUID]bool{}
	for _, source := range c.Sources {
		if source.SourceID == uuid.Nil || seen[source.SourceID] || source.Space.ID == uuid.Nil ||
			source.Space.Model != c.Embedding.Name || source.Space.ModelRevision != c.Embedding.Revision ||
			len(source.Documents) == 0 || len(source.Documents) > 1000 ||
			source.PollSeconds < 5 || source.PollSeconds > 30 || len(source.Recipients) > 1000 {
			return errors.New("invalid source configuration")
		}
		seen[source.SourceID] = true
		documents := map[string]bool{}
		for _, document := range source.Documents {
			if !sourceIdentifier.MatchString(document) || documents[document] {
				return errors.New("source documents must be unique native IDs")
			}
			documents[document] = true
		}
		users := map[string]bool{}
		for _, user := range source.Recipients {
			if users[user.OpenID] {
				return errors.New("duplicate source recipient")
			}
			users[user.OpenID] = true
		}
		for _, user := range append([]SourceUser{source.Owner}, source.Recipients...) {
			if user.UseOAuth && (c.OAuth == nil || user.AccessTokenEnv != "") {
				return errors.New("OAuth source requires key configuration and forbids static token")
			}
			if !user.UseOAuth && !secretReference.MatchString(user.AccessTokenEnv) {
				return errors.New("source credential environment reference required")
			}
			if _, err := feishudocs.NewCredential(feishudocs.User{AppID: c.Binding.AppID, TenantKey: c.Binding.TenantKey, OpenID: user.OpenID}, "validation-only"); err != nil {
				return errors.New("invalid source user binding")
			}
		}
	}
	return nil
}

type preparedSource struct {
	config     SourceConfig
	owner      feishudocs.Credential
	recipients []feishudocs.Credential
	key        string
	oauthKeys  *feishudocs.OAuthKeys
}

func (c Config) loadOAuthKeys() (*feishudocs.OAuthKeys, error) {
	var keys *feishudocs.OAuthKeys
	if c.OAuth != nil {
		material := map[string][]byte{}
		defer func() {
			for _, key := range material {
				clear(key)
			}
		}()
		for id, env := range c.OAuth.KeyEnvs {
			encoded, err := secret(env)
			if err != nil {
				return nil, err
			}
			key, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil || len(key) != 32 {
				return nil, errors.New("OAuth key must be base64-encoded 32 bytes")
			}
			material[id] = key
		}
		var err error
		keys, err = feishudocs.NewOAuthKeys(c.OAuth.ActiveKeyID, material)
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func (c Config) sourceCredentials() ([]preparedSource, error) {
	var out []preparedSource
	keys, err := c.loadOAuthKeys()
	if err != nil {
		return nil, err
	}
	for _, config := range c.Sources {
		credential := func(user SourceUser) (feishudocs.Credential, error) {
			if user.UseOAuth {
				return feishudocs.Credential{}, nil
			} // Bound to the database in openSources.
			token, err := secret(user.AccessTokenEnv)
			if err != nil {
				return feishudocs.Credential{}, err
			}
			return feishudocs.NewCredential(feishudocs.User{AppID: c.Binding.AppID, TenantKey: c.Binding.TenantKey, OpenID: user.OpenID}, token)
		}
		source := preparedSource{config: config, oauthKeys: keys}
		var err error
		source.owner, err = credential(config.Owner)
		if err != nil {
			return nil, err
		}
		for _, user := range config.Recipients {
			value, err := credential(user)
			if err != nil {
				return nil, err
			}
			source.recipients = append(source.recipients, value)
		}
		if c.Embedding.APIKeyEnv != "" {
			source.key, err = secret(c.Embedding.APIKeyEnv)
			if err != nil {
				return nil, err
			}
		}
		out = append(out, source)
	}
	return out, nil
}

type sourceRunner struct {
	syncer *feishudocs.Syncer
	store  *knowledge.Store
	actor  identity.ExternalIdentity
	config SourceConfig
}

func (c Config) openSources(ctx context.Context, db *database.DB, prepared []preparedSource, appSecret string) ([]sourceRunner, error) {
	var out []sourceRunner
	for _, source := range prepared {
		credential := func(user SourceUser, static feishudocs.Credential) (feishudocs.Credential, error) {
			if !user.UseOAuth {
				return static, nil
			}
			oauth, err := feishudocs.NewOAuth(db, c.Binding.TenantID, feishudocs.User{AppID: c.Binding.AppID, TenantKey: c.Binding.TenantKey, OpenID: user.OpenID}, appSecret, source.oauthKeys)
			if err != nil {
				return feishudocs.Credential{}, err
			}
			if err = oauth.CheckReady(ctx); err != nil {
				return feishudocs.Credential{}, err
			}
			return oauth.Credential(), nil
		}
		var err error
		source.owner, err = credential(source.config.Owner, source.owner)
		if err != nil {
			return nil, err
		}
		for i, user := range source.config.Recipients {
			source.recipients[i], err = credential(user, source.recipients[i])
			if err != nil {
				return nil, err
			}
		}
		store, err := knowledge.New(db, c.Binding.TenantID, c.Binding.AppID)
		if err != nil {
			return nil, err
		}
		actor := identity.ExternalIdentity{TenantID: c.Binding.TenantID, Provider: "feishu", Issuer: c.Binding.Issuer(), Subject: source.config.Owner.OpenID}
		if err := store.CheckSyncSource(ctx, actor, source.config.SourceID); err != nil {
			return nil, errors.New("source or synchronization identity unavailable")
		}
		if err := db.Transaction(ctx, func(q database.Queries) error {
			return store.CheckSpaceIn(ctx, q, source.config.Space, c.Embedding.Dimensions)
		}); err != nil {
			return nil, errors.New("source embedding space unavailable or inconsistent")
		}
		reader, err := feishudocs.New(c.Binding.AppID)
		if err != nil {
			return nil, err
		}
		mapped, err := feishudocs.NewMappedReader(reader, identitypg.New(db), c.Binding.TenantID, c.Binding.TenantKey)
		if err != nil {
			return nil, err
		}
		syncer, err := feishudocs.NewSyncer(mapped, store, source.config.SourceID, source.owner, source.recipients, *c.Embedding, source.key, source.config.Space)
		if err != nil {
			return nil, err
		}
		syncer, err = syncer.WithDocuments(source.config.Documents)
		if err != nil {
			return nil, err
		}
		out = append(out, sourceRunner{syncer: syncer, store: store, actor: actor, config: source.config})
	}
	return out, nil
}

func (r sourceRunner) cycle(ctx context.Context) error {
	for _, document := range r.config.Documents {
		if err := r.store.PollSync(ctx, r.actor, r.config.SourceID, document, r.syncer.ConfigHash()); err != nil {
			return err
		}
	}
	for i := 0; i < len(r.config.Documents); i++ {
		worked, err := r.syncer.RunOnce(ctx)
		if err != nil {
			return err
		}
		if !worked {
			break
		}
	}
	return nil
}

func (c *Channel) runSource(ctx context.Context, runner sourceRunner) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if err := runner.cycle(ctx); err != nil && ctx.Err() == nil {
				// Provider/SQL error text may contain customer data. Persisted
				// state is authoritative; logs identify only the configured source.
				c.logger.Error("knowledge source sync failed", "source_id", runner.config.SourceID)
			}
			timer.Reset(time.Duration(runner.config.PollSeconds) * time.Second)
		}
	}
}

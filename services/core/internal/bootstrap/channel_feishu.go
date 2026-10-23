package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/feishu"
	"agentos.local/core/internal/health"
	"agentos.local/core/internal/telemetry"
)

// transport is the only surface the runtime needs from an IM channel. A
// channel authenticates ingress and enqueues through the shared store; it never
// owns task or authorization facts. Provider is the identity provider written
// to every task it enqueues.
type transport interface {
	Provider() string
	// Handler serves webhook ingress, or is nil when the channel uses an
	// outbound connection.
	Handler() http.Handler
	// Run blocks until ctx ends or the channel fails. It reports connection
	// state through update.
	Run(ctx context.Context, update func(health.Transport)) error
	conversation.Sender
}

// ChannelConfig selects one channel implementation by type. Unknown types are
// rejected at parse time; there is no default channel.
type ChannelConfig struct {
	Type   string
	Feishu *FeishuChannel
}

// FeishuChannel holds the Feishu-only settings. Installation tenant and app
// identity stay at the top level of Config because the core scopes by them.
type FeishuChannel struct {
	TenantKey            string `json:"tenant_key"`
	Mode                 string `json:"mode"`
	AppSecretEnv         string `json:"app_secret_env"`
	VerificationTokenEnv string `json:"verification_token_env,omitempty"`
	EncryptKeyEnv        string `json:"encrypt_key_env,omitempty"`
}

func (c ChannelConfig) MarshalJSON() ([]byte, error) {
	if c.Type != "feishu" || c.Feishu == nil {
		return nil, errors.New("unsupported channel type")
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		FeishuChannel
	}{c.Type, *c.Feishu})
}

func (c *ChannelConfig) UnmarshalJSON(raw []byte) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return errors.New("invalid channel configuration")
	}
	if head.Type != "feishu" {
		return errors.New("unsupported channel type")
	}
	var v struct {
		Type string `json:"type"`
		FeishuChannel
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&v); err != nil {
		return errors.New("invalid channel configuration")
	}
	*c = ChannelConfig{Type: v.Type, Feishu: &v.FeishuChannel}
	return nil
}

// feishuBinding is the trusted installation binding. It is only meaningful for
// Feishu-specific code (sources, OAuth, revision); callers outside this
// channel must use TenantID and AppID.
func (c Config) feishuBinding() feishu.Binding {
	binding := feishu.Binding{TenantID: c.TenantID, AppID: c.AppID}
	if c.Channel.Feishu != nil {
		binding.TenantKey = c.Channel.Feishu.TenantKey
	}
	return binding
}

func (c Config) validateChannel() error {
	if c.Channel.Type != "feishu" || c.Channel.Feishu == nil {
		return errors.New("unsupported channel type")
	}
	f := c.Channel.Feishu
	if err := c.feishuBinding().Validate(); err != nil {
		return err
	}
	if f.Mode != "websocket" && f.Mode != "webhook" {
		return errors.New("channel mode must be websocket or webhook")
	}
	if f.AppSecretEnv == "" {
		return errors.New("app-secret environment reference required")
	}
	if f.Mode == "webhook" && (f.VerificationTokenEnv == "" || f.EncryptKeyEnv == "") {
		return errors.New("webhook authentication references required")
	}
	return nil
}

// secretRefs lists the environment references the channel needs, for recovery
// inventory.
func (c ChannelConfig) secretRefs() []string {
	if c.Feishu == nil {
		return nil
	}
	return []string{c.Feishu.AppSecretEnv, c.Feishu.VerificationTokenEnv, c.Feishu.EncryptKeyEnv}
}

type feishuSecrets struct{ appSecret, token, key string }

func (f FeishuChannel) loadSecrets() (feishuSecrets, error) {
	var s feishuSecrets
	var err error
	if s.appSecret, err = secret(f.AppSecretEnv); err != nil {
		return s, err
	}
	if f.Mode == "webhook" {
		if s.token, err = secret(f.VerificationTokenEnv); err != nil {
			return s, err
		}
		if s.key, err = secret(f.EncryptKeyEnv); err != nil {
			return s, err
		}
	}
	return s, nil
}

type feishuTransport struct {
	adapter *feishu.Adapter
	sender  *feishu.Sender
	handler http.Handler
	mode    string
	secret  string
	logger  *slog.Logger
}

func newFeishuTransport(c Config, s feishuSecrets, store channelStore, tel *telemetry.Telemetry, logger *slog.Logger) (*feishuTransport, error) {
	adapter, err := feishu.New(c.feishuBinding(), s.token, s.key, store, tel)
	if err != nil {
		return nil, err
	}
	adapter = adapter.WithApprovals(store)
	t := &feishuTransport{adapter: adapter, sender: feishu.NewSender(c.AppID, s.appSecret, logger), mode: c.Channel.Feishu.Mode, secret: s.appSecret, logger: logger}
	if t.mode == "webhook" {
		if t.handler, err = adapter.Webhook(); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *feishuTransport) Provider() string      { return "feishu" }
func (t *feishuTransport) Handler() http.Handler { return t.handler }

func (t *feishuTransport) Reply(ctx context.Context, message, id, text string) error {
	return t.sender.Reply(ctx, message, id, text)
}

func (t *feishuTransport) Notify(ctx context.Context, recipient, id string, notice conversation.ApprovalNotice) error {
	return t.sender.Notify(ctx, recipient, id, notice)
}

func (t *feishuTransport) Run(ctx context.Context, update func(health.Transport)) error {
	if t.mode == "webhook" {
		// Listening only means the local handler can receive; it does not prove
		// the public endpoint is configured.
		update(health.Listening)
		<-ctx.Done()
		return nil
	}
	return t.adapter.LongConnection(ctx, t.secret, t.logger, update)
}

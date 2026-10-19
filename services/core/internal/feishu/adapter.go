// Package feishu owns transport verification and installation identity binding.
package feishu

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/telemetry"
	"github.com/google/uuid"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
)

type Binding struct {
	TenantID  uuid.UUID `json:"tenant_id"`
	AppID     string    `json:"app_id"`
	TenantKey string    `json:"tenant_key"`
}

func (b Binding) Issuer() string { return "feishu:" + b.AppID + ":" + b.TenantKey }
func (b Binding) Validate() error {
	if b.TenantID == uuid.Nil || b.AppID == "" || b.TenantKey == "" || strings.ContainsAny(b.AppID+b.TenantKey, ": \t\r\n") {
		return errors.New("trusted Feishu installation binding required")
	}
	return nil
}

type Inbox interface {
	Enqueue(context.Context, conversation.Message) error
}
type Adapter struct {
	binding    Binding
	token, key string
	inbox      Inbox
	now        func() time.Time
	telemetry  *telemetry.Telemetry
	approvals  ApprovalDecider
}

func New(b Binding, token, key string, inbox Inbox, tel *telemetry.Telemetry) (*Adapter, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &Adapter{binding: b, token: token, key: key, inbox: inbox, now: time.Now, telemetry: tel}, nil
}

type envelope struct {
	Schema    string                `json:"schema"`
	Type      string                `json:"type"`
	Token     string                `json:"token"`
	Challenge string                `json:"challenge"`
	Header    larkevent.EventHeader `json:"header"`
	Event     struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
			SenderType string `json:"sender_type"`
			TenantKey  string `json:"tenant_key"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			ChatID      string `json:"chat_id"`
			ChatType    string `json:"chat_type"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
		} `json:"message"`
	} `json:"event"`
}

// receive is called only after webhook authentication or by our credential-bound SDK connection.
func (a *Adapter) receive(ctx context.Context, body []byte) error {
	if len(body) > 65536 {
		return errors.New("event too large")
	}
	var e envelope
	if json.Unmarshal(body, &e) != nil {
		return errors.New("invalid Feishu event")
	}
	if e.Schema != "2.0" || e.Header.AppID != a.binding.AppID || e.Header.TenantKey != a.binding.TenantKey || e.Event.Sender.TenantKey != a.binding.TenantKey {
		return errors.New("Feishu installation mismatch")
	}
	if e.Header.EventType != "im.message.receive_v1" || e.Event.Sender.SenderType != "user" || e.Event.Message.ChatType != "p2p" || e.Event.Message.MessageType != "text" {
		return errors.New("unsupported Feishu event; private user text required")
	}
	if e.Header.EventID == "" || e.Event.Message.MessageID == "" || e.Event.Message.ChatID == "" || e.Event.Sender.SenderID.OpenID == "" {
		return errors.New("incomplete Feishu event")
	}
	var content struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(e.Event.Message.Content), &content) != nil || strings.TrimSpace(content.Text) == "" || len(content.Text) > 16000 {
		return errors.New("invalid message text")
	}
	// Ignore sender-supplied employee/tenant fields. The installation owns TenantID and issuer.
	external := identity.ExternalIdentity{TenantID: a.binding.TenantID, Provider: "feishu", Issuer: a.binding.Issuer(), Subject: e.Event.Sender.SenderID.OpenID}
	fingerprintData, _ := json.Marshal([]string{external.Issuer, external.Subject, e.Event.Message.MessageID, e.Event.Message.ChatID, content.Text})
	hash := sha256.Sum256(fingerprintData)
	return a.inbox.Enqueue(ctx, conversation.Message{EventID: e.Header.EventID, MessageID: e.Event.Message.MessageID, Text: content.Text, Fingerprint: hex.EncodeToString(hash[:]), Identity: external})
}

func (a *Adapter) Webhook() (http.Handler, error) {
	if a.token == "" || a.key == "" {
		return nil, errors.New("webhook requires verification token and encrypt key")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, end := a.telemetry.Start(r.Context(), "ingress.webhook")
		success := false
		defer func() { end(success) }()
		r = r.WithContext(ctx)
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		var encrypted struct {
			Encrypt string `json:"encrypt"`
		}
		if json.Unmarshal(body, &encrypted) != nil || encrypted.Encrypt == "" {
			http.Error(w, "encrypted event required", http.StatusUnauthorized)
			return
		}
		plain, err := larkevent.EventDecrypt(encrypted.Encrypt, a.key)
		var e envelope
		if err != nil || json.Unmarshal(plain, &e) != nil {
			http.Error(w, "invalid encrypted event", http.StatusUnauthorized)
			return
		}
		// Feishu URL verification has no signature header; encrypted challenge + token authenticates only configuration.
		if e.Type == "url_verification" {
			if e.Challenge == "" || len(e.Challenge) > 1024 || subtle.ConstantTimeCompare([]byte(e.Token), []byte(a.token)) != 1 {
				http.Error(w, "invalid challenge", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			success = json.NewEncoder(w).Encode(map[string]string{"challenge": e.Challenge}) == nil
			return
		}
		timestamp, nonce := r.Header.Get("X-Lark-Request-Timestamp"), r.Header.Get("X-Lark-Request-Nonce")
		seconds, err := strconv.ParseInt(timestamp, 10, 64)
		now := a.now().Unix()
		if err != nil || seconds < now-300 || seconds > now+300 || nonce == "" || len(nonce) > 256 || subtle.ConstantTimeCompare([]byte(e.Header.Token), []byte(a.token)) != 1 || subtle.ConstantTimeCompare([]byte(larkevent.Signature(timestamp, nonce, a.key, string(body))), []byte(r.Header.Get("X-Lark-Signature"))) != 1 {
			http.Error(w, "invalid event authentication", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if e.Header.EventType == "card.action.trigger" {
			response, err := a.receiveCard(ctx, plain)
			if err != nil {
				http.Error(w, "card action rejected", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			success = json.NewEncoder(w).Encode(response) == nil
			return
		}
		if err := a.receive(ctx, plain); err != nil {
			http.Error(w, "event rejected", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write([]byte(`{"code":0}`))
		success = err == nil
	}), nil
}

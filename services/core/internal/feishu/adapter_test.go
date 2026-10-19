package feishu

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/telemetry"
	"github.com/google/uuid"
)

const testKey = "test-encrypt-key"
const testToken = "test-verification-token"

type recordingInbox struct {
	messages []conversation.Message
	err      error
}

func (i *recordingInbox) Enqueue(_ context.Context, m conversation.Message) error {
	i.messages = append(i.messages, m)
	return i.err
}

func testBinding() Binding {
	return Binding{TenantID: uuid.MustParse("10000000-0000-0000-0000-000000000001"), AppID: "cli_test", TenantKey: "tenant_test"}
}
func eventBody(t *testing.T, mutate func(*envelope)) []byte {
	t.Helper()
	var e envelope
	e.Schema = "2.0"
	e.Header.AppID = "cli_test"
	e.Header.TenantKey = "tenant_test"
	e.Header.EventID = "event_1"
	e.Header.EventType = "im.message.receive_v1"
	e.Header.Token = testToken
	e.Event.Sender.SenderID.OpenID = "ou_user"
	e.Event.Sender.SenderType = "user"
	e.Event.Sender.TenantKey = "tenant_test"
	e.Event.Message.MessageID = "om_message"
	e.Event.Message.ChatID = "oc_private"
	e.Event.Message.ChatType = "p2p"
	e.Event.Message.MessageType = "text"
	e.Event.Message.Content = `{"text":"测试输入"}`
	if mutate != nil {
		mutate(&e)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func encryptedRequest(t *testing.T, plain []byte) *http.Request {
	t.Helper()
	key := sha256.Sum256([]byte(testKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(padding)}, padding)...)
	// Fixed IV is solely a reproducible test fixture; never used for production encryption.
	encrypted := make([]byte, aes.BlockSize+len(padded))
	cipher.NewCBCEncrypter(block, encrypted[:aes.BlockSize]).CryptBlocks(encrypted[aes.BlockSize:], padded)
	body, err := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(encrypted)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/channels/feishu/events", bytes.NewReader(body))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request.Header.Set("X-Lark-Request-Timestamp", timestamp)
	request.Header.Set("X-Lark-Request-Nonce", "test-nonce")
	hash := sha256.Sum256(append([]byte(timestamp+"test-nonce"+testKey), body...))
	request.Header.Set("X-Lark-Signature", hex.EncodeToString(hash[:]))
	return request
}
func testAdapter(t *testing.T, inbox Inbox) *Adapter {
	t.Helper()
	tel, err := telemetry.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	a, err := New(testBinding(), testToken, testKey, inbox, tel)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func webhookStatus(t *testing.T, a *Adapter, r *http.Request) int {
	t.Helper()
	handler, err := a.Webhook()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w.Code
}

func TestWebhookAuthentication(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*http.Request)
		body   func(*envelope)
		want   int
	}{
		{name: "valid", want: 200},
		{name: "untrusted identity headers ignored", modify: func(r *http.Request) {
			r.Header.Set("X-Tenant-ID", uuid.NewString())
			r.Header.Set("X-Employee-ID", "admin")
		}, want: 200},
		{name: "signature", modify: func(r *http.Request) { r.Header.Set("X-Lark-Signature", strings.Repeat("0", 64)) }, want: 401},
		{name: "missing signature", modify: func(r *http.Request) { r.Header.Del("X-Lark-Signature") }, want: 401},
		{name: "expired", modify: func(r *http.Request) { r.Header.Set("X-Lark-Request-Timestamp", "1") }, want: 401},
		{name: "future", modify: func(r *http.Request) {
			r.Header.Set("X-Lark-Request-Timestamp", strconv.FormatInt(time.Now().Unix()+3600, 10))
		}, want: 401},
		{name: "nonce", modify: func(r *http.Request) { r.Header.Del("X-Lark-Request-Nonce") }, want: 401},
		{name: "token", body: func(e *envelope) { e.Header.Token = "other" }, want: 401},
		{name: "app", body: func(e *envelope) { e.Header.AppID = "other" }, want: 503},
		{name: "tenant", body: func(e *envelope) { e.Header.TenantKey = "other" }, want: 503},
		{name: "sender tenant", body: func(e *envelope) { e.Event.Sender.TenantKey = "other" }, want: 503},
		{name: "group", body: func(e *envelope) { e.Event.Message.ChatType = "group" }, want: 503},
		{name: "bot", body: func(e *envelope) { e.Event.Sender.SenderType = "app" }, want: 503},
		{name: "image", body: func(e *envelope) { e.Event.Message.MessageType = "image" }, want: 503},
		{name: "missing sender", body: func(e *envelope) { e.Event.Sender.SenderID.OpenID = "" }, want: 503},
		{name: "malformed", modify: func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"encrypt":"bad"}`)) }, want: 401},
		{name: "oversized", modify: func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 65537))) }, want: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inbox := &recordingInbox{}
			a := testAdapter(t, inbox)
			r := encryptedRequest(t, eventBody(t, tc.body))
			if tc.modify != nil {
				tc.modify(r)
			}
			if tc.name == "expired" || tc.name == "future" {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				hash := sha256.Sum256(append([]byte(r.Header.Get("X-Lark-Request-Timestamp")+r.Header.Get("X-Lark-Request-Nonce")+testKey), body...))
				r.Header.Set("X-Lark-Signature", hex.EncodeToString(hash[:]))
			}
			if got := webhookStatus(t, a, r); got != tc.want {
				t.Fatalf("status=%d want %d", got, tc.want)
			}
			if tc.want != 200 && len(inbox.messages) != 0 {
				t.Fatal("unauthenticated event reached inbox")
			}
			if tc.want == 200 {
				m := inbox.messages[0]
				if m.Identity.TenantID != testBinding().TenantID || m.Identity.Issuer != testBinding().Issuer() || m.Identity.Subject != "ou_user" {
					t.Fatal("identity binding changed")
				}
			}
		})
	}
}

func TestChallengeAndPersistBeforeAck(t *testing.T) {
	inbox := &recordingInbox{}
	a := testAdapter(t, inbox)
	for _, token := range []string{testToken, "wrong"} {
		raw, _ := json.Marshal(map[string]string{"type": "url_verification", "token": token, "challenge": "challenge"})
		r := encryptedRequest(t, raw)
		r.Header = http.Header{}
		w := httptest.NewRecorder()
		h, _ := a.Webhook()
		h.ServeHTTP(w, r)
		if token == testToken {
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"challenge":"challenge"`) {
				t.Fatal("challenge failed")
			}
		} else if w.Code != 401 {
			t.Fatal("invalid challenge accepted")
		}
	}
	if len(inbox.messages) != 0 {
		t.Fatal("challenge dispatched a task")
	}
	inbox.err = errors.New("database unavailable")
	if webhookStatus(t, a, encryptedRequest(t, eventBody(t, nil))) == 200 {
		t.Fatal("acknowledged failed persistence")
	}
}

func TestCredentialBoundSDKDispatch(t *testing.T) {
	inbox := &recordingInbox{}
	a := testAdapter(t, inbox)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := a.dispatcher(logger)
	if _, err := d.Do(context.Background(), eventBody(t, func(e *envelope) { e.Header.Token = "" })); err != nil {
		t.Fatal(err)
	}
	if len(inbox.messages) != 1 {
		t.Fatal("SDK did not dispatch raw event")
	}
	if _, err := d.Do(context.Background(), eventBody(t, func(e *envelope) { e.Header.AppID = "other" })); err == nil {
		t.Fatal("accepted other installation")
	}
}

func TestSDKLogsContainNoPayload(t *testing.T) {
	var output bytes.Buffer
	logger := sdkLogger{slog.New(slog.NewJSONHandler(&output, nil))}
	logger.Error(context.Background(), "secret-prompt-token")
	logger.Warn(context.Background(), "secret-prompt-token")
	if strings.Contains(output.String(), "secret-prompt-token") || output.Len() == 0 {
		t.Fatal("unsafe SDK log")
	}
}

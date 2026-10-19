package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestSDKReplyContract(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "failure"
		if success {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			attempts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"test-tenant-token","expire":7200}`))
				case "/open-apis/im/v1/messages/om_original/reply":
					attempts++
					if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test-tenant-token" {
						t.Error("invalid reply authorization")
					}
					var body struct {
						Content string `json:"content"`
						Type    string `json:"msg_type"`
						UUID    string `json:"uuid"`
					}
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.Type != "text" || body.UUID != "stable-id" {
						t.Errorf("invalid reply body: %+v", body)
					}
					var content struct {
						Text string `json:"text"`
					}
					if json.Unmarshal([]byte(body.Content), &content) != nil || content.Text != `untrusted <at user_id="all">text</at>` {
						t.Error("reply text changed")
					}
					if success {
						_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_reply"}}`))
					} else {
						_, _ = w.Write([]byte(`{"code":999,"msg":"secret details"}`))
					}
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			sender := &Sender{lark.NewClient("cli_"+uuid.NewString(), "test-secret", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()), lark.WithLogger(sdkLogger{slog.New(slog.NewTextHandler(io.Discard, nil))}))}
			err := sender.Reply(context.Background(), "om_original", "stable-id", `untrusted <at user_id="all">text</at>`)
			if (err == nil) != success || attempts != 1 {
				t.Fatalf("attempts=%d err=%v", attempts, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("leaked API response")
			}
		})
	}
}

func TestSDKNotificationContract(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal" {
					_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"synthetic","expire":7200}`))
					return
				}
				calls++
				if r.URL.Path != "/open-apis/im/v1/messages" || r.URL.Query().Get("receive_id_type") != "open_id" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("wrong notice request")
				}
				var body struct {
					ReceiveID string `json:"receive_id"`
					Type      string `json:"msg_type"`
					Content   string `json:"content"`
					ID        string `json:"uuid"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.ReceiveID != "ou_approver" || body.Type != "interactive" || body.ID != "notice-id" {
					t.Error("wrong notification body")
				}
				cardDecision(t, body.Content, 0)
				if success {
					_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_notice"}}`))
				} else {
					_, _ = w.Write([]byte(`{"code":999,"msg":"private upstream detail"}`))
				}
			}))
			defer server.Close()
			sender := &Sender{lark.NewClient("cli_"+uuid.NewString(), "synthetic", lark.WithOpenBaseUrl(server.URL), lark.WithHttpClient(server.Client()), lark.WithLogger(sdkLogger{slog.New(slog.NewTextHandler(io.Discard, nil))}))}
			err := sender.Notify(t.Context(), "ou_approver", "notice-id", syntheticNotice())
			if (err == nil) != success || calls != 1 {
				t.Fatal("notice confirmation or retry mismatch", err, calls)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("upstream error leaked")
			}
		})
	}
}

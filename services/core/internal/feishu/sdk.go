package feishu

import (
	"agentos.local/core/internal/conversation"
	"agentos.local/core/internal/health"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// SDK arguments can contain payloads, signed URLs or credentials. Record severity only.
type sdkLogger struct{ logger *slog.Logger }

func (l sdkLogger) Debug(context.Context, ...interface{}) {}
func (l sdkLogger) Info(context.Context, ...interface{})  {}
func (l sdkLogger) Warn(ctx context.Context, _ ...interface{}) {
	l.logger.WarnContext(ctx, "feishu SDK warning")
}
func (l sdkLogger) Error(ctx context.Context, _ ...interface{}) {
	l.logger.ErrorContext(ctx, "feishu SDK error")
}

func (a *Adapter) LongConnection(ctx context.Context, secret string, logger *slog.Logger, update func(health.Transport)) error {
	if secret == "" {
		return errors.New("Feishu app secret required")
	}
	handler := a.dispatcher(logger)
	defer update(health.Stopped)
	client := larkws.NewClient(a.binding.AppID, secret, larkws.WithEventHandler(handler),
		larkws.WithOnReady(func() { update(health.Connected) }), larkws.WithOnReconnected(func() { update(health.Connected) }),
		larkws.WithOnReconnecting(func() { update(health.Reconnecting) }), larkws.WithOnDisconnected(func() { update(health.Disconnected) }),
		larkws.WithOnError(func(error) { update(health.Disconnected) }),
		larkws.WithLogger(sdkLogger{logger}), larkws.WithHttpClient(&http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Feishu redirect forbidden") }}))
	if err := client.Start(ctx); err != nil && ctx.Err() == nil {
		return errors.New("Feishu long connection failed")
	}
	return nil
}

func (a *Adapter) dispatcher(logger *slog.Logger) *dispatcher.EventDispatcher {
	handler := dispatcher.NewEventDispatcher("", "")
	handler.Config.Logger = sdkLogger{logger}
	handler.OnP2CardActionTrigger(func(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
		ctx, end := a.telemetry.Start(ctx, "ingress.approval")
		limited, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if event == nil || event.EventReq == nil {
			end(false)
			return nil, errors.New("Feishu card event missing")
		}
		response, err := a.receiveCard(limited, event.Body)
		end(err == nil)
		if err != nil {
			return nil, errors.New("Feishu card event rejected")
		}
		return response, nil
	})
	handler.OnCustomizedEvent("im.message.receive_v1", func(ctx context.Context, event *larkevent.EventReq) error {
		ctx, end := a.telemetry.Start(ctx, "ingress.websocket")
		limited, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		err := a.receive(limited, event.Body)
		end(err == nil)
		if err != nil {
			return errors.New("Feishu event rejected")
		}
		return nil
	})
	return handler
}

type Sender struct{ client *lark.Client }

func NewSender(app, secret string, logger *slog.Logger) *Sender {
	return &Sender{lark.NewClient(app, secret, lark.WithLogger(sdkLogger{logger}), lark.WithLogReqAtDebug(false), lark.WithHttpClient(&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Feishu redirect forbidden") }}))}
}
func (s *Sender) Reply(ctx context.Context, message, id, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	request := larkim.NewReplyMessageReqBuilder().MessageId(message).Body(larkim.NewReplyMessageReqBodyBuilder().MsgType("text").Content(string(content)).Uuid(id).Build()).Build()
	response, err := s.client.Im.V1.Message.Reply(ctx, request)
	if err != nil || response == nil || response.ApiResp == nil || response.StatusCode != http.StatusOK || !response.Success() || response.Data == nil || response.Data.MessageId == nil || *response.Data.MessageId == "" {
		return errors.New("Feishu reply result unconfirmed")
	}
	return nil
}

func (s *Sender) Notify(ctx context.Context, recipient, id string, notice conversation.ApprovalNotice) error {
	content, err := approvalCard(notice)
	if err != nil {
		return err
	}
	request := larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(recipient).MsgType("interactive").Content(content).Uuid(id).Build()).Build()
	response, err := s.client.Im.V1.Message.Create(ctx, request)
	if err != nil || response == nil || response.ApiResp == nil || response.StatusCode != http.StatusOK || !response.Success() || response.Data == nil || response.Data.MessageId == nil || *response.Data.MessageId == "" {
		return errors.New("Feishu notification result unconfirmed")
	}
	return nil
}

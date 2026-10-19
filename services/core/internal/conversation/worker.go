package conversation

import (
	"agentos.local/core/internal/telemetry"
	"context"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"time"
)

type Model interface {
	Complete(context.Context, string) (string, error)
}
type Sender interface {
	Reply(context.Context, string, string, string) error
}
type Worker struct {
	Store     *Store
	Model     Model
	Sender    Sender
	Logger    *slog.Logger
	Telemetry *telemetry.Telemetry
}

// ProcessOne never reclaims an attempted task. Interrupted running states require reconciliation.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	t, err := w.Store.Claim(ctx)
	if err != nil || t == nil {
		return false, err
	}
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithAttributes(attribute.String("task_id", t.ID.String()))}
	if t.Ingress.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: t.Ingress}))
	}
	ctx, end := w.Telemetry.Start(ctx, "task.run", opts...)
	succeeded := false
	defer func() { end(succeeded) }()
	finish := func(from, to, reason, response string) (bool, error) {
		// Persist terminal evidence even when the request deadline expires.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := w.Store.Transition(cleanup, t.ID, from, to, reason, response)
		if err == nil {
			w.Logger.Info("channel task transition", "task_id", t.ID, "trace_id", spanTraceID(ctx), "state", to, "reason", reason)
		}
		succeeded = err == nil && to == "succeeded"
		return true, err
	}
	if err := w.authorize(ctx, t); err != nil {
		return finish("model_running", "failed", "identity_denied", "")
	}
	modelCtx, modelEnd := w.Telemetry.Start(ctx, "model.complete")
	response, err := w.Model.Complete(modelCtx, t.Message.Text)
	modelEnd(err == nil)
	if err != nil {
		return finish("model_running", "unknown", "model_failed", "")
	}
	if err := w.authorize(ctx, t); err != nil {
		return finish("model_running", "failed", "delivery_denied", "")
	}
	if err := w.Store.Transition(ctx, t.ID, "model_running", "delivery_running", "", response); err != nil {
		return true, err
	}
	// No tools, Markdown cards, attachments or recipient override: only text to the original private message.
	deliveryCtx, deliveryEnd := w.Telemetry.Start(ctx, "channel.reply")
	err = w.Sender.Reply(deliveryCtx, t.Message.MessageID, t.ID.String(), response)
	deliveryEnd(err == nil)
	if err != nil {
		return finish("delivery_running", "unknown", "delivery_failed", "")
	}
	return finish("delivery_running", "succeeded", "", "")
}

func (w *Worker) authorize(ctx context.Context, t *Task) error {
	ctx, end := w.Telemetry.Start(ctx, "identity.authorize")
	err := w.Store.Authorize(ctx, t)
	end(err == nil)
	return err
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		worked, err := w.ProcessOne(ctx)
		if err != nil {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

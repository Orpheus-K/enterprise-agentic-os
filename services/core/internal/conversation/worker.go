package conversation

import (
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/quotation"
	"agentos.local/core/internal/telemetry"
	"agentos.local/core/internal/toolgateway"
	"context"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"time"
)

type Model interface {
	Complete(context.Context, string) (string, error)
}
type SkillModel interface {
	CompleteSkill(context.Context, string, string) (string, error)
}
type ToolModel interface {
	Turn(context.Context, string, string, []model.Message, []model.Tool) (model.TurnResult, error)
}
type Sender interface {
	Reply(context.Context, string, string, string) error
}
type Worker struct {
	Notifier  Notifier
	Store     *Store
	Tools     *toolgateway.Gateway
	Model     Model
	Sender    Sender
	Logger    *slog.Logger
	Telemetry *telemetry.Telemetry
}

// ProcessOne resumes only checkpoints before an external intent.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if err := w.Store.ExpireApprovals(ctx); err != nil {
		return false, err
	}
	if worked, err := w.Store.NotifyOne(ctx, w.Notifier); err != nil || worked {
		return worked, err
	}
	if err := w.Store.RecoverExpired(ctx); err != nil {
		return false, err
	}
	t, err := w.Store.Claim(ctx)
	if err != nil || t == nil {
		return false, err
	}
	err = w.processTask(ctx, t)
	if errors.Is(err, ErrLeaseLost) {
		w.Logger.Warn("task lease lost", "task_id", t.ID)
		return true, nil
	}
	return true, err
}
func (w *Worker) processTask(ctx context.Context, t *Task) error {
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithAttributes(attribute.String("task_id", t.ID.String()))}
	if t.Ingress.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: t.Ingress}))
	}
	ctx, end := w.Telemetry.Start(ctx, "task.run", opts...)
	succeeded := false
	defer func() { end(succeeded) }()
	finish := func(to State, reason string) error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := w.Store.Transition(cleanup, t, to, reason, "")
		if err == nil {
			w.Logger.Info("channel task transition", "task_id", t.ID, "trace_id", spanTraceID(ctx), "state", to, "reason", reason)
		}
		succeeded = err == nil && to == Succeeded
		return err
	}
	if t.State == ToolPending || t.State == ToolRunning {
		return w.runTool(ctx, t, finish)
	}
	if t.State == Claimed && t.Skill.Name == "" && len(t.Routes) > 0 {
		return w.route(ctx, t, finish)
	}
	if t.State == Claimed {
		if err := w.authorize(ctx, t); err != nil {
			return finish(Failed, "identity_denied")
		}
		var instructions string
		quoteRequired := false
		var skillModel SkillModel
		var turnModel ToolModel
		var definitions []toolgateway.Definition
		if t.Skill.Name != "" {
			loaded, err := w.Store.LoadSkill(ctx, t)
			if err != nil {
				return finish(Failed, "skill_unavailable")
			}
			var ok bool
			quoteRequired = loaded.Manifest.QuotationTool != ""
			skillModel, ok = w.Model.(SkillModel)
			if !ok {
				return finish(Failed, "skill_model_unsupported")
			}
			if len(loaded.Manifest.Tools) > 0 || len(t.History) > 0 {
				if w.Tools == nil {
					return finish(Failed, "tool_gateway_unavailable")
				}
				turnModel, ok = w.Model.(ToolModel)
				if !ok {
					return finish(Failed, "tool_model_unsupported")
				}
				definitions, err = w.Tools.Definitions(ctx, t.Message.Identity, t.Skill)
				if err != nil {
					return finish(Failed, "tool_definition_denied")
				}
			}
			instructions, err = loaded.SystemPrompt()
			if err != nil {
				return err
			}
		}
		if err := w.Store.Transition(ctx, t, ModelRunning, "", ""); err != nil {
			if errors.Is(err, ErrSkillDenied) {
				return finish(Failed, "skill_denied")
			}
			return err
		}
		modelCtx, modelEnd := w.Telemetry.Start(ctx, "model.complete")
		var response string
		var err error
		var turn model.TurnResult
		if turnModel != nil {
			tools := make([]model.Tool, 0, len(definitions))
			for _, d := range definitions {
				tools = append(tools, model.Tool{Name: d.Ref.Name, Description: d.Description, Parameters: d.InputSchema})
			}
			turn, err = turnModel.Turn(modelCtx, instructions, t.Message.Text, t.History, tools)
			response = turn.Text
		} else if skillModel != nil {
			response, err = skillModel.CompleteSkill(modelCtx, instructions, t.Message.Text)
		} else {
			response, err = w.Model.Complete(modelCtx, t.Message.Text)
		}
		modelEnd(err == nil)
		if err != nil {
			return finish(Unknown, "model_failed")
		}
		if turn.Call != nil {
			if t.ToolSteps >= 4 {
				return finish(Failed, "tool_budget_exceeded")
			}
			for _, d := range definitions {
				if d.Ref.Name == turn.Call.Function.Name {
					return w.Store.ScheduleTool(ctx, t, *turn.Call, d.Ref)
				}
			}
			return finish(Failed, "unapproved_tool")
		}
		if quoteRequired {
			return finish(Failed, "quotation_tool_required")
		}
		if err := w.Store.Transition(ctx, t, DeliveryPending, "", response); err != nil {
			return err
		}
	}
	if err := w.authorize(ctx, t); err != nil {
		return finish(Failed, "delivery_denied")
	}
	if waiting, err := w.Store.SuspendApproval(ctx, t); err != nil {
		return err
	} else if waiting {
		return nil
	}
	deliveryText := t.Response
	if t.QuoteVersion > 0 {
		var draft quotation.Draft
		if json.Unmarshal([]byte(t.Response), &draft) != nil || draft.Version != t.QuoteVersion || draft.ID != t.ID {
			return finish(Failed, "quotation_render_invalid")
		}
		var err error
		deliveryText, err = quotation.RenderText(draft)
		if err != nil {
			return finish(Failed, "quotation_render_invalid")
		}
	}
	if err := w.Store.Transition(ctx, t, DeliveryRunning, "", ""); err != nil {
		if errors.Is(err, ErrSkillDenied) {
			return finish(Failed, "skill_denied")
		}
		if errors.Is(err, ErrApprovalDenied) {
			return finish(Failed, "approval_denied")
		}
		return err
	}
	deliveryCtx, deliveryEnd := w.Telemetry.Start(ctx, "channel.reply")
	err := w.Sender.Reply(deliveryCtx, t.Message.MessageID, t.ID.String(), deliveryText)
	deliveryEnd(err == nil)
	if err != nil {
		return finish(Unknown, "delivery_failed")
	}
	return finish(Succeeded, "")
}

func (w *Worker) authorize(ctx context.Context, t *Task) error {
	ctx, end := w.Telemetry.Start(ctx, "identity.authorize")
	err := w.Store.Authorize(ctx, t)
	if err == nil && len(t.History) > 0 {
		if w.Tools == nil {
			err = toolgateway.ErrDenied
		} else {
			var definitions []toolgateway.Definition
			definitions, err = w.Tools.Definitions(ctx, t.Message.Identity, t.Skill)
			if err == nil {
				names := map[string]bool{}
				for _, d := range definitions {
					names[d.Ref.Name] = true
				}
				for _, m := range t.History {
					for _, c := range m.ToolCalls {
						if !names[c.Function.Name] {
							err = toolgateway.ErrDenied
						}
					}
				}
			}
		}
	}
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

func (w *Worker) runTool(ctx context.Context, t *Task, finish func(State, string) error) error {
	if w.Tools == nil || t.Tool == nil {
		if t.State == ToolPending {
			return finish(Failed, "tool_runtime_unavailable")
		}
		return finish(Unknown, "tool_runtime_unavailable")
	}
	if err := w.authorize(ctx, t); err != nil {
		return finish(Failed, "tool_identity_denied")
	}
	recovering := t.State == ToolRunning
	if !recovering {
		if err := w.Store.Transition(ctx, t, ToolRunning, "", ""); err != nil {
			return err
		}
	}
	args := json.RawMessage(t.Tool.Call.Function.Arguments)
	var raw json.RawMessage
	var err error
	if recovering {
		raw, err = w.Tools.Result(ctx, t.Message.Identity, t.Tool.Operation, t.Skill, t.Tool.Ref, args)
	} else {
		raw, err = w.Tools.Call(ctx, t.Message.Identity, t.Tool.Operation, t.Skill, t.Tool.Ref, args)
	}
	if err != nil {
		if errors.Is(err, toolgateway.ErrDenied) || errors.Is(err, toolgateway.ErrInvalid) || errors.Is(err, toolgateway.ErrWriteRestricted) {
			return finish(Failed, "tool_denied")
		}
		return finish(Unknown, "tool_result_unknown")
	}
	loaded, err := w.Store.LoadSkill(ctx, t)
	if err != nil {
		return finish(Failed, "tool_skill_denied")
	}
	if loaded.Manifest.QuotationTool == t.Tool.Ref.Name {
		draft, err := quotation.FromTool(t.ID, raw)
		if err != nil {
			return finish(Failed, "quotation_invalid")
		}
		err = w.Store.SaveQuotation(ctx, t, draft)
		if errors.Is(err, ErrApprovalDenied) || errors.Is(err, ErrInvalidApproval) {
			return finish(Failed, "quotation_policy_denied")
		}
		if err != nil {
			return err
		}
		return w.processTask(ctx, t)
	}
	return w.Store.CompleteTool(ctx, t, raw)
}

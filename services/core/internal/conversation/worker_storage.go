package conversation

import (
	"agentos.local/core/internal/identity"
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/quotation"
	"agentos.local/core/internal/skills"
	"context"
	"encoding/json"
	"errors"
)

// ExecutionStore owns durable state and authorization for basic text tasks.
// Optional capabilities are separate; drivers must not pretend to support them.
type ExecutionStore interface {
	ExpireApprovals(context.Context) error
	RecoverExpired(context.Context) error
	Claim(context.Context) (*Task, error)
	Authorize(context.Context, *Task) error
	Renew(context.Context, *Task) error
	Transition(context.Context, *Task, State, string, string) error
	SuspendApproval(context.Context, *Task) (bool, error)
}
type NotificationStore interface {
	NotifyOne(context.Context, Notifier) (bool, error)
}
type SkillExecutionStore interface {
	LoadSkill(context.Context, *Task) (skills.Loaded, error)
	RouteSummaries(context.Context, identity.Principal, []skills.Ref) ([]skills.Summary, error)
	BindRoute(context.Context, *Task, skills.Ref) error
	ScheduleTool(context.Context, *Task, model.ToolCall, skills.ToolRef) error
	CompleteTool(context.Context, *Task, json.RawMessage) error
	SaveQuotation(context.Context, *Task, quotation.Draft) error
}
type KnowledgeExecutionStore interface {
	PrepareKnowledge(context.Context, *Task) ([]knowledge.Evidence, error)
	GroundTask(context.Context, *Task) (knowledge.Grounding, error)
}

var ErrUnsupportedExecution = errors.New("task requires unsupported storage capability")
var _ ExecutionStore = (*Store)(nil)
var _ NotificationStore = (*Store)(nil)
var _ SkillExecutionStore = (*Store)(nil)
var _ KnowledgeExecutionStore = (*Store)(nil)

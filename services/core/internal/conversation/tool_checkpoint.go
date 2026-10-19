package conversation

import (
	"agentos.local/core/internal/database"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/skills"
	"context"
	"encoding/json"
	"github.com/google/uuid"
)

type ToolInvocation struct {
	Operation uuid.UUID
	Ref       skills.ToolRef
	Call      model.ToolCall
}

func (s *Store) ScheduleTool(ctx context.Context, t *Task, call model.ToolCall, ref skills.ToolRef) error {
	if t.State != ModelRunning || t.ToolSteps >= 4 || call.Function.Name != ref.Name {
		return ErrInvalidTransition
	}
	history := append(append([]model.Message(nil), t.History...), model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call}})
	pending := &ToolInvocation{uuid.New(), ref, call}
	return s.toolCheckpoint(ctx, t, ToolPending, history, pending, t.ToolSteps+1)
}
func (s *Store) CompleteTool(ctx context.Context, t *Task, result json.RawMessage) error {
	if t.State != ToolRunning || t.Tool == nil || !json.Valid(result) || len(result) > 65536 {
		return ErrInvalidTransition
	}
	history := append(append([]model.Message(nil), t.History...), model.Message{Role: "tool", Content: string(result), ToolCallID: t.Tool.Call.ID})
	return s.toolCheckpoint(ctx, t, Pending, history, nil, t.ToolSteps)
}
func (s *Store) toolCheckpoint(ctx context.Context, t *Task, to State, history []model.Message, pending *ToolInvocation, steps int) error {
	raw, err := json.Marshal(history)
	if err != nil || len(raw) > 1048576 {
		return ErrInvalidTransition
	}
	err = s.scoped(ctx, func(q database.Queries) error {
		if err := s.lockTask(ctx, q, t); err != nil {
			return err
		}
		result, err := q.Exec(ctx, `UPDATE channel.tasks SET state=$8,tool_history=$9,tool_invocation=$10,tool_steps=$11,step_version=step_version+1,lease_owner=CASE WHEN $8='pending' THEN NULL ELSE lease_owner END,lease_until=CASE WHEN $8='pending' THEN NULL ELSE clock_timestamp() END,updated_at=clock_timestamp() WHERE tenant_id=$1 AND app_id=$2 AND model_revision=$3 AND id=$4 AND lease_owner=$5 AND lease_generation=$6 AND step_version=$7 AND state=$12 AND lease_until>clock_timestamp()`, s.tenant, s.app, s.revision, t.ID, s.owner, t.Generation, t.Version, to, raw, pending, steps, t.State)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return audit(ctx, q, s.tenant, t.ID, string(to), "tool_checkpoint")
	})
	if err == nil {
		t.State = to
		t.Version++
		t.History = history
		t.Tool = pending
		t.ToolSteps = steps
	}
	return err
}

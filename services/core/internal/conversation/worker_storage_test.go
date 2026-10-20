package conversation

import (
	"agentos.local/core/internal/knowledge"
	"agentos.local/core/internal/model"
	"agentos.local/core/internal/skills"
	"errors"
	"testing"
)

// The embedded interface has no implementation: crossing the capability guard
// into storage, telemetry or external calls fails the test rather than succeeding.
type basicOnly struct{ ExecutionStore }

func TestWorkerRejectsUnsupportedPersistedCapabilities(t *testing.T) {
	for name, task := range map[string]*Task{
		"skill":        {Skill: skills.Ref{Name: "skill"}},
		"routes":       {Routes: []skills.Ref{{Name: "skill"}}},
		"history":      {History: []model.Message{{}}},
		"tool":         {Tool: &ToolInvocation{}},
		"tool_pending": {State: ToolPending},
		"tool_running": {State: ToolRunning},
		"quotation":    {QuoteVersion: 1},
		"knowledge":    {KnowledgeRefs: []knowledge.Reference{}},
	} {
		t.Run(name, func(t *testing.T) {
			w := Worker{Store: basicOnly{}}
			if err := w.processTask(t.Context(), task); !errors.Is(err, ErrUnsupportedExecution) {
				t.Fatalf("unsupported task accepted: %v", err)
			}
		})
	}
}

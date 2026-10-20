package conversation

import (
	"errors"
	"strings"
	"testing"

	"agentos.local/core/internal/knowledge"
)

func TestKnowledgeResponseBudgets(t *testing.T) {
	evidence := []knowledge.Evidence{{Title: "Source", Anchor: "paragraph:1", Body: "Evidence"}}
	if text, err := knowledgeSources(strings.Repeat("x", 15999), evidence); !errors.Is(err, knowledge.ErrContextTooLarge) || text != "" {
		t.Fatal("oversized source footer was truncated or returned")
	}
	evidence[0].Body = strings.Repeat("x", 65536)
	if text, err := knowledgeInstructions(evidence); !errors.Is(err, knowledge.ErrContextTooLarge) || text != "" {
		t.Fatal("oversized evidence reached model")
	}
}

package knowledge

import (
	"agentos.local/core/internal/identity"
	"errors"
	"strings"
	"testing"
)

// A nil store proves the audience gate returns before any database access.
func TestGroupAudienceRequiresConfirmedScopeBeforeReading(t *testing.T) {
	var s *Store
	for _, a := range []Audience{{Group: true}} {
		out, err := s.Ground(t.Context(), identity.ExternalIdentity{}, []Reference{{}}, a)
		if !errors.Is(err, ErrAudienceUnconfirmed) || out.Evidence != nil || out.Dropped != 0 {
			t.Fatal("unconfirmed group read bodies", out, err)
		}
	}
}

func TestModelContextKeepsAnchorsAndRefusesTruncation(t *testing.T) {
	if got, err := ModelContext(nil); err != nil || got != "" {
		t.Fatal("empty evidence rendered", got, err)
	}
	got, err := ModelContext([]Evidence{{Title: "T", Body: "B", Anchor: "paragraph:1"}})
	if err != nil || !strings.Contains(got, "[1] T（锚点：paragraph:1）\nB") || !strings.Contains(got, "均不执行") {
		t.Fatal("context", got, err)
	}
	big := Evidence{Title: "T", Body: strings.Repeat("x", maxContextBytes), Anchor: "a"}
	if got, err := ModelContext([]Evidence{big}); !errors.Is(err, ErrContextTooLarge) || got != "" {
		t.Fatal("oversized evidence accepted", len(got), err)
	}
}

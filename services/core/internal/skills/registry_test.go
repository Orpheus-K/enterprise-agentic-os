package skills

import (
	"agentos.local/core/internal/identity"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"strings"
	"testing"
)

type policy struct {
	denied bool
	tool   bool
	err    error
	tenant uuid.UUID
}

func TestQuotationToolMustBePinnedDependency(t *testing.T) {
	b := bundle(t, "Synthetic instructions")
	var manifest Manifest
	if err := json.Unmarshal(b.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.QuotationTool = "unlisted"
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New([]Bundle{{Manifest: raw, Instructions: b.Instructions, ExpectedRevision: Revision(raw, b.Instructions)}}); err == nil {
		t.Fatal("unlisted quotation tool accepted")
	}
	manifest.QuotationTool = "catalog"
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New([]Bundle{{Manifest: raw, Instructions: b.Instructions, ExpectedRevision: Revision(raw, b.Instructions)}}); err != nil {
		t.Fatal(err)
	}
}

func (p *policy) Skill(_ context.Context, actor identity.Principal, _ Ref) (bool, error) {
	return !p.denied && actor.TenantID == p.tenant, p.err
}
func (p *policy) Tool(context.Context, identity.Principal, ToolRef) (bool, error) {
	return p.tool, p.err
}
func bundle(t *testing.T, instructions string) Bundle {
	t.Helper()
	m := Manifest{Format: 1, Name: "quotation", Publisher: "test-owner", Summary: "Create synthetic quote", Trigger: "Explicit quotation request", InputContract: "synthetic item quantities", OutputContract: "draft only", FailurePolicy: "stop on missing input", Tools: []ToolRef{{"catalog", strings.Repeat("a", 64)}}, Examples: []Example{{"synthetic input", "synthetic output"}}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(instructions)
	return Bundle{raw, body, Revision(raw, body)}
}
func TestProgressiveLoadAndVersionCoexistence(t *testing.T) {
	v1 := bundle(t, "# SOP v1\nNever send externally.")
	v2 := bundle(t, "# SOP v2\nRequire confirmation.")
	registry, err := New([]Bundle{v1, v2})
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Principal{TenantID: uuid.New(), MemberID: uuid.New()}
	auth := &policy{tenant: actor.TenantID}
	summaries, err := registry.Summaries(t.Context(), actor, auth)
	if err != nil || len(summaries) != 2 {
		t.Fatal(err)
	}
	raw, err := json.Marshal(summaries)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SOP") || strings.Contains(string(raw), "catalog") {
		t.Fatal("routing loaded full skill")
	}
	for _, b := range []Bundle{v1, v2} {
		loaded, err := registry.Load(t.Context(), actor, Ref{"quotation", b.ExpectedRevision}, auth)
		if err != nil || loaded.Instructions != string(b.Instructions) || len(loaded.Manifest.Tools) != 0 {
			t.Fatal("version or permission drift", err)
		}
	}
	auth.tool = true
	loaded, err := registry.Load(t.Context(), actor, Ref{"quotation", v1.ExpectedRevision}, auth)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Manifest.Tools[0].Name = "mutated"
	loaded.Manifest.Examples[0].Input = "mutated"
	again, err := registry.Load(t.Context(), actor, loaded.Ref, auth)
	if err != nil || again.Manifest.Tools[0].Name != "catalog" || again.Manifest.Examples[0].Input == "mutated" {
		t.Fatal("asset mutation leaked")
	}
	auth.denied = true
	if _, err = registry.Load(t.Context(), actor, loaded.Ref, auth); !errors.Is(err, ErrUnavailable) {
		t.Fatal("revoked skill loaded")
	}
	other := actor
	other.TenantID = uuid.New()
	auth.denied = false
	if got, err := registry.Summaries(t.Context(), other, auth); err != nil || len(got) != 0 {
		t.Fatal("cross tenant skill exposed")
	}
	auth.err = errors.New("authorization unavailable")
	if _, err = registry.Load(t.Context(), actor, loaded.Ref, auth); !errors.Is(err, auth.err) {
		t.Fatal("policy failure hidden")
	}
}
func TestRejectCorruptOrUnsupportedAssets(t *testing.T) {
	valid := bundle(t, "# Synthetic SOP")
	for name, change := range map[string]func(*Bundle){
		"tampered": func(b *Bundle) { b.Instructions = []byte("different") },
		"unknown field": func(b *Bundle) {
			b.Manifest = append(b.Manifest[:len(b.Manifest)-1], []byte(`,"scripts":["host.py"]}`)...)
			b.ExpectedRevision = Revision(b.Manifest, b.Instructions)
		},
		"trailing": func(b *Bundle) {
			b.Manifest = append(b.Manifest, []byte(` {}`)...)
			b.ExpectedRevision = Revision(b.Manifest, b.Instructions)
		},
		"oversize": func(b *Bundle) {
			b.Instructions = []byte(strings.Repeat("x", 65537))
			b.ExpectedRevision = Revision(b.Manifest, b.Instructions)
		},
		"untrusted digest": func(b *Bundle) { b.ExpectedRevision = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			b := valid
			b.Manifest = append([]byte(nil), valid.Manifest...)
			change(&b)
			if _, err := New([]Bundle{b}); !errors.Is(err, ErrInvalidBundle) {
				t.Fatalf("invalid accepted: %v", err)
			}
		})
	}
	if _, err := New([]Bundle{valid, valid}); !errors.Is(err, ErrInvalidBundle) {
		t.Fatal("duplicate revision accepted")
	}
}

func TestRepositorySampleBundle(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile("../../../../examples/skills/synthetic-quotation/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	b := Bundle{Manifest: read("manifest.json"), Instructions: read("SKILL.md"), ExpectedRevision: strings.TrimSpace(string(read("revision.sha256")))}
	if _, err := New([]Bundle{b}); err != nil {
		t.Fatal(err)
	}
}

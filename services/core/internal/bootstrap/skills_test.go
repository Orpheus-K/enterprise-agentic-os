package bootstrap

import (
	"agentos.local/core/internal/skills"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPinnedSkillFiles(t *testing.T) {
	base, err := filepath.Abs("../../../../examples/skills/synthetic-quotation")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(base, "revision.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.TrimSpace(string(raw))
	c := SkillsConfig{Selected: skills.Ref{Name: "synthetic-quotation", Revision: revision}, Assets: []SkillAsset{{ManifestPath: filepath.Join(base, "manifest.json"), InstructionsPath: filepath.Join(base, "SKILL.md"), Revision: revision}}}
	if _, err = c.Load(); err != nil {
		t.Fatal(err)
	}
	selected := c.Selected
	c.Routes = []skills.Ref{selected}
	if err = c.Validate(); err == nil {
		t.Fatal("accepted simultaneous selected and routes")
	}
	c.Selected = skills.Ref{}
	if _, err = c.Load(); err != nil {
		t.Fatal(err)
	}
	c.Routes = append(c.Routes, selected)
	if err = c.Validate(); err == nil {
		t.Fatal("accepted ambiguous route versions")
	}
	c.Routes = nil
	c.Selected = selected
	c.Assets[0].Revision = strings.Repeat("a", 64)
	if _, err = c.Load(); err == nil {
		t.Fatal("tampered package loaded")
	}
	c.Assets[0].ManifestPath = "relative/manifest.json"
	if err = c.Validate(); err == nil {
		t.Fatal("relative deployment path accepted")
	}
}

package bootstrap

import (
	"agentos.local/core/internal/skills"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type SkillAsset struct {
	ManifestPath     string `json:"manifest_path"`
	InstructionsPath string `json:"instructions_path"`
	Revision         string `json:"revision"`
}
type SkillsConfig struct {
	Routes   []skills.Ref `json:"routes,omitempty"`
	Selected skills.Ref   `json:"selected"`
	Assets   []SkillAsset `json:"assets"`
}

func (c SkillsConfig) Validate() error {
	if len(c.Assets) == 0 || len(c.Assets) > 64 {
		return errors.New("invalid selected skill configuration")
	}
	if len(c.Routes) > 0 {
		if len(c.Routes) > 32 || c.Selected != (skills.Ref{}) {
			return errors.New("choose selected or routes")
		}
		names := map[string]bool{}
		for _, ref := range c.Routes {
			if ref.Name == "" || len(ref.Revision) != 64 || names[ref.Name] {
				return errors.New("invalid skill routes")
			}
			names[ref.Name] = true
		}
	} else if c.Selected.Name == "" || len(c.Selected.Revision) != 64 {
		return errors.New("missing selected skill or routes")
	}
	for _, a := range c.Assets {
		if !filepath.IsAbs(a.ManifestPath) || !filepath.IsAbs(a.InstructionsPath) || len(a.Revision) != 64 {
			return errors.New("skill assets require absolute paths and pinned revisions")
		}
	}
	return nil
}
func readSkillFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("skill asset unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("skill asset must be a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("skill asset unreadable or oversized")
	}
	return raw, nil
}
func (c SkillsConfig) Load() (*skills.Registry, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	bundles := make([]skills.Bundle, 0, len(c.Assets))
	for _, asset := range c.Assets {
		manifest, err := readSkillFile(asset.ManifestPath, 32768)
		if err != nil {
			return nil, err
		}
		instructions, err := readSkillFile(asset.InstructionsPath, 65536)
		if err != nil {
			return nil, err
		}
		bundles = append(bundles, skills.Bundle{Manifest: manifest, Instructions: instructions, ExpectedRevision: asset.Revision})
	}
	registry, err := skills.New(bundles)
	if err != nil {
		return nil, err
	}
	refs := c.Routes
	if len(refs) == 0 {
		refs = []skills.Ref{c.Selected}
	}
	for _, ref := range refs {
		if !registry.Contains(ref) {
			return nil, errors.New("selected skill revision is not installed")
		}
	}
	return registry, nil
}

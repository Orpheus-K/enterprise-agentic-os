// Package skills validates immutable, administrator-pinned skill assets.
package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"agentos.local/core/internal/identity"
)

var ErrInvalidBundle = errors.New("invalid skill bundle")
var ErrUnavailable = errors.New("skill or tool unavailable")
var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Ref struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}
type ToolRef struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}
type Example struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}
type Manifest struct {
	QuotationTool  string    `json:"quotation_tool,omitempty"`
	Format         int       `json:"format"`
	Name           string    `json:"name"`
	Publisher      string    `json:"publisher"`
	Summary        string    `json:"summary"`
	Trigger        string    `json:"trigger"`
	InputContract  string    `json:"input_contract"`
	OutputContract string    `json:"output_contract"`
	FailurePolicy  string    `json:"failure_policy"`
	Tools          []ToolRef `json:"tools"`
	Examples       []Example `json:"examples"`
}

// ExpectedRevision comes from trusted deployment configuration, not the bundle itself.
type Bundle struct {
	Manifest         []byte
	Instructions     []byte
	ExpectedRevision string
}
type Summary struct {
	Ref     Ref    `json:"ref"`
	Summary string `json:"summary"`
	Trigger string `json:"trigger"`
}
type Loaded struct {
	Ref          Ref
	Manifest     Manifest
	Instructions string
}

// Implementations must check current tenant/member policy; cached routing is not authority.
type Authorizer interface {
	Skill(context.Context, identity.Principal, Ref) (bool, error)
	Tool(context.Context, identity.Principal, ToolRef) (bool, error)
}
type Registry struct{ assets map[Ref]Loaded }

func Revision(manifest, instructions []byte) string {
	h := sha256.New()
	h.Write(manifest)
	h.Write([]byte{0})
	h.Write(instructions)
	return hex.EncodeToString(h.Sum(nil))
}
func canonical(text string, limit int) bool {
	return text != "" && len(text) <= limit && strings.TrimSpace(text) == text && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}
func revisionValid(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}
func parse(b Bundle) (Loaded, error) {
	if len(b.Manifest) == 0 || len(b.Manifest) > 32768 || len(b.Instructions) == 0 || len(b.Instructions) > 65536 || !utf8.Valid(b.Instructions) || bytes.ContainsRune(b.Instructions, 0) || strings.TrimSpace(string(b.Instructions)) == "" || !revisionValid(b.ExpectedRevision) || Revision(b.Manifest, b.Instructions) != b.ExpectedRevision {
		return Loaded{}, ErrInvalidBundle
	}
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(b.Manifest))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF {
		return Loaded{}, ErrInvalidBundle
	}
	if m.Format != 1 || !identifier.MatchString(m.Name) || !canonical(m.Publisher, 128) || !canonical(m.Summary, 512) || !canonical(m.Trigger, 512) || !canonical(m.InputContract, 2048) || !canonical(m.OutputContract, 2048) || !canonical(m.FailurePolicy, 2048) || len(m.Tools) > 32 || len(m.Examples) > 16 {
		return Loaded{}, ErrInvalidBundle
	}
	seen := map[string]bool{}
	for _, tool := range m.Tools {
		if !identifier.MatchString(tool.Name) || !revisionValid(tool.Revision) || seen[tool.Name] {
			return Loaded{}, ErrInvalidBundle
		}
		seen[tool.Name] = true
	}
	if m.QuotationTool != "" && !seen[m.QuotationTool] {
		return Loaded{}, ErrInvalidBundle
	}
	for _, example := range m.Examples {
		if !canonical(example.Input, 2048) || !canonical(example.Output, 2048) {
			return Loaded{}, ErrInvalidBundle
		}
	}
	return Loaded{Ref{m.Name, b.ExpectedRevision}, m, string(b.Instructions)}, nil
}
func New(bundles []Bundle) (*Registry, error) {
	assets := make(map[Ref]Loaded, len(bundles))
	for _, bundle := range bundles {
		asset, err := parse(bundle)
		if err != nil {
			return nil, err
		}
		if _, exists := assets[asset.Ref]; exists {
			return nil, fmt.Errorf("%w: duplicate revision", ErrInvalidBundle)
		}
		assets[asset.Ref] = asset
	}
	return &Registry{assets}, nil
}
func (r *Registry) Summaries(ctx context.Context, p identity.Principal, auth Authorizer) ([]Summary, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, ErrUnavailable
	}
	refs := make([]Ref, 0, len(r.assets))
	for ref := range r.assets {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Name == refs[j].Name {
			return refs[i].Revision < refs[j].Revision
		}
		return refs[i].Name < refs[j].Name
	})
	result := make([]Summary, 0, len(refs))
	for _, ref := range refs {
		allowed, err := auth.Skill(ctx, p, ref)
		if err != nil {
			return nil, err
		}
		if allowed {
			asset := r.assets[ref]
			result = append(result, Summary{ref, asset.Manifest.Summary, asset.Manifest.Trigger})
		}
	}
	return result, nil
}
func (r *Registry) Load(ctx context.Context, p identity.Principal, ref Ref, auth Authorizer) (Loaded, error) {
	if err := p.Validate(); err != nil {
		return Loaded{}, err
	}
	if auth == nil {
		return Loaded{}, ErrUnavailable
	}
	allowed, err := auth.Skill(ctx, p, ref)
	if err != nil {
		return Loaded{}, err
	}
	if !allowed {
		return Loaded{}, ErrUnavailable
	}
	asset, ok := r.assets[ref]
	if !ok {
		return Loaded{}, ErrUnavailable
	}
	tools := make([]ToolRef, 0, len(asset.Manifest.Tools))
	for _, tool := range asset.Manifest.Tools {
		allowed, err := auth.Tool(ctx, p, tool)
		if err != nil {
			return Loaded{}, err
		}
		if allowed {
			tools = append(tools, tool)
		}
	}
	// Callers cannot mutate shared assets; unauthorized tools never enter a loaded context.
	asset.Manifest.Tools = tools
	asset.Manifest.Examples = append([]Example(nil), asset.Manifest.Examples...)
	return asset, nil
}

// Contains only checks artifact availability, never user authorization.
func (r *Registry) Contains(ref Ref) bool { _, ok := r.assets[ref]; return ok }

func (asset Loaded) SystemPrompt() (string, error) {
	contract, err := json.Marshal(asset.Manifest)
	if err != nil {
		return "", err
	}
	return asset.Instructions + "\n\nSkill contract and examples:\n" + string(contract), nil
}

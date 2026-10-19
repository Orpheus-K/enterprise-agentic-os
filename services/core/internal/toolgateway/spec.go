package toolgateway

import (
	"agentos.local/core/internal/skills"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/jsonschema-go/jsonschema"
	"net/url"
	"regexp"
	"strings"
)

const ProtocolVersion = "2026-07-28"

var ErrDenied = errors.New("tool access denied")
var ErrPolicyUnavailable = errors.New("tool authorization unavailable")
var ErrInvalid = errors.New("invalid tool contract or arguments")
var ErrUnknown = errors.New("tool result unknown; reconciliation required")
var ErrRejected = errors.New("tool result rejected")
var ErrAlreadyStarted = errors.New("tool operation already started; do not replay")
var ErrAudit = errors.New("tool audit persistence failed")
var ErrWriteRestricted = errors.New("write tool requires durable runtime approval")
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Spec struct {
	Name           string          `json:"name"`
	RemoteName     string          `json:"remote_name"`
	Endpoint       string          `json:"endpoint"`
	Description    string          `json:"description"`
	Owner          string          `json:"owner"`
	Risk           string          `json:"risk"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	AllowLocalHTTP bool            `json:"allow_local_http"`
	InputSchema    json.RawMessage `json:"input_schema"`
	OutputSchema   json.RawMessage `json:"output_schema"`
}
type Definition struct {
	Ref         skills.ToolRef  `json:"ref"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type compiledTool struct {
	spec          Spec
	ref           skills.ToolRef
	input, output *jsonschema.Resolved
}

func Revision(spec Spec) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", ErrInvalid
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func compileSchema(raw []byte) (*jsonschema.Resolved, error) {
	if len(raw) == 0 || len(raw) > 16384 {
		return nil, ErrInvalid
	}
	var s jsonschema.Schema
	if json.Unmarshal(raw, &s) != nil || s.Type != "object" {
		return nil, ErrInvalid
	}
	// Top-level object fields must be explicit. Nested rules remain the author's contract.
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil || !bytes.Equal(bytes.TrimSpace(shape["additionalProperties"]), []byte("false")) {
		return nil, ErrInvalid
	}
	resolved, err := s.Resolve(&jsonschema.ResolveOptions{Loader: func(*url.URL) (*jsonschema.Schema, error) { return nil, ErrInvalid }})
	if err != nil {
		return nil, ErrInvalid
	}
	return resolved, nil
}
func compile(spec Spec) (compiledTool, error) {
	u, err := url.Parse(spec.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Scheme != "https" && !(u.Scheme == "http" && spec.AllowLocalHTTP && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return compiledTool{}, ErrInvalid
	}
	if !namePattern.MatchString(spec.Name) || strings.TrimSpace(spec.RemoteName) == "" || len(spec.RemoteName) > 128 || strings.TrimSpace(spec.Owner) == "" || strings.TrimSpace(spec.Description) == "" || len(spec.Description) > 1024 || (spec.Risk != "read" && spec.Risk != "write") || spec.TimeoutSeconds < 1 || spec.TimeoutSeconds > 60 {
		return compiledTool{}, ErrInvalid
	}
	in, err := compileSchema(spec.InputSchema)
	if err != nil {
		return compiledTool{}, err
	}
	out, err := compileSchema(spec.OutputSchema)
	if err != nil {
		return compiledTool{}, err
	}
	revision, err := Revision(spec)
	if err != nil {
		return compiledTool{}, err
	}
	spec.InputSchema = append(json.RawMessage(nil), spec.InputSchema...)
	spec.OutputSchema = append(json.RawMessage(nil), spec.OutputSchema...)
	return compiledTool{spec, skills.ToolRef{Name: spec.Name, Revision: revision}, in, out}, nil
}

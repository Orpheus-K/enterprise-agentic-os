package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/api"
)

var ErrArtifact = errors.New("sandbox artifact rejected")

const MaxArtifactBytes = 1 << 20

// ArtifactPolicy is supplied by trusted configuration, never by the module.
// Scan is mandatory for deployment-specific sensitive content/AV checks. It
// must honor context cancellation and must not persist or release the input.
type ArtifactPolicy struct {
	MediaType string
	MaxBytes  uint32
	Scan      func(context.Context, []byte) error
}

// RunArtifact is an explicit text-output ABI. It grants no filesystem or WASI
// capabilities. Integer-only callers keep Run's original contract.
func RunArtifact(ctx context.Context, program Program, limits Limits, export string, policy ArtifactPolicy, args ...uint64) ([]byte, error) {
	if policy.MaxBytes == 0 || policy.MaxBytes > MaxArtifactBytes || policy.Scan == nil || (policy.MediaType != "text/plain" && policy.MediaType != "application/json") {
		return nil, ErrInvalid
	}
	var artifact []byte
	_, err := run(ctx, program, limits, export, true, func(ctx context.Context, module api.Module, values []uint64) error {
		if values[1] == 0 || values[1] > uint64(policy.MaxBytes) {
			return ErrArtifact
		}
		view, ok := module.ExportedMemory("memory").Read(uint32(values[0]), uint32(values[1]))
		if !ok {
			return ErrArtifact
		}
		// Copy before running a scanner; never return a runtime-backed memory view.
		data := bytes.Clone(view)
		if err := scanText(data, policy.MediaType); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ErrTimeout
		}
		// A scanner sees its own copy and cannot replace approved output bytes.
		if err := policy.Scan(ctx, bytes.Clone(data)); err != nil {
			return ErrArtifact
		}
		if ctx.Err() != nil {
			return ErrTimeout
		}
		artifact = data
		return nil
	}, args...)
	if err != nil {
		return nil, err
	}
	return artifact, nil
}

func scanText(data []byte, mediaType string) error {
	if !utf8.Valid(data) {
		return ErrArtifact
	}
	for _, r := range string(data) {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return ErrArtifact
		}
	}
	if mediaType == "application/json" {
		var decoded any
		if json.Unmarshal(data, &decoded) != nil {
			return ErrArtifact
		}
		pending := []any{decoded}
		for len(pending) > 0 {
			value := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			switch v := value.(type) {
			case string:
				if err := scanText([]byte(v), "text/plain"); err != nil {
					return err
				}
			case []any:
				pending = append(pending, v...)
			case map[string]any:
				for key, item := range v {
					if err := scanText([]byte(key), "text/plain"); err != nil {
						return err
					}
					pending = append(pending, item)
				}
			}
		}
	}
	lower := bytes.ToLower(data)
	for _, signature := range [][]byte{[]byte("<script"), []byte("javascript:"), []byte("-----begin "), []byte("eicar-standard-antivirus-test-file")} {
		if bytes.Contains(lower, signature) {
			return ErrArtifact
		}
	}
	return nil
}

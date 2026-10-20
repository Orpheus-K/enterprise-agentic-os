// Package sandbox runs untrusted computation behind explicit resource limits.
// The WASM route grants a module nothing: no host functions, no WASI (so no files, clock,
// randomness, environment or network), and a fresh runtime per run so nothing persists.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

var (
	// ErrInvalid rejects bad limits, digests, modules, exports or arguments before or during setup.
	ErrInvalid = errors.New("invalid sandbox request")
	// ErrTimeout reports that the deadline stopped the module; no partial result is returned.
	ErrTimeout = errors.New("sandbox deadline exceeded")
	// ErrFailed reports a trap or runtime failure inside the module.
	ErrFailed = errors.New("sandbox module failed")
)

const (
	maxModuleBytes = 1 << 20
	maxValues      = 8
	// MaxMemoryPages caps linear memory at 16 MiB (64 KiB pages).
	MaxMemoryPages = 256
	MaxTimeout     = time.Minute
)

// Limits are mandatory: a zero value is rejected rather than defaulted to "unbounded".
type Limits struct {
	Timeout     time.Duration
	MemoryPages uint32
}

// Program is a module pinned by the SHA-256 the administrator approved.
type Program struct {
	Wasm   []byte
	SHA256 string
}

func (l Limits) validate() error {
	if l.Timeout <= 0 || l.Timeout > MaxTimeout || l.MemoryPages < 1 || l.MemoryPages > MaxMemoryPages {
		return fmt.Errorf("%w: limits", ErrInvalid)
	}
	return nil
}

func (p Program) validate() error {
	if len(p.Wasm) == 0 || len(p.Wasm) > maxModuleBytes {
		return fmt.Errorf("%w: module size", ErrInvalid)
	}
	sum := sha256.Sum256(p.Wasm)
	if hex.EncodeToString(sum[:]) != p.SHA256 {
		return fmt.Errorf("%w: module digest", ErrInvalid)
	}
	return nil
}

// Run calls one integer-only export with at most eight arguments and results.
// i32 values use their low 32-bit representation, with upper bits zero.
// A new runtime is created and
// closed for every call, so memory, globals and compiled state never leak between tasks.
func Run(ctx context.Context, program Program, limits Limits, export string, args ...uint64) ([]uint64, error) {
	return run(ctx, program, limits, export, false, nil, args...)
}

func run(ctx context.Context, program Program, limits Limits, export string, artifact bool, consume func(context.Context, api.Module, []uint64) error, args ...uint64) ([]uint64, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	if err := program.validate(); err != nil {
		return nil, err
	}
	if export == "" || len(args) > maxValues {
		return nil, fmt.Errorf("%w: call", ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	config := wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(limits.MemoryPages)
	runtime := wazero.NewRuntimeWithConfig(ctx, config)
	defer func() { _ = runtime.Close(context.WithoutCancel(ctx)) }()
	compiled, err := runtime.CompileModule(ctx, program.Wasm)
	if err != nil {
		return nil, stopped(ctx, fmt.Errorf("%w: compile: %v", ErrInvalid, err))
	}
	definition := compiled.ExportedFunctions()[export]
	if definition == nil {
		return nil, fmt.Errorf("%w: unknown export", ErrInvalid)
	}
	params, outputs := definition.ParamTypes(), definition.ResultTypes()
	if artifact && (len(outputs) != 2 || outputs[0] != api.ValueTypeI32 || outputs[1] != api.ValueTypeI32 || compiled.ExportedMemories()["memory"] == nil) {
		return nil, fmt.Errorf("%w: artifact requires memory and (i32 offset, i32 length)", ErrInvalid)
	}
	if len(params) != len(args) || len(outputs) > maxValues {
		return nil, fmt.Errorf("%w: signature size", ErrInvalid)
	}
	for i, typ := range params {
		if (typ != api.ValueTypeI32 && typ != api.ValueTypeI64) ||
			(typ == api.ValueTypeI32 && args[i] > uint64(^uint32(0))) {
			return nil, fmt.Errorf("%w: integer argument", ErrInvalid)
		}
	}
	for _, typ := range outputs {
		if typ != api.ValueTypeI32 && typ != api.ValueTypeI64 {
			return nil, fmt.Errorf("%w: integer result", ErrInvalid)
		}
	}
	// Reject unsupported signatures before instantiation can run a WASM start
	// section. No caller can opt out of the bounded integer ABI.
	// Imports stay unresolved on purpose: a module asking for a host capability fails here.
	module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithStartFunctions())
	if err != nil {
		return nil, stopped(ctx, fmt.Errorf("%w: instantiate: %v", ErrInvalid, err))
	}
	function := module.ExportedFunction(export)
	if function == nil {
		return nil, fmt.Errorf("%w: unknown export", ErrInvalid)
	}
	results, err := function.Call(ctx, args...)
	if err != nil {
		return nil, stopped(ctx, fmt.Errorf("%w: %v", ErrFailed, err))
	}
	if len(results) != len(outputs) {
		return nil, ErrFailed
	}
	for i, typ := range outputs {
		if typ == api.ValueTypeI32 {
			results[i] = uint64(uint32(results[i]))
		}
	}
	if consume != nil {
		if err := consume(ctx, module, results); err != nil {
			return nil, err
		}
	}
	return results, nil
}

// stopped prefers the deadline over whatever error the interrupted module surfaced.
func stopped(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ErrTimeout
	}
	return err
}

package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
)

// Modules are assembled by hand so the tests need no compiler and the bytes stay reviewable.
var (
	header = []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	// (func (param i32 i32) (result i32) local.get 0 local.get 1 i32.add), exported "add".
	addModule = join(header,
		[]byte{0x01, 0x07, 0x01, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f},
		[]byte{0x03, 0x02, 0x01, 0x00},
		[]byte{0x07, 0x07, 0x01, 0x03, 'a', 'd', 'd', 0x00, 0x00},
		[]byte{0x0a, 0x09, 0x01, 0x07, 0x00, 0x20, 0x00, 0x20, 0x01, 0x6a, 0x0b})
	// (func (loop br 0)), exported "spin".
	spinModule = join(header,
		[]byte{0x01, 0x04, 0x01, 0x60, 0x00, 0x00},
		[]byte{0x03, 0x02, 0x01, 0x00},
		[]byte{0x07, 0x08, 0x01, 0x04, 's', 'p', 'i', 'n', 0x00, 0x00},
		[]byte{0x0a, 0x09, 0x01, 0x07, 0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x0b})
	// (func (result i32) i32.const 100 memory.grow), exported "grow", declaring `pages` of memory.
	growModule = func(pages byte) []byte {
		return join(header,
			[]byte{0x01, 0x05, 0x01, 0x60, 0x00, 0x01, 0x7f},
			[]byte{0x03, 0x02, 0x01, 0x00},
			[]byte{0x05, 0x03, 0x01, 0x00, pages},
			[]byte{0x07, 0x08, 0x01, 0x04, 'g', 'r', 'o', 'w', 0x00, 0x00},
			[]byte{0x0a, 0x09, 0x01, 0x07, 0x00, 0x41, 0xe4, 0x00, 0x40, 0x00, 0x0b})
	}
	// Imports env.leak and calls it from "run": a host capability the sandbox never provides.
	importModule = join(header,
		[]byte{0x01, 0x04, 0x01, 0x60, 0x00, 0x00},
		[]byte{0x02, 0x0c, 0x01, 0x03, 'e', 'n', 'v', 0x04, 'l', 'e', 'a', 'k', 0x00, 0x00},
		[]byte{0x03, 0x02, 0x01, 0x00},
		[]byte{0x07, 0x07, 0x01, 0x03, 'r', 'u', 'n', 0x00, 0x01},
		[]byte{0x0a, 0x06, 0x01, 0x04, 0x00, 0x10, 0x00, 0x0b})
	limits = Limits{Timeout: 2 * time.Second, MemoryPages: 2}
)

func join(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
func pin(wasm []byte) Program {
	sum := sha256.Sum256(wasm)
	return Program{Wasm: wasm, SHA256: hex.EncodeToString(sum[:])}
}

func TestRunComputesWithPinnedModule(t *testing.T) {
	got, err := Run(t.Context(), pin(addModule), limits, "add", 40, 2)
	if err != nil || len(got) != 1 || got[0] != 42 {
		t.Fatal("add", got, err)
	}
	// Every run starts from a fresh runtime, so repeating is independent and identical.
	again, err := Run(t.Context(), pin(addModule), limits, "add", 40, 2)
	if err != nil || again[0] != got[0] {
		t.Fatal("repeat run differs", again, err)
	}
}

func TestRunRejectsUnapprovedOrMalformedRequests(t *testing.T) {
	tampered := pin(addModule)
	tampered.Wasm = append([]byte{}, addModule...)
	tampered.Wasm[len(tampered.Wasm)-2] = 0x6b // i32.sub: same size, different behavior
	for name, c := range map[string]struct {
		program Program
		limits  Limits
		export  string
		args    []uint64
	}{
		"digest_mismatch": {tampered, limits, "add", []uint64{1, 2}},
		"no_digest":       {Program{Wasm: addModule}, limits, "add", []uint64{1, 2}},
		"empty":           {pin(nil), limits, "add", nil},
		"garbage":         {pin([]byte("not wasm")), limits, "add", nil},
		"unknown_export":  {pin(addModule), limits, "mul", []uint64{1, 2}},
		"wrong_arity":     {pin(addModule), limits, "add", []uint64{1}},
		"too_many_args":   {pin(addModule), limits, "add", make([]uint64, maxValues+1)},
		"no_timeout":      {pin(addModule), Limits{MemoryPages: 1}, "add", []uint64{1, 2}},
		"long_timeout":    {pin(addModule), Limits{Timeout: MaxTimeout + 1, MemoryPages: 1}, "add", []uint64{1, 2}},
		"no_memory":       {pin(addModule), Limits{Timeout: time.Second}, "add", []uint64{1, 2}},
		"huge_memory":     {pin(addModule), Limits{Timeout: time.Second, MemoryPages: MaxMemoryPages + 1}, "add", []uint64{1, 2}},
		"host_import":     {pin(importModule), limits, "run", nil},
		"memory_over_cap": {pin(growModule(3)), limits, "grow", nil},
	} {
		if got, err := Run(t.Context(), c.program, c.limits, c.export, c.args...); !errors.Is(err, ErrInvalid) || got != nil {
			t.Errorf("%s: got %v %v", name, got, err)
		}
	}
}

func TestRunStopsRunawayModulesAtDeadline(t *testing.T) {
	start := time.Now()
	got, err := Run(t.Context(), pin(spinModule), Limits{Timeout: 200 * time.Millisecond, MemoryPages: 1}, "spin")
	if !errors.Is(err, ErrTimeout) || got != nil {
		t.Fatal("infinite loop not stopped", got, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatal("deadline enforced too late", elapsed)
	}
	// A caller that cancels is also stopped promptly.
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := Run(ctx, pin(spinModule), limits, "spin"); !errors.Is(err, ErrTimeout) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestRunCapsMemoryGrowth(t *testing.T) {
	// memory.grow past the cap fails in-guest (returns -1) instead of consuming host memory.
	got, err := Run(t.Context(), pin(growModule(1)), limits, "grow")
	if err != nil || len(got) != 1 || uint32(got[0]) != 0xffffffff {
		t.Fatal("growth beyond limit allowed", got, err)
	}
}

// Small single-function fixtures keep signature tests independent of a compiler.
func signatureModule(params, results, instructions []byte) []byte {
	section := func(id byte, body []byte) []byte { return append([]byte{id, byte(len(body))}, body...) }
	typ := append([]byte{1, 0x60, byte(len(params))}, params...)
	typ = append(typ, byte(len(results)))
	typ = append(typ, results...)
	body := append([]byte{0}, instructions...)
	body = append(body, 0x0b)
	return join(header, section(1, typ), []byte{3, 2, 1, 0},
		[]byte{7, 7, 1, 3, 'r', 'u', 'n', 0, 0},
		section(10, append([]byte{1, byte(len(body))}, body...)))
}

func TestRunEnforcesIntegerOutputContract(t *testing.T) {
	for _, count := range []int{8, 9} {
		var results, code []byte
		for range count {
			results = append(results, 0x7e) // i64
			code = append(code, 0x42, 1)    // i64.const 1
		}
		got, err := Run(t.Context(), pin(signatureModule(nil, results, code)), limits, "run")
		if count == 8 {
			if err != nil || len(got) != count {
				t.Fatal("valid maximum rejected", got, err)
			}
		} else if !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatal("oversized result signature accepted", got, err)
		}
	}
	for _, tc := range []struct {
		module []byte
		args   []uint64
	}{
		{signatureModule([]byte{0x7d}, nil, nil), []uint64{0}},
		{signatureModule(nil, []byte{0x7d}, []byte{0x43, 0, 0, 0, 0}), nil},
	} {
		got, err := Run(t.Context(), pin(tc.module), limits, "run", tc.args...)
		if !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatal("floating point signature accepted", got, err)
		}
	}
	if got, err := Run(t.Context(), pin(addModule), limits, "add", 1<<32, 1); !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatal("i32 input silently truncated", got, err)
	}
	compare := signatureModule([]byte{0x7e, 0x7e}, []byte{0x7f}, []byte{0x20, 0, 0x20, 1, 0x54})
	got, err := Run(t.Context(), pin(compare), limits, "run", 9_000_000_000_000_000, 9_000_000_000_000_001)
	if err != nil || len(got) != 1 || got[0] != 1 {
		t.Fatal("i32 result not normalized", got, err)
	}
}

func TestInvalidSignatureRejectedBeforeStartSection(t *testing.T) {
	// run returns f32; a separate WASM start function loops forever. The
	// signature rejection must happen before the start section executes.
	module := join(header,
		[]byte{1, 8, 2, 0x60, 0, 1, 0x7d, 0x60, 0, 0},
		[]byte{3, 3, 2, 0, 1},
		[]byte{7, 7, 1, 3, 'r', 'u', 'n', 0, 0},
		[]byte{8, 1, 1},
		[]byte{10, 17, 2, 7, 0, 0x43, 0, 0, 0, 0, 0x0b,
			7, 0, 0x03, 0x40, 0x0c, 0, 0x0b, 0x0b})
	runtime := wazero.NewRuntime(t.Context())
	defer func() { _ = runtime.Close(context.Background()) }()
	if _, err := runtime.CompileModule(t.Context(), module); err != nil {
		t.Fatal("malformed start-section fixture", err)
	}
	got, err := Run(t.Context(), pin(module), Limits{Timeout: 100 * time.Millisecond, MemoryPages: 1}, "run")
	if !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatal("invalid signature ran start section", got, err)
	}
}

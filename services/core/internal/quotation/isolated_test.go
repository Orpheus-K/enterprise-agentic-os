package quotation

import (
	"context"
	"math/big"
	"math/rand/v2"
	"testing"
	"time"

	"agentos.local/core/internal/sandbox"
)

func TestIsolatedApprovalMatchesExactArithmetic(t *testing.T) {
	check := func(standard, offered, bps int64) {
		t.Helper()
		want := new(big.Int).Mul(big.NewInt(offered), big.NewInt(10000)).Cmp(
			new(big.Int).Mul(big.NewInt(standard), big.NewInt(bps))) < 0
		got, err := ApprovalInSandbox(t.Context(), standard, offered, bps)
		if err != nil || got != want {
			raw, rawErr := sandbox.Run(t.Context(), sandbox.Program{Wasm: approvalModule, SHA256: ApprovalModuleSHA256}, sandbox.Limits{Timeout: time.Second, MemoryPages: 1}, "approval", uint64(standard), uint64(offered), uint64(bps))
			t.Fatalf("%d/%d at %d bps: got %v err=%v want=%v raw=%v rawErr=%v", offered, standard, bps, got, err, want, raw, rawErr)
		}
	}
	for _, standard := range []int64{1, 3, 5, 9999, 10000, 10001, MaxMinor - 1, MaxMinor} {
		for _, bps := range []int64{1, 3333, 8000, 9999, 10000} {
			// Use independent arbitrary-precision arithmetic for boundary inputs.
			cutoff := new(big.Int).Mul(big.NewInt(standard), big.NewInt(bps))
			cutoff.Add(cutoff, big.NewInt(9999)).Div(cutoff, big.NewInt(10000))
			for _, offered := range []int64{1, cutoff.Int64() - 1, cutoff.Int64(), cutoff.Int64() + 1, standard} {
				if offered >= 1 && offered <= standard {
					check(standard, offered, bps)
				}
			}
		}
	}
	rng := rand.New(rand.NewPCG(42, 73))
	for range 100 {
		standard := 1 + rng.Int64N(MaxMinor)
		check(standard, 1+rng.Int64N(standard), 1+rng.Int64N(10000))
	}
}

func TestIsolatedApprovalRejectsInvalidAndCancelled(t *testing.T) {
	for _, args := range [][3]int64{{0, 1, 8000}, {MaxMinor + 1, 1, 8000}, {100, 0, 8000}, {100, 101, 8000}, {100, 1, 0}, {100, 1, 10001}, {-1, 1, 8000}} {
		if _, err := ApprovalInSandbox(t.Context(), args[0], args[1], args[2]); err == nil {
			t.Fatal("invalid rule input accepted", args)
		}
		// The WASM module must reject invalid values even without host guards.
		if _, err := sandbox.Run(t.Context(), sandbox.Program{Wasm: approvalModule, SHA256: ApprovalModuleSHA256},
			sandbox.Limits{Timeout: time.Second, MemoryPages: 1}, "approval",
			uint64(args[0]), uint64(args[1]), uint64(args[2])); err == nil {
			t.Fatal("module lacks its own bounds", args)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ApprovalInSandbox(ctx, 100, 75, 8000); err == nil {
		t.Fatal("cancelled computation accepted")
	}
}

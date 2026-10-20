package sandbox

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

// A single function can execute an unlimited loop while notifying Before only
// once. Function listeners cannot implement a WASM instruction budget.
func TestFunctionListenerDoesNotCountLoopInstructions(t *testing.T) {
	var entries atomic.Int32
	ctx := experimental.WithFunctionListenerFactory(t.Context(), experimental.FunctionListenerFactoryFunc(func(api.FunctionDefinition) experimental.FunctionListener {
		return experimental.FunctionListenerFunc(func(context.Context, api.Module, api.FunctionDefinition, []uint64, experimental.StackIterator) {
			entries.Add(1)
		})
	}))
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	defer runtime.Close(t.Context())
	module, err := runtime.Instantiate(ctx, spinModule)
	if err != nil {
		t.Fatal(err)
	}
	limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := module.ExportedFunction("spin").Call(limited); err == nil || limited.Err() == nil {
		t.Fatal("runaway loop not stopped by wall clock")
	}
	if entries.Load() != 1 {
		t.Fatalf("expected one function entry, got %d", entries.Load())
	}
}

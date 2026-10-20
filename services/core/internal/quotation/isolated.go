package quotation

import (
	"context"
	_ "embed"
	"time"

	"agentos.local/core/internal/sandbox"
)

// ApprovalModuleSHA256 pins the reviewed build artifact, not a caller-supplied
// module. A future runtime binding must include this digest in its revision.
const ApprovalModuleSHA256 = "b1899480373d1df63263bc8988fd36fe164fa9cec28c76c5dd436d53a9b49ec7"

//go:embed rules/approval.wasm
var approvalModule []byte

// ApprovalInSandbox computes only the exact discount rule. The caller must
// obtain authorized prices, validate totals, and separately enforce approvals.
// This does not replace New/Verify or select a runtime path implicitly.
func ApprovalInSandbox(ctx context.Context, standard, offered, bps int64) (bool, error) {
	if standard < 1 || standard > MaxMinor || offered < 1 || offered > standard || bps < 1 || bps > 10000 {
		return false, ErrInvalid
	}
	result, err := sandbox.Run(ctx, sandbox.Program{Wasm: approvalModule, SHA256: ApprovalModuleSHA256},
		sandbox.Limits{Timeout: time.Second, MemoryPages: 1}, "approval",
		uint64(standard), uint64(offered), uint64(bps))
	if err != nil {
		return false, err
	}
	if len(result) != 1 || result[0] > 1 {
		return false, sandbox.ErrFailed
	}
	return result[0] == 1, nil
}

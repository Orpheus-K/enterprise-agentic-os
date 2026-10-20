package conversation

import "errors"

type State string

const (
	WaitingApproval State = "waiting_approval"
	Rejected        State = "rejected"
	Expired         State = "expired"
	ToolPending     State = "tool_pending"
	ToolRunning     State = "tool_running"
	Pending         State = "pending"
	Claimed         State = "claimed"
	ModelRunning    State = "model_running"
	DeliveryPending State = "delivery_pending"
	DeliveryRunning State = "delivery_running"
	Succeeded       State = "succeeded"
	Failed          State = "failed"
	Unknown         State = "unknown"
)

var ErrLeaseLost = errors.New("task lease or step version lost")
var ErrInvalidTransition = errors.New("invalid task transition")

// ValidTransition requires an intent before each external call. Only checkpoints
// without an external intent are reclaimable.
func ValidTransition(from, to State) bool {
	switch from {
	case ToolPending:
		return to == ToolRunning || to == Failed
	case ToolRunning:
		return to == Unknown || to == Failed
	case Claimed:
		return to == ModelRunning || to == Failed
	case ModelRunning:
		return to == DeliveryPending || to == Unknown || to == Failed
	case DeliveryPending:
		return to == DeliveryRunning || to == Failed
	case DeliveryRunning:
		return to == Succeeded || to == Unknown
	}
	return false
}

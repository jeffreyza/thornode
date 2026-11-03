package ethereum

import "time"

// The mock RPC "mines" the approval immediately; skip the real 3-block wait.
func init() {
	waitForApprovalMined = func(time.Duration) {}
}

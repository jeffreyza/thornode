package blockscanner

import (
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
)

// Bridges in these tests talk to a THORNode that is absent or fails on purpose;
// keep the retries but skip retryablehttp's real backoff (1s..30s).
func init() {
	thorclient.BridgeRetryWaitMin = time.Millisecond
	thorclient.BridgeRetryWaitMax = 10 * time.Millisecond
}

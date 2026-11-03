//go:build mocknet
// +build mocknet

package thorchain

import (
	"os"
	"strconv"
)

// xmrMonovaultGenesisBootstrapDelayBlocks is how many blocks after genesis the
// first XMR monovault keygen is scheduled: 128, or XMR_MONOVAULT_GENESIS_DELAY_BLOCKS
// when set. Every node replays genesis, so all of them need the same value.
var xmrMonovaultGenesisBootstrapDelayBlocks = func() int64 {
	if v, err := strconv.ParseInt(os.Getenv("XMR_MONOVAULT_GENESIS_DELAY_BLOCKS"), 10, 64); err == nil && v > 0 {
		return v
	}
	return 128
}()

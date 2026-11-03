//go:build !mocknet
// +build !mocknet

package keeperv1

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// assertAdvSwapQueueIndexUnchanged is a no-op outside mocknet so the
// state-only write skips the index read it exists to avoid.
func (k KVStore) assertAdvSwapQueueIndexUnchanged(_ cosmos.Context, _ MsgSwap) {}

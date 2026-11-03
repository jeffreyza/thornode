//go:build mocknet
// +build mocknet

package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// assertAdvSwapQueueIndexUnchanged enforces the SetAdvSwapQueueItemState
// contract in tests and mocknet: the item must still be indexed under the key
// derived from msg, otherwise a caller changed an index-key field and the
// state-only write would orphan the old index entry.
func (k KVStore) assertAdvSwapQueueIndexUnchanged(ctx cosmos.Context, msg MsgSwap) {
	ok, err := k.HasAdvSwapQueueIndex(ctx, msg)
	if err != nil {
		panic(fmt.Sprintf("SetAdvSwapQueueItemState: fail to read index for %s-%d: %s", msg.Tx.ID, msg.Index, err))
	}
	if !ok {
		panic(fmt.Sprintf("SetAdvSwapQueueItemState: %s-%d not indexed under its current key; index-key fields changed", msg.Tx.ID, msg.Index))
	}
}

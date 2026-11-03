package observer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type claimFinalityCaptureClient struct {
	chainclients.ChainClient
	committed []types.TxInItem
}

func (c *claimFinalityCaptureClient) OnObservedTxIn(item types.TxInItem, _ int64) {
	c.committed = append(c.committed, item)
}

// The production callback used to forward the deck item's stale pre-final
// marker even after receiving a finalized event, so XMR never terminalized its
// exact signed claim after the ordinary pre-final -> final sequence.
func TestObservedXMRCommitCallbackPropagatesAuthoritativeFinality(t *testing.T) {
	for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
		t.Run(chain.String(), func(t *testing.T) {
			observer, _, cleanup := newPersistenceCallbackObserver(t)
			defer cleanup()
			client := &claimFinalityCaptureClient{}
			observer.chains[chain] = client
			vault := randomXMRObservedPubKey(t)
			item := &types.TxInItem{
				BlockHeight:         100,
				Tx:                  strings.Repeat("a", 64),
				Memo:                "OUT:" + strings.Repeat("b", 64),
				Sender:              "source-vault",
				To:                  "recipient",
				Coins:               common.NewCoins(common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(10))),
				Gas:                 common.Gas{common.NewCoin(chain.GetGasAsset(), cosmos.NewUint(1))},
				ObservedVaultPubKey: vault,
			}
			deck := &types.TxIn{Chain: chain, Filtered: true, ConfirmationRequired: 10, TxArray: []*types.TxInItem{item}}
			key := TxInKey(deck)
			observer.onDeck[key] = deck
			require.NoError(t, observer.storage.AddOrUpdateTx(deck))
			committed := common.NewObservedTx(common.Tx{
				ID: common.TxID(item.Tx), Chain: chain, FromAddress: common.Address(item.Sender),
				ToAddress: common.Address(item.To), Coins: item.Coins, Gas: item.Gas, Memo: item.Memo,
			}, 100, vault, 110)
			if chain.Equals(common.XMRChain) {
				committed.XmrMinedHeight = item.BlockHeight
			}

			observer.handleObservedTxCommitted(committed, false)
			require.Len(t, client.committed, 1)
			require.True(t, client.committed[0].CommittedUnFinalised)
			require.True(t, observer.onDeck[key].TxArray[0].CommittedUnFinalised)

			committed.BlockHeight = committed.FinaliseHeight
			observer.handleObservedTxCommitted(committed, false)
			require.Len(t, client.committed, 2)
			if chain.Equals(common.XMRChain) {
				require.False(t, client.committed[1].CommittedUnFinalised,
					"authoritative final XMR event must terminalize the previously unfinalized signing claim")
			} else {
				require.True(t, client.committed[1].CommittedUnFinalised, "non-XMR callback behavior must remain unchanged")
			}
			require.NotContains(t, observer.onDeck, key)
			stored, err := observer.storage.GetOnDeckTxs()
			require.NoError(t, err)
			require.Empty(t, stored)
		})
	}
}

package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestS10ExtractOpenAPIObservedTxPreservesMinedHeight(t *testing.T) {
	minedHeight, zero := int64(100), int64(0)
	for _, tc := range []struct {
		name   string
		chain  common.Chain
		height *int64
	}{
		{"mined-xmr", common.XMRChain, &minedHeight},
		{"absent-xmr", common.XMRChain, nil},
		{"zero-xmr", common.XMRChain, &zero},
		{"btc", common.BTCChain, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			txID, chain := types.GetRandomTxHash().String(), tc.chain.String()
			pk := types.GetRandomPubkeyForChain(tc.chain)
			vaultAddress, err := pk.GetAddress(tc.chain)
			require.NoError(t, err)
			sender, err := types.GetRandomPubkeyForChain(tc.chain).GetAddress(tc.chain)
			require.NoError(t, err)
			from, to := sender.String(), vaultAddress.String()
			pubKey, memo := pk.String(), "SWAP:THOR.RUNE"
			finalHeight := int64(120)
			apiTx := openapi.ObservedTx{
				Tx: openapi.Tx{
					Id: &txID, Chain: &chain, FromAddress: &from, ToAddress: &to, Memo: &memo,
					Coins: []openapi.Coin{{Asset: tc.chain.GetGasAsset().String(), Amount: "100"}},
					Gas:   []openapi.Coin{{Asset: tc.chain.GetGasAsset().String(), Amount: "1"}},
				},
				ObservedPubKey: &pubKey, ExternalObservedHeight: &finalHeight,
				ExternalConfirmationDelayHeight: &finalHeight, XmrMinedHeight: tc.height,
			}
			got, err := extractOpenAPIObservedTx(apiTx)
			require.NoError(t, err)
			require.Equal(t, finalHeight, got.BlockHeight)
			require.Equal(t, finalHeight, got.FinaliseHeight)
			if tc.height == nil {
				require.Zero(t, got.XmrMinedHeight)
			} else {
				require.Equal(t, *tc.height, got.XmrMinedHeight)
			}
			if tc.height != nil && *tc.height > 0 {
				msg := types.NewMsgObservedTxIn(common.ObservedTxs{*got}, types.GetRandomBech32Addr())
				require.NoError(t, msg.ValidateBasic(), "--txids must retain the mined identity required by inbound admission")
			}
		})
	}
}

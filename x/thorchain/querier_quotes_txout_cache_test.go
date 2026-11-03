package thorchain

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// QuoteSwap shares txout store reads through a keeper.TxOutCache. The response
// must not depend on it, and a quote run on behalf of a contract
// (CtxWASMQuery) must not use it: its store reads are gas inside a tx.
func TestQuoteSwapTxOutCache(t *testing.T) {
	f := newPerfFixture(t)
	height := f.ctx.BlockHeight() + 1
	f.seedPendingOutbounds(t, height)
	qs := NewQueryServerImpl(f.mgr, nil, cosmos.KeybaseStore{})
	h := strconv.FormatInt(height, 10)

	quote := func(req types.QueryQuoteSwapRequest, wasm, uncached bool) ([]byte, string, uint64) {
		ctx := f.branch(height)
		f.mgr.gasMgr.BeginBlock()
		f.mgr.obMgr = newObserverMgr()
		if wasm {
			ctx = ctx.WithValue(constants.CtxWASMQuery, true)
		}
		if uncached {
			// queryQuoteSwap keeps an attached cache: a disabled one reads
			// everything from the store, as before the cache existed
			ctx = keeper.WithTxOutCache(ctx)
			keeper.TxOutCacheFromContext(ctx).Disable()
		}
		res, err := qs.QuoteSwap(ctx, &req)
		gas := ctx.GasMeter().GasConsumed()
		if err != nil {
			return nil, err.Error(), gas
		}
		bz, err := res.Marshal()
		require.NoError(t, err)
		return bz, "", gas
	}

	for _, tc := range []struct {
		name string
		req  types.QueryQuoteSwapRequest
	}{
		{"l1-to-l1", types.QueryQuoteSwapRequest{
			FromAsset:   "BTC.BTC",
			ToAsset:     "ETH.ETH",
			Amount:      "10000000",
			Destination: perfAddr(t, common.ETHChain, "quote/l1-to-l1").String(),
		}},
		{"rune-to-l1", types.QueryQuoteSwapRequest{
			FromAsset:   "THOR.RUNE",
			ToAsset:     "BTC.BTC",
			Amount:      "100000000000",
			Destination: perfAddr(t, common.BTCChain, "quote/rune-to-l1").String(),
		}},
		{"l1-to-rune", types.QueryQuoteSwapRequest{
			FromAsset:   "ETH.ETH",
			ToAsset:     "THOR.RUNE",
			Amount:      "100000000",
			Destination: perfAddr(t, common.THORChain, "quote/l1-to-rune").String(),
		}},
		{"streaming", types.QueryQuoteSwapRequest{
			FromAsset:         "BTC.BTC",
			ToAsset:           "ETH.ETH",
			Amount:            "100000000",
			Destination:       perfAddr(t, common.ETHChain, "quote/streaming").String(),
			StreamingInterval: "1",
		}},
		{"too-small", types.QueryQuoteSwapRequest{
			FromAsset: "BTC.BTC",
			ToAsset:   "ETH.ETH",
			Amount:    "10001",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Height = h

			// API quote: same response, fewer store reads
			want, wantErr, uncachedGas := quote(req, false, true)
			got, gotErr, cachedGas := quote(req, false, false)
			require.Equal(t, wantErr, gotErr)
			require.Equal(t, want, got)
			if wantErr == "" {
				require.NotEmpty(t, got)
				require.Less(t, cachedGas, uncachedGas)
			}

			// contract quote: same response and same gas as without the cache
			want, wantErr, uncachedGas = quote(req, true, true)
			got, gotErr, gas := quote(req, true, false)
			require.Equal(t, wantErr, gotErr)
			require.Equal(t, want, got)
			require.Equal(t, uncachedGas, gas)
		})
	}
}

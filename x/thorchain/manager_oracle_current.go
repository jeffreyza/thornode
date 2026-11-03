package thorchain

import (
	"github.com/cosmos/cosmos-sdk/telemetry"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type OracleMgr struct {
	keeper keeper.Keeper
}

// newOracleMgr creates a new instance of OracleMgr
func newOracleMgr(
	keeper keeper.Keeper,
) *OracleMgr {
	return &OracleMgr{
		keeper: keeper,
	}
}

func (om *OracleMgr) BeginBlock(ctx cosmos.Context) error {
	iterator := om.keeper.GetPriceIterator(ctx)
	defer iterator.Close()

	// Collect keys to delete after iteration so a corrupted entry (whose
	// Symbol cannot be recovered via Unmarshal) is still removed from the
	// store via its raw key. Without this, malformed entries would persist
	// forever and produce per-block error logs.
	var keys [][]byte
	for ; iterator.Valid(); iterator.Next() {
		key := append([]byte(nil), iterator.Key()...)
		keys = append(keys, key)
		var price OraclePrice
		if err := om.keeper.Cdc().Unmarshal(iterator.Value(), &price); err != nil {
			ctx.Logger().Error("failed to unmarshal price, removing malformed entry by key", "key", key, "error", err)
		}
	}

	// The price store is wiped every block and repopulated by
	// PriceFeedQuorumBatchHandler, so this is exactly the number of symbols that
	// reached consensus last block. 0 and staying there means the feed is dead -
	// which thornode_oracle_price cannot show, because it is only written from
	// inside the handler and simply freezes when the handler stops running.
	telemetry.SetGauge(float32(len(keys)), "thornode", "oracle", "prices_set")

	for _, key := range keys {
		om.keeper.DeleteKey(ctx, key)
	}

	return nil
}

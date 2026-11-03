// Package gascontract runs THORNode's half of the gas-unit contract for
// bifrost chain-client tests: a scanner posts a network fee, and THORNode's
// GasMgr turns it into the MaxGas and GasRate every outbound on that chain is
// scheduled with. Tests feed those into the client's real outbound path.
//
// It imports x/thorchain, so only test code may import it.
package gascontract

import (
	"cosmossdk.io/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// GasDetails stores fee as THORNode's network-fee handler does once it
// reaches consensus, then returns THORNode's GasMgr.GetGasDetails for the
// chain: the MaxGas coin and GasRate that TxOutStorage writes on outbounds.
func GasDetails(fee common.NetworkFee) (common.Coin, int64, error) {
	k := networkFeeKeeper{fee: types.NewNetworkFee(fee.Chain, fee.TransactionSize, fee.TransactionRate)}
	gasMgr, err := thorchain.GetGasManager(constants.SWVersion, k)
	if err != nil {
		return common.NoCoin, 0, err
	}
	return gasMgr.GetGasDetails(cosmos.Context{}.WithLogger(log.NewNopLogger()), fee.Chain)
}

type networkFeeKeeper struct {
	keeper.KVStoreDummy
	fee types.NetworkFee
}

func (k networkFeeKeeper) GetNetworkFee(_ cosmos.Context, chain common.Chain) (types.NetworkFee, error) {
	if !chain.Equals(k.fee.Chain) {
		return types.NetworkFee{}, nil
	}
	return k.fee, nil
}

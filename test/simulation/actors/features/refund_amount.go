package features

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// refundAmount keeps the historical 1% sizing for deep pools while allowing
// three outbound fees for shallow pools and fee changes before observation.
func refundAmount(poolDepth, outboundFee cosmos.Uint) cosmos.Uint {
	amount := poolDepth.QuoUint64(100)
	minimum := outboundFee.MulUint64(3)
	if amount.LT(minimum) {
		return minimum
	}
	return amount
}

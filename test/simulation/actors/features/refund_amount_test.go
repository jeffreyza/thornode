package features

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestRefundAmountCoversShallowPoolFee(t *testing.T) {
	// Run C deposited 31,055,006 BASE token units (1% depth), below its
	// 48,883,897-unit outbound fee, so no refund could be scheduled.
	fee := cosmos.NewUint(48_883_897)
	amount := refundAmount(cosmos.NewUint(3_105_500_600), fee)
	if !amount.GT(fee) || !amount.Equal(fee.MulUint64(3)) {
		t.Fatalf("refund deposit %s must cover fee %s with headroom", amount, fee)
	}
}

func TestRefundAmountKeepsDeepPoolSizing(t *testing.T) {
	amount := refundAmount(cosmos.NewUint(10_000_000_000), cosmos.NewUint(1_000))
	if !amount.Equal(cosmos.NewUint(100_000_000)) {
		t.Fatalf("deep pool deposit = %s, want 1%% of pool", amount)
	}
}

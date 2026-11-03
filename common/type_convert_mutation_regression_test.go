package common

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestSecurityMutationSafeSubConservation(t *testing.T) {
	for _, tc := range []struct{ balance, debit, want uint64 }{
		{11, 4, 7}, {4, 4, 0}, {4, 11, 0}, {0, 1, 0},
	} {
		got := SafeSub(cosmos.NewUint(tc.balance), cosmos.NewUint(tc.debit))
		if !got.Equal(cosmos.NewUint(tc.want)) {
			t.Errorf("SafeSub(%d, %d) = %s, want %d", tc.balance, tc.debit, got, tc.want)
		}
	}
}

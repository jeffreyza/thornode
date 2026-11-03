package signing_test

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestSecurityMutationSigningHaltBoundary(t *testing.T) {
	for _, tc := range []struct {
		key            string
		value, height  int64
		halted, global bool
	}{
		{"HALTSIGNING", 100, 99, false, false},
		{"HALTSIGNING", 100, 100, true, true},
		{"HaltSigningBTC", 100, 100, true, false},
		{"HaltSigningBTC", 100, 99, false, false},
	} {
		b := &fakeBridge{mimirs: map[string]int64{tc.key: tc.value}}
		got, global, err := signing.CheckSigningHalt(b, common.BTCChain, tc.height)
		if err != nil || got != tc.halted || global != tc.global {
			t.Errorf("%s=%d at %d: halted=%v global=%v err=%v", tc.key, tc.value, tc.height, got, global, err)
		}
	}
}

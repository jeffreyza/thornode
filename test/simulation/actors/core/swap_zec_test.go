package core

import (
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestSwapZECAddressFormatSurvivesUserRetry(t *testing.T) {
	for _, useTex := range []bool{false, true} {
		a := newSwapBalanceTestActor(common.ETHAsset, common.ZECAsset, 1, 2)
		a.useTexAddress = useTex
		a.requireOutbound = true
		user := newTestSwapUser(t, "zec", a.from, sdkmath.NewUint(10000), a.to, sdkmath.ZeroUint())
		address, err := user.PubKey(common.ZECChain).GetAddress(common.ZECChain)
		if err != nil {
			t.Fatal(err)
		}
		if useTex {
			address, err = address.ToTexAddress()
			if err != nil {
				t.Fatal(err)
			}
		}
		// Keep the address format fixed when user selection is retried.
		for attempt := 0; attempt < 2; attempt++ {
			result := a.acquireUserForAmount(&simtypes.OpConfig{Users: []*simtypes.User{user}}, sdkmath.NewUint(100), false)
			if !result.Continue || result.Finish || result.Error != nil {
				t.Fatalf("useTex=%v: acquisition failed: %+v", useTex, result)
			}
			if !a.toAddress.Equals(address) {
				t.Fatalf("useTex=%v: destination = %s, want %s", useTex, a.toAddress, address)
			}
			a.releaseUserSelection()
		}
	}
}

func TestSwapZECVerifyOutboundRequiresCanonicalAddresses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		useTex      bool
		actionTex   bool
		outboundTex bool
	}{
		{name: "transparent destination"},
		{name: "TEX destination", useTex: true},
		{name: "unnormalized action", useTex: true, actionTex: true},
		{name: "unnormalized observation", useTex: true, outboundTex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newSwapBalanceTestActor(common.ETHAsset, common.ZECAsset, 1, 2)
			a.user = newTestSwapUser(t, "zec", a.from, sdkmath.NewUint(1000), a.to, sdkmath.NewUint(90))
			if !a.user.Acquire() {
				t.Fatal("failed to lock swap user")
			}
			t.Cleanup(func() {
				if a.user.IsLocked() {
					a.user.Release()
				}
			})
			address, err := a.user.PubKey(common.ZECChain).GetAddress(common.ZECChain)
			if err != nil {
				t.Fatal(err)
			}
			tex, err := address.ToTexAddress()
			if err != nil {
				t.Fatal(err)
			}
			a.toAddress = address
			if tc.useTex {
				a.toAddress = tex
			}
			a.toBalance = sdkmath.ZeroUint()
			a.minExpected, a.maxExpected = sdkmath.NewUint(100), sdkmath.NewUint(100)
			a.getSwapStages = func(string) (openapi.TxStagesResponse, error) {
				return openapi.TxStagesResponse{OutboundSigned: &openapi.OutboundSignedStage{Completed: true}}, nil
			}
			a.getSwapDetails = func(string) (openapi.TxDetailsResponse, error) {
				actionAddress, outboundAddress := address, address
				if tc.actionTex {
					actionAddress = tex
				}
				if tc.outboundTex {
					outboundAddress = tex
				}
				return openapi.TxDetailsResponse{
					Actions: []openapi.TxOutItem{{Chain: "ZEC", ToAddress: actionAddress.String(), Memo: openapi.PtrString("OUT:inbound")}},
					OutTxs: []openapi.Tx{{
						Chain: openapi.PtrString("ZEC"), ToAddress: openapi.PtrString(outboundAddress.String()),
						Coins: []openapi.Coin{{Asset: "ZEC.ZEC", Amount: "90"}},
						Gas:   []openapi.Coin{{Asset: "ZEC.ZEC", Amount: "10"}},
					}},
				}, nil
			}
			result := a.verifyOutbound(&simtypes.OpConfig{})
			wantErr := tc.actionTex || tc.outboundTex
			if !result.Finish || result.Continue || (result.Error != nil) != wantErr {
				t.Fatalf("want terminal verification with error=%v, got %+v", wantErr, result)
			}
			if a.user.IsLocked() {
				t.Fatal("terminal verification did not release swap user")
			}
		})
	}
}

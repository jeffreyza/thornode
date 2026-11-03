package core

import (
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestSwapVerifyOutboundPinnedVault(t *testing.T) {
	const expected = "expected-vault-key"
	const other = "other-vault-key"
	for _, tc := range []struct {
		name    string
		asset   common.Asset
		primary *string
		frost   *string
		wantErr string
	}{
		{
			name: "XMR uses FROST with distinct primary key", asset: common.XMRAsset,
			primary: openapi.PtrString(other), frost: openapi.PtrString(expected),
		},
		{
			name: "XMR rejects wrong FROST despite matching primary key", asset: common.XMRAsset,
			primary: openapi.PtrString(expected), frost: openapi.PtrString(other),
			wantErr: "expected outbound vault " + expected + ", got " + other,
		},
		{
			name: "XMR rejects missing FROST despite matching primary key", asset: common.XMRAsset,
			primary: openapi.PtrString(expected),
			wantErr: "got empty vault pubkey",
		},
		{
			name: "XMR rejects empty FROST despite matching primary key", asset: common.XMRAsset,
			primary: openapi.PtrString(expected), frost: openapi.PtrString(""),
			wantErr: "got empty vault pubkey",
		},
		{
			name: "BTC uses primary with distinct FROST key", asset: common.BTCAsset,
			primary: openapi.PtrString(expected), frost: openapi.PtrString(other),
		},
		{
			name: "BTC rejects wrong primary despite matching FROST key", asset: common.BTCAsset,
			primary: openapi.PtrString(other), frost: openapi.PtrString(expected),
			wantErr: "expected outbound vault " + expected + ", got " + other,
		},
		{
			name: "BTC rejects missing primary despite matching FROST key", asset: common.BTCAsset,
			frost: openapi.PtrString(expected), wantErr: "got empty vault pubkey",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newSwapBalanceTestActor(common.ETHAsset, tc.asset, 1, 2)
			a.expectedOutboundVault = common.PubKey(expected)
			a.swapTxID = "swap-inbound"
			a.toBalance = sdkmath.ZeroUint()
			a.minExpected = sdkmath.NewUint(100)
			a.maxExpected = sdkmath.NewUint(100)
			a.user = newTestSwapUser(t, "outbound", a.from, sdkmath.NewUint(1000), a.to, sdkmath.NewUint(90))
			if !a.user.Acquire() {
				t.Fatal("failed to lock swap user")
			}
			t.Cleanup(func() {
				if a.user.IsLocked() {
					a.user.Release()
				}
			})

			var stagesCalls, detailsCalls int
			a.getSwapStages = func(txID string) (openapi.TxStagesResponse, error) {
				stagesCalls++
				if txID != a.swapTxID {
					t.Fatalf("queried stages for %q instead of swap %q", txID, a.swapTxID)
				}
				return openapi.TxStagesResponse{OutboundSigned: &openapi.OutboundSignedStage{Completed: true}}, nil
			}
			a.getSwapDetails = func(txID string) (openapi.TxDetailsResponse, error) {
				detailsCalls++
				if txID != a.swapTxID {
					t.Fatalf("queried details for %q instead of swap %q", txID, a.swapTxID)
				}
				return openapi.TxDetailsResponse{
					Actions: []openapi.TxOutItem{{
						Chain: tc.asset.Chain.String(), VaultPubKey: tc.primary, VaultPubKeyFrost: tc.frost,
						Memo: openapi.PtrString("OUT:" + a.swapTxID),
					}},
					OutTxs: []openapi.Tx{{
						Id: openapi.PtrString("swap-outbound"), Chain: openapi.PtrString(tc.asset.Chain.String()),
						Coins: []openapi.Coin{{Asset: tc.asset.String(), Amount: "90"}},
						Gas:   []openapi.Coin{{Asset: tc.asset.String(), Amount: "10"}},
					}},
				}, nil
			}

			result := a.verifyOutbound(&simtypes.OpConfig{})
			if !result.Finish || result.Continue {
				t.Fatalf("expected terminal verification, got %+v", result)
			}
			if tc.wantErr == "" {
				if result.Error != nil {
					t.Fatalf("correct vault failed verification: %v", result.Error)
				}
			} else if result.Error == nil || !strings.Contains(result.Error.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, result.Error)
			}
			if stagesCalls != 1 || detailsCalls != 1 {
				t.Fatalf("expected one query each, got stages=%d details=%d", stagesCalls, detailsCalls)
			}
			if a.user.IsLocked() {
				t.Fatal("terminal verification did not release swap user")
			}
		})
	}
}

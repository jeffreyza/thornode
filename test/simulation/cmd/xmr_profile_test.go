package main

import (
	"errors"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
)

func TestXMRSimulationProfileRequiredReadiness(t *testing.T) {
	for _, tc := range []struct {
		name     string
		required bool
		address  common.Address
		err      error
		wantErr  bool
	}{
		{name: "optional absent", err: errors.New("no inbound")},
		{name: "required absent", required: true, err: errors.New("no inbound"), wantErr: true},
		{name: "required empty", required: true, wantErr: true},
		{name: "required ready", required: true, address: common.Address("xmr-inbound")},
		{name: "optional ready", address: common.Address("xmr-inbound")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := acommon.XMRChainEnabled()
			t.Cleanup(func() { acommon.SetXMRChainEnabled(previous) })
			var waited time.Duration
			err := configureXMRSimulationProfile(tc.required, func(timeout time.Duration) (common.Address, error) {
				waited = timeout
				return tc.address, tc.err
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want failure = %v", err, tc.wantErr)
			}
			wantWait := 30 * time.Second
			if tc.required {
				wantWait = 8 * time.Minute
			}
			if waited != wantWait {
				t.Fatalf("wait budget = %s, want %s", waited, wantWait)
			}
			if acommon.XMRChainEnabled() != !tc.address.IsEmpty() {
				t.Fatal("XMR coverage must reflect successful inbound readiness")
			}
		})
	}
}

func TestXMRSimulationInboundRetriesBeforeReadiness(t *testing.T) {
	attempts := 0
	sleeps := 0
	want := common.Address("xmr-inbound")
	got, err := waitForXMRSimulationInbound(time.Minute, func(chain common.Chain) (common.Address, *common.Address, error) {
		if !chain.Equals(common.XMRChain) {
			t.Fatal("queried another chain")
		}
		attempts++
		if attempts < 3 {
			return common.NoAddress, nil, errors.New("ceremony pending")
		}
		return want, nil, nil
	}, func(time.Duration) { sleeps++ })
	if err != nil || !got.Equals(want) || attempts != 3 || sleeps != 2 {
		t.Fatalf("address=%s error=%v attempts=%d sleeps=%d", got, err, attempts, sleeps)
	}
}

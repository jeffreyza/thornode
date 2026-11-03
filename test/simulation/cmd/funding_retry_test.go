package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type fundingRetryClient struct {
	LiteChainClient
	sign      func(SimTx) ([]byte, error)
	broadcast func([]byte) (string, error)
}

func (c *fundingRetryClient) SignTx(tx SimTx) ([]byte, error) { return c.sign(tx) }
func (c *fundingRetryClient) BroadcastTx(payload []byte) (string, error) {
	return c.broadcast(payload)
}

func TestSignAndBroadcastFundingTx(t *testing.T) {
	signErr := errors.New("fail to get unspent UTXO: connection refused")
	rejectedErr := errors.New("funding transaction rejected")
	uncertainErr := fmt.Errorf("%w: broadcast timed out", ErrSimTxUncertain)
	cases := []struct {
		name           string
		chain          common.Chain
		signFailures   int
		broadcastErr   error
		broadcastTxID  string
		wantSigns      int
		wantBroadcasts int
		wantSleeps     int
		wantTxID       string
		wantErr        error
	}{
		{name: "dash success", chain: common.DASHChain, wantSigns: 1, wantBroadcasts: 1, wantTxID: "confirmed"},
		{name: "dash signing recovers", chain: common.DASHChain, signFailures: 2, wantSigns: 3, wantBroadcasts: 1, wantSleeps: 2, wantTxID: "confirmed"},
		{name: "dash stops after five attempts", chain: common.DASHChain, signFailures: 5, wantSigns: 5, wantSleeps: 4, wantErr: signErr},
		{name: "dash rejected broadcast retries", chain: common.DASHChain, broadcastErr: rejectedErr, wantSigns: 2, wantBroadcasts: 2, wantSleeps: 1, wantTxID: "confirmed"},
		{name: "dash uncertain broadcast stops", chain: common.DASHChain, broadcastErr: uncertainErr, broadcastTxID: "unresolved", wantSigns: 1, wantBroadcasts: 1, wantTxID: "unresolved", wantErr: ErrSimTxUncertain},
		{name: "dash uncertainty without txid stops", chain: common.DASHChain, broadcastErr: uncertainErr, wantSigns: 1, wantBroadcasts: 1, wantErr: ErrSimTxUncertain},
		{name: "other chains keep their retry policy", chain: common.BTCChain, signFailures: 1, wantSigns: 1, wantErr: signErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signs, broadcasts, sleeps := 0, 0, 0
			client := &fundingRetryClient{
				sign: func(SimTx) ([]byte, error) {
					signs++
					if signs <= tc.signFailures {
						return nil, signErr
					}
					return []byte(fmt.Sprintf("signed-%d", signs)), nil
				},
				broadcast: func([]byte) (string, error) {
					broadcasts++
					if broadcasts == 1 && tc.broadcastErr != nil {
						return tc.broadcastTxID, tc.broadcastErr
					}
					return "confirmed", nil
				},
			}
			txID, err := signAndBroadcastFundingTx(client, SimTx{Chain: tc.chain}, func(delay time.Duration) {
				sleeps++
				if delay != 2*time.Second {
					t.Fatalf("unexpected funding retry delay: %s", delay)
				}
			})
			if !errors.Is(err, tc.wantErr) || txID != tc.wantTxID {
				t.Fatalf("got txid=%q err=%v, want txid=%q err=%v", txID, err, tc.wantTxID, tc.wantErr)
			}
			if signs != tc.wantSigns || broadcasts != tc.wantBroadcasts || sleeps != tc.wantSleeps {
				t.Fatalf("got signs=%d broadcasts=%d sleeps=%d, want signs=%d broadcasts=%d sleeps=%d",
					signs, broadcasts, sleeps, tc.wantSigns, tc.wantBroadcasts, tc.wantSleeps)
			}
		})
	}
}

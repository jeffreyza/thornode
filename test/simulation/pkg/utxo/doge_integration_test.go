package utxo

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// Run only against an isolated mocknet dogecoind whose initial blocks are mined
// to the simulation master address. Set DOGE_SIM_INTEGRATION_RPC_URL (for
// example http://thorchain:password@localhost:18332), then run:
// go test -tags mocknet ./pkg/utxo -run TestDogeStalledBroadcastIsNotDuplicated -v
//
// This reproduces the late DOGE deposit in job 16347608704: sendrawtransaction
// did not answer within the client timeout, and signing again sent a second
// deposit. Each case stalls the first broadcast and requires the recipient to
// be paid exactly once.
func TestDogeStalledBroadcastIsNotDuplicated(t *testing.T) {
	endpoint := os.Getenv("DOGE_SIM_INTEGRATION_RPC_URL")
	if endpoint == "" {
		t.Skip("set DOGE_SIM_INTEGRATION_RPC_URL to an isolated mocknet dogecoind")
	}

	for _, tc := range []struct {
		name string
		// forwardFirst sends the stalled broadcast to dogecoind (accepted but
		// unanswered) instead of dropping it (never received)
		forwardFirst bool
		wantSends    int
		recipient    string
	}{
		{name: "accepted but unanswered", forwardFirst: true, wantSends: 1, recipient: "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"},
		{name: "dropped before the daemon", forwardFirst: false, wantSends: 2, recipient: "zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo wrong"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			sends := 0
			payloads := make(map[string]bool)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
					return
				}
				var rpcRequest struct {
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				if err = json.Unmarshal(body, &rpcRequest); err != nil {
					t.Error(err)
					return
				}
				stall := false
				if rpcRequest.Method == "sendrawtransaction" {
					mu.Lock()
					sends++
					payloads[string(rpcRequest.Params[0])] = true
					stall = sends == 1
					mu.Unlock()
				}
				if stall && !tc.forwardFirst {
					time.Sleep(utxoStartupRPCTimeout + time.Second)
					return
				}
				upstream, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
				if err != nil {
					t.Error(err)
					return
				}
				upstream.Header = req.Header.Clone()
				res, err := http.DefaultClient.Do(upstream)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				defer res.Body.Close()
				response, err := io.ReadAll(res.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if stall {
					time.Sleep(utxoStartupRPCTimeout + time.Second)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(res.StatusCode)
				_, _ = w.Write(response)
			}))
			defer proxy.Close()

			masterClient, err := NewClient(common.DOGEChain, proxy.URL, newTestKeys(t, "master", testMnemonic))
			if err != nil {
				t.Fatal(err)
			}
			userClient, err := NewClient(common.DOGEChain, endpoint, newTestKeys(t, "user", tc.recipient))
			if err != nil {
				t.Fatal(err)
			}
			user, ok := userClient.(*Client)
			if !ok {
				t.Fatalf("unexpected client type %T", userClient)
			}
			before, err := user.GetAccount(nil)
			if err != nil {
				t.Fatal(err)
			}

			amount := sdkmath.NewUint(10 * common.One)
			tx := SimTx{
				Chain:     common.DOGEChain,
				ToAddress: user.address,
				Coin:      common.NewCoin(common.DOGEAsset, amount),
				Memo:      "SIMULATION:stalled-broadcast",
			}
			txid, err := SignAndBroadcastTx(masterClient, tx)
			if err != nil {
				t.Fatalf("stalled broadcast was not recovered: %v", err)
			}

			target := before.Coins.GetCoin(common.DOGEAsset).Amount.Add(amount)
			deadline := time.Now().Add(30 * time.Second)
			for {
				account, acctErr := user.GetAccount(nil)
				if acctErr != nil {
					t.Fatal(acctErr)
				}
				got := account.Coins.GetCoin(common.DOGEAsset).Amount
				if got.GT(target) {
					t.Fatalf("duplicate payment: got %s, want %s", got, target)
				}
				if got.Equal(target) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("payment %s did not arrive: got %s, want %s", txid, got, target)
				}
				time.Sleep(200 * time.Millisecond)
			}
			// a duplicate would land within a few blocks of the first payment
			time.Sleep(3 * time.Second)
			account, err := user.GetAccount(nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := account.Coins.GetCoin(common.DOGEAsset).Amount; !got.Equal(target) {
				t.Fatalf("duplicate payment: got %s, want %s", got, target)
			}

			mu.Lock()
			defer mu.Unlock()
			if sends != tc.wantSends || len(payloads) != 1 {
				t.Fatalf("sends=%d distinct payloads=%d", sends, len(payloads))
			}
			t.Logf("tx %s paid once after a stalled broadcast (sends=%d)", txid, sends)
		})
	}
}

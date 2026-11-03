package utxo

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// Run only against a fresh, isolated Zebra owned by the test operator.
// Set ZEC_SIM_INTEGRATION_RPC_URL, then run:
// go test -tags mocknet ./pkg/utxo -run TestZecIsolatedFunding -v
func TestZecIsolatedFunding(t *testing.T) {
	endpoint := os.Getenv("ZEC_SIM_INTEGRATION_RPC_URL")
	if endpoint == "" {
		t.Skip("set ZEC_SIM_INTEGRATION_RPC_URL to an isolated, bootstrapped Zebra")
	}
	transport := &http.Client{Timeout: 15 * time.Second}
	readyDeadline := time.Now().Add(2 * time.Minute)
	for {
		res, err := transport.Post(endpoint, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"getblockcount","params":[]}`))
		ready := false
		if err == nil {
			var response struct {
				Result *int64 `json:"result"`
				Error  any    `json:"error"`
			}
			decodeErr := json.NewDecoder(res.Body).Decode(&response)
			ready = res.StatusCode == http.StatusOK && decodeErr == nil && response.Error == nil && response.Result != nil && *response.Result > 400
			_ = res.Body.Close()
		}
		if ready {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("Zebra did not finish warm-up")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var mu sync.Mutex
	sends, cancellations := 0, 0
	payloads := make(map[string]bool)
	// Forward the first send, but lose its response as verification-cancelled.
	// Recovery must find that exact transaction without creating another payment.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var rpcRequest struct {
			ID     any               `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &rpcRequest); err != nil {
			t.Error(err)
			return
		}
		res, err := transport.Post(endpoint, "application/json", bytes.NewReader(body))
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
		mu.Lock()
		defer mu.Unlock()
		if rpcRequest.Method == "sendrawtransaction" {
			sends++
			payloads[string(rpcRequest.Params[0])] = true
			var upstream struct {
				Result string `json:"result"`
				Error  any    `json:"error"`
			}
			decodeErr := json.Unmarshal(response, &upstream)
			if cancellations == 0 && res.StatusCode == http.StatusOK && decodeErr == nil && upstream.Error == nil && upstream.Result != "" {
				cancellations++
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpcRequest.ID, "error": map[string]any{"code": -1, "message": "verification cancelled"}})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(response)
	}))
	defer proxy.Close()
	masterClient, err := NewClient(common.ZECChain, proxy.URL, newTestKeys(t, "master", testMnemonic))
	if err != nil {
		t.Fatal(err)
	}
	master, ok := masterClient.(*Client)
	if !ok {
		t.Fatal("unexpected master client type")
	}
	mnemonics := []string{
		"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
		"zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo zoo wrong",
	}
	users := make([]*Client, 0, len(mnemonics))
	for _, mnemonic := range mnemonics {
		userClient, err := NewClient(common.ZECChain, endpoint, newTestKeys(t, "user", mnemonic))
		if err != nil {
			t.Fatal(err)
		}
		user, ok := userClient.(*Client)
		if !ok {
			t.Fatal("unexpected user client type")
		}
		account, err := user.GetAccount(nil)
		if err != nil || !account.Coins.GetCoin(common.ZECAsset).Amount.IsZero() {
			t.Fatalf("integration requires fresh user state: %v %v", account, err)
		}
		users = append(users, user)
	}
	hashes := make(map[string]bool)
	for round := 1; round <= 20; round++ {
		for _, user := range users {
			tx := SimTx{Chain: common.ZECChain, ToAddress: user.address, Coin: common.NewCoin(common.ZECAsset, sdkmath.NewUint(common.One).MulUint64(5)), Memo: "SIMULATION:startup-race"}
			hash, err := SignAndBroadcastTx(master, tx)
			if err != nil {
				t.Fatal(err)
			}
			if hashes[hash] {
				t.Fatalf("repeated funding txid %s", hash)
			}
			hashes[hash] = true
			target := sdkmath.NewUint(common.One).MulUint64(uint64(round * 5))
			deadline := time.Now().Add(30 * time.Second)
			for {
				account, err := user.GetAccount(nil)
				if err != nil {
					t.Fatal(err)
				}
				amount := account.Coins.GetCoin(common.ZECAsset).Amount
				if amount.GT(target) {
					t.Fatalf("duplicate payment: got %s, want %s", amount, target)
				}
				if amount.Equal(target) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("funding did not confirm: got %s, want %s", amount, target)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if cancellations != 1 || len(payloads) != 40 {
		t.Fatalf("sends=%d distinct payloads=%d cancellations=%d", sends, len(payloads), cancellations)
	}
	if len(master.pendingSpentUtxos) != 0 || len(master.spentUtxos) == 0 {
		t.Fatalf("pending=%d spent=%d", len(master.pendingSpentUtxos), len(master.spentUtxos))
	}
	t.Logf("confirmed %d unique funding transfers with one lost acceptance response; both recipients received exactly 100 ZEC", len(hashes))
}

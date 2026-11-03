package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/wire"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zcash"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestZecBroadcastRecoversOriginalTransaction(t *testing.T) {
	tx := &zcash.Tx{MsgTx: wire.NewMsgTx(6), ConsensusBranchID: 0x37a5165b, ExpiryHeight: 120}
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{}, []byte{1}, nil))
	tx.AddTxOut(wire.NewTxOut(1, []byte{0x51}))
	var encoded bytes.Buffer
	if err := tx.Serialize(&encoded); err != nil {
		t.Fatal(err)
	}
	payload := encoded.Bytes()
	payloadHex := hex.EncodeToString(payload)
	id, err := tx.ID()
	if err != nil {
		t.Fatal(err)
	}
	txid := id.String()
	for _, tc := range []struct {
		name             string
		firstError       string
		acceptedOnLookup bool
		lookupMismatch   bool
		lookupFailure    bool
		retry            bool
	}{
		{name: "cancelled but accepted", firstError: "verification cancelled", acceptedOnLookup: true},
		{name: "already mined", firstError: "transaction already in block chain", acceptedOnLookup: true},
		{name: "cancelled then rebroadcast", firstError: "verification cancelled", retry: true},
		{name: "transport uncertainty", firstError: "channel closed", lookupFailure: true, retry: true},
		{name: "mismatched response", firstError: "verification cancelled", lookupMismatch: true},
		{name: "invalid transaction", firstError: "failed to validate tx: bad-txns-inputs-missingorspent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sends, lookups := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string            `json:"method"`
					ID     any               `json:"id"`
					Params []json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
				switch req.Method {
				case "sendrawtransaction":
					sends++
					var raw string
					_ = json.Unmarshal(req.Params[0], &raw)
					if raw != payloadHex {
						t.Errorf("broadcast changed payload: %s", raw)
					}
					if sends == 1 {
						response["error"] = map[string]any{"code": -1, "message": tc.firstError}
					} else {
						response["result"] = txid
					}
				case "getrawtransaction":
					lookups++
					var hash string
					_ = json.Unmarshal(req.Params[0], &hash)
					if hash != txid {
						t.Errorf("queried wrong transaction: %s", hash)
					}
					switch {
					case tc.acceptedOnLookup:
						response["result"] = strings.ToUpper(payloadHex)
					case tc.lookupMismatch:
						response["result"] = "00"
					case tc.lookupFailure:
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					default:
						response["error"] = map[string]any{"code": -5, "message": "No such mempool or main chain transaction"}
					}
				default:
					t.Errorf("unexpected RPC: %s", req.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			rpcClient, err := rpc.NewClient(server.URL, "", "", 0, time.Second, common.ZECChain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{chain: common.ZECChain, rpc: rpcClient}
			client.rememberPendingSpentUtxos(payload, map[string]int64{"input-0": 1})
			got, err := client.BroadcastTx(payload)
			if tc.acceptedOnLookup {
				if err != nil || got != txid {
					t.Fatalf("original acceptance lost: %s %v", got, err)
				}
			} else {
				if err == nil || got != "" {
					t.Fatalf("unexpected success: %s %v", got, err)
				}
				if client.spentUtxos["input-0"] || len(client.pendingSpentUtxos[payloadHex]) != 1 {
					t.Fatal("failed broadcast must retain reserved inputs without marking them spent")
				}
				if tc.retry {
					got, err = client.BroadcastTx(payload)
					if err != nil || got != txid {
						t.Fatalf("retry failed: %s %v", got, err)
					}
				}
			}
			if tc.retry || tc.acceptedOnLookup {
				if !client.spentUtxos["input-0"] || len(client.pendingSpentUtxos) != 0 {
					t.Fatal("accepted transaction did not finalize the original inputs")
				}
			}
			expectedSends := 1
			if tc.retry {
				expectedSends = 2
			}
			if sends != expectedSends || lookups != 1 {
				t.Fatalf("unexpected RPC counts: sends=%d lookups=%d", sends, lookups)
			}
		})
	}
}

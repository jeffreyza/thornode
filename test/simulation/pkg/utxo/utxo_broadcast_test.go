package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// TestBroadcastDoesNotDuplicateTimedOutTx reproduces the DOGE DualLP failure in
// job 16347608704: sendrawtransaction timed out on an overloaded daemon that
// still accepted the deposit, and the retried op signed a second deposit that
// left the LP position pending. The client must resolve a failed broadcast by
// txid and only ever resend the same payload.
func TestBroadcastDoesNotDuplicateTimedOutTx(t *testing.T) {
	origDelay := utxoBroadcastRetryDelay
	utxoBroadcastRetryDelay = 0
	t.Cleanup(func() { utxoBroadcastRetryDelay = origDelay })

	tx := wire.NewMsgTx(wire.TxVersion)
	prev, err := chainhash.NewHashFromStr(strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(prev, 0), []byte{0x51}, nil))
	tx.AddTxOut(wire.NewTxOut(1_000_000, []byte{0x51}))
	var buf bytes.Buffer
	if err = tx.Serialize(&buf); err != nil {
		t.Fatal(err)
	}
	payload := buf.Bytes()
	payloadHex := hex.EncodeToString(payload)
	txid := tx.TxHash().String()

	const (
		hang     = "hang"
		drop     = "drop"
		accept   = "accept"
		inChain  = "in-chain"
		rejected = "rejected"
		warmup   = "warmup"
	)
	for _, tc := range []struct {
		name string
		// sends lists the sendrawtransaction outcome for each attempt
		sends []string
		// lookupFound makes getrawtransaction return the payload
		lookupFound bool
		// lookupFails makes getrawtransaction fail without answering for the txid
		lookupFails bool
		// retries sets rpc.Call transport retries
		retries       int
		wantSends     int
		wantOK        bool
		wantUncertain bool
	}{
		{name: "timed out but accepted", sends: []string{hang}, lookupFound: true, wantSends: 1, wantOK: true},
		{name: "timed out then accepted on resend", sends: []string{hang, accept}, wantSends: 2, wantOK: true},
		{name: "already mined while lookup fails", sends: []string{hang, inChain}, wantSends: 2, wantOK: true},
		{name: "rejected by node", sends: []string{rejected}, wantSends: 1},
		{name: "daemon unresponsive", sends: []string{hang, hang, hang}, wantSends: 3, wantUncertain: true},
		{name: "rejected while lookup fails", sends: []string{rejected, rejected, rejected}, lookupFails: true, wantSends: 3, wantUncertain: true},
		{name: "warmup then accepted on resend", sends: []string{warmup, accept}, lookupFails: true, wantSends: 2, wantOK: true},
		// job 16347608704 follow-up: the first send was accepted without an
		// answer, then the restarting daemon failed lookups and sends
		{name: "timed out then warmup while lookup fails", sends: []string{hang, warmup, hang}, lookupFails: true, wantSends: 3, wantUncertain: true},
		{name: "dropped in transport retry then warmup", sends: []string{drop, warmup, hang, hang}, lookupFails: true, retries: 1, wantSends: 4, wantUncertain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sends := 0
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
					var raw string
					_ = json.Unmarshal(req.Params[0], &raw)
					if raw != payloadHex {
						t.Errorf("broadcast changed payload: %s", raw)
					}
					outcome := hang
					if sends < len(tc.sends) {
						outcome = tc.sends[sends]
					}
					sends++
					switch outcome {
					case hang:
						time.Sleep(200 * time.Millisecond)
						return
					case drop:
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					case accept:
						response["result"] = txid
					case inChain:
						response["error"] = map[string]any{"code": -27, "message": "transaction already in block chain"}
					case rejected:
						response["error"] = map[string]any{"code": -26, "message": "16: mandatory-script-verify-flag-failed"}
					case warmup:
						response["error"] = map[string]any{"code": -28, "message": "Verifying blocks..."}
					}
				case "getrawtransaction":
					var hash string
					_ = json.Unmarshal(req.Params[0], &hash)
					if hash != txid {
						t.Errorf("queried wrong transaction: %s", hash)
					}
					switch {
					case tc.lookupFound:
						response["result"] = payloadHex
					case tc.lookupFails:
						response["error"] = map[string]any{"code": -28, "message": "Verifying blocks..."}
					default:
						response["error"] = map[string]any{"code": -5, "message": "No such mempool or blockchain transaction"}
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

			rpcClient, err := rpc.NewClient(server.URL, "", "", tc.retries, 50*time.Millisecond, common.DOGEChain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{chain: common.DOGEChain, rpc: rpcClient}

			got, err := client.BroadcastTx(payload)
			if sends != tc.wantSends {
				t.Fatalf("sendrawtransaction calls: got %d, want %d", sends, tc.wantSends)
			}
			switch {
			case tc.wantOK:
				if err != nil || got != txid {
					t.Fatalf("accepted transaction lost: %q %v", got, err)
				}
			case tc.wantUncertain:
				if !errors.Is(err, ErrSimTxUncertain) || got != txid {
					t.Fatalf("unresolved broadcast must be uncertain with its txid: %q %v", got, err)
				}
			default:
				if err == nil || errors.Is(err, ErrSimTxUncertain) || got != "" {
					t.Fatalf("rejected broadcast must fail without a txid: %q %v", got, err)
				}
			}
			if err != nil && !strings.Contains(err.Error(), txid) {
				t.Fatalf("error should name the transaction: %v", err)
			}
		})
	}
}

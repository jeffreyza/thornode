package rpc

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestSharedBlockAndTransactionRPCs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chain common.Chain
		locks bool
	}{
		{"bitcoin", common.BTCChain, false},
		{"litecoin", common.LTCChain, false},
		{"bitcoin cash", common.BCHChain, false},
		{"dogecoin", common.DOGEChain, false},
		{"zcash", common.ZECChain, false},
		{"dash locked", common.DASHChain, true},
		{"dash unlocked", common.DASHChain, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
				if len(params) != 2 {
					t.Errorf("unexpected %s params: %v", method, params)
					return nil, btcjson.ErrRPCInvalidParams
				}
				switch method {
				case "getblock":
					if params[0] != "block" || params[1] != float64(2) {
						t.Errorf("unexpected block params: %v", params)
					}
					block := map[string]interface{}{
						"hash": "block", "height": 123, "nonce": 42,
						"tx": []interface{}{map[string]interface{}{"txid": "tx1", "version": 2}},
					}
					if tc.chain == common.ZECChain {
						// Exercise the existing Zcash transport's nonce normalization.
						block["nonce"] = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
					}
					if tc.chain == common.DASHChain {
						block["chainlock"] = tc.locks
					}
					return block, nil
				case "getrawtransaction":
					var verbosity interface{} = true
					if tc.chain == common.ZECChain {
						verbosity = float64(1)
					}
					if params[1] != verbosity {
						t.Errorf("unexpected transaction params: %v", params)
					}
					tx := map[string]interface{}{
						"txid": params[0], "version": 2,
						"vin":  []interface{}{map[string]interface{}{"txid": "parent", "vout": 1}},
						"vout": []interface{}{map[string]interface{}{"value": 1.25, "n": 0}},
					}
					if tc.chain == common.DASHChain {
						tx["instantlock"] = tc.locks
						tx["instantlock_internal"] = tc.locks
						tx["chainlock"] = tc.locks
					}
					return tx, nil
				default:
					t.Errorf("unexpected method %s", method)
					return nil, btcjson.ErrRPCMethodNotFound
				}
			})
			defer server.Close()
			client, err := NewClient(server.URL, "", "", 0, time.Second, tc.chain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			block, err := client.GetBlockVerboseTxs("block")
			if err != nil {
				t.Fatal(err)
			}
			if block.Hash != "block" || block.Height != 123 {
				t.Fatalf("unexpected block identity: %+v", block)
			}
			if block.ChainLock != tc.locks {
				t.Fatalf("block ChainLock: got %v, want %v", block.ChainLock, tc.locks)
			}
			if len(block.Tx) != 1 {
				t.Fatalf("block transaction count: got %d, want 1", len(block.Tx))
			}
			if block.Tx[0].Txid != "tx1" {
				t.Fatalf("unexpected block transaction: %+v", block.Tx[0])
			}
			if tc.chain == common.ZECChain && block.Nonce != 0 {
				t.Fatalf("Zcash nonce was not normalized: %d", block.Nonce)
			}
			tx, err := client.GetRawTransactionVerbose("tx1")
			if err != nil {
				t.Fatal(err)
			}
			if tx.Txid != "tx1" {
				t.Fatalf("transaction ID: got %q, want tx1", tx.Txid)
			}
			if len(tx.Vin) != 1 || len(tx.Vout) != 1 {
				t.Fatalf("expected one input and one output: %+v", tx)
			}
			if tx.Vin[0].Txid != "parent" || tx.Vout[0].Value != 1.25 {
				t.Fatalf("unexpected input or output: %+v", tx)
			}
			if tx.InstantLock != tc.locks || tx.InstantLockInternal != tc.locks || tx.ChainLock != tc.locks {
				t.Fatalf("unexpected transaction lock metadata: %+v", tx)
			}
			batch, errs, err := client.BatchGetRawTransactionVerbose([]string{"tx1", "tx2"})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch) != 2 || len(errs) != 2 {
				t.Fatalf("batch lengths: got %d results and %d errors, want 2 each", len(batch), len(errs))
			}
			if errs[0] != nil || errs[1] != nil {
				t.Fatalf("unexpected transaction errors: %v", errs)
			}
			if !reflect.DeepEqual(batch[0], tx) || batch[1].Txid != "tx2" {
				t.Fatalf("batch lost transaction fields or order: %+v", batch)
			}
			second := batch[1]
			if second.InstantLock != tc.locks || second.InstantLockInternal != tc.locks || second.ChainLock != tc.locks {
				t.Fatalf("unexpected batch lock metadata: %+v", second)
			}
		})
	}
}

func TestSharedRPCResultErrors(t *testing.T) {
	for _, chain := range []common.Chain{common.BTCChain, common.DASHChain} {
		t.Run(chain.String(), func(t *testing.T) {
			server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
				if method == "getblock" || params[0] == "missing" {
					return nil, &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "not found"}
				}
				return map[string]interface{}{"txid": params[0]}, nil
			})
			defer server.Close()
			client, err := NewClient(server.URL, "", "", 0, time.Second, chain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			_, blockErr := client.GetBlockVerboseTxs("missing")
			_, txErr := client.GetRawTransactionVerbose("missing")
			batch, errs, err := client.BatchGetRawTransactionVerbose([]string{"first", "missing", "last"})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch) != 3 || len(errs) != 3 {
				t.Fatalf("batch lengths: got %d results and %d errors, want 3 each", len(batch), len(errs))
			}
			for _, err := range []error{blockErr, txErr, errs[1]} {
				var rpcErr *btcjson.RPCError
				if !errors.As(err, &rpcErr) || rpcErr.Code != btcjson.ErrRPCInvalidAddressOrKey {
					t.Fatalf("lost RPC error: %v", err)
				}
			}
			if errs[0] != nil || errs[2] != nil {
				t.Fatalf("unexpected errors for successful transactions: %v", errs)
			}
			if batch[0].Txid != "first" || batch[2].Txid != "last" {
				t.Fatalf("batch error shifted successful results: %+v", batch)
			}
		})
	}
}

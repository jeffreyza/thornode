package rpc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestSharedRPCResultDecoding(t *testing.T) {
	// The extended response must preserve every shared field while also
	// decoding Dash finality from the same JSON object.
	const txJSON = `{
		"hex":"0102", "txid":"tx", "hash":"hash", "size":225, "vsize":225,
		"weight":900, "version":2, "locktime":123,
		"vin":[
			{"txid":"parent", "vout":2, "scriptSig":{"asm":"sig", "hex":"0304"}, "sequence":1234},
			{"coinbase":"0506", "sequence":4294967295}
		],
		"vout":[{"value":1.23456789, "n":0, "scriptPubKey":{
			"asm":"OP_DUP", "hex":"76", "reqSigs":1, "type":"pubkeyhash", "addresses":["dash-address"]
		}}],
		"blockhash":"block", "confirmations":3, "time":100, "blocktime":99,
		"instantlock":true, "instantlock_internal":true, "chainlock":true
	}`
	var tx TxResult
	var wantTx btcjson.TxRawResult
	err := json.Unmarshal([]byte(txJSON), &tx)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal([]byte(txJSON), &wantTx)
	if err != nil {
		t.Fatal(err)
	}
	if !tx.QuorumLocked() || !tx.InstantSendLocked() || !tx.ChainLock {
		t.Fatal("shared response lost Dash finality")
	}
	if got := tx.TxRawResult; !reflect.DeepEqual(got, wantTx) {
		t.Fatalf("transaction fields: got %+v, want %+v", got, wantTx)
	}
	blockJSON := fmt.Sprintf(`{
		"hash":"block", "confirmations":3, "strippedsize":500, "size":501, "weight":2001,
		"height":123, "version":4, "versionHex":"00000004", "merkleroot":"root",
		"tx":[%s], "rawtx":[%s], "time":100, "nonce":42, "bits":"bits",
		"difficulty":1.5, "previousblockhash":"previous", "nextblockhash":"next", "chainlock":true
	}`, txJSON, txJSON)
	var block BlockResult
	var wantBlock btcjson.GetBlockVerboseTxResult
	err = json.Unmarshal([]byte(blockJSON), &block)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal([]byte(blockJSON), &wantBlock)
	if err != nil {
		t.Fatal(err)
	}
	if !block.ChainLock {
		t.Fatal("shared response lost block finality")
	}
	if got := block.GetBlockVerboseTxResult; !reflect.DeepEqual(got, wantBlock) {
		t.Fatalf("block fields: got %+v, want %+v", got, wantBlock)
	}
}

func TestISLocksBatchesAndPreservesMissingResults(t *testing.T) {
	for _, count := range []int{0, 1, 100, 101, 201} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			calls := 0
			server := newMockRPCServer(func(method string, params []interface{}) (interface{}, *btcjson.RPCError) {
				calls++
				if len(params) != 1 {
					t.Errorf("unexpected getislocks params: %v", params)
					return nil, btcjson.ErrRPCInvalidParams
				}
				ids, ok := params[0].([]interface{})
				if !ok {
					t.Errorf("getislocks txids: got %T, want array", params[0])
					return nil, btcjson.ErrRPCInvalidParams
				}
				if method != "getislocks" || len(ids) > 100 {
					t.Errorf("invalid getislocks request: %s, %d ids", method, len(ids))
				}
				result := make([]interface{}, len(ids))
				for i, id := range ids {
					if i == 0 {
						result[i] = "None"
					} else {
						result[i] = map[string]interface{}{"txid": id}
					}
				}
				return result, nil
			})
			defer server.Close()
			client, err := NewClient(server.URL, "", "", 0, time.Second, common.DASHChain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, count)
			for i := range ids {
				ids[i] = fmt.Sprintf("%064x", i)
			}
			locks, err := client.GetISLocks(ids)
			if err != nil {
				t.Fatal(err)
			}
			if len(locks) != count {
				t.Fatalf("lock count: got %d, want %d", len(locks), count)
			}
			if calls != (count+99)/100 {
				t.Fatalf("batch calls: got %d, want %d", calls, (count+99)/100)
			}
			for i, lock := range locks {
				if i%100 == 0 {
					if lock != nil {
						t.Fatalf("missing lock %d shifted", i)
					}
				} else if lock == nil || lock.TxID != ids[i] {
					t.Fatalf("wrong lock at %d: %+v", i, lock)
				}
			}
		})
	}
}

func TestISLocksFailedChunksDoNotDiscardSuccessfulChunks(t *testing.T) {
	for _, failure := range []string{"rpc", "count", "txid", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			ids := make([]string, 101)
			for i := range ids {
				ids[i] = fmt.Sprintf("%064x", i)
			}
			server := newMockRPCServer(func(_ string, params []interface{}) (interface{}, *btcjson.RPCError) {
				if len(params) != 1 {
					t.Errorf("unexpected getislocks params: %v", params)
					return nil, btcjson.ErrRPCInvalidParams
				}
				batch, ok := params[0].([]interface{})
				if !ok {
					t.Errorf("getislocks txids: got %T, want array", params[0])
					return nil, btcjson.ErrRPCInvalidParams
				}
				if len(batch) == 1 {
					return []interface{}{map[string]interface{}{"txid": batch[0]}}, nil
				}
				if failure == "rpc" {
					return nil, &btcjson.RPCError{Code: -1, Message: "temporarily unavailable"}
				}
				if failure == "count" {
					return []interface{}{}, nil
				}
				result := make([]interface{}, len(batch))
				for i, id := range batch {
					result[i] = map[string]interface{}{"txid": id}
				}
				if failure == "txid" {
					result[99] = map[string]interface{}{"txid": "wrong"}
				} else {
					result[99] = "unexpected string"
				}
				return result, nil
			})
			defer server.Close()
			client, err := NewClient(server.URL, "", "", 0, time.Second, common.DASHChain, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			locks, err := client.GetISLocks(ids)
			if err == nil {
				t.Fatal("failed lock batch returned no error")
			}
			if len(locks) != 101 {
				t.Fatalf("partial result count: got %d, want 101", len(locks))
			}
			if locks[100] == nil || locks[100].TxID != ids[100] {
				t.Fatalf("successful chunk was not preserved: %+v", locks[100])
			}
			for _, lock := range locks[:100] {
				if lock != nil {
					t.Fatal("failed chunk was partially accepted")
				}
			}
		})
	}
}

func TestQuorumLockFields(t *testing.T) {
	for _, value := range []string{"true", "false", "unknown"} {
		var entry GetRawMempoolVerboseResult
		err := json.Unmarshal([]byte(`{"instantlock":"`+value+`"}`), &entry)
		if err != nil {
			t.Fatal(err)
		}
		if entry.InstantLock != value {
			t.Fatalf("mempool lock: got %q, want %q", entry.InstantLock, value)
		}
	}
	for _, tc := range []struct {
		json           string
		final, instant bool
	}{
		{`{}`, false, false},
		{`{"instantlock":true,"instantlock_internal":true}`, true, true},
		{`{"instantlock":true,"chainlock":true}`, true, false},
		{`{"chainlock":true}`, true, false},
	} {
		var tx TxResult
		err := json.Unmarshal([]byte(tc.json), &tx)
		if err != nil {
			t.Fatal(err)
		}
		if tx.QuorumLocked() != tc.final || tx.InstantSendLocked() != tc.instant {
			t.Fatalf("wrong finality for %s", tc.json)
		}
	}
}

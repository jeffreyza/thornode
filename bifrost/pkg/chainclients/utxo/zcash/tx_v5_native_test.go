package zcash

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
)

// TestV5Zebra signs transparent v5 (ZIP-225 / ZIP-244) transactions and has an official zebrad
// validate the sighash by accepting them into its mempool, then confirms the mined version and
// txid. v5 is valid both before and after NU7, so THORChain signs v5 for transparent vault
// payments. Set ZEC_DAEMON to an official zebrad binary to run.
func TestV5Zebra(t *testing.T) {
	daemon := os.Getenv("ZEC_DAEMON")
	if daemon == "" {
		t.Skip("set ZEC_DAEMON to an official zebrad binary")
	}
	// Exercise v5 at a live NU6.x branch (the current mainnet situation). v5 is valid after NU7
	// as well, which is the reason the vault signs v5 unconditionally; the post-NU7 branch is
	// covered deterministically by TestV5BranchFlipDeterminism / TestV5ResignAcrossBoundary.
	// (zebrad 2.5.0's Regtest cannot activate NU7 without a full subsidy config, unrelated to v5.)
	runZebraV5(t, daemon, "NU5 = 1\nNU6 = 1\n\"NU6.1\" = 1")
}

func runZebraV5(t *testing.T, daemon, activations string) {
	key, public := btcec.PrivKeyFromBytes(btcec.S256(), bytes.Repeat([]byte{1}, 32))
	publicKey := public.SerializeCompressed()
	var keyHash [20]byte
	copy(keyHash[:], btcutil.Hash160(publicKey))
	address, err := NewAddressPubKeyHash(keyHash, "regtest")
	if err != nil {
		t.Fatal(err)
	}
	script, err := address.Script()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := fmt.Sprintf(`[network]
network = "Regtest"
listen_addr = "127.0.0.1:0"
initial_testnet_peers = []
initial_mainnet_peers = []
[network.testnet_parameters]
disable_pow = true
[network.testnet_parameters.activation_heights]
%s
[state]
cache_dir = %q
[rpc]
listen_addr = %q
enable_cookie_auth = false
[mining]
miner_address = %q
`, activations, filepath.Join(dir, "data"), endpoint, address.String())
	path := filepath.Join(dir, "zebrad.toml")
	if err = os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "zebrad.log"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, daemon, "--config", path, "start")
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		_ = log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Log(string(data))
		}
	})
	client := &http.Client{Timeout: 30 * time.Second}
	rpcURL := "http://" + endpoint
	call := func(method string, args any, result any) error {
		payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": args}
		body, rpcErr := json.Marshal(payload)
		if rpcErr != nil {
			return rpcErr
		}
		request, rpcErr := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(body))
		if rpcErr != nil {
			return rpcErr
		}
		request.Header.Set("Content-Type", "application/json")
		response, rpcErr := client.Do(request)
		if rpcErr != nil {
			return rpcErr
		}
		defer response.Body.Close()
		var reply struct {
			Result json.RawMessage
			Error  json.RawMessage
		}
		if rpcErr = json.NewDecoder(response.Body).Decode(&reply); rpcErr != nil {
			return rpcErr
		}
		if len(reply.Error) > 0 && string(reply.Error) != "null" {
			return fmt.Errorf("%s: %s", method, reply.Error)
		}
		if result != nil {
			return json.Unmarshal(reply.Result, result)
		}
		return nil
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = call("getblockcount", []any{}, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Zebra startup timeout")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err = call("generate", []any{110}, nil); err != nil {
		t.Fatal(err)
	}
	var state ConsensusState
	if err = call("getblockchaininfo", []any{}, &state); err != nil {
		t.Fatal(err)
	}
	branch, expiry, err := state.PaymentState(uint32(state.Blocks), 40)
	if err != nil {
		t.Fatal(err)
	}
	var outputs []struct {
		TxID   string `json:"txid"`
		Index  uint32 `json:"outputIndex"`
		Value  int64  `json:"satoshis"`
		Height int64  `json:"height"`
	}
	params := []any{map[string]any{"addresses": []string{address.String()}}}
	if err = call("getaddressutxos", params, &outputs); err != nil {
		t.Fatal(err)
	}
	tx := &Tx{MsgTx: wire.NewMsgTx(5), ConsensusBranchID: branch, ExpiryHeight: expiry}
	var previous []*wire.TxOut
	var total int64
	for _, output := range outputs {
		if output.Height > state.Blocks-100 {
			continue
		}
		hash, hashErr := chainhash.NewHashFromStr(output.TxID)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		outpoint := wire.NewOutPoint(hash, output.Index)
		tx.AddTxIn(wire.NewTxIn(outpoint, nil, nil))
		previous = append(previous, wire.NewTxOut(output.Value, script))
		total += output.Value
		if len(previous) == 2 {
			break
		}
	}
	if len(previous) != 2 {
		t.Fatal("missing mature regtest faucet outputs")
	}
	tx.AddTxOut(wire.NewTxOut(total-100000, script))
	memo, err := txscript.NewScriptBuilder().AddOp(txscript.OP_RETURN).AddData([]byte("OUT:v5-native-test")).Script()
	if err != nil {
		t.Fatal(err)
	}
	tx.AddTxOut(wire.NewTxOut(0, memo))
	hashes, err := NewSigHashes(tx, previous)
	if err != nil {
		t.Fatal(err)
	}
	for i := range previous {
		digest, signErr := hashes.SignatureHash(i)
		if signErr != nil {
			t.Fatal(signErr)
		}
		signature, signErr := key.Sign(digest)
		if signErr != nil {
			t.Fatal(signErr)
		}
		signatureBytes := append(signature.Serialize(), byte(txscript.SigHashAll))
		tx.TxIn[i].SignatureScript, signErr = txscript.NewScriptBuilder().
			AddData(signatureBytes).
			AddData(publicKey).
			Script()
		if signErr != nil {
			t.Fatal(signErr)
		}
	}
	var buffer bytes.Buffer
	err = tx.Serialize(&buffer)
	raw := hex.EncodeToString(buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	// The ZIP-225 empty Sapling/Orchard tail (3 zero bytes) and v5 header, locked against the
	// exact bytes the node accepts rather than against our own Serialize.
	encoded := buffer.Bytes()
	if n := len(encoded); n < 11 || !bytes.Equal(encoded[n-3:], []byte{0, 0, 0}) {
		t.Fatalf("v5 tail is not three empty-bundle bytes: %x", encoded)
	}
	if binary.LittleEndian.Uint32(encoded[:4]) != 0x80000005 || binary.LittleEndian.Uint32(encoded[4:8]) != v5GroupID {
		t.Fatalf("v5 header word/group id wrong: %x", encoded[:8])
	}
	id, err := tx.ID()
	if err != nil {
		t.Fatal(err)
	}
	var accepted string
	if err = call("sendrawtransaction", []any{raw}, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted != id.String() {
		t.Fatal("broadcast txid mismatch", accepted, id)
	}
	if err = call("generate", []any{1}, nil); err != nil {
		t.Fatal(err)
	}
	var mined struct {
		TxID          string `json:"txid"`
		Version       int    `json:"version"`
		Confirmations int64  `json:"confirmations"`
	}
	if err = call("getrawtransaction", []any{accepted, 1}, &mined); err != nil {
		t.Fatal(err)
	}
	if mined.TxID != accepted || mined.Version != 5 || mined.Confirmations < 1 {
		t.Fatal("transaction not mined", mined)
	}
	t.Logf("zebra accepted v5 tx %s (branch %08x, raw %s)", accepted, branch, raw)
}

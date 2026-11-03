package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	dashtxscript "github.com/dashpay/dashd-go/txscript"
	dashwire "github.com/dashpay/dashd-go/wire"
	"github.com/prometheus/client_golang/prometheus/testutil"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *DashSuite) TestDashSignTxCheckpointAndBroadcast(c *C) {
	ttypes.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(bob, keyring.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	keys := thorclient.NewKeysWithKeybase(kb, bob, password)
	m := GetMetricForTest(c, common.DASHChain)

	var utxos []btcjson.ListUnspentResult
	var broadcastBytes []byte
	var broadcastMaxFee float64
	var customerScript string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, thorclient.MimirEndpoint) {
			c.Assert(json.NewEncoder(w).Encode(-1), IsNil)
			return
		}
		if r.URL.Path == thorclient.ChainVersionEndpoint {
			c.Assert(json.NewEncoder(w).Encode(map[string]string{"current": ttypes.GetCurrentVersion().String()}), IsNil)
			return
		}
		if r.URL.Path == thorclient.PubKeysEndpoint {
			c.Assert(json.NewEncoder(w).Encode(map[string]any{"asgard": []any{}}), IsNil)
			return
		}
		var req rpcRequest
		c.Assert(json.NewDecoder(r.Body).Decode(&req), IsNil)
		var result any
		switch req.Method {
		case "createwallet":
			result = map[string]string{"name": ""}
		case "importaddress":
		case "getnetworkinfo":
			result = map[string]any{"relayfee": 0.00001}
		case "getblockcount":
			result = 123
		case "listunspent":
			result = utxos
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(req.Params[0], &id), IsNil)
			if id == strings.Repeat("2", 64) {
				result = map[string]any{
					"txid": id, "instantlock": true,
					"vin": []any{map[string]any{"txid": strings.Repeat("3", 64), "vout": 0}},
				}
			} else {
				c.Assert(id, Equals, strings.Repeat("3", 64))
				result = map[string]any{"txid": id, "vout": []any{map[string]any{
					"n": 0, "value": 0.6, "scriptPubKey": map[string]any{"hex": customerScript, "type": "pubkeyhash"},
				}}}
			}
		case "sendrawtransaction":
			c.Assert(req.Params, HasLen, 2)
			var payload string
			c.Assert(json.Unmarshal(req.Params[0], &payload), IsNil)
			c.Assert(json.Unmarshal(req.Params[1], &broadcastMaxFee), IsNil)
			var decodeErr error
			broadcastBytes, decodeErr = hex.DecodeString(payload)
			c.Assert(decodeErr, IsNil)
			var tx dashwire.MsgTx
			c.Assert(tx.Deserialize(bytes.NewReader(broadcastBytes)), IsNil)
			result = tx.TxHash().String()
		default:
			c.Fatalf("unexpected RPC %s", req.Method)
		}
		c.Assert(json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}), IsNil)
	}))
	defer server.Close()
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID: "thorchain", ChainHost: server.Listener.Addr().String(),
		SignerName: bob, SignerPasswd: password, ChainHomeFolder: c.MkDir(),
	}, m, keys)
	c.Assert(err, IsNil)
	cfg := config.BifrostChainConfiguration{
		ChainID: common.DASHChain, RPCHost: server.URL, UserName: bob, Password: password,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID: common.DASHChain, StartBlockHeight: 1, HTTPRequestTimeout: time.Second,
		},
	}
	cfg.UTXO.MaxSatsPerVByte = 9765
	cfg.UTXO.MaxUTXOsToSpend = 2
	cfg.UTXO.MinUTXOConfirmations = 1
	client, err := NewClient(keys, cfg, nil, bridge, m)
	c.Assert(err, IsNil)
	defer client.Stop()
	to, err := ttypes.GetRandomPubKey().GetAddress(common.DASHChain)
	c.Assert(err, IsNil)
	memo := "OUT:" + strings.Repeat("a", 64) + "|" + strings.Repeat("b", 40)
	txOut := stypes.TxOutItem{
		Chain: common.DASHChain, VaultPubKey: client.nodePubKey, ToAddress: to,
		Coins:   common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))),
		MaxGas:  common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(10000))},
		GasRate: 10, Memo: memo,
	}
	sourceScript, err := client.getSourceScript(txOut)
	c.Assert(err, IsNil)
	utxos = []btcjson.ListUnspentResult{
		{TxID: strings.Repeat("1", 64), Amount: 0.7, ScriptPubKey: hex.EncodeToString(sourceScript), Confirmations: 10},
		{TxID: strings.Repeat("2", 64), Amount: 0.6, ScriptPubKey: hex.EncodeToString(sourceScript), Confirmations: 9},
	}
	previous := []*dashwire.TxOut{dashwire.NewTxOut(70_000_000, sourceScript), dashwire.NewTxOut(60_000_000, sourceScript)}
	customerAddress, err := dashutil.DecodeAddress(to.String(), client.getChainCfgDASH())
	c.Assert(err, IsNil)
	pkScript, err := dashtxscript.PayToAddrScript(customerAddress)
	c.Assert(err, IsNil)
	customerScript = hex.EncodeToString(pkScript)
	// An InstantSend customer input cannot fund a signature until it meets the
	// confirmation requirement. Failure must happen before creating a checkpoint.
	utxos[1].Confirmations = 0
	signed, checkpoint, observation, err := client.SignTx(txOut, 1)
	c.Assert(err, ErrorMatches, ".*insufficient available UTXOs:.*")
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, IsNil)
	c.Assert(broadcastBytes, IsNil)
	utxos[1].Confirmations = 9

	// Exercise construction, per-input signing, verification and serialization.
	signed, checkpoint, observation, err = client.SignTx(txOut, 1)
	c.Assert(err, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, NotNil)
	var decoded dashwire.MsgTx
	c.Assert(decoded.Deserialize(bytes.NewReader(signed)), IsNil)
	c.Assert(decoded.TxIn, HasLen, 2)
	c.Assert(dashutil.VerifyTransaction(&decoded, previous), IsNil)
	c.Assert(observation.Tx, Equals, decoded.TxHash().String())
	c.Assert(observation.To, Equals, to.String())
	c.Assert(observation.Memo, Equals, memo)
	c.Assert(observation.Coins[0].Amount.Uint64(), Equals, uint64(common.One))
	// Destination, change, OP_RETURN, and two P2PKH continuation outputs.
	c.Assert(decoded.TxOut, HasLen, 5)
	c.Assert(dashtxscript.GetScriptClass(decoded.TxOut[2].PkScript), Equals, dashtxscript.NullDataTy)
	firstChunk, err := dashtxscript.PushedData(decoded.TxOut[2].PkScript)
	c.Assert(err, IsNil)
	c.Assert(firstChunk, HasLen, 1)
	c.Assert(string(firstChunk[0]), Equals, memo[:79]+"^")
	recoveredMemo := append([]byte(nil), firstChunk[0][:79]...)
	for _, output := range decoded.TxOut[3:] {
		c.Assert(dashtxscript.GetScriptClass(output.PkScript), Equals, dashtxscript.PubKeyHashTy)
		c.Assert(output.Value, Equals, common.DASHChain.P2WPKHOutputValue())
		var chunks [][]byte
		chunks, err = dashtxscript.PushedData(output.PkScript)
		c.Assert(err, IsNil)
		c.Assert(chunks, HasLen, 1)
		recoveredMemo = append(recoveredMemo, chunks[0]...)
	}
	c.Assert(string(bytes.TrimRight(recoveredMemo, "\x00")), Equals, memo)

	// A wrong local key must fail verification, retain the unsigned checkpoint,
	// and never classify this as a failure to form the TSS party.
	partyFailures := m.GetCounterVec(metrics.UTXOKeysignPartyNotFormed).WithLabelValues(common.DASHChain.String())
	before := testutil.ToFloat64(partyFailures)
	correctKey := client.nodePrivKey
	client.nodePrivKey, err = btcec.NewPrivateKey(btcec.S256())
	c.Assert(err, IsNil)
	failed, checkpoint, observation, err := client.SignTx(txOut, 1)
	c.Assert(err, ErrorMatches, "fail to verify Dash input .*")
	c.Assert(failed, IsNil)
	c.Assert(checkpoint, NotNil)
	c.Assert(observation, IsNil)
	c.Assert(testutil.ToFloat64(partyFailures), Equals, before)
	c.Assert(broadcastBytes, IsNil)

	// Resume the committed transaction even if the newly estimated fee changes.
	client.nodePrivKey = correctKey
	txOut.Checkpoint = checkpoint
	txOut.GasRate = 20
	resumed, checkpoint, observation, err := client.SignTx(txOut, 2)
	c.Assert(err, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, NotNil)
	c.Assert(resumed, DeepEquals, signed)
	c.Assert(decoded.Deserialize(bytes.NewReader(resumed)), IsNil)
	c.Assert(dashutil.VerifyTransaction(&decoded, previous), IsNil)
	hash, err := client.BroadcastTx(txOut, resumed)
	c.Assert(err, IsNil)
	c.Assert(hash, Equals, decoded.TxHash().String())
	c.Assert(broadcastBytes, DeepEquals, resumed)
	c.Assert(broadcastMaxFee, Equals, float64(0.1))
}

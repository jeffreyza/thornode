package utxo

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/gascontract"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// GasUnitContractSuite runs market fee rates through the whole THORNode <->
// bifrost gas-unit contract with the real code on both sides: the network fee
// the scanner posts, THORNode's GasMgr, and buildTx. Each side's unit tests
// use hand-set values in its own units; !3925 shipped THORNode reading
// TransactionFeeRate in GetGasUnits units while bifrost still posted 1e8 units.
type GasUnitContractSuite struct {
	keys *thorclient.Keys
}

var _ = Suite(&GasUnitContractSuite{})

func (s *GasUnitContractSuite) SetUpSuite(c *C) {
	types2.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	kb := cKeys.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(bob, cKeys.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.keys = thorclient.NewKeysWithKeybase(kb, bob, password)
}

// TestGasUnitContract checks the scanner's sats/vbyte comes back as the fee
// rate buildTx pays: at least the observed rate and at most THORNode's 1.5x
// buffer on it, within the MaxGas THORNode reserved.
func (s *GasUnitContractSuite) TestGasUnitContract(c *C) {
	config.Init()
	chains := config.GetBifrost().GetChains()

	// Market fee rates in sats/vbyte, high enough that 1.5x of them clears the
	// 1,000 sat minimum relay fee buildTx floors at. DOGE posts from the block
	// and always rounds up to its next 500,000 sats/vbyte resolution step, so
	// its rate sits one below a step and may be posted one sat high; the next
	// step up is already clamped by max_sats_per_vbyte.
	for _, tc := range []struct {
		chain   common.Chain
		satsPer uint64
		roundUp uint64
	}{
		{common.BTCChain, 10, 0},
		{common.BTCChain, 120, 0},
		{common.LTCChain, 35, 0},
		{common.LTCChain, 400, 0},
		{common.BCHChain, 5, 0},
		{common.BCHChain, 40, 0},
		{common.DOGEChain, 499_999, 1},
	} {
		comment := Commentf("%s at %d sats/vbyte", tc.chain, tc.satsPer)
		client, server, utxo, script := s.newClient(c, chains[tc.chain], tc.satsPer)

		// 1. bifrost scanner: post the network fee for a block at this rate
		feeQueue := make(chan common.NetworkFee, 1)
		client.globalNetworkFeeQueue = feeQueue
		if tc.chain.Equals(common.DOGEChain) {
			c.Assert(client.sendNetworkFeeFromBlock(blockAtFeeRate(tc.chain, tc.satsPer)), IsNil, comment)
		} else {
			c.Assert(client.sendNetworkFee(100), IsNil, comment)
		}
		c.Assert(feeQueue, HasLen, 1, comment)
		networkFee := <-feeQueue

		// 2. THORNode: GasMgr turns the stored network fee into MaxGas and GasRate
		maxGas, gasRate, err := gascontract.GasDetails(networkFee)
		c.Assert(err, IsNil, comment)

		// 3. bifrost client: build the outbound THORNode scheduled with them
		to, err := types2.GetRandomPubKey().GetAddress(tc.chain)
		c.Assert(err, IsNil)
		inHash := types2.GetRandomTxHash()
		redeemTx, _, err := client.buildTx(stypes.TxOutItem{
			Chain:       tc.chain,
			ToAddress:   to,
			VaultPubKey: client.nodePubKey,
			Coins:       common.NewCoins(common.NewCoin(tc.chain.GetGasAsset(), cosmos.NewUint(common.One))),
			MaxGas:      common.Gas{maxGas},
			GasRate:     gasRate,
			InHash:      inHash,
			Memo:        mem.NewOutboundMemo(inHash).String(),
		}, script, 0)
		server.Close()
		c.Assert(err, IsNil, comment)

		// 4. the fee pays about the observed rate, within what THORNode reserved
		var out int64
		for _, o := range redeemTx.TxOut {
			out += o.Value
		}
		inbound, err := btcutil.NewAmount(utxo.Amount)
		c.Assert(err, IsNil)
		fee := int64(inbound) - out
		// customer, change back to the vault, memo
		c.Assert(redeemTx.TxOut, HasLen, 3, comment)
		size := client.estimateTxSize([]btcjson.ListUnspentResult{utxo}, [][]byte{redeemTx.TxOut[2].PkScript}, redeemTx.TxOut[0].PkScript, script)
		rate := uint64(fee / size)
		detail := fmt.Sprintf("%s at %d sats/vbyte: posted rate %d (size %d), THORNode GasRate %d MaxGas %s",
			tc.chain, tc.satsPer, networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas)
		maxRate := (tc.satsPer + tc.roundUp) * 3 / 2
		c.Check(rate >= tc.satsPer && rate <= maxRate, Equals, true,
			Commentf("fee %d sats for %d vbytes is %d sats/vbyte, want [%d, %d]: %s", fee, size, rate, tc.satsPer, maxRate, detail))
		c.Check(uint64(fee) <= maxGas.Amount.Uint64(), Equals, true,
			Commentf("fee %d sats exceeds MaxGas: %s", fee, detail))
	}
}

// TestZECGasUnitContract checks ZEC, whose signer ignores GasRate and pays the
// ZIP-317 conventional fee: the MaxGas THORNode derives from the static fee
// the scanner posts must cover the fee of the largest outbound buildTx makes.
func (s *GasUnitContractSuite) TestZECGasUnitContract(c *C) {
	config.Init()
	client, server, _, _ := s.newClient(c, config.GetBifrost().GetChains()[common.ZECChain], 0)
	defer server.Close()

	// 1. bifrost scanner: ZEC posts its static fee from any block
	feeQueue := make(chan common.NetworkFee, 1)
	client.globalNetworkFeeQueue = feeQueue
	c.Assert(client.sendNetworkFeeFromBlock(&btcjson.GetBlockVerboseTxResult{Height: 1201}), IsNil)
	c.Assert(feeQueue, HasLen, 1)
	networkFee := <-feeQueue

	// 2. THORNode: GasMgr turns the stored network fee into MaxGas
	maxGas, gasRate, err := gascontract.GasDetails(networkFee)
	c.Assert(err, IsNil)

	// 3. bifrost client: the ZIP-317 fee of an outbound spending the most inputs
	inputs := int(client.getMaximumUtxosToSpend())
	fee := uint64(zecFeeForInputCount(inputs, mem.NewOutboundMemo(types2.GetRandomTxHash()).String()))
	detail := fmt.Sprintf("ZEC: posted rate %d (size %d), THORNode GasRate %d MaxGas %s, ZIP-317 fee for %d inputs %d",
		networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas, inputs, fee)
	c.Check(fee <= maxGas.Amount.Uint64() && maxGas.Amount.Uint64() <= 2*fee, Equals, true,
		Commentf("MaxGas should cover the fee without overpaying it 2x: %s", detail))
}

// newClient starts a mock daemon and THORNode serving a block at satsPer
// sats/vbyte and one 10-coin vault UTXO, and returns a client for cfg's chain
// with that UTXO and the vault script.
func (s *GasUnitContractSuite) newClient(c *C, cfg config.BifrostChainConfiguration, satsPer uint64) (*Client, *httptest.Server, btcjson.ListUnspentResult, []byte) {
	chain := cfg.ChainID
	m := GetMetricForTest(c, chain)
	var utxo btcjson.ListUnspentResult
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodGet { // THORNode: no mimir overrides
			_, err := rw.Write([]byte("-1"))
			c.Check(err, IsNil)
			return
		}
		body, err := io.ReadAll(req.Body)
		c.Check(err, IsNil)
		var r struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		c.Check(json.Unmarshal(body, &r), IsNil)
		var result any
		switch r.Method {
		case "getnetworkinfo":
			result = map[string]any{"relayfee": 0.00001}
		case "getblockhash":
			result = strings.Repeat("0", 63) + "1"
		case "getblockstats":
			if chain.Equals(common.BCHChain) {
				result = map[string]any{"avgfeerate": float64(satsPer) / common.One} // BCH per byte
			} else {
				result = map[string]any{"avgfeerate": satsPer}
			}
		case "listunspent":
			result = []btcjson.ListUnspentResult{utxo}
		}
		buf, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result})
		c.Check(err, IsNil)
		_, err = rw.Write(buf)
		c.Check(err, IsNil)
	}))

	cfg.RPCHost = server.Listener.Addr().String()
	cfg.UserName = bob
	cfg.Password = password
	cfg.BlockScanner.StartBlockHeight = 1
	cfg.BlockScanner.DBPath = "" // in-memory scanner storage
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID:      "thorchain",
		ChainHost:    server.Listener.Addr().String(),
		SignerName:   bob,
		SignerPasswd: password,
	}, m, s.keys)
	c.Assert(err, IsNil)
	client, err := NewClient(s.keys, cfg, nil, bridge, m)
	c.Assert(err, IsNil)

	addr, err := client.nodePubKey.GetAddress(chain)
	c.Assert(err, IsNil)
	script, err := client.getSourceScript(stypes.TxOutItem{VaultPubKey: client.nodePubKey, Chain: chain})
	c.Assert(err, IsNil)
	utxo = btcjson.ListUnspentResult{
		TxID: strings.Repeat("ab", 32), Address: addr.String(), ScriptPubKey: hex.EncodeToString(script),
		Amount: 10, Confirmations: 10, Spendable: true,
	}
	return client, server, utxo, script
}

// blockAtFeeRate is a block whose one transaction paid satsPer sats/vbyte.
func blockAtFeeRate(chain common.Chain, satsPer uint64) *btcjson.GetBlockVerboseTxResult {
	const vsize = 1_000
	fees := btcutil.Amount(satsPer * vsize).ToBTC()
	return &btcjson.GetBlockVerboseTxResult{
		Height: 1201,
		Tx: []btcjson.TxRawResult{
			{Vin: []btcjson.Vin{{Coinbase: "01"}}, Vout: []btcjson.Vout{{Value: chain.DefaultCoinbase() + fees}}},
			{Vin: []btcjson.Vin{{Txid: strings.Repeat("cd", 32)}}, Vsize: vsize},
		},
	}
}

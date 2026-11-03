package ethereum

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/gascontract"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// nativeTransferOutGas is about what a router transferOut of ETH costs; the
// mock node returns it for eth_estimateGas.
const nativeTransferOutGas = 60_000

// TestGasUnitContract runs a market gas price through the whole THORNode <->
// bifrost gas-unit contract with the real code on both sides: the scanner's
// network-fee report in FetchTxs, THORNode's GasMgr, and SignTx. !3925 kept
// THORNode reading TransactionFeeRate in GetGasUnits units (gwei) but dropped
// bifrost's matching change, so the outbound gas price came out 10x what
// MaxGas paid for and every outbound aborted with "max gas exceeded".
func (s *EthereumSuite) TestGasUnitContract(c *C) {
	config.Init()
	cfg := config.GetBifrost().GetChains()[common.ETHChain]
	c.Assert(cfg.ChainID, Equals, common.ETHChain)

	const blockHeight = 101
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		c.Check(err, IsNil)
		var rpcReq struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		c.Check(json.Unmarshal(body, &rpcReq), IsNil)
		var result any
		switch rpcReq.Method {
		case "eth_chainId":
			result = "0x1"
		case "eth_blockNumber":
			result = fmt.Sprintf("0x%x", blockHeight)
		case "eth_getBlockByNumber":
			result = emptyBlockJSON(c, blockHeight)
		case "eth_getTransactionCount":
			result = "0x0"
		case "eth_estimateGas":
			result = fmt.Sprintf("0x%x", nativeTransferOutGas)
		case "eth_getBalance":
			result = "0x56bc75e2d63100000" // 100 ETH
		default:
			c.Errorf("unexpected RPC %s", rpcReq.Method)
		}
		buf, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcReq.ID, "result": result})
		c.Check(err, IsNil)
		_, err = rw.Write(buf)
		c.Check(err, IsNil)
	}))
	defer server.Close()
	cfg.RPCHost = server.URL
	cfg.BlockScanner.StartBlockHeight = 1
	cfg.BlockScanner.HTTPRequestTimeout = time.Second
	cfg.BlockScanner.DBPath = "" // in-memory scanner storage

	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	poolMgr := thorclient.NewPoolMgr(s.bridge)

	// Market prices in wei on the configured 10 gwei resolution grid, so the
	// scanner's round-up does not blur the ratio: the floor and a busy market.
	for _, priceWei := range []uint64{10e9, 200e9} {
		price := new(big.Int).SetUint64(priceWei)
		comment := Commentf("ETH at %s wei", price)
		e, err := NewClient(s.thorKeys, cfg, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
		c.Assert(err, IsNil)
		e.signingReady.Store(true)

		// 1. bifrost scanner: base fee plus tip for a full gas cache, then the
		// network fee FetchTxs posts for the next (empty) block
		feeQueue := make(chan common.NetworkFee, 1)
		e.ethScanner.globalNetworkFeeQueue = feeQueue
		tip := new(big.Int).Div(price, big.NewInt(10))
		baseFee := new(big.Int).Sub(price, tip)
		for i := 0; i < cfg.BlockScanner.GasCacheBlocks; i++ {
			e.ethScanner.updateGasPrice(baseFee, []*big.Int{tip})
		}
		_, err = e.ethScanner.FetchTxs(blockHeight, blockHeight)
		c.Assert(err, IsNil, comment)
		c.Assert(feeQueue, HasLen, 1, comment)
		networkFee := <-feeQueue

		// 2. THORNode: GasMgr turns the stored network fee into MaxGas and GasRate
		maxGas, gasRate, err := gascontract.GasDetails(networkFee)
		c.Assert(err, IsNil, comment)

		// 3. bifrost client: sign the outbound THORNode scheduled with them
		inHash := types2.GetRandomTxHash()
		raw, _, _, err := e.SignTx(stypes.TxOutItem{
			Chain:       common.ETHChain,
			ToAddress:   types2.GetRandomETHAddress(),
			VaultPubKey: e.localPubKey,
			Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))},
			MaxGas:      common.Gas{maxGas},
			GasRate:     gasRate,
			InHash:      inHash,
			Memo:        mem.NewOutboundMemo(inHash).String(),
		}, 1)
		c.Assert(err, IsNil, comment)

		// 4. the outbound is signed, at about the observed price, within what THORNode reserved
		detail := fmt.Sprintf("ETH at %s wei: posted rate %d (size %d), THORNode GasRate %d MaxGas %s",
			price, networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas)
		if !c.Check(raw, NotNil, Commentf("outbound aborted (max gas exceeded): %s", detail)) {
			continue
		}
		tx := &etypes.Transaction{}
		c.Assert(tx.UnmarshalJSON(raw), IsNil)
		maxPrice := new(big.Int).Div(new(big.Int).Mul(price, big.NewInt(3)), big.NewInt(2))
		c.Check(tx.GasFeeCap().Cmp(price) >= 0 && tx.GasFeeCap().Cmp(maxPrice) <= 0, Equals, true,
			Commentf("max fee per gas %s wei, want [%s, %s]: %s", tx.GasFeeCap(), price, maxPrice, detail))
		maxFee := new(big.Int).Mul(new(big.Int).SetUint64(tx.Gas()), tx.GasFeeCap())
		reserved := e.convertThorchainAmountToWei(maxGas.Amount.BigInt())
		c.Check(maxFee.Cmp(reserved) <= 0, Equals, true,
			Commentf("gas limit %d at %s wei can spend %s wei, THORNode reserved %s wei: %s",
				tx.Gas(), tx.GasFeeCap(), maxFee, reserved, detail))
	}
}

// emptyBlockJSON is an eth_getBlockByNumber result with no transactions, so
// FetchTxs leaves the gas cache alone and only reports the network fee.
func emptyBlockJSON(c *C, height int64) map[string]any {
	header := &etypes.Header{
		ParentHash:  ecommon.HexToHash("0x01"),
		UncleHash:   etypes.EmptyUncleHash,
		Root:        etypes.EmptyRootHash,
		TxHash:      etypes.EmptyTxsHash,
		ReceiptHash: etypes.EmptyReceiptsHash,
		Difficulty:  big.NewInt(0),
		Number:      big.NewInt(height),
		GasLimit:    30_000_000,
		Time:        1_700_000_000,
		BaseFee:     big.NewInt(1),
	}
	buf, err := header.MarshalJSON()
	c.Assert(err, IsNil)
	block := map[string]any{}
	c.Assert(json.Unmarshal(buf, &block), IsNil)
	block["transactions"] = []any{}
	block["uncles"] = []any{}
	return block
}

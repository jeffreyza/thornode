package evm

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/gascontract"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// nativeTransferOutGas is about what a router transferOut of the gas asset
// costs; the mock node returns it for eth_estimateGas.
const nativeTransferOutGas = 60_000

// TestGasUnitContract runs a market gas price through the whole THORNode <->
// bifrost gas-unit contract with the real code on both sides: the scanner's
// network-fee report, THORNode's GasMgr, and the client's outbound builder.
// !3925 kept THORNode reading TransactionFeeRate in GetGasUnits units but
// dropped bifrost's matching change; the outbound gas price came out 10x what
// MaxGas paid for and every EVM outbound aborted with "max gas exceeded". Each
// side's own tests used hand-set values in its own units, so neither noticed.
func (s *EVMSuite) TestGasUnitContract(c *C) {
	config.Init()
	chains := config.GetBifrost().GetChains()

	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		c.Check(err, IsNil)
		var rpcReq struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		c.Check(json.Unmarshal(body, &rpcReq), IsNil)
		c.Check(rpcReq.Method, Equals, "eth_estimateGas")
		_, err = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":%s,"result":"0x%x"}`, rpcReq.ID, nativeTransferOutGas)
		c.Check(err, IsNil)
	}))
	defer server.Close()
	ethClient, err := ethclient.Dial(server.URL)
	c.Assert(err, IsNil)

	vaultABI, _, err := evm.GetContractABI(routerContractABI, erc20ContractABI)
	c.Assert(err, IsNil)

	// Market prices in wei, on each chain's configured gas_price_resolution
	// grid so the scanner's round-up does not blur the ratio: the floor the
	// scanner can report, and a busy market.
	for _, tc := range []struct {
		chain     common.Chain
		pricesWei []uint64
	}{
		{common.AVAXChain, []uint64{10e9, 250e9}},
		{common.BSCChain, []uint64{1e9, 5e9}},
		{common.BASEChain, []uint64{1e8, 5e9}},
		{common.POLChain, []uint64{30e9, 500e9}},
	} {
		cfg := chains[tc.chain]
		c.Assert(cfg.ChainID, Equals, tc.chain)
		ethRPC, err := evm.NewEthRPC(ethClient, time.Second, tc.chain.String())
		c.Assert(err, IsNil)

		for i, priceWei := range tc.pricesWei {
			price := new(big.Int).SetUint64(priceWei)
			comment := Commentf("%s at %s wei", tc.chain, price)

			// 1. bifrost scanner: observe the price for a full gas cache and post the network fee
			feeQueue := make(chan common.NetworkFee, 1)
			scanner := &EVMScanner{
				cfg:                   cfg.BlockScanner,
				m:                     s.m,
				ethRpc:                ethRPC,
				globalNetworkFeeQueue: feeQueue,
			}
			scanner.gasPrice.Store(big.NewInt(0))
			for j := 0; j < cfg.BlockScanner.GasCacheBlocks; j++ {
				scanner.updateGasPrice([]*big.Int{price})
			}
			scanner.reportNetworkFee(int64(101 + i))
			c.Assert(feeQueue, HasLen, 1, comment)
			networkFee := <-feeQueue

			// 2. THORNode: GasMgr turns the stored network fee into MaxGas and GasRate
			maxGas, gasRate, err := gascontract.GasDetails(networkFee)
			c.Assert(err, IsNil, comment)

			// 3. bifrost client: build the outbound THORNode scheduled with them
			client := &EVMClient{
				logger:     zerolog.Nop(),
				cfg:        cfg,
				evmScanner: scanner,
				vaultABI:   vaultABI,
				pubkeyMgr:  gasContractRouter{router: types2.GetRandomETHAddress()},
			}
			toi := stypes.TxOutItem{
				Chain:       tc.chain,
				ToAddress:   types2.GetRandomETHAddress(),
				VaultPubKey: types2.GetRandomPubKey(),
				Coins:       common.Coins{common.NewCoin(tc.chain.GetGasAsset(), cosmos.NewUint(common.One))},
				MaxGas:      common.Gas{maxGas},
				GasRate:     gasRate,
				InHash:      types2.GetRandomTxHash(),
			}
			toi.Memo = mem.NewOutboundMemo(toi.InHash).String()
			memo, err := mem.ParseMemo(common.LatestVersion, toi.Memo)
			c.Assert(err, IsNil)
			tx, err := client.buildOutboundTx(toi, memo, 0)
			c.Assert(err, IsNil, comment)

			// 4. the outbound is built, at about the observed price, within what THORNode reserved
			detail := fmt.Sprintf("%s at %s wei: posted rate %d (size %d), THORNode GasRate %d MaxGas %s",
				tc.chain, price, networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas)
			if !c.Check(tx, NotNil, Commentf("outbound aborted (max gas exceeded): %s", detail)) {
				continue
			}
			maxPrice := new(big.Int).Div(new(big.Int).Mul(price, big.NewInt(3)), big.NewInt(2))
			c.Check(tx.GasPrice().Cmp(price) >= 0 && tx.GasPrice().Cmp(maxPrice) <= 0, Equals, true,
				Commentf("effective gas price %s wei, want [%s, %s]: %s", tx.GasPrice(), price, maxPrice, detail))
			maxFee := new(big.Int).Mul(new(big.Int).SetUint64(tx.Gas()), tx.GasPrice())
			reserved := convertThorchainAmountToWei(maxGas.Amount.BigInt())
			c.Check(maxFee.Cmp(reserved) <= 0, Equals, true,
				Commentf("gas limit %d at %s wei can spend %s wei, THORNode reserved %s wei: %s",
					tx.Gas(), tx.GasPrice(), maxFee, reserved, detail))
		}
	}
}

// gasContractRouter answers the vault router lookup buildOutboundTx makes.
type gasContractRouter struct {
	pubkeymanager.PubKeyValidator
	router common.Address
}

func (r gasContractRouter) GetContract(common.Chain, common.PubKey) common.Address {
	return r.router
}

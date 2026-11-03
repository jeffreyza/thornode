package xrp

import (
	"encoding/hex"
	"fmt"
	"strconv"

	binarycodec "github.com/Peersyst/xrpl-go/binary-codec"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/rpc/testutil"
	txtypes "github.com/Peersyst/xrpl-go/xrpl/transaction/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/gascontract"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestGasUnitContract runs a market fee through the whole THORNode <->
// bifrost gas-unit contract with the real code on both sides: the scanner's
// fee cache and network-fee report, THORNode's GasMgr, and SignTx. The
// scanner posts a whole-transaction fee in drops (size 1), and the signer pays
// GasRate drops outright, so the signed Fee must sit between the observed fee
// and THORNode's 1.5x buffer on it. !3925 shipped THORNode reading
// TransactionFeeRate in GetGasUnits units while bifrost still posted 1e8 units.
func (s *XrpTestSuite) TestGasUnitContract(c *C) {
	config.Init()
	cfg := config.GetBifrost().GetChains()[common.XRPChain]
	c.Assert(cfg.ChainID, Equals, common.XRPChain)
	cfg.ChainNetwork = "1234"
	cfg.RPCHost = "http://testnode/"
	cfg.BlockScanner.StartBlockHeight = 1
	cfg.BlockScanner.DBPath = "" // in-memory scanner storage

	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	cmtPub, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	vaultPubKey, err := common.NewPubKeyFromCrypto(cmtPub)
	c.Assert(err, IsNil)

	// Market fees in drops on the configured gas_price_resolution grid, so the
	// scanner's round-up does not blur the ratio: the floor and a busy market.
	for _, feeDrops := range []uint64{100, 2_000} {
		comment := Commentf("XRP at %d drops", feeDrops)
		client, err := NewClient(s.thorKeys, cfg, nil, s.bridge, s.m)
		c.Assert(err, IsNil)
		mc := &testutil.JSONRPCMockClient{}
		mc.DoFunc = testutil.MockResponse(`{"result":{"ledger_current_index":50}}`, 200, mc)
		rpcCfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
		c.Assert(err, IsNil)
		client.xrpScanner.rpcClient = rpc.NewClient(rpcCfg)
		client.accts.Set(vaultPubKey, XrpMetadata{SeqNumber: 5, BlockHeight: 100})

		// 1. bifrost scanner: observe the fee for a full cache and post the network fee
		feeQueue := make(chan common.NetworkFee, 1)
		client.xrpScanner.globalNetworkFeeQueue = feeQueue
		fee, err := fromXrpToThorchain(txtypes.XRPCurrencyAmount(feeDrops))
		c.Assert(err, IsNil)
		for i := 0; i < FeeCacheTransactions; i++ {
			client.xrpScanner.updateFeeCache(fee)
		}
		c.Assert(client.xrpScanner.updateFees(FeeUpdatePeriodBlocks), IsNil, comment)
		c.Assert(feeQueue, HasLen, 1, comment)
		networkFee := <-feeQueue

		// 2. THORNode: GasMgr turns the stored network fee into MaxGas and GasRate
		maxGas, gasRate, err := gascontract.GasDetails(networkFee)
		c.Assert(err, IsNil, comment)

		// 3. bifrost client: sign the outbound THORNode scheduled with them
		toAddr, err := stypes.GetRandomPubKey().GetAddress(common.XRPChain)
		c.Assert(err, IsNil)
		inHash := stypes.GetRandomTxHash()
		signed, _, _, err := client.SignTx(types.TxOutItem{
			Chain:       common.XRPChain,
			ToAddress:   toAddr,
			VaultPubKey: vaultPubKey,
			Coins:       common.Coins{common.NewCoin(common.XRPAsset, cosmos.NewUint(common.One))},
			MaxGas:      common.Gas{maxGas},
			GasRate:     gasRate,
			InHash:      inHash,
			Memo:        mem.NewOutboundMemo(inHash).String(),
		}, 100)
		c.Assert(err, IsNil, comment)

		// 4. the signed fee is the observed fee up to THORNode's 1.5x buffer
		detail := fmt.Sprintf("XRP at %d drops: posted rate %d (size %d), THORNode GasRate %d MaxGas %s",
			feeDrops, networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas)
		if !c.Check(signed, NotNil, Commentf("outbound not signed: %s", detail)) {
			continue
		}
		flatTx, err := binarycodec.Decode(hex.EncodeToString(signed))
		c.Assert(err, IsNil)
		feeField, ok := flatTx["Fee"].(string)
		c.Assert(ok, Equals, true)
		signedFee, err := strconv.ParseUint(feeField, 10, 64)
		c.Assert(err, IsNil)
		c.Check(signedFee >= feeDrops && signedFee*2 <= feeDrops*3, Equals, true,
			Commentf("signed fee %d drops, want [%d, %d]: %s", signedFee, feeDrops, feeDrops*3/2, detail))
	}
}

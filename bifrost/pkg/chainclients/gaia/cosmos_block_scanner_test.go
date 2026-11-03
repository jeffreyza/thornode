package gaia

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/cometbft/cometbft/crypto/tmhash"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	ibctransfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	ibccoretypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcchanneltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	ibclightclient "github.com/cosmos/ibc-go/v8/modules/light-clients/07-tendermint"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	tmtypes "github.com/cometbft/cometbft/types"

	protov2 "google.golang.org/protobuf/proto"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"

	"gitlab.com/thorchain/thornode/v3/cmd"
	. "gopkg.in/check.v1"
)

// -------------------------------------------------------------------------------------
// Mock FeeTx
// -------------------------------------------------------------------------------------

var _ ctypes.FeeTx = &MockFeeTx{}

type MockFeeTx struct {
	fee ctypes.Coins
	gas uint64
}

func (m *MockFeeTx) GetMsgs() []ctypes.Msg {
	return nil
}

func (m *MockFeeTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

func (m *MockFeeTx) ValidateBasic() error {
	return nil
}

func (m *MockFeeTx) GetGas() uint64 {
	return m.gas
}

func (m *MockFeeTx) GetFee() ctypes.Coins {
	return m.fee
}

func (m *MockFeeTx) FeePayer() []byte {
	return nil
}

func (m *MockFeeTx) FeeGranter() []byte {
	return nil
}

// -------------------------------------------------------------------------------------
// Tests
// -------------------------------------------------------------------------------------

type BlockScannerTestSuite struct {
	m      *metrics.Metrics
	bridge thorclient.ThorchainBridge
	keys   *thorclient.Keys
}

var _ = Suite(&BlockScannerTestSuite{})

func assertTxInItem(c *C, item *stypes.TxInItem, tx, sender, to, memo string, amount uint64) {
	c.Assert(item.Tx, Equals, tx)
	c.Assert(item.Sender, Equals, sender)
	c.Assert(item.To, Equals, to)
	c.Assert(item.Memo, Equals, memo)
	c.Assert(item.Coins, HasLen, 1)
	c.Assert(item.Coins[0].Asset.Equals(common.ATOMAsset), Equals, true)
	c.Assert(item.Coins[0].Amount.Uint64(), Equals, amount)
}

func (s *BlockScannerTestSuite) SetUpSuite(c *C) {
	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: "",
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)
	thorKeys := thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	c.Assert(err, IsNil)
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, thorKeys)
	c.Assert(err, IsNil)
	s.keys = thorKeys
}

func (s *BlockScannerTestSuite) TestCalculateAverageGasFees(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{
		ChainID:            common.GAIAChain,
		GasPriceResolution: 1_000, // 1,000 uatom
		WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
			{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
		},
	}
	blockScanner := CosmosBlockScanner{cfg: cfg}

	blockScanner.updateGasCache(&MockFeeTx{
		gas: GasLimit / 2,
		fee: ctypes.Coins{ctypes.NewCoin("uatom", sdkmath.NewInt(10000))},
	})
	c.Check(len(blockScanner.feeCache), Equals, 1)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(20000)))

	blockScanner.updateGasCache(&MockFeeTx{
		gas: GasLimit / 2,
		fee: ctypes.Coins{ctypes.NewCoin("uatom", sdkmath.NewInt(10000))},
	})
	c.Check(len(blockScanner.feeCache), Equals, 2)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(20000)))

	// two blocks at half fee should average to 75% of last
	blockScanner.updateGasCache(&MockFeeTx{
		gas: GasLimit,
		fee: ctypes.Coins{ctypes.NewCoin("uatom", sdkmath.NewInt(10000))},
	})
	blockScanner.updateGasCache(&MockFeeTx{
		gas: GasLimit,
		fee: ctypes.Coins{ctypes.NewCoin("uatom", sdkmath.NewInt(10000))},
	})
	c.Check(len(blockScanner.feeCache), Equals, 4)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(15000)))

	// skip transactions with multiple coins
	blockScanner.updateGasCache(&MockFeeTx{
		gas: GasLimit,
		fee: ctypes.Coins{
			ctypes.NewCoin("uatom", sdkmath.NewInt(10000)),
			ctypes.NewCoin("uusd", sdkmath.NewInt(10000)),
		},
	})
	c.Check(len(blockScanner.feeCache), Equals, 4)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(15000)))

	// skip transactions with fees not in uatom
	blockScanner.updateGasCache(&MockFeeTx{
		gas: GasLimit,
		fee: ctypes.Coins{
			ctypes.NewCoin("uusd", sdkmath.NewInt(10000)),
		},
	})
	c.Check(len(blockScanner.feeCache), Equals, 4)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(15000)))

	// skip transactions with zero fee
	blockScanner.updateGasCache(&MockFeeTx{
		gas: GasLimit,
		fee: ctypes.Coins{
			ctypes.NewCoin("uusd", sdkmath.NewInt(0)),
		},
	})
	c.Check(len(blockScanner.feeCache), Equals, 4)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(15000)))

	// ensure we only cache the transaction limit number of blocks
	for i := 0; i < GasCacheTransactions; i++ {
		blockScanner.updateGasCache(&MockFeeTx{
			gas: GasLimit,
			fee: ctypes.Coins{
				ctypes.NewCoin("uatom", sdkmath.NewInt(10000)),
			},
		})
	}
	c.Check(len(blockScanner.feeCache), Equals, GasCacheTransactions)
	c.Check(blockScanner.averageFee().String(), Equals, fmt.Sprintf("%d", uint64(10000)))
}

func (s *BlockScannerTestSuite) TestGetBlock(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{ChainID: common.GAIAChain}
	blockScanner := CosmosBlockScanner{
		cfg: cfg,
		rpc: &mockTendermintRPC{},
	}

	block, err := blockScanner.GetBlock(1)

	c.Assert(err, IsNil)
	c.Assert(len(block.Data.Txs), Equals, 1)
	c.Assert(block.Header.Height, Equals, int64(6509672))
}

func (s *BlockScannerTestSuite) TestProcessTxs(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{
		ChainID: common.GAIAChain,
		WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
			{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
			{Denom: "ibc/5DB84693CB3F67A2D25D133C177B06786F423D6D1AFF2B8E080F0753B2E4D585", Decimals: 6, THORChainSymbol: "NTRN"},
		},
	}
	registry := s.bridge.GetContext().InterfaceRegistry
	btypes.RegisterInterfaces(registry)
	ibcchanneltypes.RegisterInterfaces(registry)
	ibccoretypes.RegisterInterfaces(registry)
	ibclightclient.RegisterInterfaces(registry)

	cdc := codec.NewProtoCodec(registry)

	blockScanner := CosmosBlockScanner{
		cfg:    cfg,
		rpc:    &mockTendermintRPC{},
		cdc:    cdc,
		logger: log.Logger.With().Str("module", "blockscanner").Str("chain", common.GAIAChain.String()).Logger(),
	}

	block, err := blockScanner.GetBlock(1)
	c.Assert(err, IsNil)

	txInItems, err := blockScanner.processTxs(1, block.Data.Txs)
	c.Assert(err, IsNil)

	// proccessTxs should filter out everything besides the valid MsgSend
	c.Assert(len(txInItems), Equals, 1)
	assertTxInItem(c, txInItems[0],
		"712c71e7eb9636aaf6112eaa3cd002a37fdf63c04b9b8e3eb096f265fca9a52c",
		"cosmos1q5mpsyzzh09e9p3rs72e5hqwsfkdv4zwwrspaz",
		"cosmos1c39nmn8nze5cqf8dqmqdq7ue3x98kyqv35gc0t",
		"test",
		100000000000,
	)

	// ibc deposits are not observed, even for whitelisted denoms
	// ------------------------------------------------------------------------

	for _, height := range []int64{
		11350886, // single deposit via ibc
		11350935, // two deposits in single ibc transaction
		26750027, // two ibc txs sending atom back to gaia (from osmosis & secret)
	} {
		block, err = blockScanner.GetBlock(height)
		c.Assert(err, IsNil)

		txInItems, err = blockScanner.processTxs(height, block.Data.Txs)
		c.Assert(err, IsNil)

		c.Assert(txInItems, HasLen, 0, Commentf("height %d", height))
	}

	// ibc usdc from noble to gaia + atom transfer
	// ------------------------------------------------------------------------

	block, err = blockScanner.GetBlock(26757930)
	c.Assert(err, IsNil)

	txInItems, err = blockScanner.processTxs(26757930, block.Data.Txs)
	c.Assert(err, IsNil)

	// only reports the atom transfer
	c.Assert(len(txInItems), Equals, 1)
	assertTxInItem(c, txInItems[0],
		"d1490eb0303aa7f4612f46826230b14102ab6d3ee77645c3710ed53be31ad230",
		"cosmos1t5u0jfg3ljsjrh2m9e47d4ny2hea7eehxrzdgd",
		"cosmos1l0vnemk4q9kcvhyh2vxnn4d8pl7v42ym4rfehl",
		"",
		29886400,
	)
}

func (s *BlockScannerTestSuite) TestProcessTxsMsgSendWithIBCMessage(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{
		ChainID: common.GAIAChain,
		WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
			{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
		},
	}
	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)

	// use the production codec so the test covers its interface registrations
	blockScanner, err := NewCosmosBlockScanner("http://localhost:26657", cfg, storage, s.bridge, s.m, nil, nil)
	c.Assert(err, IsNil)
	blockScanner.rpc = &mockTendermintRPC{}

	// take the MsgUpdateClient from a real relayer tx and batch it with a MsgSend
	block, err := blockScanner.GetBlock(11350886)
	c.Assert(err, IsNil)
	c.Assert(block.Data.Txs, HasLen, 1)
	relayerTx, err := blockScanner.txConfig.TxDecoder()(block.Data.Txs[0])
	c.Assert(err, IsNil)
	updateClient, ok := relayerTx.GetMsgs()[0].(*ibccoretypes.MsgUpdateClient)
	c.Assert(ok, Equals, true)

	sender := "cosmos1q5mpsyzzh09e9p3rs72e5hqwsfkdv4zwwrspaz"
	vault := "cosmos1c39nmn8nze5cqf8dqmqdq7ue3x98kyqv35gc0t"
	builder := blockScanner.txConfig.NewTxBuilder()
	err = builder.SetMsgs(updateClient, &btypes.MsgSend{
		FromAddress: sender,
		ToAddress:   vault,
		Amount:      ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(1000000))),
	})
	c.Assert(err, IsNil)
	builder.SetMemo("test")
	builder.SetGasLimit(GasLimit)
	builder.SetFeeAmount(ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(10000))))
	rawTx, err := blockScanner.txConfig.TxEncoder()(builder.GetTx())
	c.Assert(err, IsNil)
	block.Data.Txs[0] = rawTx

	txInItems, err := blockScanner.processTxs(11350886, block.Data.Txs)
	c.Assert(err, IsNil)
	c.Assert(txInItems, HasLen, 1)
	assertTxInItem(c, txInItems[0],
		hex.EncodeToString(tmhash.Sum(rawTx)), sender, vault, "test", 100000000,
	)
}

// -------------------------------------------------------------------------------------
// Wasm-bearing transactions
// -------------------------------------------------------------------------------------

// A transaction bundling MsgExecuteContract with a MsgSend deposit must still have
// the MsgSend observed. Tx decoding is all-or-nothing, so before the codec
// registered the CosmWasm types the WHOLE transaction failed decode ("unable to
// resolve type URL /cosmwasm.wasm.v1.MsgExecuteContract") and the bundled deposit
// was silently dropped by every node on the network. This test builds its scanner
// with newGaiaCodec — the exact production registry — because registering types
// inside the test (as other tests here do) would have hidden the bug.
func (s *BlockScannerTestSuite) TestProcessTxsObservesMsgSendBundledWithWasm(c *C) {
	cfg := config.BifrostBlockScannerConfiguration{
		ChainID: common.GAIAChain,
		WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
			{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
		},
	}

	cdc := newGaiaCodec()
	txConfig := tx.NewTxConfig(cdc, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})

	blockScanner := CosmosBlockScanner{
		cfg:    cfg,
		rpc:    &mockTendermintRPC{},
		cdc:    cdc,
		logger: log.Logger.With().Str("module", "blockscanner").Str("chain", common.GAIAChain.String()).Logger(),
	}

	from := ctypes.AccAddress(make([]byte, 20))
	to := ctypes.AccAddress(bytes.Repeat([]byte{0x1}, 20))

	builder := txConfig.NewTxBuilder()
	err := builder.SetMsgs(
		&wasmtypes.MsgExecuteContract{
			Sender:   from.String(),
			Contract: to.String(),
			Msg:      wasmtypes.RawContractMessage(`{"swap":{}}`),
		},
		&btypes.MsgSend{
			FromAddress: from.String(),
			ToAddress:   to.String(),
			Amount:      ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(5_000_000))),
		},
	)
	c.Assert(err, IsNil)
	builder.SetMemo("ADD:GAIA.ATOM")

	rawTx, err := txConfig.TxEncoder()(builder.GetTx())
	c.Assert(err, IsNil)

	txInItems, err := blockScanner.processTxs(1, []tmtypes.Tx{rawTx})
	c.Assert(err, IsNil)

	// The wasm message itself is not a deposit vector; the bundled MsgSend is.
	c.Assert(len(txInItems), Equals, 1)
	c.Assert(txInItems[0].Memo, Equals, "ADD:GAIA.ATOM")
	c.Assert(len(txInItems[0].Coins), Equals, 1)
	c.Assert(txInItems[0].Coins[0].Asset.String(), Equals, "GAIA.ATOM")
	c.Assert(txInItems[0].Coins[0].Amount.String(), Equals, "500000000") // 5 ATOM in 1e8

	// Same shape with an IBC MsgTransfer sibling — MsgTransfer was never
	// registered in the decode codec, so this bundle was dropped whole just like
	// the wasm one.
	builder = txConfig.NewTxBuilder()
	err = builder.SetMsgs(
		&ibctransfertypes.MsgTransfer{
			SourcePort:    "transfer",
			SourceChannel: "channel-0",
			Token:         ctypes.NewCoin("uatom", sdkmath.NewInt(1_000_000)),
			Sender:        from.String(),
			Receiver:      to.String(),
			TimeoutHeight: ibccoretypes.Height{RevisionNumber: 1, RevisionHeight: 1},
		},
		&btypes.MsgSend{
			FromAddress: from.String(),
			ToAddress:   to.String(),
			Amount:      ctypes.NewCoins(ctypes.NewCoin("uatom", sdkmath.NewInt(5_000_000))),
		},
	)
	c.Assert(err, IsNil)
	builder.SetMemo("ADD:GAIA.ATOM")

	rawTx, err = txConfig.TxEncoder()(builder.GetTx())
	c.Assert(err, IsNil)

	txInItems, err = blockScanner.processTxs(1, []tmtypes.Tx{rawTx})
	c.Assert(err, IsNil)
	c.Assert(len(txInItems), Equals, 1)
	c.Assert(txInItems[0].Coins[0].Asset.String(), Equals, "GAIA.ATOM")
}

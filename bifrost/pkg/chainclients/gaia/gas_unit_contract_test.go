package gaia

import (
	"context"
	"fmt"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/rs/zerolog"
	grpc "google.golang.org/grpc"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/gascontract"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestGasUnitContract runs a market gas price through the whole THORNode <->
// bifrost gas-unit contract with the real code on both sides: the scanner's
// fee cache and network-fee report, THORNode's GasMgr, and SignTx. !3925 left
// bifrost posting rates in 1e8 units while THORNode read them in uatom, and
// GAIA MaxGas came out 100x too high. The scanner posts a whole-transaction
// fee (size 1), so the signer pays MaxGas outright for GasLimit gas.
func (s *ExtraTestSuite) TestGasUnitContract(c *C) {
	config.Init()
	chains := config.GetBifrost().GetChains()

	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)
	cmtPub, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)
	vaultPubKey, err := common.NewPubKeyFromCrypto(cmtPub)
	c.Assert(err, IsNil)
	localKm := &keyManager{
		privKey: priv,
		addr:    ctypes.AccAddress(priv.PubKey().Address()),
		pubkey:  vaultPubKey,
	}

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	interfaceRegistry.RegisterImplementations((*ctypes.Msg)(nil), &btypes.MsgSend{})
	cryptocodec.RegisterInterfaces(interfaceRegistry) // to decode the signed tx
	txConfig := tx.NewTxConfig(codec.NewProtoCodec(interfaceRegistry), []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})

	// Market prices as an observed transaction fee and the gas it bought: a
	// quiet and a busy market, each on the configured gas_price_resolution grid
	// once scaled to GasLimit, so the scanner's round-up does not blur the ratio.
	// Both sit above the grid's first step: updateGasFees never posts a first
	// fee equal to gas_price_resolution.
	type observedFee struct {
		fee int64
		gas uint64
	}
	for _, tc := range []struct {
		chain    common.Chain
		denom    string
		observed []observedFee
	}{
		{common.GAIAChain, "uatom", []observedFee{{5_000, 200_000}, {20_000, 100_000}}},
		{common.NOBLEChain, "uusdc", []observedFee{{200_000, 200_000}, {150_000, 50_000}}},
	} {
		cfg := chains[tc.chain]
		c.Assert(cfg.ChainID, Equals, tc.chain)

		for i, obs := range tc.observed {
			comment := Commentf("%s at %d%s for %d gas", tc.chain, obs.fee, tc.denom, obs.gas)

			// 1. bifrost scanner: observe the fee for a full cache and post the network fee
			feeQueue := make(chan common.NetworkFee, 1)
			scanner := &CosmosBlockScanner{
				cfg:                   cfg.BlockScanner,
				logger:                zerolog.Nop(),
				rpc:                   &mockTendermintRPC{},
				globalNetworkFeeQueue: feeQueue,
			}
			for j := 0; j < GasCacheTransactions; j++ {
				scanner.updateGasCache(&MockFeeTx{
					fee: ctypes.Coins{ctypes.NewCoin(tc.denom, sdkmath.NewInt(obs.fee))},
					gas: obs.gas,
				})
			}
			c.Assert(scanner.updateGasFees(GasUpdatePeriodBlocks), IsNil, comment)
			c.Assert(feeQueue, HasLen, 1, comment)
			networkFee := <-feeQueue

			// 2. THORNode: GasMgr turns the stored network fee into MaxGas and GasRate
			maxGas, gasRate, err := gascontract.GasDetails(networkFee)
			c.Assert(err, IsNil, comment)

			// 3. bifrost client: sign the outbound THORNode scheduled with them
			storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
			c.Assert(err, IsNil)
			signerCacheMgr, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
			c.Assert(err, IsNil)
			client := CosmosClient{
				logger:             zerolog.Nop(),
				cfg:                cfg,
				txConfig:           txConfig,
				cosmosScanner:      scanner,
				bankClient:         richBankClient{QueryClient: NewMockBankServiceClient(), denom: tc.denom},
				accountClient:      NewMockAccountServiceClient(),
				chainID:            cfg.CosmosChainID,
				localKeyManager:    localKm,
				accts:              NewCosmosMetaDataStore(),
				signerCacheManager: signerCacheMgr,
				thorchainBridge:    s.bridge,
			}
			toAddr, err := types2.GetRandomPubKey().GetAddress(tc.chain)
			c.Assert(err, IsNil)
			inHash := types2.GetRandomTxHash()
			txBytes, _, _, err := client.SignTx(stypes.TxOutItem{
				Chain:       tc.chain,
				ToAddress:   toAddr,
				VaultPubKey: vaultPubKey,
				Coins:       common.Coins{common.NewCoin(tc.chain.GetGasAsset(), cosmos.NewUint(common.One))},
				MaxGas:      common.Gas{maxGas},
				GasRate:     gasRate,
				InHash:      inHash,
				Memo:        mem.NewOutboundMemo(inHash).String(),
			}, int64(i+1))
			c.Assert(err, IsNil, comment)
			c.Assert(storage.Close(), IsNil)

			// 4. the signed fee buys gas at about the observed price
			detail := fmt.Sprintf("%s at %d%s for %d gas: posted rate %d (size %d), THORNode GasRate %d MaxGas %s",
				tc.chain, obs.fee, tc.denom, obs.gas, networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas)
			if !c.Check(txBytes, NotNil, Commentf("outbound not signed: %s", detail)) {
				continue
			}
			decoded, err := txConfig.TxDecoder()(txBytes)
			c.Assert(err, IsNil)
			feeTx, ok := decoded.(ctypes.FeeTx)
			c.Assert(ok, Equals, true)
			signedFee := feeTx.GetFee().AmountOf(tc.denom).BigInt()
			signedGas := sdkmath.NewIntFromUint64(feeTx.GetGas()).BigInt()
			// signedFee/signedGas within [1, 1.5] x obs.fee/obs.gas, cross-multiplied
			paid := sdkmath.NewIntFromBigInt(signedFee).MulRaw(int64(obs.gas))
			observed := sdkmath.NewInt(obs.fee).Mul(sdkmath.NewIntFromBigInt(signedGas))
			c.Check(paid.GTE(observed) && paid.MulRaw(2).LTE(observed.MulRaw(3)), Equals, true,
				Commentf("signed fee %s%s for %s gas, observed %d%s for %d gas, want 1-1.5x the price: %s",
					signedFee, tc.denom, signedGas, obs.fee, tc.denom, obs.gas, detail))
		}
	}
}

// richBankClient funds the vault with plenty of the chain's gas denom, so the
// pre-sign balance check passes for any sane MaxGas.
type richBankClient struct {
	btypes.QueryClient
	denom string
}

func (b richBankClient) AllBalances(context.Context, *btypes.QueryAllBalancesRequest, ...grpc.CallOption) (*btypes.QueryAllBalancesResponse, error) {
	return &btypes.QueryAllBalancesResponse{
		Balances: ctypes.Coins{ctypes.NewCoin(b.denom, sdkmath.NewInt(1_000_000_000_000))},
	}, nil
}

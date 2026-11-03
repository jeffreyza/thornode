package gaia

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	feegranttypes "cosmossdk.io/x/feegrant"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	icacontrollertypes "github.com/cosmos/ibc-go/v8/modules/apps/27-interchain-accounts/controller/types"
	ibctransfertypes "github.com/cosmos/ibc-go/v8/modules/apps/transfer/types"
	ibccoretypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	ibcchanneltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	ibclightclient "github.com/cosmos/ibc-go/v8/modules/light-clients/07-tendermint"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	ctypes "github.com/cosmos/cosmos-sdk/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authvesting "github.com/cosmos/cosmos-sdk/x/auth/vesting/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	distribtypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	govv1types "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	govv1beta1types "github.com/cosmos/cosmos-sdk/x/gov/types/v1beta1"
	grouptypes "github.com/cosmos/cosmos-sdk/x/group"
	paramsproptypes "github.com/cosmos/cosmos-sdk/x/params/types/proposal"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/cometbft/cometbft/crypto/tmhash"
	rpcclienthttp "github.com/cometbft/cometbft/rpc/client/http"
	tmtypes "github.com/cometbft/cometbft/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// SolvencyReporter is to report solvency info to THORNode
type SolvencyReporter func(int64) error

const (
	// GasUpdatePeriodBlocks is the block interval at which we report gas fee changes.
	GasUpdatePeriodBlocks = 10

	// GasPriceFactor is a multiplier applied to the gas amount before dividing by the gas
	// limit to determine the gas price, and later used as a divisor on the final fee -
	// this avoid the integer division going to zero, and can be thought of as the
	// reciprocal of the gas price precision.
	GasPriceFactor = uint64(1e9)

	// GasLimit is the default gas limit we will use for all outbound transactions.
	GasLimit = 200000

	// GasCacheTransactions is the number of transactions over which we compute an average
	// (mean) gas price to use for outbound transactions. Note that only transactions
	// using the chain fee asset will be considered.
	GasCacheTransactions = 100
)

var (
	ErrInvalidScanStorage = errors.New("scan storage is empty or nil")
	ErrInvalidMetrics     = errors.New("metrics is empty or nil")
	ErrEmptyTx            = errors.New("empty tx")
)

// CosmosBlockScanner is to scan the blocks
type CosmosBlockScanner struct {
	cfg                   config.BifrostBlockScannerConfiguration
	logger                zerolog.Logger
	db                    blockscanner.ScannerStorage
	cdc                   *codec.ProtoCodec
	txConfig              client.TxConfig
	rpc                   TendermintRPC
	bridge                thorclient.ThorchainBridge
	pubkeyMgr             pubkeymanager.PubKeyValidator
	solvencyReporter      SolvencyReporter
	globalNetworkFeeQueue chan common.NetworkFee

	// feeCache contains a rolling window of suggested gas fees which are computed as the
	// gas price paid in each observed transaction multiplied by the default GasLimit.
	// Fees are stored at 100x the values on the observed chain due to compensate for the
	// difference in base chain decimals (thorchain:1e8, gaia:1e6).
	// Guards feeCache/lastFee between FetchTxs prefetch updates and solvency/network-fee readers.
	networkFeeMu sync.Mutex
	feeCache     []sdkmath.Uint
	lastFee      sdkmath.Uint
}

// NewCosmosBlockScanner create a new instance of BlockScan
func NewCosmosBlockScanner(
	rpcHost string,
	cfg config.BifrostBlockScannerConfiguration,
	scanStorage blockscanner.ScannerStorage,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	solvencyReporter SolvencyReporter,
	pubkeyMgr pubkeymanager.PubKeyValidator,
) (*CosmosBlockScanner, error) {
	if scanStorage == nil {
		return nil, errors.New("scanStorage is nil")
	}
	if m == nil {
		return nil, errors.New("metrics is nil")
	}

	logger := log.Logger.With().Str("module", "blockscanner").Str("chain", cfg.ChainID.String()).Logger()

	// Bifrost only supports an "RPCHost" in its configuration.
	// We also need to access GRPC for Cosmos chains

	cdc := newGaiaCodec()

	// Registry for encoding txs
	txConfig := tx.NewTxConfig(cdc, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})
	rpcClient, err := rpcclienthttp.New(rpcHost, "/websocket")
	if err != nil {
		logger.Fatal().Err(err).Msg("fail to create tendemrint rpcclient")
	}

	return &CosmosBlockScanner{
		cfg:              cfg,
		logger:           logger,
		db:               scanStorage,
		cdc:              cdc,
		txConfig:         txConfig,
		rpc:              rpcClient,
		feeCache:         make([]sdkmath.Uint, 0),
		lastFee:          sdkmath.NewUint(0),
		bridge:           bridge,
		pubkeyMgr:        pubkeyMgr,
		solvencyReporter: solvencyReporter,
	}, nil
}

// GetHeight returns the height from the latest block minus 1
// NOTE: we must lag by one block due to a race condition fetching the block results
// Since the GetLatestBlockRequests tells what transactions will be in the block at T+1
func (c *CosmosBlockScanner) GetHeight() (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	resultBlock, err := c.rpc.Block(ctx, nil)
	if err != nil {
		return 0, err
	}
	if resultBlock == nil || resultBlock.Block == nil {
		return 0, fmt.Errorf("nil block response from RPC")
	}

	return resultBlock.Block.Header.Height - 1, nil
}

// GetNetworkFee returns current chain network fee according to Bifrost.
func (c *CosmosBlockScanner) GetNetworkFee() (transactionSize, transactionFeeRate uint64) {
	return 1, c.getLastFee().Uint64()
}

func (c *CosmosBlockScanner) getLastFee() sdkmath.Uint {
	// Solvency/network-fee readers can run while FetchTxs prefetch goroutines update lastFee.
	c.networkFeeMu.Lock()
	defer c.networkFeeMu.Unlock()
	if c.lastFee.IsNil() {
		return sdkmath.ZeroUint()
	}
	return c.lastFee
}

func (c *CosmosBlockScanner) setLastFee(fee sdkmath.Uint) {
	// Tests set lastFee directly; use the same lock as FetchTxs updates and solvency reads.
	c.networkFeeMu.Lock()
	defer c.networkFeeMu.Unlock()
	c.lastFee = fee
}

// FetchMemPool returns nothing since we are only concerned about finalized transactions in Cosmos
func (c *CosmosBlockScanner) FetchMemPool(height int64) (types.TxIn, error) {
	return types.TxIn{}, nil
}

// GetBlock returns a Tendermint block as a reference to a ResultBlock for a
// given height. As noted above, this is not necessarily the final state of transactions
// and must be checked again for success by getting the BlockResults in FetchTxs
func (c *CosmosBlockScanner) GetBlock(height int64) (*tmtypes.Block, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	resultBlock, err := c.rpc.Block(ctx, &height)
	if err != nil {
		c.logger.Error().Int64("height", height).Msgf("failed to get block: %v", err)
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	if resultBlock == nil || resultBlock.Block == nil {
		c.logger.Error().Int64("height", height).Msg("received nil block from RPC")
		return nil, fmt.Errorf("received nil block from RPC for height %d", height)
	}

	return resultBlock.Block, nil
}

func (c *CosmosBlockScanner) updateGasCache(tx ctypes.FeeTx) {
	fees := tx.GetFee()

	// only consider transactions that have a single fee
	if len(fees) != 1 {
		return
	}

	// only consider transactions with fee paid in uatom
	coin, err := c.fromCosmosToThorchain(fees[0])
	if err != nil || !coin.Asset.Equals(c.cfg.ChainID.GetGasAsset()) {
		return
	}

	// sanity check to ensure fee is non-zero
	err = coin.Valid()
	if err != nil {
		c.logger.Err(err).Interface("fees", fees).Msg("transaction with zero fee")
		return
	}

	if tx.GetGas() == 0 {
		c.logger.Err(err).Interface("tx", tx).Msg("transaction with zero gas")
		return
	}

	// TODO: This conversion could be broken into a separate function for additional testing.
	// add the fee to our cache
	amount := coin.Amount.Mul(sdkmath.NewUint(GasPriceFactor)) // multiply to handle price < 1
	price := amount.Quo(sdkmath.NewUint(tx.GetGas()))          // divide by gas to get the price
	fee := price.Mul(sdkmath.NewUint(GasLimit))                // tx fee for default gas limit
	fee = fee.Quo(sdkmath.NewUint(GasPriceFactor))             // unroll the multiple

	// gas rate from THORChain decimals to gas rate units
	fee = c.cfg.ChainID.ThorchainToNativeGas(fee)
	if fee.IsZero() {
		fee = cosmos.OneUint()
	}

	// FetchTxs can run in prefetch goroutines, so serialize feeCache appends.
	c.networkFeeMu.Lock()
	defer c.networkFeeMu.Unlock()

	c.feeCache = append(c.feeCache, fee)

	// truncate gas prices older than our max cached transactions
	if len(c.feeCache) > GasCacheTransactions {
		c.feeCache = c.feeCache[(len(c.feeCache) - GasCacheTransactions):]
	}
}

func (c *CosmosBlockScanner) averageFee() sdkmath.Uint {
	// Tests call averageFee directly; use the same lock as FetchTxs prefetch cache writers.
	c.networkFeeMu.Lock()
	defer c.networkFeeMu.Unlock()
	return c.averageFeeLocked()
}

func (c *CosmosBlockScanner) averageFeeLocked() sdkmath.Uint {
	// avoid divide by zero
	if len(c.feeCache) == 0 {
		return sdkmath.NewUint(0)
	}

	// compute mean
	sum := sdkmath.NewUint(0)
	for _, val := range c.feeCache {
		sum = sum.Add(val)
	}
	mean := sum.QuoUint64(uint64(len(c.feeCache)))

	// round the price up to avoid fee noise
	resolution := sdkmath.NewUint(uint64(c.cfg.GasPriceResolution))
	if mean.LTE(resolution) {
		return resolution
	}
	mean = mean.Sub(sdkmath.NewUint(1))
	mean = mean.Quo(resolution)
	mean = mean.Add(sdkmath.NewUint(1))
	mean = mean.Mul(resolution)

	return mean
}

func (c *CosmosBlockScanner) updateGasFees(height int64) error {
	var gasFee sdkmath.Uint
	var networkFee common.NetworkFee
	postNetworkFee, err := func() (bool, error) {
		// Keep averaging and lastFee update atomic.
		c.networkFeeMu.Lock()
		defer c.networkFeeMu.Unlock()

		// post the gas fee over every cache period when we have a full gas cache
		if height%GasUpdatePeriodBlocks != 0 || len(c.feeCache) != GasCacheTransactions {
			return false, nil
		}

		gasFee = c.averageFeeLocked()

		// sanity check the fee is not zero
		if gasFee.IsZero() {
			return false, errors.New("suggested gas fee was zero")
		}

		// skip fee if less than 1 resolution away from the last
		lastFee := c.lastFee
		if lastFee.IsNil() {
			lastFee = sdkmath.ZeroUint()
		}
		feeDelta := sdkmath.MaxUint(lastFee, gasFee).Sub(sdkmath.MinUint(lastFee, gasFee))
		if feeDelta.LTE(sdkmath.NewUint(uint64(c.cfg.GasPriceResolution))) {
			return false, nil
		}

		c.lastFee = gasFee
		// NOTE: We post the fee to the network as the transaction rate (in gas rate units),
		// and set the transaction size 1 to ensure the MaxGas in the generated TxOut
		// contains the correct fee. We previously could not pass the proper size and rate
		// without a deeper change to Thornode, as the rate on Cosmos chains can be less
		// than 1.
		networkFee = common.NetworkFee{
			Chain:           c.cfg.ChainID,
			Height:          height,
			TransactionSize: 1,
			TransactionRate: gasFee.Uint64(),
		}
		return true, nil
	}()
	if err != nil {
		return err
	}
	if postNetworkFee {
		// The fee decision is made while holding networkFeeMu above, but the channel send
		// happens after releasing the lock so blocked consumers cannot stall fee-state updates.
		c.globalNetworkFeeQueue <- networkFee
		c.logger.Info().
			Uint64("fee", gasFee.Uint64()).
			Int64("height", height).
			Msg("sent network fee to THORChain")
	}

	return nil
}

// newGaiaCodec builds the codec used to DECODE Cosmos Hub transactions. It is a
// named function (not constructor inline) so tests exercise the exact registry
// production uses — a test that registers its own types would have hidden the
// missing-registration bug this codec once had.
//
// Note: we register gaia's cosmos sdk types; don't use thorchain's codec as it
// is a smaller subset of codecs.
func newGaiaCodec() *codec.ProtoCodec {
	registry := codectypes.NewInterfaceRegistry()
	authtypes.RegisterInterfaces(registry)
	banktypes.RegisterInterfaces(registry)
	authvesting.RegisterInterfaces(registry)
	stakingtypes.RegisterInterfaces(registry)
	cryptocodec.RegisterInterfaces(registry)
	govv1types.RegisterInterfaces(registry)
	govv1beta1types.RegisterInterfaces(registry)
	paramsproptypes.RegisterInterfaces(registry)
	upgradetypes.RegisterInterfaces(registry)
	distribtypes.RegisterInterfaces(registry)
	// IBC messages are not observed, but must still decode so a MsgSend batched
	// with them (e.g. MsgUpdateClient, MsgAcknowledgement) is not dropped.
	ibcchanneltypes.RegisterInterfaces(registry)
	ibccoretypes.RegisterInterfaces(registry)
	ibclightclient.RegisterInterfaces(registry)
	// Tx decoding is all-or-nothing: a single message type missing from this
	// registry fails decode of the WHOLE transaction ("unable to resolve type
	// URL ...") and every message in it is dropped — including a valid MsgSend
	// deposit to an Asgard vault bundled in the same transaction, which is then
	// unobserved by the entire network (the funds arrive; no node credits them).
	// The registrations below are the message types Cosmos Hub transactions
	// routinely carry that were missing here; registering them lets such
	// transactions decode, while processTxs still observes only MsgSend and
	// ignores the messages themselves — this widens decoding, not the deposit
	// surface.
	// (Residual gap: a nested Any of a type absent from ALL deps — e.g. an
	// 08-wasm light-client update — still fails whole-tx; processTxs logs every
	// unresolved type URL, so the next gap surfaces in logs instead of silence.)
	wasmtypes.RegisterInterfaces(registry)        // CosmWasm is live on the Hub (MsgExecuteContract)
	ibctransfertypes.RegisterInterfaces(registry) // MsgTransfer — outgoing IBC transfers
	authztypes.RegisterInterfaces(registry)       // MsgGrant/MsgExec/MsgRevoke — constant restake-bot traffic
	feegranttypes.RegisterInterfaces(registry)    // MsgGrantAllowance + its allowance Anys
	slashingtypes.RegisterInterfaces(registry)    // MsgUnjail — routine validator traffic
	grouptypes.RegisterInterfaces(registry)       // x/group user transactions
	icacontrollertypes.RegisterInterfaces(registry)
	return codec.NewProtoCodec(registry)
}

// typeURLPattern extracts the offending type URL from the SDK's
// "unable to resolve type URL <url>" decode error.
var typeURLPattern = regexp.MustCompile(`unable to resolve type URL ([^\s:]+)`)

func (c *CosmosBlockScanner) processTxs(height int64, rawTxs []tmtypes.Tx) ([]*types.TxInItem, error) {
	// One log per unresolved type URL per scan pass (see the decode-failure branch).
	unresolvedTypeURLs := map[string]struct{}{}
	// Proto types for Cosmos chains that we are transacting with may not be included in this repo.
	// Therefore, it is necessary to include them in the "proto" directory and register them in
	// the cdc (codec) that is passed below. Registry occurs in the NewCosmosBlockScanner function.
	decoder := tx.DefaultTxDecoder(c.cdc)

	// Fetch the block results so that we can ensure the transaction was successful before processing a TxInItem
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	blockResults, err := c.rpc.BlockResults(ctx, &height)
	if err != nil {
		return nil, fmt.Errorf("unable to get BlockResults: %w", err)
	}
	if blockResults == nil {
		return nil, fmt.Errorf("nil BlockResults response from RPC for height %d", height)
	}

	var txIn []*types.TxInItem
	for i, rawTx := range rawTxs {
		hash := hex.EncodeToString(tmhash.Sum(rawTx))

		if i >= len(blockResults.TxsResults) || blockResults.TxsResults[i] == nil {
			c.logger.Warn().
				Str("txhash", hash).
				Int64("height", height).
				Msg("inbound tx result missing, ignoring...")
			continue
		}

		if blockResults.TxsResults[i].Code != 0 {
			// Transaction failed on-chain but gas was still consumed. If it was
			// an outbound from a vault, observe the gas cost so THORChain
			// accounting stays in sync with the actual on-chain balance.
			if item := c.getTxInFromFailedTransaction(hash, height, rawTx, decoder); item != nil {
				txIn = append(txIn, item)
			}
			continue
		}

		var tx ctypes.Tx
		tx, err = decoder(rawTx)
		if err != nil {
			if strings.Contains(err.Error(), "unable to resolve type URL") {
				// One of the transaction's messages has a type absent from the interface
				// registry (newGaiaCodec), and tx decoding is all-or-nothing: the WHOLE
				// transaction is dropped here, including any valid MsgSend deposit bundled
				// in it. That is exactly how wasm- and IBC-transfer-bearing deposits went
				// unobserved. This log line is the tripwire that catches the next missing
				// registration — it deliberately fires for EVERY unresolved type URL: the
				// previous version logged only errors naming an allowlist of message
				// types, which silently discarded precisely the unknown unknowns this
				// tripwire exists to catch. Deduped per type URL per scan pass so a busy
				// block cannot spam the log.
				typeURL := "unknown"
				if m := typeURLPattern.FindStringSubmatch(err.Error()); len(m) == 2 {
					typeURL = m[1]
				}
				if _, seen := unresolvedTypeURLs[typeURL]; !seen {
					unresolvedTypeURLs[typeURL] = struct{}{}
					c.logger.Err(err).
						Str("type_url", typeURL).
						Str("tx_hash", hash).
						Msg("dropping whole tx: message type missing from decode registry")
				}
			}
			continue
		}

		feeTx, _ := tx.(ctypes.FeeTx)
		fees := feeTx.GetFee()
		mem, _ := tx.(ctypes.TxWithMemo)
		memo := mem.GetMemo()

		// Filter out transactions with memos that exceed MaxMemoSize
		// This prevents spam from reaching the observer and matches behavior of other chains
		if len([]byte(memo)) > constants.MaxMemoSize {
			c.logger.Debug().
				Str("txhash", hash).
				Int64("height", height).
				Msg("memo too long, skipping transaction")
			continue
		}

		c.updateGasCache(feeTx)

		for _, message := range tx.GetMsgs() {
			// If there are more than one TxIn item per transaction hash,
			// thornode will fail to process any after the first.
			// Therefore, limit to 1 MsgSend per transaction.
			msg, ok := message.(*banktypes.MsgSend)
			if !ok {
				continue
			}

			// Convert cosmos coins to thorchain coins (taking into account asset decimal precision)
			coins := common.Coins{}
			for _, coin := range msg.Amount {
				var cCoin common.Coin
				cCoin, err = c.fromCosmosToThorchain(coin)
				if err != nil {
					c.logger.Debug().Err(err).
						Interface("coins", coin).
						Msg("unable to convert coin, not whitelisted. skipping...")
					continue
				}
				coins = append(coins, cCoin)
			}

			// Ignore the tx when no coins exist
			if coins.IsEmpty() {
				continue
			}

			// filter gas asset transactions below dust threshold
			if len(coins) == 1 && coins[0].Asset.Equals(c.cfg.ChainID.GetGasAsset()) {
				if coins[0].Amount.LT(c.cfg.ChainID.DustThreshold()) {
					c.logger.Debug().Str("tx_hash", hash).Msg("dropping tx below dust threshold")
					continue
				}
			}

			// Convert cosmos gas to thorchain coins (taking into account gas asset decimal precision)
			gasFees := common.Gas{}
			for _, fee := range fees {
				var cCoin common.Coin
				cCoin, err = c.fromCosmosToThorchain(fee)
				if err != nil {
					c.logger.Debug().Err(err).Interface("fees", fees).
						Msg("unable to convert coin, not whitelisted. skipping...")
					continue
				}
				gasFees = append(gasFees, cCoin)
			}
			// THORChain only supports gas paid in ATOM, if gas is paid in another asset
			// then fake gas as `0.000001 ATOM`, the fee is not used but cannot be empty
			if gasFees.IsEmpty() {
				gasFees = append(
					gasFees, common.NewCoin(
						c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(1),
					),
				)
			}

			txIn = append(txIn, &types.TxInItem{
				Tx:          hash,
				BlockHeight: height,
				Memo:        memo,
				Sender:      msg.FromAddress,
				To:          msg.ToAddress,
				Coins:       coins,
				Gas:         gasFees,
			})

			// found a MsgSend
			break
		}
	}

	return txIn, nil
}

// getTxInFromFailedTransaction checks whether a failed on-chain transaction was an
// outbound from a vault. If so, it returns a gas-only TxInItem so THORChain can account
// for the gas consumed by the failed transaction and keep vault balances in sync.
func (c *CosmosBlockScanner) getTxInFromFailedTransaction(
	hash string,
	height int64,
	rawTx tmtypes.Tx,
	decoder ctypes.TxDecoder,
) *types.TxInItem {
	if c.pubkeyMgr == nil {
		return nil
	}

	tx, err := decoder(rawTx)
	if err != nil {
		return nil
	}

	// Extract the sender from the first MsgSend in the transaction.
	var fromAddress, toAddress string
	for _, message := range tx.GetMsgs() {
		msg, ok := message.(*banktypes.MsgSend)
		if !ok {
			continue
		}
		fromAddress = msg.FromAddress
		toAddress = msg.ToAddress
		break
	}
	if fromAddress == "" {
		return nil
	}

	// Only observe if the sender is a vault address.
	ok, cif := c.pubkeyMgr.IsValidPoolAddress(fromAddress, c.cfg.ChainID)
	if !ok || cif.IsEmpty() {
		return nil
	}

	// Use the fee paid in the transaction as the gas cost.
	feeTx, ok := tx.(ctypes.FeeTx)
	if !ok {
		return nil
	}
	gasFees := common.Gas{}
	for _, fee := range feeTx.GetFee() {
		cCoin, err := c.fromCosmosToThorchain(fee)
		if err != nil {
			continue
		}
		gasFees = append(gasFees, cCoin)
	}
	if gasFees.IsEmpty() {
		c.logger.Error().
			Str("txhash", hash).
			Int64("height", height).
			Msg("failed vault outbound has no gas fee in gas asset")
		return nil
	}

	c.logger.Info().
		Str("txhash", hash).
		Int64("height", height).
		Str("from", fromAddress).
		Msg("observed failed outbound from vault")

	return &types.TxInItem{
		Tx:          hash,
		BlockHeight: height,
		Memo:        memo.NewOutboundMemo(common.TxID(strings.ToUpper(hash))).String(),
		Sender:      fromAddress,
		To:          toAddress,
		Coins:       common.NewCoins(common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(1))),
		Gas:         gasFees,
	}
}

func (c *CosmosBlockScanner) FetchTxs(height, chainHeight int64) (types.TxIn, error) {
	block, err := c.GetBlock(height)
	if err != nil {
		return types.TxIn{}, err
	}

	// The block scanner should never receive a nil block.
	if block == nil {
		return types.TxIn{}, fmt.Errorf("received nil block for height %d", height)
	}

	txs, err := c.processTxs(height, block.Data.Txs)
	if err != nil {
		return types.TxIn{}, err
	}

	txIn := types.TxIn{
		Chain:    c.cfg.ChainID,
		TxArray:  txs,
		Filtered: false,
		MemPool:  false,
	}

	// skip reporting network fee and solvency if block more than flexibility blocks from tip
	if chainHeight-height > c.cfg.ObservationFlexibilityBlocks {
		return txIn, nil
	}

	err = c.updateGasFees(height)
	if err != nil {
		c.logger.Err(err).Int64("height", height).Msg("unable to update network fee")
	}

	if c.solvencyReporter != nil {
		if err = c.solvencyReporter(height); err != nil {
			c.logger.Err(err).Msg("fail to send solvency to THORChain")
		}
	}

	return txIn, nil
}

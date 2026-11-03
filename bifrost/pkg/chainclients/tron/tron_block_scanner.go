package tron

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	stdatomic "sync/atomic"

	"cosmossdk.io/math"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
	"gitlab.com/thorchain/thornode/v3/config"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

var updateGasInterval int64 = 30

// batchReceiptMinTxs is the number of vault-involved transactions in a block
// from which their fees are resolved with one gettransactioninfobyblocknum
// call instead of one gettransactioninfobyid call each. The batch response
// carries the info of every transaction in the block (~100-400 KB on mainnet,
// ~0.25s on TronGrid vs ~0.12s for a ~200 B single lookup), so below this it
// costs more than the individual calls it replaces. Mainnet sample over 24h:
// ~7% of blocks carry a vault transaction and none carry more than two, so the
// batch only engages for unusual bursts (e.g. many transfers sent to a vault
// in one block).
const batchReceiptMinTxs = 3

type ReportSolvency func(int64) error

type RefBlock struct {
	Timestamp int64
	Height    int64
	Id        string
}

type TronBlockScanner struct {
	config      config.BifrostBlockScannerConfiguration
	logger      zerolog.Logger
	bridge      thorclient.ThorchainBridge
	pubkeyMgr   pubkeymanager.PubKeyValidator
	api         *api.TronApi
	whitelist   map[string]tokenlist.ERC20Token
	abi         abi.ABI
	refBlocksMu sync.RWMutex
	refBlocks   []RefBlock
	refAddress  string // needed for energy estimation via api
	// Serializes FetchTxs/prefetch network-fee compare and currentFee update.
	networkFeeMu          sync.Mutex
	currentFee            stdatomic.Uint64
	reportSolvency        ReportSolvency
	globalNetworkFeeQueue chan common.NetworkFee
}

func NewTronBlockScanner(
	cfg config.BifrostChainConfiguration,
	bridge thorclient.ThorchainBridge,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	reportSolvency ReportSolvency,
) (*TronBlockScanner, error) {
	logger := log.Logger.With().
		Str("module", "blockscanner").
		Str("chain", cfg.ChainID.String()).
		Logger()

	// load whitelisted tokens
	tokens := tokenlist.GetEVMTokenList(cfg.ChainID).Tokens

	whitelist := map[string]tokenlist.ERC20Token{}
	for _, token := range tokens {
		for _, address := range cfg.BlockScanner.WhitelistTokens {
			if strings.EqualFold(address, token.Address) {
				whitelist[address] = token
			}
		}
	}

	refAddress := cfg.BlockScanner.ReferenceAddress
	if len(whitelist) > 0 && refAddress == "" {
		return nil, fmt.Errorf("reference address is empty")
	}

	scanner := TronBlockScanner{
		config:         cfg.BlockScanner,
		logger:         logger,
		whitelist:      whitelist,
		api:            api.NewTronApi(cfg.APIHost, cfg.BlockScanner.HTTPRequestTimeout),
		bridge:         bridge,
		pubkeyMgr:      pubkeyMgr,
		refBlocks:      []RefBlock{},
		refAddress:     refAddress,
		reportSolvency: reportSolvency,
	}

	var err error

	scanner.abi, err = abi.JSON(bytes.NewReader(trc20ContractABI))
	if err != nil {
		logger.Err(err).Msg("failed to parse ABI")
		return nil, err
	}

	return &scanner, nil
}

func (s *TronBlockScanner) GetHeight() (int64, error) {
	block, err := s.api.GetLatestBlock()
	if err != nil {
		s.logger.Err(err).Msg("failed to get latest block")
		return 0, err
	}

	height := block.Header.RawData.Number - ConfirmationBlocks
	if height < 0 {
		height = 0
	}

	return height, nil
}

func (s *TronBlockScanner) FetchMemPool(_ int64) (types.TxIn, error) {
	return types.TxIn{Chain: common.TRONChain}, nil
}

func (s *TronBlockScanner) FetchTxs(
	fetchHeight, chainHeight int64,
) (types.TxIn, error) {
	block, err := s.api.GetBlock(fetchHeight)
	if err != nil {
		s.logger.Err(err).Msg("")
		return types.TxIn{}, err
	}

	txs, err := s.processTxs(block)
	if err != nil {
		s.logger.Err(err).Msg("")
		return types.TxIn{}, err
	}

	txIn := types.TxIn{
		Chain:    s.config.ChainID,
		TxArray:  txs,
		Filtered: false,
		MemPool:  false,
	}

	if chainHeight-fetchHeight > s.config.ObservationFlexibilityBlocks {
		return txIn, nil
	}

	s.updateFees(fetchHeight)

	if fetchHeight%refBlockInterval != 0 {
		return txIn, nil
	}

	s.addRefBlock(RefBlock{
		Timestamp: block.Header.RawData.Timestamp,
		Height:    block.Header.RawData.Number,
		Id:        block.BlockId,
	})

	return txIn, nil
}

// ReportSolvency runs after the shared scanner has recomputed health. TRON
// retains its existing tip-height reporting cadence while scanning near the tip.
func (s *TronBlockScanner) ReportSolvency(fetchHeight, chainHeight int64) error {
	if chainHeight-fetchHeight > s.config.ObservationFlexibilityBlocks || s.reportSolvency == nil {
		return nil
	}
	return s.reportSolvency(chainHeight)
}

func (s *TronBlockScanner) addRefBlock(refBlock RefBlock) {
	s.refBlocksMu.Lock()
	defer s.refBlocksMu.Unlock()

	for i := range s.refBlocks {
		if s.refBlocks[i].Height == refBlock.Height {
			s.refBlocks[i] = refBlock
			return
		}
	}

	s.refBlocks = append(s.refBlocks, refBlock)
	s.sortAndTrimRefBlocks()
}

func (s *TronBlockScanner) sortAndTrimRefBlocks() {
	sort.Slice(s.refBlocks, func(i, j int) bool {
		if s.refBlocks[i].Height == s.refBlocks[j].Height {
			return s.refBlocks[i].Id < s.refBlocks[j].Id
		}
		return s.refBlocks[i].Height < s.refBlocks[j].Height
	})

	if len(s.refBlocks) > refBlocksMax {
		s.refBlocks = s.refBlocks[len(s.refBlocks)-refBlocksMax:]
	}
}

func (s *TronBlockScanner) refBlockSnapshot() []RefBlock {
	s.refBlocksMu.RLock()
	defer s.refBlocksMu.RUnlock()

	refBlocks := make([]RefBlock, len(s.refBlocks))
	copy(refBlocks, s.refBlocks)
	return refBlocks
}

func (s *TronBlockScanner) GetNetworkFee() (uint64, uint64) {
	return 1, s.currentFee.Load()
}

// private
// ----------------------------------------------------------------------------

func (s *TronBlockScanner) processTxs(
	block api.Block,
) ([]*types.TxInItem, error) {
	var txInItems []*types.TxInItem

	height := block.Header.RawData.Number

	contracts := map[string]struct{}{}

	// Every item collected below needs its fee from the transaction info.
	// Filtering only reads the block payload, so collect the items first and
	// resolve the fees afterwards, once their number is known.
	for _, rawTx := range block.Transactions {
		logger := s.logger.With().Str("hash", rawTx.TxId).Logger()

		// only accept direct 1:1 transfers
		if len(rawTx.RawData.Contract) != 1 {
			logger.Warn().
				Int("len", len(rawTx.RawData.Contract)).
				Msg("amount != 1")
			continue
		}

		// we need tx return code
		if len(rawTx.Ret) == 0 {
			logger.Warn().Msg("no return code found")
			continue
		}

		// check for failed outbound transactions from vaults
		if rawTx.Ret[0].ContractRet != "SUCCESS" {
			// process failed transactions that originated from a vault
			txInItem := s.getTxInFromFailedTransaction(&rawTx, height)
			if txInItem != nil {
				txInItems = append(txInItems, txInItem)
			}
			continue
		}

		raw := rawTx.RawData.Contract[0]

		var coins common.Coins
		var dest string
		var err error

		switch raw.Type {
		case "TransferContract":
			dest, err = api.ConvertAddress(raw.Parameter.Value.ToAddress)
			if err != nil {
				logger.Err(err).Msg("failed to convert destination address")
			}

			// 1e6 -> 1e8
			amount := math.NewUint(uint64(raw.Parameter.Value.Amount)).Mul(math.NewUint(100))
			coins = common.Coins{{
				Asset:    common.TRXAsset,
				Amount:   amount,
				Decimals: 6,
			}}

		case "TriggerSmartContract":
			var contractAddr string
			contractAddr, err = api.ConvertAddress(raw.Parameter.Value.ContractAddress)
			if err != nil {
				logger.Err(err).Msg("failed to convert contract address")
				continue
			}

			// skip unknown contracts
			token, ok := s.whitelist[contractAddr]
			if !ok {
				continue
			}

			// check contract penalty factor later
			contracts[contractAddr] = struct{}{}

			var method string
			var inputs map[string]interface{}
			method, inputs, err = s.decodeTRC20Input(raw.Parameter.Value.Data)
			if err != nil {
				logger.Err(err).Msg("failed to get inputs")
				continue
			}

			if method != "transfer" {
				continue
			}

			to, ok := inputs["_to"]
			if !ok {
				logger.Error().Msg("no destination address found")
				continue
			}

			dest, err = api.ConvertAddress(fmt.Sprintf("%v", to))
			if err != nil {
				logger.Err(err).Msg("failed to convert address")
				continue
			}

			value, ok := inputs["_value"]
			if !ok {
				logger.Error().Msg("no amount found")
				continue
			}

			amount := new(big.Int)
			amount, ok = amount.SetString(fmt.Sprintf("%v", value), 10)
			if !ok {
				logger.Error().Msg("failed to convert amount")
				continue
			}

			amount, err = common.ConvertDecimals(
				amount, token.Decimals, common.THORChainDecimals,
			)
			if err != nil {
				logger.Err(err).Msg("failed to convert amount to decimals")
				continue
			}

			coins = common.Coins{{
				Asset:    token.Asset(common.TRONChain),
				Amount:   math.NewUintFromBigInt(amount),
				Decimals: int64(token.Decimals),
			}}

		default:
			continue
		}

		// filter gas asset transactions below dust threshold
		if len(coins) == 1 && coins[0].Asset.Equals(s.config.ChainID.GetGasAsset()) {
			if coins[0].Amount.LT(s.config.ChainID.DustThreshold()) {
				logger.Debug().Msg("dropping tx below dust threshold")
				continue
			}
		}

		memo, err := hex.DecodeString(rawTx.RawData.Data)
		if err != nil {
			logger.Warn().Msg("Decode data failed")
			continue
		}

		sender, err := api.ConvertAddress(raw.Parameter.Value.OwnerAddress)
		if err != nil {
			logger.Err(err).Msg("failed to convert sender address")
			continue
		}

		// Receipts only supply gas for observations involving known vaults. Use
		// the observer's address predicate, including retiring/inactive vaults.
		isFromVault, _ := s.pubkeyMgr.IsValidPoolAddress(sender, s.config.ChainID)
		isToVault, _ := s.pubkeyMgr.IsValidPoolAddress(dest, s.config.ChainID)
		if !isFromVault && !isToVault {
			continue
		}

		txInItems = append(txInItems, &types.TxInItem{
			Tx:          rawTx.TxId,
			BlockHeight: height,
			Memo:        strings.TrimSpace(string(memo)),
			Sender:      sender,
			To:          dest,
			Coins:       coins,
		})
	}

	if err := s.setReceiptGas(txInItems, height); err != nil {
		return nil, err
	}

	return txInItems, nil
}

// setReceiptGas sets the gas of every item from its transaction info, batching
// the lookups into one call when the block has at least batchReceiptMinTxs
// items. Any unavailable or mismatched receipt fails the whole block.
func (s *TronBlockScanner) setReceiptGas(txInItems []*types.TxInItem, height int64) error {
	if len(txInItems) == 0 {
		return nil
	}

	txInfos := s.api.NewTxInfoResolver(height, len(txInItems) >= batchReceiptMinTxs)
	for _, item := range txInItems {
		info, err := s.getBlockReceipt(txInfos, item.Tx, height)
		if err != nil {
			return err
		}

		gasAmount := math.NewUint(info.Fee).Mul(math.NewUint(100))
		if gasAmount.IsZero() {
			gasAmount = math.NewUint(1)
		}
		item.Gas = common.Gas{{
			Amount:   gasAmount,
			Asset:    common.TRXAsset,
			Decimals: 6,
		}}
	}

	return nil
}

// getBlockReceipt requires inclusion in the block being scanned. The shared API
// still permits empty lookups for transactions not yet included in a block.
func (s *TronBlockScanner) getBlockReceipt(txInfos *api.TxInfoResolver, txID string, height int64) (api.TransactionInfo, error) {
	info, err := txInfos.Get(txID)
	if err != nil {
		return api.TransactionInfo{}, fmt.Errorf("%w: transaction %s receipt: %w", btypes.ErrUnavailableBlock, txID, err)
	}
	if txID == "" || !strings.EqualFold(info.ID, txID) || info.BlockNumber != height || height <= 0 {
		return api.TransactionInfo{}, fmt.Errorf("%w: transaction %s at height %d has receipt id %q at height %d", btypes.ErrUnavailableBlock, txID, height, info.ID, info.BlockNumber)
	}
	return info, nil
}

// getTxInFromFailedTransaction processes a failed transaction and returns a TxInItem if it
// originated from a vault. This allows the vault accounting to be updated for gas spent on
// failed outbound transactions. The caller sets the item's gas from its receipt.
func (s *TronBlockScanner) getTxInFromFailedTransaction(
	rawTx *api.Transaction,
	height int64,
) *types.TxInItem {
	raw := rawTx.RawData.Contract[0]

	// get sender address
	sender, err := api.ConvertAddress(raw.Parameter.Value.OwnerAddress)
	if err != nil {
		s.logger.Err(err).Msg("failed to convert sender address")
		return nil
	}

	// check if the sender is a vault address
	ok, cif := s.pubkeyMgr.IsValidPoolAddress(sender, s.config.ChainID)
	if !ok || cif.IsEmpty() {
		return nil
	}

	// get destination address
	var dest string
	switch raw.Type {
	case "TransferContract":
		dest, err = api.ConvertAddress(raw.Parameter.Value.ToAddress)
		if err != nil {
			s.logger.Err(err).Msg("failed to convert destination address")
			return nil
		}
	case "TriggerSmartContract":
		dest, err = api.ConvertAddress(raw.Parameter.Value.ContractAddress)
		if err != nil {
			s.logger.Err(err).Msg("failed to convert contract address")
			return nil
		}
	default:
		return nil
	}

	return &types.TxInItem{
		Tx:          rawTx.TxId,
		BlockHeight: height,
		// memo is the OUT:<txid> with self-reference to indicate failed outbound
		Memo:   memo.NewOutboundMemo(common.TxID(strings.ToUpper(rawTx.TxId))).String(),
		Sender: sender,
		To:     dest,
		Coins:  common.NewCoins(common.NewCoin(common.TRXAsset, math.NewUint(1))),
	}
}

func (s *TronBlockScanner) decodeTRC20Input(
	data string,
) (string, map[string]interface{}, error) {
	bz, err := hex.DecodeString(data)
	if err != nil {
		s.logger.Err(err).Msg("failed to decode raw input data")
		return "", nil, err
	}

	methodSigData := bz[:4]
	inputsHexData := bz[4:]

	method, err := s.abi.MethodById(methodSigData)
	if err != nil {
		s.logger.Err(err).Msg("failed to lookup method")
		return "", nil, err
	}

	inputs := make(map[string]interface{})
	err = method.Inputs.UnpackIntoMap(inputs, inputsHexData)
	if err != nil {
		s.logger.Err(err).Msg("failed to unpack inputs")
		return "", nil, err
	}

	return method.Name, inputs, nil
}

func (s *TronBlockScanner) updateFees(height int64) {
	if height%updateGasInterval != 0 {
		return
	}

	params, err := s.api.GetChainParameters()
	if err != nil {
		s.logger.Err(err).Msg("failed get chain parameters")
	}

	var fee, bandwidth, energy int64

	// bandwidth calculation:
	// len(raw_data) + protobuf overhead + max_result_size + signature length
	// len(raw_data) + 3 bytes + 64 bytes + 67 bytes
	if len(s.whitelist) == 0 {
		// only TRX transfers
		// => 150 + 3 + 64 + 67 = 284
		bandwidth = 284 * params.BandwidthFee
	} else {
		// hex data is longer and penalty factor is applied
		// => 211 + 3 + 64 + 67 = 361
		bandwidth = 361 * params.BandwidthFee

		maxEnergy, err := s.getMaxEnergy()
		if err != nil || maxEnergy <= 0 {
			s.logger.Err(err).Msg("failed to get max energy")
			return
		}

		energy = maxEnergy * params.EnergyFee
	}

	// add 1.1 TRX in case the new account needs to be activated:
	// https://developers.tron.network/docs/account#account-activation
	fee = energy + bandwidth + params.MemoFee + 1_100_000

	if fee <= 0 {
		s.logger.Error().Msg("fee is zero")
		return
	}

	feeRate := uint64(fee)

	// FetchTxs can run in prefetch goroutines; serialize fee comparison and currentFee.
	networkFee, postNetworkFee := func() (common.NetworkFee, bool) {
		s.networkFeeMu.Lock()
		defer s.networkFeeMu.Unlock()

		// skip sending the network fee if it did not change
		if feeRate == s.currentFee.Load() {
			return common.NetworkFee{}, false
		}

		s.currentFee.Store(feeRate)
		return common.NetworkFee{
			Chain:           s.config.ChainID,
			Height:          height,
			TransactionSize: 1,
			TransactionRate: feeRate,
		}, true
	}()
	if !postNetworkFee {
		return
	}

	// The fee decision is made while holding networkFeeMu above, but the channel send
	// happens after releasing the lock so blocked consumers cannot stall fee-state updates.
	s.globalNetworkFeeQueue <- networkFee
	s.logger.Info().
		Int64("height", height).
		Int64("bandwidth", bandwidth).
		Int64("energy", energy).
		Int64("memo_fee", params.MemoFee).
		Int64("energy_fee", params.EnergyFee).
		Int64("bandwidth_fee", params.BandwidthFee).
		Int64("total_fee", fee).
		Msg("updated network fee")
}

func (s *TronBlockScanner) getMaxEnergy() (int64, error) {
	// get max energy usage of all whitelisted tokens
	maxEnergy := int64(0)

	hexAddress, err := api.ConvertAddress(s.refAddress)
	if err != nil {
		s.logger.Err(err).Msg("failed to convert address")
		return 0, err
	}

	input := fmt.Sprintf("%024x%s%064x", 0, hexAddress[2:], 1)

	for _, token := range s.whitelist {
		energy, err := s.api.EstimateEnergy(
			s.refAddress,
			token.Address,
			"transfer(address,uint256)",
			input,
		)
		if err != nil {
			s.logger.Err(err).Msg("failed to estimate energy")
			return 0, err
		}

		// it takes almost twice the energy to send a token to a wallet
		// for the first time (x2 for safety)
		maxEnergy = max(maxEnergy, energy*2)
	}

	return maxEnergy, nil
}

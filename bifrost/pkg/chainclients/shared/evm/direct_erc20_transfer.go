package evm

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const erc20TransferEvent = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

var erc20TransferSelector = []byte{0xa9, 0x05, 0x9c, 0xbb}

// PoolAddressProvider is the subset of pubkeymanager.PubKeyValidator needed to
// match active pool addresses.
type PoolAddressProvider interface {
	IsActivePoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo)
}

// DirectERC20TransferTokenResolver resolves token metadata for direct ERC20
// transfers. Generic EVM chains and legacy ETH use different token metadata
// sources, so scanners provide the source that matches their normal router path.
type DirectERC20TransferTokenResolver interface {
	GetAssetFromTokenAddress(token string) (common.Asset, error)
	GetTokenDecimalsForTHORChain(token string) int64
	ConvertAmount(token string, amt *big.Int) cosmos.Uint
}

// DirectERC20TransferLogOptions contains the chain-specific callbacks needed to
// turn direct ERC20 Transfer logs into THORChain coins.
type DirectERC20TransferLogOptions struct {
	Chain               common.Chain
	MaxLogs             int64
	PoolAddressProvider PoolAddressProvider
	TokenResolver       DirectERC20TransferTokenResolver
	Logger              zerolog.Logger
}

// DirectERC20TransferTxInOptions contains the scanner-specific dependencies
// needed to observe direct ERC20 transfers as inbound TxInItems.
type DirectERC20TransferTxInOptions struct {
	Chain                 common.Chain
	Signer                etypes.Signer
	Sender                *ecommon.Address
	MaxContractTxLogs     int64
	L1Fee                 *big.Int
	PoolAddressProvider   PoolAddressProvider
	TokenResolver         DirectERC20TransferTokenResolver
	Logger                zerolog.Logger
	SenderErrorMessage    string
	ParseLogsErrorMessage string
}

// IsActivePoolAddress reports whether addr is an active vault address for chain.
func IsActivePoolAddress(chain common.Chain, poolAddressProvider PoolAddressProvider, addr string) bool {
	if poolAddressProvider == nil {
		return false
	}
	ok, _ := poolAddressProvider.IsActivePoolAddress(addr, chain)
	return ok
}

// ParseDirectERC20TransferLogs parses direct ERC20 transfers to active vaults.
// Direct transfers are not router deposits, so they have no THORChain memo. The
// caller must pass the transaction target as tokenAddress, and this parser only
// accepts Transfer logs emitted by that exact token contract. This prevents
// router or aggregator deposits from being reinterpreted through their internal
// ERC20 Transfer logs.
func ParseDirectERC20TransferLogs(
	logs []*etypes.Log,
	txInItem *stypes.TxInItem,
	tokenAddress ecommon.Address,
	sender ecommon.Address,
	opts DirectERC20TransferLogOptions,
) (bool, error) {
	if txInItem == nil {
		return false, errors.New("tx in item is nil")
	}
	if opts.TokenResolver == nil {
		return false, errors.New("direct ERC20 transfer token resolver is nil")
	}
	if len(logs) == 0 {
		opts.Logger.Info().Msg("tx logs are empty return nil")
		return false, nil
	} else if int(opts.MaxLogs) > 0 && len(logs) > int(opts.MaxLogs) {
		opts.Logger.Info().Msgf("tx logs are too many, ignore")
		return false, nil
	}

	observed := false
	for _, item := range logs {
		// Direct ERC20 transfers are sent to the token contract itself. Ignore
		// internal Transfer logs from router, aggregator, or other contract calls.
		if item == nil || !strings.EqualFold(item.Address.String(), tokenAddress.String()) {
			continue
		}
		if len(item.Topics) != 3 || item.Topics[0].String() != erc20TransferEvent {
			continue
		}
		if len(item.Data) != ecommon.HashLength {
			opts.Logger.Info().Str("token", item.Address.String()).Msg("direct ERC20 transfer event has invalid amount data")
			continue
		}

		from := ecommon.BytesToAddress(item.Topics[1].Bytes())
		// Only observe simple transfer(...) flows funded by the tx signer.
		// transferFrom(...) can debit a different token owner, so it is ignored.
		if !strings.EqualFold(from.String(), sender.String()) {
			continue
		}

		to := ecommon.BytesToAddress(item.Topics[2].Bytes())
		// The recipient must be an active vault. This keeps unrelated token
		// transfers from being interpreted as THORChain inbounds.
		if !IsActivePoolAddress(opts.Chain, opts.PoolAddressProvider, to.String()) {
			continue
		}

		amount := new(big.Int).SetBytes(item.Data)
		if amount.Sign() == 0 {
			opts.Logger.Info().Str("token", item.Address.String()).Msg("direct ERC20 transfer amount is 0, ignore")
			continue
		}

		if len(txInItem.To) > 0 && !strings.EqualFold(txInItem.To, to.String()) {
			return false, fmt.Errorf("multiple direct ERC20 transfer events in the same transaction have different to addresses")
		}
		if observed {
			opts.Logger.Info().Str("token", item.Address.String()).Msg("duplicate direct ERC20 transfer event, ignore")
			continue
		}

		// Reuse the scanner's normal token resolver so direct transfers follow
		// the same asset eligibility and amount conversion path as router deposits.
		asset, err := opts.TokenResolver.GetAssetFromTokenAddress(item.Address.String())
		if err != nil {
			opts.Logger.Err(err).Str("token_address", item.Address.String()).Msg("failed to get asset from token address")
			continue
		}
		if asset.IsEmpty() {
			continue
		}

		txInItem.To = to.String()
		decimals := opts.TokenResolver.GetTokenDecimalsForTHORChain(item.Address.String())
		txInItem.Coins = append(txInItem.Coins,
			common.NewCoin(asset, opts.TokenResolver.ConvertAmount(item.Address.String(), amount)).WithDecimals(decimals))
		observed = true
	}

	return observed, nil
}

// GetTxInFromDirectERC20Transfer observes a direct ERC20 transfer transaction to
// an active vault, if the transfer is eligible and economically non-zero.
func GetTxInFromDirectERC20Transfer(
	tx *etypes.Transaction,
	receipt *etypes.Receipt,
	opts DirectERC20TransferTxInOptions,
) (*stypes.TxInItem, error) {
	if tx == nil || tx.To() == nil || receipt == nil || receipt.Status != 1 {
		return nil, nil
	}
	if !HasERC20TransferSelector(tx) {
		return nil, nil
	}

	// Start with an empty memo. If THORNode cannot process the memoless inbound,
	// its normal refund path can handle the observed token transfer.
	txInItem := &stypes.TxInItem{
		Tx: tx.Hash().Hex()[2:],
	}
	// The recovered signer is the refund sender for direct transfer(...) calls.
	var sender ecommon.Address
	if opts.Sender != nil {
		sender = *opts.Sender
	} else {
		if opts.Signer == nil {
			return nil, errors.New("signer is nil")
		}
		var err error
		sender, err = etypes.Sender(opts.Signer, tx)
		if err != nil {
			if opts.SenderErrorMessage == "" {
				opts.SenderErrorMessage = "failed to get sender"
			}
			return nil, fmt.Errorf("%s: %w", opts.SenderErrorMessage, err)
		}
	}
	txInItem.Sender = strings.ToLower(sender.String())

	observed, err := ParseDirectERC20TransferLogs(receipt.Logs, txInItem, *tx.To(), sender, DirectERC20TransferLogOptions{
		Chain:               opts.Chain,
		MaxLogs:             opts.MaxContractTxLogs,
		PoolAddressProvider: opts.PoolAddressProvider,
		TokenResolver:       opts.TokenResolver,
		Logger:              opts.Logger,
	})
	if err != nil {
		if opts.ParseLogsErrorMessage == "" {
			opts.ParseLogsErrorMessage = "failed to parse direct ERC20 transfer logs"
		}
		return nil, fmt.Errorf("%s, err: %w", opts.ParseLogsErrorMessage, err)
	}
	if !observed || txInItem.Coins.IsEmpty() {
		return nil, nil
	}

	// Preserve gas observation so an accepted direct transfer has the same fee
	// accounting data as other EVM inbound observations.
	txGasPrice := receipt.EffectiveGasPrice
	if txGasPrice == nil {
		txGasPrice = big.NewInt(0)
	}
	txInItem.Gas = common.MakeEVMGas(opts.Chain, txGasPrice, receipt.GasUsed, opts.L1Fee)

	opts.Logger.Info().
		Str("tx", txInItem.Tx).
		Str("sender", txInItem.Sender).
		Str("vault", txInItem.To).
		Str("coins", txInItem.Coins.String()).
		Msg("detected direct ERC20 transfer to vault")

	return txInItem, nil
}

package ethereum

import (
	"context"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
)

// fetchBlockReceipts fetches the receipt of every transaction in the block
// with a single eth_getBlockReceipts call and returns them keyed by
// transaction hash, replacing one eth_getTransactionReceipt round trip per
// transaction. This is the same strategy the evm client uses for chains
// registered in its getTxInOptimized switch.
//
// It returns nil when the block carries no transactions or when the call
// fails — most notably on nodes whose software or version does not serve
// eth_getBlockReceipts — in which case the caller falls back to individual
// per-transaction receipt lookups, preserving the previous behavior. The
// result is read-only after return and safe to share across the extractTxs
// worker goroutines.
func fetchBlockReceipts(
	ctx context.Context,
	client *rpc.Client,
	logger zerolog.Logger,
	block *etypes.Block,
) map[ecommon.Hash]*etypes.Receipt {
	// Say which of these it was: all three return nil and send the caller down
	// the per-transaction path, and only the empty block is ordinary.
	switch {
	case block == nil:
		logger.Error().Msg("no block to fetch receipts for, falling back to per-transaction receipt lookups")
		return nil
	case client == nil:
		logger.Error().
			Uint64("block", block.NumberU64()).
			Msg("no rpc client to fetch block receipts with, falling back to per-transaction receipt lookups")
		return nil
	case len(block.Transactions()) == 0:
		// Not a fallback: an empty block has no receipts to look up either way.
		logger.Debug().
			Uint64("block", block.NumberU64()).
			Msg("block carries no transactions, skipping block receipts fetch")
		return nil
	}

	// Query by block hash rather than number so a reorg between fetching the
	// block and its receipts cannot hand back another block's receipts; a
	// provider that rejects the hash form takes the per-transaction fallback.
	blockHash := block.Hash()
	var receipts []*etypes.Receipt
	if err := client.CallContext(ctx, &receipts, "eth_getBlockReceipts", blockHash.Hex()); err != nil {
		logger.Warn().Err(err).
			Uint64("block", block.NumberU64()).
			Msg("failed to fetch block receipts, falling back to per-transaction receipt lookups")
		return nil
	}

	// Skipped receipts are absent from the map, so their transactions fall
	// back to individual lookups.
	byHash := make(map[ecommon.Hash]*etypes.Receipt, len(receipts))
	for _, receipt := range receipts {
		switch {
		case receipt == nil:
			continue
		case receipt.BlockHash != blockHash:
			logger.Warn().
				Uint64("block", block.NumberU64()).
				Str("tx", receipt.TxHash.Hex()).
				Str("receipt_block_hash", receipt.BlockHash.Hex()).
				Msg("dropping block receipt from a different block")
			continue
		case receipt.EffectiveGasPrice == nil:
			// Gas accounting needs the effective price; a nil one would panic
			// in MakeEVMGas.
			logger.Warn().
				Uint64("block", block.NumberU64()).
				Str("tx", receipt.TxHash.Hex()).
				Msg("dropping block receipt without effective gas price")
			continue
		}
		byHash[receipt.TxHash] = receipt
	}

	return byHash
}

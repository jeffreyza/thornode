package thorclient

import (
	"fmt"
	"time"

	clienttx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	flag "github.com/spf13/pflag"

	stypes "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/common"
)

// broadcastWithPolicy broadcasts a transaction with the specified mode.
// The current SDK supports "async" and "sync". When strict is true every
// non-zero ABCI code is returned as an error; the legacy code-6 exception is
// retained only for generic Broadcast callers which explicitly use it.
func (b *thorchainBridge) broadcastWithPolicy(mode string, strict bool, msgs ...stypes.Msg) (common.TxID, error) {
	b.broadcastLock.Lock()
	defer b.broadcastLock.Unlock()

	noTxID := common.TxID("")

	start := time.Now()
	defer func() {
		b.m.GetHistograms(metrics.SendToThorchainDuration).Observe(time.Since(start).Seconds())
	}()

	blockHeight, err := b.GetBlockHeight()
	if err != nil {
		return noTxID, err
	}
	if blockHeight > b.blockHeight {
		var seqNum uint64
		b.accountNumber, seqNum, err = b.getAccountNumberAndSequenceNumber()
		if err != nil {
			return noTxID, fmt.Errorf("fail to get account number and sequence number from thorchain : %w", err)
		}
		b.blockHeight = blockHeight
		if seqNum > b.seqNumber {
			b.seqNumber = seqNum
		}
	}

	b.logger.Info().Uint64("account_number", b.accountNumber).Uint64("sequence_number", b.seqNumber).Str("broadcast_mode", mode).Msg("account info")

	flags := flag.NewFlagSet("thorchain", 0)

	ctx := b.GetContext()
	ctx = ctx.WithBroadcastMode(mode)
	factory, err := clienttx.NewFactoryCLI(ctx, flags)
	if err != nil {
		return noTxID, fmt.Errorf("failed to get factory, %w", err)
	}
	factory = factory.WithAccountNumber(b.accountNumber)
	factory = factory.WithSequence(b.seqNumber)
	factory = factory.WithSignMode(signing.SignMode_SIGN_MODE_DIRECT)

	builder, err := factory.BuildUnsignedTx(msgs...)
	if err != nil {
		return noTxID, err
	}
	builder.SetGasLimit(4000000000)
	err = clienttx.Sign(ctx.CmdContext, factory, ctx.GetFromName(), builder, true)
	if err != nil {
		return noTxID, err
	}

	txBytes, err := ctx.TxConfig.TxEncoder()(builder.GetTx())
	if err != nil {
		return noTxID, err
	}

	// broadcast to a Tendermint node
	commit, err := ctx.BroadcastTx(txBytes)
	if err != nil {
		return noTxID, fmt.Errorf("fail to broadcast tx: %w", err)
	}

	b.m.GetCounter(metrics.TxToThorchainSigned).Inc()
	txHash, err := common.NewTxID(commit.TxHash)
	if err != nil {
		return common.BlankTxID, fmt.Errorf("fail to convert txhash: %w", err)
	}
	// Code will be the tendermint ABICode , it start at 1 , so if it is an error , code will not be zero
	if commit.Code > 0 {
		if commit.Code == ABCIWrongSequenceCode {
			// bad sequence number, fetch new one
			_, seqNum, _ := b.getAccountNumberAndSequenceNumber()
			if seqNum > 0 {
				b.seqNumber = seqNum
			}
		}
		b.logger.Info().Int("bytes", len(txBytes)).Uint32("code", commit.Code).Interface("messages", msgs).Msg("failed tx")
		// Generic callers historically treat code 6 (unknown request) as a
		// terminal no-op. Security-sensitive callers use BroadcastStrict below so
		// every non-zero CheckTx response, including code 6, remains visible. A
		// zero code here does not prove later DeliverTx or voter persistence.
		if policyErr := broadcastApplicationError(commit.Code, commit.RawLog, strict); policyErr != nil {
			return txHash, policyErr
		}
	} else {
		b.seqNumber++
	}

	b.m.GetCounter(metrics.TxToThorchain).Inc()
	b.logger.Info().Msgf("Received a TxHash of %v from the thorchain", commit.TxHash)

	return txHash, nil
}

func (b *thorchainBridge) broadcast(mode string, msgs ...stypes.Msg) (common.TxID, error) {
	return b.broadcastWithPolicy(mode, false, msgs...)
}

// Broadcast Broadcasts tx to thorchain using sync mode (waits for CheckTx)
func (b *thorchainBridge) Broadcast(msgs ...stypes.Msg) (common.TxID, error) {
	return b.broadcast("sync", msgs...)
}

// BroadcastStrict broadcasts in sync mode and returns every non-zero CheckTx
// ABCI code as an error. It does not prove later DeliverTx execution or durable
// voter persistence. It deliberately is not part of ThorchainBridge: Signer
// discovers the production capability explicitly and fails closed when it is
// unavailable.
func (b *thorchainBridge) BroadcastStrict(msgs ...stypes.Msg) (common.TxID, error) {
	return b.broadcastWithPolicy("sync", true, msgs...)
}

// BroadcastWithBlocking broadcasts in sync mode and waits for a later block
// height. It does not query the transaction and therefore does not prove
// DeliverTx execution or inclusion in that block.
func (b *thorchainBridge) BroadcastWithBlocking(msgs ...stypes.Msg) (common.TxID, error) {
	// First broadcast with sync mode
	txID, err := b.broadcast("sync", msgs...)
	if err != nil {
		return txID, err
	}

	// Poll for the transaction to be included in a block
	// Poll for up to 30 seconds (6 blocks at 5 seconds per block)
	maxAttempts := 30
	for i := 0; i < maxAttempts; i++ {
		// Query for the block height to trigger state update
		currentHeight, err := b.GetBlockHeight()
		if err == nil && currentHeight > b.blockHeight {
			// A later height is only a timing signal; callers still lack transaction
			// inclusion or DeliverTx evidence.
			b.logger.Info().Str("txid", txID.String()).Int64("height", currentHeight).Msg("transaction likely committed to block")
			return txID, nil
		}

		// Wait 1 second before retrying
		time.Sleep(time.Second)
	}

	// Return success even if we timeout - the transaction was accepted in CheckTx
	b.logger.Warn().Str("txid", txID.String()).Msg("timeout waiting for block commitment, but transaction was accepted")
	return txID, nil
}

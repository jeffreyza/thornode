package types

import (
	"math/big"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ChainClient is the interface for chain clients.
type ChainClient interface {
	// Start starts the chain client with the given queues.
	Start(
		globalTxsQueue chan types.TxIn,
		globalErrataQueue chan types.ErrataBlock,
		globalSolvencyQueue chan types.Solvency,
		globalNetworkFeeQueue chan common.NetworkFee,
	)

	// Stop stops the chain client.
	Stop()

	// IsBlockScannerHealthy returns true if the block scanner is healthy.
	IsBlockScannerHealthy() bool

	// SignTx returns the signed transaction.
	SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error)

	// BroadcastTx broadcasts the transaction and returns the transaction hash.
	BroadcastTx(_ types.TxOutItem, _ []byte) (string, error)

	// GetHeight returns the current height of the chain.
	GetHeight() (int64, error)

	// GetAddress returns the address for the given public key.
	GetAddress(poolPubKey common.PubKey) string

	// GetAccount returns the account for the given public key.
	GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error)

	// GetAccountByAddress returns the account for the given address.
	GetAccountByAddress(address string, height *big.Int) (common.Account, error)

	// GetChain returns the chain.
	GetChain() common.Chain

	// GetConfig returns the chain configuration.
	GetConfig() config.BifrostChainConfiguration

	// OnObservedTxIn is called when a new observed tx is received.
	OnObservedTxIn(txIn types.TxInItem, blockHeight int64)

	// GetConfirmationCount returns the confirmation count for the given tx.
	GetConfirmationCount(txIn types.TxIn) int64

	// ConfirmationCountReady returns true if the confirmation count is ready.
	ConfirmationCountReady(txIn types.TxIn) bool

	// GetBlockScannerHeight returns block scanner height for chain
	GetBlockScannerHeight() (int64, error)

	// GetLatestTxForVault returns last observed and broadcasted tx for a particular vault and chain
	GetLatestTxForVault(vault string) (string, string, error)

	// RollbackBlockScanner rolls back the block scanner to re-observe recent blocks.
	RollbackBlockScanner() error
}

// PermanentErrataQuarantiner is an optional chain-client safety hook used
// when Observer receives a committed errata quorum event. Implementations must
// durably prevent the exact transaction from being broadcast or re-observed
// before returning success. Observer currently requires this hook for XMR,
// whose transactions do not expire, and leaves other chains unchanged.
type PermanentErrataQuarantiner interface {
	// BlockPermanentErrataBroadcast persists the exact hash fence without the
	// broader logical-owner/alias/scanner cleanup performed by quarantine.
	BlockPermanentErrataBroadcast(errata common.ErrataTx) error
	QuarantinePermanentErrata(errata common.ErrataTx, orphanHeight int64) error
}

// SpentOutputRefsImport keeps the semantic observation separate from the exact
// wire metadata authenticated by a chain client. WireMemo may be explicitly
// empty for vault-certified imports. ChainCommitted internal transactions
// derive their exact wire memo from Tx because internal memos are never
// stripped; other committed transactions remain accounting-only imports.
type SpentOutputRefsImport struct {
	Tx             common.ObservedTx
	WireMemo       string
	ChainCommitted bool
	// ReplaySource transfers a peer-certified mined payout to the scanner's
	// durable receipt queue before the import is acknowledged. Local signing
	// imports leave this false because they run before broadcast.
	ReplaySource bool
}

// SpentOutputRefsApplier is the optional XMR durability hook used after a
// signed outbound exposes its exact consumed outputs. Implementations report
// the final import outcome exactly once, including after any bounded retries.
// Callers must retain the outbound until the callback reports success.
type SpentOutputRefsApplier interface {
	ApplySpentOutputRefsWithOutcome(request SpentOutputRefsImport, onOutcome func(applied bool))
}

// SpentOutputRefsGuard orders an import and its durable effects against
// height-scoped errata cleanup. Rejected admission must prevent all mutation.
type SpentOutputRefsGuard func(common.ObservedTx) (release func(), ok bool)

// SpentOutputRefsGuardSetter wires durable Observer admission before the
// client starts, covering live imports, retries and periodic recovery.
type SpentOutputRefsGuardSetter interface {
	SetSpentOutputRefsGuard(SpentOutputRefsGuard)
}

// CustomKeygenRequest carries common keygen context for chain-specific hooks.
type CustomKeygenRequest struct {
	Members         []string
	BlockHeight     int64
	PoolPubKey      common.PubKey
	PoolPubKeyEddsa common.PubKey
}

// CustomKeygenResult carries the chain-specific outputs from a custom keygen
// hook, including the finalized EdDSA pool pubkey when the chain derives it
// from its own keygen transcript rather than reusing the generic THORChain one.
type CustomKeygenResult struct {
	Address              common.Address
	PoolPubKeyEddsa      common.PubKey
	PoolPubKeyFrost      common.PubKey
	FrostKeyshareHex     string
	ObservedMoneroHeight int64
	Blame                []ttypes.Blame
}

// CustomKeygenChainClient is an optional extension for chains which require
// chain-specific keygen state initialization during THORChain keygen.
type CustomKeygenChainClient interface {
	ChainClient
	CustomKeygen() bool
	Keygen(req CustomKeygenRequest) (CustomKeygenResult, error)
}

// SolvencyReporter reports the solvency of the chain at the given height.
type SolvencyReporter func(height int64) error

package rpc

import "github.com/btcsuite/btcd/btcjson"

// BlockResult extends the shared verbosity-2 block response with ChainLock
// metadata for DASH. ChainLock is false when absent.
// Transactions retain the shared Bitcoin fields; block acceptance uses the
// block's ChainLock rather than individual transaction locks.
type BlockResult struct {
	btcjson.GetBlockVerboseTxResult
	ChainLock bool `json:"chainlock"`
}

// TxResult extends the shared transaction response with InstantSend and
// ChainLock metadata for DASH.
// Other chains omit these fields and continue to use their own finality rules.
type TxResult struct {
	btcjson.TxRawResult
	InstantLock bool `json:"instantlock"`
	// DASH's internal InstantSend state excludes ChainLocks.
	InstantLockInternal bool `json:"instantlock_internal"`
	ChainLock           bool `json:"chainlock"`
}

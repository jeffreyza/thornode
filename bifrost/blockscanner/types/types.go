package types

import "fmt"

var (
	ErrUnavailableBlock        = fmt.Errorf("block is not yet available")
	ErrFailOutputMatchCriteria = fmt.Errorf("fail to get output matching criteria")
)

// ErrRollbackValidationUnavailable keeps a rollback pending when a temporary
// failure prevents the chain client from validating it. It does not authorize
// a cursor change or reject the requested replay.
var ErrRollbackValidationUnavailable = fmt.Errorf("rollback validation temporarily unavailable")

// MaxCursorRewindBlocks is the largest chain-client-directed cursor rewind the
// shared block scanner will apply automatically. Larger discontinuities need
// explicit operator recovery so a compromised or corrupt chain client cannot
// silently force an unbounded historical rescan.
const MaxCursorRewindBlocks int64 = 730

// RewindRequiredError tells BlockScanner that a chain-scanner cursor moved
// backwards and the next fetch must start at NextHeight. It is deliberately a
// typed control-flow error so arbitrary RPC failures cannot move the durable
// block-scanner cursor.
type RewindRequiredError struct {
	NextHeight int64
}

func (e RewindRequiredError) Error() string {
	return fmt.Sprintf("block scanner rewind required: next height %d", e.NextHeight)
}

// ScannerRecoveryRequiredError is a terminal scanner error. Retrying the same
// height cannot repair it; the operator must restore compatible chain-client
// state or deliberately reset the Bifrost cursor before restarting.
type ScannerRecoveryRequiredError struct {
	CurrentHeight int64
	NextHeight    int64
	Reason        string
}

func (e ScannerRecoveryRequiredError) Error() string {
	return fmt.Sprintf(
		"block scanner operator recovery required at height %d (chain client next height %d): %s",
		e.CurrentHeight,
		e.NextHeight,
		e.Reason,
	)
}

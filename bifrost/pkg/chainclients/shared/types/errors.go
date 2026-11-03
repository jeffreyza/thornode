package types

import "errors"

// ErrTxExpired means this signing attempt can no longer be mined. The signer
// must retire it without observing success or rebuilding it with a new digest;
// THORChain remains responsible for rescheduling the outbound.
var ErrTxExpired = errors.New("transaction expired")

// ErrUnsupportedXMRDestination retires only an unsigned local XMR attempt.
// The client must first exclude durable signing claims and recoverable artifacts;
// the signer must also exclude its own checkpoints and terminal-round retries.
// It does not discharge THORChain's pending outbound obligation.
var ErrUnsupportedXMRDestination = errors.New("unsupported unsigned XMR destination")

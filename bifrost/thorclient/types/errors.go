package types

import "errors"

// ErrResignRequired signals that a previously-signed outbound can never land on
// chain and must be re-signed rather than re-broadcast. The canonical case is a
// Solana transaction whose recent blockhash expired before broadcast (keysign
// ran long): the signed bytes are permanently dead, so re-broadcasting them
// loops forever. On this error the signer clears the stored signed tx and
// checkpoint so the next pass re-enters SignTx, where the chain client's
// sign-time validity gate re-evaluates the outbound (for SOL, whose blockhash is
// derived deterministically per keysign height, that gate drops the item for a
// fresh-height reschedule instead of re-deriving the same expired hash).
var ErrResignRequired = errors.New("signed tx can never land; re-sign required")

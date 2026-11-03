package solana

import (
	"time"

	"github.com/rs/zerolog"
)

// slotGetter is the subset of the Solana RPC client the initial-slot wait
// needs.
type slotGetter interface {
	GetSlot() (uint64, error)
}

// waitForInitialSlot fetches the slot the scanner initializes from, retrying
// until the RPC answers or the scanner is stopped. Giving up on the first
// error here would silently disable SOL observation for the life of the
// process: Start() runs once as a goroutine, nothing restarts it, and an
// early return leaves every other chain running with no indication beyond a
// single log line. An unavailable RPC at startup must behave like an
// unavailable RPC at runtime — retried, not fatal. Returns ok=false only
// when the scanner is stopped.
func waitForInitialSlot(
	g slotGetter,
	stop <-chan struct{},
	retryInterval time.Duration,
	logger zerolog.Logger,
) (uint64, bool) {
	for {
		slot, err := g.GetSlot()
		if err == nil {
			return slot, true
		}
		logger.Error().Err(err).
			Dur("retry_in", retryInterval).
			Msg("failed to get initial slot, retrying")
		select {
		case <-stop:
			return 0, false
		case <-time.After(retryInterval):
		}
	}
}

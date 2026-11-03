package monero

import (
	"errors"
	"fmt"
	"testing"

	"github.com/rs/zerolog/log"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
)

// mapScannerContinuityError decides whether a continuity failure is retryable.
// Cursor-ahead is the one case that is not: the sidecar has durably scanned past
// the height, so a generic wrap would leave BlockScanner re-fetching the same
// block forever. It must stay a distinct, typed, operator-actionable error.
func TestMapScannerContinuityErrorTypesCursorAhead(t *testing.T) {
	t.Parallel()

	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)

	t.Run("cursor ahead is terminal", func(t *testing.T) {
		t.Parallel()

		in := &sidecar.ScannerRecoveryRequiredError{
			RequestedHeight: 50, NextHeight: 60, ResetEpoch: 9,
		}
		got := scanner.mapScannerContinuityError(50, fmt.Errorf("preflight: %w", in))

		// BlockScanner STOPS on ScannerRecoveryRequiredError. Anything else here
		// leaves it re-fetching height 50 forever, which is the bug: the sidecar
		// has already durably scanned past this block.
		var recovery *btypes.ScannerRecoveryRequiredError
		if !errors.As(got, &recovery) {
			t.Fatalf("cursor-ahead must map to the terminal ScannerRecoveryRequiredError, got %v", got)
		}
		if recovery.CurrentHeight != 50 || recovery.NextHeight != 60 {
			t.Fatalf("recovery detail lost: %+v", recovery)
		}

		// It must NOT look like "block not ready", or the scanner would spin
		// quietly on it without ever going unhealthy.
		if errors.Is(got, btypes.ErrUnavailableBlock) {
			t.Fatal("cursor-ahead must not be mapped to ErrUnavailableBlock")
		}
		// And it must not be a rewind: rewinding FORWARD to meet the sidecar
		// would skip the intervening blocks without ever observing them.
		var rewind *btypes.RewindRequiredError
		if errors.As(got, &rewind) {
			t.Fatal("cursor-ahead must not be mapped to a rewind")
		}
	})

	t.Run("cursor behind still maps to a rewind", func(t *testing.T) {
		t.Parallel()

		in := &sidecar.ScannerRewindRequiredError{RequestedHeight: 50, NextHeight: 40}
		got := scanner.mapScannerContinuityError(50, in)

		var rewind *btypes.RewindRequiredError
		if !errors.As(got, &rewind) {
			t.Fatalf("expected a RewindRequiredError, got %v", got)
		}
		if rewind.NextHeight != 40 {
			t.Fatalf("unexpected rewind height: %+v", rewind)
		}
	})
}

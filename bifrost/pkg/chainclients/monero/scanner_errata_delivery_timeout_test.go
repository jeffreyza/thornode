package monero

import (
	"testing"
	"time"

	"github.com/rs/zerolog/log"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// deliverReorgErrata sends on an unbuffered queue while holding scanMu, from
// inside FetchTxs. If the observer's errata loop has already exited, an
// unbounded send parks the block-scanner goroutine forever and
// BlockScanner.Stop() hangs on wg.Wait(). Both the send and the acknowledgement
// must therefore be bounded, and the failure must be a returned error so the
// replay is redriven on the next FetchTxs.
func TestDeliverReorgErrataTimesOutWhenObserverIsGone(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// drain consumes the send without ever acknowledging, isolating the
		// acknowledgement wait from the send itself. Acknowledgement is always
		// required, so both paths must be independently bounded.
		drain bool
	}{
		// Nobody ever receives: the send itself must time out.
		{name: "observer not consuming", drain: false},
		// Received but never acknowledged: the ack wait must time out.
		{name: "observer never acknowledges", drain: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
			scanner.errataDeliveryTimeout = 50 * time.Millisecond

			queue := make(chan stypes.ErrataBlock)
			scanner.SetErrataQueue(queue)
			if tc.drain {
				// Take the block but never invoke OnStored/OnRejected.
				go func() { <-queue }()
			}

			done := make(chan error, 1)
			go func() {
				done <- scanner.deliverReorgErrata(100, []stypes.ErrataTx{
					{TxID: common.TxID(testXMRHash("stranded-tx"))},
				})
			}()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("expected a timeout error, got nil")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("deliverReorgErrata blocked forever; scanMu is held here, " +
					"so this wedges the scanner and hangs BlockScanner.Stop()")
			}
		})
	}
}

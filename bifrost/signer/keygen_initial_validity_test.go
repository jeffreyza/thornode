package signer

import (
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Catch-up delivers old keygen blocks only once. Even a first admission must
// reject a result window that has already closed, before starting any DKG.
func TestXMRKeygenInitialValidity(t *testing.T) {
	minimum := int64((6*time.Minute + constants.ThorchainBlockTime - 1) / constants.ThorchainBlockTime)
	def := constants.NewConstantValue().GetInt64Value(constants.ChurnRetryInterval)
	// The acceptance window ends one block before the retry tick (mirror of
	// THORNode's keygenResultValidityBlocks returning N-1): the last valid age is
	// N-1 and age N — the block the churn retry fires — is already expired, so a
	// stale result cannot finalize a vault on the block its replacement is
	// scheduled. churn==0 keeps the disabled-retry sentinel (delivery block only).
	for _, test := range []struct {
		name           string
		churn, elapsed int64
		wantRuns       int
	}{
		{"current", 1000, 0, 1},
		{"last valid before retry tick", 1000, 999, 1},
		{"expired at retry tick", 1000, 1000, 0},
		{"expired past retry tick", 1000, 1001, 0},
		{"unset default last valid", -1, def - 1, 1},
		{"unset default expired at tick", -1, def, 0},
		{"XMR minimum last valid", 1, minimum - 1, 1},
		{"XMR minimum expired at tick", 1, minimum, 0},
		{"zero window same block", 0, 0, 1},
		{"zero window next block", 0, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			signer, bridge, client := replacementTestSigner(t, nil)
			client.releaseFirst()
			block := newSingleFlightXMRKeygen(t, 2700)
			bridge.setMimir(constants.ChurnRetryInterval.String(), test.churn)
			bridge.setMimir(constants.KeygenRetryInterval.String(), 0)
			bridge.setHeight(block.Height + test.elapsed)
			signer.processKeygenBlock(block)
			if client.callCount() != test.wantRuns || bridge.broadcastCount() != test.wantRuns {
				t.Fatalf("DKG/result counts %d/%d, want %d", client.callCount(), bridge.broadcastCount(), test.wantRuns)
			}
			if signer.xmrKeygen.active != nil || signer.xmrKeygen.pending != nil {
				t.Fatal("initial admission retained completed or expired work")
			}
			if bridge.mimirReadCount(constants.KeygenRetryInterval.String()) != 0 {
				t.Fatal("initial admission depended on retry enablement")
			}
			if test.wantRuns == 0 {
				fresh := newSingleFlightXMRKeygen(t, block.Height+test.elapsed)
				signer.processKeygenBlock(fresh)
				if client.callCount() != 1 || bridge.broadcastCount() != 1 {
					t.Fatal("expired initial request prevented a later fresh ceremony")
				}
			}
		})
	}
}

// Rejecting an expired XMR request must let the serial consumer reach its
// same-block Asgard companion, without starting XMR or changing Asgard policy.
func TestExpiredInitialXMRDoesNotDelayAsgard(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	signer.tssKeygen = &tss.KeyGen{}
	bridge.setHeight(3701)
	block := newSingleFlightXMRKeygen(t, 2700)
	block.Keygens = append(block.Keygens, ttypes.Keygen{Type: ttypes.KeygenType_AsgardKeygen})
	done := make(chan struct{})
	go func() {
		defer close(done)
		signer.processKeygenBlock(block)
	}()
	waitForSingleFlightReturn(t, done, "Asgard after expired initial XMR")
	// The empty Asgard key takes its existing failure/retry path. One window
	// read for XMR admission and one for Asgard retries prove both decisions.
	if client.callCount() != 0 || bridge.mimirReadCount(constants.ChurnRetryInterval.String()) != 2 {
		t.Fatal("expired XMR started DKG or prevented ordinary Asgard processing")
	}
}

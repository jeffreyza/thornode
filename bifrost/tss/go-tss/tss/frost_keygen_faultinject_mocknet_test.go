//go:build mocknet
// +build mocknet

package tss

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// TestFrostKeygenRound3TestTimeoutOverrideShrinksOnlyRound3Wait covers the
// fault-injection hook used by the xmr-keygen-recovery simulation: with
// XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS set, the round-3 wait times out on
// the override instead of the coordinator timeout, and the blame is a normal
// round-3 timeout blame. It also emits the same-ceremony fault evidence required
// by the simulation, only after the timer actually expires.
func TestFrostKeygenRound3TestTimeoutOverrideShrinksOnlyRound3Wait(t *testing.T) {
	ensureBech32Prefix()
	t.Setenv("XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS", "50")

	members := []string{testPubKeys[0], testPubKeys[1]}
	_, participantIndexMap, signerByNormalized, err := buildParticipantIndex(members)
	if err != nil {
		t.Fatalf("build participant index: %v", err)
	}
	activeSet, err := buildActiveSet(members, signerByNormalized)
	if err != nil {
		t.Fatalf("build active set: %v", err)
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(members)
	if err != nil {
		t.Fatalf("peer ids: %v", err)
	}

	// Coordinator timeout is far beyond the test budget; only the override can
	// end the round-3 wait this quickly.
	coord := newTestFrostKeygenCoordinator(testPubKeys[0], "msg-id", newFrostKeygenInbox(), time.Hour)
	var logOutput bytes.Buffer
	coord.logger = zerolog.New(&logOutput).With().Str("msg_id", coord.msgID).Logger()
	initTestFrostKeygenRoundState(t, coord, activeSet, participantIndexMap)
	start := time.Now()
	_, _, _, failResp := coord.exchangeRound3(activeSet, participantIndexMap, peerIDs, FrostDKGRound3Payload{
		SenderPubKey:     testPubKeys[0],
		ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		Address:          "48addr",
		GroupKey:         strings.Repeat("ab", 32),
	})
	elapsed := time.Since(start)

	if failResp == nil {
		t.Fatalf("expected round3 timeout failure under test override")
	}
	if failResp.Status != tcommon.Fail {
		t.Fatalf("expected fail status, got %d", failResp.Status)
	}
	if failResp.Blame.Round != frostDKGRound3 {
		t.Fatalf("expected blame round %s, got %s", frostDKGRound3, failResp.Blame.Round)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("round3 wait ignored the test override, took %s", elapsed)
	}
	var sawOverride, sawTimeout bool
	for _, line := range bytes.Split(logOutput.Bytes(), []byte("\n")) {
		var event struct {
			Message  string `json:"message"`
			Ceremony string `json:"msg_id"`
			Round    string `json:"round"`
		}
		if json.Unmarshal(line, &event) != nil || event.Ceremony != "msg-id" {
			continue
		}
		switch event.Message {
		case "TEST override active: XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS shrinks the round-3 wait":
			sawOverride = true
		case "TEST injected XMR FROST round-3 timeout":
			sawTimeout = sawOverride && event.Round == frostDKGRound3
		}
	}
	if !sawTimeout {
		t.Fatalf("missing ordered same-ceremony override and actual timeout evidence: %s", logOutput.String())
	}
}

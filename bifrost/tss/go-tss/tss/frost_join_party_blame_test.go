package tss

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
)

func TestFrostJoinAttemptIDIsDeterministicAndAttemptScoped(t *testing.T) {
	peers := []string{"peer-b", "peer-a", "peer-c"}
	first, err := frostJoinAttemptID("stable-msg-id", "leader-a", peers, 0)
	if err != nil {
		t.Fatalf("first attempt ID: %v", err)
	}
	repeated, err := frostJoinAttemptID("stable-msg-id", "leader-a", []string{"peer-c", "peer-b", "peer-a"}, 0)
	if err != nil {
		t.Fatalf("repeated attempt ID: %v", err)
	}
	if first != repeated {
		t.Fatalf("join attempt ID is not deterministic: %q != %q", first, repeated)
	}
	if !strings.HasPrefix(first, p2p.FrostJoinAttemptIDPrefix) {
		t.Fatalf("join attempt ID %q is not marked ephemeral", first)
	}

	nextAttempt, err := frostJoinAttemptID("stable-msg-id", "leader-b", peers, 1)
	if err != nil {
		t.Fatalf("next attempt ID: %v", err)
	}
	if first == nextAttempt {
		t.Fatal("rotated leader reused the previous join session ID")
	}
	if _, err := frostJoinAttemptID("", "leader-a", peers, 0); err == nil {
		t.Fatal("expected empty signing msgID to be rejected")
	}
}

func TestFrostJoinPartyBlameDoesNotBlameAmbiguousLeader(t *testing.T) {
	b := frostJoinPartyBlame()
	if b.FailReason != blame.TssSyncFail {
		t.Fatalf("expected shared blame reason %q for consensus, got %q", blame.TssSyncFail, b.FailReason)
	}
	if len(b.BlameNodes) != 0 {
		t.Fatalf("expected no blamed nodes without positive evidence, got %d", len(b.BlameNodes))
	}
}

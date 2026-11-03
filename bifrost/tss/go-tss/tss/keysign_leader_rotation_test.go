package tss

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	tnet "github.com/libp2p/go-libp2p-testing/net"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/rs/zerolog"
	tsslibcommon "gitlab.com/thorchain/tss/tss-lib-private.git/v3/common"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	tcommon "gitlab.com/thorchain/thornode/v3/common"
)

func TestKeysignLeaderRotationVersionGate(t *testing.T) {
	cases := []struct {
		version string
		want    bool
		wantErr bool
	}{
		{"3.20.3", false, false}, // pre-gate bifrost patch
		{"3.21.0", false, false}, // pre-gate release
		{"3.21.1", false, false}, // the release before the gate; mainnet 3.21.1 does not ship rotation
		{"3.21.2", true, false},  // gate opens exactly here
		{"3.21.3", true, false},  // later patch
		{"3.22.0", true, false},  // later minor
		{"not-a-version", false, true},
	}
	for _, c := range cases {
		got, err := keysignLeaderRotationEnabled(c.version)
		if c.wantErr {
			if err == nil {
				t.Fatalf("version %q: expected parse error", c.version)
			}
			continue
		}
		if err != nil {
			t.Fatalf("version %q: unexpected error %v", c.version, err)
		}
		if got != c.want {
			t.Fatalf("version %q: enabled=%v, want %v", c.version, got, c.want)
		}
	}
}

func TestKeysignJoinAttemptIDDeterministicAttemptScopedAndAlgoDistinct(t *testing.T) {
	peers := []string{"peer-b", "peer-a", "peer-c"}
	first, err := keysignJoinAttemptID("stable-msg-id", "leader-a", peers, 0)
	if err != nil {
		t.Fatalf("first attempt ID: %v", err)
	}
	// deterministic + order-independent in the peer set.
	repeated, err := keysignJoinAttemptID("stable-msg-id", "leader-a", []string{"peer-c", "peer-b", "peer-a"}, 0)
	if err != nil {
		t.Fatalf("repeated attempt ID: %v", err)
	}
	if first != repeated {
		t.Fatalf("join attempt ID is not deterministic: %q != %q", first, repeated)
	}
	// marked ephemeral so the coordinator resets an early/stood-down request.
	if !strings.HasPrefix(first, p2p.FrostJoinAttemptIDPrefix) {
		t.Fatalf("join attempt ID %q is not marked ephemeral", first)
	}
	// a rotated leader / later attempt must use a fresh session id.
	next, err := keysignJoinAttemptID("stable-msg-id", "leader-b", peers, 1)
	if err != nil {
		t.Fatalf("next attempt ID: %v", err)
	}
	if first == next {
		t.Fatal("rotated leader reused the previous join session ID")
	}
	// domain separation: the GG20 id must differ from the FROST id for identical inputs,
	// so the two protocols can never collide on a shared msgID.
	frostID, err := frostJoinAttemptID("stable-msg-id", "leader-a", peers, 0)
	if err != nil {
		t.Fatalf("frost attempt ID: %v", err)
	}
	if first == frostID {
		t.Fatal("keysign and FROST join attempt IDs collide for identical inputs")
	}
	// input validation.
	if _, err := keysignJoinAttemptID("", "leader-a", peers, 0); err == nil {
		t.Fatal("expected empty msgID to be rejected")
	}
	if _, err := keysignJoinAttemptID("m", "", peers, 0); err == nil {
		t.Fatal("expected empty leader to be rejected")
	}
	if _, err := keysignJoinAttemptID("m", "leader-a", nil, 0); err == nil {
		t.Fatal("expected empty peer set to be rejected")
	}
	if _, err := keysignJoinAttemptID("m", "leader-a", []string{"p", "p"}, 0); err == nil {
		t.Fatal("expected duplicate peers to be rejected")
	}
}

func TestKeysignSignatureWaitTimeoutCoversRotatedJoins(t *testing.T) {
	s := &TssServer{conf: tsscommon.TssConfig{KeySignTimeout: 45 * time.Second, PartyTimeout: 45 * time.Second}}
	cases := []struct {
		version string
		want    time.Duration
	}{
		{"3.21.1", 45 * time.Second},  // pre-gate: legacy wait unchanged
		{"3.21.2", 185 * time.Second}, // keysign timeout + slack + 3 rotated join attempts
		{"not-a-version", 45 * time.Second},
	}
	for _, c := range cases {
		if got := s.keysignSignatureWaitTimeout(c.version); got != c.want {
			t.Fatalf("version %q: wait=%s, want %s", c.version, got, c.want)
		}
	}
}

// A party formed on a later rotation attempt signs after the keysign timeout alone has
// elapsed; the rotation-sized wait must still deliver that peer's signature.
func TestWaitForSignaturesReceivesSignatureFromRotatedParty(t *testing.T) {
	conversion.SetupBech32Prefix()
	p2p.ApplyDeadline.Store(false)
	poolPubKey := `thorpub1addwnpepq0ul3xt882a6nm6m7uhxj4tk2n82zyu647dyevcs5yumuadn4uamqx7neak`
	msg, err := base64.StdEncoding.DecodeString("yhEwrxWuNBGnPT/L7PNnVWg7gFWNzCYTV+GuX3tKRH8=")
	if err != nil {
		t.Fatal(err)
	}
	msgID, err := tsscommon.MsgToHashString(msg)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile("../test_data/signature_notify/sig1.json")
	if err != nil {
		t.Fatal(err)
	}
	var signature tsslibcommon.SignatureData
	if err := json.Unmarshal(content, &signature); err != nil {
		t.Fatal(err)
	}

	const timeout = 300 * time.Millisecond
	conf := tsscommon.TssConfig{KeySignTimeout: timeout, PartyTimeout: timeout}
	cases := []struct {
		version string
		wantSig bool
	}{
		{"3.21.1", false}, // pre-gate wait has already expired
		{"3.21.2", true},
	}
	for _, c := range cases {
		mn := mocknet.New(context.Background())
		h1, err := mn.AddPeer(tnet.RandIdentityOrFatal(t).PrivateKey(), tnet.RandLocalTCPAddress())
		if err != nil {
			t.Fatal(err)
		}
		h2, err := mn.AddPeer(tnet.RandIdentityOrFatal(t).PrivateKey(), tnet.RandLocalTCPAddress())
		if err != nil {
			t.Fatal(err)
		}
		if err := mn.LinkAll(); err != nil {
			t.Fatal(err)
		}
		if err := mn.ConnectAllButSelf(); err != nil {
			t.Fatal(err)
		}
		s := &TssServer{conf: conf, logger: zerolog.Nop(), signatureNotifier: keysign.NewSignatureNotifier(h1)}
		signer := keysign.NewSignatureNotifier(h2)

		sent := make(chan struct{})
		go func() {
			defer close(sent)
			// well past the pre-gate wait and well inside the rotated one, so a loaded runner
			// is unlikely to flip either case.
			time.Sleep(4 * timeout)
			_ = signer.BroadcastSignature(msgID, []*tsslibcommon.SignatureData{&signature}, []peer.ID{h1.ID()})
		}()
		resp, err := s.waitForSignatures(msgID, poolPubKey, [][]byte{msg}, tcommon.SigningAlgoSecp256k1, s.keysignSignatureWaitTimeout(c.version), make(chan string))
		<-sent
		if !c.wantSig {
			if err == nil || !strings.Contains(err.Error(), "timeout") {
				t.Fatalf("version %q: expected the wait to time out, got err=%v", c.version, err)
			}
		} else {
			if err != nil {
				t.Fatalf("version %q: expected the peer signature, got err=%v", c.version, err)
			}
			if resp.Status != tsscommon.Success || len(resp.Signatures) != 1 {
				t.Fatalf("version %q: unexpected response %+v", c.version, resp)
			}
		}
		s.signatureNotifier.Stop()
		signer.Stop()
		_ = h1.Close()
		_ = h2.Close()
	}
}

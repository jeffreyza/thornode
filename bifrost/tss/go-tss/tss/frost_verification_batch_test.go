package tss

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tcrypto "github.com/cometbft/cometbft/crypto"
	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
)

func TestFrostKeysignVerificationBatchAcceptsOnlyBoundedHomogeneousEnvelopes(t *testing.T) {
	entry := FrostVerifyPayload{
		SenderPubKey:           testPubKeys[0],
		Round:                  frostRoundPreprocess,
		TargetSenderPubKey:     testPubKeys[1],
		TargetParticipantIndex: 2,
		PayloadHash:            strings.Repeat("a", 64),
	}
	tooMany := make([]FrostVerifyPayload, frostVerificationBatchMaxEntries+1)
	for i := range tooMany {
		tooMany[i] = entry
	}
	mixedRound := entry
	mixedRound.Round = frostRoundShare
	mixedSender := entry
	mixedSender.SenderPubKey = testPubKeys[1]

	tests := []struct {
		name    string
		payload any
	}{
		{name: "legacy single echo", payload: entry},
		{name: "empty batch", payload: FrostVerifyBatchPayload{}},
		{name: "too many entries", payload: FrostVerifyBatchPayload{Verifications: tooMany}},
		{name: "mixed rounds", payload: FrostVerifyBatchPayload{Verifications: []FrostVerifyPayload{entry, mixedRound}}},
		{name: "mixed senders", payload: FrostVerifyBatchPayload{Verifications: []FrostVerifyPayload{entry, mixedSender}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if _, err := decodeFrostVerificationBatch(payload); err == nil {
				t.Fatal("expected verification envelope to be rejected")
			}
		})
	}

	payload, err := json.Marshal(FrostVerifyBatchPayload{Verifications: []FrostVerifyPayload{entry, entry}})
	if err != nil {
		t.Fatalf("marshal valid batch: %v", err)
	}
	entries, err := decodeFrostVerificationBatch(payload)
	if err != nil || len(entries) != 2 {
		t.Fatalf("valid homogeneous batch failed: entries=%d err=%v", len(entries), err)
	}
}

func TestDecodeFrostKeysignVerificationBatchStopsBeforeOversizedEntry(t *testing.T) {
	entry := FrostVerifyPayload{
		SenderPubKey:           testPubKeys[0],
		Round:                  frostRoundPreprocess,
		TargetSenderPubKey:     testPubKeys[1],
		TargetParticipantIndex: 2,
		PayloadHash:            strings.Repeat("a", 64),
	}
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}

	// Entry 129 is intentionally invalid as a FrostVerifyPayload. The decoder
	// must enforce the entry-count bound before attempting to unmarshal it.
	entries := make([]string, frostVerificationBatchMaxEntries+1)
	for i := range frostVerificationBatchMaxEntries {
		entries[i] = string(encodedEntry)
	}
	entries[frostVerificationBatchMaxEntries] = `{"target_participant_index":"not-a-number"}`
	payload := []byte(`{"verifications":[` + strings.Join(entries, ",") + `]}`)

	_, err = decodeFrostVerificationBatch(payload)
	if err == nil {
		t.Fatal("expected oversized verification batch to be rejected")
	}
	if !strings.Contains(err.Error(), "maximum is 128") {
		t.Fatalf("oversized batch was decoded past the entry bound: %v", err)
	}

	// Entries inside the bound still receive normal strict typed decoding.
	withinBound := []byte(`{"verifications":[{"target_participant_index":"not-a-number"}]}`)
	_, err = decodeFrostVerificationBatch(withinBound)
	if err == nil {
		t.Fatal("expected malformed in-bound verification entry to be rejected")
	}
	if strings.Contains(err.Error(), "maximum is") {
		t.Fatalf("malformed in-bound entry was mistaken for an oversized batch: %v", err)
	}
}

func TestDecodeFrostKeysignVerificationBatchRetainsOnlyNeededCapacity(t *testing.T) {
	for _, count := range []int{1, 2, frostVerificationBatchMaxEntries} {
		t.Run(fmt.Sprintf("entries_%d", count), func(t *testing.T) {
			payload, err := json.Marshal(FrostVerifyBatchPayload{Verifications: make([]FrostVerifyPayload, count)})
			if err != nil {
				t.Fatal(err)
			}
			entries, err := decodeFrostVerificationBatch(payload)
			if err != nil || len(entries) != count {
				t.Fatalf("decoded entries=%d, want %d: %v", len(entries), count, err)
			}
			// Go's allocation size classes can round capacity past the logical
			// entry cap. Retained storage must still scale with this frame's count.
			if cap(entries) > 2*count {
				t.Fatalf("%d-entry frame retains capacity=%d", count, cap(entries))
			}
		})
	}
}

func BenchmarkDecodeFrostKeysignVerificationBatch(b *testing.B) {
	for _, count := range []int{1, frostVerificationBatchMaxEntries} {
		b.Run(fmt.Sprintf("entries_%d", count), func(b *testing.B) {
			entries := make([]FrostVerifyPayload, count)
			for i := range entries {
				entries[i] = FrostVerifyPayload{
					ProtocolVersion:        messages.FrostProtocolVersion,
					SenderPubKey:           testPubKeys[0],
					Round:                  frostRoundShare,
					TargetSenderPubKey:     testPubKeys[1],
					TargetParticipantIndex: 2,
					PayloadHash:            strings.Repeat("a", 64),
					TargetSignature:        make([]byte, 64),
					AttesterSignature:      make([]byte, 64),
				}
			}
			payload, err := json.Marshal(FrostVerifyBatchPayload{Verifications: entries})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for range b.N {
				decoded, decodeErr := decodeFrostVerificationBatch(payload)
				if decodeErr != nil || len(decoded) != count {
					b.Fatalf("decoded entries=%d, want %d: %v", len(decoded), count, decodeErr)
				}
			}
		})
	}
}

func receiveFrostKeysignBroadcast(t *testing.T, channel <-chan *messages.BroadcastMsgChan) *messages.BroadcastMsgChan {
	t.Helper()
	select {
	case queued := <-channel:
		if queued == nil {
			t.Fatal("keysign broadcast channel closed or returned nil")
		}
		return queued
	case <-time.After(10 * frostVerificationBatchInterval):
		t.Fatal("timed out waiting for keysign broadcast")
		return nil
	}
}

func testFrostKeysignVerification(t *testing.T, attesterKey tcrypto.PrivKey, attesterPubKey string, target messages.FrostSignedEnvelope) FrostVerifyPayload {
	t.Helper()
	echo, err := newFrostSignedEcho(attesterKey, attesterPubKey, target)
	if err != nil {
		t.Fatalf("sign verification: %v", err)
	}
	return FrostVerifyPayload{
		ProtocolVersion:        echo.ProtocolVersion,
		SenderPubKey:           echo.AttesterPubKey,
		Round:                  echo.Target.Round,
		TargetSenderPubKey:     echo.Target.SenderPubKey,
		TargetParticipantIndex: echo.Target.ParticipantIndex,
		PayloadHash:            echo.Target.PayloadHash,
		TargetSignature:        echo.Target.Signature,
		AttesterSignature:      echo.Signature,
	}
}

func TestFrostKeysignVerificationBatchValidatesEverySignedEntry(t *testing.T) {
	for _, currentRound := range []string{frostRoundShare, frostRoundPreprocess} {
		t.Run(currentRound, func(t *testing.T) {
			ensureBech32Prefix()
			localKey, localPubKey := newTestFrostSigner(t)
			remoteKey, remotePubKey := newTestFrostSigner(t)
			activeSet, indexes, _ := testFrostSignParticipants(t, []string{localPubKey, remotePubKey})
			coord := newTestFrostSignCoordinator(localPubKey, testHashOf('f'), newFrostSignInbox(), time.Second)
			coord.privateKey, coord.strictTranscript = localKey, true
			initTestFrostSignRoundState(t, coord, activeSet, indexes)
			var entries []FrostVerifyPayload
			for _, statement := range []struct {
				key    tcrypto.PrivKey
				pubKey string
				hash   rune
			}{
				{localKey, localPubKey, 'a'},
				{remoteKey, remotePubKey, 'b'},
				{remoteKey, remotePubKey, 'c'},
			} {
				target, err := newFrostSignedEnvelope(statement.key, coord.msgID, coord.vaultPubKey, coord.network,
					frostRoundShare, statement.pubKey, indexes[normalizePubKey(statement.pubKey)], testHashOf(statement.hash))
				if err != nil {
					t.Fatal(err)
				}
				entries = append(entries, testFrostKeysignVerification(t, remoteKey, remotePubKey, target))
			}
			// The target statement remains correctly signed. Only its attester is
			// forged; accepting it would make the next valid entry look equivocal.
			entries[1].AttesterSignature[0] ^= 1
			enqueueKeySignMessage(t, coord.inbox, messages.TSSFrostVerifyMsg, coord.msgID, remotePubKey,
				FrostVerifyBatchPayload{Verifications: entries})
			if response := coord.routeVerificationMessage(<-coord.inbox.verify, currentRound); response != nil {
				t.Fatalf("forged entry poisoned valid neighbors: %+v", response)
			}
			verifier, err := coord.roundState.verifier(frostRoundShare)
			if err != nil {
				t.Fatal(err)
			}
			if currentRound == frostRoundPreprocess {
				if len(verifier.votesByTarget) != 0 || len(verifier.signedByTarget) != 0 || len(coord.roundState.deferredVerifications[frostRoundShare]) != 3 {
					t.Fatal("future batch was applied or lost before share activation")
				}
				if response := coord.activateDeferredVerifications(frostRoundShare); response != nil {
					t.Fatalf("forged deferred entry poisoned valid neighbors: %+v", response)
				}
			}
			for _, entry := range []FrostVerifyPayload{entries[0], entries[2]} {
				target := normalizePubKey(entry.TargetSenderPubKey)
				if got := verifier.votesByTarget[target][normalizePubKey(remotePubKey)]; got != entry.PayloadHash {
					t.Fatalf("valid target %s vote=%q, want %q", target, got, entry.PayloadHash)
				}
				if got := verifier.signedByTarget[target].PayloadHash; got != entry.PayloadHash {
					t.Fatalf("signed target %s hash=%q, want %q", target, got, entry.PayloadHash)
				}
			}
			remotePeer, err := conversion.GetPeerIDFromPubKey(remotePubKey)
			if err != nil {
				t.Fatal(err)
			}
			if coord.verificationEntriesByPeer[remotePeer] != 3 || len(coord.roundState.deferredVerifications) != 0 {
				t.Fatal("logical entries were not charged exactly once or remained deferred")
			}
		})
	}
}

func TestFrostKeysignVerificationBatchEquivocationStopsLocalShare(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprintf("deferred_%t", deferred), func(t *testing.T) {
			ensureBech32Prefix()
			localKey, localPubKey := newTestFrostSigner(t)
			remoteKey, remotePubKey := newTestFrostSigner(t)
			activeSet, indexes, peers := testFrostSignParticipants(t, []string{localPubKey, remotePubKey})
			coord := newTestFrostSignCoordinator(localPubKey, testHashOf('f'), newFrostSignInbox(), time.Second)
			coord.privateKey, coord.strictTranscript = localKey, true
			initTestFrostSignRoundState(t, coord, activeSet, indexes)
			var entries []FrostVerifyPayload
			for _, hash := range []rune{'a', 'a', 'b'} {
				target, err := newFrostSignedEnvelope(remoteKey, coord.msgID, coord.vaultPubKey, coord.network,
					frostRoundShare, remotePubKey, indexes[normalizePubKey(remotePubKey)], testHashOf(hash))
				if err != nil {
					t.Fatal(err)
				}
				entries = append(entries, testFrostKeysignVerification(t, remoteKey, remotePubKey, target))
			}
			enqueueKeySignMessage(t, coord.inbox, messages.TSSFrostVerifyMsg, coord.msgID, remotePubKey,
				FrostVerifyBatchPayload{Verifications: entries})
			if deferred {
				if response := coord.routeVerificationMessage(<-coord.inbox.verify, frostRoundPreprocess); response != nil {
					t.Fatalf("future equivocation changed preprocess semantics: %+v", response)
				}
				if len(coord.roundState.deferredVerifications[frostRoundShare]) != 3 {
					t.Fatal("future conflicting batch was not retained")
				}
			}
			_, response := coord.exchangeShares(activeSet, indexes, peers, FrostSharePayload{
				SenderPubKey: localPubKey, ParticipantIndex: indexes[normalizePubKey(localPubKey)], ShareHex: "cc",
			})
			if response == nil || response.Blame.FailReason != frostEquivocationFailReason || response.Blame.Round != frostRoundShare {
				t.Fatalf("signed conflicting batch did not terminate share phase: %+v", response)
			}
			if response.releaseSigningClaim || len(coord.p2pCommunication.BroadcastMsgChan) != 0 {
				t.Fatal("terminal batch evidence released the claim or allowed local-share broadcast")
			}
		})
	}
}

type frostKeysignVerificationTraffic struct {
	broadcastTasks       int
	logicalVerifications int
	fanoutIDs            int
	remoteStreamWrites   int
	payloadBytes         int
	networkBytes         uint64
}

func measureFrostKeysignVerificationTraffic(t *testing.T, partySize int, fragmentedWindows bool) frostKeysignVerificationTraffic {
	t.Helper()
	ensureBech32Prefix()
	localPrivateKey, localPubKey := newTestFrostSigner(t)
	remotePrivateKey, remotePubKey := newTestFrostSigner(t)
	msgID := strings.Repeat("a", 64)
	coord := newTestFrostSignCoordinator(localPubKey, msgID, newFrostSignInbox(), time.Second)
	coord.privateKey = localPrivateKey
	coord.strictTranscript = true
	coord.vaultPubKey = localPubKey
	localPeerID := peer.ID("local-signer")
	peers := make([]peer.ID, partySize)
	peers[0] = localPeerID
	for i := 1; i < len(peers); i++ {
		peers[i] = peer.ID(fmt.Sprintf("peer-%03d", i-1))
	}

	traffic := frostKeysignVerificationTraffic{}
	drain := func(round string) {
		t.Helper()
		for len(coord.p2pCommunication.BroadcastMsgChan) > 0 {
			queued := <-coord.p2pCommunication.BroadcastMsgChan
			if queued.WrappedMessage.MessageType != messages.TSSFrostVerifyMsg {
				t.Fatalf("traffic fixture message type=%s, want verification", queued.WrappedMessage.MessageType)
			}
			entries, err := decodeFrostVerificationBatch(queued.WrappedMessage.Payload)
			if err != nil {
				t.Fatalf("decode %s traffic batch: %v", round, err)
			}
			encoded, err := json.Marshal(queued.WrappedMessage)
			if err != nil {
				t.Fatalf("encode %s traffic wrapper: %v", round, err)
			}
			if got := len(queued.WrappedMessage.Payload); got >= p2p.FrostMaxSessionPayload {
				t.Fatalf("%s traffic batch payload bytes=%d exceeds frame bound=%d", round, got, p2p.FrostMaxSessionPayload)
			}
			if got := len(queued.PeersID); got != partySize {
				t.Fatalf("%s traffic batch fanout IDs=%d, want %d", round, got, partySize)
			}
			localIDs := 0
			for _, peerID := range queued.PeersID {
				if peerID == localPeerID {
					localIDs++
				}
			}
			if localIDs != 1 {
				t.Fatalf("%s traffic batch local fanout IDs=%d, want 1", round, localIDs)
			}
			// Production fanout includes self, so broadcastToPeers schedules one
			// goroutine for every ID. The local goroutine returns before stream I/O.
			remoteWrites := len(queued.PeersID) - localIDs
			traffic.broadcastTasks++
			traffic.logicalVerifications += len(entries)
			traffic.fanoutIDs += len(queued.PeersID)
			traffic.remoteStreamWrites += remoteWrites
			traffic.payloadBytes += len(queued.WrappedMessage.Payload)
			traffic.networkBytes += uint64(len(encoded)) * uint64(remoteWrites)
		}
	}

	for _, round := range []string{frostRoundPreprocess, frostRoundShare} {
		batch := newFrostVerificationBatcher(round, localPubKey, partySize)
		for i := range partySize {
			target, err := newFrostSignedEnvelope(
				remotePrivateKey,
				msgID,
				coord.vaultPubKey,
				coord.network,
				round,
				remotePubKey,
				uint32(i+1),
				fmt.Sprintf("%064x", i),
			)
			if err != nil {
				t.Fatalf("sign %s target %d: %v", round, i, err)
			}
			if err := coord.queueSignedTranscriptVerification(batch, target, peers); err != nil {
				t.Fatalf("queue %s verification %d: %v", round, i, err)
			}
			if fragmentedWindows {
				// A direct flush deterministically models one non-empty 100 ms
				// window; the timer-specific test separately proves the tick path.
				if err := coord.flushTranscriptVerifications(batch, peers); err != nil {
					t.Fatalf("flush %s verification window %d: %v", round, i, err)
				}
				drain(round)
			}
		}
		if err := coord.flushTranscriptVerifications(batch, peers); err != nil {
			t.Fatalf("flush final %s verification window: %v", round, err)
		}
		drain(round)
	}
	return traffic
}

func TestFrostKeysignVerificationBatchBoundaryTrafficShape(t *testing.T) {
	const (
		partySize                     = frostVerificationBatchMaxEntries
		keysignVerificationRounds     = 2
		nonVerificationBroadcastTasks = 3 // preprocess, share, and one terminal result
		// Independent wire-size anchors for signed entries and the base64 JSON
		// wrapper. After an intentional encoding change, run this test with -v,
		// review the measured totals, then update these constants and S02 sizing.
		wantCoalescedPayloadBytes         = 148_174
		wantCoalescedEncodedBytes  uint64 = 25_119_330
		wantFragmentedPayloadBytes        = 153_000
		wantFragmentedEncodedBytes uint64 = 29_563_568
		// bifrost/pkg/chainclients/monero/client.go holds sidecarSignMu from
		// before the sole production FrostKeySign call until it returns. XMR
		// keysign is therefore structurally serialized per node; generic signer
		// worker concurrency is not a valid multiplier for this traffic shape.
		localKeysignCeremonyConcurrency = 1
	)
	coalesced := measureFrostKeysignVerificationTraffic(t, partySize, false)
	fragmented := measureFrostKeysignVerificationTraffic(t, partySize, true)
	t.Logf("128-party verification-only: coalesced tasks=%d writer_launches=%d remote_writes=%d payload_bytes=%d network_bytes=%d; fragmented tasks=%d writer_launches=%d remote_writes=%d payload_bytes=%d network_bytes=%d", coalesced.broadcastTasks, coalesced.fanoutIDs, coalesced.remoteStreamWrites, coalesced.payloadBytes, coalesced.networkBytes, fragmented.broadcastTasks, fragmented.fanoutIDs, fragmented.remoteStreamWrites, fragmented.payloadBytes, fragmented.networkBytes)
	logicalVerifications := keysignVerificationRounds * partySize
	remotePeers := partySize - 1

	// Verification batching is measured independently from the round-payload
	// and result broadcasts. Coalescing is the normal shape; the fragmented
	// shape is the finite valid maximum when every logical verification lands
	// in its own non-empty 100 ms window. Batching does not reduce that maximum.
	if coalesced.broadcastTasks != keysignVerificationRounds || coalesced.logicalVerifications != logicalVerifications || coalesced.fanoutIDs != keysignVerificationRounds*partySize || coalesced.remoteStreamWrites != keysignVerificationRounds*remotePeers {
		t.Fatalf("coalesced verification tasks=%d entries=%d fanout IDs=%d remote writes=%d, want 2/256/256/254", coalesced.broadcastTasks, coalesced.logicalVerifications, coalesced.fanoutIDs, coalesced.remoteStreamWrites)
	}
	if fragmented.broadcastTasks != logicalVerifications || fragmented.logicalVerifications != logicalVerifications || fragmented.fanoutIDs != logicalVerifications*partySize || fragmented.remoteStreamWrites != logicalVerifications*remotePeers {
		t.Fatalf("fragmented verification tasks=%d entries=%d fanout IDs=%d remote writes=%d, want 256/256/32,768/32,512", fragmented.broadcastTasks, fragmented.logicalVerifications, fragmented.fanoutIDs, fragmented.remoteStreamWrites)
	}
	if coalesced.payloadBytes != wantCoalescedPayloadBytes || coalesced.networkBytes != wantCoalescedEncodedBytes {
		t.Fatalf("coalesced verification payload bytes=%d network bytes=%d, want %d/%d", coalesced.payloadBytes, coalesced.networkBytes, wantCoalescedPayloadBytes, wantCoalescedEncodedBytes)
	}
	if fragmented.payloadBytes != wantFragmentedPayloadBytes || fragmented.networkBytes != wantFragmentedEncodedBytes {
		t.Fatalf("fragmented verification payload bytes=%d network bytes=%d, want %d/%d", fragmented.payloadBytes, fragmented.networkBytes, wantFragmentedPayloadBytes, wantFragmentedEncodedBytes)
	}

	// S02 must size BroadcastMsgChan work for the verification tasks plus two
	// round payloads and one terminal result. Those three fixed broadcasts are
	// excluded from the verification-only encoded-byte fixture because their
	// payload sizes are protocol inputs with separate outer-frame bounds.
	coalescedCeremonyTasks := coalesced.broadcastTasks + nonVerificationBroadcastTasks
	fragmentedCeremonyTasks := fragmented.broadcastTasks + nonVerificationBroadcastTasks
	coalescedCeremonyWriterLaunches := coalesced.fanoutIDs + nonVerificationBroadcastTasks*partySize
	fragmentedCeremonyWriterLaunches := fragmented.fanoutIDs + nonVerificationBroadcastTasks*partySize
	coalescedCeremonyRemoteWrites := coalesced.remoteStreamWrites + nonVerificationBroadcastTasks*remotePeers
	fragmentedCeremonyRemoteWrites := fragmented.remoteStreamWrites + nonVerificationBroadcastTasks*remotePeers
	if coalescedCeremonyTasks != 5 || fragmentedCeremonyTasks != 259 {
		t.Fatalf("total ceremony tasks coalesced=%d fragmented=%d, want 5/259", coalescedCeremonyTasks, fragmentedCeremonyTasks)
	}
	if coalescedCeremonyWriterLaunches != 640 || fragmentedCeremonyWriterLaunches != 33_152 {
		t.Fatalf("total ceremony writer launches coalesced=%d fragmented=%d, want 640/33,152", coalescedCeremonyWriterLaunches, fragmentedCeremonyWriterLaunches)
	}
	if coalescedCeremonyRemoteWrites != 635 || fragmentedCeremonyRemoteWrites != 32_893 {
		t.Fatalf("total ceremony remote writes coalesced=%d fragmented=%d, want 635/32,893", coalescedCeremonyRemoteWrites, fragmentedCeremonyRemoteWrites)
	}

	// Full-network values are deterministic arithmetic over the supported
	// boundary, not measurements from a small integration network.
	if networkVerificationTasksCoalesced, networkVerificationTasksFragmented := partySize*coalesced.broadcastTasks, partySize*fragmented.broadcastTasks; networkVerificationTasksCoalesced != 256 || networkVerificationTasksFragmented != 32_768 {
		t.Fatalf("network verification tasks coalesced=%d fragmented=%d, want 256/32,768", networkVerificationTasksCoalesced, networkVerificationTasksFragmented)
	}
	if networkVerificationLaunchesCoalesced, networkVerificationLaunchesFragmented := partySize*coalesced.fanoutIDs, partySize*fragmented.fanoutIDs; networkVerificationLaunchesCoalesced != 32_768 || networkVerificationLaunchesFragmented != 4_194_304 {
		t.Fatalf("network verification writer launches coalesced=%d fragmented=%d, want 32,768/4,194,304", networkVerificationLaunchesCoalesced, networkVerificationLaunchesFragmented)
	}
	if networkVerificationWritesCoalesced, networkVerificationWritesFragmented := partySize*coalesced.remoteStreamWrites, partySize*fragmented.remoteStreamWrites; networkVerificationWritesCoalesced != 32_512 || networkVerificationWritesFragmented != 4_161_536 {
		t.Fatalf("network verification remote writes coalesced=%d fragmented=%d, want 32,512/4,161,536", networkVerificationWritesCoalesced, networkVerificationWritesFragmented)
	}
	if networkCeremonyTasksCoalesced, networkCeremonyTasksFragmented := partySize*coalescedCeremonyTasks, partySize*fragmentedCeremonyTasks; networkCeremonyTasksCoalesced != 640 || networkCeremonyTasksFragmented != 33_152 {
		t.Fatalf("network ceremony tasks coalesced=%d fragmented=%d, want 640/33,152", networkCeremonyTasksCoalesced, networkCeremonyTasksFragmented)
	}
	if networkCeremonyLaunchesCoalesced, networkCeremonyLaunchesFragmented := partySize*coalescedCeremonyWriterLaunches, partySize*fragmentedCeremonyWriterLaunches; networkCeremonyLaunchesCoalesced != 81_920 || networkCeremonyLaunchesFragmented != 4_243_456 {
		t.Fatalf("network ceremony writer launches coalesced=%d fragmented=%d, want 81,920/4,243,456", networkCeremonyLaunchesCoalesced, networkCeremonyLaunchesFragmented)
	}
	if networkCeremonyWritesCoalesced, networkCeremonyWritesFragmented := partySize*coalescedCeremonyRemoteWrites, partySize*fragmentedCeremonyRemoteWrites; networkCeremonyWritesCoalesced != 81_280 || networkCeremonyWritesFragmented != 4_210_304 {
		t.Fatalf("network ceremony remote writes coalesced=%d fragmented=%d, want 81,280/4,210,304", networkCeremonyWritesCoalesced, networkCeremonyWritesFragmented)
	}
	if networkEncodedBytesCoalesced := uint64(partySize) * coalesced.networkBytes; networkEncodedBytesCoalesced != 3_215_274_240 {
		t.Fatalf("network coalesced verification encoded bytes=%d, want 3,215,274,240", networkEncodedBytesCoalesced)
	}
	if networkEncodedBytesFragmented := uint64(partySize) * fragmented.networkBytes; networkEncodedBytesFragmented != 3_784_136_704 {
		t.Fatalf("network fragmented verification encoded bytes=%d, want 3,784,136,704", networkEncodedBytesFragmented)
	}

	t.Logf("128-party total ceremony: coalesced tasks=%d writer_launches=%d remote_writes=%d; fragmented tasks=%d writer_launches=%d remote_writes=%d; node-wide keysign multiplier=%d", coalescedCeremonyTasks, coalescedCeremonyWriterLaunches, coalescedCeremonyRemoteWrites, fragmentedCeremonyTasks, fragmentedCeremonyWriterLaunches, fragmentedCeremonyRemoteWrites, localKeysignCeremonyConcurrency)
}

func TestFrostKeysignVerificationBatchSplitsAtEntryBound(t *testing.T) {
	coord := newTestFrostSignCoordinator(testPubKeys[0], "keysign-split", newFrostSignInbox(), time.Second)
	batch := newFrostVerificationBatcher(
		frostRoundShare,
		testPubKeys[0],
		frostVerificationBatchMaxEntries+1,
	)
	for i := 0; i < frostVerificationBatchMaxEntries+1; i++ {
		if err := coord.queueSignedTranscriptVerification(batch, messages.FrostSignedEnvelope{
			Round:            frostRoundShare,
			SenderPubKey:     fmt.Sprintf("target-%03d", i),
			ParticipantIndex: uint32(i + 1),
			PayloadHash:      fmt.Sprintf("%064x", i),
		}, []peer.ID{"remote"}); err != nil {
			t.Fatalf("queue keysign verification %d: %v", i, err)
		}
	}

	for i, want := range []int{frostVerificationBatchMaxEntries, 1} {
		queued := receiveFrostKeysignBroadcast(t, coord.p2pCommunication.BroadcastMsgChan)
		entries, err := decodeFrostVerificationBatch(queued.WrappedMessage.Payload)
		if err != nil {
			t.Fatalf("decode keysign batch %d: %v", i, err)
		}
		if len(entries) != want {
			t.Fatalf("keysign batch %d entries=%d, want %d", i, len(entries), want)
		}
	}
}

func TestFrostKeysignVerificationBatchRejectsMalformedAndOversizedInput(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, _ := testFrostSignParticipants(t, members)
	entry := FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundShare,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		PayloadHash:            testHashOf('a'),
	}
	tooMany := make([]FrostVerifyPayload, frostVerificationBatchMaxEntries+1)
	for i := range tooMany {
		tooMany[i] = entry
	}
	mixed := entry
	mixed.Round = frostRoundPreprocess

	tests := []struct {
		name    string
		payload any
	}{
		{name: "legacy single echo", payload: map[string]any{
			"sender_pub_key":           entry.SenderPubKey,
			"round":                    entry.Round,
			"target_sender_pub_key":    entry.TargetSenderPubKey,
			"target_participant_index": entry.TargetParticipantIndex,
			"payload_hash":             entry.PayloadHash,
		}},
		{name: "oversized", payload: FrostVerifyBatchPayload{Verifications: tooMany}},
		{name: "mixed round", payload: FrostVerifyBatchPayload{Verifications: []FrostVerifyPayload{entry, mixed}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inbox := newFrostSignInbox()
			coord := newTestFrostSignCoordinator(testPubKeys[0], "invalid-batch", inbox, time.Second)
			initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
			enqueueKeySignMessage(t, inbox, messages.TSSFrostVerifyMsg, "invalid-batch", testPubKeys[1], tc.payload)
			if response := coord.routeVerificationMessage(<-inbox.verify, frostRoundPreprocess); response != nil {
				t.Fatalf("invalid batch became terminal: %+v", response)
			}
			if len(coord.verificationEntriesByPeer) != 0 || len(coord.roundState.deferredVerifications) != 0 {
				t.Fatal("invalid batch changed accounting or deferred state")
			}
			for round, verifier := range coord.roundState.verifiers {
				if len(verifier.votesByTarget) != 0 || len(verifier.signedByTarget) != 0 {
					t.Fatalf("invalid batch changed %s verifier state", round)
				}
			}
		})
	}
}

func TestFrostKeysignVerificationBatchAccountsEveryEntry(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, _ := testFrostSignParticipants(t, members)
	inbox := newFrostSignInbox()
	coord := newTestFrostSignCoordinator(testPubKeys[0], "entry-limit", inbox, time.Second)
	coord.verificationEntryLimit = 2
	initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
	entry := FrostVerifyPayload{
		SenderPubKey:           testPubKeys[1],
		Round:                  frostRoundShare,
		TargetSenderPubKey:     testPubKeys[0],
		TargetParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
		PayloadHash:            testHashOf('a'),
	}
	enqueueKeySignMessage(t, inbox, messages.TSSFrostVerifyMsg, "entry-limit", testPubKeys[1], FrostVerifyBatchPayload{
		Verifications: []FrostVerifyPayload{entry, entry, entry},
	})
	if response := coord.routeVerificationMessage(<-inbox.verify, frostRoundPreprocess); response != nil {
		t.Fatalf("entry limit changed keysign failure semantics: %+v", response)
	}
	peerID, err := conversion.GetPeerIDFromPubKey(testPubKeys[1])
	if err != nil {
		t.Fatalf("peer ID: %v", err)
	}
	if got := coord.verificationEntriesByPeer[peerID]; got != 2 {
		t.Fatalf("accounted logical entries=%d, want 2", got)
	}
	shareVerifier, err := coord.roundState.verifier(frostRoundShare)
	if err != nil {
		t.Fatalf("share verifier: %v", err)
	}
	if got := len(shareVerifier.votesByTarget); got != 0 {
		t.Fatalf("future entries were applied before share activation: targets=%d", got)
	}
	if got := len(coord.roundState.deferredVerifications[frostRoundShare]); got != 2 {
		t.Fatalf("accounted future entries retained=%d, want 2", got)
	}
	if response := coord.activateDeferredVerifications(frostRoundShare); response != nil {
		t.Fatalf("activate accounted entries: %+v", response)
	}
	targetVotes := shareVerifier.votesByTarget[normalizePubKey(testPubKeys[0])]
	if got := len(targetVotes); got != 1 {
		t.Fatalf("duplicate entries retained votes=%d, want 1", got)
	}
}

func TestFrostKeysignVerificationBatchFlushesOnTickAndRoundExit(t *testing.T) {
	ensureBech32Prefix()
	members := []string{testPubKeys[0], testPubKeys[1]}
	activeSet, participantIndexMap, peerIDs := testFrostSignParticipants(t, members)

	t.Run("tick", func(t *testing.T) {
		inbox := newFrostSignInbox()
		coord := newTestFrostSignCoordinator(testPubKeys[0], "keysign-tick", inbox, 20*frostVerificationBatchInterval)
		initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
		localPayload := FrostPreprocessPayload{
			SenderPubKey:     testPubKeys[0],
			ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
			PreprocessHex:    "local-preprocess",
		}
		remotePayload := FrostPreprocessPayload{
			SenderPubKey:     testPubKeys[1],
			ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[1])],
			PreprocessHex:    "remote-preprocess",
		}
		localHash, err := FrostTranscriptPreprocessHash("keysign-tick", localPayload)
		if err != nil {
			t.Fatalf("local hash: %v", err)
		}
		remoteHash, err := FrostTranscriptPreprocessHash("keysign-tick", remotePayload)
		if err != nil {
			t.Fatalf("remote hash: %v", err)
		}
		done := make(chan *FrostKeySignResponse, 1)
		go func() {
			_, failure := coord.exchangePreprocess(activeSet, participantIndexMap, peerIDs, localPayload)
			done <- failure
		}()

		if queued := receiveFrostKeysignBroadcast(t, coord.p2pCommunication.BroadcastMsgChan); queued.WrappedMessage.MessageType != messages.TSSFrostPreprocessMsg {
			t.Fatalf("first broadcast type=%s, want preprocess", queued.WrappedMessage.MessageType)
		}
		select {
		case queued := <-coord.p2pCommunication.BroadcastMsgChan:
			entries, err := decodeFrostVerificationBatch(queued.WrappedMessage.Payload)
			if queued.WrappedMessage.MessageType != messages.TSSFrostVerifyMsg || err != nil || len(entries) != 1 {
				t.Fatalf("tick batch type=%s entries=%d err=%v", queued.WrappedMessage.MessageType, len(entries), err)
			}
		case <-time.After(10 * frostVerificationBatchInterval):
			t.Fatal("pending keysign verification was not flushed on tick")
		}

		enqueueKeySignMessage(t, inbox, messages.TSSFrostPreprocessMsg, "keysign-tick", testPubKeys[1], remotePayload)
		for _, verification := range []FrostVerifyPayload{
			{
				SenderPubKey:           testPubKeys[1],
				Round:                  frostRoundPreprocess,
				TargetSenderPubKey:     testPubKeys[1],
				TargetParticipantIndex: remotePayload.ParticipantIndex,
				PayloadHash:            remoteHash,
			},
			{
				SenderPubKey:           testPubKeys[1],
				Round:                  frostRoundPreprocess,
				TargetSenderPubKey:     testPubKeys[0],
				TargetParticipantIndex: localPayload.ParticipantIndex,
				PayloadHash:            localHash,
			},
		} {
			enqueueKeySignMessage(t, inbox, messages.TSSFrostVerifyMsg, "keysign-tick", testPubKeys[1], verification)
		}
		select {
		case failure := <-done:
			if failure != nil {
				t.Fatalf("complete preprocess after tick: %+v", failure)
			}
		case <-time.After(10 * frostVerificationBatchInterval):
			t.Fatal("preprocess did not complete after transcript quorum")
		}
	})

	t.Run("timeout round exit final flush", func(t *testing.T) {
		coord := newTestFrostSignCoordinator(testPubKeys[0], "keysign-round-exit", newFrostSignInbox(), 5*time.Millisecond)
		initTestFrostSignRoundState(t, coord, activeSet, participantIndexMap)
		if _, failure := coord.exchangePreprocess(activeSet, participantIndexMap, peerIDs, FrostPreprocessPayload{
			SenderPubKey:     testPubKeys[0],
			ParticipantIndex: participantIndexMap[normalizePubKey(testPubKeys[0])],
			PreprocessHex:    "local-preprocess",
		}); failure == nil {
			t.Fatal("expected preprocess timeout")
		}
		verificationEnvelopes := 0
		for len(coord.p2pCommunication.BroadcastMsgChan) > 0 {
			queued := <-coord.p2pCommunication.BroadcastMsgChan
			if queued.WrappedMessage.MessageType == messages.TSSFrostVerifyMsg {
				verificationEnvelopes++
			}
		}
		if verificationEnvelopes != 1 {
			t.Fatalf("verification envelopes after round exit=%d, want 1", verificationEnvelopes)
		}
	})

	t.Run("final flush is idempotent", func(t *testing.T) {
		coord := newTestFrostSignCoordinator(testPubKeys[0], "keysign-final", newFrostSignInbox(), time.Second)
		if err := coord.queueSignedTranscriptVerification(nil, messages.FrostSignedEnvelope{}, nil); err == nil {
			t.Fatal("nil keysign verification batch did not return an error")
		}
		batch := newFrostVerificationBatcher(frostRoundShare, testPubKeys[0], 2)
		if err := coord.queueSignedTranscriptVerification(batch, messages.FrostSignedEnvelope{
			Round:            frostRoundShare,
			SenderPubKey:     testPubKeys[0],
			ParticipantIndex: 1,
			PayloadHash:      testHashOf('c'),
		}, []peer.ID{"remote"}); err != nil {
			t.Fatalf("queue final verification: %v", err)
		}
		if err := coord.flushTranscriptVerifications(batch, []peer.ID{"remote"}); err != nil {
			t.Fatalf("final flush: %v", err)
		}
		if err := coord.flushTranscriptVerifications(batch, []peer.ID{"remote"}); err != nil {
			t.Fatalf("repeated final flush: %v", err)
		}
		if got := len(coord.p2pCommunication.BroadcastMsgChan); got != 1 {
			t.Fatalf("idempotent final flush envelopes=%d, want 1", got)
		}
	})
}

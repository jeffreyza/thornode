package tss

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

const (
	// keysignLeaderRotationVersion gates the GG20/ECDSA keysign leader rotation +
	// no-blame-on-sync-failure behavior. The version flows from the THORChain network
	// consensus version (GetThorchainVersion), which is the lowest version among active
	// validators, so the whole fleet flips to the new party-formation protocol atomically
	// once the last active node runs this release — a rotating node derives a distinct
	// per-attempt join id and cannot form a party with a pre-rotation peer, so an atomic,
	// consensus-synchronized switch is mandatory. The gate must therefore be the first
	// release that ships this code: a lower gate would switch nodes on one by one as they
	// upgrade from a release that already meets it. Mirrors frostJoinRotationVersion.
	keysignLeaderRotationVersion = "3.21.2"
	// keysignJoinAttemptDomain separates GG20 join-attempt digests from the FROST ones so
	// the two protocols can never collide on a shared msgID.
	keysignJoinAttemptDomain = "thorchain-keysign-join-attempt-v1"
	// keysignSignatureWaitSlack pads the rotated signature wait for start skew between
	// nodes, matching the slack FROST adds to its result wait (KeySignTimeout+5s).
	keysignSignatureWaitSlack = 5 * time.Second
)

// keysignLeaderRotationEnabled reports whether the gated GG20 keysign leader-rotation /
// no-blame protocol is active for this request version.
func keysignLeaderRotationEnabled(version string) (bool, error) {
	old, err := conversion.VersionLTCheck(strings.TrimSpace(version), keysignLeaderRotationVersion)
	if err != nil {
		return false, fmt.Errorf("fail to parse keysign leader rotation version: %w", err)
	}
	return !old, nil
}

// keysignSignatureWaitTimeout bounds how long a node waits for a peer's keysign signature.
// With leader rotation a party can form on any of frostMaxJoinLeaderAttempts join attempts,
// each bounded by the party timeout, before the signing ceremony itself starts. The wait
// adds those attempts and keysignSignatureWaitSlack to the keysign timeout, so a signature
// from a party formed on a later attempt, or by a peer that started a little later, still
// arrives while this node listens and can abort its own remaining attempts. It is a
// best-effort budget, not a hard bound: the keysign timeout is an idle timeout, so a
// ceremony that keeps exchanging messages can run past it.
func (t *TssServer) keysignSignatureWaitTimeout(version string) time.Duration {
	if enabled, err := keysignLeaderRotationEnabled(version); err == nil && enabled {
		return t.conf.KeySignTimeout + keysignSignatureWaitSlack + frostMaxJoinLeaderAttempts*t.conf.PartyTimeout
	}
	return t.conf.KeySignTimeout
}

// keysignJoinAttemptID derives a unique, ephemeral join-party session id for one GG20
// keysign leader-rotation attempt. It reuses p2p.FrostJoinAttemptIDPrefix — the generic
// ephemeral-retryable-join marker the coordinator special-cases (an early/stood-down request
// is reset, not parked) — and a distinct domain so GG20 digests are disjoint from FROST's.
func keysignJoinAttemptID(msgID, leader string, eligiblePeers []string, attempt int) (string, error) {
	msgID = strings.TrimSpace(msgID)
	leader = strings.TrimSpace(leader)
	if msgID == "" || leader == "" || len(eligiblePeers) == 0 || attempt < 0 {
		return "", errors.New("invalid keysign join attempt context")
	}
	peerSet := make([]string, 0, len(eligiblePeers))
	seen := make(map[string]struct{}, len(eligiblePeers))
	for _, pid := range eligiblePeers {
		pid = strings.TrimSpace(pid)
		if pid == "" {
			return "", errors.New("invalid keysign join attempt peer")
		}
		if _, ok := seen[pid]; ok {
			return "", errors.New("duplicate keysign join attempt peer")
		}
		seen[pid] = struct{}{}
		peerSet = append(peerSet, pid)
	}
	sort.Strings(peerSet)
	canonical := strings.Join([]string{
		"domain=" + keysignJoinAttemptDomain,
		"msg_id=" + msgID,
		"eligible_peers=" + strings.Join(peerSet, ","),
		"leader=" + leader,
		"attempt=" + strconv.Itoa(attempt),
	}, "\n")
	digest, err := tcommon.MsgToHashString([]byte(canonical))
	if err != nil {
		return "", err
	}
	return p2p.FrostJoinAttemptIDPrefix + digest, nil
}

// joinKeySignParty forms the GG20/ECDSA keysign party. Pre-gate it falls back to the legacy
// single-fixed-leader joinParty unchanged. Post-gate it rotates the deterministic leader per
// attempt (p2p.LeaderNodes) with a distinct ephemeral join id each time, so a single
// unavailable/overloaded coordinator no longer dooms every retry of a pinned txout to the
// same leader — and it emits NO blame between attempts (a join timeout is not signed evidence
// against any node). Mirrors joinFrostKeySignParty; GG20 keysign has no pre-share state, so
// rotating a coordinator cannot create a second signing attempt. Attempts are capped at
// frostMaxJoinLeaderAttempts; keysignSignatureWaitTimeout covers that whole join budget.
// Quorum-only validation (threshold+1) — unlike FROST the GG20 leader need not be a signer,
// so there is no leader-presence requirement.
func (t *TssServer) joinKeySignParty(
	msgID, version string,
	blockHeight int64,
	participants []string,
	threshold int,
	sigChan chan string,
) ([]peer.ID, string, error) {
	enabled, err := keysignLeaderRotationEnabled(version)
	if err != nil {
		return nil, "", err
	}
	if !enabled {
		return t.joinParty(msgID, version, blockHeight, participants, threshold, sigChan)
	}
	if len(participants) == 0 {
		return nil, "", errors.New("no participants can be found")
	}
	eligiblePeers, err := conversion.GetPeerIDsFromPubKeys(participants)
	if err != nil {
		return nil, "", fmt.Errorf("fail to resolve keysign join peers: %w", err)
	}
	peerStrings := make([]string, len(eligiblePeers))
	for i, el := range eligiblePeers {
		peerStrings[i] = el.String()
	}
	leaders, err := p2p.LeaderNodes(msgID, blockHeight, peerStrings)
	if err != nil {
		return nil, "", fmt.Errorf("fail to order keysign join leaders: %w", err)
	}
	attempts := len(leaders)
	if attempts > frostMaxJoinLeaderAttempts {
		attempts = frostMaxJoinLeaderAttempts
	}

	var lastErr error
	var lastLeader string
	for attempt := 0; attempt < attempts; attempt++ {
		leader := leaders[attempt]
		joinID, idErr := keysignJoinAttemptID(msgID, leader, peerStrings, attempt)
		if idErr != nil {
			return nil, leader, idErr
		}
		onlinePeers, selectedLeader, joinErr := t.partyCoordinator.JoinPartyWithSelectedLeader(
			joinID, leader, peerStrings, threshold, sigChan,
		)
		t.partyCoordinator.ReleaseStream(joinID)
		// A signature arriving from another party aborts formation immediately; propagate it
		// so the caller returns the received signature rather than rotating.
		if errors.Is(joinErr, p2p.ErrSignReceived) {
			return onlinePeers, selectedLeader, joinErr
		}
		if joinErr == nil && len(onlinePeers) < threshold+1 {
			joinErr = fmt.Errorf("formed keysign party has %d peers; need %d", len(onlinePeers), threshold+1)
		}
		if joinErr == nil {
			return onlinePeers, selectedLeader, nil
		}
		lastErr = joinErr
		lastLeader = selectedLeader
		t.logger.Warn().
			Err(joinErr).
			Str("msg_id", msgID).
			Str("leader", selectedLeader).
			Int("attempt", attempt+1).
			Int("max_attempts", attempts).
			Msg("keysign join attempt failed; rotating coordinator without blame")
	}
	return nil, lastLeader, fmt.Errorf("all %d keysign join attempts failed: %w", attempts, lastErr)
}

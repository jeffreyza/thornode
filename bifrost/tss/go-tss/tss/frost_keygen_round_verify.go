package tss

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

const frostKeygenTranscriptDomain = "thorchain-frost-keygen-round-transcript-v1"

// frostKeygenTranscriptVote is the homogeneous keygen-only verification wire
// format. The authenticated libp2p stream identifies the attester; one vote
// confirms the complete canonical transcript for one DKG round.
type frostKeygenTranscriptVote struct {
	Round          string `json:"round"`
	TranscriptHash string `json:"transcript_hash"`
}

type frostKeygenTranscriptEntry struct {
	participantIndex uint32
	payloadHash      string
}

// frostKeygenRoundVerifier reuses the established payload identity and
// equivocation checks while replacing the per-target vote matrix with one vote
// per complete round transcript.
type frostKeygenRoundVerifier struct {
	*frostTranscriptVerifier
	transcriptHash        string
	votesByAttester       map[string]string
	equivocatingAttesters map[string]struct{}
	localVoteSent         bool
}

// frostKeygenVerificationGaps partitions incomplete transcript confirmation
// into disjoint causes. A single conflicting vote is not attributable because
// this node may hold the divergent transcript; two authenticated votes from
// one attester are attributable equivocation and remain blameable.
type frostKeygenVerificationGaps struct {
	missingPubKeys      []string
	conflictingPubKeys  []string
	equivocatingPubKeys []string
}

func newFrostKeygenRoundVerifier(
	round string,
	activeSet *activeSignerSet,
	participantIndexMap map[string]uint32,
) (*frostKeygenRoundVerifier, error) {
	payloadVerifier, err := newFrostTranscriptVerifier(round, activeSet, participantIndexMap)
	if err != nil {
		return nil, err
	}
	return &frostKeygenRoundVerifier{
		frostTranscriptVerifier: payloadVerifier,
		votesByAttester:         make(map[string]string, len(activeSet.originalByNormalized)),
		equivocatingAttesters:   make(map[string]struct{}, len(activeSet.originalByNormalized)),
	}, nil
}

// prepareTranscriptHash fixes the local digest once every authenticated round
// payload is present. Participant indexes provide canonical order, while msgID
// binds the deterministic member set, vault label, height, and expected address.
func (v *frostKeygenRoundVerifier) prepareTranscriptHash(msgID string) (string, bool, error) {
	if v == nil || v.frostTranscriptVerifier == nil {
		return "", false, fmt.Errorf("frost keygen round verifier is not initialized")
	}
	if v.transcriptHash != "" {
		return v.transcriptHash, true, nil
	}
	if len(v.payloadHashByTarget) != v.totalParticipants {
		return "", false, nil
	}

	entries := make([]frostKeygenTranscriptEntry, 0, v.totalParticipants)
	for targetNorm, payloadHash := range v.payloadHashByTarget {
		participantIndex, ok := v.participantIndexMap[targetNorm]
		if !ok {
			return "", false, fmt.Errorf("transcript target is missing a participant index")
		}
		entries = append(entries, frostKeygenTranscriptEntry{
			participantIndex: participantIndex,
			payloadHash:      payloadHash,
		})
	}

	transcriptHash, err := frostKeygenTranscriptHash(msgID, v.round, entries)
	if err != nil {
		return "", false, err
	}
	v.transcriptHash = transcriptHash
	return transcriptHash, true, nil
}

func frostKeygenTranscriptHash(msgID, round string, entries []frostKeygenTranscriptEntry) (string, error) {
	msgID = strings.TrimSpace(msgID)
	round = strings.TrimSpace(round)
	if msgID == "" {
		return "", fmt.Errorf("frost keygen transcript msgID is required")
	}
	if frostKeygenRoundOrder(round) == 0 {
		return "", fmt.Errorf("unsupported frost keygen transcript round %q", round)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("frost keygen transcript is empty")
	}

	canonical := append([]frostKeygenTranscriptEntry(nil), entries...)
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].participantIndex < canonical[j].participantIndex
	})
	var builder strings.Builder
	builder.WriteString(frostKeygenTranscriptDomain)
	builder.WriteString("\nmessage_id=")
	builder.WriteString(msgID)
	builder.WriteString("\nround=")
	builder.WriteString(round)
	builder.WriteString("\nparticipants=")
	builder.WriteString(strconv.Itoa(len(canonical)))
	for i, entry := range canonical {
		if entry.participantIndex == 0 {
			return "", fmt.Errorf("frost keygen transcript participant index is zero")
		}
		if i > 0 && canonical[i-1].participantIndex == entry.participantIndex {
			return "", fmt.Errorf("frost keygen transcript contains duplicate participant index %d", entry.participantIndex)
		}
		payloadHash, err := normalizeTranscriptHash(entry.payloadHash)
		if err != nil {
			return "", fmt.Errorf("invalid frost keygen transcript payload hash: %w", err)
		}
		builder.WriteString("\nparticipant_index=")
		builder.WriteString(strconv.FormatUint(uint64(entry.participantIndex), 10))
		builder.WriteString("\npayload_hash=")
		builder.WriteString(payloadHash)
	}
	return common.MsgToHashString([]byte(builder.String()))
}

func decodeFrostKeygenTranscriptVote(payload []byte) (frostKeygenTranscriptVote, error) {
	var vote frostKeygenTranscriptVote
	if err := json.Unmarshal(payload, &vote); err != nil {
		return frostKeygenTranscriptVote{}, err
	}
	vote.Round = strings.TrimSpace(vote.Round)
	if frostKeygenRoundOrder(vote.Round) == 0 {
		return frostKeygenTranscriptVote{}, fmt.Errorf("unsupported frost keygen transcript round %q", vote.Round)
	}
	transcriptHash, err := normalizeTranscriptHash(vote.TranscriptHash)
	if err != nil {
		return frostKeygenTranscriptVote{}, fmt.Errorf("invalid frost keygen transcript hash: %w", err)
	}
	vote.TranscriptHash = transcriptHash
	return vote, nil
}

func (v *frostKeygenRoundVerifier) recordTranscriptVote(
	attesterPubKey string,
	transcriptHash string,
) (bool, *frostTranscriptFailure) {
	attesterNorm, attesterOriginal, ok := v.resolveActivePubKey(attesterPubKey)
	if !ok {
		return false, newFrostTranscriptFailure(
			"verification attester is not in active participant set",
			[]string{attesterPubKey},
		)
	}
	normalizedHash, err := normalizeTranscriptHash(transcriptHash)
	if err != nil {
		return false, newFrostTranscriptFailure("verification transcript hash is invalid", []string{attesterOriginal})
	}
	if existing, exists := v.votesByAttester[attesterNorm]; exists {
		if existing == normalizedHash {
			return false, nil
		}
		delete(v.votesByAttester, attesterNorm)
		v.equivocatingAttesters[attesterNorm] = struct{}{}
		return false, nil
	}
	if _, disqualified := v.equivocatingAttesters[attesterNorm]; disqualified {
		return false, nil
	}
	v.votesByAttester[attesterNorm] = normalizedHash
	return true, nil
}

func (v *frostKeygenRoundVerifier) isComplete() bool {
	if v == nil || v.transcriptHash == "" {
		return false
	}
	matching := 0
	for _, transcriptHash := range v.votesByAttester {
		if transcriptHash == v.transcriptHash {
			matching++
		}
	}
	return matching >= v.confirmationQuorum
}

func (v *frostKeygenRoundVerifier) verificationGaps() frostKeygenVerificationGaps {
	if v == nil {
		return frostKeygenVerificationGaps{}
	}

	gaps := frostKeygenVerificationGaps{}
	for attesterNorm, attesterOriginal := range v.activeSet.originalByNormalized {
		if _, equivocating := v.equivocatingAttesters[attesterNorm]; equivocating {
			gaps.equivocatingPubKeys = append(gaps.equivocatingPubKeys, attesterOriginal)
			continue
		}
		// Until the local transcript is complete, a single vote cannot be
		// classified as missing, matching, or conflicting. Equivocation above
		// remains independently provable from two authenticated votes.
		if v.transcriptHash == "" {
			continue
		}
		vote, present := v.votesByAttester[attesterNorm]
		switch {
		case !present:
			gaps.missingPubKeys = append(gaps.missingPubKeys, attesterOriginal)
		case vote != v.transcriptHash:
			gaps.conflictingPubKeys = append(gaps.conflictingPubKeys, attesterOriginal)
		}
	}
	sort.Strings(gaps.missingPubKeys)
	sort.Strings(gaps.conflictingPubKeys)
	sort.Strings(gaps.equivocatingPubKeys)
	return gaps
}

// frostKeygenTranscriptTimeoutReason adds keygen-only diagnostics without
// changing the shared keysign timeout wording. Conflicting votes identify a
// transcript disagreement, while equivocation identifies attributable fault.
func frostKeygenTranscriptTimeoutReason(
	payloadLabel string,
	missingPayloads []string,
	gaps frostKeygenVerificationGaps,
) string {
	reason := transcriptTimeoutReason(payloadLabel, missingPayloads, gaps.missingPubKeys)
	if len(gaps.conflictingPubKeys) > 0 {
		reason += "; local transcript disagrees with peer verification votes"
	}
	if len(gaps.equivocatingPubKeys) > 0 {
		reason += "; peer verification vote equivocation detected"
	}
	return reason
}

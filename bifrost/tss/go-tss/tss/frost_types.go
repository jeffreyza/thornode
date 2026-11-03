package tss

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// FrostFailReasonXMRMaxGasExceeded is intentionally a stable machine-readable
// prefix so Bifrost can recognize the local fee-cap failure without depending
// on the full human-readable blame text.
const FrostFailReasonXMRMaxGasExceeded = "xmr_max_gas_exceeded"

// FrostKeySignRequest is the canonical request context for a FROST signing ceremony.
type FrostKeySignRequest struct {
	VaultPubKey   string
	Network       string
	ToAddress     string
	AmountAtomic  uint64
	Memo          string
	FeeRate       uint64
	SignerPubKeys []string
	// ActiveSignerPubKeys is the keysign-eligible subset invited to the FROST
	// ceremony. SignerPubKeys remains the full vault membership so participant
	// indexes and the original threshold stay stable.
	ActiveSignerPubKeys []string
	BlockHeight         int64
	Version             string
	TxInHash            string
	RequestID           string
	RequiredInputs      []FrostSpentOutputRef
	MaxInputs           int64
	DustThreshold       uint64
	// MaxSelectedInputValueAtomic caps the total value of selected inputs for
	// management transactions such as XMR consolidation.
	MaxSelectedInputValueAtomic uint64
	// MaxGasAtomic is the scheduled THORChain MaxGas cap converted to Monero
	// atomic units. The sidecar uses it to bound the fee and, for migrations,
	// sweep unused gas into the destination vault.
	MaxGasAtomic uint64
	// ReserveSigningClaim is a local durability barrier excluded from canonical
	// request hashing. It runs after party selection and before sidecar signing.
	ReserveSigningClaim func() error `json:"-"`
	// ReleaseSigningClaim removes the exact reservation only when local signing
	// failed before a signature share could have escaped.
	ReleaseSigningClaim func() error `json:"-"`
}

// FrostKeySignResponse is the result of a FROST signing ceremony.
type FrostKeySignResponse struct {
	TxHash          string
	TxHex           string
	TxKey           string
	FeeAtomic       uint64
	MsgID           string
	Eventuality     FrostEventuality
	SpentOutputRefs []FrostSpentOutputRef
	Status          tcommon.Status
	Blame           blame.Blame
	// releaseSigningClaim is local coordinator state, never result gossip.
	releaseSigningClaim bool
}

// FrostEventuality is the sidecar-verified commitment used to prove the same
// transaction plan produced the final FROST-signed transaction.
type FrostEventuality struct {
	Version       uint8  `json:"version"`
	PlanHash      string `json:"plan_hash"`
	TxHexHash     string `json:"tx_hex_hash"`
	TxKeyHash     string `json:"tx_key_hash"`
	EventualityID string `json:"eventuality_id"`
}

// FrostSidecarClient is the local sidecar API used by the coordinator.
type FrostSidecarClient interface {
	FrostSignPrepare(req FrostSidecarPrepareReq) (FrostSidecarPrepareResp, error)
	FrostSignPartial(req FrostSidecarPartialReq) (FrostSidecarPartialResp, error)
	FrostSignCombine(req FrostSidecarCombineReq) (FrostSidecarCombineResp, error)
}

// FrostKeygenRequest is the canonical request context for a FROST DKG ceremony.
type FrostKeygenRequest struct {
	VaultPubKey   string
	SignerPubKeys []string
	BlockHeight   int64
	ExpectedAddr  string
}

// FrostKeygenResponse is the result of a FROST DKG ceremony.
type FrostKeygenResponse struct {
	Address              string
	GroupKey             string
	KeyBytesHex          string
	ObservedMoneroHeight int64
	Status               tcommon.Status
	Blame                blame.Blame
}

// FrostSidecarKeygenClient is the local sidecar API used by the DKG coordinator.
type FrostSidecarKeygenClient interface {
	FrostDKGRound1(req FrostSidecarDKGRound1Req) (FrostSidecarDKGRound1Resp, error)
	FrostDKGRound2(req FrostSidecarDKGRound2Req) (FrostSidecarDKGRound2Resp, error)
	FrostDKGRound3(req FrostSidecarDKGRound3Req) (FrostSidecarDKGRound3Resp, error)
	FrostDKGFinalize(req FrostSidecarDKGFinalizeReq) (FrostSidecarDKGFinalizeResp, error)
	GetChainHeight() (int64, error)
}

type FrostSidecarDKGRound1Req struct {
	VaultPubKey      string `json:"vault_pub_key"`
	Threshold        uint32 `json:"threshold"`
	N                uint32 `json:"n"`
	ParticipantIndex uint32 `json:"participant_index"`
	Context          string `json:"context,omitempty"`
	SessionID        string `json:"session_id"`
}

type FrostSidecarDKGRound1Resp struct {
	Commitments string `json:"commitments"`
}

type FrostSidecarDKGRound2Req struct {
	SessionID      string            `json:"session_id"`
	AllCommitments map[string]string `json:"all_commitments"`
}

type FrostSidecarDKGRound2Resp struct {
	EncryptedShares map[string]string `json:"encrypted_shares"`
}

type FrostSidecarDKGRound3Req struct {
	SessionID string            `json:"session_id"`
	AllShares map[string]string `json:"all_shares"`
}

type FrostSidecarDKGRound3Resp struct {
	Address  string `json:"address"`
	GroupKey string `json:"group_key"`
}

type FrostSidecarDKGFinalizeReq struct {
	SessionID string `json:"session_id"`
	GroupKey  string `json:"group_key"`
	Address   string `json:"address"`
}

type FrostSidecarDKGFinalizeResp struct {
	Success     bool   `json:"success"`
	KeyBytesHex string `json:"key_bytes_hex"`
}

type FrostSidecarPrepareReq struct {
	MsgID                       string                `json:"msg_id"`
	VaultPubKey                 string                `json:"vault_pub_key"`
	ToAddress                   string                `json:"to_address"`
	AmountAtomic                uint64                `json:"amount_atomic"`
	Memo                        string                `json:"memo,omitempty"`
	FeeRate                     uint64                `json:"fee_rate,omitempty"`
	DecoyContext                string                `json:"decoy_context"`
	RequiredInputs              []FrostSpentOutputRef `json:"required_inputs,omitempty"`
	MaxInputs                   int64                 `json:"max_inputs,omitempty"`
	DustThreshold               uint64                `json:"dust_threshold,omitempty"`
	MaxSelectedInputValueAtomic uint64                `json:"max_selected_input_value_atomic,omitempty"`
	// MaxGasAtomic keeps the Rust signer bound to THORChain's scheduled gas
	// cap while staying optional for older 3.19-compatible sidecar payloads.
	MaxGasAtomic uint64 `json:"max_gas_atomic,omitempty"`
}

type FrostSidecarPrepareResp struct {
	SessionID      string                `json:"session_id"`
	Preprocess     string                `json:"preprocess"`
	SelectedInputs []FrostSpentOutputRef `json:"selected_inputs"`
}

type FrostSidecarPartialReq struct {
	MsgID           string            `json:"msg_id"`
	SessionID       string            `json:"session_id"`
	AllPreprocesses map[string]string `json:"all_preprocesses"`
}

type FrostSidecarPartialResp struct {
	SignatureShare string `json:"signature_share"`
}

type FrostSidecarCombineReq struct {
	MsgID     string            `json:"msg_id"`
	SessionID string            `json:"session_id"`
	AllShares map[string]string `json:"all_shares"`
}

type FrostSidecarCombineResp struct {
	TxHash          string                `json:"tx_hash"`
	TxHex           string                `json:"tx_hex"`
	TxKey           string                `json:"tx_key"`
	FeeAtomic       uint64                `json:"fee_atomic"`
	SpentOutputRefs []FrostSpentOutputRef `json:"spent_output_refs,omitempty"`
}

type FrostSpentOutputRef struct {
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
	KeyImage    string `json:"key_image,omitempty"`
	SpendTxHash string `json:"spend_tx_hash,omitempty"`
}

type FrostPreprocessPayload struct {
	ProtocolVersion  uint16 `json:"protocol_version,omitempty"`
	SenderPubKey     string `json:"sender_pub_key"`
	ParticipantIndex uint32 `json:"participant_index"`
	PreprocessHex    string `json:"preprocess_hex"`
	Signature        []byte `json:"signature,omitempty"`
}

type FrostSharePayload struct {
	ProtocolVersion  uint16 `json:"protocol_version,omitempty"`
	SenderPubKey     string `json:"sender_pub_key"`
	ParticipantIndex uint32 `json:"participant_index"`
	ShareHex         string `json:"share_hex"`
	Signature        []byte `json:"signature,omitempty"`
}

type FrostVerifyPayload struct {
	ProtocolVersion        uint16 `json:"protocol_version,omitempty"`
	SenderPubKey           string `json:"sender_pub_key"`
	Round                  string `json:"round"`
	TargetSenderPubKey     string `json:"target_sender_pub_key"`
	TargetParticipantIndex uint32 `json:"target_participant_index"`
	PayloadHash            string `json:"payload_hash"`
	TargetSignature        []byte `json:"target_signature,omitempty"`
	AttesterSignature      []byte `json:"attester_signature,omitempty"`
}

// FrostVerifyBatchPayload is the homogeneous keysign verification wire format.
// Every entry remains independently validated and accounted. Keygen uses its
// separate whole-round transcript vote format.
type FrostVerifyBatchPayload struct {
	Verifications []FrostVerifyPayload `json:"verifications"`
}

type FrostResultPayload struct {
	SenderPubKey string      `json:"sender_pub_key"`
	Response     FrostResult `json:"response"`
}

type FrostResult struct {
	MsgID           string                `json:"msg_id"`
	TxHash          string                `json:"tx_hash,omitempty"`
	TxHex           string                `json:"tx_hex,omitempty"`
	TxKey           string                `json:"tx_key,omitempty"`
	FeeAtomic       uint64                `json:"fee_atomic,omitempty"`
	Eventuality     FrostEventuality      `json:"eventuality"`
	SpentOutputRefs []FrostSpentOutputRef `json:"spent_output_refs,omitempty"`
	Status          uint8                 `json:"status"`
	Blame           blame.Blame           `json:"blame"`
}

type FrostDKGRound1Payload struct {
	SenderPubKey     string `json:"sender_pub_key"`
	ParticipantIndex uint32 `json:"participant_index"`
	Commitments      string `json:"commitments"`
}

type FrostDKGRound2Payload struct {
	SenderPubKey     string            `json:"sender_pub_key"`
	ParticipantIndex uint32            `json:"participant_index"`
	EncryptedShares  map[string]string `json:"encrypted_shares"`
}

type FrostDKGRound3Payload struct {
	SenderPubKey         string `json:"sender_pub_key"`
	ParticipantIndex     uint32 `json:"participant_index"`
	Address              string `json:"address"`
	GroupKey             string `json:"group_key"`
	ObservedMoneroHeight int64  `json:"observed_monero_height,omitempty"`
}

type FrostDKGResultPayload struct {
	SenderPubKey string         `json:"sender_pub_key"`
	Response     FrostDKGResult `json:"response"`
}

type FrostDKGResult struct {
	Address              string      `json:"address,omitempty"`
	GroupKey             string      `json:"group_key,omitempty"`
	ObservedMoneroHeight int64       `json:"observed_monero_height,omitempty"`
	Status               uint8       `json:"status"`
	Blame                blame.Blame `json:"blame"`
}

// FrostTranscriptPreprocessHash returns the signed transcript hash for a
// preprocess payload in one FROST signing session.
func FrostTranscriptPreprocessHash(msgID string, payload FrostPreprocessPayload) (string, error) {
	preprocessHex := strings.ToLower(strings.TrimSpace(payload.PreprocessHex))
	if preprocessHex == "" {
		return "", fmt.Errorf("preprocess payload is empty")
	}
	return frostTranscriptHash(
		messages.FROSTKEYSIGNPREPROCESS,
		msgID,
		payload.SenderPubKey,
		payload.ParticipantIndex,
		[]string{"preprocess_hex=" + preprocessHex},
	)
}

// FrostTranscriptShareHash returns the signed transcript hash for a partial
// signature share payload.
func FrostTranscriptShareHash(msgID string, payload FrostSharePayload) (string, error) {
	shareHex := strings.ToLower(strings.TrimSpace(payload.ShareHex))
	if shareHex == "" {
		return "", fmt.Errorf("share payload is empty")
	}
	return frostTranscriptHash(
		messages.FROSTKEYSIGNSHARE,
		msgID,
		payload.SenderPubKey,
		payload.ParticipantIndex,
		[]string{"share_hex=" + shareHex},
	)
}

// FrostTranscriptDKGRound1Hash returns the signed transcript hash for DKG
// round 1 commitments.
func FrostTranscriptDKGRound1Hash(msgID string, payload FrostDKGRound1Payload) (string, error) {
	commitments := strings.TrimSpace(payload.Commitments)
	if commitments == "" {
		return "", fmt.Errorf("round1 commitments are empty")
	}
	return frostTranscriptHash(
		frostDKGRound1,
		msgID,
		payload.SenderPubKey,
		payload.ParticipantIndex,
		[]string{"commitments=" + commitments},
	)
}

// FrostTranscriptDKGRound2Hash returns the signed transcript hash for DKG
// round 2 encrypted shares using deterministic target-index ordering.
func FrostTranscriptDKGRound2Hash(msgID string, payload FrostDKGRound2Payload) (string, error) {
	indexByRaw := make(map[uint64]string, len(payload.EncryptedShares))
	for rawKey := range payload.EncryptedShares {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return "", fmt.Errorf("round2 encrypted shares contains empty target index")
		}
		idx, err := strconv.ParseUint(key, 10, 32)
		if err != nil {
			return "", fmt.Errorf("round2 encrypted share target index %q is invalid", rawKey)
		}
		if _, exists := indexByRaw[idx]; exists {
			return "", fmt.Errorf("round2 encrypted shares has duplicate target index %d", idx)
		}
		indexByRaw[idx] = rawKey
	}

	sorted := make([]uint64, 0, len(indexByRaw))
	for idx := range indexByRaw {
		sorted = append(sorted, idx)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	canonicalShares := make([]string, 0, len(sorted)+1)
	canonicalShares = append(canonicalShares, "encrypted_shares_count="+strconv.Itoa(len(sorted)))
	for _, idx := range sorted {
		share := strings.TrimSpace(payload.EncryptedShares[indexByRaw[idx]])
		if share == "" {
			return "", fmt.Errorf("round2 encrypted share for target %d is empty", idx)
		}
		canonicalShares = append(canonicalShares, fmt.Sprintf("encrypted_share[%d]=%s", idx, share))
	}

	return frostTranscriptHash(
		frostDKGRound2,
		msgID,
		payload.SenderPubKey,
		payload.ParticipantIndex,
		canonicalShares,
	)
}

// FrostTranscriptDKGRound3Hash returns the signed transcript hash for the DKG
// final address, group key, and observed Monero height announcement.
func FrostTranscriptDKGRound3Hash(msgID string, payload FrostDKGRound3Payload) (string, error) {
	address := strings.TrimSpace(payload.Address)
	groupKey := strings.ToLower(strings.TrimSpace(payload.GroupKey))
	if address == "" {
		return "", fmt.Errorf("round3 address is empty")
	}
	if groupKey == "" {
		return "", fmt.Errorf("round3 group key is empty")
	}
	if payload.ObservedMoneroHeight < 0 {
		return "", fmt.Errorf("round3 observed monero height cannot be negative")
	}
	return frostTranscriptHash(
		frostDKGRound3,
		msgID,
		payload.SenderPubKey,
		payload.ParticipantIndex,
		[]string{
			"address=" + address,
			"group_key=" + groupKey,
			"observed_monero_height=" + strconv.FormatInt(payload.ObservedMoneroHeight, 10),
		},
	)
}

// frostTranscriptHash builds the canonical transcript string shared by signing
// and DKG round payload hashes.
func frostTranscriptHash(round, msgID, targetSenderPubKey string, participantIndex uint32, roundFields []string) (string, error) {
	msgID = strings.TrimSpace(msgID)
	if msgID == "" {
		return "", fmt.Errorf("msg_id is required")
	}
	round = strings.TrimSpace(round)
	if round == "" {
		return "", fmt.Errorf("round is required")
	}
	targetSenderNorm := normalizePubKey(targetSenderPubKey)
	if targetSenderNorm == "" {
		return "", fmt.Errorf("target sender pubkey is required")
	}
	if participantIndex == 0 {
		return "", fmt.Errorf("participant index is required")
	}
	canonical := []string{
		"domain=" + frostTranscriptHashDomain,
		"protocol_version=" + strconv.FormatUint(uint64(frostWireProtocolVersion), 10),
		"msg_id=" + msgID,
		"round=" + round,
		"target_sender_pub_key=" + targetSenderNorm,
		"participant_index=" + strconv.FormatUint(uint64(participantIndex), 10),
	}
	canonical = append(canonical, roundFields...)
	return tcommon.MsgToHashString([]byte(strings.Join(canonical, "\n")))
}

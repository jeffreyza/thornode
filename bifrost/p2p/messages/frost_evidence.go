package messages

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	FrostProtocolVersion = uint16(2)

	FrostBlameRoundPreprocessEquivocation = "FROSTV2PreprocessEquivocation"
	FrostBlameRoundShareEquivocation      = "FROSTV2ShareEquivocation"

	FrostMaxEvidenceBytes       = 16 * 1024
	frostHashHexChars           = 64
	frostMaxSignatureSize       = 128
	frostMaxVaultKeyChars       = 256
	frostMaxParticipantKeyChars = 256
	frostMaxRoundChars          = 64
)

func validateFrostStatementField(name, value string, maxLen int) error {
	if value == "" || len(value) > maxLen {
		return fmt.Errorf("invalid FROST %s length", name)
	}
	for i := 0; i < len(value); i++ {
		// Signed statements are newline-delimited key=value records. Restrict
		// free-form values to visible ASCII so control characters cannot make one
		// signature authenticate two different field tuples.
		if value[i] < 0x21 || value[i] > 0x7e {
			return fmt.Errorf("invalid FROST %s character", name)
		}
	}
	return nil
}

func IsFrostEvidenceBlameRound(round string) bool {
	return round == FrostBlameRoundPreprocessEquivocation || round == FrostBlameRoundShareEquivocation
}

func FrostEvidenceTranscriptRound(round string) (string, bool) {
	switch round {
	case FrostBlameRoundPreprocessEquivocation:
		return "frost_preprocess", true
	case FrostBlameRoundShareEquivocation:
		return "frost_share", true
	default:
		return "", false
	}
}

// FrostSignedEnvelope is an application-authenticated statement by the node
// that owns a FROST round payload. Unlike transport authentication, it remains
// independently verifiable after the libp2p stream is gone and can therefore
// be used as positive equivocation evidence.
type FrostSignedEnvelope struct {
	ProtocolVersion  uint16 `json:"protocol_version"`
	SessionID        string `json:"session_id"`
	VaultPubKey      string `json:"vault_pub_key"`
	Network          string `json:"network"`
	Round            string `json:"round"`
	SenderPubKey     string `json:"sender_pub_key"`
	ParticipantIndex uint32 `json:"participant_index"`
	PayloadHash      string `json:"payload_hash"`
	Signature        []byte `json:"signature"`
}

func (e FrostSignedEnvelope) SignBytes() ([]byte, error) {
	if e.ProtocolVersion != FrostProtocolVersion {
		return nil, fmt.Errorf("unsupported FROST envelope version")
	}
	sessionID := strings.ToLower(strings.TrimSpace(e.SessionID))
	vaultPubKey := strings.ToLower(strings.TrimSpace(e.VaultPubKey))
	network := strings.ToLower(strings.TrimSpace(e.Network))
	round := strings.TrimSpace(e.Round)
	sender := strings.TrimSpace(e.SenderPubKey)
	payloadHash := strings.ToLower(strings.TrimSpace(e.PayloadHash))
	if sessionID == "" || vaultPubKey == "" || network == "" || round == "" || sender == "" || e.ParticipantIndex == 0 {
		return nil, fmt.Errorf("incomplete FROST envelope")
	}
	switch network {
	case "mainnet", "mocknet", "stagenet", "chainnet":
	default:
		return nil, fmt.Errorf("invalid FROST envelope network")
	}
	if err := validateFrostStatementField("envelope vault pubkey", vaultPubKey, frostMaxVaultKeyChars); err != nil {
		return nil, err
	}
	if err := validateFrostStatementField("envelope round", round, frostMaxRoundChars); err != nil {
		return nil, err
	}
	if err := validateFrostStatementField("envelope sender pubkey", sender, frostMaxParticipantKeyChars); err != nil {
		return nil, err
	}
	if len(sessionID) != frostHashHexChars {
		return nil, fmt.Errorf("invalid FROST envelope session id length")
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		return nil, fmt.Errorf("invalid FROST envelope session id")
	}
	if len(payloadHash) != frostHashHexChars {
		return nil, fmt.Errorf("invalid FROST envelope payload hash length")
	}
	if _, err := hex.DecodeString(payloadHash); err != nil {
		return nil, fmt.Errorf("invalid FROST envelope payload hash")
	}
	return []byte(strings.Join([]string{
		"domain=thorchain-frost-signed-envelope-v2",
		"protocol_version=" + strconv.FormatUint(uint64(e.ProtocolVersion), 10),
		"session_id=" + sessionID,
		"vault_pub_key=" + vaultPubKey,
		"network=" + network,
		"round=" + round,
		"sender_pub_key=" + sender,
		"participant_index=" + strconv.FormatUint(uint64(e.ParticipantIndex), 10),
		"payload_hash=" + payloadHash,
	}, "\n")), nil
}

// FrostSignedEcho authenticates an attester's statement that it received the
// enclosed target-signed envelope. The target signature is included in the
// signed bytes, preventing an echo from being transplanted onto another proof.
type FrostSignedEcho struct {
	ProtocolVersion uint16              `json:"protocol_version"`
	AttesterPubKey  string              `json:"attester_pub_key"`
	Target          FrostSignedEnvelope `json:"target"`
	Signature       []byte              `json:"signature"`
}

func (e FrostSignedEcho) SignBytes() ([]byte, error) {
	targetBytes, err := e.Target.SignBytes()
	if err != nil {
		return nil, err
	}
	attester := strings.TrimSpace(e.AttesterPubKey)
	if e.ProtocolVersion != FrostProtocolVersion || attester == "" {
		return nil, fmt.Errorf("incomplete FROST echo")
	}
	if err := validateFrostStatementField("echo attester pubkey", attester, frostMaxParticipantKeyChars); err != nil {
		return nil, err
	}
	return []byte(strings.Join([]string{
		"domain=thorchain-frost-signed-echo-v2",
		"protocol_version=" + strconv.FormatUint(uint64(e.ProtocolVersion), 10),
		"attester_pub_key=" + attester,
		"target_statement=" + hex.EncodeToString(targetBytes),
		"target_signature=" + hex.EncodeToString(e.Target.Signature),
	}, "\n")), nil
}

type FrostEquivocationEvidence struct {
	ProtocolVersion uint16              `json:"protocol_version"`
	First           FrostSignedEnvelope `json:"first"`
	Second          FrostSignedEnvelope `json:"second"`
}

func (e FrostEquivocationEvidence) Validate() error {
	if e.ProtocolVersion != FrostProtocolVersion {
		return fmt.Errorf("unsupported FROST evidence version")
	}
	if _, err := e.First.SignBytes(); err != nil {
		return fmt.Errorf("invalid first envelope: %w", err)
	}
	if _, err := e.Second.SignBytes(); err != nil {
		return fmt.Errorf("invalid second envelope: %w", err)
	}
	if len(e.First.Signature) == 0 || len(e.First.Signature) > frostMaxSignatureSize ||
		len(e.Second.Signature) == 0 || len(e.Second.Signature) > frostMaxSignatureSize {
		return fmt.Errorf("invalid FROST evidence signature size")
	}
	if !strings.EqualFold(strings.TrimSpace(e.First.SessionID), strings.TrimSpace(e.Second.SessionID)) ||
		!strings.EqualFold(strings.TrimSpace(e.First.VaultPubKey), strings.TrimSpace(e.Second.VaultPubKey)) ||
		!strings.EqualFold(strings.TrimSpace(e.First.Network), strings.TrimSpace(e.Second.Network)) ||
		e.First.Round != e.Second.Round ||
		e.First.SenderPubKey != e.Second.SenderPubKey ||
		e.First.ParticipantIndex != e.Second.ParticipantIndex {
		return fmt.Errorf("FROST evidence envelopes identify different statements")
	}
	if strings.EqualFold(strings.TrimSpace(e.First.PayloadHash), strings.TrimSpace(e.Second.PayloadHash)) {
		return fmt.Errorf("FROST evidence payload hashes are identical")
	}
	return nil
}

// canonical returns the envelope with every field normalized exactly as
// SignBytes normalizes it. Signing and validation both trim and lower-case,
// so an envelope that differs from its canonical form only in whitespace or
// case produces identical signing bytes and verifies identically: without
// pinning the wire form, one equivocation would have many valid encodings.
func (e FrostSignedEnvelope) canonical() FrostSignedEnvelope {
	e.SessionID = strings.ToLower(strings.TrimSpace(e.SessionID))
	e.VaultPubKey = strings.ToLower(strings.TrimSpace(e.VaultPubKey))
	e.Network = strings.ToLower(strings.TrimSpace(e.Network))
	e.Round = strings.TrimSpace(e.Round)
	e.SenderPubKey = strings.TrimSpace(e.SenderPubKey)
	e.PayloadHash = strings.ToLower(strings.TrimSpace(e.PayloadHash))
	return e
}

func (e FrostSignedEnvelope) isCanonical() bool {
	c := e.canonical()
	return c.SessionID == e.SessionID &&
		c.VaultPubKey == e.VaultPubKey &&
		c.Network == e.Network &&
		c.Round == e.Round &&
		c.SenderPubKey == e.SenderPubKey &&
		c.PayloadHash == e.PayloadHash
}

func MarshalFrostEquivocationEvidence(e FrostEquivocationEvidence) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	// Normalize before ordering. Comparing raw payload hashes would sort on
	// whitespace and case that neither the signature nor Validate can see.
	e.First, e.Second = e.First.canonical(), e.Second.canonical()
	if e.Second.PayloadHash < e.First.PayloadHash {
		e.First, e.Second = e.Second, e.First
	}
	buf, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(buf) > FrostMaxEvidenceBytes {
		return nil, fmt.Errorf("FROST evidence exceeds size limit")
	}
	return buf, nil
}

func UnmarshalFrostEquivocationEvidence(buf []byte) (FrostEquivocationEvidence, error) {
	if len(buf) == 0 || len(buf) > FrostMaxEvidenceBytes {
		return FrostEquivocationEvidence{}, fmt.Errorf("invalid FROST evidence size")
	}
	var evidence FrostEquivocationEvidence
	decoder := json.NewDecoder(bytes.NewReader(buf))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evidence); err != nil {
		return FrostEquivocationEvidence{}, fmt.Errorf("invalid FROST evidence encoding: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return FrostEquivocationEvidence{}, fmt.Errorf("trailing FROST evidence data")
	}
	if err := evidence.Validate(); err != nil {
		return FrostEquivocationEvidence{}, err
	}
	// Evidence is a slashing artifact, so exactly one byte encoding may decode
	// for a given equivocation. Reject any envelope that is not already in its
	// canonical form rather than normalizing it here, which would let the same
	// equivocation arrive under several distinct evidence hashes.
	if !evidence.First.isCanonical() || !evidence.Second.isCanonical() {
		return FrostEquivocationEvidence{}, fmt.Errorf("non-canonical FROST evidence encoding")
	}
	if evidence.Second.PayloadHash < evidence.First.PayloadHash {
		return FrostEquivocationEvidence{}, fmt.Errorf("non-canonical FROST evidence ordering")
	}
	canonical, err := MarshalFrostEquivocationEvidence(evidence)
	if err != nil || !bytes.Equal(buf, canonical) {
		return FrostEquivocationEvidence{}, fmt.Errorf("non-canonical FROST evidence encoding")
	}
	return evidence, nil
}

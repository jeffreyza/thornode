package sidecar

import (
	"encoding/json"
	"fmt"
)

const (
	ProtocolName  = "thorchain-xmr-frost"
	APIMajorV2    = 2
	StateSchemaV4 = 4

	CapabilityAtomicOutgoingV2       = "atomic-outgoing-v2"
	CapabilityBalanceReconciliation  = "balance-reconciliation-v1"
	CapabilityBoundRecovery          = "bound-recovery-envelope-v2"
	CapabilityPerVaultKeyStatus      = "per-vault-key-status-v1"
	CapabilityRestartReservation     = "restart-reservation-sweep-v1"
	CapabilityDeadOutgoingQuarantine = "dead-outgoing-quarantine-v1"
	CapabilityScannerContinuity      = "scanner-continuity-v1"
	CapabilityDurableInboundReplay   = "durable-inbound-replay-v1"
	CapabilityScannerBoundedRewind   = "scanner-bounded-rewind-v1"
	CapabilitySharePlanQuarantine    = "share-issued-plan-quarantine-v1"
	CapabilityVerifiedAbsenceBalance = "verified-absence-balance-quarantine-v1"
	CapabilityCanonicalObservation   = "canonical-outgoing-observation-v1"
	CapabilityCanonicalTxLocation    = "canonical-tx-location-v1"
	CapabilityMoneroNetworkIdentity  = "monero-network-identity-v1"

	MoneroMainnetGenesisHash  = "418015bb9ae982a1975da7d79277c2705727a56894ba0fb246adaabb1f4632e3"
	MoneroTestnetGenesisHash  = "48ca7cd3c8de5b6a4d53d2861fbdaedca141553559f9be9520068053cda8430b"
	MoneroStagenetGenesisHash = "76ee3cc98646292206cd3e86f74d88b4dcc1d937088645e9b0cbca84b7ce74eb"

	// CapabilityIdempotentCombine asserts that the signer durably replays combine
	// by (msg_id, session_id, all_shares), so a retried combine cannot produce a
	// second Monero broadcast. The signer advertises this alongside its
	// durable_combine_response path, which replays the committed result after a
	// session loss instead of re-signing. It is deliberately NOT required by
	// NewClient: a signer that does not advertise it still works, it just gets
	// single-shot combine. Requiring it would hard-fail every deployment whose
	// signer image predates the contract.
	CapabilityIdempotentCombine = "durable-sign-result-recovery-v1"
)

// Info is the authenticated compatibility contract exposed by the signer.
// It is not an artifact-authentication mechanism; the configured image digest
// and release provenance remain the trust root.
type Info struct {
	Protocol          string   `json:"protocol"`
	APIMajor          int      `json:"api_major"`
	SoftwareVersion   string   `json:"software_version"`
	GitCommit         string   `json:"git_commit"`
	Network           string   `json:"network"`
	MoneroNettype     string   `json:"monero_nettype"`
	MoneroGenesisHash string   `json:"monero_genesis_hash"`
	StateSchema       int      `json:"state_schema"`
	Capabilities      []string `json:"capabilities"`
}

func (i Info) HasCapability(required string) bool {
	for _, capability := range i.Capabilities {
		if capability == required {
			return true
		}
	}
	return false
}

// envelope is the optional wrapper some sidecar endpoints return. Callers try
// envelope-shape first and fall back to direct unmarshal.
type envelope struct {
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

// --- FROST DKG ---

type DKGRound1Req struct {
	VaultPubKey      string `json:"vault_pub_key"`
	Threshold        uint32 `json:"threshold"`
	N                uint32 `json:"n"`
	ParticipantIndex uint32 `json:"participant_index"`
	Context          string `json:"context,omitempty"`
	SessionID        string `json:"session_id"`
}

type DKGRound1Resp struct {
	Commitments string `json:"commitments"`
}

type DKGRound2Req struct {
	SessionID      string            `json:"session_id"`
	AllCommitments map[string]string `json:"all_commitments"`
}

type DKGRound2Resp struct {
	EncryptedShares map[string]string `json:"encrypted_shares"`
}

type DKGRound3Req struct {
	SessionID string            `json:"session_id"`
	AllShares map[string]string `json:"all_shares"`
}

type DKGRound3Resp struct {
	Address  string `json:"address"`
	GroupKey string `json:"group_key"`
}

type DKGFinalizeReq struct {
	SessionID string `json:"session_id"`
	GroupKey  string `json:"group_key"`
	Address   string `json:"address"`
}

type DKGFinalizeResp struct {
	Success     bool   `json:"success"`
	KeyBytesHex string `json:"key_bytes_hex"`
}

// --- FROST signing ---

type SignPrepareReq struct {
	MsgID                       string     `json:"msg_id"`
	VaultPubKey                 string     `json:"vault_pub_key"`
	ToAddress                   string     `json:"to_address"`
	AmountAtomic                uint64     `json:"amount_atomic"`
	Memo                        string     `json:"memo,omitempty"`
	FeeRate                     uint64     `json:"fee_rate,omitempty"`
	DecoyContext                string     `json:"decoy_context"`
	RequiredInputs              []SpentRef `json:"required_inputs,omitempty"`
	MaxInputs                   int64      `json:"max_inputs,omitempty"`
	DustThreshold               uint64     `json:"dust_threshold,omitempty"`
	MaxSelectedInputValueAtomic uint64     `json:"max_selected_input_value_atomic,omitempty"`
	// MaxGasAtomic is optional so this client remains wire-compatible with
	// 3.19-era signers that ignore unknown JSON fields, while newer signers use
	// it to cap fees and migration sweep math.
	MaxGasAtomic uint64 `json:"max_gas_atomic,omitempty"`
}

type SignPrepareResp struct {
	SessionID      string     `json:"session_id"`
	Preprocess     string     `json:"preprocess"`
	SelectedInputs []SpentRef `json:"selected_inputs"`
}

type SignPartialReq struct {
	MsgID           string            `json:"msg_id"`
	SessionID       string            `json:"session_id"`
	AllPreprocesses map[string]string `json:"all_preprocesses"`
}

type SignPartialResp struct {
	SignatureShare string `json:"signature_share"`
}

type SignCombineReq struct {
	MsgID     string            `json:"msg_id"`
	SessionID string            `json:"session_id"`
	AllShares map[string]string `json:"all_shares"`
}

type SignCombineResp struct {
	TxHash          string     `json:"tx_hash"`
	TxHex           string     `json:"tx_hex"`
	TxKey           string     `json:"tx_key"`
	FeeAtomic       uint64     `json:"fee_atomic"`
	SpentOutputRefs []SpentRef `json:"spent_output_refs,omitempty"`
}

// SpentRef identifies one (input tx, output index) tuple consumed by a sign.
type SpentRef struct {
	TxHash      string `json:"tx_hash"`
	OutputIndex uint32 `json:"output_index"`
	KeyImage    string `json:"key_image,omitempty"`
	SpendTxHash string `json:"spend_tx_hash,omitempty"`
}

// OutgoingOrigin is the provenance of an outgoing transfer as recorded by the
// sidecar. Fields carrying it are typed so an arbitrary string cannot be
// smuggled into a mark-spent request.
type OutgoingOrigin string

// The origins are declared as untyped string constants on purpose: they are
// both assigned to OutgoingOrigin-typed request fields and compared against
// the plain-string Origin the sidecar reports on a Transfer.
const (
	OutgoingOriginLocalCombined  = "local_combined"
	OutgoingOriginVaultCertified = "vault_certified"
	OutgoingOriginChainCommitted = "chain_committed"
)

type MarkSpentReq struct {
	VaultPubKey      string         `json:"vault_pub_key"`
	TxHash           string         `json:"tx_hash"`
	TxKey            string         `json:"tx_key,omitempty"`
	SpentRefs        []SpentRef     `json:"spent_refs"`
	SkipVerification bool           `json:"skip_verification,omitempty"`
	FromAddress      string         `json:"from_address,omitempty"`
	ToAddress        string         `json:"to_address,omitempty"`
	Amount           uint64         `json:"amount,omitempty"`
	Fee              uint64         `json:"fee,omitempty"`
	Memo             string         `json:"memo,omitempty"`
	Origin           OutgoingOrigin `json:"origin"`
	CertificateHash  string         `json:"certificate_hash,omitempty"`
}

type MarkSpentResp struct {
	Marked   int `json:"marked"`
	NotFound int `json:"not_found"`
}

// OutboundMeta carries outbound attribution for MarkSpent calls.
type OutboundMeta struct {
	FromAddress     string
	ToAddress       string
	Amount          uint64
	Fee             uint64
	Memo            string
	TxKey           string
	Origin          OutgoingOrigin
	CertificateHash string
}

// --- Broadcast ---

type BroadcastReq struct {
	TxHex       string `json:"tx_hex"`
	TxHash      string `json:"tx_hash,omitempty"`
	TxKey       string `json:"tx_key,omitempty"`
	VaultPubKey string `json:"vault_pub_key"`
	MsgID       string `json:"msg_id"`
}

type BroadcastResp struct {
	TxHash string `json:"tx_hash"`
	TxKey  string `json:"tx_key,omitempty"`
}

// --- Scanner / balance / reads ---

type Balance struct {
	Balance              uint64  `json:"balance"`
	UnlockedBalance      uint64  `json:"unlocked_balance"`
	SpendableBalance     *uint64 `json:"spendable_balance,omitempty"`
	Reconciled           *bool   `json:"reconciled,omitempty"`
	ReconciliationState  string  `json:"reconciliation_state,omitempty"`
	ReconciliationReason string  `json:"reconciliation_reason,omitempty"`
	PendingOutgoingCount *int    `json:"pending_outgoing_count"`
	// Verified-absent outgoings whose value counts toward balance while their
	// inputs remain durably reserved and unavailable to signing.
	QuarantinedOutgoingCount *int `json:"quarantined_outgoing_count,omitempty"`
}

func (b Balance) IsReconciled() bool {
	// API v2's explicit enum is authoritative. The legacy bool remains decoded
	// for diagnostics/rolling upgrades but can never make an absent or unknown
	// state pass open.
	return b.ReconciliationState == "reconciled" &&
		b.PendingOutgoingCount != nil && *b.PendingOutgoingCount == 0
}

type RegisterVaultReq struct {
	VaultPubKey         string `json:"vault_pub_key"`
	GroupKey            string `json:"group_key"`
	Address             string `json:"address"`
	SpendKeyHex         string `json:"spend_key_hex,omitempty"`
	ViewKeyHex          string `json:"view_key_hex,omitempty"`
	Source              string `json:"source,omitempty"`
	VaultCreationHeight int64  `json:"birthday,omitempty"`
}

type RegisterVaultResp struct {
	Registered         bool    `json:"registered"`
	EffectiveBirthday  uint64  `json:"effective_birthday"`
	ResetEpoch         uint64  `json:"reset_epoch"`
	ExpectedNextHeight *uint64 `json:"expected_next_height,omitempty"`
}

// UnmarshalJSON requires the non-optional registration continuity fields to
// be present while retaining expected_next_height as a pointer. A successful
// registration must still be validated by Client.RegisterVaultContext because
// a nil cursor is not usable by Bifrost.
func (r *RegisterVaultResp) UnmarshalJSON(data []byte) error {
	type wireResponse struct {
		Registered         *bool   `json:"registered"`
		EffectiveBirthday  *uint64 `json:"effective_birthday"`
		ResetEpoch         *uint64 `json:"reset_epoch"`
		ExpectedNextHeight *uint64 `json:"expected_next_height"`
	}

	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Registered == nil {
		return fmt.Errorf("register vault response omitted registered")
	}
	if wire.EffectiveBirthday == nil {
		return fmt.Errorf("register vault response omitted effective_birthday")
	}
	if wire.ResetEpoch == nil {
		return fmt.Errorf("register vault response omitted reset_epoch")
	}

	*r = RegisterVaultResp{
		Registered:         *wire.Registered,
		EffectiveBirthday:  *wire.EffectiveBirthday,
		ResetEpoch:         *wire.ResetEpoch,
		ExpectedNextHeight: wire.ExpectedNextHeight,
	}
	return nil
}

type Transfer struct {
	TxID                string     `json:"txid"`
	TxKey               string     `json:"tx_key,omitempty"`
	Address             string     `json:"address"`
	FromAddress         string     `json:"from_address,omitempty"`
	VaultPubKey         string     `json:"vault_pub_key,omitempty"`
	Amount              uint64     `json:"amount"`
	Fee                 uint64     `json:"fee"`
	Height              uint64     `json:"height"`
	Confirmations       uint64     `json:"confirmations"`
	Note                string     `json:"note"`
	Locked              bool       `json:"locked"`
	Origin              string     `json:"origin,omitempty"`
	ObservationEligible bool       `json:"observation_eligible,omitempty"`
	SpentOutputRefs     []SpentRef `json:"spent_output_refs,omitempty"`
}

type Transfers struct {
	Incoming []Transfer `json:"incoming"`
	Outgoing []Transfer `json:"outgoing"`
}

type SyncReq struct {
	Height             int64   `json:"height"`
	TipHeight          int64   `json:"tip_height"`
	BlockHash          string  `json:"block_hash"`
	ParentHash         string  `json:"parent_hash"`
	ReorgWindow        int64   `json:"reorg_window"`
	ResetEpoch         *uint64 `json:"reset_epoch,omitempty"`
	ExpectedNextHeight *uint64 `json:"expected_next_height,omitempty"`
}

// ScannerState is the sidecar's durable global scan cursor. Once
// StrictAfterReset is set, every scanner/sync must bind itself to ResetEpoch
// and the requested height so an in-flight pre-rewind request cannot commit on
// the replacement branch.
type ScannerState struct {
	ResetEpoch       uint64  `json:"reset_epoch"`
	NextHeight       *uint64 `json:"next_height"`
	LastHeight       *uint64 `json:"last_height"`
	LastBlockHash    string  `json:"last_block_hash,omitempty"`
	LastParentHash   string  `json:"last_parent_hash,omitempty"`
	StrictAfterReset bool    `json:"strict_after_reset"`
}

// UnmarshalJSON distinguishes an explicit fresh epoch/strict=false state from
// a malformed response which silently omitted those required fields.
func (s *ScannerState) UnmarshalJSON(data []byte) error {
	type wireState struct {
		ResetEpoch       *uint64 `json:"reset_epoch"`
		NextHeight       *uint64 `json:"next_height"`
		LastHeight       *uint64 `json:"last_height"`
		LastBlockHash    string  `json:"last_block_hash"`
		LastParentHash   string  `json:"last_parent_hash"`
		StrictAfterReset *bool   `json:"strict_after_reset"`
	}

	var wire wireState
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.ResetEpoch == nil {
		return fmt.Errorf("scanner state omitted reset_epoch")
	}
	if wire.StrictAfterReset == nil {
		return fmt.Errorf("scanner state omitted strict_after_reset")
	}

	*s = ScannerState{
		ResetEpoch:       *wire.ResetEpoch,
		NextHeight:       wire.NextHeight,
		LastHeight:       wire.LastHeight,
		LastBlockHash:    wire.LastBlockHash,
		LastParentHash:   wire.LastParentHash,
		StrictAfterReset: *wire.StrictAfterReset,
	}
	return nil
}

// ScannerRewindRequiredError reports that the authenticated sidecar cursor is
// behind the height Bifrost attempted to scan. Callers must rewind their local
// block-scanner cursor to NextHeight before trying again.
type ScannerRewindRequiredError struct {
	RequestedHeight int64
	NextHeight      int64
	ResetEpoch      uint64
}

func (e *ScannerRewindRequiredError) Error() string {
	if e == nil {
		return "sidecar scanner rewind required"
	}
	return fmt.Sprintf(
		"sidecar scanner rewind required: requested height %d, next height %d, reset epoch %d",
		e.RequestedHeight,
		e.NextHeight,
		e.ResetEpoch,
	)
}

// ScannerRecoveryRequiredError reports that the authenticated sidecar cursor is
// AHEAD of the height Bifrost attempted to scan, which happens after a
// Bifrost-side cursor rollback. Unlike a rewind, no amount of retrying repairs
// it: the sidecar has already durably scanned past this height, so re-fetching
// the same block forever would silently stall the chain. It is terminal and
// requires an operator to reconcile the two cursors.
type ScannerRecoveryRequiredError struct {
	RequestedHeight int64
	NextHeight      int64
	ResetEpoch      uint64
}

func (e *ScannerRecoveryRequiredError) Error() string {
	if e == nil {
		return "sidecar scanner recovery required"
	}
	return fmt.Sprintf(
		"sidecar scanner cursor is ahead of requested height, operator recovery required: "+
			"requested %d, next height %d, reset epoch %d",
		e.RequestedHeight,
		e.NextHeight,
		e.ResetEpoch,
	)
}

type ScannerResetResp struct {
	Reset              bool   `json:"reset"`
	BirthdayHeight     int64  `json:"birthday_height"`
	ResetEpoch         uint64 `json:"reset_epoch"`
	ExpectedNextHeight uint64 `json:"expected_next_height"`
}

// ScannerRewindReq is the compare-and-set contract for a bounded canonical
// reorg rewind. The client derives Network and both Expected fields from its
// configured identity and an authenticated scanner-state read.
type ScannerRewindReq struct {
	Network            string `json:"network"`
	ExpectedResetEpoch uint64 `json:"expected_reset_epoch"`
	ExpectedNextHeight uint64 `json:"expected_next_height"`
	TargetNextHeight   uint64 `json:"target_next_height"`
	AnchorHeight       uint64 `json:"anchor_height"`
	AnchorHash         string `json:"anchor_hash"`
}

type ScannerRewindResp struct {
	Rewound            bool   `json:"rewound"`
	PreviousNextHeight uint64 `json:"previous_next_height"`
	NextHeight         uint64 `json:"next_height"`
	ResetEpoch         uint64 `json:"reset_epoch"`
}

// UnmarshalJSON makes zero-valued but required rewind response fields
// distinguishable from fields omitted by a malformed or incompatible signer.
func (r *ScannerRewindResp) UnmarshalJSON(data []byte) error {
	type wireResponse struct {
		Rewound            *bool   `json:"rewound"`
		PreviousNextHeight *uint64 `json:"previous_next_height"`
		NextHeight         *uint64 `json:"next_height"`
		ResetEpoch         *uint64 `json:"reset_epoch"`
	}

	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if wire.Rewound == nil || wire.PreviousNextHeight == nil ||
		wire.NextHeight == nil || wire.ResetEpoch == nil {
		return fmt.Errorf("scanner rewind response omitted a required field")
	}
	*r = ScannerRewindResp{
		Rewound:            *wire.Rewound,
		PreviousNextHeight: *wire.PreviousNextHeight,
		NextHeight:         *wire.NextHeight,
		ResetEpoch:         *wire.ResetEpoch,
	}
	return nil
}

// MarkObservedReq asks the sidecar to reconcile one already authenticated
// outgoing against an exact canonical Monero block. It is intentionally
// separate from scanner sync so late migration recovery never rewinds scanner
// continuity.
type MarkObservedReq struct {
	VaultPubKey string `json:"vault_pub_key"`
	TxHash      string `json:"tx_hash"`
	Height      uint64 `json:"height"`
	BlockHash   string `json:"block_hash"`
}

type Addresses struct {
	Addresses []string `json:"addresses"`
}

type Output struct {
	TxHash          string `json:"tx_hash"`
	OutputIndex     uint32 `json:"output_index"`
	Amount          uint64 `json:"amount"`
	Height          uint64 `json:"height"`
	KeyImage        string `json:"key_image,omitempty"`
	Spent           bool   `json:"spent"`
	State           string `json:"state,omitempty"`
	SubaddressMajor uint32 `json:"subaddress_major"`
	SubaddressMinor uint32 `json:"subaddress_minor"`
}

type Outputs struct {
	Outputs []Output `json:"outputs"`
}

type FeeRate struct {
	FeeRatePerWeight uint64 `json:"fee_rate_per_weight"`
}

type ChainHeightResp struct {
	Height int64 `json:"height"`
}

type ChainBlockResp struct {
	Height       int64    `json:"height"`
	Reward       uint64   `json:"reward"`
	Hash         string   `json:"hash"`
	PreviousHash string   `json:"previous_hash"`
	TxHashes     []string `json:"tx_hashes"`
}

type TxExistsResp struct {
	Exists bool `json:"exists"`
}

type CanonicalTxLocationResp struct {
	TxHash       string `json:"tx_hash"`
	Height       int64  `json:"height"`
	BlockHash    string `json:"block_hash"`
	PreviousHash string `json:"previous_hash"`
}

// ChainBlock is the transport-independent block descriptor (used across both
// the sidecar and monerod-fallback paths).
type ChainBlock struct {
	Height       int64
	Reward       uint64
	Hash         string
	PreviousHash string
	TxHashes     []string
}

// ChainBlockHeightMismatchError rejects a chain response that is not bound to
// the height requested by Bifrost, regardless of which transport supplied it.
type ChainBlockHeightMismatchError struct {
	RequestedHeight int64
	ReturnedHeight  int64
}

func (e *ChainBlockHeightMismatchError) Error() string {
	if e == nil {
		return "monero chain block height mismatch"
	}
	return fmt.Sprintf(
		"monero chain block requested height %d but returned height %d",
		e.RequestedHeight,
		e.ReturnedHeight,
	)
}

// VaultKey describes the sidecar's knowledge of a single vault's threshold
// share.
type VaultKey struct {
	VaultPubKey  string `json:"vault_pub_key"`
	GroupKey     string `json:"group_key,omitempty"`
	HasFrostKeys bool   `json:"has_frost_keys"`
	Signable     bool   `json:"signable"`
	Reason       string `json:"reason,omitempty"`
}

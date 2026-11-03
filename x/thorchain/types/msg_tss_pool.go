package types

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const (
	// MinKeysharesBackupEntropy was selected based on a few spot checks of the entropy in
	// encrypted keyshares for mocknet, which were always greater than 7, this is just a
	// sanity check and is safe to set lower.
	MinKeysharesBackupEntropy   float64 = 7
	MinKeysharesBackupSize      int     = 1024
	MinFrostKeysharesBackupSize int     = 256
)

// MatchMnemonic will match substrings that look like a 12+ word mnemonic.
var MatchMnemonic = regexp.MustCompile(`([a-zA-Z]+ ){11}[a-zA-Z]+`)

var (
	_ sdk.Msg              = &MsgTssPool{}
	_ sdk.HasValidateBasic = &MsgTssPool{}
	_ sdk.LegacyMsg        = &MsgTssPool{}
)

// NewMsgTssPool is a constructor function for MsgTssPool
func NewMsgTssPool(pks []string, poolpk common.PubKey, secp256k1Signature, keysharesBackup []byte, keygenType KeygenType, height int64, bl []Blame, chains []string, signer cosmos.AccAddress, keygenTime int64) (*MsgTssPool, error) {
	id, err := getTssID(pks, poolpk, common.EmptyPubKey, height, bl)
	if err != nil {
		return nil, fmt.Errorf("fail to get tss id: %w", err)
	}
	return &MsgTssPool{
		ID:                 id,
		PubKeys:            pks,
		PoolPubKey:         poolpk,
		Height:             height,
		KeygenType:         keygenType,
		Blame:              bl,
		Chains:             chains,
		Signer:             signer,
		KeygenTime:         keygenTime,
		KeysharesBackup:    keysharesBackup,
		Secp256K1Signature: secp256k1Signature,
	}, nil
}

// getTssID computes the identity of a keygen result, which is also the identity
// of the TssVoter that tallies it.
//
// Every field the handler requires all reporters of an ID to agree on must
// either be bound into this identity or be pinned to a fixed value by
// TssHandler.validate. An unbound field is set in the voter by whichever
// message is processed first, and every later message that disagrees is
// rejected before it is counted - so a single keygen member could otherwise
// pin a bogus value and stall consensus for that keygen (issue #2953).
func getTssID(members []string, poolPk, poolPubKeyEddsa common.PubKey, height int64, bl []Blame) (string, error) {
	return getTssIDWithXMRFields(members, poolPk, poolPubKeyEddsa, height, bl, common.EmptyPubKey, 0, false)
}

func getXMRTssID(members []string, poolPk, poolPubKeyEddsa common.PubKey, height int64, bl []Blame, poolPubKeyFrost common.PubKey, observedMoneroHeight int64) (string, error) {
	return getTssIDWithXMRFields(members, poolPk, poolPubKeyEddsa, height, bl, poolPubKeyFrost, observedMoneroHeight, true)
}

func getTssIDWithXMRFields(
	members []string,
	poolPk common.PubKey,
	poolPubKeyEddsa common.PubKey,
	height int64,
	bl []Blame,
	poolPubKeyFrost common.PubKey,
	observedMoneroHeight int64,
	includeXMRFields bool,
) (string, error) {
	// ensure input pubkeys list is deterministically sorted
	sort.SliceStable(members, func(i, j int) bool {
		return members[i] < members[j]
	})

	pubkeys := make([]string, 0)
	for _, b := range bl {
		for _, node := range b.BlameNodes {
			pubkeys = append(pubkeys, node.Pubkey)
		}
	}
	sort.SliceStable(pubkeys, func(i, j int) bool {
		return pubkeys[i] < pubkeys[j]
	})

	sb := strings.Builder{}
	for _, item := range members {
		sb.WriteString("m:" + item)
	}
	// IsSuccess is determined by whether the Blame slice itself is empty, not
	// by whether any blamed pubkeys are present. Bind that distinction
	// explicitly so a legitimate failure with zero BlameNodes cannot share a
	// voter with successful reports.
	if len(bl) == 0 {
		sb.WriteString("r:success")
	} else {
		sb.WriteString("r:failure")
	}
	for _, item := range pubkeys {
		sb.WriteString("p:" + item)
	}
	sb.WriteString(poolPk.String())
	sb.WriteString(fmt.Sprintf("%d", height))
	sb.WriteString("e:" + poolPubKeyEddsa.String())
	if includeXMRFields {
		sb.WriteString("f:" + poolPubKeyFrost.String())
		sb.WriteString(fmt.Sprintf("x:%d", observedMoneroHeight))
	}
	hash := sha256.New()
	_, err := hash.Write([]byte(sb.String()))
	if err != nil {
		return "", fmt.Errorf("fail to get tss id: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router handler for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented.
// No versioning is used there.
func (m *MsgTssPool) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if len(m.ID) == 0 {
		return cosmos.ErrUnknownRequest("ID cannot be blank")
	}
	minPubKeys := 2
	if m.KeygenType == KeygenType_XMRMonovaultKeygen {
		minPubKeys = 1
	}
	if len(m.PubKeys) < minPubKeys {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("Must have at least %d pub keys", minPubKeys))
	}
	if len(m.PubKeys) > 100 {
		return cosmos.ErrUnknownRequest("Must have no more then 100 pub keys")
	}
	// Validate blame nodes: all blamed pubkeys must be keygen participants and no duplicates.
	// This prevents a malicious quorum from spamming blame against non-participants.
	pubKeySet := make(map[string]struct{}, len(m.PubKeys))
	for _, pk := range m.PubKeys {
		pubKeySet[pk] = struct{}{}
	}
	seenBlameNodes := make(map[string]struct{})
	for _, b := range m.Blame {
		for _, node := range b.BlameNodes {
			if _, exists := pubKeySet[node.Pubkey]; !exists {
				return cosmos.ErrUnknownRequest("blame node not in keygen participants")
			}
			if _, seen := seenBlameNodes[node.Pubkey]; seen {
				return cosmos.ErrUnknownRequest("duplicate blame node")
			}
			seenBlameNodes[node.Pubkey] = struct{}{}
		}
	}
	pks := m.GetPubKeys()
	if len(m.PubKeys) != len(pks) {
		return cosmos.ErrUnknownRequest("One or more pubkeys were not valid")
	}
	isSignerInPubKeys := false
	for _, pk := range pks {
		if pk.IsEmpty() {
			return cosmos.ErrUnknownRequest("Pubkey cannot be empty")
		}
		signerAddress, err := pk.GetThorAddress()
		if err != nil {
			return cosmos.ErrUnknownRequest("invalid pub key")
		}
		if signerAddress.Equals(m.Signer) {
			isSignerInPubKeys = true
		}
	}
	if !isSignerInPubKeys {
		return cosmos.ErrUnknownRequest("signer is not part of keygen member")
	}
	// PoolPubKey can't be empty only when keygen success
	if m.IsSuccess() {
		if m.PoolPubKey.IsEmpty() {
			return cosmos.ErrUnknownRequest("Pool pubkey cannot be empty")
		}
	}
	// ensure pool pubkey is a valid bech32 pubkey
	if _, err := common.NewPubKey(m.PoolPubKey.String()); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.KeygenType == KeygenType_XMRMonovaultKeygen && m.IsSuccess() {
		if m.PoolPubKeyFrost.IsEmpty() {
			return cosmos.ErrUnknownRequest("xmr frost pool pubkey cannot be empty")
		}
		if _, err := m.PoolPubKeyFrost.GetAddress(common.XMRChain); err != nil {
			return cosmos.ErrUnknownRequest("invalid xmr frost pool pubkey")
		}
		if m.ObservedMoneroHeight <= 0 {
			return cosmos.ErrUnknownRequest("xmr observed monero height must be greater than zero")
		}
	}
	chains := m.GetChains()
	if len(chains) != len(m.Chains) {
		return cosmos.ErrUnknownRequest("One or more chains were not valid")
	}
	if !chains.Has(common.RuneAsset().Chain) {
		return cosmos.ErrUnknownRequest("must support rune asset chain")
	}
	if len(chains) != len(chains.Distinct()) {
		return cosmos.ErrUnknownRequest("cannot have duplicate chains")
	}

	if err := validateKeysharesBackup(m.KeysharesBackup, "keyshares backup", MinKeysharesBackupSize); err != nil {
		return err
	}
	if err := validateKeysharesBackup(m.KeysharesBackupEddsa, "eddsa keyshares backup", MinKeysharesBackupSize); err != nil {
		return err
	}
	if err := validateKeysharesBackup(m.KeysharesBackupFrost, "frost keyshares backup", MinFrostKeysharesBackupSize); err != nil {
		return err
	}

	return nil
}

func validateKeysharesBackup(backup []byte, label string, minSize int) error {
	if len(backup) == 0 {
		return nil
	}

	if MatchMnemonic.Match(backup) {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("invalid %s", label))
	}

	if len(backup) < minSize {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("invalid %s", label))
	}

	entropy := common.Entropy(backup)
	// analyze-ignore(float-comparison)
	if entropy < MinKeysharesBackupEntropy {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("invalid %s", label))
	}

	return nil
}

// IsSuccess when blame is empty , then treat it as success
func (m MsgTssPool) IsSuccess() bool {
	return len(m.Blame) == 0
}

func (m MsgTssPool) GetChains() common.Chains {
	chains := make(common.Chains, 0)
	for _, c := range m.Chains {
		chain, err := common.NewChain(c)
		if err != nil {
			continue
		}
		chains = append(chains, chain)
	}
	return chains
}

func (m MsgTssPool) GetPubKeys() common.PubKeys {
	pubkeys := make(common.PubKeys, 0)
	for _, pk := range m.PubKeys {
		pk, err := common.NewPubKey(pk)
		if err != nil {
			continue
		}
		pubkeys = append(pubkeys, pk)
	}
	return pubkeys
}

// GetSigners defines whose signature is required
func (m *MsgTssPool) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgTssPoolCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgTssPool)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgTssPool: %T", m)
	}
	return [][]byte{msg.Signer}, nil
}

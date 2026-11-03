package tss

import (
	"errors"
	"fmt"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var ErrNotSelectedForKeysign = errors.New("not selected for keysign party")

// KeysignError is a custom error create to include which party to blame
type KeysignError struct {
	Blame types.Blame
}

// NewKeysignError create a new instance of KeysignError
func NewKeysignError(blame types.Blame) KeysignError {
	return KeysignError{
		Blame: blame,
	}
}

// Error implement error interface
func (k KeysignError) Error() string {
	return fmt.Sprintf("fail to complete TSS keysign, reason:%s, round:%s, culprit:%+v", k.Blame.FailReason, k.Blame.Round, k.Blame.BlameNodes)
}

// IsJoinPartyFailure reports whether the keysign failed while forming the
// party, before any TSS round ran. No signature share has been produced, so
// the caller has not signed anything for this attempt.
//
// go-tss reports a join-party failure as TssSyncFail with no round. Every
// member computes the party leader from a hash of the messages, so members
// that built different messages wait on different leaders and all fail this
// way. That split can only heal if the members rebuild their messages.
func (k KeysignError) IsJoinPartyFailure() bool {
	return k.Blame.FailReason == blame.TssSyncFail && k.Blame.Round == ""
}

// IsTerminalRound reports whether a blame round has crossed the terminal
// keysign safety boundary for the algorithm: secp256k1 KEYSIGN9 (the round
// that emits the signature share in tss-lib v3), ed25519 EDDSAKEYSIGN3, or the
// FROST share/result phases.
//
// Only the terminal round matters for the signer's vault-stick retry path: a
// withholding-exploit node has the signature at hand after the terminal-round
// message has been computed locally, so failures earlier in the protocol can
// safely reschedule to a different vault.
//
// FROST's final coordinator phase is frost_result, but exchangeShares broadcasts
// the local signature share before it can return a frost_share failure. At that
// point another participant may already hold enough shares to aggregate and
// broadcast the transaction, so the signer must keep retrying with the same
// vault. frost_preprocess remains non-terminal because no signature share has
// escaped yet.
//
// Pre-v3 KEYSIGN7 blames are not accepted — a node running v3 will only ever
// emit KEYSIGN9 for its own terminal failures, and on-chain history with
// KEYSIGN7 markers is consumed by the chain-side freeze handler, not the
// per-signer retry path.
func (k KeysignError) IsTerminalRound() bool {
	round := types.CanonicalTssRound(k.Blame.Round, "")
	return round == tssMessages.KEYSIGN9 ||
		round == tssMessages.EDDSAKEYSIGN3 ||
		round == tssMessages.FROSTKEYSIGNSHARE ||
		round == tssMessages.FROSTKEYSIGNRESULT
}

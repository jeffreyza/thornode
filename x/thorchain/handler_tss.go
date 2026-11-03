package thorchain

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/btcsuite/btcd/btcec"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type TssHandler struct {
	mgr Manager
}

// NewTssHandler create a new handler to process MsgTssPool
func NewTssHandler(mgr Manager) TssHandler {
	return TssHandler{mgr: mgr}
}

// Run it the main entry point to execute Version logic
func (h TssHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgTssPool)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, msg); err != nil {
		ctx.Logger().Error("msg set version failed validation", "error", err)
		return nil, err
	}
	if err := h.handle(ctx, msg); err != nil {
		ctx.Logger().Error("fail to process msg set version", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

// verifySecp256K1Signature verifies the provided signature of the public key. This is
// set as a variable so tests can override verification when using random public keys.
var verifySecp256K1Signature = func(pk common.PubKey, sig []byte) error {
	// verify signature length
	if len(sig) != 64 {
		return fmt.Errorf("invalid secp256k1 signature length")
	}

	// build the signature
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])

	// reject high-S signatures (BIP-62) to prevent signature malleability
	halfOrder := new(big.Int).Rsh(btcec.S256().N, 1)
	if s.Cmp(halfOrder) == 1 {
		return fmt.Errorf("high-S signature rejected (BIP-62)")
	}

	signature := &btcec.Signature{R: r, S: s}

	// verify the signature
	spk, err := pk.Secp256K1()
	if err != nil {
		return fmt.Errorf("fail to get secp256k1 pubkey: %w", err)
	}
	if !signature.Verify([]byte(pk.String()), spk) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

func (h TssHandler) validate(ctx cosmos.Context, msg *MsgTssPool) error {
	// ValidateBasic is also executed in message service router's handler and isn't versioned there
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	if msg.KeygenType != AsgardKeygen && msg.KeygenType != XMRMonovaultKeygen {
		return fmt.Errorf("only asgard or xmr monovault keygens allowed for tss")
	}

	if !msg.PoolPubKeyEddsa.IsEmpty() {
		if _, err := common.NewPubKey(msg.PoolPubKeyEddsa.String()); err != nil {
			return cosmos.ErrUnknownRequest(err.Error())
		}
	}

	// handle() requires every reporter of an ID to agree on PoolPubKey,
	// PoolPubKeyEddsa, PoolPubKeyFrost and ObservedMoneroHeight, pinning them
	// from whichever message lands first. Each of those fields must therefore be
	// bound into the message ID (and so into the voter identity) or be pinned to
	// a fixed value here, otherwise one keygen member can pin bogus values and
	// every honest report is rejected before it is counted, stalling the keygen
	// (issue #2953). The FROST pubkey and the observed Monero height are only
	// part of the ID for XMR keygens, so reject asgard messages that set them -
	// bifrost never populates them for an asgard keygen, and a non-empty FROST
	// pubkey would additionally be written onto the asgard vault.
	if msg.KeygenType == AsgardKeygen {
		if !msg.PoolPubKeyFrost.IsEmpty() {
			return cosmos.ErrUnknownRequest("asgard keygen must not set frost pool pubkey")
		}
		if msg.ObservedMoneroHeight != 0 {
			return cosmos.ErrUnknownRequest("asgard keygen must not set observed monero height")
		}
	}

	var newMsg *MsgTssPool
	var err error
	if msg.KeygenType == XMRMonovaultKeygen {
		newMsg, err = NewMsgTssPoolV3(
			msg.PubKeys,
			msg.PoolPubKey,
			nil,
			nil,
			msg.KeygenType,
			msg.Height,
			msg.Blame,
			msg.Chains,
			msg.Signer,
			msg.KeygenTime,
			msg.PoolPubKeyEddsa,
			msg.KeysharesBackupEddsa,
			nil,
			msg.PoolPubKeyFrost,
			msg.ObservedMoneroHeight,
		)
	} else {
		newMsg, err = NewMsgTssPoolV2(msg.PubKeys, msg.PoolPubKey, nil, nil, msg.KeygenType, msg.Height, msg.Blame, msg.Chains, msg.Signer, msg.KeygenTime, msg.PoolPubKeyEddsa, msg.KeysharesBackupEddsa, nil, common.EmptyPubKey)
	}
	if err != nil {
		return fmt.Errorf("fail to recreate MsgTssPool,err: %w", err)
	}
	if msg.ID != newMsg.ID {
		return cosmos.ErrUnknownRequest("invalid tss message")
	}

	// A keygen result can only report a keygen block that has already been
	// reached: bifrost only runs a keygen once the chain scanner reaches its
	// height. Keygen blocks may be scheduled ahead of time (the genesis XMR
	// monovault keygen is scheduled 128 blocks out), so without this bound
	// members could vote on such a keygen before its height arrives and finalize
	// the vault early, defeating the scheduled delay (issue #3046).
	if msg.Height > ctx.BlockHeight() {
		return cosmos.ErrUnknownRequest("invalid keygen block")
	}

	keygenBlock, err := h.mgr.Keeper().GetKeygenBlock(ctx, msg.Height)
	if err != nil {
		return fmt.Errorf("fail to get keygen block from data store: %w", err)
	}
	keygenValidityBlocks := keygenResultValidityBlocksForBlock(ctx, h.mgr.Keeper(), msg.KeygenType, keygenBlock)
	// The window ends one block before the retry tick, so a result is rejected
	// exactly at the block the churn retry schedules a replacement keygen. Matches
	// ValidatorMgr.hasPendingKeygen; accepting a stale result there would let it
	// finalize a vault the retry is replacing (a double-trigger).
	if msg.Height < ctx.BlockHeight()-keygenValidityBlocks {
		return cosmos.ErrUnknownRequest("invalid keygen block")
	}

	// verify the check signatures if provided (only a subset of members in signing party)
	if len(msg.Secp256K1Signature) > 0 {
		err = verifySecp256K1Signature(msg.PoolPubKey, msg.Secp256K1Signature)
		if err != nil {
			ctx.Logger().Error(
				"invalid secp256k1 check signature",
				"err", err,
				"ID", msg.ID,
				"signer", msg.Signer.String(),
				"pubkey", msg.PoolPubKey,
				"signature", base64.StdEncoding.EncodeToString(msg.Secp256K1Signature),
			)
			return cosmos.ErrUnknownRequest("invalid secp256k1 check signature")
		}
	}

	for _, keygen := range keygenBlock.Keygens {
		keyGenMembers := keygen.GetMembers()
		if !msg.GetPubKeys().Equals(keyGenMembers) {
			continue
		}
		// Make sure the keygen type are consistent
		if msg.KeygenType != keygen.Type {
			continue
		}
		for _, member := range keygen.GetMembers() {
			addr, err := member.GetThorAddress()
			if err == nil && addr.Equals(msg.Signer) {
				return validateTssAuth(ctx, h.mgr.Keeper(), msg.Signer)
			}
		}
	}

	return cosmos.ErrUnauthorized("not authorized")
}

func keygenResultValidityBlocks(ctx cosmos.Context, k keeper.Keeper, keygenType types.KeygenType) int64 {
	blocks := k.GetConfigInt64(ctx, constants.ChurnRetryInterval)
	// An explicit zero disables churn retries. Preserve that operational
	// setting for both ordinary and XMR keygens instead of replacing it with
	// the default or the XMR minimum window.
	if blocks == 0 {
		return 0
	}
	if blocks < 0 {
		blocks = constants.NewConstantValue().GetInt64Value(constants.ChurnRetryInterval)
	}
	if keygenType == XMRMonovaultKeygen {
		blocks = max(blocks, durationToBlocks(6*time.Minute))
	}
	// The window ends one block before the retry tick. A keygen scheduled at H is
	// valid for ages 0..blocks-1 and expires at age `blocks`, the same age the
	// churn retry fires (ticks run every ChurnRetryInterval). Returning the full
	// interval instead kept the keygen valid AT the first tick, so hasPendingKeygen
	// suppressed that retry and the effective interval doubled (the off-by-one).
	// Subtracting one lets the first retry fire on schedule while a stale result is
	// already rejected on that block, so the retry cannot double-trigger a live keygen.
	return blocks - 1
}

func keygenResultValidityBlocksForBlock(ctx cosmos.Context, k keeper.Keeper, keygenType types.KeygenType, keygenBlock KeygenBlock) int64 {
	blocks := keygenResultValidityBlocks(ctx, k, keygenType)
	if keygenType != AsgardKeygen {
		return blocks
	}
	for _, keygen := range keygenBlock.Keygens {
		if keygen.Type == XMRMonovaultKeygen {
			return max(blocks, keygenResultValidityBlocks(ctx, k, XMRMonovaultKeygen))
		}
	}
	return blocks
}

func durationToBlocks(d time.Duration) int64 {
	if constants.ThorchainBlockTime <= 0 {
		return int64(d / time.Second)
	}
	blocks := d / constants.ThorchainBlockTime
	if d%constants.ThorchainBlockTime != 0 {
		blocks++
	}
	if blocks < 1 {
		return 1
	}
	return int64(blocks)
}

func validateTssAuth(ctx cosmos.Context, k keeper.Keeper, signer cosmos.AccAddress) error {
	nodeSigner, err := k.GetNodeAccount(ctx, signer)
	if err != nil {
		return fmt.Errorf("invalid signer")
	}
	if nodeSigner.IsEmpty() {
		return fmt.Errorf("invalid signer")
	}
	if nodeSigner.Status != NodeActive && nodeSigner.Status != NodeReady {
		return fmt.Errorf("invalid signer status(%s)", nodeSigner.Status)
	}
	// ensure we have enough rune
	minBond := k.GetConfigInt64(ctx, constants.MinimumBondInRune)
	if nodeSigner.Bond.LT(cosmos.NewUint(uint64(minBond))) {
		return fmt.Errorf("signer doesn't have enough rune")
	}
	return nil
}

// canonicalKeygenRoundLabel bounds the telemetry round label for keygen blame.
// Unlike MsgTssKeysignFail, MsgTssPool.ValidateBasic does not canonicalise
// Blame.Round, and CanonicalTssRound passes an unrecognised name through
// unchanged - so anything outside the known keygen rounds collapses to "other"
// rather than becoming an unbounded label value.
func canonicalKeygenRoundLabel(round string) string {
	switch types.CanonicalTssRound(round, "") {
	case tssMessages.KEYGEN1:
		return tssMessages.KEYGEN1
	case tssMessages.KEYGEN2aUnicast:
		return tssMessages.KEYGEN2aUnicast
	case tssMessages.KEYGEN2b:
		return tssMessages.KEYGEN2b
	case tssMessages.KEYGEN3:
		return tssMessages.KEYGEN3
	case tssMessages.EDDSAKEYGEN1:
		return tssMessages.EDDSAKEYGEN1
	case tssMessages.EDDSAKEYGEN2a:
		return tssMessages.EDDSAKEYGEN2a
	case tssMessages.EDDSAKEYGEN2b:
		return tssMessages.EDDSAKEYGEN2b
	default:
		return "other"
	}
}

func (h TssHandler) handle(ctx cosmos.Context, msg *MsgTssPool) error {
	ctx.Logger().Info("handler tss", "current version", h.mgr.GetVersion())
	blames := make([]string, 0)
	if len(msg.Blame) > 0 {
		var failReason string
		for _, b := range msg.Blame {
			for _, bn := range b.BlameNodes {
				pk, err := common.NewPubKey(bn.Pubkey)
				if err != nil {
					ctx.Logger().Error("fail to get tss keygen pubkey", "pubkey", bn.Pubkey, "error", err)
					continue
				}
				acc, err := pk.GetThorAddress()
				if err != nil {
					ctx.Logger().Error("fail to get tss keygen thor address", "pubkey", bn.Pubkey, "error", err)
					continue
				}
				blames = append(blames, acc.String())
			}
			if len(failReason) == 0 {
				failReason = b.FailReason
			} else {
				failReason = fmt.Sprintf("%s: %s", failReason, b.FailReason)
			}
		}
		sort.Strings(blames)
		ctx.Logger().Info(
			"tss keygen results blame",
			"height", msg.Height,
			"id", msg.ID,
			"pubkey_ecdsa", msg.PoolPubKey,
			"pubkey_eddsa", msg.PoolPubKeyEddsa,
			"blames", strings.Join(blames, ", "),
			"reason", failReason,
			"blamer", msg.Signer,
		)
	}
	// only record TSS metric when keygen is success
	if msg.IsSuccess() && !msg.PoolPubKey.IsEmpty() {
		metric, err := h.mgr.Keeper().GetTssKeygenMetric(ctx, msg.PoolPubKey)
		if err != nil {
			ctx.Logger().Error("fail to get keygen metric", "error", err)
		} else {
			ctx.Logger().Info("save keygen metric to db")
			metric.AddNodeTssTime(msg.Signer, msg.KeygenTime)
			h.mgr.Keeper().SetTssKeygenMetric(ctx, metric)
		}
	}
	voter, err := h.mgr.Keeper().GetTssVoter(ctx, msg.ID)
	if err != nil {
		return fmt.Errorf("fail to get tss voter: %w", err)
	}

	// when PoolPubKey is empty , which means TssVoter with id(msg.ID) doesn't
	// exist before, this is the first time to create it
	// set the PoolPubKey to the one in msg, there is no reason voter.PubKeys
	// have anything in it either, thus override it with msg.PubKeys as well
	if voter.PoolPubKey.IsEmpty() {
		voter.PoolPubKey = msg.PoolPubKey
		voter.PoolPubKeyEddsa = msg.PoolPubKeyEddsa
		voter.PoolPubKeyFrost = msg.PoolPubKeyFrost
		voter.ObservedMoneroHeight = msg.ObservedMoneroHeight
		voter.PubKeys = msg.PubKeys
	}
	// voter's pool pubkey is the same as the one in message
	if !voter.PoolPubKey.Equals(msg.PoolPubKey) ||
		!voter.PoolPubKeyEddsa.Equals(msg.PoolPubKeyEddsa) ||
		!voter.PoolPubKeyFrost.Equals(msg.PoolPubKeyFrost) ||
		voter.ObservedMoneroHeight != msg.ObservedMoneroHeight {
		return fmt.Errorf("invalid pool pubkey or monero height")
	}
	observeSlashPoints := h.mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := h.mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := h.mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_tss_pool"),
	}))

	if !voter.Sign(msg.Signer, msg.Chains, string(msg.Secp256K1Signature)) {
		// Slash for the network having to handle the extra message/s.
		h.mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, msg.Signer)
		ctx.Logger().Info("signer already signed MsgTssPool", "signer", msg.Signer.String(), "txid", msg.ID)
		return nil

	}
	h.mgr.Keeper().SetTssVoter(ctx, voter)

	// Record this reporter's declared chain set. voter.Sign concatenates every
	// member's chains, so per-member attribution only exists here, in the message.
	// NodeAccountPreflightCheck reads it to keep a node that reported without a chain
	// the active vaults serve out of the next Ready set, instead of the churn retrying
	// the same ceremony with the same member every ChurnRetryInterval. XMR monovault
	// reports carry a fixed chain set of their own and say nothing about the node's
	// asgard chain config, so they are not recorded.
	// An empty chain set is never what bifrost reports (keygenChainsForType always
	// carries at least THORChain), so treat it as a malformed report and keep the
	// node's previous record rather than recording a set that omits everything.
	if declaredChains := msg.GetChains(); msg.KeygenType == AsgardKeygen && len(declaredChains) > 0 {
		h.mgr.Keeper().SetLastKeygenChains(ctx, msg.Signer, declaredChains, msg.Height)
	}

	if !voter.HasConsensus() {
		// Slash until 2/3rds consensus.
		h.mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, msg.Signer)
		return nil
	}

	if voter.BlockHeight > 0 && (voter.BlockHeight+observeFlex) >= ctx.BlockHeight() {
		// After 2/3rds consensus, only decrement slash points if within the ObservationDelayFlexibility period.
		// (This is expected to only apply for a failed keygen.)
		h.mgr.Slasher().DecSlashPoints(slashCtx, lackOfObservationPenalty, msg.Signer)
	}

	if voter.BlockHeight == 0 {
		// This message brings the voter to 2/3rds consensus.
		// For an IsSuccess() message, BlockHeight and MajorityConsensusBlockHeight will initially be the same.
		voter.BlockHeight = ctx.BlockHeight()
		h.mgr.Keeper().SetTssVoter(ctx, voter)

		// A list of keygen node accounts isn't readily available,
		// so (rather than do a KVStore-check GetNodeAccount)
		// prepare the non-signer AccAddresses manually.
		signers := voter.GetSigners()
		var keygenNodeAccAddresses []cosmos.AccAddress
		for _, member := range msg.PubKeys {
			pkey, err := common.NewPubKey(member)
			if err != nil {
				ctx.Logger().Error("fail to get pub key", "error", err)
				continue
			}
			thorAddr, err := pkey.GetThorAddress()
			if err != nil {
				ctx.Logger().Error("fail to get thor address", "error", err)
				continue
			}
			keygenNodeAccAddresses = append(keygenNodeAccAddresses, thorAddr)
		}
		var nonSigners []cosmos.AccAddress
		var signed bool
		for _, keygenNodeAccAddress := range keygenNodeAccAddresses {
			signed = false
			for _, signer := range signers {
				if keygenNodeAccAddress.Equals(signer) {
					signed = true
					break
				}
			}

			if !signed {
				nonSigners = append(nonSigners, keygenNodeAccAddress)
			}
		}

		// As this signer brings the voter to 2/3rds consensus,
		// increment the signer's slash points like the before-consensus signers,
		// then decrement all the signers' slash points and increment the non-signers' slash points.
		h.mgr.Slasher().IncSlashPoints(slashCtx, observeSlashPoints, msg.Signer)
		h.mgr.Slasher().DecSlashPoints(slashCtx, observeSlashPoints, signers...)
		h.mgr.Slasher().IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)

		// Do the below only for a non-success message upon 2/3rds consensus.
		if !msg.IsSuccess() {
			// One increment per failed keygen reaching 2/3rds consensus. The
			// existing failed_keygen labels live on thornode_point_slash and
			// thornode_bond_slash, which fire per BLAMED node (and bond_slash only
			// for a non-zero amount), so a keygen failure with an empty Blame list
			// emits nothing at all today and one blaming 5 nodes looks like 5.
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "keygen_failure"},
				1,
				[]metrics.Label{telemetry.NewLabel("keygen_type", msg.KeygenType.String())},
			)
			// Per-round/unicast breakdown. Emitted here rather than in the blame
			// loop below, because a Blame entry carrying zero BlameNodes never
			// enters that loop and so produces no attributable signal at all.
			for _, b := range msg.Blame {
				telemetry.IncrCounterWithLabels(
					[]string{"thornode", "tss", "keygen", "failure"},
					1,
					[]metrics.Label{
						telemetry.NewLabel("keygen_type", msg.KeygenType.String()),
						telemetry.NewLabel("round", canonicalKeygenRoundLabel(b.Round)),
						telemetry.NewLabel("is_unicast", fmt.Sprintf("%t", b.IsUnicast)),
					},
				)
			}

			// since the keygen failed, it's now safe to reset all nodes in
			// ready status back to standby status
			ready, err := h.mgr.Keeper().ListValidatorsByStatus(ctx, NodeReady)
			if err != nil {
				ctx.Logger().Error("fail to get list of ready node accounts", "error", err)
			}
			for _, na := range ready {
				na.UpdateStatus(NodeStandby, ctx.BlockHeight())
				if err := h.mgr.Keeper().SetNodeAccount(ctx, na); err != nil {
					ctx.Logger().Error("fail to set node account", "error", err)
				}
			}

			// if a node fail to join the keygen, thus hold off the network
			// from churning then it will be slashed accordingly
			slashPoints := h.mgr.GetConstants().GetInt64Value(constants.FailKeygenSlashPoints)
			for _, b := range msg.Blame {
				for _, node := range b.BlameNodes {
					nodePubKey, err := common.NewPubKey(node.Pubkey)
					if err != nil {
						return ErrInternal(err, fmt.Sprintf("fail to parse pubkey(%s)", node.Pubkey))
					}

					na, err := h.mgr.Keeper().GetNodeAccountByPubKey(ctx, nodePubKey)
					if err != nil {
						return fmt.Errorf("fail to get node from it's pub key: %w", err)
					}
					// Which node is blamed for the keygen failure. Blamed non-Active
					// nodes take the jail+bond-slash branch below, which emits
					// thornode_bond_slash only when the slash amount is non-zero, so
					// a blamed standby node with no rewards owed is invisible today.
					// Count both branches here.
					telemetry.IncrCounterWithLabels(
						[]string{"thornode", "tss", "keygen", "blame"},
						1,
						[]metrics.Label{
							telemetry.NewLabel("node_address", na.NodeAddress.String()),
							telemetry.NewLabel("round", canonicalKeygenRoundLabel(b.Round)),
							telemetry.NewLabel("keygen_type", msg.KeygenType.String()),
						},
					)

					if na.Status == NodeActive {
						failedKeygenSlashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
							telemetry.NewLabel("reason", "failed_keygen"),
						}))
						if err := h.mgr.Keeper().IncNodeAccountSlashPoints(failedKeygenSlashCtx, na.NodeAddress, slashPoints); err != nil {
							ctx.Logger().Error("fail to inc slash points", "error", err)
						}

						if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventSlashPoint(na.NodeAddress, slashPoints, "fail keygen")); err != nil {
							ctx.Logger().Error("fail to emit slash point event")
						}
					} else {
						// go to jail
						jailTime := h.mgr.GetConstants().GetInt64Value(constants.JailTimeKeygen)
						releaseHeight := ctx.BlockHeight() + jailTime
						reason := "failed to perform keygen"
						if err := h.mgr.Keeper().SetNodeAccountJail(ctx, na.NodeAddress, releaseHeight, reason); err != nil {
							ctx.Logger().Error("fail to set node account jail", "node address", na.NodeAddress, "reason", reason, "error", err)
						}

						network, err := h.mgr.Keeper().GetNetwork(ctx)
						if err != nil {
							return fmt.Errorf("fail to get network: %w", err)
						}

						slashBond := network.CalcNodeRewards(cosmos.NewUint(uint64(slashPoints)))
						if slashBond.GT(na.Bond) {
							slashBond = na.Bond
						}
						ctx.Logger().Info("fail keygen , slash bond", "address", na.NodeAddress, "amount", slashBond.String())
						// take out bond from the node account and add it to the Reserve
						// thus good behaviour nodes and liquidity providers will get reward
						na.Bond = common.SafeSub(na.Bond, slashBond)
						coin := common.NewCoin(common.RuneNative, slashBond)
						if !coin.Amount.IsZero() {
							if err := h.mgr.Keeper().SendFromModuleToModule(ctx, BondName, ReserveName, common.NewCoins(coin)); err != nil {
								return fmt.Errorf("fail to transfer funds from bond to reserve: %w", err)
							}
							slashFloat, _ := new(big.Float).SetInt(slashBond.BigInt()).Float32()
							telemetry.IncrCounterWithLabels(
								[]string{"thornode", "bond_slash"},
								slashFloat,
								[]metrics.Label{
									telemetry.NewLabel("address", na.NodeAddress.String()),
									telemetry.NewLabel("reason", "failed_keygen"),
								},
							)
						}

						bondEvent := NewEventBond(slashBond, BondCost, common.Tx{}, &na, nil)
						if err := h.mgr.EventMgr().EmitEvent(ctx, bondEvent); err != nil {
							return fmt.Errorf("fail to emit bond event: %w", err)
						}
					}
					if err := h.mgr.Keeper().SetNodeAccount(ctx, na); err != nil {
						return fmt.Errorf("fail to save node account: %w", err)
					}
				}

				if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventTssKeygenFailure(b.FailReason, b.Round, b.IsUnicast, msg.Height, blames)); err != nil {
					ctx.Logger().Error("fail to emit keygen failure event")
				}
			}
		}
	}

	// when keygen success
	if msg.IsSuccess() {
		// Separately from the usual consensus-agreement slash points,
		// those who haven't agreed with a consensus success message incur FailKeygenSlashPoints until agreement.
		judgeLateSigner(ctx, h.mgr, msg, &voter)

		// Finalize once at least len(PubKeys) - MaxAbsentKeygenReporters members have reported
		// success. During churn, when ready nodes are being promoted into the new vault,
		// finalize on first supermajority so one delayed ready-node observation does not stall
		// rotation forever. The dedicated XMR monovault also finalizes on first supermajority.
		// The VaultExists check is the re-entry guard: late reporters arriving after
		// finalization must not re-create the vault or re-trigger churn.
		maxAbsent := maxAbsentKeygenReporters(ctx, h.mgr.Keeper())
		reportsRequired := int64(len(voter.PubKeys)) - maxAbsent
		shouldFinalizeVault := int64(len(voter.Signers)) >= reportsRequired
		if msg.KeygenType == AsgardKeygen && voter.HasConsensus() && !asgardKeygenRequiresCompleteConsensus(ctx, h.mgr.Keeper(), voter) {
			shouldFinalizeVault = true
		}
		if msg.KeygenType == XMRMonovaultKeygen && voter.HasConsensus() {
			shouldFinalizeVault = true
		}

		// A vault finalized at first supermajority (above) computed its chain set
		// from as few as 2/3 of the reports, so a single reporter running with a
		// chain disabled leaves that chain one vote short and it is dropped even
		// though every remaining report carries it. Reconsider the chain set as
		// the late reports land. This must stay after the voter.Sign/SetTssVoter
		// above so the recomputed set includes the report being handled.
		if msg.KeygenType == AsgardKeygen && h.mgr.Keeper().VaultExists(ctx, voter.PoolPubKey) {
			return h.refreshInitVaultChains(ctx, msg, voter)
		}

		if shouldFinalizeVault && !h.mgr.Keeper().VaultExists(ctx, voter.PoolPubKey) {
			ctx.Logger().Info(
				"tss keygen results success",
				"height", msg.Height,
				"id", msg.ID,
				"pubkey", msg.PoolPubKey,
			)

			if msg.KeygenType == AsgardKeygen {
				// we must also have quorum on the check signature
				consensusSig, ok := voter.ConsensusCheckSignature()
				if !ok {
					ctx.Logger().Error("keygen rejected due to lacking check signature quorum")
					return nil
				}

				// log an error if any bad nodes submitted a mismatched check signature
				for _, sig := range voter.Secp256K1Signatures {
					if sig != consensusSig {
						ctx.Logger().Error(
							"mismatched check signature detected",
							"expected", base64.StdEncoding.EncodeToString([]byte(consensusSig)),
							"found", base64.StdEncoding.EncodeToString([]byte(sig)),
						)
					}
				}
			}

			// Update the BlockHeight to reflect the newly reached state.
			voter.BlockHeight = ctx.BlockHeight()
			h.mgr.Keeper().SetTssVoter(ctx, voter)

			vaultType := AsgardVault
			chains := voter.ConsensusChains()
			if msg.KeygenType == XMRMonovaultKeygen {
				chains = xmrMonovaultChains()
			}
			vaultPubKeyEddsa := voter.PoolPubKeyEddsa
			if msg.KeygenType == XMRMonovaultKeygen {
				if voter.PoolPubKeyFrost.IsEmpty() {
					return fmt.Errorf("xmr monovault keygen requires pool_pub_key_frost")
				}
				if voter.ObservedMoneroHeight <= 0 {
					return fmt.Errorf("xmr monovault keygen requires observed_monero_height")
				}
				vaultPubKeyEddsa = common.EmptyPubKey
			}
			vault := NewVaultV2(ctx.BlockHeight(), InitVault, vaultType, voter.PoolPubKey, chains.Strings(), h.mgr.Keeper().GetChainContracts(ctx, chains), vaultPubKeyEddsa)
			vault.PubKeyFrost = voter.PoolPubKeyFrost
			vault.Membership = voter.PubKeys
			if vault.GetChains().Has(common.XMRChain) {
				// Round 3 already applies the reorg margin to its quorum-derived,
				// transcript-bound birthday. Persist that exact value so a newer
				// global height cannot skip early vault outputs.
				xmrHeight := voter.ObservedMoneroHeight
				if xmrHeight > 0 {
					ctx.Logger().Info("XMR vault birthday: using transcript-bound Monero height from keygen", "height", xmrHeight)
				} else {
					xmrHeight, _ = h.mgr.Keeper().GetLastChainHeight(ctx, common.XMRChain)
				}
				if xmrHeight <= 0 {
					ctx.Logger().Error("XMR vault birthday: no observed chain height available at keygen, vault scanner will use chain tip")
				}
				vault.VaultCreationHeightXmr = xmrHeight
			}

			if err := h.mgr.Keeper().SetVault(ctx, vault); err != nil {
				return fmt.Errorf("fail to save vault: %w", err)
			}
			keygenBlock, err := h.mgr.Keeper().GetKeygenBlock(ctx, msg.Height)
			if err != nil {
				return fmt.Errorf("fail to get keygen block, err: %w, height: %d", err, msg.Height)
			}
			var metric *keeper.TssKeygenMetric
			metric, err = h.mgr.Keeper().GetTssKeygenMetric(ctx, msg.PoolPubKey)
			if err != nil {
				ctx.Logger().Error("fail to get keygen metric", "error", err)
			} else {
				var total int64
				for _, item := range metric.NodeTssTimes {
					total += item.TssTime
				}
				evt := NewEventTssKeygenMetric(metric.PubKey, metric.GetMedianTime())
				if err = h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
					ctx.Logger().Error("fail to emit tss metric event", "error", err)
				}
			}

			if rotateErr := h.rotateReadyKeygenVaultFamilies(ctx, keygenBlock); rotateErr != nil {
				return rotateErr
			}

			addrs, err := vault.GetMembership().Addresses()
			members := make([]string, len(addrs))
			if err != nil {
				ctx.Logger().Error("fail to get member addresses", "error", err)
			} else {
				for i, addr := range addrs {
					members[i] = addr.String()
				}
				if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventTssKeygenSuccess(msg.PoolPubKey, msg.Height, members)); err != nil {
					ctx.Logger().Error("fail to emit keygen success event")
				}
			}

			// Counts a keygen that actually finalized a vault, so
			// thornode_churn_keygen_failure has a matching denominator on the same
			// keygen_type dimension. Keygen success is unmetered today. Emitted
			// outside the members if/else so an Addresses() failure - unrelated to
			// the keygen outcome - does not silently drop the success, and after
			// the last fallible step so a vault that never finalized is not counted.
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "churn", "keygen_success"},
				1,
				[]metrics.Label{telemetry.NewLabel("keygen_type", msg.KeygenType.String())},
			)
		}
	}

	return nil
}

// refreshInitVaultChains handles a keygen success report that arrives after the
// vault was already finalized. Finalizing on first supermajority means
// ConsensusChains() ran against as few as 2/3 of the members, so a chain needs
// unanimity among those early reporters to survive - one member with the chain
// disabled in its bifrost config is enough to drop it, stalling the churn on the
// tryRotateInitVaults chain set guard. Widening the set as the remaining reports
// land repairs that, and rotation is re-attempted because the widening is what
// makes the init vaults agree.
//
// Widening only ever adds: Sign appends and the denominator len(PubKeys) is
// fixed, so ConsensusChains() is monotone non-decreasing.
//
// It deliberately does not re-emit the keygen success event, re-run the keygen
// metric, re-check the check signature quorum, or restamp voter.BlockHeight -
// restamping would re-open the ObservationDelayFlexibility window and refund
// slash points to arbitrarily late reporters.
//
// ponytail: a chain still under threshold at the instant rotation succeeds is
// excluded for the life of the vault. Rotation only fires once every init vault
// of the keygen block agrees, so that needs a dissenter inside the early
// reporters of every group - out of reach for a couple of misconfigured nodes.
func (h TssHandler) refreshInitVaultChains(ctx cosmos.Context, msg *MsgTssPool, voter TssVoter) error {
	vault, err := h.mgr.Keeper().GetVault(ctx, voter.PoolPubKey)
	if err != nil {
		return fmt.Errorf("fail to get vault: %w", err)
	}
	if vault.Status != InitVault {
		// Already rotated or retired. NetworkMgr.RotateVault is not idempotent -
		// a second call matches the now-Active vault against its own membership
		// and sets it Retiring - so stop here.
		return nil
	}

	// Start from the sorted consensus set and keep whatever the vault already
	// carries: AddFunds appends a chain when a coin for it is observed, and an
	// inbound must not be undone by a later keygen report.
	merged := voter.ConsensusChains()
	for _, c := range vault.GetChains() {
		if !merged.Has(c) {
			merged = append(merged, c)
		}
	}
	// Keep the sorted order ConsensusChains() produces: initVaultsAgreeOnChains
	// compares the slices positionally.
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].String() < merged[j].String() })
	if slices.Equal(merged.Strings(), vault.Chains) {
		return nil
	}

	ctx.Logger().Info("init vault chain set widened by late keygen report",
		"pubkey", vault.PubKey.String(),
		"before", strings.Join(vault.Chains, ","),
		"after", strings.Join(merged.Strings(), ","),
	)
	vault.Chains = merged.Strings()
	// Routers are only recomputed for an active vault when Routers is entirely
	// nil, so a chain added here would otherwise never get its router.
	vault.Routers = h.mgr.Keeper().GetChainContracts(ctx, merged)
	if err = h.mgr.Keeper().SetVault(ctx, vault); err != nil {
		return fmt.Errorf("fail to save vault: %w", err)
	}

	keygenBlock, err := h.mgr.Keeper().GetKeygenBlock(ctx, msg.Height)
	if err != nil {
		return fmt.Errorf("fail to get keygen block, err: %w, height: %d", err, msg.Height)
	}
	// Return rather than log on failure: tryRotateInitVaults returns nil for a
	// chain set mismatch, so an error here is a keeper failure partway through
	// the per-vault rotation loop. Reverting the tx is better than leaving the
	// churn half rotated.
	return h.rotateReadyKeygenVaultFamilies(ctx, keygenBlock)
}

// tryRotateInitVaults rotates every vault into Active, or skips and emits
// a security event if their Chains disagree. vault.Chains is immutable once
// Active, so an asymmetric set would leave some Asgards permanently
// without an address for a chain. ChurnRetryInterval retries on skip.
func (h TssHandler) tryRotateInitVaults(ctx cosmos.Context, initVaults Vaults) error {
	if !initVaultsAgreeOnChains(initVaults) {
		chainSets := make([]string, len(initVaults))
		for i, v := range initVaults {
			chainSets[i] = fmt.Sprintf("%s:[%s]", v.PubKey.String(), strings.Join(v.Chains, ","))
		}
		secMsg := fmt.Sprintf("init vault chain set mismatch, skipping rotation: %s", strings.Join(chainSets, " | "))
		ctx.Logger().Error(secMsg)
		if err := h.mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(common.Tx{ID: common.BlankTxID}, secMsg)); err != nil {
			ctx.Logger().Error("fail to emit security event", "error", err)
		}
		return nil
	}
	for _, v := range initVaults {
		if err := h.mgr.NetworkMgr().RotateVault(ctx, v); err != nil {
			return fmt.Errorf("fail to rotate vault: %w", err)
		}
	}
	return nil
}

// initVaultsAgreeOnChains returns true if every vault's Chains slice is
// identical. ConsensusChains() sorts before returning, so a positional
// equality check is sufficient.
func initVaultsAgreeOnChains(vaults Vaults) bool {
	if len(vaults) < 2 {
		return true
	}
	ref := vaults[0].Chains
	for _, v := range vaults[1:] {
		if !slices.Equal(v.Chains, ref) {
			return false
		}
	}
	return true
}

func asgardKeygenRequiresCompleteConsensus(ctx cosmos.Context, k keeper.Keeper, voter TssVoter) bool {
	for _, memberPubKey := range voter.GetPubKeys() {
		nodeAccount, err := k.GetNodeAccountByPubKey(ctx, memberPubKey)
		if err != nil {
			return true
		}
		if nodeAccount.Status != NodeActive {
			return false
		}
	}
	return true
}

// maxAbsentKeygenReporters returns the configured tolerance for absent keygen
// reports, falling back to constants when mimir is unset or unavailable.
func maxAbsentKeygenReporters(ctx cosmos.Context, k keeper.Keeper) int64 {
	maxAbsent := k.GetConfigInt64(ctx, constants.MaxAbsentKeygenReporters)
	if maxAbsent < 0 {
		return 0
	}
	return maxAbsent
}

func (h TssHandler) rotateReadyKeygenVaultFamilies(ctx cosmos.Context, keygenBlock KeygenBlock) error {
	activeVaults, err := h.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return fmt.Errorf("fail to get active vaults: %w", err)
	}
	initVaults, err := h.mgr.Keeper().GetAsgardVaultsByStatus(ctx, InitVault)
	if err != nil {
		return fmt.Errorf("fail to get init vaults: %w", err)
	}

	readyVaults := append(append(Vaults(nil), activeVaults...), initVaults...)
	if keygenBlockHasType(keygenBlock, AsgardKeygen) {
		regularInitVaults := filterVaultsByKeygenType(initVaults, keygenBlock, AsgardKeygen)
		if keygenTypeReady(keygenBlock, readyVaults, AsgardKeygen) {
			if len(regularInitVaults) > 0 {
				ctx.Logger().Info("tss keygen results churn", "asgards", len(regularInitVaults))
				if rotateErr := h.tryRotateInitVaults(ctx, regularInitVaults); rotateErr != nil {
					return fmt.Errorf("fail to rotate regular vaults: %w", rotateErr)
				}
				activeVaults, err = h.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
				if err != nil {
					return fmt.Errorf("fail to refresh active vaults: %w", err)
				}
				initVaults, err = h.mgr.Keeper().GetAsgardVaultsByStatus(ctx, InitVault)
				if err != nil {
					return fmt.Errorf("fail to refresh init vaults: %w", err)
				}
				readyVaults = append(append(Vaults(nil), activeVaults...), initVaults...)
			}
		} else {
			ctx.Logger().Info("not enough regular keygen yet")
		}
	}

	if keygenBlockHasType(keygenBlock, XMRMonovaultKeygen) {
		xmrInitVaults := filterVaultsByKeygenType(initVaults, keygenBlock, XMRMonovaultKeygen)
		if keygenTypeReady(keygenBlock, readyVaults, XMRMonovaultKeygen) {
			if len(xmrInitVaults) > 0 {
				ctx.Logger().Info("tss keygen results xmr monovault", "vaults", len(xmrInitVaults))
				if rotateErr := h.tryRotateInitVaults(ctx, xmrInitVaults); rotateErr != nil {
					return fmt.Errorf("fail to rotate xmr monovault: %w", rotateErr)
				}
			}
		} else {
			ctx.Logger().Info("not enough xmr keygen yet")
		}
	}

	return nil
}

// judgeLateSigner penalises keygen members that have not agreed with a
// consensus success message and releases them once they do.
//
// It takes the voter by pointer and mutates MajorityConsensusBlockHeight on the
// caller's voter so the vault-finalize SetTssVoter in handle() persists the
// field instead of re-writing a stale zero copy over it. That persistence is
// what makes the late-signer release branch (MajorityConsensusBlockHeight > 0)
// reachable. Previously the field was set on a by-value copy and clobbered by
// the finalize SetTssVoter, so an honest member whose success report landed one
// block after consensus stayed jailed for JailTimeKeygen and every later report
// re-jailed the remaining absentees (issue #3092, defect 2).
func judgeLateSigner(ctx cosmos.Context, mgr Manager, msg *MsgTssPool, voter *TssVoter) {
	// if the voter doesn't reach 2/3 majority consensus , this method should not take any actions
	if !voter.HasConsensus() || !msg.IsSuccess() {
		return
	}
	slashPoints := mgr.GetConstants().GetInt64Value(constants.FailKeygenSlashPoints)
	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_tss_pool"),
	}))

	// when voter already has 2/3 majority signers , restore current message signer's slash points
	if voter.MajorityConsensusBlockHeight > 0 {
		mgr.Slasher().DecSlashPoints(slashCtx, slashPoints, msg.Signer)
		// XMR monovault keygen is slash-only and never jails, so it must not
		// release any jail. The jail reason is a generic string not tied to a
		// keygen ID, so a late XMR report could otherwise clear a "failed to vote
		// keygen in time" jail imposed by a different (e.g. Asgard) keygen in a
		// shared block. For other keygen types only lift the keygen-vote jail this
		// path imposed. This protects a later, longer jail with a different
		// reason that replaced the keygen-vote sentence. Jail stores only one
		// sentence, so it cannot preserve a shorter overlapping sentence that
		// SetNodeAccountJail already replaced or ignored.
		if msg.KeygenType.JailsLateSigners() {
			if jail, jerr := mgr.Keeper().GetNodeAccountJail(ctx, msg.Signer); jerr != nil {
				ctx.Logger().Error("fail to get node account jail", "node address", msg.Signer, "error", jerr)
			} else if jail.Reason == jailReasonFailedToVoteKeygen {
				if err := mgr.Keeper().ReleaseNodeAccountFromJail(ctx, msg.Signer); err != nil {
					ctx.Logger().Error("fail to release node account from jail", "node address", msg.Signer, "error", err)
				}
			}
		}
		return
	}

	voter.MajorityConsensusBlockHeight = ctx.BlockHeight()
	mgr.Keeper().SetTssVoter(ctx, *voter)
	for _, member := range msg.PubKeys {
		pkey, err := common.NewPubKey(member)
		if err != nil {
			ctx.Logger().Error("fail to get pub key", "error", err)
			continue
		}
		thorAddr, err := pkey.GetThorAddress()
		if err != nil {
			ctx.Logger().Error("fail to get thor address", "error", err)
			continue
		}
		// whoever is in the keygen list , but didn't broadcast MsgTssPool
		if !voter.HasSigned(thorAddr) {
			mgr.Slasher().IncSlashPoints(slashCtx, slashPoints, thorAddr)
			// XMR monovault keygen runs at large scale (~95 members), where
			// per-round timeout skew leaves honest members briefly behind
			// through no fault of their own. Removing them from the active set
			// for JailTimeKeygen (~7h) can drop a small active set below quorum,
			// so for this keygen type accrue slash points only and do not jail.
			// Other keygen types keep the jail penalty. Members that report the
			// success late still get these slash points refunded by the release
			// branch above.
			if !msg.KeygenType.JailsLateSigners() {
				continue
			}
			// go to jail
			jailTime := mgr.GetConstants().GetInt64Value(constants.JailTimeKeygen)
			releaseHeight := ctx.BlockHeight() + jailTime
			reason := jailReasonFailedToVoteKeygen
			if err := mgr.Keeper().SetNodeAccountJail(ctx, thorAddr, releaseHeight, reason); err != nil {
				ctx.Logger().Error("fail to set node account jail", "node address", thorAddr, "reason", reason, "error", err)
			}
		}
	}
}

// TssAnteHandler called by the ante handler to gate mempool entry
// and also during deliver. Store changes will persist if this function
// succeeds, regardless of the success of the transaction.
func TssAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgTssPool) (cosmos.Context, error) {
	err := validateTssAuth(ctx, k, msg.Signer)
	if err != nil {
		return ctx, err
	}

	return ctx.WithPriority(ActiveNodePriority), nil
}

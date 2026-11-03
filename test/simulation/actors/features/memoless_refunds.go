package features

import (
	"fmt"
	rand "math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// MemolessRefunds
////////////////////////////////////////////////////////////////////////////////////////

//go:generate stringer -type=MemolessRefundScenario
type MemolessRefundScenario int

const (
	MemolessRefundUnparsable       MemolessRefundScenario = iota // garbled memo
	MemolessRefundNonexistentRef                                 // r:XXXXX with no registered memo
	MemolessRefundExpiredRef                                     // r:XXXXX after TTL
	MemolessRefundUnpoolableTarget                               // valid ref, resolved memo targets asset with no pool
)

// unpoolableAsset is a synthetic ERC20 that is guaranteed to have no pool in
// mocknet. Format is valid so memo parsing succeeds, but the swap handler will
// fail to locate the pool and refund the inbound.
const unpoolableAsset = "ETH.FAKE-0xDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF"

// mocknetRefCeiling matches MemolessTxnRefCount from constants_v1.go. Using a
// reference in the top of the range minimizes the chance of colliding with a
// reference allocated by other actors during the same sim run.
const mocknetRefCeiling = 99_999

// MemolessRefunds builds a child actor per (scenario, ERC20 asset) pair. Only
// non-gas EVM assets are enqueued because Section B covers the Path A router
// failure path, which exists only for ERC20 inbounds via depositWithExpiry.
//
// The aggregator runs three sequential stages so the ExpiredRef scenario stays
// within the DAG's per-actor timeout:
//  1. Lower MEMOLESSTXNTTL to 10 blocks so an expired reference is reached
//     in ~10s instead of ~100s. MEMOLESSTXNTTL is an economic mimir, so the
//     lower stage broadcasts MsgMimir from every active node to reach
//     supermajority (same technique Ragnarok uses).
//  2. Run the refund actors in parallel.
//  3. Restore MEMOLESSTXNTTL to the mocknet default (100) so downstream stages
//     see the original configuration.
//
// ExpiredRef is intentionally scoped to a single chain (ETH). The TTL-expiry
// behaviour is chain-agnostic, so running it on all EVM chains would inflate
// the stage time without adding coverage.
func MemolessRefunds(rng *rand.Rand) *Actor {
	a := NewActor("Feature-MemolessRefunds", rng)

	ttlKey := constants.MemolessTxnTTL.String()
	lowerTTL := NewActor("Memoless Refund Lower TTL", rng)
	lowerTTL.Children[NewMultiNodeMimirActor(ttlKey, 10, rng)] = true

	refundActors := NewActor("Memoless Refund Actors", rng)
	for _, chain := range acommon.SimChains {
		if !chain.IsEVM() {
			continue
		}
		for asset := range evm.Tokens(chain) {
			if asset.IsGasAsset() {
				continue
			}
			for _, s := range []MemolessRefundScenario{
				MemolessRefundUnparsable,
				MemolessRefundNonexistentRef,
				MemolessRefundUnpoolableTarget,
			} {
				refundActors.Children[NewMemolessRefundActor(s, asset, rng)] = true
			}
			if chain.Equals(common.ETHChain) {
				refundActors.Children[NewMemolessRefundActor(MemolessRefundExpiredRef, asset, rng)] = true
			}
		}
	}

	restoreTTL := NewActor("Memoless Refund Restore TTL", rng)
	restoreTTL.Children[NewMultiNodeMimirActor(ttlKey, 100, rng)] = true

	a.Append(lowerTTL)
	a.Append(refundActors)
	a.Append(restoreTTL)

	return a
}

// NewMultiNodeMimirActor broadcasts a MsgMimir from every active node so that
// an economic mimir reaches supermajority. Its op ends with Continue, so callers
// may append ops, such as waiting for the value to take effect. Single-node MimirActor is insufficient
// on the mocknet-cluster where MEMOLESSTXNTTL stays at its default if only one
// vote is cast. Pattern borrowed from core.RagnarokPoolActor.sendMimir.
func NewMultiNodeMimirActor(key string, value int64, rng *rand.Rand) *Actor {
	a := NewActor(fmt.Sprintf("Mimir(all) %s=%d", key, value), rng)
	a.Timeout = 2 * time.Minute
	a.Ops = append(a.Ops, func(config *OpConfig) OpResult {
		nodes, err := thornode.GetNodes()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get nodes")
			return OpResult{Continue: false, Error: err}
		}
		activeNodes := make(map[string]bool)
		for _, n := range nodes {
			if n.Status == atypes.NodeStatus_Active.String() {
				activeNodes[n.NodeAddress] = true
			}
		}

		for _, node := range config.NodeUsers {
			accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
			if err != nil {
				a.Log().Error().Err(err).Msg("failed to get thor address")
				return OpResult{Continue: false, Error: err}
			}
			if !activeNodes[accAddr.String()] {
				a.Log().Debug().Stringer("signer", accAddr).Msg("skipping inactive node")
				continue
			}
			msg := types.NewMsgMimir(key, value, accAddr)
			txid, err := node.Thorchain.Broadcast(msg)
			if err != nil {
				a.Log().Error().Err(err).Stringer("signer", accAddr).Msg("failed to broadcast mimir")
				return OpResult{Continue: false, Error: err}
			}
			a.Log().Info().Stringer("signer", accAddr).Stringer("txid", txid).Msg("broadcasted mimir vote")
		}
		return OpResult{Continue: true}
	})
	return a
}

////////////////////////////////////////////////////////////////////////////////////////
// MemolessRefundActor
////////////////////////////////////////////////////////////////////////////////////////

type MemolessRefundActor struct {
	Actor

	scenario MemolessRefundScenario
	asset    common.Asset
	user     *User
	amount   cosmos.Uint

	// depositMemo is what the actor sends in the router depositWithExpiry call.
	depositMemo string

	// registration-related state (populated only for scenarios that pre-register a memo).
	registrationTxID common.TxID
	registeredBody   string
	registeredHeight int64
	memolessRef      uint64

	inboundTxid string
}

func NewMemolessRefundActor(scenario MemolessRefundScenario, asset common.Asset, rng *rand.Rand) *Actor {
	a := &MemolessRefundActor{
		Actor:    *NewActor(fmt.Sprintf("MemolessRefund-%s-%s", scenario, asset), rng),
		scenario: scenario,
		asset:    asset,
	}

	a.SetLogger(a.Log().With().
		Str("scenario", scenario.String()).
		Str("asset", asset.String()).
		Logger())

	// ExpiredRef must wait at least MEMOLESSTXNTTL blocks for the reference to
	// lapse. The aggregator lowers the TTL to 10 via a multi-node mimir vote,
	// but that vote is applied fire-and-forget with no supermajority
	// confirmation, so until it takes effect memolessTTL() reads the mocknet
	// default (100 blocks). Allow up to 5 minutes to cover that worst case;
	// the actor still finishes as soon as expiry is observed, so this only
	// raises the ceiling before a stuck actor fails loudly.
	if scenario == MemolessRefundExpiredRef {
		a.Timeout = 5 * time.Minute
	}

	a.Ops = append(a.Ops, a.acquireUser)

	switch scenario {
	case MemolessRefundExpiredRef, MemolessRefundUnpoolableTarget:
		a.Ops = append(a.Ops, a.registerMemo)
	}

	if scenario == MemolessRefundExpiredRef {
		a.Ops = append(a.Ops, a.waitForExpiry)
	}

	a.Ops = append(a.Ops, a.prepareMemo)
	a.Ops = append(a.Ops, a.sendInbound)
	a.Ops = append(a.Ops, a.verifyRefund)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *MemolessRefundActor) acquireUser(config *OpConfig) OpResult {
	pool, err := thornode.GetPool(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pool")
		return OpResult{Continue: false}
	}

	fee, err := thornode.GetOutboundFee(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get refund outbound fee")
		return OpResult{Continue: false}
	}
	a.amount = refundAmount(cosmos.NewUintFromString(pool.BalanceAsset), fee)

	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}

		l1Acct, err := user.ChainClients[a.asset.Chain].GetAccount(nil)
		if err != nil {
			user.Release()
			continue
		}
		if l1Acct.Coins.GetCoin(a.asset).Amount.LTE(a.amount) {
			user.Release()
			continue
		}

		a.user = user
		a.Log().Info().Str("user", user.Name()).Msg("acquired user")
		break
	}

	return OpResult{Continue: a.user != nil}
}

func (a *MemolessRefundActor) registerMemo(config *OpConfig) OpResult {
	// Once we have a ref, we're done — short-circuit re-entries from later
	// ops that return Continue:false (DAG replays from the pending op, but
	// this function is written to be re-entrant-safe anyway).
	if a.memolessRef != 0 {
		return OpResult{Continue: true}
	}

	// Broadcast the registration only once per actor. If we already have a
	// registration txid but haven't resolved the ref yet, fall through to
	// the resolution block below.
	if a.registrationTxID.IsEmpty() {
		toAddr, err := a.user.PubKey(a.asset.Chain).GetAddress(a.asset.Chain)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to derive L1 address")
			return OpResult{Continue: false, Error: err}
		}

		var registeredBody string
		switch a.scenario {
		case MemolessRefundExpiredRef:
			// Valid target (same-chain gas asset) — never consumed because the
			// reference will expire before we use it.
			registeredBody = fmt.Sprintf("=:%s:%s", a.asset.Chain.GetGasAsset(), toAddr)
		case MemolessRefundUnpoolableTarget:
			registeredBody = fmt.Sprintf("=:%s:%s", unpoolableAsset, toAddr)
		default:
			return OpResult{Continue: true}
		}
		a.registeredBody = registeredBody

		registerMemo := fmt.Sprintf("REFERENCE:%s:%s", a.asset, registeredBody)

		thorAddr, err := a.user.PubKey(common.THORChain).GetAddress(common.THORChain)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get thor address")
			return OpResult{Continue: false, Error: err}
		}
		thorAccAddr, err := thorAddr.AccAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get thor acc address")
			return OpResult{Continue: false, Error: err}
		}

		coin := common.NewCoin(common.RuneNative, cosmos.ZeroUint())
		deposit := types.NewMsgDeposit(common.NewCoins(coin), registerMemo, thorAccAddr)
		txid, err := a.user.Thorchain.BroadcastWithBlocking(deposit)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to broadcast registration")
			return OpResult{Continue: false, Error: err}
		}
		a.registrationTxID = txid
		a.Log().Info().
			Stringer("txid", txid).
			Str("memo", registerMemo).
			Msg("broadcasted memoless registration")
	}

	// Poll for the ref; state may lag the tx commit by a block or two. Re-enter
	// from the DAG (Continue:false) rather than sleeping so other actors run.
	rm, err := thornode.GetMemoHash(a.registrationTxID.String())
	if err != nil || rm.Reference == "" || rm.Memo != a.registeredBody {
		return OpResult{Continue: false}
	}

	ref, err := strconv.ParseUint(rm.Reference, 10, 64)
	if err != nil {
		a.Log().Error().Err(err).Str("reference", rm.Reference).Msg("failed to parse reference")
		return OpResult{Continue: false, Error: err}
	}
	a.memolessRef = ref

	block, err := thornode.GetBlock(0)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to read block height")
		return OpResult{Continue: false, Error: err}
	}
	a.registeredHeight = block.Header.Height

	a.Log().Info().
		Uint64("ref", a.memolessRef).
		Int64("registered_height", a.registeredHeight).
		Msg("resolved memoless reference")

	return OpResult{Continue: true}
}

func (a *MemolessRefundActor) waitForExpiry(config *OpConfig) OpResult {
	ttl, err := memolessTTL()
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to read memoless TTL; will re-poll")
		return OpResult{Continue: false}
	}

	block, err := thornode.GetBlock(0)
	if err != nil {
		return OpResult{Continue: false}
	}

	// +2 safety margin to avoid racing expiry semantics at the boundary.
	required := a.registeredHeight + ttl + 2
	if block.Header.Height < required {
		return OpResult{Continue: false}
	}

	a.Log().Info().
		Int64("height", block.Header.Height).
		Int64("required", required).
		Msg("reference is expired")
	return OpResult{Continue: true}
}

func (a *MemolessRefundActor) prepareMemo(config *OpConfig) OpResult {
	switch a.scenario {
	case MemolessRefundUnparsable:
		a.depositMemo = "xyz not a valid memo"
	case MemolessRefundNonexistentRef:
		// Top of range minimizes collision with refs registered by other actors.
		a.depositMemo = fmt.Sprintf("r:%d", mocknetRefCeiling-1)
	case MemolessRefundExpiredRef, MemolessRefundUnpoolableTarget:
		a.depositMemo = fmt.Sprintf("r:%d", a.memolessRef)
	default:
		return OpResult{Continue: false, Error: fmt.Errorf("unknown scenario %v", a.scenario)}
	}

	a.Log().Info().Str("memo", a.depositMemo).Msg("prepared deposit memo")
	return OpResult{Continue: true}
}

func (a *MemolessRefundActor) sendInbound(config *OpConfig) OpResult {
	client := a.user.ChainClients[a.asset.Chain]

	txid, err := acommon.DepositL1Token(a.Log(), client, a.asset, a.depositMemo, a.amount)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to send router deposit")
		return OpResult{Continue: false, Error: err}
	}

	a.inboundTxid = txid
	a.Log().Info().Str("txid", txid).Msg("broadcasted router deposit for refund")
	return OpResult{Continue: true}
}

func (a *MemolessRefundActor) verifyRefund(config *OpConfig) OpResult {
	stages, err := thornode.GetTxStages(a.inboundTxid)
	if err != nil {
		return OpResult{Continue: false}
	}
	if stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
		return OpResult{Continue: false}
	}

	details, err := thornode.GetTxDetails(a.inboundTxid)
	if err != nil {
		return OpResult{Continue: false}
	}

	if len(details.OutTxs) != 1 {
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("expected exactly one out tx, got %d", len(details.OutTxs)),
		}
	}
	if len(details.Actions) != 1 {
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("expected exactly one action, got %d", len(details.Actions)),
		}
	}

	actionMemo := ""
	if details.Actions[0].Memo != nil {
		actionMemo = *details.Actions[0].Memo
	} else if details.Actions[0].OriginalMemo != nil {
		actionMemo = *details.Actions[0].OriginalMemo
	}
	if !strings.HasPrefix(actionMemo, "REFUND:") {
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("expected REFUND action, got memo %q", actionMemo),
		}
	}

	// Sub-item #5: refunded coin must be the original ERC20, not gas or another asset.
	if len(details.OutTxs[0].Coins) == 0 {
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("refund out tx has no coins"),
		}
	}
	outCoin := details.OutTxs[0].Coins[0]
	if outCoin.Asset != a.asset.String() {
		return OpResult{
			Finish: true,
			Error: fmt.Errorf(
				"refund asset mismatch: expected %s, got %s",
				a.asset, outCoin.Asset,
			),
		}
	}

	a.Log().Info().
		Str("memo", actionMemo).
		Str("asset", outCoin.Asset).
		Str("amount", outCoin.Amount).
		Msg("verified ERC20 refund")

	a.user.Release()
	return OpResult{Finish: true}
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

// memolessTTL reads the active TTL from the network's mimir so the sim tracks
// any runtime override; the mocknet default is 100 blocks.
func memolessTTL() (int64, error) {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		return 0, err
	}
	if v, ok := mimirs["MEMOLESSTXNTTL"]; ok && v > 0 {
		return v, nil
	}
	// Fall back to the mocknet constant so we aren't blocked on an unset mimir.
	log.Debug().Msg("memoless TTL mimir unset, falling back to 100")
	return 100, nil
}

// MemolessRefundScenario String method (manually inlined in lieu of stringer
// to avoid adding a generated file in this change).
func (s MemolessRefundScenario) String() string {
	switch s {
	case MemolessRefundUnparsable:
		return "Unparsable"
	case MemolessRefundNonexistentRef:
		return "NonexistentRef"
	case MemolessRefundExpiredRef:
		return "ExpiredRef"
	case MemolessRefundUnpoolableTarget:
		return "UnpoolableTarget"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

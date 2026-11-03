package core

import (
	"fmt"
	rand "math/rand/v2"
	"sort"
	"time"

	"github.com/rs/zerolog"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// DualLPActor
////////////////////////////////////////////////////////////////////////////////////////

type DualLPActor struct {
	Actor

	asset            common.Asset
	account          *User
	thorAddress      common.Address
	l1Address        common.Address
	runeAmount       cosmos.Uint
	l1Amount         cosmos.Uint
	minLastAddHeight int64

	// l1DepositAt and lpWait show how the remaining actor timeout was spent
	// between the L1 deposit and a completed LP position
	l1DepositAt time.Time
	lpWait      string
}

func NewDualLPActor(asset common.Asset, rng *rand.Rand) *Actor {
	a := &DualLPActor{
		Actor: *NewActor(fmt.Sprintf("DualLP-%s", asset), rng),
		asset: asset,
	}
	a.Timeout = dualLPActorTimeout(asset)

	// lock a user that has L1 and RUNE balance
	a.Ops = append(a.Ops, a.acquireUser)

	// deposit 10% of the user RUNE balance
	a.Ops = append(a.Ops, a.depositRune)

	// deposit 10% of the user L1 balance to match
	if asset.Chain.IsEVM() && !asset.IsGasAsset() {
		a.Ops = append(a.Ops, a.depositL1Token)
	} else {
		a.Ops = append(a.Ops, a.depositL1)
	}

	// ensure the lp is created and release the account
	a.Ops = append(a.Ops, a.verifyLP)

	return &a.Actor
}

func dualLPActorTimeout(asset common.Asset) time.Duration {
	if asset.Chain.Equals(common.XMRChain) || asset.Chain.Equals(common.TRONChain) {
		return 8 * time.Minute
	}
	return 2 * time.Minute
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *DualLPActor) acquireUser(config *OpConfig) OpResult {
	existingLPs, err := thornode.GetLiquidityProviders(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Str("step", "acquireUser").Msg("failed to get liquidity providers")
		return OpResult{
			Continue: false,
		}
	}

	// THORChain checks are cheap. Every L1 balance query hits one shared daemon,
	// so query L1 only in RUNE order until the first funded candidate.
	var skips dualLPSkips
	var candidates []dualLPCandidate
	for _, user := range config.Users {
		if candidate := a.runeCandidate(user, existingLPs, &skips); candidate != nil {
			candidates = append(candidates, *candidate)
		}
	}
	selected := selectFundedCandidate(a.asset, candidates, &skips)
	if selected == nil {
		// remain pending, naming the checks that ruled users out
		skips.event(a.Log().Warn()).
			Str("step", "acquireUser").
			Int("candidates", len(candidates)).
			Msg("no user with RUNE and L1 balance available")
		return OpResult{
			Continue: false,
		}
	}

	a.account = selected.user
	a.thorAddress = selected.thorAddress
	a.l1Address = selected.l1Address
	a.runeAmount = selected.runeBalance.QuoUint64(4)
	a.l1Amount = selected.l1Balance.QuoUint64(4)
	a.SetLogger(a.Log().With().Str("user", a.account.Name()).Logger())
	a.Log().Info().
		Stringer("address", a.thorAddress).
		Stringer("l1Address", a.l1Address).
		Msg("acquired user")

	block, err := thornode.GetBlock(0)
	if err != nil {
		a.Log().Warn().Err(err).Msg("failed to get current block height")
	} else {
		a.minLastAddHeight = block.Header.Height
	}
	return OpResult{
		Continue: true,
	}
}

// dualLPCandidate is a user with RUNE and no existing LP position.
type dualLPCandidate struct {
	user        *User
	thorAddress common.Address
	l1Address   common.Address
	runeBalance cosmos.Uint
	l1Balance   cosmos.Uint
}

// dualLPSkips counts why users were not selected, so a stalled acquireUser
// reports the failing check rather than the last user it inspected.
type dualLPSkips struct {
	busy, noRune, existingLP, noL1Balance, l1Errors int
	lastErr                                         error
}

func (s *dualLPSkips) event(e *zerolog.Event) *zerolog.Event {
	return e.Err(s.lastErr).
		Int("busy", s.busy).
		Int("no_rune", s.noRune).
		Int("existing_lp", s.existingLP).
		Int("no_l1_balance", s.noL1Balance).
		Int("l1_errors", s.l1Errors)
}

// runeCandidate returns the user if it can fund the RUNE side. Users are locked
// only in selectFundedCandidate, so other actors are not blocked while slow L1
// balances are queried.
func (a *DualLPActor) runeCandidate(user *User, existingLPs []openapi.LiquidityProvider, skips *dualLPSkips) *dualLPCandidate {
	if user.IsLocked() {
		skips.busy++
		return nil
	}
	thorAddress, err := user.PubKey(common.THORChain).GetAddress(common.THORChain)
	if err != nil {
		skips.lastErr = fmt.Errorf("%s thor address: %w", user.Name(), err)
		return nil
	}
	l1Address, err := user.PubKey(a.asset.Chain).GetAddress(a.asset.Chain)
	if err != nil {
		skips.lastErr = fmt.Errorf("%s %s address: %w", user.Name(), a.asset.Chain, err)
		return nil
	}
	if findLP(existingLPs, thorAddress, l1Address) != nil {
		skips.existingLP++
		return nil
	}
	thorBalances, err := thornode.GetBalances(thorAddress)
	if err != nil {
		skips.lastErr = fmt.Errorf("%s thorchain balances: %w", user.Name(), err)
		return nil
	}
	runeBalance := thorBalances.GetCoin(common.RuneAsset()).Amount
	if runeBalance.IsZero() {
		skips.noRune++
		return nil
	}
	return &dualLPCandidate{
		user:        user,
		thorAddress: thorAddress,
		l1Address:   l1Address,
		runeBalance: runeBalance,
	}
}

// dualLPMaxL1Errors bounds the L1 balance query errors tolerated per
// acquireUser attempt. UTXO users share one daemon, so repeated errors mean the
// daemon is unavailable and further queries only spend the actor timeout.
// Allowing more than one keeps a single user with a failing per-user backend
// (for example an XMR wallet) from hiding the funded users after it.
const dualLPMaxL1Errors = 2

// selectFundedCandidate acquires and returns the candidate with the most RUNE
// that also has an L1 balance. The search ends after dualLPMaxL1Errors failed
// L1 queries.
func selectFundedCandidate(asset common.Asset, candidates []dualLPCandidate, skips *dualLPSkips) *dualLPCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].runeBalance.GT(candidates[j].runeBalance)
	})

	for i := range candidates {
		if skips.l1Errors >= dualLPMaxL1Errors {
			break
		}
		candidate := &candidates[i]
		if !candidate.user.Acquire() {
			skips.busy++
			continue
		}
		acct, err := candidate.user.ChainClients[asset.Chain].GetAccount(nil)
		if err != nil {
			skips.lastErr = fmt.Errorf("%s %s balance: %w", candidate.user.Name(), asset.Chain, err)
			skips.l1Errors++
			candidate.user.Release()
			continue
		}
		candidate.l1Balance = acct.Coins.GetCoin(asset).Amount
		if candidate.l1Balance.IsZero() {
			skips.noL1Balance++
			candidate.user.Release()
			continue
		}
		return candidate
	}
	return nil
}

// findLP returns the position for the address pair, in any state.
func findLP(lps []openapi.LiquidityProvider, thorAddress, l1Address common.Address) *openapi.LiquidityProvider {
	for i, lp := range lps {
		if lp.RuneAddress == nil || lp.AssetAddress == nil {
			continue
		}
		if common.Address(*lp.RuneAddress).Equals(thorAddress) &&
			common.Address(*lp.AssetAddress).Equals(l1Address) {
			return &lps[i]
		}
	}
	return nil
}

func (a *DualLPActor) depositL1Token(config *OpConfig) OpResult {
	memo := fmt.Sprintf("+:%s:%s", a.asset, a.thorAddress)
	client := a.account.ChainClients[a.asset.Chain]
	txid, err := DepositL1Token(a.Log(), client, a.asset, memo, a.l1Amount)
	if err != nil {
		a.Log().Error().Err(err).Str("step", "depositL1Token").Msg("failed to deposit L1 token")
		return OpResult{
			Continue: false,
		}
	}

	a.l1DepositAt = time.Now()
	a.Log().Info().Str("txid", txid).Msg("broadcasted token add liquidity tx")
	return OpResult{
		Continue: true,
	}
}

func (a *DualLPActor) depositL1(config *OpConfig) OpResult {
	memo := fmt.Sprintf("+:%s:%s", a.asset, a.thorAddress)
	client := a.account.ChainClients[a.asset.Chain]
	txid, err := DepositL1(a.Log(), client, a.asset, memo, a.l1Amount)
	if err != nil {
		a.Log().Error().Err(err).Str("step", "depositL1").Msg("failed to deposit L1")
		return SimTxErrorResult(err)
	}

	a.l1DepositAt = time.Now()
	a.Log().Info().Str("txid", txid).Msg("broadcasted L1 add liquidity tx")
	return OpResult{
		Continue: true,
	}
}

// dualLPWaitState describes why the LP position for the address pair is not
// complete yet.
func dualLPWaitState(lps []openapi.LiquidityProvider, thorAddress, l1Address common.Address) string {
	lp := findLP(lps, thorAddress, l1Address)
	if lp == nil {
		return "waiting for LP position to be created"
	}
	return fmt.Sprintf("waiting for LP position with pending_rune=%s pending_asset=%s units=%s",
		lp.PendingRune, lp.PendingAsset, lp.Units)
}

func (a *DualLPActor) depositRune(config *OpConfig) OpResult {
	memo := fmt.Sprintf("+:%s:%s", a.asset, a.l1Address)
	accAddr, err := a.account.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Str("step", "depositRune").Msg("failed to get thor address")
		return OpResult{
			Continue: false,
		}
	}
	deposit := types.NewMsgDeposit(
		common.NewCoins(common.NewCoin(common.RuneAsset(), a.runeAmount)),
		memo,
		accAddr,
	)
	txid, err := a.account.Thorchain.Broadcast(deposit)
	if err != nil {
		a.Log().Error().Err(err).Str("step", "depositRune").Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().Stringer("txid", txid).Msg("broadcasted RUNE add liquidity tx")
	return OpResult{
		Continue: true,
	}
}

func (a *DualLPActor) verifyLP(config *OpConfig) OpResult {
	lps, err := thornode.GetLiquidityProviders(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Str("step", "verifyLP").Msg("failed to get liquidity providers")
		return OpResult{
			Continue: false,
		}
	}

	lp := findCompletedLP(lps, a.thorAddress, a.l1Address)
	if lp == nil {
		// log each change of the pending position once, so a timeout shows
		// which side of the deposit never arrived
		if wait := dualLPWaitState(lps, a.thorAddress, a.l1Address); wait != a.lpWait {
			a.lpWait = wait
			a.Log().Info().
				Str("step", "verifyLP").
				Dur("since_l1_deposit", time.Since(a.l1DepositAt)).
				Msg(wait)
		}
		return OpResult{
			Continue: false,
		}
	}
	if a.minLastAddHeight > 0 {
		if lp.LastAddHeight == nil {
			a.Log().Info().Msg("waiting for LP last_add_height")
			return OpResult{
				Continue: false,
			}
		}
		if *lp.LastAddHeight <= a.minLastAddHeight {
			a.Log().Info().
				Int64("last_add_height", *lp.LastAddHeight).
				Int64("min_last_add_height", a.minLastAddHeight).
				Msg("waiting for LP position to update")
			return OpResult{
				Continue: false,
			}
		}
	}

	a.Log().Info().
		Str("units", lp.Units).
		Str("rune_deposit_value", lp.RuneDepositValue).
		Str("asset_deposit_value", lp.AssetDepositValue).
		Msg("verified liquidity provider")
	a.account.Release()
	return OpResult{
		Finish: true,
	}
}

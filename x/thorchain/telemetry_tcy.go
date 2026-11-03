package thorchain

import (
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// emitTCYTelemetry emits TCY claim and stake accounting state.
//
// The failure class this detects is silent TCY breakage - every one of these modes
// happens with no event, no log line and no error returned to anyone:
//   - claim insolvency: outstanding TCYClaimer entitlements exceed what the tcy_claim
//     module actually holds, so some claims are simply unpayable. The invariant for
//     this already exists (keeper/v1/invariants.go TCYClaimInvariant, route
//     "tcy_claim") but is only ever evaluated on a manual /invariant query, so a break
//     can sit undetected indefinitely.
//   - stake drift: the tcy_stake module no longer holds every staker's TCY. There is
//     no invariant route for this at all - an entirely uncovered accounting surface.
//   - distribution stall: tcy_stake RUNE should sawtooth once a day as
//     distributeTCYStake pays out; a monotonic climb means stakers stopped being paid.
//   - halt-by-mimir: all five TCY halt keys default to 1 (halted) in constants_v1.go,
//     so mainnet runs entirely on mimir overrides. A mimir reset or a bad vote
//     re-halts TCY and today that is only discoverable by reading mimir by hand.
//
// Strictly read-only. In particular NEVER reach for getTCYDistributions from here to
// sum stakers: it calls DeleteTCYStaker (manager_network_current.go), and a keeper
// write on this node-local telemetry path forks the chain.
func emitTCYTelemetry(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()

	// O(1) module balances, every block.
	// tcy_stake RUNE is the daily distribution float: distributeTCYStake runs on
	// IsPeriodLastBlock(ctx, blocksPerDay) and returns early and silently when
	// TCYStakeDistributionHalt is set or the balance is below
	// MinRuneForTCYStakeDistribution, so a monotonic climb here is the payout alarm.
	telemetry.SetGauge(telem(k.GetRuneBalanceOfModule(ctx, TCYStakeName)), "thornode", "tcy", "stake_pending_rune")

	// tcy_claim TCY is the pot every claim is paid out of - TCYClaimHandler bails
	// with "claiming module doesn't have tcy funds" the instant it hits zero.
	// Reused below as the denominator of claim_deficit.
	claimingTCY := k.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	telemetry.SetGauge(telem(claimingTCY), "thornode", "tcy", "claiming_tcy")

	// TCY halts. Consumers test a bare `> 0` with no height comparison
	// (handler_tcy_claim.go, handler_tcy_stake.go, handler_tcy_unstake.go,
	// manager_network_current.go), so match that predicate exactly.
	for _, h := range []struct {
		reason string
		key    constants.ConstantName
	}{
		{"claiming", constants.TCYClaimingHalt},
		{"staking", constants.TCYStakingHalt},
		{"unstaking", constants.TCYUnstakingHalt},
		{"distribution", constants.TCYStakeDistributionHalt},
		{"claiming_swap", constants.TCYClaimingSwapHalt},
	} {
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "tcy", "halted"},
			boolTelem(k.GetConfigInt64(ctx, h.key) > 0),
			[]metrics.Label{telemetry.NewLabel("reason", h.reason)},
		)
	}

	// Everything below scans an unbounded store (~11.6k claimers at genesis), so it is
	// gated. The gate must stay well under the Prometheus sink expiration - the
	// cosmos telemetry config's prometheus_retention_time is 600s, i.e. 100 blocks -
	// because a gauge set less often than that is dropped from the registry between
	// emissions and vanishes from most scrapes, which makes any `> 0 for 1h` alert
	// unfireable. 50 blocks (~5m) keeps every series continuous with 2x margin.
	// Do not raise this to 1000.
	if !IsPeriodLastBlock(ctx, 50) {
		return
	}

	// TCYClaimInvariant evaluated on a schedule instead of only on demand. Scoped so
	// the ~11.6k-entry claimer iterator is released before the staker scan below opens
	// its own, rather than being held to the end of the function by a bare defer.
	claimerSum := cosmos.ZeroUint()
	var claimerCount int64
	func() {
		iter := k.GetTCYClaimerIterator(ctx)
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var claimer TCYClaimer
			if err := k.Cdc().Unmarshal(iter.Value(), &claimer); err != nil {
				continue
			}
			claimerCount++
			if claimer.Amount.IsNil() {
				continue
			}
			claimerSum = claimerSum.Add(claimer.Amount)
		}
	}()

	// Subtract in Int space, not in gauge space: telem() divides by 1e8 into a
	// float32 (~7 significant digits), so differencing two ~1e8-scale float32s
	// cancels a real deficit to 0. Positive means insolvent, negative is the
	// protocol-owned surplus claimingSwapRuneToTCY accumulates.
	telemetry.SetGauge(
		telemInt(cosmos.NewIntFromBigInt(claimerSum.BigInt()).Sub(cosmos.NewIntFromBigInt(claimingTCY.BigInt()))),
		"thornode", "tcy", "claim_deficit",
	)
	telemetry.SetGauge(telem(claimerSum), "thornode", "tcy", "claim_outstanding")
	telemetry.SetGauge(float32(claimerCount), "thornode", "tcy", "claim_outstanding_count")

	// TCY stake solvency: stakes route TCY into tcy_stake and are recorded by
	// UpdateTCYStaker, claims move TCY from tcy_claim into tcy_stake, unstakes send it
	// back out - so module TCY must cover the sum of recorded staker amounts.
	// Smart-contract stakers are synthesised by ListTCYStakers from the holders' OWN
	// account balances, which are not module-held, so they must be excluded or the
	// comparison is systematically wrong.
	stakers, err := k.ListTCYStakers(ctx)
	if err != nil {
		ctx.Logger().Error("fail to list tcy stakers for telemetry", "error", err)
		return
	}
	stakerSum := cosmos.ZeroUint()
	for _, staker := range stakers {
		if tcysmartcontract.IsTCYSmartContractAddress(staker.Address) {
			continue
		}
		if staker.Amount.IsNil() {
			continue
		}
		stakerSum = stakerSum.Add(staker.Amount)
	}
	stakeTCY := k.GetBalanceOfModule(ctx, TCYStakeName, common.TCY.Native())
	telemetry.SetGauge(
		telemInt(cosmos.NewIntFromBigInt(stakerSum.BigInt()).Sub(cosmos.NewIntFromBigInt(stakeTCY.BigInt()))),
		"thornode", "tcy", "stake_deficit",
	)
}

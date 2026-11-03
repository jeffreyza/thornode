package thorchain

import (
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// solvencyDelta renders held-minus-owed as a signed gauge value. The subtraction is
// done on cosmos.Uint before the float32 conversion on purpose: at module-balance
// scale one float32 ULP is tens of RUNE, so subtracting two telem() results (here or
// in PromQL) would quantise away the small persistent divergence these gauges exist
// to catch. Negative means the module owes more than it holds.
func solvencyDelta(held, owed cosmos.Uint) float32 {
	if held.IsNil() || owed.IsNil() {
		return 0
	}
	if held.GTE(owed) {
		return telem(common.SafeSub(held, owed))
	}
	return -telem(common.SafeSub(owed, held))
}

// emitSolvencyTelemetry emits the network-wide accounting invariants: circulating
// RUNE against the MaxRuneSupply circuit breaker, the balance of every module
// account, per-module solvency deltas (bond, affiliate collector, RUNEPool), and
// the registered keeper invariants that those deltas do not already cover.
//
// The failure class these detect is "the chain's books no longer balance", which is
// entirely dark today:
//   - Supply: MintToModule (keeper/v1/keeper.go) holds the infinite-mint circuit
//     breaker. Past the cap it silently converts minting into Reserve drainage and
//     then writes HaltTrading/HaltChainGlobal/PauseLP/HaltTHORChain. Nothing exports
//     supply, so the first visible symptom of an inflation bug is the whole network
//     halting with zero lead time - and the cap itself moves, since UpdateNetwork
//     decrements the MaxRuneSupply mimir on every system-income burn.
//   - Module deltas: a module holding less than it owes only surfaces when a user's
//     withdraw, claim or bond refund fails.
//   - Invariants: AppModule.RegisterInvariants is an empty stub so the crisis module
//     never evaluates them, and the only other exposure is the queryInvariant gRPC
//     endpoint, which nothing polls.
//
// Emission schedule: the RUNE supply / cap / headroom scalars are written every block
// (they are the liveness signal here). The per-module balance loop and the bond_delta
// node-account scan refresh on height%5==3 (stagger slot 3). The affiliate/RUNEPool
// prefix scans keep their 50-block gate and the invariant sweep its 600-block gate.
//
// Strictly read-only. Every call below is a Get*/Is* accessor or a store iterator,
// each verified against x/thorchain/keeper/v1: nothing writes state, emits an event
// or mutates ctx. This runs only on nodes with telemetry.enabled, so a write here
// would fork the chain.
func emitSolvencyTelemetry(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()

	// ~5 minutes at 6s blocks. Gates the prefix scans over stores that are not
	// compile-time bounded (affiliate collectors, RUNE providers) while still
	// refreshing well inside THORNode's shipped prometheus_retention_time of
	// 600s - a gauge set less often than that is dropped from the registry
	// between emissions and vanishes from most scrapes.
	const scanPeriod = 50
	// ~1 hour, and a multiple of scanPeriod. Gates the invariant sweep, whose
	// PoolsInvariant walks every liquidity provider of every pool and whose
	// AsgardInvariant walks both swap queues - by far the most expensive read in
	// this file. IsPeriodLastBlock is height-based, so every telemetry-enabled
	// node pays that cost on the same height: size it against the LP store before
	// enabling telemetry on a validator that is tight on block time. The
	// resulting series are intermittent by design, since this period is longer
	// than prometheus_retention_time - alert on max_over_time(...[2h]), not on
	// the instant vector.
	const invariantPeriod = 600

	// --- RUNE supply vs the MaxRuneSupply circuit breaker (O(1), every block) ---
	// Read the raw mimir rather than GetConfigInt64: the breaker arms only when the
	// mimir itself is > 0, so the constant fallback would advertise a cap that is
	// not actually enforced.
	runeSupply := k.GetTotalSupply(ctx, common.RuneAsset())
	telemetry.SetGauge(telem(runeSupply), "thornode", "network", "rune_supply")
	maxRuneSupply, mErr := k.GetMimir(ctx, constants.MaxRuneSupply.String())
	capArmed := mErr == nil && maxRuneSupply > 0
	telemetry.SetGauge(boolTelem(capArmed), "thornode", "network", "rune_supply_cap_armed")
	if capArmed {
		maxSupply := cosmos.SafeUintFromInt64(maxRuneSupply)
		telemetry.SetGauge(telem(maxSupply), "thornode", "network", "rune_max_supply")
		// Minting headroom left before the breaker fires. Trending to zero
		// precedes an automatic full-network halt, and at exactly zero minting
		// silently becomes Reserve drainage, which looks like ordinary reward
		// payment on every other metric.
		telemetry.SetGauge(telem(common.SafeSub(maxSupply, runeSupply)), "thornode", "network", "rune_supply_headroom")
	}

	// Stagger slot 3 of 5 (height%5==3), shared with emitVaultDetailTelemetry - both
	// groups are light. Residue 0 carries emitEndBlockTelemetry's vault-count loop,
	// emitQueueDetailTelemetry's IsPeriodLastBlock(ctx, 5) and every pre-existing
	// 10/50/100/600 gate (all of which imply height%5==0); residue 1 the node loop and
	// residue 2 the pool loop, the heaviest of the three. Residue 3 carries only
	// emitMimirTelemetry, so this group is added there. Gates the per-module balance
	// loop (one gauge per module x denom; the asgard account alone carries every synth
	// denom on mainnet) and the node-account scan behind bond_delta.
	// IsPeriodLastBlock only does height%n==0, so the residue is written out directly.
	slowGroup := ctx.BlockHeight()%5 == 3

	// --- module account balances, all twelve (every slowGroup block) -----------
	// Widens the three-module (reserve/asgard/bond) loop in emitEndBlockTelemetry
	// to every module account in app.maccPerms. Those three are deliberately kept
	// here so this file stands alone if that loop is later removed; re-emitting
	// them is a last-write-wins of the identical value under the identical
	// {module,denom} labels, at the cost of three extra GetAllBalances (asgard,
	// with its synth denoms, being the only one that is not trivially small).
	// module="thorchain" is the sharpest of the added nine: THORChainInvariant
	// asserts that account must NEVER carry a balance - it is only a transit
	// account for the system-income burn and for MintAndSendToAccount - so any
	// series at all under module="thorchain" is stuck or unburnt funds.
	if slowGroup {
		for _, name := range []string{
			ModuleName, ReserveName, AsgardName, BondName, LendingName,
			AffiliateCollectorName, TreasuryName, RUNEPoolName,
			TCYClaimingName, TCYStakeName, POLReserveName, StableReserveName,
		} {
			modAddr := k.GetModuleAccAddress(name)
			if modAddr.Empty() {
				continue
			}
			for _, coin := range k.GetBalance(ctx, modAddr) {
				telemetry.SetGaugeWithLabels(
					[]string{"thornode", "module", "balance"},
					telemInt(coin.Amount),
					[]metrics.Label{
						telemetry.NewLabel("module", name),
						telemetry.NewLabel("denom", coin.Denom),
					},
				)
			}
		}

		// --- BondInvariant (O(nodes), every slowGroup block) --------------------
		// bond module RUNE == sum(NodeAccount.Bond) + Network.BondRewardRune. Catches
		// a slash, a bond-provider payout or a fee deduction that debits a node
		// account without moving module coins, or the reverse. Deliberately uses the
		// raw node account iterator rather than ListValidatorsWithBond so the sum
		// matches BondInvariant exactly (all statuses, bond-only accounts included).
		// One gauge, but a full node-account scan behind it, which is why it rides the
		// slot-2 gate: a bond accounting divergence is a standing condition, not an
		// event, so 5-block resolution is indistinguishable at any scrape interval.
		bondedRune := cosmos.ZeroUint()
		naIter := k.GetNodeAccountIterator(ctx)
		for ; naIter.Valid(); naIter.Next() {
			var na NodeAccount
			if err := k.Cdc().Unmarshal(naIter.Value(), &na); err != nil {
				continue
			}
			if na.Bond.IsNil() {
				continue
			}
			bondedRune = bondedRune.Add(na.Bond)
		}
		// closed here rather than deferred: a defer would hold the iterator open
		// across the gated scans and the invariant sweep below. Nothing between the
		// open and this close can panic - Unmarshal returns an error, it is not a
		// Must* variant.
		_ = naIter.Close()

		network, nErr := k.GetNetwork(ctx)
		switch {
		case nErr != nil:
			ctx.Logger().Error("fail to get network for solvency telemetry", "error", nErr)
		case network.BondRewardRune.IsNil():
			ctx.Logger().Error("network bond reward rune is nil, skipping bond solvency telemetry")
		default:
			heldBond := k.GetBalanceOfModule(ctx, BondName, common.RuneAsset().Native())
			telemetry.SetGauge(
				solvencyDelta(heldBond, bondedRune.Add(network.BondRewardRune)),
				"thornode", "solvency", "bond_delta",
			)
		}
	}

	// --- the gated prefix scans (every scanPeriod blocks) -----------------------
	if IsPeriodLastBlock(ctx, scanPeriod) {
		// AffilliateCollectorInvariant: affiliate_collector module RUNE == sum of
		// accrued affiliate RUNE. Negative means affiliates are owed RUNE the
		// module does not hold, positive means RUNE is stranded with no claimant.
		// rune_total is emitted from the same pass because a total that only ever
		// grows means the preferred-asset payout path has stopped firing.
		if affCols, acErr := k.GetAffiliateCollectors(ctx); acErr != nil {
			ctx.Logger().Error("fail to get affiliate collectors for telemetry", "error", acErr)
		} else {
			totalAffRune := cosmos.ZeroUint()
			for _, ac := range affCols {
				if ac.RuneAmount.IsNil() {
					continue
				}
				totalAffRune = totalAffRune.Add(ac.RuneAmount)
			}
			heldAff := k.GetBalanceOfModule(ctx, AffiliateCollectorName, common.RuneAsset().Native())
			telemetry.SetGauge(solvencyDelta(heldAff, totalAffRune), "thornode", "solvency", "affiliate_collector_delta")
			telemetry.SetGauge(telem(totalAffRune), "thornode", "affiliate_collector", "rune_total")
		}

		// No TCY claim scan here on purpose: emitTCYTelemetry
		// (x/thorchain/telemetry_tcy.go) already walks GetTCYClaimerIterator on
		// this same 50-block gate and emits the claim deficit. That store is
		// unbounded, so a second pass would double the most expensive scan on the
		// telemetry path for a metric that already exists.

		// RUNEPoolInvariant: RUNEPool.PoolUnits / RuneDeposited / RuneWithdrawn vs
		// the provider sums. thornode_runepool_pool_units is already emitted but
		// has nothing to compare against, so a withdraw that debits provider units
		// without debiting pool units (or the reverse) stays invisible until
		// RUNEPool withdrawals start mispricing. All three deltas share one pass.
		providerUnits := cosmos.ZeroUint()
		providerDeposited := cosmos.ZeroUint()
		providerWithdrawn := cosmos.ZeroUint()
		rpIter := k.GetRUNEProviderIterator(ctx)
		for ; rpIter.Valid(); rpIter.Next() {
			var rp RUNEProvider
			if err := k.Cdc().Unmarshal(rpIter.Value(), &rp); err != nil {
				continue
			}
			// the invariant skips address-less records; match it exactly
			if rp.RuneAddress.Empty() {
				continue
			}
			if rp.Units.IsNil() || rp.DepositAmount.IsNil() || rp.WithdrawAmount.IsNil() {
				continue
			}
			providerUnits = providerUnits.Add(rp.Units)
			providerDeposited = providerDeposited.Add(rp.DepositAmount)
			providerWithdrawn = providerWithdrawn.Add(rp.WithdrawAmount)
		}
		// closed here, not deferred: a defer inside this block would only run at
		// function return, holding the iterator open across the invariant sweep.
		_ = rpIter.Close()

		if rpool, rpErr := k.GetRUNEPool(ctx); rpErr != nil {
			ctx.Logger().Error("fail to get rune pool for solvency telemetry", "error", rpErr)
		} else {
			telemetry.SetGauge(solvencyDelta(rpool.PoolUnits, providerUnits), "thornode", "solvency", "runepool_units_delta")
			telemetry.SetGauge(solvencyDelta(rpool.RuneDeposited, providerDeposited), "thornode", "solvency", "runepool_deposited_delta")
			telemetry.SetGauge(solvencyDelta(rpool.RuneWithdrawn, providerWithdrawn), "thornode", "solvency", "runepool_withdrawn_delta")
		}
	}

	// --- the registered keeper invariants (every invariantPeriod blocks) -------
	// Last in this function on purpose: the invariant closures use
	// Cdc().MustUnmarshal, which panics on a corrupt record. Each route runs inside
	// its own recover so one bad record costs a single invariant rather than every
	// metric emitted after this point. A panicked route emits nothing, so it goes
	// stale rather than reporting a false "healthy".
	if IsPeriodLastBlock(ctx, invariantPeriod) {
		for _, route := range k.InvariantRoutes() {
			switch route.Route {
			// invariantPeriod is a multiple of scanPeriod, so on every block that
			// reaches here this function has already walked the affiliate collectors
			// and the RUNE providers. The node-account scan and the module balance
			// loop are on the height%5==3 stagger slot instead, which h%600==0 never
			// hits - but they ran at most 2 blocks ago, i.e. still fresher than any
			// scrape. Either way, re-running these four routes would repeat a scan
			// for a boolean that is strictly less informative than the signed gauge
			// already emitted:
			//   bond                -> solvency.bond_delta
			//   affiliate_collector -> solvency.affiliate_collector_delta
			//   runepool            -> solvency.runepool_{units,deposited,withdrawn}_delta
			//   thorchain           -> module.balance{module="thorchain"} (any series = broken)
			case "bond", "affiliate_collector", "runepool", "thorchain":
				continue
			}
			if route.Invariant == nil {
				continue
			}
			var msgs []string
			var broken, panicked bool
			func() {
				defer func() {
					if r := recover(); r != nil {
						panicked = true
						ctx.Logger().Error("panic while running invariant for telemetry", "invariant", route.Route, "error", r)
					}
				}()
				msgs, broken = route.Invariant(ctx)
			}()
			if panicked {
				continue
			}
			labels := []metrics.Label{telemetry.NewLabel("invariant", route.Route)}
			telemetry.SetGaugeWithLabels([]string{"thornode", "invariant", "broken"}, boolTelem(broken), labels)
			// how many distinct breakages the invariant reported, not an amount
			telemetry.SetGaugeWithLabels([]string{"thornode", "invariant", "break_count"}, float32(len(msgs)), labels)
		}
	}
}

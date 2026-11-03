package thorchain

import (
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// emitVaultDetailTelemetry emits per-asgard vault health: standing deficit, frozen
// chains, per-chain insolvency and compromise flags, how long each vault has held
// its status, migration/churn progress, and keysign signer headroom.
//
// The failure class these detect is the slow, silent one: a churn that never
// finishes. A retiring vault whose value never drains, a migrate outbound that is
// signed but never observed back, a chain frozen for keysign failure that can only
// unfreeze on an outbound that will never be scheduled, a vault quietly dropped from
// inbound-address election by a mimir flag left set after an incident, or a vault
// that has lost enough members that it can no longer form a signing party. None of
// these halt the chain, none raise an error, and every one of them ends with funds
// stranded in a vault the network can no longer sign for.
//
// Emission schedule: thornode_churn_halted is written every block; the per-vault and
// per-(vault x chain) gauges refresh on height%5==3 (stagger slot 3, ~30s - inside any
// scrape interval and far inside the 600s prometheus_retention_time); the keysign
// headroom block keeps its pre-existing 10-block gate. On a height that is neither,
// this function does one mimir read and returns without touching the vault store.
//
// Strictly read-only: this runs only on nodes with telemetry.enabled, so any keeper
// write, event or gas consumption here would fork the chain.
func emitVaultDetailTelemetry(ctx cosmos.Context, mgr Manager) {
	k := mgr.Keeper()

	// Churning halted by mimir. Same predicate as ValidatorMgr.BeginBlock and
	// NetworkMgr.scheduleKeygen: a height-valued key, effective once reached. Emitted
	// as the effective boolean so one alert works however the mimir was set.
	haltChurning, hcErr := k.GetMimir(ctx, "HaltChurning")
	telemetry.SetGauge(
		boolTelem(hcErr == nil && haltChurning > 0 && haltChurning <= ctx.BlockHeight()),
		"thornode", "churn", "halted",
	)

	// Stagger slot 3 of 5 (height%5==3). Every gauge below this line is per-vault or
	// per-(vault x chain) - thousands of writes at mainnet vault counts, all of them
	// slow-moving - so they refresh every 5 blocks (~30s) instead of every block,
	// which is still inside any Prometheus scrape interval.
	//
	// Residue choice, against the groups in emitEndBlockTelemetry: residue 0 carries
	// the vault-count loop plus emitQueueDetailTelemetry's IsPeriodLastBlock(ctx, 5)
	// and every pre-existing 10/50/100/600 gate (all of which imply height%5==0),
	// residue 1 the node loop and residue 2 the pool loop - the heaviest of the three.
	// Residue 3 carries only emitMimirTelemetry, so this group is added there.
	//
	// IsPeriodLastBlock only does height%n==0, so the residue is written out directly.
	emitPerVault := ctx.BlockHeight()%5 == 3
	// The pre-existing 10-block section at the bottom of this function. h%10==0
	// implies h%5==0, so it NEVER coincides with slot 3 - both flags have to be
	// checked before the vault fetch is skipped, or the keysign gauges would stop
	// being emitted entirely.
	churnScan := IsPeriodLastBlock(ctx, 10)
	if !emitPerVault && !churnScan {
		return
	}

	asgards, err := k.GetAsgardVaults(ctx)
	if err != nil {
		ctx.Logger().Error("fail to get asgard vaults for telemetry", "error", err)
		return
	}

	// Read once for the whole loop, and only when the per-vault gauges are actually
	// emitted. FundMigrationInterval is mimir-settable, so the zero must be guarded
	// before dividing by it - a panic here is swallowed by the recover in
	// emitEndBlockTelemetry and would silently kill every later metric.
	migrateInterval := int64(0)
	if emitPerVault {
		migrateInterval = k.GetConfigInt64(ctx, constants.FundMigrationInterval)
	}

	deficitTotals := common.Coins{}
	retiringValue := cosmos.ZeroUint()
	activeVaults := make(Vaults, 0, len(asgards))

	for _, vault := range asgards {
		// Always collected: the 10-block keysign section below needs it even on a
		// height where the per-vault gauges are not emitted.
		if vault.Status == ActiveVault {
			activeVaults = append(activeVaults, vault)
		}
		if !emitPerVault {
			continue
		}

		// Loop-invariant label values, resolved once per vault. PubKey.String() is a
		// free cast and VaultStatus.String() an enum map lookup, so this saves little
		// on its own - it is the label slices below that allocate, and those cannot be
		// pooled: the in-memory sink retains the slice it is handed
		// (go-metrics inmem.go, `intv.Gauges[k] = GaugeValue{... Labels: labels}`),
		// so a reused-and-mutated slice would corrupt every gauge already recorded.
		vaultType := vault.Type.String()
		pubkey := vault.PubKey.String()
		labels := []metrics.Label{
			telemetry.NewLabel("vault_type", vaultType),
			telemetry.NewLabel("pubkey", pubkey),
		}

		// Unsettled accounting hole standing open on this vault. Emitted for every
		// asgard, including inactive ones: a drained-but-still-in-deficit retiring
		// vault is moved straight to inactive, and that is exactly the vault an
		// operator still has to reconcile.
		//
		// vaultDeficitWithAmount drops an asset from Deficit entirely once it
		// settles, so a cleared deficit does not report zero here - the series
		// simply stops updating and the sink expires it after
		// cosmos.telemetry.prometheus_retention_time. Alert with a `for:` longer
		// than that rather than on a bare non-zero sample.
		for _, coin := range vault.Deficit {
			if coin.Amount.IsZero() {
				continue
			}
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "deficit"},
				telem(coin.Amount),
				[]metrics.Label{
					telemetry.NewLabel("vault_type", vaultType),
					telemetry.NewLabel("pubkey", pubkey),
					telemetry.NewLabel("asset", coin.Asset.String()),
				},
			)
		}
		// Coins.Add is an order-preserving slice merge, never a map range.
		deficitTotals = deficitTotals.Add(vault.Deficit...)

		// How long this vault has held its current status. A retiring age that keeps
		// growing means migration is not finishing and blocks every later churn; an
		// init age that keeps growing means keygen succeeded but rotation never
		// happened. The other statuses are zero-written so a transition clears the
		// stale series rather than leaving it at its last value.
		for _, status := range []VaultStatus{ActiveVault, RetiringVault, InactiveVault, InitVault} {
			age := float32(0)
			if vault.Status == status {
				age = float32(ctx.BlockHeight() - vault.StatusSince)
			}
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "status_blocks"},
				age,
				[]metrics.Label{
					telemetry.NewLabel("vault_type", vaultType),
					telemetry.NewLabel("pubkey", pubkey),
					telemetry.NewLabel("status", status.String()),
				},
			)
		}

		// Migrate outbounds scheduled off this vault and not yet observed back.
		// migrateFunds pauses their chains while other chains can continue. Use
		// the oldest pending age below to distinguish progress from a stuck migrate
		// that can still prevent this vault from retiring and churn from completing.
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "vault", "pending_migrate_txs"},
			float32(len(vault.PendingTxBlockHeights)),
			labels,
		)
		// Age of the oldest such outbound. Past SigningTransactionPeriod *
		// StaleMigratePendingSigningPeriodMultiplier (10) the network stops reserving
		// funds for it and force-prunes; alerting near half that catches a stalling
		// churn a full cycle before the retry.
		oldestPending := int64(0)
		for _, h := range vault.PendingTxBlockHeights {
			if h > 0 && ctx.BlockHeight()-h > oldestPending {
				oldestPending = ctx.BlockHeight() - h
			}
		}
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "vault", "pending_migration_oldest_age"},
			float32(oldestPending),
			labels,
		)

		// Which migration round a retiring vault is on - the same nth migrateFunds
		// computes. migrateFunds changes behaviour once nth passes ChurnMigrateRounds
		// (gas hold-back, partial amounts, dust burn); a round far above it means the
		// final round keeps failing. Zero for every non-retiring vault so the series
		// clears when migration completes.
		nth := float32(0)
		if vault.Status == RetiringVault && migrateInterval > 0 {
			nth = float32((ctx.BlockHeight()-vault.StatusSince)/migrateInterval + 1)
		}
		telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "migrate_round"}, nth, labels)

		// The rest is only meaningful for vaults still in service, and bounds the
		// pubkey label: inactive asgards linger until the next churn cleans the index.
		if vault.Status != ActiveVault && vault.Status != RetiringVault {
			continue
		}

		// NewChains returns the chains that did parse alongside the error, so keep
		// the partial set - discarding it would report frozen=0 for chains that are
		// in fact frozen.
		frozenChains, fErr := common.NewChains(vault.Frozen)
		if fErr != nil {
			ctx.Logger().Error("fail to parse frozen chains for telemetry", "pubkey", vault.PubKey, "error", fErr)
		}

		// One pass over the vault's chains serves both the freeze and the insolvency
		// flags, so the per-chain mimir read happens once.
		insolvent := false
		insolventReadErr := false
		for _, vaultChain := range vault.GetChains() {
			// A frozen chain means this vault will not sign that chain's outbounds and
			// the rescheduler will not move them elsewhere; unfreezing needs an observed
			// outbound from this vault, which cannot happen while nothing is assigned.
			// Emit an explicit 0 per chain so alerts do not rely on absent series.
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "frozen"},
				boolTelem(frozenChains.Has(vaultChain)),
				[]metrics.Label{
					telemetry.NewLabel("vault_type", vaultType),
					telemetry.NewLabel("pubkey", pubkey),
					telemetry.NewLabel("chain", vaultChain.String()),
				},
			)

			flagged, mErr := k.GetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, vaultChain))
			if mErr != nil {
				// isVaultInsolvent treats a failed read as insolvent; mirror that for the
				// eligibility gauge below, but do not publish a read failure as a flag.
				insolventReadErr = true
				continue
			}
			if flagged > 0 {
				insolvent = true
			}
			// One flagged chain drops the whole vault out of inbound-address election,
			// on every chain it serves - so this says which wallet is actually short.
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "insolvent"},
				boolTelem(flagged > 0),
				[]metrics.Label{
					telemetry.NewLabel("pubkey", pubkey),
					telemetry.NewLabel("chain", vaultChain.String()),
				},
			)
		}

		// isVaultCompromised fails closed (true on a KV error), so a failed read is
		// logged and the flag gauge skipped rather than reported as a compromise.
		compromised, cErr := isVaultCompromised(ctx, k, vault.PubKey)
		if cErr != nil {
			ctx.Logger().Error("fail to get compromised vault flag for telemetry", "pubkey", vault.PubKey, "error", cErr)
		} else {
			// Flagging a vault silently shrinks the pool of vaults available for
			// outbound scheduling in prepareTxOutItem and in LackSigning reassignment.
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "compromised"},
				boolTelem(compromised),
				[]metrics.Label{telemetry.NewLabel("pubkey", pubkey)},
			)
		}

		// Why this vault is excluded from inbound-address election. Fails closed the
		// same way the querier filters do - a failed read counts as excluded, not as
		// healthy - and emits 0 as well as 1 so a cleared flag returns to 0 instead of
		// leaving a stale 1 behind. Active vaults only: queryInboundAddresses starts
		// from GetAsgardVaultsByStatus(ActiveVault), so "ineligible" on a retiring
		// vault would be an alarming statement about an election it never entered.
		if vault.Status == ActiveVault {
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "ineligible"},
				boolTelem(cErr != nil || compromised),
				[]metrics.Label{
					telemetry.NewLabel("pubkey", pubkey),
					telemetry.NewLabel("reason", "compromised"),
				},
			)
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "ineligible"},
				boolTelem(insolventReadErr || insolvent),
				[]metrics.Label{
					telemetry.NewLabel("pubkey", pubkey),
					telemetry.NewLabel("reason", "insolvent"),
				},
			)
		}

		// Value still sitting in the retiring vaults. The per-vault total_value series
		// cannot be separated by status in PromQL; this can, and a figure that stays
		// flat over thousands of blocks is the cleanest sign that fund migration is
		// wedged and the churn can never complete. Pool reads only happen while a vault
		// is retiring, so this costs nothing outside a churn.
		if vault.Status == RetiringVault {
			for _, coin := range vault.Coins {
				if coin.IsRune() {
					retiringValue = retiringValue.Add(coin.Amount)
					continue
				}
				pool, pErr := k.GetPool(ctx, coin.Asset.GetLayer1Asset())
				if pErr != nil {
					continue
				}
				retiringValue = retiringValue.Add(pool.AssetValueInRune(coin.Amount))
			}
		}
	}

	if emitPerVault {
		// Deficit that outlived the vault carrying it: migrateFunds marks a drained
		// retiring vault inactive while it still has a deficit, and the next churn
		// drops it from the asgard index entirely, at which point the per-vault series
		// above simply stops. Labelled by asset only - never pubkey - for that reason.
		for _, coin := range deficitTotals {
			if coin.Amount.IsZero() {
				continue
			}
			telemetry.SetGaugeWithLabels(
				[]string{"thornode", "vault", "deficit_total"},
				telem(coin.Amount),
				[]metrics.Label{telemetry.NewLabel("asset", coin.Asset.String())},
			)
		}
		// Single scalar, but it is backed by a GetPool for every non-RUNE coin in
		// every retiring vault, so it rides the slot-3 gate with the loop that
		// computes it. Migration rounds are FundMigrationInterval blocks apart -
		// hundreds of blocks - so 5-block resolution loses nothing here.
		telemetry.SetGauge(telem(retiringValue), "thornode", "churn", "retiring_value_rune")
	}

	// Everything below needs a scan the per-block path does not already do - a full
	// validator list, and node/jail/slash reads for every member of every active
	// vault - so it runs once per 10 blocks. Both are slow-moving conditions, so
	// nothing is lost at that resolution.
	if !churnScan {
		return
	}

	// How many active asgards the current validator set should have, mirroring
	// ValidatorMgr.BeginBlock's incompleteChurnCheck (including its conditional
	// increment for the dedicated XMR monovault). Compared against the existing
	// thornode_vault_count{status="ActiveVault"}, a persistent inequality is the
	// earliest machine-readable sign of an incomplete churn: some keygen groups
	// completed and others did not, leaving fewer signing vaults than nodes expect.
	if asgardSize := k.GetConfigInt64(ctx, constants.AsgardSize); asgardSize > 0 {
		nas, nErr := k.ListActiveValidators(ctx)
		if nErr != nil {
			ctx.Logger().Error("fail to list active validators for telemetry", "error", nErr)
		} else {
			expectedActiveVaults := splitAsgardGroupCount(len(nas), asgardSize)
			if hasDedicatedXMRMonovault(activeVaults) {
				expectedActiveVaults++
			}
			telemetry.SetGauge(float32(expectedActiveVaults), "thornode", "churn", "expected_active_vaults")
		}
	}

	// queryKeysignParty skips the participant policy entirely while
	// DisableTSSSignerFiltering is active, signing with the whole membership. Read
	// once, outside the loop, and mirror it below - without this the emergency
	// switch operators flip during an incident would make every vault report zero
	// available signers. A failed read falls through to the policy path, which is
	// the same conservative direction the rest of this emitter takes.
	filteringDisabled, fdErr := tssSignerFilteringDisabled(ctx, k)
	if fdErr != nil {
		ctx.Logger().Error("fail to get tss signer filtering mimir for telemetry", "error", fdErr)
	}

	// How close each active vault is to being unable to form a signing party.
	// signers_available below signers_required means the vault cannot sign at all and
	// every outbound on its chains stalls; it degrades one jailed or forced-to-leave
	// member at a time, so it is a true leading indicator.
	for _, vault := range activeVaults {
		members := vault.GetMembership()
		if len(members) == 0 {
			continue
		}
		threshold, tErr := conversion.GetThreshold(len(members))
		if tErr != nil {
			continue
		}
		requiredSigners := threshold + 1
		vaultLabels := []metrics.Label{telemetry.NewLabel("pubkey", vault.PubKey.String())}
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "vault", "keysign", "signers_required"},
			float32(requiredSigners),
			vaultLabels,
		)

		if filteringDisabled {
			// Every member signs, so the classification below does not apply. Zero the
			// two policy series rather than dropping them, so they do not park on the
			// last value the policy produced before the mimir was set.
			telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "keysign", "signers_available"}, float32(len(members)), vaultLabels)
			telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "keysign", "jailed_fallback_signers"}, 0, vaultLabels)
			telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "keysign", "never_signers"}, 0, vaultLabels)
			continue
		}

		policy, pErr := keysignPolicyForVaultMembers(ctx, k, members, requiredSigners)
		if pErr != nil {
			// This errors precisely when no party can be formed, so report zero
			// available rather than dropping the series at the moment it matters.
			ctx.Logger().Error("fail to classify keysign party for telemetry", "pubkey", vault.PubKey, "error", pErr)
			telemetry.SetGaugeWithLabels([]string{"thornode", "vault", "keysign", "signers_available"}, 0, vaultLabels)
			continue
		}
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "vault", "keysign", "signers_available"},
			float32(len(policy.signers)),
			vaultLabels,
		)
		// Jailed members only counted in when clean members alone cannot reach
		// threshold: a non-zero value means the vault is already signing on fallbacks.
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "vault", "keysign", "jailed_fallback_signers"},
			float32(len(policy.jailedFallbackSigners)),
			vaultLabels,
		)
		// Members that can never sign (nodeAccountForcedOrFlagged, i.e. ForcedToLeave)
		// - permanent loss of headroom, unlike jailing, which heals.
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "vault", "keysign", "never_signers"},
			float32(len(policy.neverSigners)),
			vaultLabels,
		)
	}
}

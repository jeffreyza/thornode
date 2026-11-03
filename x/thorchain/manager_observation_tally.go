package thorchain

import (
	"fmt"
	"strconv"

	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

// The observation tally applies the MissingBlocks idea to chain observations: per
// Active node and chain, the share of voters on that chain that the node failed to
// sign in time. A node whose scanner for one chain is broken keeps signing blocks
// and so never trips MissingBlocks, but it misses every voter on that chain.
//
// Every voter the slasher scores for lack of observation (observed txs in and out,
// network fee, solvency, errata) is queued when it reaches consensus and scored
// when its observation window closes. Scoring at consensus would be
// wrong: consensus forms at a supermajority, so up to a third of the Active set has
// not signed yet, and most of those sign within the window. At the close of the
// window the voter's signers are exactly the ones the slasher credited, since a
// signer after the window has not arrived yet.
//
// A voter is skipped as a shared event when more than MissingObservationGuardBps of
// the Active nodes missed it, not counting nodes already failing its chain that missed
// it. Provider outages and post-halt catch-up then do not count against anyone, and
// nodes that are already failing a chain cannot shield each other by inflating the
// share.
//
// The guard is a grace period, not an immunity. A group above the guard share that
// keeps failing would otherwise never be counted, so it could never become "already
// failing" and leave the guard's population: a large enough operator would be immune.
// So each chain has a grace of MissingObservationGuardMaxBlocks (one churn interval by
// default). Time spent skipping the chain's voters uses it up, and time in which its
// voters pass the guard refills it at the same rate, up to the limit. A block in which
// any of the chain's voters reached consensus above the guard share is time spent
// skipping, however many others passed. Once the grace is used up, the voters count
// again. A persistent group then accumulates misses, drops out of the population as
// already failing, and the guard clears. A recovery buys back only
// as much grace as it lasts, so a group cannot keep its grace by recovering briefly
// now and then. Over a month of mainnet data the longest shared events lasted 111 h
// (GAIA after the unhalt), 61 h (BASE after a halt) and 52 h (SOL), so a 72 h limit
// spares all but the tail of the first. 0 keeps the guard open for good.
//
// Tallies decay by halving every MissingObservationHalfLife blocks and restart at
// churn-in, as MissingBlocks does. The tally is always kept; it only affects
// churn-out when MaxMissingObservationChurnOut is above zero.

// observationTallyUnit is the weight of one voter. Halving divides by two, so a
// unit above one keeps a single voter from rounding to zero after the first halving.
const observationTallyUnit = int64(1 << 10)

const (
	observationTallyKindTxIn       = "IN"
	observationTallyKindTxOut      = "OUT"
	observationTallyKindNetworkFee = "NETWORKFEE"
	observationTallyKindSolvency   = "SOLVENCY"
	observationTallyKindErrata     = "ERRATA"
)

// queueObservationTally schedules a voter that reached consensus in this block to
// be scored when its observation window closes. All voter kinds use the same
// per-chain window so voters from one consensus height are scored together.
func queueObservationTally(ctx cosmos.Context, k keeper.Keeper, kind string, chain common.Chain, ref ...string) {
	observeFlex := observationDelayFlexibility(ctx, k, chain)
	k.QueueObservationTally(ctx, kvTypes.ObservationTallyEntry{
		DueHeight:       ctx.BlockHeight() + max(observeFlex, 0),
		ConsensusHeight: ctx.BlockHeight(),
		Kind:            kind,
		Chain:           chain,
		Ref:             ref,
	})
}

// processObservationTallies ends the open guard runs on halted chains, scores every
// voter whose window closed at or before this block, then halves all tallies on a
// half-life boundary.
//
// guardedAt holds, per chain, the consensus height of the last voter above the
// guard share in this call. Another voter from that height must not close the run.
func processObservationTallies(ctx cosmos.Context, mgr Manager) error {
	k := mgr.Keeper()
	if err := endHaltedObservationGuardRuns(ctx, k); err != nil {
		ctx.Logger().Error("fail to end observation guard runs on halted chains", "error", err)
	}

	entries, err := k.PopObservationTallies(ctx, ctx.BlockHeight())
	if err != nil {
		return fmt.Errorf("fail to get observation tallies: %w", err)
	}
	if len(entries) > 0 {
		active, err := k.ListActiveValidators(ctx)
		if err != nil {
			return fmt.Errorf("fail to get active node accounts: %w", err)
		}
		guardedAt := make(map[common.Chain]int64)
		for _, entry := range entries {
			if err := scoreObservationTally(ctx, k, active, guardedAt, entry); err != nil {
				ctx.Logger().Error("fail to score observation tally", "kind", entry.Kind, "chain", entry.Chain, "ref", entry.Ref, "error", err)
			}
		}
	}

	halfLife := k.GetConfigInt64(ctx, constants.MissingObservationHalfLife)
	if halfLife > 0 && ctx.BlockHeight()%halfLife == 0 {
		if err := k.HalveObservationTallies(ctx); err != nil {
			return fmt.Errorf("fail to halve observation tallies: %w", err)
		}
	}
	return nil
}

// scoreObservationTally counts one voter towards its chain's total and towards the
// missed count of every Active node that did not sign it in time, unless the guard
// treats the misses as a shared event.
func scoreObservationTally(ctx cosmos.Context, k keeper.Keeper, active NodeAccounts, guardedAt map[common.Chain]int64, entry kvTypes.ObservationTallyEntry) error {
	// On a halted chain, which voters reach consensus is partly luck (solvency reports
	// are keyed by each node's own RPC tip), so they are dropped before the guard and
	// leave both the tally and the grace untouched (the chain's open run already ended
	// in endHaltedObservationGuardRuns).
	if k.IsChainHalted(ctx, entry.Chain) {
		emitObservationTallyTelemetry(entry, "halted")
		return nil
	}

	signers, found, err := observationTallySigners(ctx, k, entry)
	if err != nil || !found {
		return err
	}

	// Only nodes that were Active when the voter reached consensus could have signed it.
	// Churn-in sets StatusSince in EndBlock, after that block's handlers reached
	// consensus against the previous active set, so a node that went Active at the
	// consensus height is not eligible either. The voter counts towards neither the
	// misses nor the total of a node that was not eligible.
	eligible := make(NodeAccounts, 0, len(active))
	var unscored []cosmos.AccAddress
	for _, na := range active {
		if na.StatusSince < entry.ConsensusHeight {
			eligible = append(eligible, na)
		} else {
			unscored = append(unscored, na.NodeAddress)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	missed := getNonSigners(eligible, signers)

	// Nodes already failing this chain are left out of the guard's population, on
	// both sides. A failing node that did sign stays in it, which can only make the
	// guard less likely to skip.
	thresholdBps := k.GetConfigInt64(ctx, constants.MissingObservationChurnOutBps)
	minSamples := k.GetConfigInt64(ctx, constants.MissingObservationMinSamples) * observationTallyUnit
	guardBps := k.GetConfigInt64(ctx, constants.MissingObservationGuardBps)
	failing := 0
	for _, addr := range missed {
		nodeMissed, nodeTotal, tallyErr := k.GetObservationTally(ctx, addr, entry.Chain)
		if tallyErr != nil {
			return tallyErr
		}
		if observationTallyFailing(nodeMissed, nodeTotal, thresholdBps, minSamples) {
			failing++
		}
	}
	population := int64(len(eligible) - failing)
	fresh := int64(len(missed) - failing)

	guardSince, err := k.GetObservationGuardSince(ctx, entry.Chain)
	if err != nil {
		return err
	}
	guardMax := k.GetConfigInt64(ctx, constants.MissingObservationGuardMaxBlocks)
	result := "scored"
	if population > 0 && fresh*10_000 > guardBps*population {
		guardedAt[entry.Chain] = entry.ConsensusHeight
		if guardSince == 0 {
			// A new run of guarded voters starts with the grace that earlier runs used
			// and that has not refilled yet, so its start moves back by that much.
			var fullAt int64
			fullAt, err = k.GetObservationGuardFullAt(ctx, entry.Chain)
			if err != nil {
				return err
			}
			guardSince = entry.ConsensusHeight - max(fullAt-entry.ConsensusHeight, 0)
			k.SetObservationGuardSince(ctx, entry.Chain, guardSince)
			if fullAt > 0 {
				k.SetObservationGuardFullAt(ctx, entry.Chain, 0)
			}
		}
		if guardMax <= 0 || entry.ConsensusHeight-guardSince < guardMax {
			result = "guarded"
		} else {
			result = "guard_expired"
		}
	} else if guardSince != 0 && guardedAt[entry.Chain] != entry.ConsensusHeight {
		// The run ends, unless a voter at this height was above the guard share: a
		// height with any such voter uses up grace, whichever order its voters are
		// scored in. (In the other order, the start of a new run above resumes the
		// one that just ended, since none of its grace has refilled yet.)
		endObservationGuardRun(ctx, k, entry.Chain, guardSince, entry.ConsensusHeight)
	}
	if result != "guarded" {
		k.AddObservationTally(ctx, entry.Chain, observationTallyUnit, missed, unscored)
	}
	emitObservationTallyTelemetry(entry, result)
	return nil
}

// endHaltedObservationGuardRuns ends the open run of guarded voters on every halted
// chain at this block, keeping the grace it used. The grace's heights are absolute,
// so a run left open would use up grace for the whole halt and leave none for the
// catch-up after it. It is done every block rather than when a halted chain's voter
// is scored, because during a halt the chain's voters often stop altogether. The
// grace refills during the halt, which only governance or the network can start.
func endHaltedObservationGuardRuns(ctx cosmos.Context, k keeper.Keeper) error {
	chains, err := k.GetObservationGuardChains(ctx)
	if err != nil {
		return err
	}
	for _, chain := range chains {
		if !k.IsChainHalted(ctx, chain) {
			continue
		}
		guardSince, err := k.GetObservationGuardSince(ctx, chain)
		if err != nil {
			return err
		}
		endObservationGuardRun(ctx, k, chain, guardSince, ctx.BlockHeight())
	}
	return nil
}

// endObservationGuardRun ends the chain's open run of guarded voters at height. The
// grace the run used, up to the limit, refills one block per block from there, so it
// is full again that many blocks on.
func endObservationGuardRun(ctx cosmos.Context, k keeper.Keeper, chain common.Chain, guardSince, height int64) {
	k.SetObservationGuardSince(ctx, chain, 0)
	if guardMax := k.GetConfigInt64(ctx, constants.MissingObservationGuardMaxBlocks); guardMax > 0 {
		used := min(height-guardSince, guardMax)
		k.SetObservationGuardFullAt(ctx, chain, height+used)
	}
}

func emitObservationTallyTelemetry(entry kvTypes.ObservationTallyEntry, result string) {
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "observation_tally", "voter"}, 1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(entry.Chain)),
			telemetry.NewLabel("kind", entry.Kind),
			telemetry.NewLabel("result", result),
		},
	)
}

// observationTallySigners returns the signers a queued voter credited, read back
// from the voter's own store. found is false when the voter no longer has consensus.
func observationTallySigners(ctx cosmos.Context, k keeper.Keeper, entry kvTypes.ObservationTallyEntry) ([]cosmos.AccAddress, bool, error) {
	if len(entry.Ref) == 0 {
		return nil, false, fmt.Errorf("observation tally entry has no voter reference")
	}
	switch entry.Kind {
	case observationTallyKindTxIn, observationTallyKindTxOut:
		// Ref holds TxID.String(), so convert back without re-validating.
		txID := common.TxID(entry.Ref[0])
		var voter ObservedTxVoter
		var err error
		if entry.Kind == observationTallyKindTxIn {
			voter, err = k.GetObservedTxInVoter(ctx, txID)
		} else {
			voter, err = k.GetObservedTxOutVoter(ctx, txID)
		}
		if err != nil || voter.FinalisedHeight == 0 || voter.Tx.IsEmpty() {
			return nil, false, err
		}
		// The consensus tx's signers are the credited set: the observers recorded at
		// consensus plus the late signers the flex branch appended.
		return voter.Tx.GetSigners(), true, nil
	case observationTallyKindNetworkFee:
		if len(entry.Ref) < 3 {
			return nil, false, fmt.Errorf("network fee tally entry needs height, rate and size")
		}
		var refs [3]int64
		for i := range refs {
			v, err := strconv.ParseInt(entry.Ref[i], 10, 64)
			if err != nil {
				return nil, false, err
			}
			refs[i] = v
		}
		voter, err := k.GetObservedNetworkFeeVoter(ctx, refs[0], entry.Chain, refs[1], refs[2])
		if err != nil || voter.BlockHeight == 0 {
			return nil, false, err
		}
		return voter.GetSigners(), true, nil
	case observationTallyKindSolvency:
		voter, err := k.GetSolvencyVoter(ctx, common.TxID(entry.Ref[0]), entry.Chain)
		if err != nil || voter.ConsensusBlockHeight == 0 {
			return nil, false, err
		}
		return voter.GetSigners(), true, nil
	case observationTallyKindErrata:
		voter, err := k.GetErrataTxVoter(ctx, common.TxID(entry.Ref[0]), entry.Chain)
		if err != nil || voter.BlockHeight == 0 {
			return nil, false, err
		}
		return voter.GetSigners(), true, nil
	default:
		return nil, false, fmt.Errorf("unknown observation tally kind %q", entry.Kind)
	}
}

// observationTallyFailing reports whether a node's tally on a chain has enough
// samples and a missed share above thresholdBps.
func observationTallyFailing(missed, total, thresholdBps, minSamples int64) bool {
	return total > 0 && total >= minSamples && missed*10_000 > thresholdBps*total
}

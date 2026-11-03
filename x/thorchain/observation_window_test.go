package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// TestChainWindowPreservesMixedGuardGrace keeps passing and guarded voter kinds
// together, in either order, until the shared outage has used its actual grace.
func (s *ObservationTallySuite) TestChainWindowPreservesMixedGuardGrace(c *C) {
	const window, grace int64 = 5, 4
	for _, guardedIn := range []bool{false, true} {
		ctx, mgr, nas := setupObservationTallyTest(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.ObservationDelayFlexibility.String(), 2)
		k.SetMimir(ctx, "ObservationDelayFlexibilityDASH", window)
		k.SetMimir(ctx, constants.MissingObservationGuardBps.String(), 1000)
		k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), grace)
		k.SetMimir(ctx, constants.MissingObservationHalfLife.String(), 0)
		start := ctx.BlockHeight()
		for elapsed := int64(0); elapsed <= 13; elapsed++ {
			ctx = ctx.WithBlockHeight(start + elapsed)
			if elapsed <= 8 {
				inCount, feeCount := 20, 20
				if elapsed < 8 {
					if guardedIn {
						inCount = 17
					} else {
						feeCount = 17
					}
				}
				tx := observeSlashOutboundTx(GetRandomTxHash(), GetRandomPubKey(), 1000, 100+elapsed)
				tx.Tx.Chain = common.DASHChain
				tx.Tx.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One)))
				tx.Tx.Gas = common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}
				tx.Tx.Memo = "NOOP"
				voter := NewObservedTxVoter(tx.Tx.ID, nil)
				for _, na := range nas[:inCount] {
					voter, _ = processTxInAttestation(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
				}
				c.Assert(voter.FinalisedHeight, Equals, ctx.BlockHeight())
				nf := &common.NetworkFee{Chain: common.DASHChain, Height: 100 + elapsed, TransactionRate: 2, TransactionSize: 2000}
				signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, feeCount)...)
			}
			c.Assert(processObservationTallies(ctx, mgr), IsNil)
			if elapsed < 5 {
				checkTally(c, ctx, mgr, nas[19], common.DASHChain, 0, 0)
			} else if elapsed < 13 {
				// One passing voter per height; the guarded kind starts counting
				// only after four consensus heights have consumed the grace.
				missed := max(elapsed-8, 0)
				checkTally(c, ctx, mgr, nas[19], common.DASHChain, missed, elapsed-4+missed)
				since, err := k.GetObservationGuardSince(ctx, common.DASHChain)
				c.Assert(err, IsNil)
				c.Assert(since > 0, Equals, true)
				// Passing then guarded voters close and reopen the run at the same
				// height, retaining at most four spent blocks. Check the remaining grace,
				// since the stored start can move once that grace is exhausted.
				consensusHeight := ctx.BlockHeight() - window
				remaining := max(grace-(consensusHeight-since), 0)
				c.Assert(remaining, Equals, max(grace-(elapsed-window), 0))
			}
		}
		checkTally(c, ctx, mgr, nas[19], common.DASHChain, 4, 14)
		since, err := k.GetObservationGuardSince(ctx, common.DASHChain)
		c.Assert(err, IsNil)
		c.Assert(since, Equals, int64(0))
		fullAt, err := k.GetObservationGuardFullAt(ctx, common.DASHChain)
		c.Assert(err, IsNil)
		c.Assert(fullAt, Equals, start+12)
	}
}

// TestOtherObservationWindowsCreditLateSigners checks the three non-transaction
// handlers against their tally deadlines, including TRON's solvency exemption.
func (s *ObservationTallySuite) TestOtherObservationWindowsCreditLateSigners(c *C) {
	for _, kind := range []string{observationTallyKindNetworkFee, observationTallyKindSolvency, observationTallyKindErrata} {
		for _, chain := range []common.Chain{common.DASHChain, common.BTCChain, common.TRONChain} {
			if chain == common.TRONChain && kind != observationTallyKindSolvency {
				continue
			}
			for _, delay := range []int64{1, 2, 3, 5, 6} {
				checkOtherObservationWindow(c, kind, chain, delay)
			}
		}
	}
}

// checkOtherObservationWindow exercises real consensus, late credit and end-block scoring.
func checkOtherObservationWindow(c *C, kind string, chain common.Chain, delay int64) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.ObservationDelayFlexibility.String(), 2)
	k.SetMimir(ctx, "ObservationDelayFlexibilityDASH", 5)
	k.SetMimir(ctx, "ObservationDelayFlexibilityTRON", 5)
	k.SetMimir(ctx, constants.MissingObservationGuardBps.String(), 10_000)
	k.SetMimir(ctx, "StopSolvencyCheck", 1)
	late := nas[19].NodeAddress
	k.SetNodeAccountSlashPoints(ctx, late, 50)
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), []string{chain.String()}, nil)
	c.Assert(k.SetVault(ctx, vault), IsNil)
	nf := &common.NetworkFee{Chain: chain, Height: 100, TransactionRate: 2, TransactionSize: 2000}
	feeVoter, err := k.GetObservedNetworkFeeVoter(ctx, 100, chain, 2, 2000)
	c.Assert(err, IsNil)
	solvency := NewSolvencyVoter(GetRandomTxHash(), chain, vault.PubKey, common.Coins{}, 100)
	sol := &common.Solvency{Id: solvency.Id, Chain: chain, PubKey: vault.PubKey, Coins: solvency.Coins, Height: 100}
	observed := GetRandomObservedTx()
	// An unfinalized inbound gives errata no economic action while allowing
	// its observation quorum and slash accounting to complete normally.
	k.SetObservedTxInVoter(ctx, NewObservedTxVoter(observed.Tx.ID, []common.ObservedTx{observed}))
	errata := NewErrataTxVoter(observed.Tx.ID, chain)
	er := &common.ErrataTx{Id: errata.TxID, Chain: chain}
	attest := func(at cosmos.Context, node NodeAccount) {
		switch kind {
		case observationTallyKindNetworkFee:
			err = processNetworkFeeAttestation(at, mgr, &feeVoter, node.NodeAddress, nas, nf, false)
			k.SetObservedNetworkFeeVoter(at, feeVoter)
		case observationTallyKindSolvency:
			err = processSolvencyAttestation(at, mgr, &solvency, node.NodeAddress, nas, sol, false)
			k.SetSolvencyVoter(at, solvency)
		case observationTallyKindErrata:
			err = processErrataTxAttestation(at, mgr, &errata, node.NodeAddress, nas, er, false)
			k.SetErrataTxVoter(at, errata)
		}
		c.Assert(err, IsNil)
	}
	for _, node := range nas[:19] {
		attest(ctx, node)
	}
	window := int64(5)
	if chain == common.BTCChain {
		window = 2
	}
	exempt := kind == observationTallyKindSolvency && chain == common.TRONChain
	start := ctx.BlockHeight()
	for elapsed := int64(1); elapsed <= max(window, delay)+1; elapsed++ {
		ctx = ctx.WithBlockHeight(start + elapsed)
		if elapsed == delay {
			attest(ctx, nas[19])
		}
		c.Assert(processObservationTallies(ctx, mgr), IsNil)
		var missed, total int64
		if !exempt && elapsed >= window {
			total = 1
			if delay > window {
				missed = 1
			}
		}
		checkTally(c, ctx, mgr, nas[19], chain, missed, total)
	}
	points, err := k.GetNodeAccountSlashPoints(ctx, late)
	c.Assert(err, IsNil)
	want := int64(50)
	if !exempt && delay > window {
		want += mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	}
	c.Assert(points, Equals, want, Commentf("kind=%s chain=%s delay=%d", kind, chain, delay))
}

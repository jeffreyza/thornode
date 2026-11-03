package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

// TestDashTransactionWindowGuardWaitsForLateObservers checks the guard against
// the signers present at the DASH deadline, rather than the shorter global one.
func (s *ObservationTallySuite) TestDashTransactionWindowGuardWaitsForLateObservers(c *C) {
	for _, outbound := range []bool{false, true} {
		for _, catchUp := range []bool{false, true} {
			ctx, mgr, nas := setupObservationTallyTest(c)
			k := mgr.Keeper()
			k.SetMimir(ctx, constants.ObservationDelayFlexibility.String(), 2)
			k.SetMimir(ctx, "ObservationDelayFlexibilityDASH", 5)
			k.SetMimir(ctx, constants.MissingObservationGuardBps.String(), 1000)
			tx := observeSlashOutboundTx(GetRandomTxHash(), GetRandomPubKey(), 1000, 100)
			tx.Tx.Chain = common.DASHChain
			tx.Tx.Coins = common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One)))
			tx.Tx.Gas = common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(1000))}
			process := processTxInAttestation
			if outbound {
				process = processTxOutAttestation
			} else {
				tx.Tx.Memo = "NOOP"
			}
			voter := NewObservedTxVoter(tx.Tx.ID, nil)
			for _, na := range nas[:14] {
				voter, _ = process(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
			}
			consensusHeight := ctx.BlockHeight()
			c.Assert(voter.FinalisedHeight, Equals, consensusHeight)
			for elapsed := int64(2); elapsed <= 5; elapsed++ {
				ctx = ctx.WithBlockHeight(consensusHeight + elapsed)
				if catchUp && elapsed == 5 {
					// ChainLock observations arrive on the last allowed block.
					tx.BlockHeight, tx.FinaliseHeight = 101, 101
					for _, na := range nas[14:18] {
						voter, _ = process(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
					}
				}
				c.Assert(processObservationTallies(ctx, mgr), IsNil)
				guardSince, err := k.GetObservationGuardSince(ctx, common.DASHChain)
				c.Assert(err, IsNil)
				switch {
				case elapsed < 5:
					checkTally(c, ctx, mgr, nas[19], common.DASHChain, 0, 0)
					c.Assert(guardSince, Equals, int64(0))
				case catchUp:
					// Two misses out of twenty are within the 10% guard threshold.
					checkTally(c, ctx, mgr, nas[19], common.DASHChain, 1, 1)
					checkTally(c, ctx, mgr, nas[17], common.DASHChain, 0, 1)
					c.Assert(guardSince, Equals, int64(0))
				default:
					// Six misses remain a shared event and must not score anyone.
					checkTally(c, ctx, mgr, nas[19], common.DASHChain, 0, 0)
					checkTally(c, ctx, mgr, nas[0], common.DASHChain, 0, 0)
					c.Assert(guardSince, Equals, consensusHeight)
				}
			}
		}
	}
}

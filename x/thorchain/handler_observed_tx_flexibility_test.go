package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type failingObservationFlexKeeper struct{ keeper.Keeper }

func (k failingObservationFlexKeeper) GetMimir(cosmos.Context, string) (int64, error) {
	return -1, errors.New("Mimir read failed")
}

func (s *HandlerObservedTxHelpersSuite) TestObservationDelayFlexibilityChainFallback(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	key := constants.ObservationDelayFlexibility.String()
	k.SetMimir(ctx, key, 3)
	c.Assert(observationDelayFlexibility(ctx, k, common.DASHChain), Equals, int64(3))
	k.SetMimir(ctx, key+"DASH", 50)
	c.Assert(observationDelayFlexibility(ctx, k, common.DASHChain), Equals, int64(50))
	for _, chain := range []common.Chain{common.BTCChain, common.LTCChain, common.ETHChain} {
		c.Assert(observationDelayFlexibility(ctx, k, chain), Equals, int64(3))
	}
	k.SetMimir(ctx, key+"DASH", 0)
	c.Assert(observationDelayFlexibility(ctx, k, common.DASHChain), Equals, int64(0))
	k.SetMimir(ctx, key+"DASH", -1)
	c.Assert(observationDelayFlexibility(ctx, k, common.DASHChain), Equals, int64(3))
	c.Assert(observationDelayFlexibility(ctx, failingObservationFlexKeeper{k}, common.DASHChain), Equals, int64(3))
}

func (s *HandlerObservedTxHelpersSuite) TestObservationDelayFlexibilityChainSlashBoundaries(c *C) {
	for _, outbound := range []bool{false, true} {
		for _, tc := range []struct {
			chain           common.Chain
			override, delay int64
			refunded        bool
		}{
			{common.DASHChain, 10, 10, true},
			{common.DASHChain, 10, 11, false},
			{common.DASHChain, 0, 0, true},
			{common.DASHChain, 0, 1, false},
			{common.DASHChain, -1, 2, true},
			{common.DASHChain, -1, 3, false},
			{common.BTCChain, 10, 2, true},
			{common.BTCChain, 10, 3, false},
		} {
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(100)
			mgr.Keeper().SetMimir(ctx, constants.ObservationDelayFlexibility.String(), 2)
			mgr.Keeper().SetMimir(ctx, "ObservationDelayFlexibilityDASH", tc.override)
			nas, pubkey := observeSlashSetup(c, ctx, mgr)
			late := nas[3].NodeAddress
			mgr.Keeper().SetNodeAccountSlashPoints(ctx, late, 50)
			tx := observeSlashOutboundTx(GetRandomTxHash(), pubkey, 1000, 100)
			tx.Tx.Chain = tc.chain
			tx.Tx.Coins = common.NewCoins(common.NewCoin(tc.chain.GetGasAsset(), cosmos.NewUint(common.One)))
			tx.Tx.Gas = common.Gas{common.NewCoin(tc.chain.GetGasAsset(), cosmos.NewUint(1000))}
			process := processTxInAttestation
			if outbound {
				process = processTxOutAttestation
			} else {
				tx.Tx.Memo = "NOOP"
			}
			voter := NewObservedTxVoter(tx.Tx.ID, nil)
			for i := range 3 {
				voter, _ = process(ctx, mgr, voter, nas, tx, nas[i].NodeAddress, false)
			}
			c.Assert(voter.FinalisedHeight, Equals, int64(100))
			lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
			points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, late)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, int64(50)+lack) // override does not postpone penalties
			ctx = ctx.WithBlockHeight(100 + tc.delay)
			// A later ChainLock observation can use its inclusion height.
			tx.BlockHeight = 101
			tx.FinaliseHeight = 101
			_, _ = process(ctx, mgr, voter, nas, tx, late, false)
			points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, late)
			c.Assert(err, IsNil)
			want := int64(50)
			if !tc.refunded {
				want += lack + mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
			}
			c.Assert(points, Equals, want, Commentf("outbound=%v chain=%s override=%d delay=%d", outbound, tc.chain, tc.override, tc.delay))
		}
	}
}

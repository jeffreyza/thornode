package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// TestDashOutboundScannerObservationCredit verifies the two scanner variants
// remain free of an extra observe point, even beyond the late-observation window.
func (s *HandlerObservedTxHelpersSuite) TestDashOutboundScannerObservationCredit(c *C) {
	for _, migration := range []bool{false, true} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		nas := make(NodeAccounts, 4)
		for i := range nas {
			nas[i] = GetRandomValidatorNode(NodeActive)
			mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[i].NodeAddress, 50)
		}
		pubkey := GetRandomPubKey()
		from, err := pubkey.GetAddress(common.DASHChain)
		c.Assert(err, IsNil)
		to, err := GetRandomPubKey().GetAddress(common.DASHChain)
		c.Assert(err, IsNil)
		txID := GetRandomTxHash()
		memo := "OUT:" + txID.String()
		finalHeight := int64(150) // ChainLock inclusion
		if migration {
			memo = "MIGRATE:90"
			finalHeight = 110 // InstantSend lock cycle
		}
		mempool := common.NewObservedTx(common.Tx{
			ID: txID, Chain: common.DASHChain, FromAddress: from, ToAddress: to,
			Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One))),
			Gas:   common.Gas{common.NewCoin(common.DASHAsset, cosmos.NewUint(2000))}, Memo: memo,
		}, 123, pubkey, 123)
		final := mempool
		final.BlockHeight, final.FinaliseHeight = finalHeight, finalHeight
		c.Assert(mempool.Valid(), IsNil)
		c.Assert(final.Valid(), IsNil)
		voter := NewObservedTxVoter(txID, nil)
		submit := func(tx common.ObservedTx, signer cosmos.AccAddress) {
			_, _ = processTxOutAttestation(ctx, mgr, voter, nas, tx, signer, false)
			voter, err = mgr.Keeper().GetObservedTxOutVoter(ctx, txID)
			c.Assert(err, IsNil) // reload between arrivals, as separate blocks do
		}
		for _, na := range nas {
			submit(mempool, na.NodeAddress)
		}
		c.Assert(voter.FinalisedHeight, Equals, int64(100))
		ctx = ctx.WithBlockHeight(1000)
		for _, na := range nas {
			submit(final, na.NodeAddress)
			var points int64
			points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, int64(50))
		}
		// A third distinct variant still costs a point. The signer change avoids
		// creating it; the consensus spam protection itself remains unchanged.
		third := final
		third.BlockHeight, third.FinaliseHeight = 160, 160
		submit(third, nas[0].NodeAddress)
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nas[0].NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(points, Equals, 50+mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints))
	}
}

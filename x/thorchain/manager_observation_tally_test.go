package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

type ObservationTallySuite struct{}

var _ = Suite(&ObservationTallySuite{})

func (s *ObservationTallySuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// setupObservationTallyTest returns twenty Active nodes, so a supermajority is 14
// and the 10% guard allows two misses per voter.
func setupObservationTallyTest(c *C) (cosmos.Context, *Mgrs, NodeAccounts) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1024)
	nas := make(NodeAccounts, 20)
	for i := range nas {
		nas[i] = GetRandomValidatorNode(NodeActive)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nas[i]), IsNil)
	}
	return ctx, mgr, nas
}

// signNetworkFee attests a network fee for each given node, saving the voter the
// way the handler does.
func signNetworkFee(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, nf *common.NetworkFee, signers ...int) {
	voter, err := mgr.Keeper().GetObservedNetworkFeeVoter(ctx, nf.Height, nf.Chain, int64(nf.TransactionRate), int64(nf.TransactionSize))
	c.Assert(err, IsNil)
	for _, i := range signers {
		c.Assert(processNetworkFeeAttestation(ctx, mgr, &voter, nas[i].NodeAddress, nas, nf, false), IsNil)
	}
	mgr.Keeper().SetObservedNetworkFeeVoter(ctx, voter)
}

func indexes(from, to int) []int {
	out := make([]int, 0, to-from)
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}

func checkTally(c *C, ctx cosmos.Context, mgr *Mgrs, na NodeAccount, chain common.Chain, missed, total int64) {
	gotMissed, gotTotal, err := mgr.Keeper().GetObservationTally(ctx, na.NodeAddress, chain)
	c.Assert(err, IsNil)
	c.Check(gotMissed, Equals, missed*observationTallyUnit, Commentf("missed for %s", na.NodeAddress))
	c.Check(gotTotal, Equals, total*observationTallyUnit, Commentf("total for %s", na.NodeAddress))
}

func (s *ObservationTallySuite) TestScoredWhenWindowCloses(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	consensusHeight := ctx.BlockHeight()
	nf := &common.NetworkFee{Chain: common.ETHChain, Height: 500, TransactionRate: 256, TransactionSize: 100}

	// 14 of 20 reach consensus; the other six are non-signers at this point
	signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, 14)...)

	// not scored before the window closes
	c.Assert(processObservationTallies(ctx.WithBlockHeight(consensusHeight+flex-1), mgr), IsNil)
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 0, 0)

	// four sign late, the last of them in the window's final block
	signNetworkFee(c, ctx.WithBlockHeight(consensusHeight+1), mgr, nas, nf, 14, 15, 16)
	signNetworkFee(c, ctx.WithBlockHeight(consensusHeight+flex), mgr, nas, nf, 17)

	ctx = ctx.WithBlockHeight(consensusHeight + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 1)
	checkTally(c, ctx, mgr, nas[17], common.ETHChain, 0, 1)
	checkTally(c, ctx, mgr, nas[18], common.ETHChain, 1, 1)
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 1, 1)

	// a signer after the window adds itself to voter.Signers, but the voter was
	// already scored, so the miss stands, as the slasher's penalty does
	signNetworkFee(c, ctx.WithBlockHeight(consensusHeight+flex+1), mgr, nas, nf, 18)
	c.Assert(processObservationTallies(ctx.WithBlockHeight(consensusHeight+flex+1), mgr), IsNil)
	checkTally(c, ctx, mgr, nas[18], common.ETHChain, 1, 1)
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 1)
}

// TestTransactionWindowCreditsLateSigners checks slash refunds and persisted
// tallies together, including a ChainLock observation with a later mined height.
func (s *ObservationTallySuite) TestTransactionWindowCreditsLateSigners(c *C) {
	for _, outbound := range []bool{false, true} {
		for _, delay := range []int64{3, 5, 6} {
			ctx, mgr, nas := setupObservationTallyTest(c)
			k := mgr.Keeper()
			k.SetMimir(ctx, constants.ObservationDelayFlexibility.String(), 2)
			k.SetMimir(ctx, "ObservationDelayFlexibilityDASH", 5)
			// A skipped voter must not hide an incorrect miss or scoring deadline.
			k.SetMimir(ctx, constants.MissingObservationGuardBps.String(), 10_000)
			late := nas[19].NodeAddress
			k.SetNodeAccountSlashPoints(ctx, late, 50)
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
			for _, na := range nas[:19] {
				voter, _ = process(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
			}
			consensusHeight := ctx.BlockHeight()
			c.Assert(voter.FinalisedHeight, Equals, consensusHeight)
			lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
			points, err := k.GetNodeAccountSlashPoints(ctx, late)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, int64(50)+lack)

			for elapsed := int64(2); elapsed <= 6; elapsed++ {
				ctx = ctx.WithBlockHeight(consensusHeight + elapsed)
				if elapsed == delay {
					tx.BlockHeight = 101
					tx.FinaliseHeight = 101
					voter, _ = process(ctx, mgr, voter, nas, tx, late, false)
				}
				c.Assert(processObservationTallies(ctx, mgr), IsNil)
				if elapsed < 5 {
					checkTally(c, ctx, mgr, nas[19], common.DASHChain, 0, 0)
					continue
				}
				var missed int64
				if delay > 5 {
					missed = 1
				}
				checkTally(c, ctx, mgr, nas[19], common.DASHChain, missed, 1)
				checkTally(c, ctx, mgr, nas[0], common.DASHChain, 0, 1)
			}
			points, err = k.GetNodeAccountSlashPoints(ctx, late)
			c.Assert(err, IsNil)
			want := int64(50)
			if delay > 5 {
				want += lack + mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
			}
			c.Check(points, Equals, want, Commentf("outbound=%v delay=%d", outbound, delay))
		}
	}
}

// TestQueueWindowByVoterKind checks that every kind uses the same override or fallback.
func (s *ObservationTallySuite) TestQueueWindowByVoterKind(c *C) {
	for _, tc := range []struct {
		name     string
		chain    common.Chain
		override int64
		readFail bool
		window   int64
	}{
		{"DASH override", common.DASHChain, 5, false, 5},
		{"zero override", common.DASHChain, 0, false, 0},
		{"unset override", common.DASHChain, -1, false, 2},
		{"failed read", common.DASHChain, 5, true, 2},
		{"other chain", common.BTCChain, 5, false, 2},
	} {
		for _, kind := range []string{
			observationTallyKindTxIn, observationTallyKindTxOut,
			observationTallyKindNetworkFee, observationTallyKindSolvency, observationTallyKindErrata,
		} {
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(100)
			k := mgr.Keeper()
			k.SetMimir(ctx, constants.ObservationDelayFlexibility.String(), 2)
			k.SetMimir(ctx, "ObservationDelayFlexibilityDASH", tc.override)
			if tc.readFail {
				k = failingObservationFlexKeeper{k}
			}
			queueObservationTally(ctx, k, kind, tc.chain, "voter")
			dueHeight := ctx.BlockHeight() + tc.window
			info := Commentf("%s kind=%s", tc.name, kind)
			entries, err := k.PopObservationTallies(ctx, dueHeight-1)
			c.Assert(err, IsNil)
			c.Assert(entries, HasLen, 0, info)
			entries, err = k.PopObservationTallies(ctx, dueHeight)
			c.Assert(err, IsNil)
			c.Assert(entries, DeepEquals, []kvTypes.ObservationTallyEntry{{
				DueHeight: dueHeight, ConsensusHeight: ctx.BlockHeight(),
				Kind: kind, Chain: tc.chain, Ref: []string{"voter"},
			}}, info)
		}
	}
}

func (s *ObservationTallySuite) TestGuardSkipsSharedEvent(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	nf := &common.NetworkFee{Chain: common.BTCChain, Height: 900, TransactionRate: 20, TransactionSize: 250}

	// three of twenty (15%) miss: a shared event, counted against no one
	signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, 17)...)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[0], common.BTCChain, 0, 0)
	checkTally(c, ctx, mgr, nas[19], common.BTCChain, 0, 0)

	// a mimir guard of 10000 turns the guard off
	mgr.Keeper().SetMimir(ctx, constants.MissingObservationGuardBps.String(), 10_000)
	nf.Height++
	signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, 17)...)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[0], common.BTCChain, 0, 1)
	checkTally(c, ctx, mgr, nas[19], common.BTCChain, 1, 1)
}

// runNetworkFeeVoters brings n network fee voters on chain to consensus, one every
// ObservationDelayFlexibility blocks, each signed by the given nodes, and scores each
// as its window closes. It returns the context at the last scoring height.
func runNetworkFeeVoters(c *C, ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, chain common.Chain, first int64, n int, signers []int) cosmos.Context {
	flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	for v := 0; v < n; v++ {
		nf := &common.NetworkFee{Chain: chain, Height: first + int64(v), TransactionRate: 7, TransactionSize: 9}
		signNetworkFee(c, ctx, mgr, nas, nf, signers...)
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + flex)
		c.Assert(processObservationTallies(ctx, mgr), IsNil)
	}
	return ctx
}

func (s *ObservationTallySuite) TestGuardExpiresForPersistentGroup(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 100)
	k.SetMimir(ctx, constants.MaxMissingObservationChurnOut.String(), 3)

	// From empty tallies, nodes 17-19 (15%, above the 10% guard) miss all 60 voters,
	// one every 10 blocks. The first 10 fall inside the 100-block grace and are skipped
	// for everyone. From then on they count, and once the three have 24 samples they
	// are already failing, so they leave the guard's population and the guard clears.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.ETHChain, 2000, 60, indexes(0, 17))
	for _, i := range []int{17, 18, 19} {
		checkTally(c, ctx, mgr, nas[i], common.ETHChain, 50, 50)
	}
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 50)
	since, err := k.GetObservationGuardSince(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))

	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(vm.markMissingObservationActors(ctx), IsNil)
	for i, na := range nas {
		after, err := k.GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Check(after.LeaveScore > 0, Equals, i >= 17, Commentf("node %d", i))
	}
}

func (s *ObservationTallySuite) TestGuardSparesSharedOutageWithinLimit(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()

	// From empty tallies, a 300-block shared outage on 3 of 20 nodes, well inside the
	// default grace (one churn interval), then recovery.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.BTCChain, 3000, 30, indexes(0, 17))
	since, err := k.GetObservationGuardSince(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(since > 0, Equals, true)

	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.BTCChain, 4000, 10, indexes(0, 20))
	for _, i := range []int{0, 17, 18, 19} {
		checkTally(c, ctx, mgr, nas[i], common.BTCChain, 0, 10)
	}
	since, err = k.GetObservationGuardSince(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))

	// The run used 300 blocks of grace (1024 to the first passing voter at 1324), which
	// refill block for block from there.
	fullAt, err := k.GetObservationGuardFullAt(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, int64(1324+300))
}

func (s *ObservationTallySuite) TestGuardCountsLongSharedOutageAfterLimit(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 100)

	// From empty tallies, 5 of 20 nodes (25%) miss 20 voters, one every 10 blocks: the
	// first 10 are forgiven, the next 10 count. Then they recover.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.LTCChain, 5000, 20, indexes(0, 15))
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.LTCChain, 6000, 10, indexes(0, 20))
	checkTally(c, ctx, mgr, nas[15], common.LTCChain, 10, 20)
	checkTally(c, ctx, mgr, nas[0], common.LTCChain, 0, 20)

	// The recovery lasted as long as the grace the outage used (100 blocks), so a new
	// shared outage gets the full grace again.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.LTCChain, 7000, 5, indexes(0, 17))
	checkTally(c, ctx, mgr, nas[19], common.LTCChain, 10, 20)
}

func (s *ObservationTallySuite) TestGuardRefillsOnlyAsLongAsTheRecovery(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 100)

	// From empty tallies, nodes 17-19 miss 6 voters (1024-1074). The run ends at the
	// first passing voter (1084), having used 60 blocks of grace, and three passing
	// voters follow.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.LTCChain, 5000, 6, indexes(0, 17))
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.LTCChain, 6000, 3, indexes(0, 20))

	// A second outage starts at 1114, 30 blocks after the recovery began, so 30 of the
	// 60 blocks have refilled and 70 are left: 7 of its 10 voters are skipped and the
	// last 3 count.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.LTCChain, 7000, 10, indexes(0, 17))
	checkTally(c, ctx, mgr, nas[19], common.LTCChain, 3, 6)
	checkTally(c, ctx, mgr, nas[0], common.LTCChain, 0, 6)
}

func (s *ObservationTallySuite) TestGuardBriefRecoveriesDoNotResetGrace(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 100)
	k.SetMimir(ctx, constants.MaxMissingObservationChurnOut.String(), 3)

	// From empty tallies, nodes 17-19 (15%) miss every voter except one in ten, which
	// all sign, so no run of skipped voters reaches the 100-block limit by itself. Each
	// recovery refills only the 10 blocks it lasts, so from the second cycle on most
	// voters count; once the three are already failing, every miss counts.
	for cycle := int64(0); cycle < 5; cycle++ {
		ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.BSCChain, 9000+100*cycle, 9, indexes(0, 17))
		ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.BSCChain, 9050+100*cycle, 1, indexes(0, 20))
	}
	for _, i := range []int{17, 18, 19} {
		checkTally(c, ctx, mgr, nas[i], common.BSCChain, 32, 37)
	}
	checkTally(c, ctx, mgr, nas[0], common.BSCChain, 0, 37)

	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(vm.markMissingObservationActors(ctx), IsNil)
	for i, na := range nas {
		after, err := k.GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Check(after.LeaveScore > 0, Equals, i >= 17, Commentf("node %d", i))
	}
}

func (s *ObservationTallySuite) TestGuardCountsEveryVoterAtAGuardedHeight(c *C) {
	// From empty tallies, ten network fee voters reach consensus in every block for 300
	// blocks. Nodes 17-19 (15%) miss nine of them; all sign the tenth, which is scored
	// after the nine or before them. A height with any voter above the guard share uses
	// up grace in either order, so the passing voter cannot end the run at that height:
	// the first 100 blocks are skipped and the nine misses of each of the other 200
	// count.
	for _, passingRef := range []int64{90_000, 10_000} {
		ctx, mgr, nas := setupObservationTallyTest(c)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 100)
		k.SetMimir(ctx, constants.MaxMissingObservationChurnOut.String(), 3)
		flex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)

		start := ctx.BlockHeight()
		for h := start; h < start+300+flex; h++ {
			ctx = ctx.WithBlockHeight(h)
			if h < start+300 {
				for v := int64(0); v < 9; v++ {
					nf := &common.NetworkFee{Chain: common.ETHChain, Height: 20_000 + 10*h + v, TransactionRate: 7, TransactionSize: 9}
					signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, 17)...)
				}
				nf := &common.NetworkFee{Chain: common.ETHChain, Height: passingRef + h, TransactionRate: 7, TransactionSize: 9}
				signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, 20)...)
			}
			c.Assert(processObservationTallies(ctx, mgr), IsNil)
		}
		for _, i := range []int{17, 18, 19} {
			checkTally(c, ctx, mgr, nas[i], common.ETHChain, 1800, 2100)
		}
		checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 2100)

		vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
		c.Assert(vm.markMissingObservationActors(ctx), IsNil)
		for i, na := range nas {
			after, err := k.GetNodeAccount(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Check(after.LeaveScore > 0, Equals, i >= 17, Commentf("node %d, passing ref %d", i, passingRef))
		}
	}
}

func (s *ObservationTallySuite) TestGuardMaxBlocksZeroSparesForever(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 0)

	// 0 opts out of the grace limit: a group above the guard share is never counted.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.ETHChain, 8000, 60, indexes(0, 17))
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 0, 0)
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 0)
}

func (s *ObservationTallySuite) TestGuardLeavesOutNodesAlreadyFailing(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	flex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)

	// nodes 15-18 have missed all of 30 earlier voters on ETH
	failing := []cosmos.AccAddress{nas[15].NodeAddress, nas[16].NodeAddress, nas[17].NodeAddress, nas[18].NodeAddress}
	for i := 0; i < 30; i++ {
		k.AddObservationTally(ctx, common.ETHChain, observationTallyUnit, failing, nil)
	}

	// they miss again with one healthy node: 5 of 20 is 25%, but only 1 of the 16
	// not already failing missed, so the voter counts
	nf := &common.NetworkFee{Chain: common.ETHChain, Height: 700, TransactionRate: 9, TransactionSize: 21}
	signNetworkFee(c, ctx, mgr, nas, nf, indexes(0, 15)...)
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 31)
	checkTally(c, ctx, mgr, nas[15], common.ETHChain, 31, 31)
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 1, 31)
}

func (s *ObservationTallySuite) TestHaltedChainVotersAreDropped(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 100)
	k.SetMimir(ctx, "HaltETHChain", 1)

	// While ETH is halted, neither a voter that one node misses (would count) nor
	// voters that three miss (would be guarded, past the grace) touch the tally or
	// the guard.
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.ETHChain, 8000, 5, indexes(0, 19))
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.ETHChain, 8100, 15, indexes(0, 17))
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 0)
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 0, 0)
	since, err := k.GetObservationGuardSince(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))
	fullAt, err := k.GetObservationGuardFullAt(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, int64(0))

	// other chains are still scored
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.BTCChain, 8200, 1, indexes(0, 19))
	checkTally(c, ctx, mgr, nas[19], common.BTCChain, 1, 1)

	// after the unhalt, the chain's voters count again
	k.SetMimir(ctx, "HaltETHChain", 0)
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.ETHChain, 8300, 3, indexes(0, 19))
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 3)
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 3, 3)
}

func (s *ObservationTallySuite) TestHaltEndsOpenGuardRun(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationGuardMaxBlocks.String(), 100)

	// a run of guarded voters opens
	start := ctx.BlockHeight()
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.ETHChain, 9000, 3, indexes(0, 17))
	since, err := k.GetObservationGuardSince(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, start)

	// the halt ends the run in its first block, keeping the grace it used, though no
	// voter on the chain is scored for the whole halt, which outlasts the grace
	k.SetMimir(ctx, "HaltETHChain", 1)
	haltAt := ctx.BlockHeight()
	for h := haltAt; h < haltAt+150; h++ {
		ctx = ctx.WithBlockHeight(h)
		c.Assert(processObservationTallies(ctx, mgr), IsNil)
	}
	since, err = k.GetObservationGuardSince(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(since, Equals, int64(0))
	fullAt, err := k.GetObservationGuardFullAt(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Check(fullAt, Equals, haltAt+(haltAt-start))

	// so the catch-up after the halt is guarded
	k.SetMimir(ctx, "HaltETHChain", 0)
	ctx = runNetworkFeeVoters(c, ctx, mgr, nas, common.ETHChain, 9200, 5, indexes(0, 17))
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 0, 0)
}

func (s *ObservationTallySuite) TestNodeNotActiveAtConsensusIsNotScored(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	flex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	consensusHeight := ctx.BlockHeight()

	// an earlier voter gives BTC a chain total, so node 19's reset sets a base
	k.AddObservationTally(ctx, common.BTCChain, observationTallyUnit, nil, nil)

	reachConsensus := func(ctx cosmos.Context) {
		tx := GetRandomObservedTx()
		tx.Tx.Chain = common.BTCChain
		tx.BlockHeight = 10
		tx.FinaliseHeight = 10
		voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{})
		for _, na := range nas[:18] {
			voter, _ = processTxInAttestation(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
		}
		c.Assert(voter.FinalisedHeight, Equals, ctx.BlockHeight())
	}
	reachConsensus(ctx)

	// node 19 churned in during the EndBlock of the block in which the voter reached
	// consensus, so the handler judged the voter against the previous active set
	nas[19].StatusSince = consensusHeight
	c.Assert(k.SetNodeAccount(ctx, nas[19]), IsNil)
	c.Assert(k.ResetObservationTally(ctx, nas[19].NodeAddress), IsNil)

	// the next voter reaches consensus after node 19 went Active, and it misses it
	reachConsensus(ctx.WithBlockHeight(consensusHeight + 1))

	ctx = ctx.WithBlockHeight(consensusHeight + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[0], common.BTCChain, 0, 2)
	checkTally(c, ctx, mgr, nas[18], common.BTCChain, 1, 2)
	// the pre-churn voter counts towards neither node 19's misses nor its total
	checkTally(c, ctx, mgr, nas[19], common.BTCChain, 0, 0)

	ctx = ctx.WithBlockHeight(consensusHeight + 1 + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[18], common.BTCChain, 2, 3)
	checkTally(c, ctx, mgr, nas[19], common.BTCChain, 1, 1)
}

func (s *ObservationTallySuite) TestEachVoterKindIsScored(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	flex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	signersOf := func(nodes NodeAccounts) []string {
		out := make([]string, 0, len(nodes))
		for _, na := range nodes {
			out = append(out, na.NodeAddress.String())
		}
		return out
	}

	// outbound: through the attestation path, so the hook is exercised
	tx := GetRandomObservedTx()
	tx.Tx.Chain = common.ETHChain
	tx.BlockHeight = 10
	tx.FinaliseHeight = 10
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{})
	for _, na := range nas[:19] {
		voter, _ = processTxOutAttestation(ctx, mgr, voter, nas, tx, na.NodeAddress, false)
	}

	// solvency and errata: voters as their handlers leave them at consensus
	solvency := NewSolvencyVoter(GetRandomTxHash(), common.LTCChain, GetRandomPubKey(), common.Coins{}, 1)
	solvency.ConsensusBlockHeight = ctx.BlockHeight()
	solvency.Signers = signersOf(nas[1:])
	k.SetSolvencyVoter(ctx, solvency)
	queueObservationTally(ctx, k, observationTallyKindSolvency, solvency.Chain, solvency.Id.String())

	errata := NewErrataTxVoter(GetRandomTxHash(), common.DOGEChain)
	errata.BlockHeight = ctx.BlockHeight()
	errata.Signers = signersOf(append(NodeAccounts{nas[0]}, nas[2:]...))
	k.SetErrataTxVoter(ctx, errata)
	queueObservationTally(ctx, k, observationTallyKindErrata, errata.Chain, errata.TxID.String())

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[19], common.ETHChain, 1, 1)
	checkTally(c, ctx, mgr, nas[0], common.ETHChain, 0, 1)
	checkTally(c, ctx, mgr, nas[0], common.LTCChain, 1, 1)
	checkTally(c, ctx, mgr, nas[19], common.LTCChain, 0, 1)
	checkTally(c, ctx, mgr, nas[1], common.DOGEChain, 1, 1)
	checkTally(c, ctx, mgr, nas[0], common.DOGEChain, 0, 1)
}

// TestErrataScoredOnceAtFirstConsensus drives processErrataTxAttestation itself: the
// errata voter is queued in the block that first settles its consensus and scored once,
// however many attestations follow.
func (s *ObservationTallySuite) TestErrataScoredOnceAtFirstConsensus(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	flex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	er := &common.ErrataTx{Id: GetRandomTxHash(), Chain: common.BTCChain}
	voter, err := k.GetErrataTxVoter(ctx, er.Id, er.Chain)
	c.Assert(err, IsNil)
	for _, na := range nas[:19] {
		// The economic side of the errata has nothing to act on here; only the
		// observation accounting matters to this test.
		_ = processErrataTxAttestation(ctx, mgr, &voter, na.NodeAddress, nas, er, false)
	}
	k.SetErrataTxVoter(ctx, voter)
	c.Assert(voter.BlockHeight, Equals, ctx.BlockHeight())

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + flex)
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	checkTally(c, ctx, mgr, nas[19], common.BTCChain, 1, 1)
	checkTally(c, ctx, mgr, nas[0], common.BTCChain, 0, 1)

	entries, err := k.PopObservationTallies(ctx, ctx.BlockHeight()+flex)
	c.Assert(err, IsNil)
	c.Check(entries, HasLen, 0, Commentf("queued once, at first consensus"))
}

func (s *ObservationTallySuite) TestHalvingOnHalfLifeBoundary(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.SetMimir(ctx, constants.MissingObservationHalfLife.String(), 100)

	for i := 0; i < 4; i++ {
		k.AddObservationTally(ctx, common.BTCChain, observationTallyUnit, []cosmos.AccAddress{nas[0].NodeAddress}, nil)
	}

	c.Assert(processObservationTallies(ctx.WithBlockHeight(1099), mgr), IsNil)
	checkTally(c, ctx, mgr, nas[0], common.BTCChain, 4, 4)

	c.Assert(processObservationTallies(ctx.WithBlockHeight(1100), mgr), IsNil)
	checkTally(c, ctx, mgr, nas[0], common.BTCChain, 2, 2)
	checkTally(c, ctx, mgr, nas[1], common.BTCChain, 0, 2)
}

func (s *ObservationTallySuite) TestUnknownKindIsDropped(c *C) {
	ctx, mgr, nas := setupObservationTallyTest(c)
	k := mgr.Keeper()
	k.QueueObservationTally(ctx, kvTypes.ObservationTallyEntry{
		DueHeight: ctx.BlockHeight(), ConsensusHeight: ctx.BlockHeight(), Kind: "BOGUS", Chain: common.BTCChain, Ref: []string{"x"},
	})
	c.Assert(processObservationTallies(ctx, mgr), IsNil)
	entries, err := k.PopObservationTallies(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Check(entries, HasLen, 0)
	checkTally(c, ctx, mgr, nas[0], common.BTCChain, 0, 0)
}

func (s *ObservationTallySuite) TestObservationTallyFailing(c *C) {
	unit := observationTallyUnit
	c.Check(observationTallyFailing(0, 0, 5000, 0), Equals, false)
	c.Check(observationTallyFailing(13*unit, 24*unit, 5000, 24*unit), Equals, true)
	c.Check(observationTallyFailing(12*unit, 24*unit, 5000, 24*unit), Equals, false) // exactly 50% is not above it
	c.Check(observationTallyFailing(23*unit, 23*unit, 5000, 24*unit), Equals, false) // too few samples
}

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

type SolNonceCreateSuite struct{}

var _ = Suite(&SolNonceCreateSuite{})

const (
	solNonceRent = uint64(144768) // 1,447,680 lamports in 1e8 units
	solNonceFee  = uint64(500)
)

// createObs builds a final observation of the vault funding its own nonce account.
func (f solNonceFixture) createObs(c *C, signers ...NodeAccount) common.ObservedTx {
	txID := GetRandomTxHash()
	tx := f.obs(txID, &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}, true, signers...)
	tx.Tx.ToAddress = common.Address(f.nonceAcct)
	tx.Tx.Memo = ""
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceRent)))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee))}
	return tx
}

func (s *SolNonceCreateSuite) TestIsSolNonceCreateTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	c.Assert(isSolNonceCreateTx(f.createObs(c)), Equals, true)

	other, err := GetRandomVault().PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	cases := map[string]func(*common.ObservedTx){
		"memo set":          func(t *common.ObservedTx) { t.Tx.Memo = "OUT:x" },
		"to not nonce acct": func(t *common.ObservedTx) { t.Tx.ToAddress = other },
		"sender not deriving the nonce acct": func(t *common.ObservedTx) {
			t.Tx.FromAddress = other
		},
		"two coins": func(t *common.ObservedTx) {
			t.Tx.Coins = append(t.Tx.Coins, common.NewCoin(common.SOLAsset, cosmos.NewUint(1)))
		},
		"non-SOL coin": func(t *common.ObservedTx) {
			t.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(solNonceRent)))
		},
		"above rent cap": func(t *common.ObservedTx) {
			t.Tx.Coins = common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceCreateMaxRent+1)))
		},
		"non-SOL chain": func(t *common.ObservedTx) { t.Tx.Chain = common.BTCChain },
	}
	for name, mutate := range cases {
		tx := f.createObs(c)
		mutate(&tx)
		c.Assert(isSolNonceCreateTx(tx), Equals, false, Commentf(name))
	}
	// The branch does not depend on the attested nonce (one vote could omit it).
	tx0 := f.createObs(c)
	tx0.SolNonce = nil
	c.Assert(isSolNonceCreateTx(tx0), Equals, true)

	// Exactly at the cap is still a creation.
	tx := f.createObs(c)
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceCreateMaxRent)))
	c.Assert(isSolNonceCreateTx(tx), Equals, true)
}

// The creation is an operational spend: rent + fee come off the vault as gas, the
// initial nonce is committed, and nobody is slashed for an unmatched outflow.
func (s *SolNonceCreateSuite) TestQuorumBooksRentAsGas(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	start := uint64(10 * common.One)
	f.fundAndBond(c, ctx, mgr)

	tx := f.createObs(c, f.nas[0], f.nas[1], f.nas[2])
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
	voter.Tx = tx
	voter.FinalisedHeight = ctx.BlockHeight()
	handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		c.Fatal("a nonce creation must not reach the outbound handler")
		return nil, nil
	}
	c.Assert(handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, handler, tx, voter, nil, true), IsNil)

	vault, err := mgr.Keeper().GetVault(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	got := vault.Coins.GetCoin(common.SOLAsset).Amount.Uint64()
	c.Assert(got, Equals, start-solNonceRent-solNonceFee)

	n, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(n.Nonce, Equals, tx.SolNonce.Nonce)

	// No member is slashed: bonds and slash points are untouched.
	for _, na := range f.nas {
		pts, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(pts, Equals, int64(0))
		after, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(after.Bond.Equal(na.Bond), Equals, true, Commentf("bond %s -> %s", na.Bond, after.Bond))
	}
}

// A memoless transfer to the vault's nonce address WITHOUT an agreed nonce (e.g. a
// scheduled memoless payment there) is not a creation: it is not written off as gas
// and goes through normal outbound processing.
func (s *SolNonceCreateSuite) TestNoCommittedNonceNotBookedAsGas(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	f.fundAndBond(c, ctx, mgr)

	tx := f.createObs(c, f.nas[0], f.nas[1], f.nas[2])
	tx.SolNonce = nil
	c.Assert(isSolNonceCreateTx(tx), Equals, true) // structurally identical
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
	voter.Tx = tx
	voter.FinalisedHeight = ctx.BlockHeight()
	handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return nil, fmt.Errorf("not a scheduled outbound")
	}
	_ = handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, handler, tx, voter, nil, true)

	// Normal outbound processing ran: the unmatched outflow is slashed from bonds.
	slashed := false
	for _, na := range f.nas {
		after, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		slashed = slashed || after.Bond.LT(na.Bond)
	}
	c.Assert(slashed, Equals, true)
}

// fundAndBond gives the vault SOL, a SOL pool, and bonded member nodes, so gas
// booking and vault slashing can be observed.
func (f *solNonceFixture) fundAndBond(c *C, ctx cosmos.Context, mgr Manager) {
	f.vault.Coins = common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(10*common.One)))
	f.vault.Membership = nil
	for _, na := range f.nas {
		f.vault.Membership = append(f.vault.Membership, na.PubKeySet.Secp256k1.String())
	}
	c.Assert(mgr.Keeper().SetVault(ctx, f.vault), IsNil)
	pool := NewPool()
	pool.Asset = common.SOLAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	for _, na := range f.nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
}

// Once the vault's nonce was committed by an earlier tx, a later memoless transfer to
// the nonce address (e.g. a scheduled memoless payment there) is still not a creation:
// solNonceCommittedBy requires THIS tx to have set the committed nonce.
func (s *SolNonceCreateSuite) TestPaymentAfterEarlierCommitNotBookedAsGas(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	f.fundAndBond(c, ctx, mgr)
	mgr.Keeper().SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 900,
		UpdatedHeight: ctx.BlockHeight() - 10, SetByTx: GetRandomTxHash(), // the real creation, earlier
	})

	tx := f.createObs(c, f.nas[0], f.nas[1], f.nas[2])
	tx.SolNonce = nil
	c.Assert(isSolNonceCreateTx(tx), Equals, true) // same shape
	c.Assert(solNonceCommittedBy(ctx, mgr, tx), Equals, false)
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
	voter.Tx = tx
	voter.FinalisedHeight = ctx.BlockHeight()
	handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return nil, fmt.Errorf("not a scheduled outbound")
	}
	_ = handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, handler, tx, voter, nil, true)
	slashed := false
	for _, na := range f.nas {
		after, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		slashed = slashed || after.Bond.LT(na.Bond)
	}
	c.Assert(slashed, Equals, true) // normal outbound processing, not written off as gas
}

// Outbound consensus ignores SolNonce, so a creation can reach quorum before its nonce
// supermajority (here 2 matching nonce votes + 1 omitting it, of 4). It is deferred —
// no slash, nothing booked — and the late vote that commits the nonce books it as gas
// exactly once.
func (s *SolNonceCreateSuite) TestCreationDeferredUntilNonceCommits(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	f.fundAndBond(c, ctx, mgr)
	start := uint64(10 * common.One)
	balance := func() uint64 {
		v, err := mgr.Keeper().GetVault(ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		return v.Coins.GetCoin(common.SOLAsset).Amount.Uint64()
	}
	unslashed := func() {
		for _, na := range f.nas {
			after, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Assert(after.Bond.Equal(na.Bond), Equals, true)
		}
	}
	noop := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		c.Fatal("a nonce creation must not reach the outbound handler")
		return nil, nil
	}

	honest := f.createObs(c, f.nas[0], f.nas[1])
	omitting := honest
	omitting.SolNonce = nil
	omitting.Signers = []string{f.nas[2].NodeAddress.String()}
	voter := NewObservedTxVoter(honest.Tx.ID, []common.ObservedTx{honest, omitting})
	voter.Tx = honest
	voter.Height = ctx.BlockHeight()
	voter.FinalisedHeight = ctx.BlockHeight()

	// Quorum: nonce not committed (2 of 4 identical) -> deferred.
	c.Assert(handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, noop, honest, voter, nil, true), IsNil)
	_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
	c.Assert(balance(), Equals, start)
	unslashed()

	// Late 4th vote completes the nonce supermajority -> committed and booked once.
	late := honest
	late.Signers = []string{f.nas[3].NodeAddress.String()}
	voter.Txs = append(voter.Txs, late)
	c.Assert(handleObservedTxOutQuorum(ctx, mgr, f.nas[3].NodeAddress, f.nas, noop, late, voter, nil, false), IsNil)
	_, found, err = mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(balance(), Equals, start-solNonceRent-solNonceFee)
	unslashed()

	// Any further late vote books nothing more.
	c.Assert(handleObservedTxOutQuorum(ctx, mgr, f.nas[3].NodeAddress, f.nas, noop, late, voter, nil, false), IsNil)
	c.Assert(balance(), Equals, start-solNonceRent-solNonceFee)
}

// slashedAny reports whether any fixture member lost bond (unmatched-outflow slash).
func (f solNonceFixture) slashedAny(c *C, ctx cosmos.Context, mgr Manager) bool {
	for _, na := range f.nas {
		after, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		if after.Bond.LT(na.Bond) {
			return true
		}
	}
	return false
}

// Only a vote attesting the vault's OWN nonce account defers: a SolNonce naming any
// other account, or carried only by a non-final vote, leaves the creation-shaped
// transfer to normal outbound processing.
func (s *SolNonceCreateSuite) TestDeferRequiresFinalVoteForOwnNonceAccount(c *C) {
	for _, tc := range []struct {
		name   string
		mutate func(f solNonceFixture, attesting *common.ObservedTx)
	}{
		{"foreign nonce account", func(f solNonceFixture, a *common.ObservedTx) {
			a.SolNonce = &common.SolNonceUpdate{NonceAccount: solNonceValue(c), Nonce: a.SolNonce.Nonce, Slot: a.SolNonce.Slot}
		}},
		{"non-final vote", func(f solNonceFixture, a *common.ObservedTx) {
			a.FinaliseHeight = a.BlockHeight + 5
		}},
	} {
		ctx, mgr := setupManagerForTest(c)
		f := newSolNonceFixture(c, ctx, mgr)
		f.fundAndBond(c, ctx, mgr)

		consensus := f.createObs(c, f.nas[0], f.nas[1], f.nas[2])
		consensus.SolNonce = nil
		attesting := f.createObs(c, f.nas[3])
		attesting.Tx = consensus.Tx
		tc.mutate(f, &attesting)
		voter := NewObservedTxVoter(consensus.Tx.ID, []common.ObservedTx{consensus, attesting})
		voter.Tx = consensus
		voter.FinalisedHeight = ctx.BlockHeight()
		handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
			return nil, fmt.Errorf("not a scheduled outbound")
		}
		_ = handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, handler, consensus, voter, nil, true)
		c.Assert(f.slashedAny(c, ctx, mgr), Equals, true, Commentf(tc.name))
	}
}

// Defensive: processTxOutAttestation always sets Height when it finalises, so
// FinalisedHeight > 0 with Height == 0 is not reachable today. The late path's
// Height guard still must not book in that state, because the quorum branch books
// the creation and booking here too would charge the rent twice.
func (s *SolNonceCreateSuite) TestLatePathDoesNotBookBeforeQuorumProcessed(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	f.fundAndBond(c, ctx, mgr)
	start := uint64(10 * common.One)
	balance := func() uint64 {
		v, err := mgr.Keeper().GetVault(ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		return v.Coins.GetCoin(common.SOLAsset).Amount.Uint64()
	}
	noop := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		c.Fatal("a nonce creation must not reach the outbound handler")
		return nil, nil
	}

	tx := f.createObs(c, f.nas[0], f.nas[1], f.nas[2])
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
	voter.Tx = tx
	voter.FinalisedHeight = ctx.BlockHeight() // Height still 0: quorum branch not run

	c.Assert(handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, noop, tx, voter, nil, false), IsNil)
	_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(balance(), Equals, start) // committed, not booked yet

	voter.Height = ctx.BlockHeight()
	c.Assert(handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, noop, tx, voter, nil, true), IsNil)
	c.Assert(balance(), Equals, start-solNonceRent-solNonceFee) // booked exactly once
}

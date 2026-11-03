package thorchain

import (
	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const inboundCreditBaseline int64 = 50

type xmrInboundCreditFixture struct {
	ctx    cosmos.Context
	mgr    *Mgrs
	nodes  NodeAccounts
	keys   []secp256k1.PrivKey
	legacy common.ObservedTx
}

func newXMRInboundCreditFixture(c *C, final bool) *xmrInboundCreditFixture {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	f := &xmrInboundCreditFixture{
		ctx: ctx, mgr: mgr, nodes: make(NodeAccounts, 4), keys: make([]secp256k1.PrivKey, 4),
	}
	for i := range f.nodes {
		f.nodes[i], f.keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, f.nodes[i].NodeAddress, inboundCreditBaseline)
	}
	pk := GetRandomEd25519PubKey()
	vault := NewVault(1, ActiveVault, AsgardVault, pk, []string{common.XMRChain.String()}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	to, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	from, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	f.legacy = common.NewObservedTx(common.Tx{
		ID: GetRandomTxHash(), Chain: common.XMRChain, FromAddress: from, ToAddress: to,
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
		Memo:  "DONATE:XMR.XMR",
	}, 100, pk, 110)
	if final {
		f.legacy.BlockHeight = f.legacy.FinaliseHeight
	}
	return f
}

func (f *xmrInboundCreditFixture) load(c *C) ObservedTxVoter {
	voter, err := f.mgr.Keeper().GetObservedTxInVoter(f.ctx, f.legacy.Tx.ID)
	c.Assert(err, IsNil)
	return voter
}

// Bypass current admission only when seeding observations accepted before the
// mined-height field existed. Reload every write across the encoding boundary.
func (f *xmrInboundCreditFixture) seed(c *C, tx common.ObservedTx, signer int) ObservedTxVoter {
	voter := f.load(c)
	processTxInAttestation(f.ctx, f.mgr, voter, f.nodes, tx, f.nodes[signer].NodeAddress, false)
	return f.load(c)
}

func (f *xmrInboundCreditFixture) submit(c *C, tx common.ObservedTx, signer int, quorum bool) ObservedTxVoter {
	if quorum {
		msg := types.NewMsgObservedTxQuorum(&common.QuorumTx{
			ObsTx: tx, Inbound: true,
			Attestations: []*common.Attestation{attestObservedTx(c, f.keys[signer], tx, true)},
		}, f.nodes[signer].NodeAddress)
		_, err := NewObservedTxQuorumHandler(f.mgr).Run(f.ctx, msg)
		c.Assert(err, IsNil)
	} else {
		_, err := NewObservedTxInHandler(f.mgr).Run(f.ctx,
			NewMsgObservedTxIn(common.ObservedTxs{tx}, f.nodes[signer].NodeAddress))
		c.Assert(err, IsNil)
	}
	return f.load(c)
}

func (f *xmrInboundCreditFixture) points(c *C, signer int, want int64) {
	points, err := f.mgr.Keeper().GetNodeAccountSlashPoints(f.ctx, f.nodes[signer].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(points, Equals, want)
}

func (f *xmrInboundCreditFixture) consensus(c *C, tx common.ObservedTx) ObservedTxVoter {
	for i := 0; i < 3; i++ {
		f.seed(c, tx, i)
	}
	voter := f.load(c)
	c.Assert(voter.Height, Equals, f.ctx.BlockHeight())
	c.Assert(voter.FinalisedHeight > 0, Equals, tx.IsFinal())
	return voter
}

func (s *HandlerObservedTxQuorumSuite) TestXMRInboundLegacyLateCredit(c *C) {
	for _, quorum := range []bool{false, true} {
		for _, final := range []bool{false, true} {
			for _, differentGas := range []bool{false, true} {
				for _, boundary := range []string{"within", "last", "outside"} {
					c.Logf("quorum=%v final=%v differentGas=%v boundary=%s", quorum, final, differentGas, boundary)
					f := newXMRInboundCreditFixture(c, final)
					oldVoter := f.consensus(c, f.legacy)
					c.Assert(oldVoter.Tx.XmrMinedHeight, Equals, int64(0))
					_, heightErr := oldVoter.Tx.CanonicalMinedHeight()
					c.Assert(heightErr, NotNil)

					observe := f.mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
					lack := f.mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
					flex := f.mgr.Keeper().GetConfigInt64(f.ctx, constants.ObservationDelayFlexibility)
					f.points(c, 3, inboundCreditBaseline+lack)
					delay := int64(1)
					if boundary == "last" {
						delay = flex
					}
					if boundary == "outside" {
						delay = flex + 1
					}
					f.ctx = f.ctx.WithBlockHeight(1000 + delay)
					late := f.legacy
					late.XmrMinedHeight = 100
					if differentGas {
						late.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(2))}
					}
					voter := f.submit(c, late, 3, quorum)
					want := inboundCreditBaseline
					if boundary == "outside" {
						want += lack + observe
					}
					f.points(c, 3, want)
					c.Assert(voter.Tx.HasSigned(f.nodes[3].NodeAddress), Equals, boundary != "outside")
					c.Assert(voter.Tx.XmrMinedHeight, Equals, int64(0))
					c.Assert(voter.Height, Equals, oldVoter.Height)
					c.Assert(voter.FinalisedHeight, Equals, oldVoter.FinalisedHeight)
					c.Assert(voter.GetConsensusSigners(), HasLen, 3)

					// Repeated submissions cannot collect the normal late credit again.
					// Direct messages still pay for exact duplicates; quorum gossip does not.
					for i := 0; i < 2; i++ {
						f.submit(c, late, 3, quorum)
						if !quorum {
							want += observe
						}
						f.points(c, 3, want)
					}
					// One distinct redundant variant is free; later variants remain charged.
					late.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3))}
					f.submit(c, late, 3, quorum)
					f.points(c, 3, want)
					late.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(4))}
					f.submit(c, late, 3, quorum)
					f.points(c, 3, want+observe)

					// An original legacy consensus signer was already credited before upgrade.
					late.Tx.Gas = f.legacy.Tx.Gas
					f.submit(c, late, 0, quorum)
					f.points(c, 0, inboundCreditBaseline)
				}
			}
		}
	}
}

func (s *HandlerObservedTxQuorumSuite) TestXMRInboundPendingLegacyCredit(c *C) {
	for _, quorum := range []bool{false, true} {
		for _, final := range []bool{false, true} {
			for _, conflicting := range []bool{false, true} {
				c.Logf("quorum=%v final=%v conflicting=%v", quorum, final, conflicting)
				f := newXMRInboundCreditFixture(c, final)
				f.seed(c, f.legacy, 3)
				current := f.legacy
				current.XmrMinedHeight = 100
				current.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(2))}
				for i := 0; i < 3; i++ {
					voter := f.submit(c, current, i, quorum)
					if i < 2 {
						c.Assert(voter.Height, Equals, int64(0))
						c.Assert(voter.Tx.IsEmpty(), Equals, true)
					} else {
						c.Assert(voter.Height, Equals, int64(1000))
						c.Assert(voter.Tx.XmrMinedHeight, Equals, int64(100))
						c.Assert(voter.GetConsensusSigners(), HasLen, 3)
						c.Assert(voter.GetInboundObserveSigners(), HasLen, 4)
						c.Assert(voter.Tx.HasSigned(f.nodes[3].NodeAddress), Equals, true)
					}
				}
				for i := range f.nodes {
					f.points(c, i, inboundCreditBaseline)
				}
				// The legacy observer receives credit without lending a vote to quorum.
				// Its unknown height must not then bridge a conflicting concrete height.
				f.ctx = f.ctx.WithBlockHeight(1001)
				want := inboundCreditBaseline
				observe := f.mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
				if conflicting {
					current.XmrMinedHeight++
					if !final {
						current.BlockHeight++
					}
					want += observe
				}
				f.submit(c, current, 3, quorum)
				f.points(c, 3, want)
				f.submit(c, current, 3, quorum)
				if !quorum {
					want += observe
				}
				f.points(c, 3, want)
			}
		}
	}
}

func (s *HandlerObservedTxQuorumSuite) TestXMRInboundCreditPreservesConflictingHeightCharge(c *C) {
	for _, quorum := range []bool{false, true} {
		for _, final := range []bool{false, true} {
			for _, beforeConsensus := range []bool{false, true} {
				c.Logf("quorum=%v final=%v beforeConsensus=%v", quorum, final, beforeConsensus)
				f := newXMRInboundCreditFixture(c, final)
				current := f.legacy
				current.XmrMinedHeight = 100
				conflicting := current
				conflicting.XmrMinedHeight++
				if !final {
					conflicting.BlockHeight++
				}
				if beforeConsensus {
					f.submit(c, conflicting, 3, quorum)
				}
				for i := 0; i < 3; i++ {
					f.submit(c, current, i, quorum)
				}
				f.ctx = f.ctx.WithBlockHeight(1001)
				if !beforeConsensus {
					f.submit(c, conflicting, 3, quorum)
				}
				observe := f.mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
				lack := f.mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
				f.points(c, 3, inboundCreditBaseline+observe+lack)
				voter := f.submit(c, current, 3, quorum)
				// The matching arrival refunds its own point and the lack penalty,
				// without using the redundant refund to erase the conflicting charge.
				f.points(c, 3, inboundCreditBaseline+observe)
				c.Assert(voter.Tx.XmrMinedHeight, Equals, int64(100))
				c.Assert(voter.Tx.HasSigned(f.nodes[3].NodeAddress), Equals, true)
			}
		}
	}
}

func (s *HandlerObservedTxQuorumSuite) TestXMRInboundLegacyCreditSeparatesFinality(c *C) {
	for _, quorum := range []bool{false, true} {
		f := newXMRInboundCreditFixture(c, false)
		f.consensus(c, f.legacy)
		current := f.legacy
		current.XmrMinedHeight = 100
		f.ctx = f.ctx.WithBlockHeight(1001)
		f.submit(c, current, 3, quorum)
		f.points(c, 3, inboundCreditBaseline)
		current.BlockHeight = current.FinaliseHeight
		f.ctx = f.ctx.WithBlockHeight(1002)
		for i := 0; i < 3; i++ {
			voter := f.submit(c, current, i, quorum)
			c.Assert(voter.FinalisedHeight > 0, Equals, i == 2)
		}
		lack := f.mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
		f.points(c, 3, inboundCreditBaseline+lack)
		f.ctx = f.ctx.WithBlockHeight(1003)
		voter := f.submit(c, current, 3, quorum)
		c.Assert(voter.FinalisedHeight, Equals, int64(1002))
		c.Assert(voter.UnfinalizedHeight, Equals, int64(1000))
		for i := range f.nodes {
			f.points(c, i, inboundCreditBaseline)
		}
	}
}

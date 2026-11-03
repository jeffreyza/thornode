package thorchain

import (
	"strings"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func setupXMRObservationCredit(c *C, ctx cosmos.Context, mgr *Mgrs, final bool) (NodeAccounts, common.ObservedTx, common.ObservedTx) {
	nas := make(NodeAccounts, 4)
	for i := range nas {
		nas[i] = GetRandomValidatorNode(NodeActive)
		mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[i].NodeAddress, 50)
	}
	pk := GetRandomEd25519PubKey()
	from, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	to, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	vault := NewVault(1, ActiveVault, AsgardVault, pk, []string{common.XMRChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	txID := GetRandomTxHash()
	instant := common.NewObservedTx(common.Tx{
		ID: txID, Chain: common.XMRChain, FromAddress: from, ToAddress: to,
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10000))),
		Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(2000))},
		Memo:  "OUT:" + txID.String(),
	}, 90, pk, 90)
	instant.XmrTxKey = strings.Repeat("a", 64)
	mined := instant
	mined.BlockHeight, mined.FinaliseHeight, mined.XmrMinedHeight = 110, 110, 100
	if !final {
		instant.BlockHeight, instant.FinaliseHeight, mined.BlockHeight = 100, 110, 100
	}
	mined.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1000))}
	c.Assert(instant.Valid(), IsNil)
	c.Assert(mined.Valid(), IsNil)
	return nas, instant, mined
}

// Exercise the shared outbound path and reload after every arrival so credit
// tracking cannot depend on an in-memory voter retained between observations.
func submitXMRObservationCredit(c *C, ctx cosmos.Context, mgr *Mgrs, voter ObservedTxVoter, nas NodeAccounts, tx common.ObservedTx, signer cosmos.AccAddress, directDuplicate bool) (ObservedTxVoter, bool) {
	c.Assert(NewMsgObservedTxOut(common.ObservedTxs{tx}, signer).ValidateBasic(), IsNil)
	_, reached := processTxOutAttestation(ctx, mgr, voter, nas, tx, signer, directDuplicate)
	stored, err := mgr.Keeper().GetObservedTxOutVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	return stored, reached
}

func (s *HandlerObservedTxHelpersSuite) TestXMROutboundCreditKeepsConflictingMinedPoint(c *C) {
	for _, final := range []bool{false, true} {
		c.Logf("final=%v", final)
		for _, beforeConsensus := range []bool{true, false} {
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(100)
			nas, _, mined := setupXMRObservationCredit(c, ctx, mgr, final)
			member := nas[3].NodeAddress
			conflicting := mined
			conflicting.XmrMinedHeight++
			if !final {
				conflicting.BlockHeight = conflicting.XmrMinedHeight
			}
			voter := NewObservedTxVoter(mined.Tx.ID, nil)
			if beforeConsensus {
				voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, conflicting, member, false)
			}
			for i := 0; i < 3; i++ {
				voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, mined, nas[i].NodeAddress, false)
			}
			ctx = ctx.WithBlockHeight(101)
			if !beforeConsensus {
				voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, conflicting, member, false)
			}
			observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
			lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
			points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, 50+observe+lack)
			c.Assert(voter.GetOutboundObserveSigners(), HasLen, 3)
			c.Assert(voter.Tx.HasSigned(member), Equals, false)

			voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, mined, member, false)
			// The matching arrival earns its own late credit, but cannot refund
			// the original conflicting location through the redundant-refund path.
			points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, 50+observe, Commentf("conflicting observation before consensus=%v", beforeConsensus))
			c.Assert(voter.Tx.HasSigned(member), Equals, true)
		}
	}
}

func (s *HandlerObservedTxHelpersSuite) TestXMROutboundCreditAtConsensus(c *C) {
	for _, final := range []bool{false, true} {
		c.Logf("final=%v", final)
		for _, zeroHeightConsensus := range []bool{false, true} {
			for _, afterFlex := range []bool{false, true} {
				for _, differentGas := range []bool{false, true} {
					ctx, mgr := setupManagerForTest(c)
					ctx = ctx.WithBlockHeight(100)
					nas, instant, mined := setupXMRObservationCredit(c, ctx, mgr, final)
					if !differentGas {
						instant.Tx.Gas = mined.Tx.Gas
					}
					consensus, alternate := mined, instant
					if zeroHeightConsensus {
						consensus, alternate = instant, mined
					}
					member := nas[3].NodeAddress
					voter := NewObservedTxVoter(mined.Tx.ID, nil)
					voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, alternate, member, false)
					for i := 0; i < 3; i++ {
						var reached bool
						voter, reached = submitXMRObservationCredit(c, ctx, mgr, voter, nas, consensus, nas[i].NodeAddress, false)
						c.Assert(reached, Equals, i == 2) // The alternate height cannot supply quorum.
					}
					c.Assert(voter.GetConsensusSigners(), HasLen, 3)
					c.Assert(voter.GetObserveSigners(), HasLen, 3)
					c.Assert(voter.GetOutboundObserveSigners(), HasLen, 4)
					c.Assert(voter.Tx.XmrMinedHeight, Equals, consensus.XmrMinedHeight)
					c.Assert(voter.Tx.Tx.Gas.Equals(consensus.Tx.Gas), Equals, true)
					for _, na := range nas {
						points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
						c.Assert(err, IsNil)
						c.Assert(points, Equals, int64(50), Commentf("zero-height consensus=%v", zeroHeightConsensus))
					}

					// Reload credited signers and prove the later matching observation
					// refunds only its own charge, including outside the flex window.
					var reloadErr error
					voter, reloadErr = mgr.Keeper().GetObservedTxOutVoter(ctx, mined.Tx.ID)
					c.Assert(reloadErr, IsNil)
					c.Assert(voter.Tx.HasSigned(member), Equals, true)
					delay := int64(1)
					if afterFlex {
						delay += mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
					}
					ctx = ctx.WithBlockHeight(100 + delay)
					for i := 0; i < 2; i++ { // First re-observation and exact duplicate.
						voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, consensus, member, false)
						points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
						c.Assert(err, IsNil)
						c.Assert(points, Equals, int64(50))
					}
					// Only one extra variant is free, even after outbound credit.
					third := consensus
					third.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3000))}
					voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, third, member, false)
					observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
					points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
					c.Assert(err, IsNil)
					c.Assert(points, Equals, 50+observe)
					_, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, third, member, true)
					points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
					c.Assert(err, IsNil)
					c.Assert(points, Equals, 50+2*observe)
				}
			}
		}
	}
}

func (s *HandlerObservedTxHelpersSuite) TestXMROutboundLateCredit(c *C) {
	for _, final := range []bool{false, true} {
		c.Logf("final=%v", final)
		for _, zeroHeightConsensus := range []bool{false, true} {
			for _, afterFlex := range []bool{false, true} {
				ctx, mgr := setupManagerForTest(c)
				ctx = ctx.WithBlockHeight(100)
				nas, instant, mined := setupXMRObservationCredit(c, ctx, mgr, final)
				consensus, alternate := mined, instant
				if zeroHeightConsensus {
					consensus, alternate = instant, mined
				}
				member := nas[3].NodeAddress
				voter := NewObservedTxVoter(mined.Tx.ID, nil)
				for i := 0; i < 3; i++ {
					voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, consensus, nas[i].NodeAddress, false)
				}
				lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
				points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
				c.Assert(err, IsNil)
				c.Assert(points, Equals, 50+lack)
				delay := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
				want := int64(50)
				if afterFlex {
					delay++
					want += lack + mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
				}
				ctx = ctx.WithBlockHeight(100 + delay)
				voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, alternate, member, false)
				points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
				c.Assert(err, IsNil)
				c.Assert(points, Equals, want)
				c.Assert(voter.Tx.HasSigned(member), Equals, !afterFlex)
				c.Assert(voter.Tx.XmrMinedHeight, Equals, consensus.XmrMinedHeight)
				c.Assert(voter.Tx.Tx.Gas.Equals(consensus.Tx.Gas), Equals, true)

				// A further scanner observation cannot refund already credited points.
				voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, consensus, member, false)
				points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
				c.Assert(err, IsNil)
				c.Assert(points, Equals, want)
			}
		}
	}
}

func (s *HandlerObservedTxHelpersSuite) TestXMROutboundCreditKeepsGasAndMinedLocationsStrict(c *C) {
	for _, final := range []bool{false, true} {
		c.Logf("final=%v", final)
		for _, heights := range [][2]int64{{100, 0}, {0, 100}, {100, 101}, {100, 100}} {
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(100)
			nas, instant, mined := setupXMRObservationCredit(c, ctx, mgr, final)
			original := mined
			if heights[0] == 0 {
				original = instant
			}
			other := original
			other.BlockHeight, other.FinaliseHeight, other.XmrMinedHeight = 110, 110, heights[1]
			if !final {
				other.BlockHeight = 100
				if heights[1] > 0 {
					other.BlockHeight = heights[1]
				}
			}
			other.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(3000))}
			voter := NewObservedTxVoter(original.Tx.ID, nil)
			for _, na := range nas {
				voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, original, na.NodeAddress, false)
			}
			ctx = ctx.WithBlockHeight(101)
			for _, na := range nas {
				voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, other, na.NodeAddress, false)
			}
			vault, err := mgr.Keeper().GetVault(ctx, original.ObservedPubKey)
			c.Assert(err, IsNil)
			wantFunds := cosmos.NewUint(100 * common.One)
			if heights[0] == heights[1] {
				// Same-location consensus still corrects gas and debits the vault.
				c.Assert(voter.Tx.Tx.Gas.Equals(other.Tx.Gas), Equals, true)
				wantFunds = wantFunds.Sub(cosmos.NewUint(2000))
			} else {
				// Even unanimous observations at another height cannot change gas.
				c.Assert(voter.Tx.Tx.Gas.Equals(original.Tx.Gas), Equals, true)
			}
			c.Assert(vault.GetCoin(common.XMRAsset).Amount.Equal(wantFunds), Equals, true)
			c.Assert(voter.Tx.XmrMinedHeight, Equals, heights[0])
		}
	}
}

func (s *HandlerObservedTxHelpersSuite) TestXMROutboundLegacyCreditDoesNotBridgeConcreteHeights(c *C) {
	for _, final := range []bool{false, true} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		nas, legacy, mined := setupXMRObservationCredit(c, ctx, mgr, final)
		member := nas[3].NodeAddress
		voter := NewObservedTxVoter(mined.Tx.ID, nil)
		voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, legacy, member, false)
		for i := 0; i < 3; i++ {
			voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, mined, nas[i].NodeAddress, false)
		}
		points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
		c.Assert(err, IsNil)
		c.Assert(points, Equals, int64(50))
		c.Assert(voter.Tx.HasSigned(member), Equals, true)

		conflicting := mined
		conflicting.XmrMinedHeight++
		if !final {
			conflicting.BlockHeight = conflicting.XmrMinedHeight
		}
		ctx = ctx.WithBlockHeight(101)
		voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, conflicting, member, false)
		// The earlier zero-height observation cannot make both concrete
		// locations eligible for credit or a redundant-observation refund.
		points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
		c.Assert(err, IsNil)
		c.Assert(points, Equals, 50+mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints))
		c.Assert(voter.Tx.XmrMinedHeight, Equals, mined.XmrMinedHeight)
		c.Assert(voter.GetConsensusSigners(), HasLen, 3)
	}
}

func (s *HandlerObservedTxQuorumSuite) TestXMROutboundLegacyLateCreditHandlers(c *C) {
	for _, quorum := range []bool{false, true} {
		for _, final := range []bool{false, true} {
			for _, boundary := range []string{"within", "last", "outside"} {
				c.Logf("quorum=%v final=%v boundary=%s", quorum, final, boundary)
				ctx, mgr := setupManagerForTest(c)
				ctx = ctx.WithBlockHeight(1000)
				nas, legacy, mined := setupXMRObservationCredit(c, ctx, mgr, final)
				if final {
					legacy.BlockHeight, legacy.FinaliseHeight = mined.BlockHeight, mined.FinaliseHeight
				}
				keys := make([]secp256k1.PrivKey, len(nas))
				for i := range nas {
					nas[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
					mgr.Keeper().SetNodeAccountSlashPoints(ctx, nas[i].NodeAddress, 50)
				}
				voter := NewObservedTxVoter(legacy.Tx.ID, nil)
				for i := 0; i < 3; i++ {
					voter, _ = submitXMRObservationCredit(c, ctx, mgr, voter, nas, legacy, nas[i].NodeAddress, false)
				}
				c.Assert(voter.Height, Equals, int64(1000))
				c.Assert(voter.FinalisedHeight > 0, Equals, final)
				member := nas[3].NodeAddress
				observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
				lack := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
				flex := mgr.Keeper().GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
				points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
				c.Assert(err, IsNil)
				c.Assert(points, Equals, 50+lack)
				delay := int64(1)
				if boundary == "last" {
					delay = flex
				}
				want := int64(50)
				if boundary == "outside" {
					delay = flex + 1
					want += observe + lack
				}
				ctx = ctx.WithBlockHeight(1000 + delay)
				for attempt := 0; attempt < 3; attempt++ {
					if quorum {
						msg := types.NewMsgObservedTxQuorum(&common.QuorumTx{
							ObsTx: mined, Inbound: false,
							Attestations: []*common.Attestation{attestObservedTx(c, keys[3], mined, false)},
						}, member)
						_, err = NewObservedTxQuorumHandler(mgr).Run(ctx, msg)
					} else {
						_, err = NewObservedTxOutHandler(mgr).Run(ctx, NewMsgObservedTxOut(common.ObservedTxs{mined}, member))
					}
					c.Assert(err, IsNil)
					if attempt > 0 && !quorum {
						want += observe
					}
					points, err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, member)
					c.Assert(err, IsNil)
					c.Assert(points, Equals, want)
					voter, err = mgr.Keeper().GetObservedTxOutVoter(ctx, mined.Tx.ID)
					c.Assert(err, IsNil)
					c.Assert(voter.Tx.HasSigned(member), Equals, boundary != "outside")
					c.Assert(voter.Tx.XmrMinedHeight, Equals, int64(0))
					c.Assert(voter.GetConsensusSigners(), HasLen, 3)
				}
			}
		}
	}
}

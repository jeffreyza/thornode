package thorchain

import (
	"fmt"
	"strings"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Exercise Run, including validation, for both real inbound entrypoints. Seed
// persisted partial zero-height evidence to prove it cannot lend valid votes.
func (s *HandlerObservedTxQuorumSuite) TestS10LegacyInboundMinedHeightRecovery(c *C) {
	testS10InboundMinedHeightRecovery(c, false)
}

func (s *HandlerObservedTxQuorumSuite) TestS10QuorumInboundMinedHeightRecovery(c *C) {
	testS10InboundMinedHeightRecovery(c, true)
}

func testS10InboundMinedHeightRecovery(c *C, quorum bool) {
	for _, final := range []bool{false, true} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		nas := make(NodeAccounts, 4)
		keys := make([]secp256k1.PrivKey, len(nas))
		for i := range nas {
			nas[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		pk := GetRandomEd25519PubKey()
		vault := NewVault(1, ActiveVault, AsgardVault, pk, []string{common.XMRChain.String()}, nil)
		vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1000))))
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		to, err := pk.GetAddress(common.XMRChain)
		c.Assert(err, IsNil)
		from, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
		c.Assert(err, IsNil)
		zero := common.NewObservedTx(common.Tx{
			ID: GetRandomTxHash(), Chain: common.XMRChain, FromAddress: from, ToAddress: to,
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
			Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())}, Memo: "DONATE:XMR.XMR",
		}, 100, pk, 110)
		if final {
			zero.BlockHeight = zero.FinaliseHeight
		}
		valid := zero
		valid.XmrMinedHeight = 100
		voter := NewObservedTxVoter(zero.Tx.ID, nil)
		for i := 0; i < 2; i++ {
			var crossed bool
			voter, crossed = processTxInAttestation(ctx, mgr, voter, nas, zero, nas[i].NodeAddress, false)
			c.Assert(crossed, Equals, false)
		}
		before, err := voter.Marshal()
		c.Assert(err, IsNil)
		points := make([]int64, len(nas))
		for i, na := range nas {
			points[i], err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
		}
		// Keep the existing TxID-wide outcome: height identity does not
		// authorize a fresh economic generation after a committed errata.
		errata := NewErrataTxVoter(zero.Tx.ID, common.XMRChain)
		errata.BlockHeight = ctx.BlockHeight()
		mgr.Keeper().SetErrataTxVoter(ctx, errata)
		run := func(at cosmos.Context, tx common.ObservedTx, signer int) error {
			if quorum {
				msg := types.NewMsgObservedTxQuorum(&common.QuorumTx{
					ObsTx: tx, Inbound: true,
					Attestations: []*common.Attestation{attestObservedTx(c, keys[signer], tx, true)},
				}, nas[signer].NodeAddress)
				_, runErr := NewObservedTxQuorumHandler(mgr).Run(at, msg)
				return runErr
			}
			_, runErr := NewObservedTxInHandler(mgr).Run(at,
				NewMsgObservedTxIn(common.ObservedTxs{tx}, nas[signer].NodeAddress))
			return runErr
		}
		for _, height := range []int64{0, -1, 111} {
			invalid := zero
			invalid.XmrMinedHeight = height
			attempt, commit := ctx.CacheContext()
			c.Assert(run(attempt, invalid, 2), NotNil, Commentf("quorum=%t final=%t height=%d", quorum, final, height))
			// Commit even the rejected attempt: validation must have written
			// nothing, independently of an outer transaction rollback.
			commit()
			stored, getErr := mgr.Keeper().GetObservedTxInVoter(ctx, zero.Tx.ID)
			c.Assert(getErr, IsNil)
			after, marshalErr := stored.Marshal()
			c.Assert(marshalErr, IsNil)
			c.Assert(after, DeepEquals, before)
			for i, na := range nas {
				got, pointsErr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
				c.Assert(pointsErr, IsNil)
				c.Assert(got, Equals, points[i])
			}
		}
		for i := 0; i < 3; i++ {
			// Start with different signers so borrowing the two stale
			// votes would incorrectly cross quorum on the first arrival.
			c.Assert(run(ctx, valid, (i+2)%len(nas)), IsNil)
			stored, getErr := mgr.Keeper().GetObservedTxInVoter(ctx, zero.Tx.ID)
			c.Assert(getErr, IsNil)
			if i < 2 {
				c.Assert(stored.Tx.IsEmpty(), Equals, true)
				c.Assert(stored.Height, Equals, int64(0))
				c.Assert(stored.FinalisedHeight, Equals, int64(0))
				c.Assert(stored.Reverted, Equals, false)
			} else {
				c.Assert(stored.Tx.XmrMinedHeight, Equals, int64(100))
				c.Assert(stored.Reverted, Equals, true)
				c.Assert(stored.Height, Equals, int64(100))
				c.Assert(stored.FinalisedHeight > 0, Equals, final)
			}
		}
		gotVault, err := mgr.Keeper().GetVault(ctx, pk)
		c.Assert(err, IsNil)
		c.Assert(gotVault.Coins, DeepEquals, vault.Coins)
	}
}

func (s *HandlerObservedTxQuorumSuite) TestS10PreMiningOutboundAdmissionPreserved(c *C) {
	pk := GetRandomEd25519PubKey()
	from, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	to, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID: GetRandomTxHash(), Chain: common.XMRChain, FromAddress: from, ToAddress: to,
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())},
		Memo:  fmt.Sprintf("OUT:%s", GetRandomTxHash()),
	}, 100, pk, 100)
	tx.XmrTxKey = strings.Repeat("a", 64)
	c.Assert(tx.XmrMinedHeight, Equals, int64(0))
	c.Assert(types.NewMsgObservedTxOut(common.ObservedTxs{tx}, GetRandomBech32Addr()).ValidateBasic(), IsNil)
	c.Assert(types.NewMsgObservedTxQuorum(&common.QuorumTx{
		ObsTx: tx, AllowFutureObservation: true,
	}, GetRandomBech32Addr()).ValidateBasic(), IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestS10QueryMinedHeight(c *C) {
	observed := common.NewObservedTx(GetRandomTx(), 110, GetRandomEd25519PubKey(), 110)
	observed.Tx.Chain = common.XMRChain
	observed.XmrMinedHeight = 100
	query := castObservedTx(observed)
	c.Assert(query.XmrMinedHeight, Equals, int64(100))
	c.Assert(query.BlockHeight, Equals, int64(110))
	encoded, err := query.Marshal()
	c.Assert(err, IsNil)
	var decoded types.QueryObservedTx
	c.Assert(decoded.Unmarshal(encoded), IsNil)
	c.Assert(decoded.XmrMinedHeight, Equals, int64(100))
}

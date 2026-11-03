package thorchain

import (
	"errors"
	"strings"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newSignedErrataQuorumMessage(c *C, er *common.ErrataTx, nodes NodeAccounts, keys ...secp256k1.PrivKey) *types.MsgErrataTxQuorum {
	q := &common.QuorumErrataTx{ErrataTx: er}
	for _, key := range keys {
		q.Attestations = append(q.Attestations, attestErrataTx(c, key, er))
	}
	return types.NewMsgErrataTxQuorum(q, nodes[0].NodeAddress)
}

// Model the SDK transaction cache: a handler error discards every economic,
// voter and slash write. Successful calls reload voters through the keeper.
func runErrataQuorum(ctx cosmos.Context, mgr Manager, msg *types.MsgErrataTxQuorum) error {
	attempt, commit := ctx.CacheContext()
	_, err := NewErrataTxQuorumHandler(mgr).Run(attempt, msg)
	if err == nil {
		commit()
	}
	return err
}

func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataUnknownAndChurnSlashOnce(c *C) {
	for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		nodes := make(NodeAccounts, 4)
		keys := make([]secp256k1.PrivKey, len(nodes))
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		er := makeErrataTx(GetRandomTxHash(), chain)
		msg := newSignedErrataQuorumMessage(c, er, nodes, keys[:2]...)
		// A submitter cannot manufacture a terminal response.
		msg.QuoErrata.Outcome = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED)
		observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
		for i := 0; i < 2; i++ {
			points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[i].NodeAddress)
			c.Assert(err, IsNil)
			c.Assert(points, Equals, observe)
		}
		// Duplicate-only retries with unchanged membership do not add charges.
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		// Two of three now suffices. The remaining non-signer is penalized once.
		nodes[3].UpdateStatus(NodeStandby, ctx.BlockHeight())
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodes[3]), IsNil)
		ctx = ctx.WithBlockHeight(101)
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		voter, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, chain)
		c.Assert(err, IsNil)
		expected := common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED
		if chain.Equals(common.XMRChain) {
			expected = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION
			c.Assert(voter.BlockHeight, Equals, int64(101))
		} else {
			c.Assert(voter.BlockHeight, Equals, int64(0))
		}
		c.Assert(msg.QuoErrata.Outcome, Equals, expected)
		c.Assert(voter.Outcome, Equals, expected)
		pointsBefore := make([]int64, len(nodes))
		for i := range nodes {
			pointsBefore[i], err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[i].NodeAddress)
			c.Assert(err, IsNil)
			if chain.Equals(common.XMRChain) {
				want := int64(0)
				if i == 2 {
					want = mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
				}
				c.Assert(pointsBefore[i], Equals, want)
			} else if i < 2 {
				c.Assert(pointsBefore[i], Equals, observe)
			}
		}
		// Terminal replay must return the same outcome and preserve all charges.
		c.Assert(runErrataQuorum(ctx.WithBlockHeight(102), mgr, msg), IsNil)
		for i := range nodes {
			got, pointsErr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[i].NodeAddress)
			c.Assert(pointsErr, IsNil)
			c.Assert(got, Equals, pointsBefore[i])
		}
		// Direct duplicate messages preserve the historical extra-message penalty.
		_, err = NewErrataTxHandler(mgr).Run(ctx, NewMsgErrataTx(er.Id, chain, nodes[0].NodeAddress))
		c.Assert(err, IsNil)
		got, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[0].NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(got, Equals, pointsBefore[0]+observe)
	}
}

func newErrataOutboundFixture(c *C, ctx cosmos.Context, mgr Manager, memo string) (ObservedTxVoter, Vault) {
	pk := GetRandomEd25519PubKey()
	from, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	to, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID: GetRandomTxHash(), Chain: common.XMRChain, FromAddress: from, ToAddress: to,
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())}, Memo: memo,
	}, 110, pk, 110)
	tx.XmrMinedHeight = 100
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
	voter.Tx = tx
	voter.FinalisedHeight = 90
	vault := NewVault(1, InactiveVault, AsgardVault, pk, []string{common.XMRChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1000))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	// Model an outbound whose observation already debited principal.
	voter.UpdatedVault = true
	mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	return voter, vault
}

func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataOutboundMemoAndCanonicalRetry(c *C) {
	for _, minedHeight := range []int64{0, 100} {
		for _, memo := range []string{"", "not-a-memo", "SWAP:XMR.XMR", "MIGRATE:1", "OUT:" + GetRandomTxHash().String()} {
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(100)
			na, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
			voter, vault := newErrataOutboundFixture(c, ctx, mgr, memo)
			er := makeErrataTx(voter.TxID, common.XMRChain)
			msg := newSignedErrataQuorumMessage(c, er, NodeAccounts{na}, key)
			valid := voter.Tx
			valid.XmrMinedHeight = minedHeight
			// An empty voter and malformed canonical location are errors,
			// not no-applicable results. Even the quorum/slash writes roll back.
			negativeHeight, invalidHeight, missingVault := valid, valid, valid
			negativeHeight.XmrMinedHeight = -1
			invalidHeight.XmrMinedHeight = 111
			missingVault.ObservedPubKey = common.EmptyPubKey
			zeroObservation, negativeObservation := valid, valid
			zeroObservation.BlockHeight, zeroObservation.FinaliseHeight = 0, 0
			negativeObservation.BlockHeight, negativeObservation.FinaliseHeight = -1, -1
			for _, invalid := range []common.ObservedTx{{}, negativeHeight, invalidHeight, missingVault, zeroObservation, negativeObservation} {
				voter.Tx = invalid
				mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
				c.Assert(runErrataQuorum(ctx, mgr, msg), NotNil)
				c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED)
				stored, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
				c.Assert(err, IsNil)
				c.Assert(stored.BlockHeight, Equals, int64(0))
				c.Assert(stored.Signers, HasLen, 0)
				got, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
				c.Assert(err, IsNil)
				c.Assert(got.Coins, DeepEquals, vault.Coins)
			}
			voter.Tx = valid
			mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
			c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
			c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			got, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
			c.Assert(err, IsNil)
			c.Assert(got.GetCoin(common.XMRAsset).Amount.Equal(cosmos.NewUint(1100)), Equals, true)
			c.Assert(got.Status, Equals, InactiveVault)
			out, err := mgr.Keeper().GetObservedTxOutVoter(ctx, er.Id)
			c.Assert(err, IsNil)
			c.Assert(out.Reverted, Equals, true)
			c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
			again, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
			c.Assert(err, IsNil)
			c.Assert(again.Coins, DeepEquals, got.Coins)
			// Re-entry below the attestation gate also preserves monotonic accounting.
			result, err := processErrataOutboundTx(ctx, mgr.Keeper(), mgr.EventMgr(), er)
			c.Assert(err, IsNil)
			c.Assert(result, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			again, err = mgr.Keeper().GetVault(ctx, vault.PubKey)
			c.Assert(err, IsNil)
			c.Assert(again.Coins, DeepEquals, got.Coins)
		}
	}
}

type errataFailVaultKeeper struct{ keeper.Keeper }

func (k errataFailVaultKeeper) SetVault(ctx cosmos.Context, vault Vault) error {
	// Deliberately write then fail to prove transaction rollback, not only an
	// error before the first durable mutation.
	if err := k.Keeper.SetVault(ctx, vault); err != nil {
		return err
	}
	return errors.New("injected vault persistence failure")
}

type errataFailAfterEventMgr struct{ EventManager }

func (m errataFailAfterEventMgr) EmitEvent(ctx cosmos.Context, event EmitEventItem) error {
	if err := m.EventManager.EmitEvent(ctx, event); err != nil {
		return err
	}
	return errors.New("injected failure after event emission")
}

func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataEconomicFailureRollsBackAndRetries(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nodes := make(NodeAccounts, 3)
	keys := make([]secp256k1.PrivKey, len(nodes))
	for i := range nodes {
		nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
	}
	voter, vault := newErrataOutboundFixture(c, ctx, mgr, "unused")
	er := makeErrataTx(voter.TxID, common.XMRChain)
	// Persist a partial vote first. A failed quorum attempt must preserve its
	// observe charge, discard the new signer and undo the non-signer penalty.
	c.Assert(runErrataQuorum(ctx, mgr, newSignedErrataQuorumMessage(c, er, nodes, keys[0])), IsNil)
	before, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
	c.Assert(err, IsNil)
	c.Assert(before.Signers, HasLen, 1)
	beforeOutbound, err := mgr.Keeper().GetObservedTxOutVoter(ctx, er.Id)
	c.Assert(err, IsNil)
	beforeEvents := append(cosmos.Events{}, ctx.EventManager().Events()...)
	observe := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	assertUnchanged := func() {
		got, vaultErr := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(vaultErr, IsNil)
		c.Assert(got.Coins, DeepEquals, vault.Coins)
		out, voterErr := mgr.Keeper().GetObservedTxOutVoter(ctx, er.Id)
		c.Assert(voterErr, IsNil)
		c.Assert(out, DeepEquals, beforeOutbound)
		stored, errataErr := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
		c.Assert(errataErr, IsNil)
		c.Assert(stored, DeepEquals, before)
		for i, node := range nodes {
			points, pointsErr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
			c.Assert(pointsErr, IsNil)
			want := int64(0)
			if i == 0 {
				want = observe
			}
			c.Assert(points, Equals, want)
		}
		c.Assert(ctx.EventManager().Events(), DeepEquals, beforeEvents)
	}
	msg := newSignedErrataQuorumMessage(c, er, nodes, keys[1])
	originalKeeper, originalEvents := mgr.K, mgr.eventMgr
	mgr.K = errataFailVaultKeeper{originalKeeper}
	c.Assert(runErrataQuorum(ctx, mgr, msg), NotNil)
	mgr.K = originalKeeper
	c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED)
	assertUnchanged()

	mgr.eventMgr = errataFailAfterEventMgr{originalEvents}
	c.Assert(runErrataQuorum(ctx, mgr, msg), NotNil)
	mgr.eventMgr = originalEvents
	c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED)
	assertUnchanged()

	ctx = ctx.WithBlockHeight(101)
	for attempt := range 3 {
		c.Assert(runErrataQuorum(ctx.WithBlockHeight(101+int64(attempt)), mgr, msg), IsNil)
		c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
		got, vaultErr := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(vaultErr, IsNil)
		c.Assert(got.GetCoin(common.XMRAsset).Amount.Uint64(), Equals, uint64(1100))
		out, voterErr := mgr.Keeper().GetObservedTxOutVoter(ctx, er.Id)
		c.Assert(voterErr, IsNil)
		c.Assert(out.Reverted, Equals, true)
		stored, errataErr := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
		c.Assert(errataErr, IsNil)
		c.Assert(stored.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
		c.Assert(stored.BlockHeight, Equals, int64(101))
		c.Assert(stored.Signers, HasLen, 2)
		for i, node := range nodes {
			points, pointsErr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
			c.Assert(pointsErr, IsNil)
			want := int64(0)
			if i == 2 {
				want = mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
			}
			c.Assert(points, Equals, want)
		}
	}
	securityEvents := 0
	for _, event := range ctx.EventManager().Events() {
		if event.Type == "security" {
			securityEvents++
		}
	}
	c.Assert(securityEvents, Equals, 1)
}

// A settled errata without inbound quorum stays pending; the later inbound
// backstop prevents initial accounting and a duplicate errata resolves it.
func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataPendingInboundAndEarlyTerminalOutcome(c *C) {
	for _, unknown := range []bool{false, true} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		nodes := make(NodeAccounts, 3)
		keys := make([]secp256k1.PrivKey, len(nodes))
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		out, vault := newErrataOutboundFixture(c, ctx, mgr, "DONATE:XMR.XMR")
		// Use an independent inbound TxID so it is unknown on both sides.
		tx := out.Tx
		tx.Tx.ID = GetRandomTxHash()
		tx.Tx.FromAddress, tx.Tx.ToAddress = tx.Tx.ToAddress, tx.Tx.FromAddress
		voter := NewObservedTxVoter(tx.Tx.ID, nil)
		if !unknown {
			voter.Add(tx, nodes[0].NodeAddress)
			mgr.Keeper().SetObservedTxInVoter(ctx, voter)
		}
		er := makeErrataTx(tx.Tx.ID, common.XMRChain)
		msg := newSignedErrataQuorumMessage(c, er, nodes, keys[:2]...)
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		want := common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED
		if unknown {
			want = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION
		}
		c.Assert(msg.QuoErrata.Outcome, Equals, want)
		saved, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
		c.Assert(err, IsNil)
		c.Assert(saved.BlockHeight, Equals, int64(100))
		// Retry the settled pending voter before any further observation.
		c.Assert(runErrataQuorum(ctx.WithBlockHeight(101), mgr, msg), IsNil)
		// Real inbound quorum entrypoint must apply the prior errata before vault credit.
		incoming := types.NewMsgObservedTxQuorum(&common.QuorumTx{
			ObsTx: tx, Inbound: true,
			Attestations: []*common.Attestation{attestObservedTx(c, keys[0], tx, true), attestObservedTx(c, keys[1], tx, true)},
		}, nodes[0].NodeAddress)
		_, err = NewObservedTxQuorumHandler(mgr).Run(ctx.WithBlockHeight(102), incoming)
		c.Assert(err, IsNil)
		inbound, err := mgr.Keeper().GetObservedTxInVoter(ctx, er.Id)
		c.Assert(err, IsNil)
		c.Assert(inbound.Reverted, Equals, true)
		got, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(got.Coins, DeepEquals, vault.Coins)
		c.Assert(runErrataQuorum(ctx.WithBlockHeight(103), mgr, msg), IsNil)
		if !unknown {
			want = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED
		}
		c.Assert(msg.QuoErrata.Outcome, Equals, want)
		saved, err = mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
		c.Assert(err, IsNil)
		c.Assert(saved.BlockHeight, Equals, int64(100))
	}
}

// A known outbound still needs its vault credit if the inbound backstop wins
// between errata retries. An inbound Reverted flag alone cannot settle both sides.
func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataRevertedInboundStillCreditsKnownOutbound(c *C) {
	for _, phase := range []string{"pre-mining", "unfinalized", "finalized"} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		na, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
		outbound, vault := newErrataOutboundFixture(c, ctx, mgr, "MIGRATE:1")
		canonical := outbound.Tx
		switch phase {
		case "pre-mining":
			outbound.Tx.XmrMinedHeight = 0
		case "unfinalized":
			outbound.Tx.BlockHeight = outbound.Tx.XmrMinedHeight
			outbound.Height, outbound.FinalisedHeight = outbound.FinalisedHeight, 0
		}
		outbound.Txs = ObservedTxs{outbound.Tx}
		mgr.Keeper().SetObservedTxOutVoter(ctx, outbound)
		inbound := NewObservedTxVoter(outbound.TxID, outbound.Txs)
		mgr.Keeper().SetObservedTxInVoter(ctx, inbound)
		er := makeErrataTx(outbound.TxID, common.XMRChain)
		msg := newSignedErrataQuorumMessage(c, er, NodeAccounts{na}, key)
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED)
		inbound.Tx = canonical
		c.Assert(applyCommittedXMRErrataToInboundConsensus(ctx, mgr.Keeper(), &inbound), IsNil)
		c.Assert(inbound.Reverted, Equals, true)
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
		got, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(got.GetCoin(common.XMRAsset).Amount.Equal(cosmos.NewUint(1100)), Equals, true)
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		again, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(again.Coins, DeepEquals, got.Coins)
	}
}

// Observation accounting stays settled across churn, even while the economic
// outcome is pending. Non-XMR keeps the historical late observe charge.
func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataPostSettlementSignerWithoutCurrentQuorum(c *C) {
	for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		nodes := make(NodeAccounts, 3)
		keys := make([]secp256k1.PrivKey, len(nodes))
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		// A known transaction with no selected observation keeps errata pending.
		tx := GetRandomObservedTx()
		tx.Tx.Chain = chain
		mgr.Keeper().SetObservedTxInVoter(ctx, NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx}))
		er := makeErrataTx(tx.Tx.ID, chain)
		c.Assert(runErrataQuorum(ctx, mgr, newSignedErrataQuorumMessage(c, er, nodes, keys[:2]...)), IsNil)
		before, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, chain)
		c.Assert(err, IsNil)
		c.Assert(before.BlockHeight, Equals, int64(100))
		c.Assert(before.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED)
		pointsBefore := make([]int64, len(nodes))
		for i, node := range nodes {
			pointsBefore[i], err = mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
			c.Assert(err, IsNil)
		}
		var lateKey secp256k1.PrivKey
		var late NodeAccount
		for i := 0; i < 3; i++ {
			node, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
			if i == 0 {
				late, lateKey = node, key
			}
		}
		ctx = ctx.WithBlockHeight(101)
		msg := newSignedErrataQuorumMessage(c, er, nodes, lateKey)
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		after, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, chain)
		c.Assert(err, IsNil)
		active, err := mgr.Keeper().ListActiveValidators(ctx)
		c.Assert(err, IsNil)
		c.Assert(after.HasConsensus(active), Equals, false)
		c.Assert(after.Signers, HasLen, 3)
		c.Assert(after.BlockHeight, Equals, before.BlockHeight)
		c.Assert(after.Outcome, Equals, before.Outcome)
		for i, node := range nodes {
			got, pointsErr := mgr.Keeper().GetNodeAccountSlashPoints(ctx, node.NodeAddress)
			c.Assert(pointsErr, IsNil)
			c.Assert(got, Equals, pointsBefore[i])
		}
		want := int64(0)
		if !chain.Equals(common.XMRChain) {
			want = mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
		}
		got, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, late.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(got, Equals, want)
	}
}

func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataMigrationCreditsOutbound(c *C) {
	for _, phase := range []string{"pre-mining", "unfinalized", "finalized"} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(100)
		na, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
		outbound, source := newErrataOutboundFixture(c, ctx, mgr, "MIGRATE:1")
		destination := NewVault(1, ActiveVault, AsgardVault, GetRandomEd25519PubKey(), []string{common.XMRChain.String()}, nil)
		destination.AddFunds(outbound.Tx.Tx.Coins)
		c.Assert(mgr.Keeper().SetVault(ctx, destination), IsNil)
		to, err := destination.PubKey.GetAddress(common.XMRChain)
		c.Assert(err, IsNil)
		outbound.Tx.Tx.ToAddress = to
		incoming := outbound.Tx
		incoming.ObservedPubKey = destination.PubKey
		inbound := NewObservedTxVoter(outbound.TxID, ObservedTxs{incoming})
		inbound.Tx = incoming
		inbound.UpdatedVault = true
		inbound.FinalisedHeight = 90
		mgr.Keeper().SetObservedTxInVoter(ctx, inbound)
		switch phase {
		case "pre-mining":
			outbound.Tx.XmrMinedHeight = 0
		case "unfinalized":
			outbound.Tx.BlockHeight = outbound.Tx.XmrMinedHeight
			outbound.Height, outbound.FinalisedHeight = outbound.FinalisedHeight, 0
		}
		outbound.Txs = ObservedTxs{outbound.Tx}
		mgr.Keeper().SetObservedTxOutVoter(ctx, outbound)
		msg := newSignedErrataQuorumMessage(c, makeErrataTx(outbound.TxID, common.XMRChain), NodeAccounts{na}, key)
		for range 2 {
			c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
			c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			got, vaultErr := mgr.Keeper().GetVault(ctx, source.PubKey)
			c.Assert(vaultErr, IsNil)
			c.Assert(got.GetCoin(common.XMRAsset).Amount.Uint64(), Equals, uint64(1100))
			got, vaultErr = mgr.Keeper().GetVault(ctx, destination.PubKey)
			c.Assert(vaultErr, IsNil)
			c.Assert(got.GetCoin(common.XMRAsset).Amount.IsZero(), Equals, true)
			c.Assert(got.Deficit.IsEmpty(), Equals, true)
		}
		inbound, err = mgr.Keeper().GetObservedTxInVoter(ctx, outbound.TxID)
		c.Assert(err, IsNil)
		c.Assert(inbound.Reverted, Equals, true)
		outbound, err = mgr.Keeper().GetObservedTxOutVoter(ctx, outbound.TxID)
		c.Assert(err, IsNil)
		c.Assert(outbound.Reverted, Equals, true)
	}
}

func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataUnfinalizedInboundOutcome(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nodes := make(NodeAccounts, 3)
	keys := make([]secp256k1.PrivKey, len(nodes))
	for i := range nodes {
		nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
	}
	out, vault := newErrataOutboundFixture(c, ctx, mgr, "DONATE:XMR.XMR")
	tx := out.Tx
	tx.Tx.ID = GetRandomTxHash()
	tx.Tx.FromAddress, tx.Tx.ToAddress = tx.Tx.ToAddress, tx.Tx.FromAddress
	tx.BlockHeight = tx.XmrMinedHeight
	deliver := func(tx common.ObservedTx) {
		msg := types.NewMsgObservedTxQuorum(&common.QuorumTx{
			ObsTx: tx, Inbound: true,
			Attestations: []*common.Attestation{attestObservedTx(c, keys[0], tx, true), attestObservedTx(c, keys[1], tx, true)},
		}, nodes[0].NodeAddress)
		attempt, commit := ctx.CacheContext()
		_, err := NewObservedTxQuorumHandler(mgr).Run(attempt, msg)
		c.Assert(err, IsNil)
		commit()
	}
	deliver(tx)
	inbound, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(inbound.Tx.IsEmpty(), Equals, false)
	c.Assert(inbound.Tx.IsFinal(), Equals, false)
	c.Assert(inbound.UpdatedVault, Equals, false)
	msg := newSignedErrataQuorumMessage(c, makeErrataTx(tx.Tx.ID, common.XMRChain), nodes, keys[:2]...)
	c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
	c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
	// Later finalization cannot credit an already reverted inbound.
	ctx = ctx.WithBlockHeight(101)
	tx.BlockHeight = tx.FinaliseHeight
	deliver(tx)
	c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
	c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
	inbound, err = mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(inbound.Reverted, Equals, true)
	c.Assert(inbound.UpdatedVault, Equals, false)
	saved, err := mgr.Keeper().GetErrataTxVoter(ctx, tx.Tx.ID, common.XMRChain)
	c.Assert(err, IsNil)
	c.Assert(saved.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
	c.Assert(saved.BlockHeight, Equals, int64(100))
	got, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(got.Coins, DeepEquals, vault.Coins)
}

func (s *HandlerErrataTxQuorumSuite) TestTerminalErrataForeignInboundOutcome(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	na, key := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	tx := GetRandomObservedTx()
	tx.Tx.Chain = common.ETHChain
	inbound := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	inbound.Tx = tx
	mgr.Keeper().SetObservedTxInVoter(ctx, inbound)
	msg := newSignedErrataQuorumMessage(c, makeErrataTx(tx.Tx.ID, common.XMRChain), NodeAccounts{na}, key)
	for range 2 {
		c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
		c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION)
		got, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
		c.Assert(err, IsNil)
		c.Assert(got, DeepEquals, inbound)
		saved, err := mgr.Keeper().GetErrataTxVoter(ctx, tx.Tx.ID, common.XMRChain)
		c.Assert(err, IsNil)
		c.Assert(saved.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION)
		c.Assert(saved.BlockHeight, Equals, int64(100))
	}
}

type errataPrincipalCase struct {
	name          string
	chain         common.Chain
	selfSend      bool
	memo          string
	coin          uint64
	inbound, outb bool
	block, final  int64
	mined         int64
	future        bool
	expObs, expEr uint64
	debited       bool
}

func (s *HandlerErrataTxQuorumSuite) TestXMRErrataRestoresRecordedPrincipal(c *C) {
	const gas = 7
	cases := []errataPrincipalCase{
		{"normal final", common.XMRChain, false, "out", 100, false, true, 110, 110, 100, false, 893, 993, true},
		{"normal never-final", common.XMRChain, false, "out", 100, false, true, 100, 110, 100, false, 893, 993, true},
		{"pre-mining final", common.XMRChain, false, "out", 100, false, true, 90, 90, 0, true, 893, 993, true},
		{"self-send out-only", common.XMRChain, true, "consolidate", 100, false, true, 110, 110, 100, false, 993, 993, false},
		{"self-send in-only", common.XMRChain, true, "consolidate", 100, true, false, 110, 110, 100, false, 1000, 1000, false},
		{"self-send both", common.XMRChain, true, "consolidate", 100, true, true, 110, 110, 100, false, 993, 993, false},
		{"inbound-memo out", common.XMRChain, false, "+:XMR.XMR", 100, false, true, 110, 110, 100, false, 893, 993, true},
		{"inbound-memo self-send", common.XMRChain, true, "+:XMR.XMR", 100, false, true, 110, 110, 100, false, 893, 993, true},
		{"fake-gas shape", common.XMRChain, false, "own", 1, false, true, 110, 110, 100, false, 993, 993, false},
		{"BTC self-send out-only", common.BTCChain, true, "consolidate", 100, false, true, 110, 110, 0, false, 993, 1093, false},
		{"BTC self-send both", common.BTCChain, true, "consolidate", 100, true, true, 110, 110, 0, false, 993, 993, false},
		{"BTC normal final", common.BTCChain, false, "out", 100, false, true, 110, 110, 0, false, 893, 993, false},
	}
	for _, tc := range cases {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(1000)
		nodes := make(NodeAccounts, 3)
		keys := make([]secp256k1.PrivKey, len(nodes))
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		var pk common.PubKey
		var other common.Address
		var err error
		if tc.chain.Equals(common.XMRChain) {
			pk = GetRandomEd25519PubKey()
			other, err = GetRandomEd25519PubKey().GetAddress(tc.chain)
			c.Assert(err, IsNil)
		} else {
			pk = GetRandomPubKey()
			other = GetRandomBTCAddress()
		}
		addr, err := pk.GetAddress(tc.chain)
		c.Assert(err, IsNil)
		to := other
		if tc.selfSend {
			to = addr
		}
		gasAsset := tc.chain.GetGasAsset()
		vault := NewVault(1, ActiveVault, AsgardVault, pk, []string{tc.chain.String()}, nil)
		vault.AddFunds(common.NewCoins(common.NewCoin(gasAsset, cosmos.NewUint(1000))))
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		pool := NewPool()
		pool.Asset, pool.Status = gasAsset, PoolAvailable
		pool.BalanceAsset, pool.BalanceRune, pool.LPUnits = cosmos.NewUint(10000), cosmos.NewUint(10000), cosmos.NewUint(10000)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		id := GetRandomTxHash()
		memo := tc.memo
		switch tc.memo {
		case "out":
			inHash := GetRandomTxHash()
			memo = "OUT:" + inHash.String()
			inVoter := NewObservedTxVoter(inHash, nil)
			inVoter.Height, inVoter.FinalisedHeight = 990, 990
			mgr.Keeper().SetObservedTxInVoter(ctx, inVoter)
			c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{Height: 990, TxArray: []TxOutItem{{
				Chain: tc.chain, InHash: inHash, ToAddress: to, VaultPubKey: pk,
				Coin: common.NewCoin(gasAsset, cosmos.NewUint(tc.coin)), MaxGas: common.Gas{common.NewCoin(gasAsset, cosmos.NewUint(gas))}, Memo: memo,
			}}}), IsNil)
		case "own":
			memo = "OUT:" + id.String()
		}
		txo := common.Tx{
			ID: id, Chain: tc.chain, FromAddress: addr, ToAddress: to,
			Coins: common.NewCoins(common.NewCoin(gasAsset, cosmos.NewUint(tc.coin))),
			Gas:   common.Gas{common.NewCoin(gasAsset, cosmos.NewUint(gas))}, Memo: memo,
		}
		deliver := func(inbound bool) {
			tx := common.NewObservedTx(txo, tc.block, pk, tc.final)
			if tc.chain.Equals(common.XMRChain) {
				tx.XmrMinedHeight = tc.mined
				if inbound && tx.XmrMinedHeight == 0 {
					tx.XmrMinedHeight = tc.block
				}
				if !inbound {
					tx.XmrTxKey = strings.Repeat("a", 64)
				}
			}
			q := &common.QuorumTx{ObsTx: tx, Inbound: inbound, AllowFutureObservation: tc.future && !inbound}
			for _, k := range keys[:2] {
				q.Attestations = append(q.Attestations, attestObservedTx(c, k, tx, inbound))
			}
			msg := types.NewMsgObservedTxQuorum(q, nodes[0].NodeAddress)
			c.Assert(msg.ValidateBasic(), IsNil, Commentf(tc.name))
			cctx, commit := ctx.CacheContext()
			_, rerr := NewObservedTxQuorumHandler(mgr).Run(cctx, msg)
			c.Assert(rerr, IsNil, Commentf(tc.name))
			commit()
		}
		if tc.inbound {
			deliver(true)
		}
		if tc.outb {
			deliver(false)
		}

		// Read persisted state before errata; a self-send inbound records
		// UpdatedVault even though it did not credit principal.
		v1, vaultErr := mgr.Keeper().GetVault(ctx, pk)
		c.Assert(vaultErr, IsNil, Commentf(tc.name))
		c.Assert(v1.GetCoin(gasAsset).Amount.Uint64(), Equals, tc.expObs, Commentf(tc.name))
		out, outErr := mgr.Keeper().GetObservedTxOutVoter(ctx, id)
		c.Assert(outErr, IsNil, Commentf(tc.name))
		c.Assert(out.UpdatedVault, Equals, tc.debited, Commentf(tc.name))
		if tc.inbound {
			in, inErr := mgr.Keeper().GetObservedTxInVoter(ctx, id)
			c.Assert(inErr, IsNil, Commentf(tc.name))
			c.Assert(in.UpdatedVault, Equals, true, Commentf(tc.name))
		}

		er := makeErrataTx(id, tc.chain)
		qe := &common.QuorumErrataTx{ErrataTx: er}
		for _, k := range keys[:2] {
			qe.Attestations = append(qe.Attestations, attestErrataTx(c, k, er))
		}

		for i := 1; i <= 3; i++ {
			errataCtx := ctx.WithBlockHeight(int64(1000 + i))
			msg := types.NewMsgErrataTxQuorum(qe, nodes[0].NodeAddress)
			c.Assert(msg.ValidateBasic(), IsNil, Commentf(tc.name))
			c.Assert(runErrataQuorum(errataCtx, mgr, msg), IsNil, Commentf("%s, delivery %d", tc.name, i))
			v, restoredVaultErr := mgr.Keeper().GetVault(ctx, pk)
			c.Assert(restoredVaultErr, IsNil, Commentf(tc.name))
			c.Assert(v.GetCoin(gasAsset).Amount.Uint64(), Equals, tc.expEr, Commentf("%s, delivery %d", tc.name, i))
			errataVoter, voterErr := mgr.Keeper().GetErrataTxVoter(ctx, id, tc.chain)
			c.Assert(voterErr, IsNil, Commentf(tc.name))
			outcome := common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED
			if tc.chain.Equals(common.XMRChain) {
				outcome = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED
			}
			c.Assert(msg.QuoErrata.Outcome, Equals, outcome, Commentf(tc.name))
			c.Assert(errataVoter.Outcome, Equals, outcome, Commentf(tc.name))
			c.Assert(errataVoter.BlockHeight, Equals, int64(1001), Commentf(tc.name))
			if tc.inbound {
				in, inErr := mgr.Keeper().GetObservedTxInVoter(ctx, id)
				c.Assert(inErr, IsNil, Commentf(tc.name))
				c.Assert(in.Reverted, Equals, true, Commentf(tc.name))
			}
			if tc.outb {
				storedOut, storedOutErr := mgr.Keeper().GetObservedTxOutVoter(ctx, id)
				c.Assert(storedOutErr, IsNil, Commentf(tc.name))
				c.Assert(storedOut.Reverted, Equals, true, Commentf(tc.name))
				c.Assert(storedOut.UpdatedVault, Equals, tc.debited, Commentf(tc.name))
			}
		}
	}
}

// Model an index failure before SetVault writes the vault record.
type errataNoWriteVaultKeeper struct{ keeper.Keeper }

func (k errataNoWriteVaultKeeper) SetVault(cosmos.Context, Vault) error {
	return errors.New("injected asgard index failure before vault write")
}

func (s *HandlerErrataTxQuorumSuite) TestXMRErrataDoesNotCreditFailedVaultSave(c *C) {
	for _, path := range []string{"quorum", "direct"} {
		for _, memo := range []string{"out", "+:XMR.XMR"} {
			ctx, mgr := setupManagerForTest(c)
			ctx = ctx.WithBlockHeight(1000)
			n := 3
			if path == "direct" {
				n = 1
			}
			nodes := make(NodeAccounts, n)
			keys := make([]secp256k1.PrivKey, n)
			for i := range nodes {
				nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
			}
			pk := GetRandomEd25519PubKey()
			from, err := pk.GetAddress(common.XMRChain)
			c.Assert(err, IsNil)
			to, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
			c.Assert(err, IsNil)
			vault := NewVault(1, ActiveVault, AsgardVault, pk, []string{common.XMRChain.String()}, nil)
			vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1000))))
			c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
			pool := NewPool()
			pool.Asset, pool.Status = common.XMRAsset, PoolAvailable
			pool.BalanceAsset, pool.BalanceRune, pool.LPUnits = cosmos.NewUint(10000), cosmos.NewUint(10000), cosmos.NewUint(10000)
			c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
			id := GetRandomTxHash()
			m := memo
			if memo == "out" {
				inHash := GetRandomTxHash()
				m = "OUT:" + inHash.String()
				inVoter := NewObservedTxVoter(inHash, nil)
				inVoter.Height, inVoter.FinalisedHeight = 990, 990
				mgr.Keeper().SetObservedTxInVoter(ctx, inVoter)
				c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{Height: 990, TxArray: []TxOutItem{{
					Chain: common.XMRChain, InHash: inHash, ToAddress: to, VaultPubKey: pk,
					Coin: common.NewCoin(common.XMRAsset, cosmos.NewUint(100)), MaxGas: common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(7))}, Memo: m,
				}}}), IsNil)
			}
			tx := common.NewObservedTx(common.Tx{
				ID: id, Chain: common.XMRChain, FromAddress: from, ToAddress: to,
				Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
				Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(7))}, Memo: m,
			}, 110, pk, 110)
			tx.XmrMinedHeight = 100
			tx.XmrTxKey = strings.Repeat("a", 64)
			originalKeeper := mgr.K
			mgr.K = errataNoWriteVaultKeeper{originalKeeper}
			attempt, observationCommit := ctx.CacheContext()
			var observationErr error
			if path == "quorum" {
				q := &common.QuorumTx{ObsTx: tx}
				for _, k := range keys[:2] {
					q.Attestations = append(q.Attestations, attestObservedTx(c, k, tx, false))
				}
				_, observationErr = NewObservedTxQuorumHandler(mgr).Run(attempt, types.NewMsgObservedTxQuorum(q, nodes[0].NodeAddress))
			} else {
				_, observationErr = NewObservedTxOutHandler(mgr).Run(attempt, types.NewMsgObservedTxOut(common.ObservedTxs{tx}, nodes[0].NodeAddress))
			}

			mgr.K = originalKeeper
			c.Assert(observationErr, IsNil, Commentf("%s, %s", path, memo))
			observationCommit()
			v1, vaultErr := mgr.Keeper().GetVault(ctx, pk)
			c.Assert(vaultErr, IsNil)
			c.Assert(v1.GetCoin(common.XMRAsset).Amount.Uint64(), Equals, uint64(1000))
			out, voterErr := mgr.Keeper().GetObservedTxOutVoter(ctx, id)
			c.Assert(voterErr, IsNil)
			c.Assert(out.Height, Equals, int64(1000), Commentf("must exercise the quorum accounting path"))
			c.Assert(out.UpdatedVault, Equals, false, Commentf("%s, %s", path, memo))
			er := makeErrataTx(id, common.XMRChain)
			msg := newSignedErrataQuorumMessage(c, er, nodes, keys[:min(2, n)]...)
			for i := 1; i <= 3; i++ {
				c.Assert(runErrataQuorum(ctx.WithBlockHeight(int64(1000+i)), mgr, msg), IsNil)
				c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
				v2, restoredVaultErr := mgr.Keeper().GetVault(ctx, pk)
				c.Assert(restoredVaultErr, IsNil)
				c.Assert(v2.Coins, DeepEquals, v1.Coins, Commentf("%s, %s, delivery %d", path, memo, i))
				stored, storedVoterErr := mgr.Keeper().GetObservedTxOutVoter(ctx, id)
				c.Assert(storedVoterErr, IsNil)
				c.Assert(stored.UpdatedVault, Equals, false)
				c.Assert(stored.Reverted, Equals, true)
				errataVoter, errataErr := mgr.Keeper().GetErrataTxVoter(ctx, id, common.XMRChain)
				c.Assert(errataErr, IsNil)
				c.Assert(errataVoter.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
				c.Assert(errataVoter.BlockHeight, Equals, int64(1001))
			}
		}
	}
}

// A later final quorum replaces the selected observation but must retain the
// original principal-debit record and must not debit the vault again.
func (s *HandlerErrataTxQuorumSuite) TestXMRErrataDebitSurvivesFinalObservation(c *C) {
	f := newErrataOrderingFixture(c, false)
	c.Assert(f.deliver(c, f.observation(c, 100, false, 2)), IsNil)
	pending, err := f.mgr.Keeper().GetObservedTxOutVoter(f.ctx, f.tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(pending.UpdatedVault, Equals, true)
	c.Assert(pending.FinalisedHeight, Equals, int64(0))
	f.ctx = f.ctx.WithBlockHeight(1001)
	c.Assert(f.deliver(c, f.observation(c, 100, true, 2)), IsNil)
	final, err := f.mgr.Keeper().GetObservedTxOutVoter(f.ctx, f.tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(final.UpdatedVault, Equals, true)
	c.Assert(final.FinalisedHeight, Equals, int64(1001))
	vault, err := f.mgr.Keeper().GetVault(f.ctx, f.tx.ObservedPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.GetCoin(common.XMRAsset).Amount.Uint64(), Equals, uint64(899))
	msg := f.errata(c, 2)
	c.Assert(f.deliver(c, msg), IsNil)
	c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
	restored, err := f.mgr.Keeper().GetVault(f.ctx, f.tx.ObservedPubKey)
	c.Assert(err, IsNil)
	c.Assert(restored.GetCoin(common.XMRAsset).Amount.Uint64(), Equals, uint64(999))
}

// Fund the vault membership so this exercises both the bond transfer and pool
// compensation, rather than only the outbound vault debit.
func (s *HandlerErrataTxQuorumSuite) TestXMRErrataPreservesSlashesAndCollectsSurplus(c *C) {
	for _, memo := range []string{"+:XMR.XMR", "not-a-memo", "OUT:" + GetRandomTxHash().String()} {
		c.Logf("slashed outbound memo: %s", memo)
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(1000)
		k := mgr.Keeper()
		k.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 1)
		k.SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 1)
		k.SetMimir(ctx, constants.OverSolvencyToTreasuryBps.String(), 10000)
		k.SetMimir(ctx, constants.PauseOnSlashThreshold.String(), common.One)
		FundModule(c, ctx, k, BondName, 1_000_000*common.One)
		nodes := make(NodeAccounts, 3)
		keys := make([]secp256k1.PrivKey, len(nodes))
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, k, ctx)
		}

		vault := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, common.EmptyPubKey)
		vault.PubKeyFrost = GetRandomEd25519PubKey()
		for _, node := range nodes {
			vault.Membership = append(vault.Membership, node.PubKeySet.Secp256k1.String())
		}
		vault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1000*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		pool := NewPool()
		pool.Asset, pool.Status = common.XMRAsset, PoolAvailable
		pool.BalanceAsset, pool.BalanceRune, pool.LPUnits = cosmos.NewUint(1000*common.One), cosmos.NewUint(1000*common.One), cosmos.NewUint(1000*common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		c.Assert(k.SaveNetworkFee(ctx, common.XMRChain, NetworkFee{Chain: common.XMRChain, TransactionSize: 1, TransactionFeeRate: 10000}), IsNil)

		type accounting struct {
			vault, poolAsset, poolRune, bond, asgard, reserve uint64
			nodeBonds                                         []uint64
		}
		readAccounting := func() accounting {
			storedVault, vaultErr := k.GetVault(ctx, vault.PubKey)
			c.Assert(vaultErr, IsNil)
			storedPool, poolErr := k.GetPool(ctx, common.XMRAsset)
			c.Assert(poolErr, IsNil)
			result := accounting{
				vault:     storedVault.GetCoin(common.XMRAsset).Amount.Uint64(),
				poolAsset: storedPool.BalanceAsset.Uint64(), poolRune: storedPool.BalanceRune.Uint64(),
				bond:    k.GetRuneBalanceOfModule(ctx, BondName).Uint64(),
				asgard:  k.GetRuneBalanceOfModule(ctx, AsgardName).Uint64(),
				reserve: k.GetRuneBalanceOfModule(ctx, ReserveName).Uint64(),
			}
			for _, node := range nodes {
				storedNode, nodeErr := k.GetNodeAccount(ctx, node.NodeAddress)
				c.Assert(nodeErr, IsNil)
				result.nodeBonds = append(result.nodeBonds, storedNode.Bond.Uint64())
			}
			return result
		}
		network := func() *NetworkMgr { return newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr()) }
		surplus := func() int64 {
			amounts, solvencyErr := network().calculateNetworkSolvency(ctx, mgr)
			c.Assert(solvencyErr, IsNil)
			c.Assert(amounts, HasLen, 1)
			c.Assert(amounts[0].Asset.Equals(common.XMRAsset), Equals, true)
			return amounts[0].Amount.Int64()
		}
		queuedSwaps := func() []MsgSwap {
			iter := k.GetAdvSwapQueueItemIterator(ctx)
			defer iter.Close()
			var swaps []MsgSwap
			for ; iter.Valid(); iter.Next() {
				var swap MsgSwap
				c.Assert(k.Cdc().Unmarshal(iter.Value(), &swap), IsNil)
				swaps = append(swaps, swap)
			}
			return swaps
		}
		destination, addressErr := cosmos.AccAddressFromBech32(k.GetConstants().GetStringValue(constants.OverSolvencyAddress))
		c.Assert(addressErr, IsNil)
		recipientBalance := func() uint64 { return k.GetBalance(ctx, destination).AmountOf(common.RuneAsset().Native()).Uint64() }

		before := readAccounting()
		c.Assert(surplus(), Equals, int64(0))
		from, fromErr := vault.GetAddress(common.XMRChain)
		c.Assert(fromErr, IsNil)
		to, toErr := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
		c.Assert(toErr, IsNil)
		tx := common.NewObservedTx(common.Tx{
			ID: GetRandomTxHash(), Chain: common.XMRChain, FromAddress: from, ToAddress: to,
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))),
			Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(7*common.One))}, Memo: memo,
		}, 110, vault.PubKeyFrost, 110)
		tx.XmrMinedHeight = 100
		tx.XmrTxKey = strings.Repeat("a", 64)
		q := &common.QuorumTx{ObsTx: tx}
		for _, key := range keys[:2] {
			q.Attestations = append(q.Attestations, attestObservedTx(c, key, tx, false))
		}
		observation := types.NewMsgObservedTxQuorum(q, nodes[0].NodeAddress)
		c.Assert(observation.ValidateBasic(), IsNil)
		attempt, commit := ctx.CacheContext()
		_, observationErr := NewObservedTxQuorumHandler(mgr).Run(attempt, observation)
		c.Assert(observationErr, IsNil)
		commit()

		observed := readAccounting()
		c.Assert(observed.vault, Equals, uint64(893*common.One))
		c.Assert(observed.poolAsset, Equals, uint64(893*common.One))
		c.Assert(observed.poolRune, Equals, uint64(1107*common.One))
		c.Assert(observed.asgard-before.asgard, Equals, uint64(107*common.One))
		c.Assert(observed.reserve-before.reserve, Equals, uint64(535*common.One/10))
		c.Assert(before.bond-observed.bond, Equals, uint64(1605*common.One/10))
		for i := range nodes {
			c.Assert(before.nodeBonds[i]-observed.nodeBonds[i], Equals, uint64(535*common.One/10))
		}
		c.Assert(surplus(), Equals, int64(0))
		out, outErr := k.GetObservedTxOutVoter(ctx, tx.Tx.ID)
		c.Assert(outErr, IsNil)
		c.Assert(out.UpdatedVault, Equals, true)
		c.Assert(out.Reverted, Equals, false)

		restored := observed
		restored.vault = 993 * common.One
		deliverErrata := func() {
			message := newSignedErrataQuorumMessage(c, makeErrataTx(tx.Tx.ID, common.XMRChain), nodes, keys[:2]...)
			c.Assert(message.ValidateBasic(), IsNil)
			c.Assert(runErrataQuorum(ctx, mgr, message), IsNil)
			c.Assert(message.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			stored, errataErr := k.GetErrataTxVoter(ctx, tx.Tx.ID, common.XMRChain)
			c.Assert(errataErr, IsNil)
			c.Assert(stored.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			c.Assert(stored.BlockHeight, Equals, int64(1001))
			reverted, voterErr := k.GetObservedTxOutVoter(ctx, tx.Tx.ID)
			c.Assert(voterErr, IsNil)
			c.Assert(reverted.UpdatedVault, Equals, true)
			c.Assert(reverted.Reverted, Equals, true)
		}
		for delivery := int64(1); delivery <= 3; delivery++ {
			ctx = ctx.WithBlockHeight(1000 + delivery)
			deliverErrata()
			// Gas, pool compensation and each member's penalty remain charged.
			c.Assert(readAccounting(), DeepEquals, restored)
			c.Assert(surplus(), Equals, int64(100*common.One))
		}

		recipientBefore := recipientBalance()
		c.Assert(network().processOverSolvency(ctx, mgr), IsNil)
		pending := queuedSwaps()
		c.Assert(pending, HasLen, 1)
		c.Assert(pending[0].State.Deposit.Uint64(), Equals, uint64(100*common.One))
		c.Assert(pending[0].Destination.String(), Equals, destination.String())
		c.Assert(surplus(), Equals, int64(0))

		ctx = ctx.WithBlockHeight(1004)
		c.Assert(network().processOverSolvency(ctx, mgr), IsNil)
		c.Assert(queuedSwaps(), DeepEquals, pending)
		// The slash's trading halt defers collection without losing its reservation.
		c.Assert(newSwapQueueAdv(k).EndBlock(ctx, mgr, false), IsNil)
		c.Assert(queuedSwaps(), DeepEquals, pending)
		c.Assert(readAccounting(), DeepEquals, restored)
		c.Assert(recipientBalance(), Equals, recipientBefore)

		ctx = ctx.WithBlockHeight(1005)
		k.SetMimir(ctx, "HaltXMRTrading", 0)
		c.Assert(newSwapQueueAdv(k).EndBlock(ctx, mgr, false), IsNil)
		c.Assert(queuedSwaps(), HasLen, 0)
		c.Assert(surplus(), Equals, int64(0))
		collected := readAccounting()
		recipientAfter := recipientBalance()
		c.Assert(recipientAfter > recipientBefore, Equals, true)
		c.Assert(collected.vault, Equals, restored.vault)
		c.Assert(collected.poolAsset, Equals, uint64(993*common.One))
		c.Assert(collected.bond, Equals, restored.bond)
		c.Assert(collected.nodeBonds, DeepEquals, restored.nodeBonds)
		// RUNE leaves Asgard only as the recipient's payout and the swap fee;
		// the same total leaves the pool's RUNE balance.
		c.Assert(restored.asgard-collected.asgard, Equals, recipientAfter-recipientBefore+collected.reserve-restored.reserve)
		c.Assert(restored.poolRune-collected.poolRune, Equals, restored.asgard-collected.asgard)

		ctx = ctx.WithBlockHeight(1006)
		deliverErrata()
		c.Assert(network().processOverSolvency(ctx, mgr), IsNil)
		c.Assert(newSwapQueueAdv(k).EndBlock(ctx, mgr, false), IsNil)
		c.Assert(queuedSwaps(), HasLen, 0)
		c.Assert(readAccounting(), DeepEquals, collected)
		c.Assert(recipientBalance(), Equals, recipientAfter)
		c.Assert(surplus(), Equals, int64(0))
	}
}

package thorchain

import (
	"bytes"
	"errors"
	"sort"

	"cosmossdk.io/log"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type SolvencyAdmissionSuite struct{}

var _ = Suite(&SolvencyAdmissionSuite{})

func setupSolvencyAdmissionTest(c *C) (cosmos.Context, *Mgrs, NodeAccounts, []secp256k1.PrivKey, Vault) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nodes := make(NodeAccounts, 4)
	keys := make([]secp256k1.PrivKey, len(nodes))
	for i := range nodes {
		nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
	}
	vault := NewVault(100, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.TRONChain.String()}, nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(common.One))))
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	// Keep report height 101 stale so consensus tests exercise admission without
	// depending on insolvency price feeds or causing a chain halt.
	c.Assert(mgr.Keeper().SetLastChainHeight(ctx, common.TRONChain, 1000), IsNil)
	return ctx, mgr, nodes, keys, vault
}

func admissionSolvency(c *C, pubKey common.PubKey, amount uint64) *common.Solvency {
	return makeSolvency(c, common.TRONChain, pubKey,
		common.NewCoins(common.NewCoin(common.TRXAsset, cosmos.NewUint(amount))), 101)
}

func admissionLegacy(c *C, sol *common.Solvency, signer cosmos.AccAddress) *MsgSolvency {
	msg, err := NewMsgSolvency(sol.Chain, sol.PubKey, sol.Coins, sol.Height, signer)
	c.Assert(err, IsNil)
	return msg
}

func admissionQuorum(c *C, sol *common.Solvency, keys ...secp256k1.PrivKey) *types.MsgSolvencyQuorum {
	atts := make([]*common.Attestation, len(keys))
	for i, key := range keys {
		atts[i] = attestSolvency(c, key, sol)
	}
	msg, err := types.NewMsgSolvencyQuorum(&common.QuorumSolvency{Solvency: sol, Attestations: atts}, GetRandomBech32Addr())
	c.Assert(err, IsNil)
	return msg
}

func assertAdmissionQuota(c *C, ctx cosmos.Context, k keeper.Keeper, signer cosmos.AccAddress, expectedWindow, expectedUsed uint64) {
	window, used, err := k.GetSolvencyVoterQuota(ctx, common.TRONChain, signer)
	c.Assert(err, IsNil)
	c.Check(window, Equals, expectedWindow)
	c.Check(used, Equals, expectedUsed)
}

func assertNoAdmissionVoter(c *C, ctx cosmos.Context, k keeper.Keeper, sol *common.Solvency) {
	voter, err := k.GetSolvencyVoter(ctx, sol.Id, sol.Chain)
	c.Assert(err, IsNil)
	// Empty() alone would also accept an erroneously persisted unsigned voter.
	c.Assert(voter.Id.IsEmpty(), Equals, true)
}

func (s *SolvencyAdmissionSuite) TestSharedBudgetAndFieldBypasses(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
	legacy := NewSolvencyHandler(mgr)
	quorum := NewSolvencyQuorumHandler(mgr)
	for i := uint64(0); i < limit; i++ {
		sol := admissionSolvency(c, vault.PubKey, i+1)
		var err error
		if i%2 == 0 {
			_, err = legacy.Run(ctx, admissionLegacy(c, sol, nodes[0].NodeAddress))
		} else {
			_, err = quorum.Run(ctx, admissionQuorum(c, sol, keys[0]))
		}
		c.Assert(err, IsNil)
	}
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[0].NodeAddress, 10, limit)
	assertSlashPoints(c, ctx, mgr, nodes, 0)

	for _, variant := range []string{"balance", "height", "vault", "chain case"} {
		sol := admissionSolvency(c, vault.PubKey, 1000)
		switch variant {
		case "height":
			sol.Height = 1_000_000_001
		case "vault":
			sol.PubKey = GetRandomPubKey()
		case "chain case":
			sol.Chain = common.Chain("tron")
		}
		var err error
		sol.Id, err = sol.Hash()
		c.Assert(err, IsNil)
		_, err = legacy.Run(ctx, admissionLegacy(c, sol, nodes[0].NodeAddress))
		c.Check(errors.Is(err, errSolvencyVoterQuotaExceeded), Equals, true, Commentf("legacy %s", variant))
		_, err = quorum.Run(ctx, admissionQuorum(c, sol, keys[0]))
		c.Check(errors.Is(err, errSolvencyVoterQuotaExceeded), Equals, true, Commentf("quorum %s", variant))
		assertNoAdmissionVoter(c, ctx, mgr.Keeper(), sol)
	}
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[0].NodeAddress, 10, limit)
	assertSlashPoints(c, ctx, mgr, nodes, 0)

	// A different active validator retains their full allowance.
	sol := admissionSolvency(c, vault.PubKey, 1001)
	_, err := quorum.Run(ctx, admissionQuorum(c, sol, keys[1]))
	c.Assert(err, IsNil)
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[1].NodeAddress, 10, 1)
}

func (s *SolvencyAdmissionSuite) TestExistingVotersReachConsensusAndDuplicatesStillSlash(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
	legacy := NewSolvencyHandler(mgr)
	quorum := NewSolvencyQuorumHandler(mgr)
	sol := admissionSolvency(c, vault.PubKey, 100)
	msg := admissionLegacy(c, sol, nodes[0].NodeAddress)
	_, err := legacy.Run(ctx, msg)
	c.Assert(err, IsNil)
	for _, node := range nodes {
		mgr.Keeper().SetSolvencyVoterQuota(ctx, common.TRONChain, node.NodeAddress, 10, limit)
	}

	_, err = quorum.Run(ctx, admissionQuorum(c, sol, keys[1]))
	c.Assert(err, IsNil)
	voter, err := mgr.Keeper().GetSolvencyVoter(ctx, sol.Id, sol.Chain)
	c.Assert(err, IsNil)
	c.Assert(voter.ConsensusBlockHeight, Equals, ctx.BlockHeight())
	_, err = legacy.Run(ctx, admissionLegacy(c, sol, nodes[2].NodeAddress))
	c.Assert(err, IsNil)
	assertSlashPoints(c, ctx, mgr, nodes, 0)

	// Quorum re-delivery keeps its existing no-duplicate-slash behavior.
	_, err = quorum.Run(ctx, admissionQuorum(c, sol, keys[1]))
	c.Assert(err, IsNil)
	assertSlashPoints(c, ctx, mgr, nodes, 0)
	_, err = legacy.Run(ctx, msg)
	c.Assert(err, IsNil)
	points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(points, Equals, mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints))
	for _, node := range nodes {
		assertAdmissionQuota(c, ctx, mgr.Keeper(), node.NodeAddress, 10, limit)
	}
}

func (s *SolvencyAdmissionSuite) TestWindowResetAndHandlerRecreation(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
	mgr.Keeper().SetSolvencyVoterQuota(ctx, common.TRONChain, nodes[0].NodeAddress, 10, limit)
	sol := admissionSolvency(c, vault.PubKey, 100)
	msg := admissionQuorum(c, sol, keys[0])
	for _, height := range []int64{100, 109} {
		handler := NewSolvencyQuorumHandler(mgr)
		_, err := handler.Run(ctx.WithBlockHeight(height), msg)
		c.Assert(errors.Is(err, errSolvencyVoterQuotaExceeded), Equals, true)
		assertNoAdmissionVoter(c, ctx, mgr.Keeper(), sol)
	}
	ctx = ctx.WithBlockHeight(110)
	handler := NewSolvencyQuorumHandler(mgr)
	_, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[0].NodeAddress, 11, 1)
}

func (s *SolvencyAdmissionSuite) TestQuorumBudgetOwnerIsVerifiedAndDeterministic(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
	// Rank the signers as the admission policy does, independently of message order.
	order := []int{0, 1, 2}
	sort.Slice(order, func(i, j int) bool {
		return bytes.Compare(nodes[order[i]].NodeAddress, nodes[order[j]].NodeAddress) < 0
	})
	for _, reverse := range []bool{false, true} {
		cache, _ := ctx.CacheContext()
		mgr.Keeper().SetSolvencyVoterQuota(cache, common.TRONChain, nodes[order[0]].NodeAddress, 10, limit)
		sol := admissionSolvency(c, vault.PubKey, 100)
		msg := admissionQuorum(c, sol, keys[order[0]], keys[order[1]], keys[order[2]])
		if reverse {
			msg.QuoSolvency.Attestations[0], msg.QuoSolvency.Attestations[2] = msg.QuoSolvency.Attestations[2], msg.QuoSolvency.Attestations[0]
		}
		handler := NewSolvencyQuorumHandler(mgr)
		_, err := handler.Run(cache, msg)
		c.Assert(err, IsNil)
		assertAdmissionQuota(c, cache, mgr.Keeper(), nodes[order[0]].NodeAddress, 10, limit)
		assertAdmissionQuota(c, cache, mgr.Keeper(), nodes[order[1]].NodeAddress, 10, 1)
		assertAdmissionQuota(c, cache, mgr.Keeper(), nodes[order[2]].NodeAddress, 0, 0)
		assertAdmissionQuota(c, cache, mgr.Keeper(), msg.Signer, 0, 0)
	}
}

func (s *SolvencyAdmissionSuite) TestInvalidAndInactiveSignaturesCannotCreateOrSpendQuota(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
	// The only genuine active signer has already used their allowance.
	mgr.Keeper().SetSolvencyVoterQuota(ctx, common.TRONChain, nodes[0].NodeAddress, 10, limit)
	sol := admissionSolvency(c, vault.PubKey, 100)
	msg := admissionQuorum(c, sol, keys[0])
	forged := attestSolvency(c, keys[1], sol)
	forged.Signature[0] ^= 1
	msg.QuoSolvency.Attestations = append(msg.QuoSolvency.Attestations, forged)
	handler := NewSolvencyQuorumHandler(mgr)
	_, err := handler.Run(ctx, msg)
	c.Assert(errors.Is(err, errSolvencyVoterQuotaExceeded), Equals, true)
	assertNoAdmissionVoter(c, ctx, mgr.Keeper(), sol)
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[1].NodeAddress, 0, 0)

	// Even a populated attestation list must not create an unsigned voter.
	msg.QuoSolvency.Attestations = []*common.Attestation{forged}
	_, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	assertNoAdmissionVoter(c, ctx, mgr.Keeper(), sol)

	nodes[1].Status = NodeStandby
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodes[1]), IsNil)
	msg = admissionQuorum(c, sol, keys[1])
	_, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	assertNoAdmissionVoter(c, ctx, mgr.Keeper(), sol)
	_, err = NewSolvencyHandler(mgr).Run(ctx, admissionLegacy(c, sol, nodes[1].NodeAddress))
	c.Assert(err, NotNil)
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[1].NodeAddress, 0, 0)
}

func (s *SolvencyAdmissionSuite) TestProcessingFailureAndTransactionRollback(c *C) {
	ctx, mgr, nodes, keys, _ := setupSolvencyAdmissionTest(c)
	k := mgr.Keeper()
	handler := NewSolvencyQuorumHandler(mgr)
	// Two signatures reach consensus but the vault lookup fails.
	sol := admissionSolvency(c, GetRandomPubKey(), 100)
	msg := admissionQuorum(c, sol, keys[0], keys[1])
	_, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	assertNoAdmissionVoter(c, ctx, k, sol)
	for _, node := range nodes {
		assertAdmissionQuota(c, ctx, k, node.NodeAddress, 0, 0)
	}

	// A successful below-consensus admission is still rolled back with a failed
	// enclosing transaction or post-handler: neither quota nor voter survives.
	msg = admissionQuorum(c, sol, keys[0])
	cache, _ := ctx.CacheContext()
	_, err = handler.Run(cache, msg)
	c.Assert(err, IsNil)
	assertAdmissionQuota(c, cache, k, nodes[0].NodeAddress, 10, 1)
	assertAdmissionQuota(c, ctx, k, nodes[0].NodeAddress, 0, 0)
	assertNoAdmissionVoter(c, ctx, k, sol)
	cache, commit := ctx.CacheContext()
	_, err = handler.Run(cache, msg)
	c.Assert(err, IsNil)
	commit()
	assertAdmissionQuota(c, ctx, k, nodes[0].NodeAddress, 10, 1)
	voter, err := k.GetSolvencyVoter(ctx, sol.Id, sol.Chain)
	c.Assert(err, IsNil)
	c.Assert(voter.GetSigners(), HasLen, 1)
}

func (s *SolvencyAdmissionSuite) TestNonTRONStillUsesObservationSlashing(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
	mgr.Keeper().SetSolvencyVoterQuota(ctx, common.TRONChain, nodes[0].NodeAddress, 10, limit)
	sol := makeSolvency(c, common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))), 101)
	_, err := NewSolvencyHandler(mgr).Run(ctx, admissionLegacy(c, sol, nodes[0].NodeAddress))
	c.Assert(err, IsNil)
	sol = makeSolvency(c, common.ETHChain, vault.PubKey,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One+1))), 101)
	_, err = NewSolvencyQuorumHandler(mgr).Run(ctx, admissionQuorum(c, sol, keys[0]))
	c.Assert(err, IsNil)
	points, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodes[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(points, Equals, 2*mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints))
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[0].NodeAddress, 10, limit)
}

func (s *SolvencyAdmissionSuite) TestUnusableQuorumsLeaveQueueWithoutChangingState(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	next := func(ctx cosmos.Context, _ sdk.Tx, _, _ bool) (cosmos.Context, error) { return ctx, nil }
	for _, invalidSignature := range []bool{false, true} {
		for _, existingVoter := range []bool{false, true} {
			c.Logf("invalidSignature=%t, existingVoter=%t", invalidSignature, existingVoter)
			ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
			k := mgr.Keeper()
			sol := admissionSolvency(c, vault.PubKey, 100)
			if existingVoter {
				_, err := NewSolvencyHandler(mgr).Run(ctx, admissionLegacy(c, sol, nodes[1].NodeAddress))
				c.Assert(err, IsNil)
			}
			before, err := k.GetSolvencyVoter(ctx, sol.Id, sol.Chain)
			c.Assert(err, IsNil)
			k.SetSolvencyVoterQuota(ctx, sol.Chain, nodes[0].NodeAddress, 10, limit)

			msg := admissionQuorum(c, sol, keys[0])
			if invalidSignature {
				msg.QuoSolvency.Attestations[0].Signature[0] ^= 1
			}
			eb := ebifrost.NewEnshrinedBifrost(cdc, log.NewNopLogger(), ebifrost.EBifrostConfig{Enable: true})
			_, err = eb.SendQuorumSolvency(ctx, msg.QuoSolvency)
			c.Assert(err, IsNil)
			if !invalidSignature {
				// The attestation was queued while the signer was still active.
				nodes[0].Status = NodeStandby
				c.Assert(k.SetNodeAccount(ctx, nodes[0]), IsNil)
			}
			ctx = ctx.WithBlockHeight(101)
			proposal, _ := eb.ProposalInjectTxs(ctx, 1_000_000)
			c.Assert(proposal, HasLen, 1)
			tx, err := ebifrost.TxDecoder(cdc, nil)(proposal[0])
			c.Assert(err, IsNil)
			c.Assert(tx.GetMsgs(), HasLen, 1)
			cache, commit := ctx.CacheContext()
			result, err := NewSolvencyQuorumHandler(mgr).Run(cache, tx.GetMsgs()[0])
			c.Assert(err, IsNil)
			c.Assert(result, NotNil)
			post := ebifrost.NewEnshrineBifrostPostDecorator(eb)
			_, err = post.PostHandle(cache, tx, false, true, next)
			c.Assert(err, IsNil)
			commit()

			after, err := k.GetSolvencyVoter(ctx, sol.Id, sol.Chain)
			c.Assert(err, IsNil)
			c.Assert(after, DeepEquals, before)
			for i, node := range nodes {
				window, used := uint64(0), uint64(0)
				if i == 0 {
					window, used = 10, limit
				} else if existingVoter && i == 1 {
					window, used = 10, 1
				}
				assertAdmissionQuota(c, ctx, k, node.NodeAddress, window, used)
			}
			assertAdmissionQuota(c, ctx, k, msg.Signer, 0, 0)
			assertSlashPoints(c, ctx, mgr, nodes, 0)
			proposal, _ = eb.ProposalInjectTxs(ctx.WithBlockHeight(102), 1_000_000)
			c.Assert(proposal, HasLen, 0)
		}
	}
}

func (s *SolvencyAdmissionSuite) TestQuorumRejectionRemainsQueuedUntilSuccessfulRetry(c *C) {
	_, limit := common.TRONChain.SolvencyVoterQuota()
	ctx, mgr, nodes, keys, vault := setupSolvencyAdmissionTest(c)
	mgr.Keeper().SetSolvencyVoterQuota(ctx, common.TRONChain, nodes[0].NodeAddress, 10, limit)
	sol := admissionSolvency(c, vault.PubKey, 100)
	msg := admissionQuorum(c, sol, keys[0])
	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	eb := ebifrost.NewEnshrinedBifrost(cdc, log.NewNopLogger(), ebifrost.EBifrostConfig{Enable: true})
	_, err := eb.SendQuorumSolvency(ctx, msg.QuoSolvency)
	c.Assert(err, IsNil)
	post := ebifrost.NewEnshrineBifrostPostDecorator(eb)
	next := func(ctx cosmos.Context, _ sdk.Tx, _, _ bool) (cosmos.Context, error) { return ctx, nil }
	handler := NewSolvencyQuorumHandler(mgr)

	for _, height := range []int64{100, 109, 110} {
		ctx = ctx.WithBlockHeight(height)
		proposal, _ := eb.ProposalInjectTxs(ctx, 1_000_000)
		c.Assert(proposal, HasLen, 1)
		tx, err := ebifrost.TxDecoder(cdc, nil)(proposal[0])
		c.Assert(err, IsNil)
		c.Assert(tx.GetMsgs(), HasLen, 1)
		cache, commit := ctx.CacheContext()
		_, runErr := handler.Run(cache, tx.GetMsgs()[0])
		success := runErr == nil
		if height < 110 {
			c.Assert(errors.Is(runErr, errSolvencyVoterQuotaExceeded), Equals, true)
		} else {
			c.Assert(runErr, IsNil)
		}
		_, err = post.PostHandle(cache, tx, false, success, next)
		c.Assert(err, IsNil)
		if success {
			commit()
		} else {
			assertNoAdmissionVoter(c, ctx, mgr.Keeper(), sol)
			assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[0].NodeAddress, 10, limit)
		}
	}
	proposal, _ := eb.ProposalInjectTxs(ctx, 1_000_000)
	c.Assert(proposal, HasLen, 0)
	assertAdmissionQuota(c, ctx, mgr.Keeper(), nodes[0].NodeAddress, 11, 1)
}

type failingSolvencyQuotaKeeper struct{ keeper.Keeper }

func (k failingSolvencyQuotaKeeper) GetSolvencyVoterQuota(cosmos.Context, common.Chain, cosmos.AccAddress) (uint64, uint64, error) {
	return 0, 0, errKaboom
}

func (s *SolvencyAdmissionSuite) TestQuotaReadAndInvalidWindowFailClosed(c *C) {
	ctx, mgr, nodes, _, _ := setupSolvencyAdmissionTest(c)
	signers := []cosmos.AccAddress{nodes[0].NodeAddress}
	quota, err := checkSolvencyVoterQuota(ctx, failingSolvencyQuotaKeeper{mgr.Keeper()}, common.TRONChain, signers)
	c.Assert(quota, IsNil)
	c.Assert(errors.Is(err, errKaboom), Equals, true)
	for _, height := range []int64{-1, 0} {
		quota, err = checkSolvencyVoterQuota(ctx.WithBlockHeight(height), mgr.Keeper(), common.TRONChain, signers)
		c.Assert(quota, IsNil)
		c.Assert(err, NotNil)
	}
	mgr.Keeper().SetSolvencyVoterQuota(ctx, common.TRONChain, nodes[0].NodeAddress, 11, 1)
	quota, err = checkSolvencyVoterQuota(ctx, mgr.Keeper(), common.TRONChain, signers)
	c.Assert(quota, IsNil)
	c.Assert(err, NotNil)
}

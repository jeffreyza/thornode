package thorchain

import (
	"context"

	"cosmossdk.io/log"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Sign-time XMR observations are final with no mined height. A subsequent mined
// quorum does not replace the selected final transaction. Errata must restore
// the principal exactly once in either state and retire the injected message.
func (s *HandlerErrataTxQuorumSuite) TestPreMiningOutboundQuorumErrata(c *C) {
	for _, minedArrived := range []bool{false, true} {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(1000)
		nodes := make(NodeAccounts, 3)
		keys := make([]secp256k1.PrivKey, len(nodes))
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		inHash := GetRandomTxHash()
		out, vault := newErrataOutboundFixture(c, ctx, mgr, "OUT:"+inHash.String())
		vault.UpdateStatus(ActiveVault, ctx.BlockHeight())
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		mgr.Keeper().SetObservedTxOutVoter(ctx, NewObservedTxVoter(out.TxID, nil))
		inVoter := NewObservedTxVoter(inHash, nil)
		inVoter.Height, inVoter.FinalisedHeight = 990, 990
		mgr.Keeper().SetObservedTxInVoter(ctx, inVoter)
		c.Assert(mgr.Keeper().SetTxOut(ctx, &TxOut{Height: 990, TxArray: []TxOutItem{{
			Chain: common.XMRChain, InHash: inHash, ToAddress: out.Tx.Tx.ToAddress,
			VaultPubKey: vault.PubKey, Coin: out.Tx.Tx.Coins[0], MaxGas: out.Tx.Tx.Gas, Memo: out.Tx.Tx.Memo,
		}}}), IsNil)
		pool := NewPool()
		pool.Asset, pool.Status = common.XMRAsset, PoolAvailable
		pool.BalanceAsset, pool.BalanceRune, pool.LPUnits = cosmos.NewUint(10000), cosmos.NewUint(10000), cosmos.NewUint(10000)
		c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
		c.Assert(mgr.Keeper().SaveNetworkFee(ctx, common.XMRChain, NetworkFee{
			Chain: common.XMRChain, TransactionSize: 1, TransactionFeeRate: 10000,
		}), IsNil)

		deliverObservation := func(tx common.ObservedTx, allowFuture bool) {
			msg := types.NewMsgObservedTxQuorum(&common.QuorumTx{
				ObsTx: tx, Inbound: false, AllowFutureObservation: allowFuture,
				Attestations: []*common.Attestation{
					attestObservedTx(c, keys[0], tx, false),
					attestObservedTx(c, keys[1], tx, false),
				},
			}, nodes[0].NodeAddress)
			c.Assert(msg.ValidateBasic(), IsNil)
			attempt, commit := ctx.CacheContext()
			_, err := NewObservedTxQuorumHandler(mgr).Run(attempt, msg)
			c.Assert(err, IsNil)
			commit()
		}
		assertVault := func(amount uint64) {
			got, err := mgr.Keeper().GetVault(ctx, vault.PubKey)
			c.Assert(err, IsNil)
			c.Assert(got.GetCoin(common.XMRAsset).Amount.Uint64(), Equals, amount)
			c.Assert(got.Deficit.IsEmpty(), Equals, true)
		}
		mined := out.Tx
		mined.XmrTxKey = "0100000000000000000000000000000000000000000000000000000000000000"
		premine := mined
		premine.XmrMinedHeight = 0
		premine.BlockHeight, premine.FinaliseHeight = 90, 90
		deliverObservation(premine, true)
		assertVault(899)
		ctx = ctx.WithBlockHeight(1001)
		if minedArrived {
			deliverObservation(mined, false)
		}
		selected, err := mgr.Keeper().GetObservedTxOutVoter(ctx, out.TxID)
		c.Assert(err, IsNil)
		c.Assert(selected.Tx.IsFinal(), Equals, true)
		c.Assert(selected.Tx.XmrMinedHeight, Equals, int64(0))
		c.Assert(selected.FinalisedHeight, Equals, int64(1000))
		variants := 1
		if minedArrived {
			variants = 2
		}
		c.Assert(selected.Txs, HasLen, variants)
		assertVault(899)

		registry := codectypes.NewInterfaceRegistry()
		types.RegisterInterfaces(registry)
		eb := ebifrost.NewEnshrinedBifrost(codec.NewProtoCodec(registry), log.NewNopLogger(), ebifrost.EBifrostConfig{Enable: true})
		msg := newSignedErrataQuorumMessage(c, makeErrataTx(out.TxID, common.XMRChain), nodes, keys[:2]...)
		_, err = eb.SendQuorumErrataTx(context.Background(), msg.QuoErrata)
		c.Assert(err, IsNil)
		raw, _ := eb.ProposalInjectTxs(ctx, 1<<20)
		injected, err := eb.GetInjectedMsgs(ctx, raw)
		c.Assert(err, IsNil)
		c.Assert(injected, HasLen, 1)
		result, ok := injected[0].(*types.MsgErrataTxQuorum)
		c.Assert(ok, Equals, true)
		c.Assert(runErrataQuorum(ctx, mgr, result), IsNil)
		c.Assert(result.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
		// This is the post-decorator's success-only cache confirmation.
		eb.MarkQuorumErrataTxAttestationsConfirmed(ctx, result.QuoErrata)
		assertVault(999)
		selected, err = mgr.Keeper().GetObservedTxOutVoter(ctx, out.TxID)
		c.Assert(err, IsNil)
		c.Assert(selected.Reverted, Equals, true)
		saved, err := mgr.Keeper().GetErrataTxVoter(ctx, out.TxID, common.XMRChain)
		c.Assert(err, IsNil)
		c.Assert(saved.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
		c.Assert(saved.BlockHeight, Equals, int64(1001))
		for _, height := range []int64{1002, 1003} {
			ctx = ctx.WithBlockHeight(height)
			c.Assert(runErrataQuorum(ctx, mgr, msg), IsNil)
			c.Assert(msg.QuoErrata.Outcome, Equals, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED)
			_, err = eb.SendQuorumErrataTx(context.Background(), msg.QuoErrata)
			c.Assert(err, IsNil)
			raw, _ = eb.ProposalInjectTxs(ctx, 1<<20)
			c.Assert(raw, HasLen, 0)
			assertVault(999)
		}
		// Errata restores principal, preserving the single gas debit.
		c.Assert(mgr.GasMgr().GetGas().ToCoins().GetCoin(common.XMRAsset).Amount.Uint64(), Equals, uint64(1))
		mgr.GasMgr().EndBlock(ctx, mgr.Keeper(), mgr.EventMgr())
		pool, err = mgr.Keeper().GetPool(ctx, common.XMRAsset)
		c.Assert(err, IsNil)
		c.Assert(pool.BalanceAsset.Uint64(), Equals, uint64(9999))
		c.Assert(pool.BalanceRune.Uint64(), Equals, uint64(10001))
	}
}

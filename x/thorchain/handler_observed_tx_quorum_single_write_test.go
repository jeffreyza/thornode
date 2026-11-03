package thorchain

import (
	"bytes"

	storetypes "cosmossdk.io/store/types"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerObservedTxQuorumSingleWriteSuite struct{}

var _ = Suite(&HandlerObservedTxQuorumSingleWriteSuite{})

// handleObservedTxQuorumPerAttestationWrite is the quorum handler as it was
// before it saved the voter once per message: processTx*Attestation saves the
// voter after every attestation. signers are the attestations' verified
// signers, in message order.
func handleObservedTxQuorumPerAttestationWrite(c *C, ctx cosmos.Context, mgr *Mgrs, msg *types.MsgObservedTxQuorum, signers []cosmos.AccAddress) {
	k := mgr.Keeper()
	nas, err := k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	obsTx := msg.QuoTx.ObsTx
	inbound := msg.QuoTx.Inbound

	var voter ObservedTxVoter
	if inbound {
		voter, err = ensureVaultAndGetTxInVoter(ctx, obsTx.ObservedPubKey, obsTx.Tx.ID, k)
	} else {
		voter, err = ensureVaultAndGetTxOutVoter(ctx, k, obsTx.ObservedPubKey, obsTx.Tx.ID, signers, obsTx.KeysignMs)
	}
	c.Assert(err, IsNil)

	var crossedQuorum bool
	for _, signer := range signers {
		var isQuorum bool
		if inbound {
			voter, isQuorum = processTxInAttestation(ctx, mgr, voter, nas, obsTx, signer, false)
		} else {
			voter, isQuorum = processTxOutAttestation(ctx, mgr, voter, nas, obsTx, signer, false)
		}
		crossedQuorum = crossedQuorum || isQuorum
	}

	handler := NewInternalHandler(mgr)
	if inbound {
		err = handleObservedTxInQuorum(ctx, mgr, msg.Signer, nas, handler, obsTx, voter, signers, crossedQuorum)
	} else {
		err = handleObservedTxOutQuorum(ctx, mgr, msg.Signer, nas, handler, obsTx, voter, signers, crossedQuorum)
	}
	c.Assert(err, IsNil)
}

// assertSameThorchainStore checks both branches hold identical thorchain state.
func assertSameThorchainStore(c *C, a, b cosmos.Context) {
	itA := a.KVStore(keyThorchain).Iterator(nil, nil)
	defer itA.Close()
	itB := b.KVStore(keyThorchain).Iterator(nil, nil)
	defer itB.Close()
	n := 0
	for ; itA.Valid() && itB.Valid(); itA.Next() {
		c.Assert(bytes.Equal(itA.Key(), itB.Key()), Equals, true, Commentf("key %d: %q vs %q", n, itA.Key(), itB.Key()))
		c.Assert(bytes.Equal(itA.Value(), itB.Value()), Equals, true, Commentf("value of key %q", itA.Key()))
		itB.Next()
		n++
	}
	c.Assert(itA.Valid(), Equals, false, Commentf("extra key in single-write store after %d keys", n))
	c.Assert(itB.Valid(), Equals, false, Commentf("extra key in per-attestation store after %d keys", n))
}

// TestSingleWriteMatchesPerAttestationWrite runs the same sequence of quorum
// messages through the handler and through the per-attestation-write flow it
// replaced: below quorum, crossing quorum with a duplicate, a late signer, and a
// fully duplicate message. Both must leave identical state after every message;
// only the gas spent rewriting the voter differs.
func (s *HandlerObservedTxQuorumSingleWriteSuite) TestSingleWriteMatchesPerAttestationWrite(c *C) {
	for _, inbound := range []bool{true, false} {
		ctx, mgr := setupManagerForTest(c)

		nas := make([]NodeAccount, 7)
		keys := make([]secp256k1.PrivKey, 7)
		for i := range nas {
			nas[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}

		vaultPubKey := GetRandomPubKey()
		vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{common.ETHChain.String()}, nil)
		c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
		vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
		c.Assert(err, IsNil)

		tx := common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		}
		if !inbound {
			tx.FromAddress, tx.ToAddress = vaultAddr, GetRandomETHAddress()
			tx.Memo = "OUT:" + GetRandomTxHash().String()
		}
		obsTx := common.NewObservedTx(tx, 1024, vaultPubKey, 1024)

		steps := [][]int{
			{0, 1, 2},    // below quorum (5 of 7)
			{2, 3, 4, 5}, // 2 is a duplicate; crosses quorum
			{6},          // late signer
			{0, 1},       // all duplicates
		}

		ctxNew, _ := ctx.CacheContext()
		ctxOld, _ := ctx.CacheContext()
		handler := NewObservedTxQuorumHandler(mgr)
		for i, step := range steps {
			atts := make([]*common.Attestation, len(step))
			signers := make([]cosmos.AccAddress, len(step))
			for j, n := range step {
				atts[j] = attestObservedTx(c, keys[n], obsTx, inbound)
				signers[j] = nas[n].NodeAddress
			}
			msg := types.NewMsgObservedTxQuorum(&common.QuorumTx{
				ObsTx:        obsTx,
				Inbound:      inbound,
				Attestations: atts,
			}, nas[0].NodeAddress)

			ctxNew = ctxNew.WithGasMeter(storetypes.NewInfiniteGasMeter())
			ctxOld = ctxOld.WithGasMeter(storetypes.NewInfiniteGasMeter())
			_, err = handler.handle(ctxNew, *msg)
			c.Assert(err, IsNil)
			handleObservedTxQuorumPerAttestationWrite(c, ctxOld, mgr, msg, signers)

			assertSameThorchainStore(c, ctxNew, ctxOld)
			c.Assert(ctxNew.GasMeter().GasConsumed() <= ctxOld.GasMeter().GasConsumed(), Equals, true,
				Commentf("inbound=%v step %d: gas %d > %d", inbound, i, ctxNew.GasMeter().GasConsumed(), ctxOld.GasMeter().GasConsumed()))
		}

		var voter ObservedTxVoter
		if inbound {
			voter, err = mgr.Keeper().GetObservedTxInVoter(ctxNew, obsTx.Tx.ID)
		} else {
			voter, err = mgr.Keeper().GetObservedTxOutVoter(ctxNew, obsTx.Tx.ID)
		}
		c.Assert(err, IsNil)
		c.Assert(voter.Height, Not(Equals), int64(0), Commentf("inbound=%v: voter never reached consensus", inbound))
		c.Assert(voter.Tx.Signers, HasLen, 7, Commentf("inbound=%v: late signer not credited", inbound))
	}
}

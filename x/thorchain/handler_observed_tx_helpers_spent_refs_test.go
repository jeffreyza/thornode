package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type SpentOutputRefsFlowSuite struct{}

var _ = Suite(&SpentOutputRefsFlowSuite{})

// Test 4: spent output refs accumulate in the bounded XMR store, keyed by the
// FROST pubkey used by XMR outbound observations.
func (s *SpentOutputRefsFlowSuite) TestSpentRefsAccumulateInKeeperStore(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ecdsaPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	vault := NewVaultV2(1, ActiveVault, AsgardVault, ecdsaPubKey, []string{common.XMRChain.String()}, nil, eddsaPubKey)
	vault.PubKeyFrost = frostPubKey
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Verify frost index was created — GetVault by frost key must resolve.
	resolved, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(resolved.PubKey.Equals(ecdsaPubKey), Equals, true,
		Commentf("GetVault by frost key should resolve to ECDSA-keyed vault"))

	// Verify VaultExists works for all three key types.
	c.Assert(k.VaultExists(ctx, ecdsaPubKey), Equals, true)
	c.Assert(k.VaultExists(ctx, eddsaPubKey), Equals, true)
	c.Assert(k.VaultExists(ctx, frostPubKey), Equals, true)

	// Simulate an outbound observation keyed by the frost pubkey (as XMR does).
	refs := []common.OutputRef{
		{TxHash: "aaa", OutputIndex: 0, KeyImage: "ki_aaa", SpendTxHash: "spend_tx"},
		{TxHash: "bbb", OutputIndex: 1, KeyImage: "ki_bbb", SpendTxHash: "spend_tx"},
	}

	c.Assert(k.SetXMRSpentOutputRefs(ctx, frostPubKey, common.TxID("spend_tx"), refs, 10, 10), IsNil)

	// Verify refs were stored.
	updated, err := k.GetXMRSpentOutputRefs(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(updated), Equals, 2)

	// Apply again with a duplicate and a new ref — dedup should work.
	moreRefs := []common.OutputRef{
		{TxHash: "aaa", OutputIndex: 0, KeyImage: "ki_aaa", SpendTxHash: "spend_tx"},
		{TxHash: "ccc", OutputIndex: 2, KeyImage: "ki_ccc", SpendTxHash: "spend_tx"},
	}
	c.Assert(k.SetXMRSpentOutputRefs(ctx, frostPubKey, common.TxID("spend_tx"), moreRefs, 11, 11), IsNil)

	final, err := k.GetXMRSpentOutputRefs(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(final), Equals, 3, Commentf("duplicate 'aaa' should be deduped"))
}

// Test 5: spent output refs are read from the dedicated XMR store without
// inflating vault state.
func (s *SpentOutputRefsFlowSuite) TestSpentRefsCatchupFromKeeperStore(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ecdsaPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	vault := NewVaultV2(1, ActiveVault, AsgardVault, ecdsaPubKey, []string{common.XMRChain.String()}, nil, eddsaPubKey)
	vault.PubKeyFrost = frostPubKey
	refs := []common.OutputRef{
		{TxHash: "spent1", OutputIndex: 0, KeyImage: "ki_1", SpendTxHash: "spend_tx"},
		{TxHash: "spent2", OutputIndex: 1, KeyImage: "ki_2", SpendTxHash: "spend_tx"},
		{TxHash: "spent3", OutputIndex: 2, KeyImage: "ki_3", SpendTxHash: "spend_tx"},
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)
	c.Assert(k.SetXMRSpentOutputRefs(ctx, frostPubKey, common.TxID("spend_tx"), refs, 10, 10), IsNil)

	got, err := k.GetXMRSpentOutputRefs(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(len(got), Equals, 3)

	// Verify individual refs match.
	want := make(map[string]uint32, len(refs))
	for _, ref := range refs {
		want[ref.TxHash] = ref.OutputIndex
	}
	for _, ref := range got {
		c.Assert(ref.OutputIndex, Equals, want[ref.TxHash])
	}
}

// Verify that the frost index is created when SetVault is called with a
// vault that has PubKeyFrost set. There is no lazy migration in GetVault to
// avoid non-deterministic state writes during read paths — the index is
// created the next time SetVault is called for the vault.
func (s *SpentOutputRefsFlowSuite) TestFrostIndexCreatedBySetVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()

	ecdsaPubKey := GetRandomPubKey()
	eddsaPubKey := GetRandomPubKey()
	frostPubKey := GetRandomPubKey()

	// Create vault without frost key first (simulates pre-frost vault).
	vault := NewVaultV2(1, ActiveVault, AsgardVault, ecdsaPubKey, []string{common.XMRChain.String()}, nil, eddsaPubKey)
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Frost lookup should fail since no frost key was set.
	c.Assert(k.VaultExists(ctx, frostPubKey), Equals, false)

	// Now update the vault to add the frost key (simulates keygen completing).
	vault.PubKeyFrost = frostPubKey
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Frost lookup should now work via the index created by SetVault.
	c.Assert(k.VaultExists(ctx, frostPubKey), Equals, true)

	resolved, err := k.GetVault(ctx, frostPubKey)
	c.Assert(err, IsNil)
	c.Assert(resolved.PubKey.Equals(ecdsaPubKey), Equals, true)
}

func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumPersistsLateSuperMajority(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}

	minority := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0])
	voter := NewObservedTxVoter(txID, []common.ObservedTx{minority})
	voter.Height = 10
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, minority, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)

	lateSuperMajority := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[1], nas[2])
	voter.Txs = append(voter.Txs, lateSuperMajority)
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, lateSuperMajority, nas)

	stored, err = mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, len(refs))

	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, lateSuperMajority, nas)
	storedAgain, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(storedAgain, HasLen, len(refs))
	c.Assert(common.SpentOutputRefsEqual(storedAgain, refs), Equals, true)
}

func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumSkipsNonXMR(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	tx := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0], nas[1], nas[2])
	tx.Tx.Chain = common.ETHChain
	voter := NewObservedTxVoter(txID, []common.ObservedTx{tx})
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, tx, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)
}

func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumRequiresRefBearingObservation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	withoutRefs := xmrSpentRefsObservedTx(txID, vaultPubKey, nil, nas[0], nas[1], nas[2])
	withRefs := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0], nas[1], nas[2])
	voter := NewObservedTxVoter(txID, []common.ObservedTx{withoutRefs, withRefs})
	voter.Height = 10
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, withoutRefs, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)
}

func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumRequiresMatchingRefSet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refsA := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}
	refsB := []common.OutputRef{
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}

	obsA := xmrSpentRefsObservedTx(txID, vaultPubKey, refsA, nas[0], nas[1])
	obsB := xmrSpentRefsObservedTx(txID, vaultPubKey, refsB, nas[2], nas[3])
	voter := NewObservedTxVoter(txID, []common.ObservedTx{obsA, obsB})
	voter.Height = 10
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, obsA, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)
}

// This preserves late ref catch-up when the consensus outbound was formed from
// observations that did not yet carry refs.
func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumMatchesConsensusWithoutRefs(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
		{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: txID.String()},
	}

	consensus := xmrSpentRefsObservedTx(txID, vaultPubKey, nil, nas[0])
	// The first observation has no refs. Two more nodes agree on the outbound,
	// but their ref set has not yet reached its own supermajority.
	lateWithRefs := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[1], nas[2])
	voter := NewObservedTxVoter(txID, []common.ObservedTx{consensus, lateWithRefs})
	voter.Height = 10
	c.Assert(voter.GetTx(nas).IsEmpty(), Equals, false)
	c.Assert(voter.GetTx(nas).SpentOutputRefs, HasLen, 0)
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, lateWithRefs, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)

	// With all four nodes, the selected consensus observation still has no
	// refs, while the three ref-bearing nodes can persist their complete set.
	lateWithRefs = xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[1], nas[2], nas[3])
	voter.Txs[1] = lateWithRefs
	c.Assert(voter.GetTx(nas).SpentOutputRefs, HasLen, 0)
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, lateWithRefs, nas)
	stored, err = mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, len(refs))
	c.Assert(common.SpentOutputRefsEqual(stored, refs), Equals, true)
}

func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumIgnoresObservationHeights(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	consensus := xmrSpentRefsObservedTx(txID, vaultPubKey, nil, nas[0], nas[1], nas[2], nas[3])
	consensus.BlockHeight = 1354
	consensus.FinaliseHeight = 1354

	refA := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0])
	refA.BlockHeight = 1337
	refA.FinaliseHeight = 1337
	refB := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[1])
	refB.BlockHeight = 1338
	refB.FinaliseHeight = 1338
	refC := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[2])
	refC.BlockHeight = 1339
	refC.FinaliseHeight = 1339

	voter := NewObservedTxVoter(txID, []common.ObservedTx{consensus, refA, refB, refC})
	voter.Height = 10
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, refA, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, len(refs))
	c.Assert(common.SpentOutputRefsEqual(stored, refs), Equals, true)
}

// Ref quorum must not persist refs for a different outbound shape, even when a
// supermajority reports matching refs.
func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumRequiresConsensusOutboundMatch(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	consensus := xmrSpentRefsObservedTx(txID, vaultPubKey, nil, nas[0], nas[1], nas[2])
	mismatched := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0], nas[1], nas[2])
	// Same tx id/signers are not enough: refs cannot be persisted for an
	// outbound shape that differs from the already-consensed outbound.
	mismatched.Tx.ToAddress = common.Address("xmr-different-to")
	voter := NewObservedTxVoter(txID, []common.ObservedTx{consensus, mismatched})
	voter.Height = 10
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, mismatched, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)
}

// Duplicate signer entries in ref-bearing observations must not inflate quorum.
func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumDoesNotCountDuplicateSigner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	consensus := xmrSpentRefsObservedTx(txID, vaultPubKey, nil, nas[0], nas[1], nas[2])
	// A repeated signer must still count once toward the ref quorum.
	duplicateSigner := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0], nas[0], nas[0])
	voter := NewObservedTxVoter(txID, []common.ObservedTx{consensus, duplicateSigner})
	voter.Height = 10
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, duplicateSigner, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)
}

// Ref quorum is based on the active node account set, not arbitrary signers on
// the observation.
func (s *SpentOutputRefsFlowSuite) TestSaveXMRSpentOutputRefsWithQuorumDoesNotCountNonActiveSigner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nas := xmrSpentRefsActiveNodes()
	nonActive := GetRandomValidatorNode(NodeActive)
	for nas.IsNodeKeys(nonActive.NodeAddress) {
		nonActive = GetRandomValidatorNode(NodeActive)
	}
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	consensus := xmrSpentRefsObservedTx(txID, vaultPubKey, nil, nas[0], nas[1], nas[2])
	// Only current active node keys count toward spent-ref persistence quorum.
	withNonActive := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0], nas[1], nonActive)
	voter := NewObservedTxVoter(txID, []common.ObservedTx{consensus, withNonActive})
	voter.Height = 10
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, withNonActive, nas)

	stored, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(stored, HasLen, 0)
}

func (s *SpentOutputRefsFlowSuite) TestHandleObservedTxOutQuorumSavesLateXMRSpentRefs(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	nas := xmrSpentRefsActiveNodes()
	vaultPubKey := GetRandomPubKey()
	txID := GetRandomTxHash()
	refs := []common.OutputRef{
		{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: txID.String()},
	}

	early := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[0])
	late := xmrSpentRefsObservedTx(txID, vaultPubKey, refs, nas[1], nas[2])
	voter := NewObservedTxVoter(txID, []common.ObservedTx{early, late})
	voter.Height = 99

	err := handleObservedTxOutQuorum(ctx, mgr, nas[0].NodeAddress, nas, nil, late, voter, nil, false)
	c.Assert(err, IsNil)

	stored, getErr := mgr.Keeper().GetXMRSpentOutputRefs(ctx, vaultPubKey)
	c.Assert(getErr, IsNil)
	c.Assert(stored, HasLen, len(refs))
	c.Assert(common.SpentOutputRefsEqual(stored, refs), Equals, true)
}

func xmrSpentRefsActiveNodes() NodeAccounts {
	return NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
}

func xmrSpentRefsObservedTx(txID common.TxID, vaultPubKey common.PubKey, refs []common.OutputRef, signers ...NodeAccount) common.ObservedTx {
	tx := common.NewObservedTx(common.Tx{
		ID:          txID,
		Chain:       common.XMRChain,
		FromAddress: common.Address("xmr-from"),
		ToAddress:   common.Address("xmr-to"),
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000))),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1000))},
		Memo:        "OUT:xmr-spent-refs",
	}, 100, vaultPubKey, 100)
	tx.XmrTxKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tx.SpentOutputRefs = refs
	for _, signer := range signers {
		tx.Signers = append(tx.Signers, signer.NodeAddress.String())
	}
	return tx
}

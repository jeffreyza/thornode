package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type SolVaultNonceSuite struct{}

var _ = Suite(&SolVaultNonceSuite{})

// solNonceFixture is a SOL vault with its seed-derived durable-nonce account.
type solNonceFixture struct {
	vault     Vault
	solAddr   common.Address
	nonceAcct string
	nas       NodeAccounts
}

func newSolNonceFixture(c *C, ctx cosmos.Context, mgr Manager) solNonceFixture {
	eddsa := types.GetRandomEd25519PubKey()
	vault := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.SOLChain.String()}, nil, eddsa)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	solAddr, err := eddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	nonceAcct, err := common.SolNonceAccountAddress(solAddr.String())
	c.Assert(err, IsNil)
	return solNonceFixture{
		vault: vault, solAddr: solAddr, nonceAcct: nonceAcct,
		nas: NodeAccounts{
			GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive),
			GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive),
		},
	}
}

// solNonceValue returns a 32-byte base58 value usable as a stored nonce.
func solNonceValue(c *C) string {
	addr, err := types.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	return addr.String()
}

// obs builds an observation of vault tx txID carrying update u, signed by signers.
// final selects a final (finalise == block height) or non-final observation.
func (f solNonceFixture) obs(txID common.TxID, u *common.SolNonceUpdate, final bool, signers ...NodeAccount) common.ObservedTx {
	finalise := int64(100)
	if !final {
		finalise = 105
	}
	tx := common.NewObservedTx(common.Tx{
		ID:          txID,
		Chain:       common.SOLChain,
		FromAddress: f.solAddr,
		ToAddress:   common.Address("sol-dest"),
		Coins:       common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(100000000))),
		Gas:         common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(5000))},
		Memo:        "OUT:sol-durable-nonce",
	}, 100, f.vault.PubKeyEddsa, finalise)
	tx.SolNonce = u
	for _, s := range signers {
		tx.Signers = append(tx.Signers, s.NodeAddress.String())
	}
	return tx
}

// finalised marks the voter's transaction as having reached final consensus on its
// first observation, as processTxOutAttestation does.
func finalised(voter ObservedTxVoter) ObservedTxVoter {
	voter.FinalisedHeight = 100
	voter.Tx = voter.Txs[0]
	return voter
}

func (s *SolVaultNonceSuite) TestCommitsOnlyOnFinalIdenticalSuperMajority(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}

	// 2 of 4 final: below supermajority (3) -> nothing committed.
	voter := NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1])})
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(voter), f.nas)
	_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	// A NON-final third vote does not count toward the nonce supermajority.
	voter.Txs = append(voter.Txs, f.obs(txID, u, false, f.nas[2]))
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(voter), f.nas)
	_, found, err = mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)

	// A late FINAL third vote completes it -> committed, keyed by the canonical vault
	// pubkey even though the observation used the EdDSA key.
	voter.Txs = append(voter.Txs, f.obs(txID, u, true, f.nas[2]))
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(voter), f.nas)
	got, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(got.VaultPubKey.Equals(f.vault.PubKey), Equals, true)
	c.Assert(got.NonceAccount, Equals, f.nonceAcct)
	c.Assert(got.Nonce, Equals, u.Nonce)
	c.Assert(got.Slot, Equals, int64(1000))
	c.Assert(got.UpdatedHeight, Equals, ctx.BlockHeight())
	c.Assert(got.SetByTx.Equals(txID), Equals, true)
}

func (s *SolVaultNonceSuite) TestSplitValuesDoNotCommit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	txID := GetRandomTxHash()
	a := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
	b := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}

	// 2 attest a, 2 attest b: no value has a supermajority, so nothing is committed
	// (there is no "absent/other matches" pooling).
	voter := NewObservedTxVoter(txID, []common.ObservedTx{
		f.obs(txID, a, true, f.nas[0], f.nas[1]),
		f.obs(txID, b, true, f.nas[2], f.nas[3]),
	})
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(voter), f.nas)
	_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
}

func (s *SolVaultNonceSuite) TestRejectsForeignNonceAccount(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	txID := GetRandomTxHash()
	// A supermajority attests an account that is NOT the vault's seed-derived one.
	u := &common.SolNonceUpdate{NonceAccount: solNonceValue(c), Nonce: solNonceValue(c), Slot: 1000}
	voter := NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2])})
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(voter), f.nas)
	_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
}

func (s *SolVaultNonceSuite) TestSlotMonotonicAndIdempotent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	commit := func(slot int64, nonce string) {
		txID := GetRandomTxHash()
		u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: nonce, Slot: slot}
		voter := NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2])})
		saveSolVaultNonceWithQuorum(ctx, mgr, finalised(voter), f.nas)
	}
	first := solNonceValue(c)
	commit(1000, first)

	// An older slot never overwrites.
	commit(900, solNonceValue(c))
	got, _, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(got.Nonce, Equals, first)

	// Re-committing the same slot is a no-op (keeps the original updated_height).
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 5)
	commit(1000, solNonceValue(c))
	again, _, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(again.Nonce, Equals, first)
	c.Assert(again.UpdatedHeight, Equals, got.UpdatedHeight)

	// A newer slot advances it and records the new commit height.
	next := solNonceValue(c)
	commit(2000, next)
	adv, _, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(adv.Nonce, Equals, next)
	c.Assert(adv.Slot, Equals, int64(2000))
	c.Assert(adv.UpdatedHeight, Equals, ctx.BlockHeight())
}

func (s *SolVaultNonceSuite) TestQueryEndpoint(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	qs := queryServer{mgr: mgr}

	// No record -> empty response, not an error.
	empty, err := qs.querySolVaultNonce(ctx, &types.QuerySolVaultNonceRequest{VaultPubKey: f.vault.PubKey.String()})
	c.Assert(err, IsNil)
	c.Assert(empty.Nonce, Equals, "")

	nonce := solNonceValue(c)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: nonce, Slot: 1000}
	voter := NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2])})
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(voter), f.nas)

	// Readable by the canonical key and by the vault's EdDSA key.
	for _, pk := range []common.PubKey{f.vault.PubKey, f.vault.PubKeyEddsa} {
		resp, qErr := qs.querySolVaultNonce(ctx, &types.QuerySolVaultNonceRequest{VaultPubKey: pk.String()})
		c.Assert(qErr, IsNil)
		c.Assert(resp.Nonce, Equals, nonce)
		c.Assert(resp.NonceAccount, Equals, f.nonceAcct)
		c.Assert(resp.Slot, Equals, int64(1000))
		c.Assert(resp.SetByTx, Equals, txID.String())
	}
}

// A late final vote that completes the nonce supermajority after the outbound already
// reached consensus (isQuorum == false) still commits the nonce.
func (s *SolVaultNonceSuite) TestLateFinalVoteCommitsViaHandler(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
	late := f.obs(txID, u, true, f.nas[2])
	voter := NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1]), late})
	voter.Height = ctx.BlockHeight() - 5 // outbound consensus happened earlier
	voter.FinalisedHeight = voter.Height
	voter.Tx = late
	handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		c.Fatal("no outbound processing without quorum")
		return nil, nil
	}
	c.Assert(handleObservedTxOutQuorum(ctx, mgr, f.nas[2].NodeAddress, f.nas, handler, late, voter, nil, false), IsNil)
	got, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(got.Nonce, Equals, u.Nonce)
}

// Identical final nonce votes do not commit while the transaction itself has no final
// consensus: the nonce may only advance once LackSigning can see the consuming tx.
func (s *SolVaultNonceSuite) TestNoCommitWithoutFinalTxConsensus(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
	notCommitted := func(voter ObservedTxVoter) {
		saveSolVaultNonceWithQuorum(ctx, mgr, voter, f.nas)
		_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(found, Equals, false)
	}

	// Supermajority of identical final nonce votes, but the tx is not finalised yet
	// (voter.Tx set from a first, non-final consensus).
	voter := NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2])})
	voter.Tx = voter.Txs[0]
	notCommitted(voter)

	// Finalised, but the nonce supermajority comes from votes on a DIFFERENT payload
	// than the final consensus tx: only the one matching vote counts.
	match := f.obs(txID, u, true, f.nas[0])
	other := f.obs(txID, u, true, f.nas[1], f.nas[2], f.nas[3])
	other.Tx.ToAddress = common.Address("sol-elsewhere")
	voter = NewObservedTxVoter(txID, []common.ObservedTx{match, other})
	voter.FinalisedHeight = 100
	voter.Tx = match
	notCommitted(voter)
}

// An observation naming this vault but sent from another address (whose derived
// nonce account it attests) is not this vault's nonce.
func (s *SolVaultNonceSuite) TestRejectsForeignFromAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	other, err := types.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	otherNonce, err := common.SolNonceAccountAddress(other.String())
	c.Assert(err, IsNil)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: otherNonce, Nonce: solNonceValue(c), Slot: 1000}
	obs := f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2])
	obs.Tx.FromAddress = other
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(NewObservedTxVoter(txID, []common.ObservedTx{obs})), f.nas)
	_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
}

// Break-glass: the query (what signers read) ignores a record committed at or before
// the SOLNonceClear-<vault> height; a record committed after it re-anchors the vault.
func (s *SolVaultNonceSuite) TestBreakGlassClearAndReanchor(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	qs := queryServer{mgr: mgr}
	query := func() string {
		resp, err := qs.querySolVaultNonce(ctx, &types.QuerySolVaultNonceRequest{VaultPubKey: f.vault.PubKey.String()})
		c.Assert(err, IsNil)
		return resp.Nonce
	}
	first := solNonceValue(c)
	mgr.Keeper().SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: first, Slot: 1000, UpdatedHeight: ctx.BlockHeight(),
	})
	c.Assert(query(), Equals, first)

	key := fmt.Sprintf(constants.MimirTemplateSOLNonceClear, f.vault.PubKey.String())
	mgr.Keeper().SetMimir(ctx, key, ctx.BlockHeight()-1) // below the commit height: in force
	c.Assert(query(), Equals, first)
	mgr.Keeper().SetMimir(ctx, key, ctx.BlockHeight()) // at the commit height: voided
	c.Assert(query(), Equals, "")

	// A fresh nonce committed after the clear height re-anchors the vault.
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	next := solNonceValue(c)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: next, Slot: 2000}
	saveSolVaultNonceWithQuorum(ctx, mgr, finalised(NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2])})), f.nas)
	c.Assert(query(), Equals, next)
}

// A memoless outbound's consensus memo is rewritten to the recovered memo when the
// outbound is processed (voter.Tx is the stored consensus). Raw votes still carry the
// empty memo, so a late vote must still complete the nonce tally against them.
func (s *SolVaultNonceSuite) TestLateVoteCommitsAfterMemoRecovery(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
	memoless := func(signers ...NodeAccount) common.ObservedTx {
		tx := f.obs(txID, u, true, signers...)
		tx.Tx.Memo = ""
		return tx
	}
	omitting := memoless(f.nas[2])
	omitting.SolNonce = nil
	voter := NewObservedTxVoter(txID, []common.ObservedTx{memoless(f.nas[0], f.nas[1]), omitting})
	voter = finalised(voter)
	voter.Tx.Tx.Memo = "OUT:recovered" // what outbound processing persisted

	voter.Txs = append(voter.Txs, memoless(f.nas[3]))
	c.Assert(saveSolVaultNonceWithQuorum(ctx, mgr, voter, f.nas), Equals, true)
	got, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(got.Nonce, Equals, u.Nonce)
}

// Counted votes must still agree on the memo: a vote with a different memo does not
// join the others' tally.
func (s *SolVaultNonceSuite) TestVotesWithDifferentMemosDoNotCombine(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
	a := f.obs(txID, u, true, f.nas[0], f.nas[1])
	b := f.obs(txID, u, true, f.nas[2])
	b.Tx.Memo = "OUT:something-else"
	voter := finalised(NewObservedTxVoter(txID, []common.ObservedTx{a, b}))
	c.Assert(saveSolVaultNonceWithQuorum(ctx, mgr, voter, f.nas), Equals, false)
}

// The vote that crosses outbound quorum may itself omit SolNonce while the recorded
// votes already form an identical nonce supermajority: the tally reads every vote,
// so it must still run (quorum and late paths alike).
func (s *SolVaultNonceSuite) TestNonceCommitsWhenTriggeringVoteOmitsSolNonce(c *C) {
	for _, isQuorum := range []bool{true, false} {
		ctx, mgr := setupManagerForTest(c)
		f := newSolNonceFixture(c, ctx, mgr)
		txID := GetRandomTxHash()
		u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
		omitting := f.obs(txID, nil, true, f.nas[3])
		voter := finalised(NewObservedTxVoter(txID, []common.ObservedTx{f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2]), omitting}))
		voter.Height = ctx.BlockHeight()
		handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) { return &cosmos.Result{}, nil }
		_ = handleObservedTxOutQuorum(ctx, mgr, f.nas[3].NodeAddress, f.nas, handler, omitting, voter, nil, isQuorum)
		got, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(found, Equals, true, Commentf("isQuorum=%v", isQuorum))
		c.Assert(got.Nonce, Equals, u.Nonce)
	}
}

// A memoless SOL outbound gets its observed link from the memo recovered at quorum,
// so the durable-nonce landed-copy hold can find it (the per-vote link parsed "").
func (s *SolVaultNonceSuite) TestMemolessSolOutboundIsLinked(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)
	mgr.Keeper().SetMimir(ctx, constants.SigningTransactionPeriod.String(), 300)
	f := newSolNonceFixture(c, ctx, mgr)
	inHash := GetRandomTxHash()
	txID := GetRandomTxHash()
	obs := f.obs(txID, nil, true, f.nas[0], f.nas[1], f.nas[2])
	obs.Tx.Memo = ""
	item := TxOutItem{
		Chain: common.SOLChain, ToAddress: obs.Tx.ToAddress, VaultPubKey: f.vault.PubKey, VaultPubKeyEddsa: f.vault.PubKeyEddsa,
		Coin: obs.Tx.Coins[0], MaxGas: obs.Tx.Gas, Memo: "", OriginalMemo: "OUT:" + inHash.String(), InHash: inHash,
	}
	txOut := NewTxOut(100)
	txOut.TxArray = append(txOut.TxArray, item)
	c.Assert(mgr.Keeper().SetTxOut(ctx, txOut), IsNil)
	voter := finalised(NewObservedTxVoter(txID, []common.ObservedTx{obs}))
	handler := func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) { return &cosmos.Result{}, nil }
	_ = handleObservedTxOutQuorum(ctx, mgr, f.nas[0].NodeAddress, f.nas, handler, obs, voter, nil, true)

	linked := false
	for _, h := range mgr.Keeper().GetObservedLink(ctx, inHash) {
		linked = linked || h.Equals(txID)
	}
	c.Assert(linked, Equals, true)
}

// reissueFixture: a vault with an ACTIVE committed nonce at prevHeight and a TxOut
// block of pending items issued after it, plus the observation consuming that nonce.
type reissueFixture struct {
	solNonceFixture
	ctx       cosmos.Context
	mgr       Manager
	obs       common.ObservedTx
	issuedAt  int64
	x, y, z   TxOutItem // x: consumed by obs; y: waiter on the vault; z: other vault
	older     TxOutItem // issued before prevHeight
	migration TxOutItem
}

func newReissueFixture(c *C, withPrev bool) reissueFixture {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	f := newSolNonceFixture(c, ctx, mgr)
	const prevHeight = int64(900)
	if withPrev {
		mgr.Keeper().SetSolVaultNonce(ctx, SolVaultNonce{
			VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 500,
			UpdatedHeight: prevHeight, SetByTx: GetRandomTxHash(),
		})
	}
	dest := func() common.Address {
		a, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
		c.Assert(err, IsNil)
		return a
	}
	txID := GetRandomTxHash()
	u := &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
	obs := f.obs(txID, u, true, f.nas[0], f.nas[1], f.nas[2])
	obs.Tx.ToAddress = dest()
	item := func(to common.Address, vault Vault) TxOutItem {
		return TxOutItem{
			Chain: common.SOLChain, ToAddress: to, VaultPubKey: vault.PubKey, VaultPubKeyEddsa: vault.PubKeyEddsa,
			Coin: obs.Tx.Coins[0], MaxGas: obs.Tx.Gas, InHash: GetRandomTxHash(), Memo: "OUT:" + GetRandomTxHash().String(),
		}
	}
	other := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), []string{common.SOLChain.String()}, nil, GetRandomEd25519PubKey())
	r := reissueFixture{solNonceFixture: f, ctx: ctx, mgr: mgr, obs: obs, issuedAt: prevHeight + 50}
	r.x = item(obs.Tx.ToAddress, f.vault)
	r.y = item(dest(), f.vault)
	r.z = item(dest(), other)
	r.migration = item(dest(), f.vault)
	r.migration.Memo = "MIGRATE:1"
	r.older = item(dest(), f.vault)
	block := NewTxOut(r.issuedAt)
	block.TxArray = []TxOutItem{r.x, r.y, r.z, r.migration}
	c.Assert(mgr.Keeper().SetTxOut(ctx, block), IsNil)
	olderBlock := NewTxOut(prevHeight - 1)
	olderBlock.TxArray = []TxOutItem{r.older}
	c.Assert(mgr.Keeper().SetTxOut(ctx, olderBlock), IsNil)
	return r
}

func (r reissueFixture) commit(c *C) {
	voter := finalised(NewObservedTxVoter(r.obs.Tx.ID, []common.ObservedTx{r.obs}))
	c.Assert(saveSolVaultNonceWithQuorum(r.ctx, r.mgr, voter, r.nas), Equals, true)
}

// reissued lists the items re-added at the current height.
func (r reissueFixture) reissued(c *C) []TxOutItem {
	out, err := r.mgr.Keeper().GetTxOut(r.ctx, r.ctx.BlockHeight())
	c.Assert(err, IsNil)
	return out.TxArray
}

func (r reissueFixture) outHashAt(c *C, height int64, inHash common.TxID) common.TxID {
	out, err := r.mgr.Keeper().GetTxOut(r.ctx, height)
	c.Assert(err, IsNil)
	for _, it := range out.TxArray {
		if it.InHash.Equals(inHash) {
			return it.OutHash
		}
	}
	c.Fatalf("item %s not found at %d", inHash, height)
	return ""
}

// When the vault's nonce advances past an active commit, a waiter issued since that
// commit is re-issued at once on the same vault (it can never sign at its own height).
// The item the consuming tx fulfils, other vaults' items, items issued before the
// previous commit, and migrations are left alone.
func (s *SolVaultNonceSuite) TestNonceAdvanceReissuesWaiters(c *C) {
	r := newReissueFixture(c, true)
	r.commit(c)

	re := r.reissued(c)
	c.Assert(re, HasLen, 1)
	c.Assert(re[0].InHash.Equals(r.y.InHash), Equals, true)
	c.Assert(re[0].VaultPubKey.Equals(r.vault.PubKey), Equals, true)
	c.Assert(r.outHashAt(c, r.issuedAt, r.y.InHash).Equals(common.BlankTxID), Equals, true)
	c.Assert(r.outHashAt(c, r.issuedAt, r.x.InHash).IsEmpty(), Equals, true)
	c.Assert(r.outHashAt(c, r.issuedAt, r.z.InHash).IsEmpty(), Equals, true)
	c.Assert(r.outHashAt(c, r.issuedAt, r.migration.InHash).IsEmpty(), Equals, true)
	c.Assert(r.outHashAt(c, 899, r.older.InHash).IsEmpty(), Equals, true)
}

// No re-issue on a first commit or after a break-glass clear: items issued then were
// signed on the recent-blockhash path, whose copies can still land.
func (s *SolVaultNonceSuite) TestNoReissueWithoutActivePreviousCommit(c *C) {
	first := newReissueFixture(c, false)
	first.commit(c)
	c.Assert(first.reissued(c), HasLen, 0)

	cleared := newReissueFixture(c, true)
	cleared.mgr.Keeper().SetMimir(cleared.ctx, fmt.Sprintf(constants.MimirTemplateSOLNonceClear, cleared.vault.PubKey.String()), 900)
	cleared.commit(c)
	c.Assert(cleared.reissued(c), HasLen, 0)
	c.Assert(cleared.outHashAt(c, cleared.issuedAt, cleared.y.InHash).IsEmpty(), Equals, true)
}

// Outbounds to the vault's own address or its nonce account are signed with a recent
// blockhash even for a durable vault, so a consumed nonce does not kill them: they are
// never re-issued early.
func (s *SolVaultNonceSuite) TestReissueSkipsRecentBlockhashDestinations(c *C) {
	r := newReissueFixture(c, true)
	toNonce := r.y
	toNonce.InHash = GetRandomTxHash()
	toNonce.ToAddress = common.Address(r.nonceAcct)
	toVault := r.y
	toVault.InHash = GetRandomTxHash()
	toVault.ToAddress = r.solAddr
	block, err := r.mgr.Keeper().GetTxOut(r.ctx, r.issuedAt)
	c.Assert(err, IsNil)
	block.TxArray = append(block.TxArray, toNonce, toVault)
	c.Assert(r.mgr.Keeper().SetTxOut(r.ctx, block), IsNil)

	r.commit(c)
	re := r.reissued(c)
	c.Assert(re, HasLen, 1) // only the durable waiter y
	c.Assert(re[0].InHash.Equals(r.y.InHash), Equals, true)
	c.Assert(r.outHashAt(c, r.issuedAt, toNonce.InHash).IsEmpty(), Equals, true)
	c.Assert(r.outHashAt(c, r.issuedAt, toVault.InHash).IsEmpty(), Equals, true)
}

// An item with a finalised landed copy is never re-issued early (the LackSigning hold
// applies), and past MaxOutboundAttempts the item is left to LackSigning's recovery.
func (s *SolVaultNonceSuite) TestReissueRespectsLandedCopyAndAttemptBudget(c *C) {
	// landed copy of y
	r := newReissueFixture(c, true)
	paidHash := GetRandomTxHash()
	paid := r.obs
	paid.Tx.ID = paidHash
	paid.Tx.ToAddress = r.y.ToAddress
	paid.Tx.Memo = r.y.Memo
	r.mgr.Keeper().SetObservedLink(r.ctx, r.y.InHash, paidHash)
	r.mgr.Keeper().SetObservedTxOutVoter(r.ctx, ObservedTxVoter{TxID: paidHash, Tx: paid, Txs: []common.ObservedTx{paid}, FinalisedHeight: 950})
	r.commit(c)
	c.Assert(r.reissued(c), HasLen, 0)
	c.Assert(r.outHashAt(c, r.issuedAt, r.y.InHash).IsEmpty(), Equals, true)

	// attempt budget spent
	b := newReissueFixture(c, true)
	b.mgr.Keeper().SetMimir(b.ctx, constants.MaxOutboundAttempts.String(), 1)
	inVoter := NewObservedTxVoter(b.y.InHash, nil)
	inVoter.FinalisedHeight = b.ctx.BlockHeight() - 400 // > 1 signing period old
	b.mgr.Keeper().SetObservedTxInVoter(b.ctx, inVoter)
	b.commit(c)
	c.Assert(b.reissued(c), HasLen, 0)
}

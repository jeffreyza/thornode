package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"

	. "gopkg.in/check.v1"
)

type SolNonceAttestSuite struct{}

var _ = Suite(&SolNonceAttestSuite{})

type attestFixture struct {
	solNonceFixture
	ctx  cosmos.Context
	mgr  Manager
	h    SolNonceAttestHandler
	seed string // the vault's previously committed (stale) nonce
}

func newAttestFixture(c *C) attestFixture {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	for _, na := range f.nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	// Re-attestation repairs an existing record: seed an old, stale committed nonce.
	seed := solNonceValue(c)
	mgr.Keeper().SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: seed, Slot: 500,
		UpdatedHeight: ctx.BlockHeight() - solNonceReattestMinAgeBlocks,
	})
	return attestFixture{solNonceFixture: f, ctx: ctx, mgr: mgr, h: NewSolNonceAttestHandler(mgr), seed: seed}
}

func (f attestFixture) attest(na NodeAccount, nonce string, slot int64, lastTx common.TxID) error {
	msg := NewMsgSolNonceAttest(f.vault.PubKey, f.nonceAcct, nonce, slot, lastTx, na.NodeAddress)
	_, err := f.h.Run(f.ctx, msg)
	return err
}

// committed returns the vault's record and whether it was re-anchored (moved off the
// seeded stale value).
func (f attestFixture) committed(c *C) (SolVaultNonce, bool) {
	rec, found, err := f.mgr.Keeper().GetSolVaultNonce(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	return rec, found && rec.Nonce != f.seed
}

// A supermajority of identical attestations (slot included) re-anchors the vault's
// nonce at the attested slot, with last_tx as set_by_tx.
func (s *SolNonceAttestSuite) TestCommitsOnSupermajority(c *C) {
	f := newAttestFixture(c)
	nonce, lastTx := solNonceValue(c), GetRandomTxHash()
	c.Assert(f.attest(f.nas[0], nonce, 1100, lastTx), IsNil)
	c.Assert(f.attest(f.nas[1], nonce, 1100, lastTx), IsNil)
	_, found := f.committed(c)
	c.Assert(found, Equals, false)                           // 2 of 4
	c.Assert(f.attest(f.nas[1], nonce, 1100, lastTx), IsNil) // a repeat vote does not count twice
	_, found = f.committed(c)
	c.Assert(found, Equals, false)
	c.Assert(f.attest(f.nas[2], nonce, 1100, lastTx), IsNil)
	rec, found := f.committed(c)
	c.Assert(found, Equals, true)
	c.Assert(rec.Nonce, Equals, nonce)
	c.Assert(rec.NonceAccount, Equals, f.nonceAcct)
	c.Assert(rec.Slot, Equals, int64(1100))
	c.Assert(rec.SetByTx.Equals(lastTx), Equals, true)
	c.Assert(rec.UpdatedHeight, Equals, f.ctx.BlockHeight())
}

// Split values never pool into a supermajority.
func (s *SolNonceAttestSuite) TestSplitValuesDoNotCommit(c *C) {
	f := newAttestFixture(c)
	lastTx := GetRandomTxHash()
	a, b := solNonceValue(c), solNonceValue(c)
	c.Assert(f.attest(f.nas[0], a, 1000, lastTx), IsNil)
	c.Assert(f.attest(f.nas[1], a, 1000, lastTx), IsNil)
	c.Assert(f.attest(f.nas[2], b, 1000, lastTx), IsNil)
	c.Assert(f.attest(f.nas[3], b, 1000, lastTx), IsNil)
	_, found := f.committed(c)
	c.Assert(found, Equals, false)
}

// Replacing a committed value needs it to be at least the minimum age old, a
// different value, and a newer slot.
func (s *SolNonceAttestSuite) TestReplaceRules(c *C) {
	f := newAttestFixture(c)
	old := SolVaultNonce{VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000, UpdatedHeight: f.ctx.BlockHeight()}
	f.mgr.Keeper().SetSolVaultNonce(f.ctx, old)
	supermajority := func(f attestFixture, nonce string, slot int64, lastTx common.TxID) {
		for _, na := range f.nas[:3] {
			c.Assert(f.attest(na, nonce, slot, lastTx), IsNil)
		}
	}

	// Too young: not replaced; a later vote once old enough re-checks and commits.
	nonce, lastTx := solNonceValue(c), GetRandomTxHash()
	young := f
	young.ctx = f.ctx.WithBlockHeight(old.UpdatedHeight + solNonceReattestMinAgeBlocks - 1)
	supermajority(young, nonce, 2000, lastTx)
	rec, _ := f.committed(c)
	c.Assert(rec.Nonce, Equals, old.Nonce)
	ripe := f
	ripe.ctx = f.ctx.WithBlockHeight(old.UpdatedHeight + solNonceReattestMinAgeBlocks)
	c.Assert(ripe.attest(f.nas[3], nonce, 2000, lastTx), IsNil)
	rec, _ = f.committed(c)
	c.Assert(rec.Nonce, Equals, nonce)
	c.Assert(rec.UpdatedHeight, Equals, ripe.ctx.BlockHeight())

	// The same value as committed: nothing changes.
	later := f
	later.ctx = f.ctx.WithBlockHeight(rec.UpdatedHeight + solNonceReattestMinAgeBlocks)
	supermajority(later, nonce, 3000, GetRandomTxHash())
	again, _ := f.committed(c)
	c.Assert(again.UpdatedHeight, Equals, rec.UpdatedHeight)

	// An older (or equal) slot never replaces.
	supermajority(later, solNonceValue(c), rec.Slot, GetRandomTxHash())
	again, _ = f.committed(c)
	c.Assert(again.Nonce, Equals, nonce)
}

// A record voided by the break-glass mimir does not impose the minimum age.
func (s *SolNonceAttestSuite) TestVoidedRecordReanchorsImmediately(c *C) {
	f := newAttestFixture(c)
	f.mgr.Keeper().SetSolVaultNonce(f.ctx, SolVaultNonce{VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000, UpdatedHeight: f.ctx.BlockHeight()})
	f.mgr.Keeper().SetMimir(f.ctx, fmt.Sprintf(constants.MimirTemplateSOLNonceClear, f.vault.PubKey.String()), f.ctx.BlockHeight())
	nonce := solNonceValue(c)
	lastTx := GetRandomTxHash()
	for _, na := range f.nas[:3] {
		c.Assert(f.attest(na, nonce, 900, lastTx), IsNil)
	}
	rec, _ := f.committed(c)
	c.Assert(rec.Nonce, Equals, nonce)
}

// Only the vault's own seed-derived account, and only active validators.
func (s *SolNonceAttestSuite) TestRejectsForeignAccountAndNonValidators(c *C) {
	f := newAttestFixture(c)
	msg := NewMsgSolNonceAttest(f.vault.PubKey, solNonceValue(c), solNonceValue(c), 1000, GetRandomTxHash(), f.nas[0].NodeAddress)
	_, err := f.h.Run(f.ctx, msg)
	c.Assert(err, NotNil)

	outsider := GetRandomValidatorNode(NodeStandby)
	c.Assert(f.mgr.Keeper().SetNodeAccount(f.ctx, outsider), IsNil)
	c.Assert(f.attest(outsider, solNonceValue(c), 1000, GetRandomTxHash()), NotNil)
}

func (s *SolNonceAttestSuite) TestValidateBasic(c *C) {
	signer := GetRandomBech32Addr()
	good := func() *MsgSolNonceAttest {
		return NewMsgSolNonceAttest(GetRandomPubKey(), solNonceValue(c), solNonceValue(c), 1000, GetRandomTxHash(), signer)
	}
	c.Assert(good().ValidateBasic(), IsNil)
	for name, mutate := range map[string]func(*MsgSolNonceAttest){
		"empty vault":   func(m *MsgSolNonceAttest) { m.VaultPubKey = "" },
		"empty last tx": func(m *MsgSolNonceAttest) { m.LastTx = "" },
		"bad last tx":   func(m *MsgSolNonceAttest) { m.LastTx = "abc" },
		"bad account":   func(m *MsgSolNonceAttest) { m.NonceAccount = "not-base58-0OIl" },
		"bad nonce":     func(m *MsgSolNonceAttest) { m.Nonce = "" },
		"zero slot":     func(m *MsgSolNonceAttest) { m.Slot = 0 },
		"no signer":     func(m *MsgSolNonceAttest) { m.Signer = nil },
	} {
		m := good()
		mutate(m)
		c.Assert(m.ValidateBasic(), NotNil, Commentf(name))
	}
}

// Votes from signers that are no longer active validators do not count.
func (s *SolNonceAttestSuite) TestStaleSignersNotCounted(c *C) {
	f := newAttestFixture(c)
	nonce, lastTx := solNonceValue(c), GetRandomTxHash()
	gone := GetRandomValidatorNode(NodeStandby)
	voter, err := f.mgr.Keeper().GetSolNonceAttestVoter(f.ctx, f.vault.PubKey, f.nonceAcct, nonce, lastTx, 1000)
	c.Assert(err, IsNil)
	voter.Add(gone.NodeAddress.String())
	f.mgr.Keeper().SetSolNonceAttestVoter(f.ctx, voter)
	c.Assert(f.attest(f.nas[0], nonce, 1000, lastTx), IsNil)
	c.Assert(f.attest(f.nas[1], nonce, 1000, lastTx), IsNil)
	_, found := f.committed(c)
	c.Assert(found, Equals, false) // 2 active of 4 (+1 stale)
}

// One validator voting the honest value with a deviant (tiny) slot splits off into
// its own voter instead of poisoning the value: the honest supermajority commits.
func (s *SolNonceAttestSuite) TestDeviantSlotCannotBlockCommit(c *C) {
	f := newAttestFixture(c)
	nonce, lastTx := solNonceValue(c), GetRandomTxHash()
	f.mgr.Keeper().SetSolVaultNonce(f.ctx, SolVaultNonce{VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000, UpdatedHeight: f.ctx.BlockHeight() - solNonceReattestMinAgeBlocks})
	c.Assert(f.attest(f.nas[3], nonce, 1, lastTx), IsNil) // byzantine: same value, slot 1
	for _, na := range f.nas[:3] {
		c.Assert(f.attest(na, nonce, 2000, lastTx), IsNil)
	}
	rec, _ := f.committed(c)
	c.Assert(rec.Nonce, Equals, nonce)
	c.Assert(rec.Slot, Equals, int64(2000))
}

// A deviant slot that would cross the supermajority also splits off, so it can't set
// the committed slot (a far-future slot would block every later commit).
func (s *SolNonceAttestSuite) TestDeviantSlotCannotSetCommittedSlot(c *C) {
	f := newAttestFixture(c)
	nonce, lastTx := solNonceValue(c), GetRandomTxHash()
	c.Assert(f.attest(f.nas[0], nonce, 2000, lastTx), IsNil)
	c.Assert(f.attest(f.nas[1], nonce, 2000, lastTx), IsNil)
	c.Assert(f.attest(f.nas[3], nonce, 999_999_999, lastTx), IsNil) // byzantine third vote
	_, found := f.committed(c)
	c.Assert(found, Equals, false)
	c.Assert(f.attest(f.nas[2], nonce, 2000, lastTx), IsNil)
	rec, _ := f.committed(c)
	c.Assert(rec.Slot, Equals, int64(2000))
}

// Re-attestation never bootstraps a vault that has no committed nonce at all.
func (s *SolNonceAttestSuite) TestRejectsVaultWithoutRecord(c *C) {
	ctx, mgr := setupManagerForTest(c)
	f := newSolNonceFixture(c, ctx, mgr)
	for _, na := range f.nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	h := NewSolNonceAttestHandler(mgr)
	_, err := h.Run(ctx, NewMsgSolNonceAttest(f.vault.PubKey, f.nonceAcct, solNonceValue(c), 1000, GetRandomTxHash(), f.nas[0].NodeAddress))
	c.Assert(err, NotNil)
	_, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
}

// A re-attestation that advances an active record re-issues the vault's pending SOL
// outbounds issued since that record at once, as an observed commit does.
func (s *SolNonceAttestSuite) TestReattestReissuesWaiters(c *C) {
	f := newAttestFixture(c)
	prev, _, err := f.mgr.Keeper().GetSolVaultNonce(f.ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	to, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	waiter := TxOutItem{
		Chain: common.SOLChain, ToAddress: to, VaultPubKey: f.vault.PubKey, VaultPubKeyEddsa: f.vault.PubKeyEddsa,
		Coin: common.NewCoin(common.SOLAsset, cosmos.NewUint(common.One)), InHash: GetRandomTxHash(),
		Memo: "OUT:" + GetRandomTxHash().String(),
	}
	// within the last signing period (older items have already been through LackSigning)
	issuedAt := f.ctx.BlockHeight() - 10
	c.Assert(issuedAt > prev.UpdatedHeight, Equals, true)
	block := NewTxOut(issuedAt)
	block.TxArray = []TxOutItem{waiter}
	c.Assert(f.mgr.Keeper().SetTxOut(f.ctx, block), IsNil)

	nonce, lastTx := solNonceValue(c), GetRandomTxHash()
	for _, na := range f.nas[:3] {
		c.Assert(f.attest(na, nonce, 1100, lastTx), IsNil)
	}
	_, found := f.committed(c)
	c.Assert(found, Equals, true)
	now, err := f.mgr.Keeper().GetTxOut(f.ctx, f.ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(now.TxArray, HasLen, 1)
	c.Assert(now.TxArray[0].InHash.Equals(waiter.InHash), Equals, true)
	old, err := f.mgr.Keeper().GetTxOut(f.ctx, issuedAt)
	c.Assert(err, IsNil)
	c.Assert(old.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
}

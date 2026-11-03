package thorchain

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type SlasherDurableNonceSuite struct{}

var _ = Suite(&SlasherDurableNonceSuite{})

// durableLackKeeper adds a committed durable nonce and outbound observations to the
// LackSigning test keeper.
type durableLackKeeper struct {
	*TestSlashingLackKeeper
	nonce               SolVaultNonce
	hasNonce            bool
	links               []common.TxID
	outVoters           map[string]ObservedTxVoter
	maxOutboundAttempts int64
	nonceErr            error
	txOutAt             map[int64]*TxOut // per-height overrides
	txOutReads          int
	coalesce            int64
}

func (k *durableLackKeeper) GetConfigInt64(ctx cosmos.Context, key constants.ConstantName) int64 {
	if key == constants.MaxOutboundAttempts && k.maxOutboundAttempts > 0 {
		return k.maxOutboundAttempts
	}
	if key == constants.RescheduleCoalesceBlocks && k.coalesce > 0 {
		return k.coalesce
	}
	return k.TestSlashingLackKeeper.GetConfigInt64(ctx, key)
}

func (k *durableLackKeeper) GetSolVaultNonce(_ cosmos.Context, pk common.PubKey) (SolVaultNonce, bool, error) {
	if k.nonceErr != nil {
		return SolVaultNonce{}, false, k.nonceErr
	}
	if k.hasNonce && k.nonce.VaultPubKey.Equals(pk) {
		return k.nonce, true, nil
	}
	return SolVaultNonce{}, false, nil
}

func (k *durableLackKeeper) GetTxOut(ctx cosmos.Context, h int64) (*TxOut, error) {
	k.txOutReads++
	if out, ok := k.txOutAt[h]; ok {
		return out, nil
	}
	return k.TestSlashingLackKeeper.GetTxOut(ctx, h)
}

func (k *durableLackKeeper) GetObservedLink(_ cosmos.Context, _ common.TxID) []common.TxID {
	return k.links
}

func (k *durableLackKeeper) GetObservedTxOutVoter(_ cosmos.Context, h common.TxID) (ObservedTxVoter, error) {
	return k.outVoters[h.String()], nil
}

type durableLackFixture struct {
	ctx      cosmos.Context
	keeper   *durableLackKeeper
	store    *TxOutStoreDummy
	assigned Vault
	other    Vault
	toi      TxOutItem
}

// issuedAt is the TxOut height the outbound was issued at (and is rescheduled from).
const issuedAt = int64(3)

func newDurableLackFixture(c *C) durableLackFixture {
	ctx, _ := setupKeeperForTest(c)
	solVault := func() Vault {
		v := GetRandomVault()
		v.Type = AsgardVault
		v.Status = ActiveVault
		v.PubKeyEddsa = types.GetRandomEd25519PubKey()
		v.Coins = common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(500000*common.One))}
		return v
	}
	assigned, other := solVault(), solVault()
	dest, err := types.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	inHash := GetRandomTxHash()
	toi := TxOutItem{
		Chain:            common.SOLChain,
		InHash:           inHash,
		VaultPubKey:      assigned.PubKey,
		VaultPubKeyEddsa: assigned.PubKeyEddsa,
		ToAddress:        dest,
		Coin:             common.NewCoin(common.SOLAsset, cosmos.NewUint(10*common.One)),
		MaxGas:           common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(15000))},
		Memo:             "REFUND:" + inHash.String(),
	}
	txOut := NewTxOut(issuedAt)
	txOut.TxArray = append(txOut.TxArray, toi)
	k := &durableLackKeeper{
		TestSlashingLackKeeper: &TestSlashingLackKeeper{
			txOut:    txOut,
			na:       GetRandomValidatorNode(NodeActive),
			vaults:   Vaults{assigned, other},
			voter:    ObservedTxVoter{Actions: []TxOutItem{toi}, FinalisedHeight: issuedAt},
			slashPts: make(map[string]int64),
		},
		outVoters: map[string]ObservedTxVoter{},
	}
	period := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.SigningTransactionPeriod)
	return durableLackFixture{
		ctx: ctx.WithBlockHeight(issuedAt + period), keeper: k, store: NewTxStoreDummy(),
		assigned: assigned, other: other, toi: toi,
	}
}

func (f durableLackFixture) commitNonce(updatedHeight int64) {
	f.keeper.hasNonce = true
	f.keeper.nonce = SolVaultNonce{VaultPubKey: f.assigned.PubKey, NonceAccount: "acct", Nonce: "nonce", Slot: 1, UpdatedHeight: updatedHeight}
}

func (f durableLackFixture) run(c *C) []TxOutItem {
	mgr := NewDummyMgr()
	// Route the manager's keeper to the fixture keeper so LackSigning reads the real
	// SigningTransactionPeriod (the bare dummy keeper returns -1, which would make the
	// examined batch height differ from the issue height this guard keys on).
	mgr.K = f.keeper
	mgr.txOutStore = f.store
	c.Assert(newSlasher(f.keeper, NewDummyEventMgr()).LackSigning(f.ctx, mgr), IsNil)
	items, err := f.store.GetOutboundItems(f.ctx)
	c.Assert(err, IsNil)
	return items
}

// Baseline: a vault with no committed durable nonce is rescheduled exactly as today
// (reassigned to another vault), so the guard is inert for legacy SOL vaults.
func (s *SlasherDurableNonceSuite) TestNoCommittedNonceUnchanged(c *C) {
	f := newDurableLackFixture(c)
	items := f.run(c)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(f.other.PubKey), Equals, true)
}

// While the vault's committed nonce has not advanced since the issue height, a durable
// copy may still land: re-issue on the SAME vault, never reassign.
func (s *SlasherDurableNonceSuite) TestKeepsVaultUntilNonceAdvances(c *C) {
	for _, updated := range []int64{1, issuedAt} { // before, and exactly at, the issue height
		f := newDurableLackFixture(c)
		f.commitNonce(updated)
		items := f.run(c)
		c.Assert(items, HasLen, 1, Commentf("updated_height %d", updated))
		c.Assert(items[0].VaultPubKey.Equals(f.assigned.PubKey), Equals, true, Commentf("updated_height %d", updated))
		c.Assert(f.keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, false) // old item marked re-issued
	}
}

// Once the vault's nonce was advanced and committed AFTER the issue height, every
// copy on the vault is dead, so the normal reassignment is allowed.
func (s *SlasherDurableNonceSuite) TestReassignsAfterNonceAdvanced(c *C) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 1)
	items := f.run(c)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(f.other.PubKey), Equals, true)
}

// A copy of the outbound landed from the vault and was finally observed, yet the
// outbound is not done (it did not match): it must not be re-issued at all, even
// though the nonce advanced — any re-issue could pay a second time.
func (s *SlasherDurableNonceSuite) TestHoldsLandedUnmatchedCopy(c *C) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 10)
	outHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	landed := common.NewObservedTx(common.Tx{
		ID: outHash, Chain: common.SOLChain, FromAddress: from, ToAddress: f.toi.ToAddress,
		Coins: common.Coins{f.toi.Coin}, Gas: f.toi.MaxGas, Memo: f.toi.Memo,
	}, 100, f.assigned.PubKey, 100)
	f.keeper.links = []common.TxID{outHash}
	f.keeper.outVoters[outHash.String()] = ObservedTxVoter{TxID: outHash, Tx: landed, Txs: []common.ObservedTx{landed}, FinalisedHeight: 50}

	items := f.run(c)
	c.Assert(items, HasLen, 0)                                          // not re-issued
	c.Assert(f.keeper.txOut.TxArray[0].OutHash.IsEmpty(), Equals, true) // left in place for resolution
}

// A finalised observation of a DIFFERENT outbound of the same inbound (another
// destination) is not a copy of this one and does not hold it.
func (s *SlasherDurableNonceSuite) TestSiblingOutputDoesNotHold(c *C) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 10)
	outHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	elsewhere, err := types.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	sibling := common.NewObservedTx(common.Tx{
		ID: outHash, Chain: common.SOLChain, FromAddress: from, ToAddress: elsewhere,
		Coins: common.Coins{f.toi.Coin}, Gas: f.toi.MaxGas, Memo: f.toi.Memo,
	}, 100, f.assigned.PubKey, 100)
	f.keeper.links = []common.TxID{outHash}
	f.keeper.outVoters[outHash.String()] = ObservedTxVoter{TxID: outHash, Tx: sibling, Txs: []common.ObservedTx{sibling}, FinalisedHeight: 50}

	items := f.run(c)
	c.Assert(items, HasLen, 1) // re-issued normally (nonce advanced -> reassign allowed)
	c.Assert(items[0].VaultPubKey.Equals(f.other.PubKey), Equals, true)
}

// An inactive (retiring) vault that is drained would normally be abandoned to an
// active vault; with a durable copy possibly still landing, it stays on the vault.
func (s *SlasherDurableNonceSuite) TestInactiveDrainedVaultKeptWhileCopyCanLand(c *C) {
	f := newDurableLackFixture(c)
	f.keeper.vaults[0].Status = InactiveVault
	f.keeper.vaults[0].Coins = common.Coins{} // drained
	f.commitNonce(issuedAt)
	items := f.run(c)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(f.assigned.PubKey), Equals, true)
}

// With MaxOutboundAttempts exhausted, an outbound would normally be sent to treasury
// recovery. While a durable copy may still land that would pay twice (treasury plus
// the landed copy), so it is re-issued on the same vault instead.
func (s *SlasherDurableNonceSuite) TestNoTreasuryRecoveryWhileCopyCanLand(c *C) {
	f := newDurableLackFixture(c)
	f.keeper.maxOutboundAttempts = 1
	f.commitNonce(issuedAt)
	items := f.run(c)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(f.assigned.PubKey), Equals, true)
	c.Assert(items[0].ToAddress.Equals(f.toi.ToAddress), Equals, true) // not redirected to treasury
}

// Only the voter's FINAL consensus tx counts as a landed copy. A single conflicting
// vote under an already-finalised hash that mimics the outstanding outbound must not
// hold (and so permanently drop) the unpaid item.
func (s *SlasherDurableNonceSuite) TestForgedVariantDoesNotHold(c *C) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 10)
	outHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	elsewhere, err := types.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	consensus := common.NewObservedTx(common.Tx{
		ID: outHash, Chain: common.SOLChain, FromAddress: from, ToAddress: elsewhere,
		Coins: common.Coins{f.toi.Coin}, Gas: f.toi.MaxGas, Memo: f.toi.Memo,
	}, 100, f.assigned.PubKey, 100)
	forged := consensus
	forged.Tx.ToAddress = f.toi.ToAddress // matches the outstanding item
	f.keeper.links = []common.TxID{outHash}
	f.keeper.outVoters[outHash.String()] = ObservedTxVoter{
		TxID: outHash, Tx: consensus, Txs: []common.ObservedTx{consensus, forged}, FinalisedHeight: 50,
	}

	items := f.run(c)
	c.Assert(items, HasLen, 1) // re-issued, not held
	c.Assert(items[0].VaultPubKey.Equals(f.other.PubKey), Equals, true)
}

// A failing committed-nonce read fails closed: the outbound stays on its vault.
func (s *SlasherDurableNonceSuite) TestNonceReadErrorKeepsVault(c *C) {
	f := newDurableLackFixture(c)
	f.keeper.nonceErr = errors.New("kaboom")
	items := f.run(c)
	c.Assert(items, HasLen, 1)
	c.Assert(items[0].VaultPubKey.Equals(f.assigned.PubKey), Equals, true)
}

// A finalised observation already attributed to a completed item of the same inbound
// (e.g. an equal split part to the same destination paid by this vault) is not a
// landed copy of the unpaid item and must not hold (drop) it.
func (s *SlasherDurableNonceSuite) TestCompletedSiblingDoesNotHold(c *C) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 10)
	outHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	paid := common.NewObservedTx(common.Tx{
		ID: outHash, Chain: common.SOLChain, FromAddress: from, ToAddress: f.toi.ToAddress,
		Coins: common.Coins{f.toi.Coin}, Gas: f.toi.MaxGas, Memo: f.toi.Memo,
	}, 100, f.assigned.PubKey, 100)
	f.keeper.links = []common.TxID{outHash}
	f.keeper.outVoters[outHash.String()] = ObservedTxVoter{TxID: outHash, Tx: paid, Txs: []common.ObservedTx{paid}, FinalisedHeight: 50}
	f.keeper.voter.OutTxs = common.Txs{paid.Tx}
	sibling := f.toi
	sibling.OutHash = outHash // the equal split part this payment was matched to
	f.keeper.txOut.TxArray = append(f.keeper.txOut.TxArray, sibling)

	items := f.run(c)
	c.Assert(items, HasLen, 1) // re-issued, not held
}

// A landed payment recorded in the inbound voter's OutTxs but matched to NO item (e.g.
// gas above MaxGas) is still a landed copy of the unpaid item: hold, never re-issue.
func (s *SlasherDurableNonceSuite) TestUnmatchedOutTxStillHolds(c *C) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 10)
	outHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	paid := common.NewObservedTx(common.Tx{
		ID: outHash, Chain: common.SOLChain, FromAddress: from, ToAddress: f.toi.ToAddress,
		Coins: common.Coins{f.toi.Coin}, Gas: f.toi.MaxGas, Memo: f.toi.Memo,
	}, 100, f.assigned.PubKey, 100)
	f.keeper.links = []common.TxID{outHash}
	f.keeper.outVoters[outHash.String()] = ObservedTxVoter{TxID: outHash, Tx: paid, Txs: []common.ObservedTx{paid}, FinalisedHeight: 50}
	f.keeper.voter.OutTxs = common.Txs{paid.Tx} // AddOutTx ran, but the queue match rejected it

	items := f.run(c)
	c.Assert(items, HasLen, 0) // held, not paid again
}

// A sibling of the same inbound matched to the payment in a DIFFERENT TxOut block
// (e.g. after a reschedule) still exempts that payment: the unpaid item is re-issued,
// not held forever.
func (s *SlasherDurableNonceSuite) TestCompletedSiblingInOtherBlockDoesNotHold(c *C) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 10)
	outHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	paid := common.NewObservedTx(common.Tx{
		ID: outHash, Chain: common.SOLChain, FromAddress: from, ToAddress: f.toi.ToAddress,
		Coins: common.Coins{f.toi.Coin}, Gas: f.toi.MaxGas, Memo: f.toi.Memo,
	}, 100, f.assigned.PubKey, 100)
	f.keeper.links = []common.TxID{outHash}
	f.keeper.outVoters[outHash.String()] = ObservedTxVoter{TxID: outHash, Tx: paid, Txs: []common.ObservedTx{paid}, FinalisedHeight: 50}
	sibling := f.toi
	sibling.OutHash = outHash
	later := NewTxOut(issuedAt + 5)
	later.TxArray = append(later.TxArray, sibling)
	f.keeper.txOutAt = map[int64]*TxOut{issuedAt + 5: later}

	items := f.run(c)
	c.Assert(items, HasLen, 1) // re-issued, not held
}

// Break-glass: SOLNonceClear-<vault> at or above the record's commit height voids the
// committed nonce, so the outbound is rescheduled as for a legacy vault (reassigned).
// A mimir height below the commit height leaves the record in force.
func (s *SlasherDurableNonceSuite) TestBreakGlassClearVoidsCommittedNonce(c *C) {
	key := fmt.Sprintf(constants.MimirTemplateSOLNonceClear, "")
	for _, tc := range []struct {
		clearAt   int64
		reassigns bool
	}{
		{clearAt: 1, reassigns: true},  // record committed at 1: voided
		{clearAt: 0, reassigns: false}, // unset: in force, kept on its vault
	} {
		f := newDurableLackFixture(c)
		f.commitNonce(1)
		if tc.clearAt > 0 {
			f.keeper.SetMimir(f.ctx, key+f.assigned.PubKey.String(), tc.clearAt)
		}
		items := f.run(c)
		c.Assert(items, HasLen, 1)
		c.Assert(items[0].VaultPubKey.Equals(f.other.PubKey), Equals, tc.reassigns, Commentf("clearAt %d", tc.clearAt))
	}
}

// landedPaymentFixture: a finalised, linked payment from the assigned vault that fulfils
// toi, with a sibling of the same inbound matched to it at siblingHeight.
func landedPaymentFixture(c *C, siblingHeight int64) (durableLackFixture, common.TxID) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt + 10)
	outHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	paid := common.NewObservedTx(common.Tx{
		ID: outHash, Chain: common.SOLChain, FromAddress: from, ToAddress: f.toi.ToAddress,
		Coins: common.Coins{f.toi.Coin}, Gas: f.toi.MaxGas, Memo: f.toi.Memo,
	}, 100, f.assigned.PubKey, 100)
	f.keeper.links = []common.TxID{outHash}
	f.keeper.outVoters[outHash.String()] = ObservedTxVoter{TxID: outHash, Tx: paid, Txs: []common.ObservedTx{paid}, FinalisedHeight: 50}
	sibling := f.toi
	sibling.OutHash = outHash
	block := NewTxOut(siblingHeight)
	block.TxArray = append(block.TxArray, sibling)
	f.keeper.txOutAt = map[int64]*TxOut{siblingHeight: block}
	return f, outHash
}

// A sibling matched in a rescheduled block at the next coalesce height (ahead of the
// current block) still exempts the payment.
func (s *SlasherDurableNonceSuite) TestCompletedSiblingAtCoalescedHeightDoesNotHold(c *C) {
	f, _ := landedPaymentFixture(c, 0)
	f.keeper.coalesce = 1000
	now := f.ctx.BlockHeight()
	future := now + f.keeper.coalesce - now%f.keeper.coalesce
	c.Assert(future > now, Equals, true)
	block := f.keeper.txOutAt[0]
	block.Height = future
	f.keeper.txOutAt = map[int64]*TxOut{future: block}
	c.Assert(f.solDurableOnly(c), Equals, false) // not held
}

// The sibling window is clamped to solSiblingScanMaxBlocks: a match at the oldest
// scanned height counts, one block older does not (the guard then holds).
func (s *SlasherDurableNonceSuite) TestSiblingScanWindowBoundary(c *C) {
	for _, tc := range []struct {
		offset int64
		held   bool
	}{{0, false}, {1, true}} {
		f, _ := landedPaymentFixture(c, 0)
		f.ctx = f.ctx.WithBlockHeight(solSiblingScanMaxBlocks + 1000)
		f.keeper.voter.FinalisedHeight = 1 // inbound far older than the window
		// Only the override block exists; every other height reads as empty.
		f.keeper.txOutHeight = 1 // below the window
		oldest := f.ctx.BlockHeight() - solSiblingScanMaxBlocks
		block := f.keeper.txOutAt[0]
		block.Height = oldest - tc.offset
		f.keeper.txOutAt = map[int64]*TxOut{oldest - tc.offset: block}
		c.Assert(f.solDurableOnly(c), Equals, tc.held, Commentf("offset %d", tc.offset))
	}
}

// Many items of one inbound examined in one pass read its TxOut window once.
func (s *SlasherDurableNonceSuite) TestSiblingScanCachedPerPass(c *C) {
	f, _ := landedPaymentFixture(c, issuedAt+5)
	slasher := newSlasher(f.keeper, NewDummyEventMgr())
	mgr := NewDummyMgr()
	mgr.K = f.keeper
	cache := solSiblingCache{}
	f.keeper.txOutReads = 0
	for i := 0; i < 5; i++ {
		hold, _ := slasher.solDurableReissue(f.ctx, mgr, f.toi, f.assigned, issuedAt, issuedAt, cache)
		c.Assert(hold, Equals, false)
	}
	first := f.keeper.txOutReads
	c.Assert(first > 0, Equals, true)
	for i := 0; i < 5; i++ {
		_, _ = slasher.solDurableReissue(f.ctx, mgr, f.toi, f.assigned, issuedAt, issuedAt, cache)
	}
	c.Assert(f.keeper.txOutReads, Equals, first) // no re-scan
}

// solDurableOnly runs just the durable decision for the fixture item: true = hold.
func (f durableLackFixture) solDurableOnly(c *C) bool {
	mgr := NewDummyMgr()
	mgr.K = f.keeper
	hold, _ := newSlasher(f.keeper, NewDummyEventMgr()).solDurableReissue(f.ctx, mgr, f.toi, f.assigned, issuedAt, f.keeper.voter.FinalisedHeight, solSiblingCache{})
	return hold
}

// heightRecordingStore records the height each item is re-added at.
type heightRecordingStore struct {
	*TxOutStoreDummy
	heights map[string]int64
	dups    int // items re-added more than once in one pass
}

func (h *heightRecordingStore) UnSafeAddTxOutItem(ctx cosmos.Context, mgr Manager, toi TxOutItem, height int64) error {
	if _, dup := h.heights[toi.InHash.String()]; dup {
		h.dups++
	}
	h.heights[toi.InHash.String()] = height
	return h.TxOutStoreDummy.UnSafeAddTxOutItem(ctx, mgr, toi, height)
}

// runRecording runs LackSigning with a store that records re-add heights.
func (f durableLackFixture) runRecording(c *C) (map[string]int64, int64) {
	mgr := NewDummyMgr()
	mgr.K = f.keeper
	store := &heightRecordingStore{TxOutStoreDummy: f.store, heights: map[string]int64{}}
	mgr.txOutStore = store
	c.Assert(newSlasher(f.keeper, NewDummyEventMgr()).LackSigning(f.ctx, mgr), IsNil)
	c.Assert(store.dups, Equals, 0)
	resched := f.ctx.BlockHeight()
	if f.keeper.coalesce > 1 && resched%f.keeper.coalesce != 0 {
		resched += f.keeper.coalesce - resched%f.keeper.coalesce
	}
	return store.heights, resched
}

// laterSiblingFixture: toi at issuedAt plus a later pending sibling on the same vault,
// with the vault's nonce committed before issue (keepVault).
func laterSiblingFixture(c *C) (durableLackFixture, TxOutItem, *TxOut) {
	f := newDurableLackFixture(c)
	f.commitNonce(issuedAt - 1)
	f.keeper.coalesce = 7 // reschedule height differs from the current height
	later := f.toi
	later.InHash = GetRandomTxHash()
	dest, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	later.ToAddress = dest
	block := NewTxOut(issuedAt + 5)
	block.TxArray = append(block.TxArray, later)
	f.keeper.txOutAt = map[int64]*TxOut{issuedAt + 5: block}
	return f, later, block
}

// When LackSigning re-issues a durable outbound on its own vault (nonce not advanced),
// the vault's later pending SOL outbounds move to the SAME reschedule height, so the
// signer's per-height queue snapshots cannot hold each other forever.
func (s *SlasherDurableNonceSuite) TestKeepVaultRescheduleMovesLaterSiblings(c *C) {
	f, later, block := laterSiblingFixture(c)
	heights, resched := f.runRecording(c)
	c.Assert(resched != f.ctx.BlockHeight(), Equals, true)
	c.Assert(heights[f.toi.InHash.String()], Equals, resched)
	c.Assert(heights[later.InHash.String()], Equals, resched)
	c.Assert(block.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
}

// The same-vault co-location moves siblings even past MaxOutboundAttempts: the
// keepVault path has no recovery for them, so leaving one behind would deadlock.
func (s *SlasherDurableNonceSuite) TestKeepVaultRescheduleMovesExhaustedSiblings(c *C) {
	f, later, block := laterSiblingFixture(c)
	f.keeper.maxOutboundAttempts = 1 // one signing period since the inbound: exhausted
	heights, resched := f.runRecording(c)
	c.Assert(heights[f.toi.InHash.String()], Equals, resched)
	c.Assert(heights[later.InHash.String()], Equals, resched)
	c.Assert(block.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
}

// The retired-vault branch (inactive vault that still holds the funds) re-adds on
// the same vault too, and co-locates the later siblings the same way.
func (s *SlasherDurableNonceSuite) TestRetiredVaultRescheduleMovesLaterSiblings(c *C) {
	f, later, block := laterSiblingFixture(c)
	f.keeper.vaults[0].Status = InactiveVault // still funded (fixture coins)
	heights, resched := f.runRecording(c)
	c.Assert(heights[f.toi.InHash.String()], Equals, resched)
	c.Assert(heights[later.InHash.String()], Equals, resched)
	c.Assert(block.TxArray[0].OutHash.Equals(common.BlankTxID), Equals, true)
}

// On the co-location path exhausted siblings are no longer skipped by the budget, so
// the landed-copy hold is what stops a second payment: a later sibling whose copy
// already landed is left where it is, unmoved and unsent.
func (s *SlasherDurableNonceSuite) TestKeepVaultColocationSkipsLandedExhaustedSibling(c *C) {
	f, later, block := laterSiblingFixture(c)
	f.keeper.maxOutboundAttempts = 1
	paidHash := GetRandomTxHash()
	from, err := f.assigned.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	paid := common.NewObservedTx(common.Tx{
		ID: paidHash, Chain: common.SOLChain, FromAddress: from, ToAddress: later.ToAddress,
		Coins: common.Coins{later.Coin}, Gas: later.MaxGas, Memo: later.Memo,
	}, 100, f.assigned.PubKey, 100)
	f.keeper.links = []common.TxID{paidHash}
	f.keeper.outVoters[paidHash.String()] = ObservedTxVoter{TxID: paidHash, Tx: paid, Txs: []common.ObservedTx{paid}, FinalisedHeight: 50}

	heights, resched := f.runRecording(c)
	c.Assert(heights[f.toi.InHash.String()], Equals, resched)
	_, moved := heights[later.InHash.String()]
	c.Assert(moved, Equals, false)
	c.Assert(block.TxArray[0].OutHash.IsEmpty(), Equals, true)
}

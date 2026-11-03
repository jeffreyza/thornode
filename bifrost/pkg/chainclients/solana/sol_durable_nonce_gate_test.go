package solana

import (
	"errors"

	"github.com/rs/zerolog/log"
	soltypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

// heightMimirBridge distinguishes the live mimir (GetMimir) from the value at a
// consensus height (GetMimirAtHeight). The durable-nonce gate must use the latter so
// nodes with divergent live mimir but the same keysign height decide identically.
type heightMimirBridge struct {
	thorclient.ThorchainBridge
	live     int64
	byHeight map[int64]int64
}

func (b *heightMimirBridge) GetMimir(string) (int64, error) { return b.live, nil }
func (b *heightMimirBridge) GetMimirAtHeight(_ string, height int64) (int64, error) {
	return b.byHeight[height], nil
}

// fallbackMimirBridge implements only the live GetMimir (no GetMimirAtHeight), like a
// test double, to exercise the type-assert fallback.
type fallbackMimirBridge struct {
	thorclient.ThorchainBridge
	live int64
}

func (b *fallbackMimirBridge) GetMimir(string) (int64, error) { return b.live, nil }

// TestDurableNonceGateUsesHeightNotLive: two nodes at the same keysign height H=100
// (gate value there = 1 = ON) but with DIFFERENT live mimir (node A sees 0, node B
// sees 1) must both resolve the gate to the height-anchored value, so they take the
// same path and sign identical bytes — never the divergent live value (N3/N10).
func (s *SolTestSuite) TestDurableNonceGateUsesHeightNotLive(c *C) {
	nodeA := &SOLClient{bridge: &heightMimirBridge{live: 0, byHeight: map[int64]int64{100: 1}}}
	nodeB := &SOLClient{bridge: &heightMimirBridge{live: 1, byHeight: map[int64]int64{100: 1}}}

	va, err := nodeA.durableNonceMimirAtHeight(100)
	c.Assert(err, IsNil)
	vb, err := nodeB.durableNonceMimirAtHeight(100)
	c.Assert(err, IsNil)

	c.Assert(va, Equals, int64(1)) // the height value, NOT node A's live 0
	c.Assert(vb, Equals, int64(1))
	c.Assert(va, Equals, vb) // identical gate => identical path => identical bytes

	// Different height with the gate OFF there is seen identically by both.
	nodeA2 := &SOLClient{bridge: &heightMimirBridge{live: 9, byHeight: map[int64]int64{200: 0}}}
	v, err := nodeA2.durableNonceMimirAtHeight(200)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(0)) // off at H=200 despite live=9
}

// TestDurableNonceGateFallsBackToLive: a bridge without GetMimirAtHeight (a test
// double) falls back to the live read.
func (s *SolTestSuite) TestDurableNonceGateFallsBackToLive(c *C) {
	fb := &SOLClient{bridge: &fallbackMimirBridge{live: 7}}
	v, err := fb.durableNonceMimirAtHeight(100)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(7))
}

// pendingBridge stubs the not-yet-observed outbound queue for the N2 serialization gate.
type pendingBridge struct {
	thorclient.ThorchainBridge
	items  []thorclient.PendingOutbound
	err    error
	readAt []int64
}

func (b *pendingBridge) GetPendingOutbounds(h int64) ([]thorclient.PendingOutbound, error) {
	b.readAt = append(b.readAt, h)
	return b.items, b.err
}

// TestDurableSerializeLowestOnly: only the lowest-height not-yet-observed SOL outbound
// for a vault may sign durable (exactly one in flight); higher-height siblings wait;
// the wait unblocks once the lower one drops from the pending queue (bounded, not a
// permanent wedge); same-height siblings follow the signer's (Height, Index) order;
// other vaults/chains don't block; and the gate fails open when unavailable.
func (s *SolTestSuite) TestDurableSerializeLowestOnly(c *C) {
	sol := common.SOLChain.String()
	mk := func(h int64, hash string) stypes.TxOutItem {
		return stypes.TxOutItem{Chain: common.SOLChain, VaultPubKey: "vaultA", InHash: common.TxID(hash), Height: h}
	}
	burst := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h100", Height: 100},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h101", Height: 101},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h102", Height: 102},
	}
	cl := &SOLClient{bridge: &pendingBridge{items: burst}, logger: log.Logger}
	c.Assert(cl.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)  // lowest — the one in flight
	c.Assert(cl.durableSerializeAllows(mk(101, "h101"), 101), Equals, false) // waits
	c.Assert(cl.durableSerializeAllows(mk(102, "h102"), 102), Equals, false) // waits

	// Lowest observed (drops from the pending queue) -> next becomes eligible (bounded).
	cl2 := &SOLClient{bridge: &pendingBridge{items: burst[1:]}, logger: log.Logger}
	c.Assert(cl2.durableSerializeAllows(mk(101, "h101"), 101), Equals, true)

	// Stuck lowest: as long as @100 is present, @101 keeps waiting (not a crash/panic —
	// bounded by the B1 reschedule guard in production).
	c.Assert(cl.durableSerializeAllows(mk(101, "h101"), 101), Equals, false)

	// Same-height siblings follow the SIGNER's (Height, Index) order — their position
	// within the height in the queue — not InHash. Here Index order (zzz first)
	// disagrees with InHash order (aaa first). The head-of-line item zzz, which
	// pipeline.SpawnSignings keeps re-picking under the vault/chain lock, must be
	// allowed and aaa waits for it. Ordering by InHash would defer zzz behind aaa,
	// which the signer never reaches, and the vault would deadlock.
	sameHeight := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", InHash: "zzz", Height: 100},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "aaa", Height: 100},
	}
	cl3 := &SOLClient{bridge: &pendingBridge{items: sameHeight}, logger: log.Logger}
	c.Assert(cl3.durableSerializeAllows(mk(100, "zzz"), 100), Equals, true)  // Index 0: goes
	c.Assert(cl3.durableSerializeAllows(mk(100, "aaa"), 100), Equals, false) // Index 1: waits

	// Same InHash and height (e.g. two outputs of one inbound): the recipient tells
	// them apart, so they still serialize in queue order.
	mkTo := func(h int64, hash, to string) stypes.TxOutItem {
		item := mk(h, hash)
		item.ToAddress = common.Address(to)
		return item
	}
	twoOutputs := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", InHash: "same", ToAddress: "dest2", Height: 100},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "same", ToAddress: "dest1", Height: 100},
	}
	cl3b := &SOLClient{bridge: &pendingBridge{items: twoOutputs}, logger: log.Logger}
	c.Assert(cl3b.durableSerializeAllows(mkTo(100, "same", "dest2"), 100), Equals, true)
	c.Assert(cl3b.durableSerializeAllows(mkTo(100, "same", "dest1"), 100), Equals, false)

	// An outbound that cannot be located in the queue does not defer on same-height
	// siblings (there is no consistent order to wait on), but lower heights still gate.
	unplaced := []thorclient.PendingOutbound{{Chain: sol, VaultPubKey: "vaultA", InHash: "other", Height: 100}}
	cl3c := &SOLClient{bridge: &pendingBridge{items: unplaced}, logger: log.Logger}
	c.Assert(cl3c.durableSerializeAllows(mk(100, "notqueued"), 100), Equals, true)
	withLower := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h99", Height: 99},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "other", Height: 100},
	}
	cl3d := &SOLClient{bridge: &pendingBridge{items: withLower}, logger: log.Logger}
	c.Assert(cl3d.durableSerializeAllows(mk(100, "notqueued"), 100), Equals, false)

	// A different vault's pending outbound does not block.
	otherVault := []thorclient.PendingOutbound{{Chain: sol, VaultPubKey: "vaultB", InHash: "hx", Height: 50}}
	cl4 := &SOLClient{bridge: &pendingBridge{items: otherVault}, logger: log.Logger}
	c.Assert(cl4.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)

	// A non-SOL pending outbound does not block.
	ethPending := []thorclient.PendingOutbound{{Chain: "ETH", VaultPubKey: "vaultA", InHash: "e50", Height: 50}}
	cl5 := &SOLClient{bridge: &pendingBridge{items: ethPending}, logger: log.Logger}
	c.Assert(cl5.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)

	// A query error retains (retry), never a guess.
	cl6 := &SOLClient{bridge: &pendingBridge{err: errors.New("boom")}, logger: log.Logger}
	c.Assert(cl6.durableSerializeAllows(mk(100, "h100"), 100), Equals, false)

	// The queue is read at the keysign height, so every signer uses the same snapshot.
	atHeight := &pendingBridge{items: otherVault}
	cl8 := &SOLClient{bridge: atHeight, logger: log.Logger}
	c.Assert(cl8.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)
	c.Assert(atHeight.readAt, DeepEquals, []int64{100})

	// Fail-open: a bridge without GetPendingOutbounds proceeds.
	cl7 := &SOLClient{bridge: &fallbackMimirBridge{}, logger: log.Logger}
	c.Assert(cl7.durableSerializeAllows(mk(100, "h100"), 100), Equals, true)
}

// TestDurableDestinationExcluded: an outbound TO the vault's own durable-nonce account
// must not take the durable path. That address is a public, deterministic function of
// the vault, and the durable message would list it twice (recipient + nonce account),
// which Solana rejects — the retained signed bytes would then fail forever and wedge
// the vault's later SOL outbounds. Self-sends are excluded too; any other recipient
// stays eligible. The check is a pure function of tx+vault, so all parties agree.
func (s *SolTestSuite) TestDurableDestinationExcluded(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	vaultPK, err := soltypes.PublicKeyFromString(vaultAddr.String())
	c.Assert(err, IsNil)
	cl := &SOLClient{}
	nonceAddr, err := cl.nonceAddressForVault(vaultPK)
	c.Assert(err, IsNil)
	other, err := types2.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	mk := func(to string) stypes.TxOutItem {
		return stypes.TxOutItem{Chain: common.SOLChain, VaultPubKeyEddsa: vault, ToAddress: common.Address(to)}
	}
	c.Assert(cl.durableDestinationExcluded(mk(nonceAddr.String())), Equals, true) // to the nonce account
	c.Assert(cl.durableDestinationExcluded(mk(vaultAddr.String())), Equals, true) // self-send
	c.Assert(cl.durableDestinationExcluded(mk(other.String())), Equals, false)    // normal recipient

	// Why: the durable message for such an outbound would carry the nonce key twice.
	tx := soltypes.NewNonceTransferTransaction(vaultPK, nonceAddr, 1, "", "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", nonceAddr, vaultPK)
	seen := 0
	for _, k := range tx.Message.Accounts {
		if k.String() == nonceAddr.String() {
			seen++
		}
	}
	c.Assert(seen, Equals, 2)
}

// pendingNonceBridge adds the vault's latest committed durable nonce to pendingBridge.
type pendingNonceBridge struct {
	pendingBridge
	latest thorclient.SolVaultNonce
}

func (b *pendingNonceBridge) GetSolVaultNonce(string, int64) (thorclient.SolVaultNonce, error) {
	return b.latest, nil
}

// A sibling issued below the vault's latest commit height can only be signed with a
// consumed nonce (SignTx drops it until LackSigning re-issues it), so it must not hold
// later siblings behind it. A sibling at or above the commit height is live and does.
func (s *SolTestSuite) TestDurableSerializeSkipsDeadSibling(c *C) {
	sol := common.SOLChain.String()
	mk := func(h int64, hash string) stypes.TxOutItem {
		return stypes.TxOutItem{Chain: common.SOLChain, VaultPubKey: "vaultA", InHash: common.TxID(hash), Height: h}
	}
	queue := []thorclient.PendingOutbound{
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h100", Height: 100},
		{Chain: sol, VaultPubKey: "vaultA", InHash: "h200", Height: 200},
	}
	committed := func(updated int64) thorclient.SolVaultNonce {
		return thorclient.SolVaultNonce{NonceAccount: "acct", Nonce: "n", Slot: 1, UpdatedHeight: updated}
	}
	gate := func(updated int64) bool {
		cl := &SOLClient{bridge: &pendingNonceBridge{pendingBridge: pendingBridge{items: queue}, latest: committed(updated)}, logger: log.Logger}
		return cl.durableSerializeAllows(mk(200, "h200"), 200)
	}
	c.Assert(gate(150), Equals, true)  // @100 is dead (commit at 150 > 100): @200 proceeds
	c.Assert(gate(100), Equals, false) // commit at @100's own height: @100 is live, @200 waits
	c.Assert(gate(0), Equals, false)   // no committed nonce: nothing is dead
}

// A lower-height sibling paying the vault's own address or nonce account signs on the
// recent-blockhash path and never consumes the nonce, so it does not hold a durable
// outbound; any other lower-height sibling still does.
func (s *SolTestSuite) TestDurableSerializeSkipsRecentBlockhashSibling(c *C) {
	eddsa := types2.GetRandomEd25519PubKey()
	vaultAddr, err := eddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	nonceAcct, err := common.SolNonceAccountAddress(vaultAddr.String())
	c.Assert(err, IsNil)
	sol := common.SOLChain.String()
	tx := stypes.TxOutItem{Chain: common.SOLChain, VaultPubKey: "vaultA", VaultPubKeyEddsa: eddsa, InHash: "h200", Height: 200}
	for _, tc := range []struct {
		to    string
		allow bool
	}{
		{nonceAcct, true},
		{vaultAddr.String(), true},
		{"someone-else", false},
	} {
		queue := []thorclient.PendingOutbound{{Chain: sol, VaultPubKey: "vaultA", ToAddress: tc.to, InHash: "h100", Height: 100}}
		cl := &SOLClient{bridge: &pendingBridge{items: queue}, logger: log.Logger}
		c.Assert(cl.durableSerializeAllows(tx, 200), Equals, tc.allow, Commentf("to %s", tc.to))
	}
}

package solana

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestKeepAliveDue(t *testing.T) {
	// 14400 thorchain blocks * 20 slots = 288_000 slots (~1 day at ~300ms)
	refreshBlocks := int64(14400)
	require.Equal(t, uint64(20), solSlotsPerThorchainBlock())
	thresholdSlots := uint64(refreshBlocks) * solSlotsPerThorchainBlock()

	// fresh signature: not due
	require.False(t, keepAliveDue(1_000_000, 1_000_000-10, refreshBlocks))

	// exactly at threshold: not due (strictly greater required)
	require.False(t, keepAliveDue(1_000_000+thresholdSlots, 1_000_000, refreshBlocks))

	// past threshold: due
	require.True(t, keepAliveDue(1_000_000+thresholdSlots+1, 1_000_000, refreshBlocks))

	// disabled or invalid threshold: never due
	require.False(t, keepAliveDue(10_000_000, 0, 0))
	require.False(t, keepAliveDue(10_000_000, 0, -1))

	// current slot behind newest signature (RPC lag): not due
	require.False(t, keepAliveDue(999_999, 1_000_000, refreshBlocks))
}

// TestKeepAliveThorBoundary covers the convergence mechanism: every member derives
// the same cadence boundary (and therefore the same keysign anchor HEIGHT) from the
// consensus THORChain block height, mirroring how scheduled outbounds share
// txOut.Height. The anchor is a valid THORChain height (≤ tip), not a Solana slot.
func TestKeepAliveThorBoundary(t *testing.T) {
	cad := int64(keepAliveThorBlockCadence)

	// Fresh node (multiple 0) jumps straight to the current boundary — it does not
	// replay every historical boundary from zero.
	fresh := &SOLClient{}
	require.True(t, fresh.ShouldKeepAlive(10*cad+5))
	require.Equal(t, int64(10), fresh.nextKeepAliveThorMultiple(10*cad+5))

	// Two members a few blocks apart but inside the SAME cadence window derive the
	// same next boundary → identical anchor height → same keysign session.
	a, b := &SOLClient{}, &SOLClient{}
	a.setKeepAliveThorMultiple(9)
	b.setKeepAliveThorMultiple(9)
	require.Equal(t, a.nextKeepAliveThorMultiple(10*cad+1), b.nextKeepAliveThorMultiple(10*cad+40))
	require.Equal(t, int64(10), a.nextKeepAliveThorMultiple(10*cad+1))

	// The keysign anchor is boundary×cadence, NOT the raw height: two members at
	// different tips in the same window must hand SignTx the SAME height, and it
	// must be ≤ their tip (a node only fires after crossing the boundary).
	anchorA := a.nextKeepAliveThorMultiple(10*cad+1) * cad
	anchorB := b.nextKeepAliveThorMultiple(10*cad+40) * cad
	require.Equal(t, anchorA, anchorB)
	require.Equal(t, int64(10)*cad, anchorA)
	require.LessOrEqual(t, anchorA, 10*cad+1)     // ≤ the firing node's tip → valid signing_height
	require.NotEqual(t, int64(10)*cad+1, anchorA) // ≠ the raw per-node height

	// Already acted on boundary 10: not due again until the height crosses boundary 11.
	c := &SOLClient{}
	c.setKeepAliveThorMultiple(10)
	require.False(t, c.ShouldKeepAlive(10*cad+cad-1))
	require.True(t, c.ShouldKeepAlive(11*cad+1))

	// Advances one boundary at a time once initialized (never skips ahead).
	require.Equal(t, int64(11), c.nextKeepAliveThorMultiple(20*cad))
}

func TestKeepAliveAmount(t *testing.T) {
	dust := common.SOLChain.DustThreshold()

	// deterministic: same anchor slot yields the same amount on every node
	require.Equal(t, keepAliveAmount(123_456_789).String(), keepAliveAmount(123_456_789).String())

	// distinct rounds (different anchor slots) yield distinct amounts, so the
	// signer-cache hash never collides with a previous keep-alive round
	require.NotEqual(t, keepAliveAmount(123_456_789).String(), keepAliveAmount(123_456_790).String())

	// always clears the scanner dust threshold (observed by thorchain) and is
	// bounded below 3x dust
	for _, slot := range []uint64{0, 1, 99_999, 100_000, 123_456_789, ^uint64(0)} {
		amt := keepAliveAmount(slot)
		require.True(t, amt.GTE(dust.MulUint64(2)), "amount %s below 2x dust for slot %d", amt, slot)
		require.True(t, amt.LT(dust.MulUint64(3)), "amount %s not below 3x dust for slot %d", amt, slot)
	}
}

// TestCreateTxRoutesSelfSend verifies that a keep-alive TxOutItem (ToAddress ==
// vault address) is built with the deduplicated self-transfer message rather
// than the two-account transfer message, which Solana would reject with
// AccountLoadedTwice.
func (s *SolTestSuite) TestCreateTxRoutesSelfSend(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewSOLClient(s.thorKeys,
		config.BifrostChainConfiguration{
			RPCHost: "http://" + s.server.Listener.Addr().String(),
			BlockScanner: config.BifrostBlockScannerConfiguration{
				StartBlockHeight:   1, // avoids querying thorchain for block height
				HTTPRequestTimeout: time.Second,
				MaxGasLimit:        15000,
			},
		}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(e, NotNil)

	vaultEddsa := common.PubKey("tthorpub1zcjduepq829qzzhllcktltnfdudyupw5qjtyyjzcmv9wx2jvpah4d6nxtl2s7vwca8")
	vaultAddr, err := vaultEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	txOutItem := stypes.TxOutItem{
		Chain:            common.SOLChain,
		ToAddress:        vaultAddr,
		VaultPubKey:      "tthorpub1addwnpepqfshsq2y6ejy2ysxmq4gj8n8mzuzyulk9wh4n946jv5w2vpwdn2yuyp6sp4",
		VaultPubKeyEddsa: vaultEddsa,
		Coins: common.Coins{
			common.NewCoin(common.SOLAsset, keepAliveAmount(12345)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.SOLAsset, cosmos.NewUint(15000)),
		},
		GasRate: 1,
		Memo:    "consolidate",
	}
	tx, err := e.CreateTx(txOutItem, txOutItem.GetMemo(), "asdfasdf")
	c.Assert(err, IsNil)

	// vault, system program, memo program — vault listed exactly once
	c.Assert(len(tx.Message.Accounts), Equals, 3)
	vaultKey, err := types.PublicKeyFromString(vaultAddr.String())
	c.Assert(err, IsNil)
	c.Assert(tx.Message.Accounts[0], Equals, vaultKey)
	c.Assert(tx.Message.Accounts[1], Equals, types.SystemProgramID)

	// transfer instruction sends from and to account index 0
	transfer := tx.Message.Instructions[len(tx.Message.Instructions)-1]
	c.Assert(transfer.Accounts, DeepEquals, []int{0, 0})

	// serializes cleanly for TSS signing
	_, err = tx.Serialize()
	c.Assert(err, IsNil)
}

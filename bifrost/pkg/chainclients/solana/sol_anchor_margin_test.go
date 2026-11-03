package solana

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/constants"
)

// marginMimirBridge serves SOLAnchorMarginSeconds per keysign height (every other
// key reads 0) and records the heights the margin was read at.
type marginMimirBridge struct {
	*mockSOLThorchainBridge
	byHeight map[int64]int64
	err      error
	reads    []int64
}

func (b *marginMimirBridge) GetMimirAtHeight(key string, height int64) (int64, error) {
	if key != constants.MimirKeySOLAnchorMarginSeconds {
		return 0, nil
	}
	b.reads = append(b.reads, height)
	if b.err != nil {
		return 0, b.err
	}
	return b.byHeight[height], nil
}

func newMarginClient(t *testing.T, sc solRPCScenario, byHeight map[int64]int64) (*SOLClient, *marginMimirBridge) {
	_, rpcClient := newCountingSOLRPC(t, sc)
	b := &marginMimirBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, byHeight: byHeight}
	return &SOLClient{bridge: b, rpcClient: rpcClient}, b
}

// marginScenario is one finalized SOL view shared by every client in a test: slots
// 950-952 carry t-15, and each later second is 3 more slots (t-12 at 959-961).
func marginScenario() solRPCScenario {
	target15 := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	return solRPCScenario{tip: 1000, floorSlot: 1, target: target15, blockTime: tiedConsensusBlockTimes(target15, 950)}
}

func TestClampAnchorMargin(t *testing.T) {
	for _, tc := range []struct {
		raw, want int64
	}{
		{-1, 15}, // deleted mimir reads -1: default, NOT the clamp floor
		{0, 15},  // null / never set
		{-100, 15},
		{1, 12},
		{11, 12},
		{12, 12},
		{20, 20},
		{30, 30},
		{31, 30},
		{1_000_000, 30},
	} {
		require.Equal(t, tc.want, clampAnchorMargin(tc.raw), "raw %d", tc.raw)
	}
}

// TestAnchorMarginUnsetIsByteIdentical is the rollout guarantee: with the mimir unset
// (-1 or 0) the derived anchor is exactly the one the hardcoded 15s margin produced.
func TestAnchorMarginUnsetIsByteIdentical(t *testing.T) {
	legacy, _ := newConsensusCacheClient(t, marginScenario()) // mockSOLThorchainBridge: GetMimir 0
	wantHash, wantSlot, err := legacy.consensusBlockHash(keysignHeight)
	require.NoError(t, err)
	require.Equal(t, int64(952), wantSlot)
	require.Equal(t, mockBlockhashForSlot(952), wantHash)

	for _, raw := range []int64{-1, 0} {
		c, b := newMarginClient(t, marginScenario(), map[int64]int64{keysignHeight: raw})
		hash, slot, err := c.consensusBlockHash(keysignHeight)
		require.NoError(t, err)
		require.Equal(t, wantSlot, slot, "raw %d", raw)
		require.Equal(t, wantHash, hash, "raw %d", raw)
		require.Equal(t, []int64{keysignHeight}, b.reads, "margin read at the keysign height")
	}
}

// TestAnchorMarginReadErrorRetains: a failed read surfaces an error (SignTx retains the
// outbound) and is never memoized, so the next attempt reads the mimir again.
func TestAnchorMarginReadErrorRetains(t *testing.T) {
	c, b := newMarginClient(t, marginScenario(), map[int64]int64{keysignHeight: 0})
	b.err = errors.New("thornode unavailable")

	hash, slot, err := c.cachedConsensusBlockHash(keysignHeight)
	require.Error(t, err)
	require.NotErrorIs(t, err, errAnchorExpired, "a read error must retain, not drop")
	require.Empty(t, hash)
	require.Zero(t, slot)

	b.err = nil
	hash, slot, err = c.cachedConsensusBlockHash(keysignHeight)
	require.NoError(t, err)
	require.Equal(t, int64(952), slot)
	require.Equal(t, mockBlockhashForSlot(952), hash)
	require.Equal(t, []int64{keysignHeight, keysignHeight}, b.reads)
}

// TestAnchorMarginDiffersAcrossValues: two clients over the SAME finalized view but a
// different margin at the height derive different anchors, so the margin reaches the
// bytes (10 clamps to the 12s floor).
func TestAnchorMarginDiffersAcrossValues(t *testing.T) {
	c10, _ := newMarginClient(t, marginScenario(), map[int64]int64{keysignHeight: 10})
	c15, _ := newMarginClient(t, marginScenario(), map[int64]int64{keysignHeight: 15})

	h10, s10, err := c10.consensusBlockHash(keysignHeight)
	require.NoError(t, err)
	h15, s15, err := c15.consensusBlockHash(keysignHeight)
	require.NoError(t, err)

	require.Equal(t, int64(961), s10, "10 clamps to 12: target t-12")
	require.Equal(t, int64(952), s15)
	require.NotEqual(t, h10, h15)

	c30, _ := newMarginClient(t, marginScenario(), map[int64]int64{keysignHeight: 999})
	_, s30, err := c30.consensusBlockHash(keysignHeight)
	require.NoError(t, err)
	require.Equal(t, int64(907), s30, "999 clamps to 30: target t-30")
}

// TestAnchorMarginFlipKeepsOldHeight: a governance flip between heights changes only
// the new height; a retry of the old height keeps the old height's margin, whether it
// is served from the memo or re-derived.
func TestAnchorMarginFlipKeepsOldHeight(t *testing.T) {
	const h1, h2 = keysignHeight, keysignHeight + 1
	byHeight := map[int64]int64{h1: -1, h2: 12}

	c, b := newMarginClient(t, marginScenario(), byHeight)
	_, s1, err := c.cachedConsensusBlockHash(h1)
	require.NoError(t, err)
	_, s2, err := c.cachedConsensusBlockHash(h2)
	require.NoError(t, err)
	require.Equal(t, int64(952), s1)
	require.Equal(t, int64(961), s2)
	require.Equal(t, []int64{h1, h2}, b.reads, "each height reads its own margin")

	// A fresh node retrying h1 after the flip derives from h1's margin, not the new one.
	fresh, _ := newMarginClient(t, marginScenario(), byHeight)
	_, s1b, err := fresh.consensusBlockHash(h1)
	require.NoError(t, err)
	require.Equal(t, s1, s1b)
}

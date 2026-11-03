package monero

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReorgStoreLatestHeightUsesNumericOrdering(t *testing.T) {
	store := newScannerReceiptStore(t)
	check := func(want int64) {
		t.Helper()
		height, err := store.LatestHeight()
		require.NoError(t, err)
		require.Equal(t, want, height)
	}
	meta := func(height int64) *reorgBlockMeta {
		return &reorgBlockMeta{Height: height, BlockHash: testBlockHash(height), PreviousHash: testBlockHash(height - 1)}
	}
	check(-1)
	require.NoError(t, store.SetBatch([]*reorgBlockMeta{meta(9), meta(100), meta(10), meta(99), meta(1)}))
	check(100)

	// Numeric ordering must survive reconstruction from the old, unpadded
	// keys. Replaying earlier heights must not lower the reconciliation point.
	store = NewReorgStore(store.db)
	check(100)
	require.NoError(t, store.SetBatch([]*reorgBlockMeta{meta(8), meta(9)}))
	check(100)
	require.NoError(t, store.Delete(100))
	check(99)
	_, err := store.PruneBefore(100)
	require.NoError(t, err)
	check(-1)
}

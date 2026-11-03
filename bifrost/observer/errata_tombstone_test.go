package observer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// The quorum-errata commit handler fires for every chain THORChain commits,
// whether or not this node ever delivered that errata. Creating a tombstone for
// each one writes a LevelDB record per BTC/ETH/DOGE errata on every node, which
// the retry ticker then re-reads on every tick until the 30-day prune collects
// it. The tombstone exists only to stop a commit that races ahead of a LOCAL
// delivery from being resurrected, which only the durable XMR outbox can do.
func TestMarkErrataCommittedOnlyTombstonesOutboxChains(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	// A commit for a chain with no local record and no durable outbox: dropped.
	btc := types.ErrataTx{Chain: common.BTCChain, TxID: "btcdeadbeef"}
	require.NoError(t, storage.MarkErrataCommitted(btc))

	_, found, err := storage.GetPendingErrataTx(btc)
	require.NoError(t, err)
	assert.False(t, found, "a BTC commit this node never delivered must not create a tombstone")

	// XMR keeps the tombstone: the scanner may still be about to deliver this
	// errata, and resurrecting work THORChain already accepted would re-attest.
	xmr := types.ErrataTx{Chain: common.XMRChain, TxID: "xmrdeadbeef"}
	require.NoError(t, storage.MarkErrataCommitted(xmr))

	record, found, err := storage.GetPendingErrataTx(xmr)
	require.NoError(t, err)
	require.True(t, found, "an XMR commit-before-delivery must be tombstoned")
	assert.True(t, record.Committed)
	assert.False(t, record.DeliverySeen)

	// A chain without the outbox still updates a record that already exists
	// locally, so an errata this node DID deliver is completed normally.
	block := types.ErrataBlock{
		Height:     500,
		DeliveryID: "btc-500",
		Txs:        []types.ErrataTx{btc},
	}
	require.NoError(t, storage.AddErrataBlock(block))
	require.NoError(t, storage.MarkErrataCommitted(btc))

	record, found, err = storage.GetPendingErrataTx(btc)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, record.Committed, "a delivered BTC errata must still record its commit")
	assert.True(t, record.DeliverySeen)
}

// PruneCommittedOnlyErrata is the ONLY bound on commit-before-delivery
// tombstone growth in LevelDB, and its skip condition is subtle. Nothing tested
// it before.
func TestPruneCommittedOnlyErrata(t *testing.T) {
	storage, tempDir := setupTestDB(t)
	defer cleanupTestDB(t, storage, tempDir)

	old := types.ErrataTx{Chain: common.XMRChain, TxID: "oldtombstone"}
	recent := types.ErrataTx{Chain: common.XMRChain, TxID: "recenttombstone"}
	delivered := types.ErrataTx{Chain: common.XMRChain, TxID: "deliverederrata"}

	require.NoError(t, storage.MarkErrataCommitted(old))
	require.NoError(t, storage.MarkErrataCommitted(recent))

	// A record whose local delivery has begun is NOT a commit-only tombstone
	// and must survive pruning regardless of age.
	require.NoError(t, storage.AddErrataBlock(types.ErrataBlock{
		Height:     700,
		DeliveryID: "xmr-reorg-700",
		Txs:        []types.ErrataTx{delivered},
	}))
	require.NoError(t, storage.MarkErrataCommitted(delivered))

	// Nothing is old enough yet: a cutoff in the past must prune nothing.
	pruned, err := storage.PruneCommittedOnlyErrata(time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 0, pruned, "tombstones newer than the cutoff must be retained")

	// Age `old` past the horizon by rewriting its commit timestamp directly.
	record, found, err := storage.GetPendingErrataTx(old)
	require.NoError(t, err)
	require.True(t, found)
	record.CommittedAtUnixNano = time.Now().Add(-48 * time.Hour).UnixNano()
	agePersistedErrata(t, storage, record)

	pruned, err = storage.PruneCommittedOnlyErrata(time.Now().Add(-24 * time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, pruned, "exactly the aged commit-only tombstone must be pruned")

	_, found, err = storage.GetPendingErrataTx(old)
	require.NoError(t, err)
	assert.False(t, found, "aged commit-only tombstone must be gone")

	_, found, err = storage.GetPendingErrataTx(recent)
	require.NoError(t, err)
	assert.True(t, found, "tombstone newer than the cutoff must survive")

	_, found, err = storage.GetPendingErrataTx(delivered)
	require.NoError(t, err)
	assert.True(t, found, "a record with DeliverySeen must never be pruned as a tombstone")
}

// agePersistedErrata rewrites a record in place so a test can push its commit
// timestamp past the prune horizon without sleeping.
func agePersistedErrata(t *testing.T, s *ObserverStorage, record persistedErrata) {
	t.Helper()
	s.errataMu.Lock()
	defer s.errataMu.Unlock()
	require.NoError(t, s.writePersistedErrataLocked(record))
}

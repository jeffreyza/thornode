package observer

import (
	"context"
	"strconv"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestS10CanonicalHeightAdmissionAndCleanup(t *testing.T) {
	for _, mined := range []int64{100, 0, -1, 111} {
		t.Run(strconv.FormatInt(mined, 10), func(t *testing.T) {
			o, g, _ := setupErrataTestGossip(t)
			old := errataTestObservation("canonical-orphan", common.XMRChain, 110)
			old.XmrMinedHeight = mined
			require.NoError(t, g.AttestObservedTx(context.Background(), &old, true, false))
			msg := *g.batcher.observedTxBatch[0]
			remine := old
			remine.XmrMinedHeight = 101
			require.NoError(t, g.AttestObservedTx(context.Background(), &remine, true, false))
			require.NoError(t, o.filterErrataTx(types.ErrataBlock{
				Height: 100, Txs: []types.ErrataTx{{Chain: common.XMRChain, TxID: old.Tx.ID}},
			}))
			require.NotContains(t, g.observedTxs, observedTxKey(old, true, false))
			require.Contains(t, g.observedTxs, observedTxKey(remine, true, false))
			require.Len(t, g.batcher.observedTxBatch, 1)
			require.Equal(t, int64(101), g.batcher.observedTxBatch[0].ObsTx.XmrMinedHeight)
			// A validated item can reach enqueue after cleanup purged the queue.
			// The batcher must reject the original mined location too, even when
			// its finalization height differs from the tombstone height.
			g.batcher.AddObservedTx(msg)
			require.Len(t, g.batcher.observedTxBatch, 1)
			// All three S09 admission routes must consult the same durable
			// canonical-location barrier after queued state has been purged.
			require.NoError(t, g.AttestObservedTx(context.Background(), &old, true, false))
			g.handleObservedTxAttestation(context.Background(), msg)
			restarted, _, _, _, _, _ := setupTestGossip(t)
			restarted.SetObserverStorage(o.storage)
			require.NoError(t, restarted.processAttestationStateBatch(zerolog.Nop(), common.QuorumState{
				QuoTxs: []*common.QuorumTx{{ObsTx: old, Inbound: true, Attestations: []*common.Attestation{msg.Attestation}}},
			}))
			require.Empty(t, restarted.observedTxs)
			require.Len(t, g.observedTxs, 1)
			require.Len(t, g.batcher.observedTxBatch, 1)
			// Explicit Reverted retains the existing permanent TxID policy.
			errata := common.ErrataTx{Chain: common.XMRChain, Id: old.Tx.ID}
			require.NoError(t, o.storage.applyCommittedErrata(errata, nil))
			require.NoError(t, o.storage.recordErrataVoterResult(errata, true, nil))
			require.NoError(t, restarted.AttestObservedTx(context.Background(), &remine, true, false))
			require.Empty(t, restarted.observedTxs)
		})
	}
}

func TestS10CanonicalDeckReplayAfterRestart(t *testing.T) {
	storage, dir := setupTestDB(t)
	txID := common.TxID("canonical-restart")
	deck := &types.TxIn{Chain: common.XMRChain, TxArray: []*types.TxInItem{
		{Tx: txID.String(), BlockHeight: 100},
		{Tx: txID.String(), BlockHeight: 101},
		{Tx: txID.String(), BlockHeight: 0},
		{Tx: txID.String(), BlockHeight: -1},
	}}
	require.NoError(t, storage.AddOrUpdateTx(deck))
	future := &types.TxIn{
		Chain: common.XMRChain, MemPool: true, AllowFutureObservation: true,
		TxArray: []*types.TxInItem{{Tx: txID.String(), BlockHeight: 101}},
	}
	require.NoError(t, storage.AddOrUpdateTx(future))
	// Keep stale serialized deck records to exercise the durable replay
	// backstop, independently of the in-memory cleanup implementation.
	require.NoError(t, storage.applyErrata(types.ErrataBlock{
		Height: 100, Txs: []types.ErrataTx{{Chain: common.XMRChain, TxID: txID}},
	}, nil))
	require.NoError(t, storage.Close())
	reopened, err := NewObserverStorage(dir, config.LevelDBOptions{})
	require.NoError(t, err)
	defer cleanupTestDB(t, reopened, dir)
	got, err := reopened.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, got[0].TxArray, 1)
	require.Equal(t, int64(101), got[0].TxArray[0].BlockHeight)
}

func TestS10UnknownDeckLocationFailsClosed(t *testing.T) {
	o, storage := newErrataOrderingObserver(t)
	errata := types.ErrataBlock{Height: 100, Txs: []types.ErrataTx{{Chain: common.XMRChain, TxID: "unknown-location"}}}
	require.NoError(t, storage.applyErrata(errata, nil))
	for _, tc := range []struct {
		height          int64
		mempool, future bool
	}{{0, false, false}, {-1, false, false}, {101, true, false}, {101, false, true}} {
		tx := &types.TxIn{
			Chain: common.XMRChain, Filtered: true, MemPool: tc.mempool,
			AllowFutureObservation: tc.future, TxArray: []*types.TxInItem{{Tx: "unknown-location", BlockHeight: tc.height}},
		}
		o.lock.Lock()
		accepted, _, err := o.enforceErrataTombstones(tx)
		require.NoError(t, err)
		require.Empty(t, accepted)
		o.onDeck = map[txInKey]*types.TxIn{TxInKey(tx): tx}
		mutations, _, removed := o.collectErrataDeckMutations(
			map[string]struct{}{errataObservationKey(common.XMRChain, "unknown-location"): {}}, 100, false)
		o.lock.Unlock()
		require.Len(t, mutations, 1)
		require.Len(t, removed, 1)
	}
	// Non-XMR pre-mining height treatment is the existing reported-height rule.
	tombstone := errataTombstone{Chain: common.BTCChain, OrphanHeight: 100}
	btc := &types.TxIn{Chain: common.BTCChain, MemPool: true, AllowFutureObservation: true}
	require.False(t, tombstone.blocksTxInItem(btc, &types.TxInItem{BlockHeight: 101}))
	require.True(t, tombstone.blocksTxInItem(btc, &types.TxInItem{BlockHeight: 100}))
}

func TestS10ObserverPreservesOriginalMinedHeight(t *testing.T) {
	pk := randomXMRObservedPubKey(t)
	from, err := pk.GetAddress(common.XMRChain)
	require.NoError(t, err)
	txIn := &types.TxIn{Chain: common.XMRChain, ConfirmationRequired: 10, TxArray: []*types.TxInItem{{
		Tx: observerTestHash("s10-conversion"), BlockHeight: 100,
		Sender: from.String(), To: common.XMRSeededDummyAddress('8', []byte("s10-destination")),
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.OneUint())}, ObservedVaultPubKey: pk,
	}}}
	o := &Observer{logger: zerolog.Nop(), tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr()}
	for _, final := range []bool{false, true} {
		txs, invalid, convertErr := o.getThorchainTxIns(txIn, final, 110)
		require.NoError(t, convertErr)
		require.Empty(t, invalid)
		require.Len(t, txs, 1)
		height, heightErr := txs[0].CanonicalMinedHeight()
		require.NoError(t, heightErr)
		require.Equal(t, int64(100), height)
		require.Equal(t, final, txs[0].IsFinal())
	}
	txIn.MemPool, txIn.AllowFutureObservation = true, true
	txs, invalid, err := o.getThorchainTxIns(txIn, false, 100)
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Len(t, txs, 1)
	require.Zero(t, txs[0].XmrMinedHeight)
}

type s10CompletionChain struct {
	chainclients.ChainClient
	observed []types.TxInItem
	heights  []int64
}

func (c *s10CompletionChain) OnObservedTxIn(item types.TxInItem, height int64) {
	c.observed = append(c.observed, item)
	c.heights = append(c.heights, height)
}

func s10CompletionObservation(deck *types.TxIn, final bool) common.ObservedTx {
	item := deck.TxArray[0]
	height := item.BlockHeight
	finalHeight := TxInKey(deck).height
	if final {
		height = finalHeight
	}
	tx := common.NewObservedTx(common.Tx{
		ID: common.TxID(item.Tx), Chain: deck.Chain, Memo: item.Memo,
		FromAddress: common.Address(item.Sender), ToAddress: common.Address(item.To),
		Coins: item.Coins, Gas: item.Gas,
	}, height, item.ObservedVaultPubKey, finalHeight)
	tx.XmrTxKey = item.TxKey
	if deck.Chain.UsesHeightScopedErrata() && !deck.MemPool && !deck.AllowFutureObservation {
		tx.XmrMinedHeight = item.BlockHeight
	}
	return tx
}

func TestS10CommittedObservationRejectsOtherMinedLocation(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run("final="+strconv.FormatBool(final), func(t *testing.T) {
			o, storage := newErrataOrderingObserver(t)
			chain := &s10CompletionChain{}
			o.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: chain}
			old := errataOrderingTxIn(common.XMRChain, 100, 20,
				errataOrderingItem(observerTestHash("s10-delayed-commit"), 100, "same-payment"))
			require.NoError(t, o.processObservedTx(*old))
			committed := s10CompletionObservation(old, final)
			require.NoError(t, o.filterErrataTx(types.ErrataBlock{
				Height: 100, Txs: []types.ErrataTx{{Chain: common.XMRChain, TxID: committed.Tx.ID}},
			}))
			require.Empty(t, o.onDeck)
			restored, err := storage.GetOnDeckTxs()
			require.NoError(t, err)
			require.Empty(t, restored)

			// Changed confirmations make both locations share the finalization
			// key: old 100+20 and newly mined 110+10 both finalize at 120.
			remine := *old
			remine.TxArray = []*types.TxInItem{old.TxArray[0].Copy()}
			remine.TxArray[0].BlockHeight = 110
			remine.ConfirmationRequired = 10
			require.Equal(t, TxInKey(old), TxInKey(&remine))
			require.NoError(t, o.processObservedTx(remine))
			key := TxInKey(&remine)
			durableKey := []byte(storage.createTxKey(&remine, key.height))
			before, err := storage.db.Get(durableKey, nil)
			require.NoError(t, err)

			o.handleObservedTxCommitted(committed, false)
			after, readErr := storage.db.Get(durableKey, nil)
			// Report every boundary consequence when run against the old code.
			if readErr != nil || string(before) != string(after) {
				t.Errorf("delayed commit changed durable replacement deck: %v", readErr)
			}
			if len(chain.observed) != 0 {
				t.Errorf("delayed commit invoked OnObservedTxIn for replacement: %v", chain.heights)
			}
			require.Contains(t, o.onDeck, key)
			require.False(t, o.onDeck[key].TxArray[0].CommittedUnFinalised)
			restored, err = storage.GetOnDeckTxs()
			require.NoError(t, err)
			require.Len(t, restored, 1)
			require.Equal(t, int64(110), restored[0].TxArray[0].BlockHeight)
			require.False(t, restored[0].TxArray[0].CommittedUnFinalised)

			// The matching location must still finish through the real handler,
			// even though a final observation reports 120 rather than mined 110.
			o.handleObservedTxCommitted(s10CompletionObservation(&remine, final), false)
			require.Len(t, chain.observed, 1)
			require.Equal(t, []int64{110}, chain.heights)
			require.Equal(t, remine.TxArray[0].Tx, chain.observed[0].Tx)
			restored, err = storage.GetOnDeckTxs()
			require.NoError(t, err)
			if final {
				require.Empty(t, o.onDeck)
				require.Empty(t, restored)
			} else {
				require.True(t, o.onDeck[key].TxArray[0].CommittedUnFinalised)
				require.Len(t, restored, 1)
				require.True(t, restored[0].TxArray[0].CommittedUnFinalised)
			}
		})
	}
}

func TestS10CommittedObservationPreservesPreMiningAndNonXMR(t *testing.T) {
	for _, tc := range []struct {
		name            string
		chain           common.Chain
		mempool, future bool
		final           bool
	}{
		{"mempool", common.XMRChain, true, false, true},
		{"future", common.XMRChain, false, true, true},
		{"instant-outbound", common.XMRChain, true, true, true},
		{"btc-nonfinal", common.BTCChain, false, false, false},
		{"btc-final", common.BTCChain, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, storage := newErrataOrderingObserver(t)
			chain := &s10CompletionChain{}
			o.chains = map[common.Chain]chainclients.ChainClient{tc.chain: chain}
			deck := errataOrderingTxIn(tc.chain, 110, 10,
				errataOrderingItem(observerTestHash(tc.name), 110, "OUT:s10-completion"))
			deck.MemPool, deck.AllowFutureObservation = tc.mempool, tc.future
			if tc.mempool || tc.future {
				deck.ConfirmationRequired = 0 // Signing height, not a mined location.
			}
			require.NoError(t, o.processObservedTx(*deck))
			committed := s10CompletionObservation(deck, tc.final)
			require.Zero(t, committed.XmrMinedHeight)
			if !tc.final {
				committed.BlockHeight = 100 // Preserve existing non-XMR height handling.
			}
			o.handleObservedTxCommitted(committed, tc.future)
			require.Len(t, chain.observed, 1)
			require.Equal(t, []int64{110}, chain.heights)
			restored, err := storage.GetOnDeckTxs()
			require.NoError(t, err)
			if tc.final {
				require.Empty(t, o.onDeck)
				require.Empty(t, restored)
			} else {
				require.True(t, o.onDeck[TxInKey(deck)].TxArray[0].CommittedUnFinalised)
				require.Len(t, restored, 1)
				require.True(t, restored[0].TxArray[0].CommittedUnFinalised)
			}
		})
	}
}

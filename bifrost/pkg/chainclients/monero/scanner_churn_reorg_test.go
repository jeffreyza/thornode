package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestChurnReconcilesReorgBeforeReplay(t *testing.T) {
	for _, mode := range []string{
		"forward control",
		"pending observation",
		"retained receipt",
		"lowered registration",
		"lost reorg rewind response",
		"lost reorg sync response",
		"rejected errata",
		"reorg after churn rewind",
		"reorg after churn checkpoint",
		"forward move after lost rewind",
	} {
		t.Run(mode, func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{next: 1004, vault: vault, address: address}
			orphan := scannerReceiptItem("churn-reorg-orphan", 1002)
			orphan.Sender = testOutboundXMRDestination().String()
			orphan.To = address.String()
			orphan.Memo = "ADD:XMR.XMR"
			oldHash := func(height int64) string {
				if height >= 1002 && height <= 1003 {
					return testXMRHash("churn-old-block-" + strconv.FormatInt(height, 10))
				}
				return testBlockHash(height)
			}
			var forked, rejectErrata atomic.Bool
			forkAfterChurn := mode == "reorg after churn rewind" || mode == "reorg after churn checkpoint"
			forked.Store(!forkAfterChurn)
			rejectErrata.Store(mode == "rejected errata")
			signer.loseRewind = mode == "lost reorg rewind response" || mode == "reorg after churn rewind" || mode == "forward move after lost rewind"
			signer.loseSync = mode == "lost reorg sync response"
			var locationChecks atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer churn-test" {
					http.Error(w, "missing authentication", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/v1/monero/scanner/state":
					signer.mu.Lock()
					defer signer.mu.Unlock()
					last := signer.next - 1
					hash, parent := testBlockHash(int64(last)), testBlockHash(int64(last)-1)
					if signer.epoch == 0 {
						hash, parent = oldHash(int64(last)), oldHash(int64(last)-1)
					}
					_ = json.NewEncoder(w).Encode(sidecar.ScannerState{
						NextHeight: &signer.next, ResetEpoch: signer.epoch, LastHeight: &last,
						LastBlockHash: hash, LastParentHash: parent, StrictAfterReset: signer.epoch > 0,
					})
				case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/") && !forked.Load():
					height, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/"), 10, 64)
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
						Height: height, Hash: oldHash(height), PreviousHash: oldHash(height - 1),
					})
				case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/tx/"):
					locationChecks.Add(1)
					if strings.HasSuffix(r.URL.Path, "/exists") {
						_ = json.NewEncoder(w).Encode(sidecar.TxExistsResp{Exists: false})
						return
					}
					http.Error(w, "orphan is absent from chain and pool", http.StatusNotFound)
				default:
					signer.serveHTTP(w, r)
				}
			}))
			defer server.Close()

			dir := t.TempDir()
			db, err := leveldb.OpenFile(dir, nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, db.Close()) }()
			store := NewReorgStore(db)
			for height := int64(1000); height <= 1003; height++ {
				meta := &reorgBlockMeta{
					Height: height, BlockHash: oldHash(height), PreviousHash: oldHash(height - 1),
					ProvenanceComplete: true,
				}
				if height == 1002 {
					meta.CustomerTransactions = []string{orphan.Tx}
					if mode != "pending observation" && mode != "forward control" {
						require.NoError(t, store.PutObservationReceipt(meta, []*stypes.TxInItem{orphan}))
					}
				}
				require.NoError(t, store.Set(meta))
			}
			if mode == "lowered registration" {
				require.NoError(t, store.BeginCatchup(scannerCatchupMarker{
					StartHeight: 1004, TargetHeight: 1006, AnchorHeight: 1003, AnchorHash: oldHash(1003),
				}))
			}
			errataQueue := make(chan stypes.ErrataBlock)
			storedErrata := make(chan stypes.ErrataBlock, 8)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for block := range errataQueue {
					if rejectErrata.Swap(false) {
						block.OnRejected(errors.New("observer storage unavailable"))
						continue
					}
					storedErrata <- block
					block.OnStored()
				}
			}()
			defer func() { close(errataQueue); <-done }()
			newScanner := func() *Scanner {
				scanner, _ := newChurnScanner(t, server.URL, store, vault)
				scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
				scanner.SetErrataQueue(errataQueue)
				return scanner
			}
			scanner := newScanner()
			// Deliberately leave CurrentHeight at its startup value. Recovery must
			// use the persisted journal, not an in-memory pre-churn scan position.
			ack := func(txIn stypes.TxIn) {
				for _, item := range txIn.TxArray {
					require.NotEqual(t, orphan.Tx, item.Tx, "orphan receipt was redelivered")
					if item.OnObservedTxStored != nil {
						item.OnObservedTxStored()
					}
				}
			}

			interrupted := signer.loseRewind || signer.loseSync || rejectErrata.Load() || mode == "reorg after churn checkpoint"
			if interrupted {
				txIn, fetchErr := scanner.FetchTxs(1001, 1012)
				if mode == "reorg after churn checkpoint" {
					require.NoError(t, fetchErr)
					// Observer has not acknowledged the still-canonical receipt
					// when this process stops. Leave all callbacks uncalled.
				} else {
					require.Error(t, fetchErr)
					require.Empty(t, txIn.TxArray)
				}
				meta, metaErr := store.Get(1002)
				require.NoError(t, metaErr)
				require.Equal(t, oldHash(1002), meta.BlockHash, "failed recovery erased orphan evidence")
				require.Equal(t, []string{orphan.Tx}, meta.CustomerTransactions)
				require.Empty(t, storedErrata)
				require.NoError(t, db.Close())
				db, err = leveldb.OpenFile(dir, nil)
				require.NoError(t, err)
				store = NewReorgStore(db)
				scanner = newScanner()
				forked.Store(true)
			}
			start := int64(1001)
			switch mode {
			case "forward control":
				start = 1004
			case "forward move after lost rewind":
				start = 1005
			case "reorg after churn checkpoint":
				start = 1002
			}
			for height := start; height <= 1012; height++ {
				txIn, fetchErr := scanner.FetchTxs(height, 1012)
				require.NoError(t, fetchErr, "fetch height %d", height)
				// Cleanup must already be durable on the very first return,
				// before Bifrost can advance or pass any receipt to Observer.
				require.Len(t, storedErrata, 1)
				ack(txIn)
			}
			meta, err := store.Get(1002)
			require.NoError(t, err)
			require.Equal(t, testBlockHash(1002), meta.BlockHash)
			require.Empty(t, meta.CustomerTransactions)
			errata := <-storedErrata
			require.Equal(t, int64(1002), errata.Height)
			require.Equal(t, []stypes.ErrataTx{{TxID: common.TxID(orphan.Tx), Chain: common.XMRChain}}, errata.Txs)
			require.Positive(t, locationChecks.Load())
			claims, err := store.ListObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, claims)
		})
	}
}

func TestRegistrationCatchupReconcilesReorgInCheckpointedChunk(t *testing.T) {
	vault, address := testVaultPubKeyAndAddress(t)
	signer := &churnSigner{next: 1072, vault: vault, address: address}
	orphan := scannerReceiptItem("registration-catchup-orphan", 1050)
	orphan.Sender = testOutboundXMRDestination().String()
	orphan.To = address.String()
	orphan.Memo = "ADD:XMR.XMR"
	oldHash := func(height int64) string {
		if height >= 1050 {
			return testXMRHash("registration-old-block-" + strconv.FormatInt(height, 10))
		}
		return testBlockHash(height)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer churn-test" {
			http.Error(w, "missing authentication", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/monero/scanner/state":
			signer.mu.Lock()
			defer signer.mu.Unlock()
			last := signer.next - 1
			hash, parent := testBlockHash(int64(last)), testBlockHash(int64(last)-1)
			if signer.epoch == 0 {
				hash, parent = oldHash(int64(last)), oldHash(int64(last)-1)
			}
			_ = json.NewEncoder(w).Encode(sidecar.ScannerState{
				NextHeight: &signer.next, ResetEpoch: signer.epoch, LastHeight: &last,
				LastBlockHash: hash, LastParentHash: parent, StrictAfterReset: signer.epoch > 0,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/tx/"):
			if strings.HasSuffix(r.URL.Path, "/exists") {
				_ = json.NewEncoder(w).Encode(sidecar.TxExistsResp{Exists: false})
				return
			}
			http.Error(w, "orphan is absent from chain and pool", http.StatusNotFound)
		default:
			signer.serveHTTP(w, r)
		}
	}))
	defer server.Close()

	// Registration catch-up targets 1100 but has only checkpointed its first
	// 72-block chunk. A fork starting at 1050 must be reconciled before the
	// remaining chunk can deliver the earlier deposit's retained receipt.
	store := newScannerReceiptStore(t)
	require.NoError(t, store.BeginCatchup(scannerCatchupMarker{
		StartHeight: 1000, TargetHeight: 1100, AnchorHeight: 999, AnchorHash: oldHash(999),
	}))
	for height := int64(1000); height <= 1071; height++ {
		meta := &reorgBlockMeta{
			Height: height, BlockHash: oldHash(height), PreviousHash: oldHash(height - 1),
			ProvenanceComplete: true,
		}
		var items []*stypes.TxInItem
		if height == orphan.BlockHeight {
			meta.CustomerTransactions = []string{orphan.Tx}
			items = []*stypes.TxInItem{orphan}
		}
		require.NoError(t, store.CheckpointCatchup(scannerCatchupMarker{
			StartHeight: height + 1, TargetHeight: 1100, AnchorHeight: height, AnchorHash: oldHash(height),
		}, meta, items))
	}
	errataQueue := make(chan stypes.ErrataBlock)
	storedErrata := make(chan stypes.ErrataBlock, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for block := range errataQueue {
			storedErrata <- block
			block.OnStored()
		}
	}()
	defer func() { close(errataQueue); <-done }()
	scanner, _ := newChurnScanner(t, server.URL, store, vault)
	scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	scanner.SetErrataQueue(errataQueue)

	txIn, err := scanner.FetchTxs(1100, 1100)
	require.NoError(t, err)
	require.Empty(t, txIn.TxArray, "orphan from the checkpointed chunk reached Observer")
	require.Len(t, storedErrata, 1, "orphan cleanup must be durable before FetchTxs returns")
	errata := <-storedErrata
	require.Equal(t, int64(1050), errata.Height)
	require.Equal(t, []stypes.ErrataTx{{TxID: common.TxID(orphan.Tx), Chain: common.XMRChain}}, errata.Txs)
	meta, err := store.Get(1050)
	require.NoError(t, err)
	require.Equal(t, testBlockHash(1050), meta.BlockHash)
	require.Empty(t, meta.CustomerTransactions)
	claims, err := store.ListObservationReceipts()
	require.NoError(t, err)
	require.Empty(t, claims)
	marker, err := store.GetCatchupMarker()
	require.NoError(t, err)
	require.Nil(t, marker)
	wantHeights := make([]int64, 0, 51)
	for height := int64(1050); height <= 1100; height++ {
		wantHeights = append(wantHeights, height)
	}
	signer.mu.Lock()
	defer signer.mu.Unlock()
	require.Equal(t, []uint64{1050}, signer.rewinds)
	require.Equal(t, wantHeights, signer.heights)
	require.Equal(t, uint64(1101), signer.next)
}

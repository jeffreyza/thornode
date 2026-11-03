package monero

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// Model the pinned signer's durable MarkSpent record and MarkObserved boundary.
// Every request reloads disk state. Repeating the same height is permitted;
// changing an already observed height requires the signer's reorg rewind first.
// This fixture never serves a certificate or accepts a second MarkSpent import.
type payoutReplayFixture struct {
	mu            sync.Mutex
	sidecarDB     *leveldb.DB
	sidecarPath   string
	scannerDB     *leveldb.DB
	scannerPath   string
	server        *httptest.Server
	observed      common.ObservedTx
	location      sidecar.CanonicalTxLocationResp
	markCalls     int
	locationCalls int
	unavailable   bool
	afterMark     func()
}

func newPayoutReplayFixture(t *testing.T, prefix string) *payoutReplayFixture {
	t.Helper()
	source, _, txID, voter, refs := testXMRCatchupMigration(t, "durable-payout-"+prefix)
	voter.Tx.Tx.Memo = prefix + testXMRHash("payout-inbound")
	voter.Tx.Tx.ToAddress = testOutboundXMRDestination()
	observed := common.NewObservedTx(voter.Tx.Tx, 110, source, 110)
	observed.XmrTxKey, observed.SpentOutputRefs = voter.Tx.XmrTxKey, []common.OutputRef{*refs[0]}
	f := &payoutReplayFixture{
		observed: observed, sidecarPath: filepath.Join(t.TempDir(), "sidecar"),
		scannerPath: filepath.Join(t.TempDir(), "bifrost"),
		location: sidecar.CanonicalTxLocationResp{
			TxHash: txID, Height: 100, BlockHash: payoutReplayBlockHash(100), PreviousHash: payoutReplayBlockHash(99),
		},
	}
	var err error
	f.sidecarDB, err = leveldb.OpenFile(f.sidecarPath, nil)
	require.NoError(t, err)
	f.writeProof(t, sidecar.Transfer{
		TxID: txID, TxKey: observed.XmrTxKey, VaultPubKey: source.String(),
		FromAddress: observed.Tx.FromAddress.String(), Address: observed.Tx.ToAddress.String(),
		Amount: 2 * moneroAtomicToThorchain, Fee: moneroAtomicToThorchain, Note: observed.Tx.Memo,
		Origin: sidecar.OutgoingOriginVaultCertified, ObservationEligible: true, Locked: true,
		SpentOutputRefs: []sidecar.SpentRef{{
			TxHash: refs[0].TxHash, OutputIndex: refs[0].OutputIndex, KeyImage: refs[0].KeyImage, SpendTxHash: txID,
		}},
	})
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(func() {
		f.server.Close()
		_ = f.sidecarDB.Close()
		if f.scannerDB != nil {
			_ = f.scannerDB.Close()
		}
	})
	return f
}

func payoutReplayBlockHash(height int64) string {
	return testXMRHash(fmt.Sprintf("payout-block-%d", height))
}

func (f *payoutReplayFixture) writeProof(t *testing.T, proof sidecar.Transfer) {
	t.Helper()
	raw, err := json.Marshal(proof)
	require.NoError(t, err)
	require.NoError(t, f.sidecarDB.Put([]byte("outgoing"), raw, syncScannerWrite))
}

func (f *payoutReplayFixture) proof(t *testing.T) sidecar.Transfer {
	t.Helper()
	raw, err := f.sidecarDB.Get([]byte("outgoing"), nil)
	require.NoError(t, err)
	var proof sidecar.Transfer
	require.NoError(t, json.Unmarshal(raw, &proof))
	return proof
}

func (f *payoutReplayFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
		height, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/"), 10, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp(withBlockHeader(
			height, defaultCoinbaseValue, payoutReplayBlockHash(height), payoutReplayBlockHash(height-1),
		)))
	case r.URL.Path == "/v1/monero/chain/tx/"+f.observed.Tx.ID.String()+"/location":
		f.locationCalls++
		if f.unavailable {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(f.location)
	case r.URL.Path == "/v1/monero/outputs/mark-observed":
		f.markCalls++
		var req sidecar.MarkObservedReq
		var stored sidecar.Transfer
		raw, err := f.sidecarDB.Get([]byte("outgoing"), nil)
		if err != nil || json.Unmarshal(raw, &stored) != nil || json.NewDecoder(r.Body).Decode(&req) != nil ||
			req.TxHash != stored.TxID || req.VaultPubKey != stored.VaultPubKey ||
			req.Height != uint64(f.location.Height) || req.BlockHash != f.location.BlockHash ||
			(!stored.Locked && stored.Height != req.Height) {
			http.Error(w, "not a repeat-safe canonical mined proof", http.StatusConflict)
			return
		}
		stored.Height, stored.Locked = req.Height, false
		raw, err = json.Marshal(stored)
		if err == nil {
			err = f.sidecarDB.Put([]byte("outgoing"), raw, syncScannerWrite)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if f.afterMark != nil {
			f.afterMark()
		}
		_ = json.NewEncoder(w).Encode(stored)
	default:
		http.NotFound(w, r)
	}
}

func (f *payoutReplayFixture) openScanner(t *testing.T) *Scanner {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var err error
	f.scannerDB, err = leveldb.OpenFile(f.scannerPath, nil)
	require.NoError(t, err)
	signer, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{SignerExpectedNetwork: "mocknet"},
		f.server.URL,
	)
	require.NoError(t, err)
	// No new incoming transfers or vault registration are needed for replay.
	scanner := NewScanner(testLogger(), signer, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(NewReorgStore(f.scannerDB))
	store := newAuthenticatedInternalInboundReplayStore(f.scannerDB)
	require.NoError(t, store.ResetClaims())
	scanner.SetAuthenticatedInternalInboundReplayStore(store)
	scanner.SetManualVaultAddress(f.observed.ObservedPubKey, f.observed.Tx.FromAddress.String())
	scanner.setCurrentHeight(200)
	return scanner
}

func requirePayoutSource(t *testing.T, f *payoutReplayFixture, txIn stypes.TxIn, height int64) {
	t.Helper()
	require.Len(t, txIn.TxArray, 1, "a payout must never manufacture a destination deposit")
	item := txIn.TxArray[0]
	require.Equal(t, normalizeTxID(f.observed.Tx.ID.String()), normalizeTxID(item.Tx))
	require.Equal(t, f.observed.ObservedPubKey, item.ObservedVaultPubKey)
	require.Equal(t, f.observed.Tx.FromAddress.String(), item.Sender)
	require.Equal(t, f.observed.Tx.ToAddress.String(), item.To)
	require.Equal(t, f.observed.Tx.Memo, item.Memo)
	require.Equal(t, f.observed.Tx.Coins, item.Coins)
	require.Equal(t, f.observed.Tx.Gas, item.Gas)
	require.Equal(t, f.observed.XmrTxKey, item.TxKey)
	require.Equal(t, height, item.BlockHeight)
	require.Len(t, item.SpentOutputRefs, 1)
	require.Equal(t, f.observed.SpentOutputRefs[0], *item.SpentOutputRefs[0])
}

func TestPayoutReplayRestartsAfterMinedBeforeReceiptWithoutRetransmission(t *testing.T) {
	for _, prefix := range []string{"OUT:", "REFUND:"} {
		t.Run(prefix, func(t *testing.T) {
			f := newPayoutReplayFixture(t, prefix)
			scanner := f.openScanner(t)
			f.mu.Lock()
			f.afterMark = func() {
				// The sidecar's mined state is synced. Stop Bifrost before the
				// proof validation, second location RPC and receipt write.
				_ = f.scannerDB.Close()
			}
			f.mu.Unlock()
			// On the old implementation this call itself crosses MarkObserved.
			// With durable enrollment, the existing FetchTxs lifecycle does so.
			_, _ = scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
			f.mu.Lock()
			alreadyMarked := f.markCalls > 0
			f.mu.Unlock()
			if !alreadyMarked {
				_, err := scanner.FetchTxs(201, 201)
				require.Error(t, err, "the interrupted receipt write must not succeed")
			}
			f.mu.Lock()
			require.Equal(t, 1, f.markCalls)
			require.False(t, f.proof(t).Locked)
			require.Equal(t, uint64(100), f.proof(t).Height)
			f.afterMark = nil
			// Reopen both processes' disk state, discarding the old client,
			// scanner, receipt cache and claim owner.
			require.NoError(t, f.sidecarDB.Close())
			var err error
			f.sidecarDB, err = leveldb.OpenFile(f.sidecarPath, nil)
			require.NoError(t, err)
			f.mu.Unlock()
			scanner = f.openScanner(t)
			receipts, err := scanner.reorgStore.ListObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, receipts, "the crash occurred before the durable receipt")
			txIn, err := scanner.FetchTxs(202, 202)
			require.NoError(t, err)
			requirePayoutSource(t, f, txIn, 100)
			f.mu.Lock()
			require.Equal(t, 2, f.markCalls, "restart must repeat the durable sidecar operation")
			f.mu.Unlock()
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Zero(t, count, "the synced receipt now owns retry")
			receipts, err = scanner.reorgStore.ListObservationReceipts()
			require.NoError(t, err)
			require.Len(t, receipts, 1)
			txIn.TxArray[0].OnObservedTxRejected(errors.New("observer disk unavailable"))
			require.NoError(t, f.scannerDB.Close())
			scanner = f.openScanner(t)
			txIn, err = scanner.FetchTxs(203, 203)
			require.NoError(t, err)
			requirePayoutSource(t, f, txIn, 100)
			txIn.TxArray[0].OnObservedTxStored()
			receipts, err = scanner.reorgStore.ListObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, receipts)
		})
	}
}

func TestPayoutReplayDuplicateEnrollmentAndConcurrentReceiptHandoff(t *testing.T) {
	f := newPayoutReplayFixture(t, "OUT:")
	scanner := f.openScanner(t)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	count, err := scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	f.mu.Lock()
	require.Zero(t, f.markCalls, "enrollment has no mined side effect")
	f.mu.Unlock()
	// A persisted in-flight lease must be reclaimed by startup.
	claims, err := scanner.internalReplayStore.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, f.scannerDB.Close())
	scanner = f.openScanner(t)
	txIn, err := scanner.FetchTxs(201, 201)
	require.NoError(t, err)
	requirePayoutSource(t, f, txIn, 100)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			txIn.TxArray[0].OnObservedTxStored()
		}()
	}
	wg.Wait()
	receipts, err := scanner.reorgStore.ListObservationReceipts()
	require.NoError(t, err)
	require.Empty(t, receipts)
	// A late duplicate after completed handoff is repeat-safe and still source-only.
	queued, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err)
	require.True(t, queued)
	require.NoError(t, f.scannerDB.Close())
	scanner = f.openScanner(t)
	txIn, err = scanner.FetchTxs(202, 202)
	require.NoError(t, err)
	requirePayoutSource(t, f, txIn, 100)
	txIn.TxArray[0].OnObservedTxStored()
}

func TestPayoutReplayEnrollmentFailureAndSharedCapacity(t *testing.T) {
	f := newPayoutReplayFixture(t, "REFUND:")
	scanner := f.openScanner(t)
	// A failed durable enqueue cannot cross MarkObserved.
	require.NoError(t, f.scannerDB.Close())
	queued, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.Error(t, err)
	require.False(t, queued)
	f.mu.Lock()
	require.Zero(t, f.markCalls)
	require.True(t, f.proof(t).Locked)
	f.mu.Unlock()
	scanner = f.openScanner(t)
	for i := range maxAuthenticatedInternalInboundReplays {
		height := int64(300 + i)
		if i == 0 {
			// Free a slot immediately before the payout's place in the rotation.
			height = 99
		}
		require.NoError(t, scanner.internalReplayStore.Enqueue(standaloneReplayRecord(t, fmt.Sprintf("shared-cap-%d", i), height)))
	}
	queued, err = scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.ErrorContains(t, err, "outbox is full")
	require.False(t, queued)
	f.mu.Lock()
	require.Zero(t, f.markCalls)
	f.mu.Unlock()
	// Release capacity through the existing terminal, then retry enrollment.
	claims, err := scanner.internalReplayStore.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, scanner.internalReplayStore.Acknowledge(claims[0]))
	queued, err = scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err)
	require.True(t, queued)
	queued, err = scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err, "duplicates do not require another capacity slot")
	require.True(t, queued)
	count, err := scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, maxAuthenticatedInternalInboundReplays, count)
	txIn, err := scanner.FetchTxs(201, 201)
	require.NoError(t, err)
	requirePayoutSource(t, f, txIn, 100)
	count, err = scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, maxAuthenticatedInternalInboundReplays-1, count)
}

func TestPayoutReplayRetainsUnresolvedProofAcrossRestart(t *testing.T) {
	for _, scenario := range []string{
		"unmined", "ineligible", "committed", "wrong-source", "wrong-payment",
		"wrong-key", "wrong-fee", "wrong-refs", "wrong-spend", "different-recorded-height", "location-race",
	} {
		t.Run(scenario, func(t *testing.T) {
			f := newPayoutReplayFixture(t, "OUT:")
			scanner := f.openScanner(t)
			_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
			require.NoError(t, err)
			f.mu.Lock()
			original := f.proof(t)
			altered := original
			switch scenario {
			case "unmined":
				f.unavailable = true
			case "ineligible":
				altered.ObservationEligible = false
			case "committed":
				altered.Origin = sidecar.OutgoingOriginChainCommitted
			case "wrong-source":
				altered.FromAddress = f.observed.Tx.ToAddress.String()
			case "wrong-payment":
				altered.Amount += moneroAtomicToThorchain
			case "wrong-key":
				altered.TxKey = testXMRHash("wrong-key")
			case "wrong-fee":
				altered.Fee += moneroAtomicToThorchain
			case "wrong-refs":
				altered.SpentOutputRefs = nil
			case "wrong-spend":
				altered.SpentOutputRefs = append([]sidecar.SpentRef(nil), original.SpentOutputRefs...)
				altered.SpentOutputRefs[0].SpendTxHash = testXMRHash("other-spend")
			case "different-recorded-height":
				// The signer must reject relocation until reorg reconciliation
				// clears its previously recorded mined height.
				altered.Locked, altered.Height = false, 101
			case "location-race":
				f.afterMark = func() { f.unavailable = true }
			}
			f.writeProof(t, altered)
			f.mu.Unlock()
			txIn, err := scanner.FetchTxs(201, 201)
			require.NoError(t, err)
			require.Equal(t, int64(201), scanner.CurrentHeight())
			require.Empty(t, txIn.TxArray)
			receipts, err := scanner.reorgStore.ListObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, receipts)
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count, "failed evidence must never manufacture completion")
			require.NoError(t, f.scannerDB.Close())
			f.mu.Lock()
			f.unavailable, f.afterMark = false, nil
			f.writeProof(t, original)
			f.mu.Unlock()
			scanner = f.openScanner(t)
			txIn, err = scanner.FetchTxs(202, 202)
			require.NoError(t, err)
			requirePayoutSource(t, f, txIn, 100)
			txIn.TxArray[0].OnObservedTxStored()
		})
	}
}

func TestPayoutReplayCanonicalRelocationBeforeMarkObserved(t *testing.T) {
	f := newPayoutReplayFixture(t, "REFUND:")
	scanner := f.openScanner(t)
	_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err)
	f.mu.Lock()
	f.location.Height, f.location.BlockHash, f.location.PreviousHash = 101, payoutReplayBlockHash(101), payoutReplayBlockHash(100)
	f.mu.Unlock()
	require.NoError(t, f.scannerDB.Close())
	scanner = f.openScanner(t)
	txIn, err := scanner.FetchTxs(201, 201)
	require.NoError(t, err)
	requirePayoutSource(t, f, txIn, 101)
	meta, err := scanner.reorgStore.Get(101)
	require.NoError(t, err)
	require.True(t, reorgMetaHasVaultOrigin(meta, f.observed.Tx.ID.String()))
	txIn.TxArray[0].OnObservedTxStored()
}

func TestPayoutReplayKindCannotCreateMigrationPair(t *testing.T) {
	f := newPayoutReplayFixture(t, "OUT:")
	scanner := f.openScanner(t)
	_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err)
	claims, err := scanner.internalReplayStore.Claim(1)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	record := claims[0].Record
	require.Equal(t, authenticatedReplaySourcePayout, record.Kind)
	require.Empty(t, record.DestinationVaultPubKey)
	record.DestinationVaultPubKey = record.SourceVaultPubKey
	require.Error(t, validateAuthenticatedInternalInboundReplay(record))
	record.DestinationVaultPubKey = ""
	record.Kind = ""
	require.Error(t, validateAuthenticatedInternalInboundReplay(record), "a payout cannot be decoded as a migration")
	record.Kind = "unknown"
	require.Error(t, validateAuthenticatedInternalInboundReplay(record))
	// Existing serialized migrations omit kind and remain valid destination work.
	require.NoError(t, validateAuthenticatedInternalInboundReplay(standaloneReplayRecord(t, "legacy-migration", 100)))
}

func TestPayoutReplayPermanentRevertIsOnlyDiscardTerminal(t *testing.T) {
	f := newPayoutReplayFixture(t, "REFUND:")
	scanner := f.openScanner(t)
	_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err)
	require.NoError(t, scanner.reorgStore.QuarantineRevertedTxs(100, []stypes.ErrataTx{{
		TxID: f.observed.Tx.ID, Chain: common.XMRChain,
	}}))
	require.NoError(t, f.scannerDB.Close())
	scanner = f.openScanner(t)
	txIn, err := scanner.FetchTxs(201, 201)
	require.NoError(t, err)
	require.Empty(t, txIn.TxArray)
	count, err := scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Zero(t, count)
	_, err = scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.ErrorContains(t, err, "permanently reverted")
	f.mu.Lock()
	require.Zero(t, f.markCalls)
	f.mu.Unlock()
}

func TestPayoutReplayRelocationAfterMinedRetainsClaimUntilRewind(t *testing.T) {
	f := newPayoutReplayFixture(t, "OUT:")
	scanner := f.openScanner(t)
	_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err)
	f.mu.Lock()
	f.afterMark = func() {
		f.location.Height, f.location.BlockHash, f.location.PreviousHash = 101, payoutReplayBlockHash(101), payoutReplayBlockHash(100)
	}
	f.mu.Unlock()
	txIn, err := scanner.FetchTxs(201, 201)
	require.NoError(t, err)
	require.Equal(t, int64(201), scanner.CurrentHeight())
	require.Empty(t, txIn.TxArray)
	receipts, err := scanner.reorgStore.ListObservationReceipts()
	require.NoError(t, err)
	require.Empty(t, receipts)
	count, err := scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NoError(t, f.scannerDB.Close())
	scanner = f.openScanner(t)
	txIn, err = scanner.FetchTxs(202, 202)
	require.NoError(t, err)
	require.Empty(t, txIn.TxArray, "different recorded height must not be accepted as an idempotent retry")
	count, err = scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	// Model the existing sidecar rewind boundary, whose end-to-end scanner
	// behavior is covered by the committed payout/reorg tests. The pending
	// source claim must survive until that separate reconciliation finishes.
	f.mu.Lock()
	proof := f.proof(t)
	proof.Locked, proof.Height = true, 0
	f.writeProof(t, proof)
	f.afterMark = nil
	f.mu.Unlock()
	txIn, err = scanner.FetchTxs(203, 203)
	require.NoError(t, err)
	requirePayoutSource(t, f, txIn, 101)
	txIn.TxArray[0].OnObservedTxStored()
}

func TestPayoutReplayReceiptFailureDoesNotStopScanning(t *testing.T) {
	for _, scenario := range []string{"ineligible", "second-location-unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f := newPayoutReplayFixture(t, "OUT:")
			scanner := f.openScanner(t)
			_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
			require.NoError(t, err)
			f.mu.Lock()
			proof := f.proof(t)
			if scenario == "ineligible" {
				proof.ObservationEligible = false
				f.writeProof(t, proof)
			} else {
				// The initial canonical lookup succeeds on every attempt, but
				// the second lookup fails after MarkObserved has committed.
				f.afterMark = func() { f.unavailable = true }
			}
			f.mu.Unlock()
			for height := int64(201); height <= 205; height++ {
				f.mu.Lock()
				f.unavailable = false
				f.mu.Unlock()
				txIn, fetchErr := scanner.FetchTxs(height, height)
				require.NoError(t, fetchErr, "one pending payout must not stop block scanning")
				require.Equal(t, height, scanner.CurrentHeight())
				require.Empty(t, txIn.TxArray)
				receipts, receiptErr := scanner.reorgStore.ListObservationReceipts()
				require.NoError(t, receiptErr)
				require.Empty(t, receipts)
				count, countErr := scanner.internalReplayStore.Count()
				require.NoError(t, countErr)
				require.Equal(t, 1, count, "unresolved evidence must retain its durable retry")
			}
			f.mu.Lock()
			require.Equal(t, 5, f.markCalls, "each fetch must actually retry the payout")
			f.afterMark, f.unavailable = nil, false
			proof = f.proof(t)
			proof.ObservationEligible = true
			f.writeProof(t, proof)
			f.mu.Unlock()
			// Recovery of the proof service is sufficient; no fresh import is
			// needed to transfer the retained claim into the durable receipt.
			txIn, err := scanner.FetchTxs(206, 206)
			require.NoError(t, err)
			requirePayoutSource(t, f, txIn, 100)
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Zero(t, count)
			txIn.TxArray[0].OnObservedTxStored()
		})
	}
}

// Fairness cannot depend on wall-clock leases: a slow next block can arrive
// after the oldest lease has expired, and restart clears all outstanding leases.
func TestPayoutReplayFairClaimsAfterLeaseExpiryAndRestart(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(fmt.Sprintf("release-%t", release), func(t *testing.T) {
			f := newPayoutReplayFixture(t, "OUT:")
			scanner := f.openScanner(t)
			older := standaloneReplayRecord(t, "fair-oldest", 99)
			require.NoError(t, scanner.internalReplayStore.Enqueue(older))
			_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
			require.NoError(t, err)
			now := time.Unix(1_000_000, 0)
			scanner.internalReplayStore.now = func() time.Time { return now }
			first, err := scanner.internalReplayStore.Claim(1)
			require.NoError(t, err)
			require.Len(t, first, 1)
			require.Equal(t, older.TxID, first[0].Record.TxID)
			if release {
				require.NoError(t, scanner.internalReplayStore.Release(first[0]))
			}
			// Even retaining a failed claim's lease must not choose it again
			// ahead of unattempted work when the next fetch comes late.
			now = now.Add(authenticatedInternalInboundClaimLease + time.Second)
			second, err := scanner.internalReplayStore.Claim(1)
			require.NoError(t, err)
			require.Len(t, second, 1)
			require.Equal(t, normalizeTxID(f.observed.Tx.ID.String()), second[0].Record.TxID)
			require.NoError(t, scanner.internalReplayStore.Release(second[0]))
			require.NoError(t, f.scannerDB.Close())
			scanner = f.openScanner(t)
			third, err := scanner.internalReplayStore.Claim(1)
			require.NoError(t, err)
			require.Len(t, third, 1)
			require.Equal(t, older.TxID, third[0].Record.TxID, "restart must preserve fair retry position")
			require.NoError(t, scanner.internalReplayStore.Release(third[0]))
			require.NoError(t, f.scannerDB.Close())
			scanner = f.openScanner(t)
			txIn, err := scanner.FetchTxs(201, 201)
			require.NoError(t, err)
			requirePayoutSource(t, f, txIn, 100)
			txIn.TxArray[0].OnObservedTxStored()
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count, "the unresolved older job must remain durable")
		})
	}
}

func TestPayoutReplayFairConsumerPreservesMigrationCredit(t *testing.T) {
	for _, tc := range []struct {
		name             string
		olderPayout      bool
		youngerMigration bool
		missingSigner    bool
	}{
		{"unmined payout before migration", true, true, false},
		{"lost payout record before migration", true, true, true},
		{"unmined migration before payout", false, false, false},
		{"lost payout record before payout", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPayoutReplayFixture(t, "OUT:")
			older := standaloneReplayRecord(t, "fair-unresolved-"+tc.name, 99)
			older.BlockHash, older.PreviousHash = payoutReplayBlockHash(99), payoutReplayBlockHash(98)
			if tc.olderPayout {
				older.Kind = authenticatedReplaySourcePayout
				older.Memo = "OUT:" + testXMRHash("older-inbound")
				older.DestinationVaultPubKey = ""
			}
			if tc.missingSigner {
				// The older transaction remains canonical, but the sidecar has
				// no matching outgoing record and rejects MarkObserved for it.
				f.server.Close()
				f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/monero/chain/tx/"+older.TxID+"/location" {
						_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
							TxHash: older.TxID, Height: 99, BlockHash: older.BlockHash, PreviousHash: older.PreviousHash,
						})
						return
					}
					f.serveHTTP(w, r)
				}))
			}
			scanner := f.openScanner(t)
			require.NoError(t, scanner.internalReplayStore.Enqueue(older))
			var destination common.PubKey
			if tc.youngerMigration {
				migration := standaloneReplayRecord(t, "fair-younger-migration", 100)
				f.mu.Lock()
				proof := f.proof(t)
				migration.TxID, migration.TxKey = proof.TxID, proof.TxKey
				migration.SourceVaultPubKey, migration.SourceAddress = proof.VaultPubKey, proof.FromAddress
				migration.BlockHash, migration.PreviousHash = f.location.BlockHash, f.location.PreviousHash
				migration.SpentOutputRefs = authenticatedInternalSpentRefsFromSidecar(proof.SpentOutputRefs)
				proof.Address, proof.Note = migration.ToAddress, migration.Memo
				// Committed pairing evidence must remain destination-only.
				proof.Origin, proof.ObservationEligible = sidecar.OutgoingOriginChainCommitted, false
				f.writeProof(t, proof)
				f.mu.Unlock()
				var err error
				destination, err = common.NewPubKey(migration.DestinationVaultPubKey)
				require.NoError(t, err)
				require.NoError(t, scanner.internalReplayStore.Enqueue(migration))
			} else {
				_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
				require.NoError(t, err)
			}
			txIn, err := scanner.FetchTxs(201, 201)
			require.NoError(t, err)
			require.Empty(t, txIn.TxArray, "the first attempt belongs to the unresolved older job")
			require.NoError(t, f.scannerDB.Close())
			scanner = f.openScanner(t)
			txIn, err = scanner.FetchTxs(202, 202)
			require.NoError(t, err)
			require.Len(t, txIn.TxArray, 1)
			if tc.youngerMigration {
				require.Equal(t, destination, txIn.TxArray[0].ObservedVaultPubKey)
				require.Equal(t, "MIGRATE:100", txIn.TxArray[0].Memo)
				require.Empty(t, txIn.TxArray[0].TxKey)
				require.Empty(t, txIn.TxArray[0].SpentOutputRefs)
			} else {
				requirePayoutSource(t, f, txIn, 100)
			}
			txIn.TxArray[0].OnObservedTxStored()
			txIn, err = scanner.FetchTxs(203, 203)
			require.NoError(t, err)
			require.Empty(t, txIn.TxArray)
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count, "the failed older job must still be retried, never discarded")
		})
	}
}

func TestPayoutReplayFairCursorSurvivesDeletionAndSkipsLiveLease(t *testing.T) {
	f := newPayoutReplayFixture(t, "OUT:")
	scanner := f.openScanner(t)
	store := scanner.internalReplayStore
	now := time.Unix(1_000_000, 0)
	store.now = func() time.Time { return now }
	record := standaloneReplayRecord(t, "same-height-fairness", 100)
	for i := 1; i <= 3; i++ {
		record.TxID = fmt.Sprintf("%064x", i)
		require.NoError(t, store.Enqueue(record))
	}
	claim := func(want int) authenticatedInternalInboundClaim {
		t.Helper()
		claims, err := store.Claim(1)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, fmt.Sprintf("%064x", want), claims[0].Record.TxID)
		return claims[0]
	}
	require.NoError(t, store.Release(claim(1)))
	second := claim(2) // keep Observer's lease live
	require.NoError(t, store.Acknowledge(claim(3)))
	require.NoError(t, store.Release(claim(1)))
	require.NoError(t, store.Release(claim(1)), "the still-leased second job must remain exclusive")
	now = now.Add(authenticatedInternalInboundClaimLease + time.Second)
	renewed := claim(2)
	require.NotEqual(t, second.Token, renewed.Token)
	require.NoError(t, store.Acknowledge(second))
	count, err := store.Count()
	require.NoError(t, err)
	require.Equal(t, 2, count, "a stale callback cannot remove the renewed claim")
	require.NoError(t, store.Acknowledge(renewed))
	require.NoError(t, store.Release(claim(1)), "a deleted cursor target must not prevent wraparound")
}

func TestPayoutReplayMalformedCursorDoesNotStopScanning(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"invalid JSON", "{"},
		{"invalid height", "{}"},
		{"invalid txid", `{"block_height":100,"tx_id":"invalid"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPayoutReplayFixture(t, "OUT:")
			scanner := f.openScanner(t)
			older := standaloneReplayRecord(t, "malformed-cursor-oldest", 99)
			require.NoError(t, scanner.internalReplayStore.Enqueue(older))
			_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
			require.NoError(t, err)
			require.NoError(t, f.scannerDB.Put([]byte(authenticatedReplayCursorKey), []byte(tc.raw), syncInternalInboundReplayWrite))
			require.NoError(t, f.scannerDB.Close())
			scanner = f.openScanner(t)

			// Corrupt scheduling metadata must not prevent scanning or drop
			// authenticated work. The first claim restarts at the oldest job.
			txIn, err := scanner.FetchTxs(201, 201)
			require.NoError(t, err)
			require.Equal(t, int64(201), scanner.CurrentHeight())
			require.Empty(t, txIn.TxArray)
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Equal(t, 2, count)
			raw, err := f.scannerDB.Get([]byte(authenticatedReplayCursorKey), nil)
			require.NoError(t, err)
			var repaired authenticatedReplayCursor
			require.NoError(t, json.Unmarshal(raw, &repaired))
			require.Equal(t, authenticatedReplayCursor{BlockHeight: older.BlockHeight, TxID: older.TxID}, repaired)

			// The same synced claim batch repairs the cursor, so another
			// restart must select the younger payout rather than the stuck job.
			require.NoError(t, f.scannerDB.Close())
			scanner = f.openScanner(t)
			txIn, err = scanner.FetchTxs(202, 202)
			require.NoError(t, err)
			require.Equal(t, int64(202), scanner.CurrentHeight())
			requirePayoutSource(t, f, txIn, 100)
			txIn.TxArray[0].OnObservedTxStored()
			txIn, err = scanner.FetchTxs(203, 203)
			require.NoError(t, err)
			require.Equal(t, int64(203), scanner.CurrentHeight())
			require.Empty(t, txIn.TxArray)
			count, err = scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count, "the unresolved oldest job must remain queued")
		})
	}
}

func TestPayoutReplayCursorReadErrorRemainsAnError(t *testing.T) {
	f := newPayoutReplayFixture(t, "OUT:")
	scanner := f.openScanner(t)
	_, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
	require.NoError(t, err)
	records, err := scanner.internalReplayStore.listLocked()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.NoError(t, f.scannerDB.Close())
	_, err = scanner.internalReplayStore.claimStartLocked(records)
	require.ErrorIs(t, err, leveldb.ErrClosed)
	require.ErrorContains(t, err, "read xmr authenticated replay cursor")
}

// Model the pinned signer's height and parent continuity while switching
// from the old branch to its replacement after the first successful SyncAt.
type authenticatedReplayForkCursor struct {
	mu                     sync.Mutex
	forked                 bool
	next, last, epoch      uint64
	lastHash, lastParent   string
	rewinds, parentRejects int
	syncHeights            []int64
}

func newAuthenticatedReplayForkFixture(t *testing.T, prefix string) (*payoutReplayFixture, *Scanner, *authenticatedReplayForkCursor) {
	t.Helper()
	f := newPayoutReplayFixture(t, prefix)
	f.server.Close()
	old99 := payoutReplayBlockHash(99)
	h97, h98 := payoutReplayBlockHash(97), payoutReplayBlockHash(98)
	new99, new100, new101 := testXMRHash("replay-fork-new99"), testXMRHash("replay-fork-new100"), testXMRHash("replay-fork-new101")
	c := &authenticatedReplayForkCursor{next: 100, last: 99, lastHash: old99, lastParent: h98}
	blockAt := func(height int64) sidecar.ChainBlock {
		hash, parent := payoutReplayBlockHash(height), payoutReplayBlockHash(height-1)
		if c.forked {
			switch height {
			case 99:
				hash, parent = new99, h98
			case 100:
				hash, parent = new100, new99
			case 101:
				hash, parent = new101, new100
			}
		}
		return withBlockHeader(height, defaultCoinbaseValue, hash, parent)
	}
	f.mu.Lock()
	f.location = sidecar.CanonicalTxLocationResp{
		TxHash: f.observed.Tx.ID.String(), Height: 99, BlockHash: old99, PreviousHash: h98,
	}
	f.mu.Unlock()
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/v1/monero/scanner/") &&
			!strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/") {
			f.serveHTTP(w, r)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
			height, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/"), 10, 64)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp(blockAt(height)))
		case r.URL.Path == "/v1/monero/scanner/state":
			_ = json.NewEncoder(w).Encode(sidecar.ScannerState{
				ResetEpoch: c.epoch, NextHeight: &c.next, LastHeight: &c.last,
				LastBlockHash: c.lastHash, LastParentHash: c.lastParent, StrictAfterReset: c.epoch > 0,
			})
		case r.URL.Path == "/v1/monero/scanner/rewind":
			var req sidecar.ScannerRewindReq
			if json.NewDecoder(r.Body).Decode(&req) != nil ||
				req.ExpectedResetEpoch != c.epoch || req.ExpectedNextHeight != c.next ||
				req.TargetNextHeight != 99 || req.AnchorHeight != 98 || req.AnchorHash != h98 {
				http.Error(w, "invalid rewind", http.StatusConflict)
				return
			}
			previous := c.next
			c.rewinds++
			c.epoch++
			c.next, c.last, c.lastHash, c.lastParent = 99, 98, h98, h97
			_ = json.NewEncoder(w).Encode(sidecar.ScannerRewindResp{
				Rewound: true, PreviousNextHeight: previous, NextHeight: c.next, ResetEpoch: c.epoch,
			})
		case r.URL.Path == "/v1/monero/scanner/sync":
			var req sidecar.SyncReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			c.syncHeights = append(c.syncHeights, req.Height)
			if (req.ResetEpoch != nil && *req.ResetEpoch != c.epoch) ||
				(req.ExpectedNextHeight != nil && *req.ExpectedNextHeight != uint64(req.Height)) ||
				(c.epoch > 0 && (req.ResetEpoch == nil || req.ExpectedNextHeight == nil)) {
				http.Error(w, "scanner epoch mismatch", http.StatusConflict)
				return
			}
			exact := uint64(req.Height) == c.last && req.BlockHash == c.lastHash && req.ParentHash == c.lastParent
			if !exact {
				if uint64(req.Height) != c.next {
					http.Error(w, "scanner request is out of order", http.StatusConflict)
					return
				}
				if req.ParentHash != c.lastHash {
					c.parentRejects++
					http.Error(w, "scanner parent continuity mismatch", http.StatusConflict)
					return
				}
				block := blockAt(req.Height)
				if req.BlockHash != block.Hash || req.ParentHash != block.PreviousHash {
					http.Error(w, "scanner daemon hash mismatch", http.StatusConflict)
					return
				}
				c.last, c.next, c.lastHash, c.lastParent = uint64(req.Height), uint64(req.Height+1), req.BlockHash, req.ParentHash
			}
			if req.Height == 100 && !c.forked {
				// SyncAt successfully committed the old chain before the fork.
				c.forked = true
				f.mu.Lock()
				f.location = sidecar.CanonicalTxLocationResp{
					TxHash: f.observed.Tx.ID.String(), Height: 101, BlockHash: new101, PreviousHash: new100,
				}
				f.mu.Unlock()
			}
			_ = json.NewEncoder(w).Encode(sidecar.Transfers{})
		default:
			http.NotFound(w, r)
		}
	}))
	scanner := f.openScanner(t)
	scanner.bridge = &scannerMemoBridge{}
	scanner.asgardMu.Lock()
	// Keep the preloaded vault registry while exercising scan continuity.
	scanner.asgardUpdated = time.Now().Add(time.Hour)
	scanner.asgardMu.Unlock()
	scanner.setCurrentHeight(99)
	require.NoError(t, scanner.reorgStore.Set(&reorgBlockMeta{Height: 98, BlockHash: h98, PreviousHash: h97, ProvenanceComplete: true}))
	require.NoError(t, scanner.reorgStore.Set(&reorgBlockMeta{Height: 99, BlockHash: old99, PreviousHash: h98, ProvenanceComplete: true}))
	return f, scanner, c
}

func TestAuthenticatedReplayDefersFutureLocationUntilReorgRepair(t *testing.T) {
	for _, prefix := range []string{"OUT:", "REFUND:", "MIGRATE:"} {
		t.Run(prefix, func(t *testing.T) {
			f, scanner, cursor := newAuthenticatedReplayForkFixture(t, prefix)
			var destination common.PubKey
			if prefix == "MIGRATE:" {
				migration := standaloneReplayRecord(t, "future-migration", 99)
				f.mu.Lock()
				proof := f.proof(t)
				migration.TxID, migration.TxKey = proof.TxID, proof.TxKey
				migration.SourceVaultPubKey, migration.SourceAddress = proof.VaultPubKey, proof.FromAddress
				migration.BlockHash, migration.PreviousHash = f.location.BlockHash, f.location.PreviousHash
				migration.SpentOutputRefs = authenticatedInternalSpentRefsFromSidecar(proof.SpentOutputRefs)
				proof.Address, proof.Note = migration.ToAddress, migration.Memo
				proof.Origin, proof.ObservationEligible = sidecar.OutgoingOriginChainCommitted, false
				f.writeProof(t, proof)
				f.mu.Unlock()
				var err error
				destination, err = common.NewPubKey(migration.DestinationVaultPubKey)
				require.NoError(t, err)
				scanner.SetManualVaultAddress(destination, migration.ToAddress)
				require.NoError(t, scanner.internalReplayStore.Enqueue(migration))
			} else {
				handled, err := scanner.EnqueueAuthenticatedOutboundReplay(f.observed)
				require.NoError(t, err)
				require.True(t, handled)
			}

			// The signer commits old block 100, then the fork relocates this
			// queued transaction to 101 before replay looks up its location.
			txIn, err := scanner.FetchTxs(100, 101)
			require.NoError(t, err)
			require.Equal(t, int64(100), scanner.CurrentHeight())
			require.Empty(t, txIn.TxArray)
			latest, err := scanner.reorgStore.LatestHeight()
			require.NoError(t, err)
			require.Equal(t, int64(100), latest, "replay must not advance the scanned journal boundary")
			meta, err := scanner.reorgStore.Get(100)
			require.NoError(t, err)
			require.Equal(t, payoutReplayBlockHash(100), meta.BlockHash)
			f.mu.Lock()
			markCalls := f.markCalls
			f.mu.Unlock()
			require.Zero(t, markCalls, "future replay must wait for ordinary scan reconciliation")
			records, err := scanner.internalReplayStore.listLocked()
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.Equal(t, int64(99), records[0].BlockHeight, "future relocation must not be persisted yet")
			require.Empty(t, records[0].ClaimToken, "the durable job remains available for retry")
			receipts, err := scanner.reorgStore.ListObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, receipts)

			// The old journal now exposes the fork. FetchTxs must rewind the
			// signer to the common ancestor and scan the replacement branch
			// before replay may mark or emit this transaction at height 101.
			txIn, err = scanner.FetchTxs(101, 101)
			require.NoError(t, err)
			require.Equal(t, int64(101), scanner.CurrentHeight())
			if prefix == "MIGRATE:" {
				require.Len(t, txIn.TxArray, 1)
				item := txIn.TxArray[0]
				require.Equal(t, destination, item.ObservedVaultPubKey)
				require.Equal(t, int64(101), item.BlockHeight)
				require.Equal(t, records[0].SourceAddress, item.Sender)
				require.Equal(t, records[0].ToAddress, item.To)
				require.Equal(t, records[0].Memo, item.Memo)
				require.Empty(t, item.TxKey)
				require.Empty(t, item.SpentOutputRefs)
			} else {
				requirePayoutSource(t, f, txIn, 101)
			}
			txIn.TxArray[0].OnObservedTxStored()
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Zero(t, count)
			latest, err = scanner.reorgStore.LatestHeight()
			require.NoError(t, err)
			require.Equal(t, int64(101), latest)
			meta, err = scanner.reorgStore.Get(100)
			require.NoError(t, err)
			require.Equal(t, testXMRHash("replay-fork-new100"), meta.BlockHash)
			f.mu.Lock()
			markCalls = f.markCalls
			f.mu.Unlock()
			require.Equal(t, 1, markCalls)

			cursor.mu.Lock()
			require.Equal(t, 1, cursor.rewinds)
			require.Equal(t, []int64{100, 99, 100, 101}, cursor.syncHeights)
			require.Zero(t, cursor.parentRejects)
			require.Equal(t, uint64(101), cursor.last)
			require.Equal(t, testXMRHash("replay-fork-new101"), cursor.lastHash)
			cursor.mu.Unlock()
		})
	}
}
